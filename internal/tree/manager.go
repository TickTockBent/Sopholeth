// Package tree manages substrate-transient attachment lifecycle for
// Discovery Protocol v2.
//
// Substrate nodes (NODE_INBOUND=true) accept inbound WS attachments, register
// transients as children, and send goodbye-with-alternatives during shutdown.
//
// Transient nodes (NODE_INBOUND=false) dial a substrate's /v1/ws after HTTP
// bootstrap, send hello, parse welcome, and cache the substrate's topology as
// fallback candidates. When the parent connection drops, a three-layer reattach
// loop tries goodbye-supplied alternatives → cached topology → seed list, with
// exponential backoff between full cycles.
//
// Relay forwarding (substrate fans out child PUTs to enclave peers) and ACK
// reverse-routing land in subsequent phases.
package tree

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sopholeth/internal/gossip"
	"sopholeth/internal/logging"
	"sopholeth/internal/transport/ws"
)

const (
	// DefaultMaxChildren is the default cap on transient attachments per
	// substrate. 0 disables inbound attachments entirely.
	DefaultMaxChildren = 100

	// ReattachBackoffInitial is the wait before the first reattach retry.
	ReattachBackoffInitial = 5 * time.Second

	// ReattachBackoffMax caps the exponential backoff between full cycles.
	ReattachBackoffMax = 60 * time.Second

	// CachedAltConnectTimeout is the per-attempt timeout for cached
	// welcome-topology alternatives — short because entries may be stale.
	CachedAltConnectTimeout = 5 * time.Second

	// SeedConnectTimeout is the per-attempt timeout for fresh seed-list
	// alternatives (and goodbye-supplied alternatives).
	SeedConnectTimeout = 10 * time.Second

	// CachedLayerDeadline caps the total time spent in the cached-topology
	// layer before falling through to the seed list (#108).
	CachedLayerDeadline = 30 * time.Second

	// AttachTimeout is the default timeout for the hello/welcome handshake.
	AttachTimeout = 10 * time.Second

	// redirectCloseDelay gives a rejected hello time to receive the goodbye
	// frame before the substrate hangs up the socket.
	redirectCloseDelay = 500 * time.Millisecond
)

// Role identifies the resolved node role.
type Role string

const (
	RoleSubstrate Role = "substrate"
	RoleTransient Role = "transient"
)

// InboundCapability mirrors the NODE_INBOUND env var: "true" makes the node
// a substrate, "false" (default) makes it a transient.
type InboundCapability string

const (
	InboundTrue  InboundCapability = "true"
	InboundFalse InboundCapability = "false"
)

// Peerer is the subset of gossip.Protocol that Manager depends on.
// *gossip.Protocol satisfies it; tests use a fake implementation.
type Peerer interface {
	GetPeers() []*gossip.Node
}

// Dialer opens a new outbound WS attachment. Defaults to ws.ConnectToSubstrate.
// Injected to keep reattach tests fast (no need to spin up live servers for
// each cached/seed alternative).
type Dialer func(ctx context.Context, address string, port int, clusterSecret string, timeout time.Duration) (*ws.Connection, error)

// Options configures Manager construction.
type Options struct {
	Inbound       InboundCapability
	MaxChildren   int    // 0 disables inbound attachments
	ClusterSecret string // empty → open-cluster mode (no HMAC)
	Dialer        Dialer // nil → ws.ConnectToSubstrate
}

// Manager tracks the substrate/transient tree state for one node.
//
// All public methods are safe for concurrent use. Children are tracked by
// node ID; close-of-child cleans up automatically through an AddCloseHandler
// installed during HandleHello.
type Manager struct {
	local  *gossip.Node
	gossip Peerer
	opts   Options
	dialer Dialer

	role           Role
	inboundCapable bool

	mu               sync.Mutex
	parent           *ws.Connection
	children         map[string]*ws.Connection
	lastKnownAlts    []ws.AlternativeParent
	reattachInFlight bool
	onReattach       func(*ws.Connection)
	seedProvider     func() []string

	// parentDispatch is the application-level gossip handler installed on
	// every parent connection (initial Attach and every successful reattach)
	// before Attach returns. Setting this up-front closes the window where
	// the substrate could push a PUT between welcome arrival and the caller
	// wiring its own OnMessage.
	parentDispatch func(*gossip.Message)

	// ackRoutes tracks (messageId → child connection) for PUTs relayed
	// through this substrate. When an enclave peer ACKs a relayed PUT, the
	// substrate forwards the ACK back through ackRoutes[messageId] so the
	// originating transient observes quorum confirmation. Entries are
	// evicted by ackRouteTimers or explicit ClearAckRoute.
	ackRoutes      map[string]*ws.Connection
	ackRouteTimers map[string]*time.Timer

	stopOnce   sync.Once
	stopping   atomic.Bool
	stopCh     chan struct{}
	stopCtx    context.Context
	stopCancel context.CancelFunc
	wg         sync.WaitGroup
}

// NewManager constructs a Manager. The Inbound option resolves the role
// immediately; subsequent calls to HandleHello / Attach honor that role.
func NewManager(local *gossip.Node, peers Peerer, opts Options) *Manager {
	if opts.Dialer == nil {
		opts.Dialer = ws.ConnectToSubstrate
	}
	stopCtx, stopCancel := context.WithCancel(context.Background())
	m := &Manager{
		local:          local,
		gossip:         peers,
		opts:           opts,
		dialer:         opts.Dialer,
		children:       make(map[string]*ws.Connection),
		ackRoutes:      make(map[string]*ws.Connection),
		ackRouteTimers: make(map[string]*time.Timer),
		stopCh:         make(chan struct{}),
		stopCtx:        stopCtx,
		stopCancel:     stopCancel,
	}
	if opts.Inbound == InboundTrue {
		m.role = RoleSubstrate
		m.inboundCapable = true
	} else {
		m.role = RoleTransient
		m.inboundCapable = false
	}
	return m
}

// Role reports the node's resolved tree role.
func (m *Manager) Role() Role { return m.role }

// IsInboundCapable reports whether this node accepts inbound attachments.
func (m *Manager) IsInboundCapable() bool { return m.inboundCapable }

// Parent returns the active outbound substrate attachment, or nil.
func (m *Manager) Parent() *ws.Connection {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.parent
}

// ChildCount returns the number of attached transients.
func (m *Manager) ChildCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.children)
}

// HasChild reports whether nodeID is currently attached.
func (m *Manager) HasChild(nodeID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.children[nodeID]
	return ok
}

// Children returns a snapshot of the attached-children map. The returned map
// is a copy — callers may inspect freely without holding any lock.
func (m *Manager) Children() map[string]*ws.Connection {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]*ws.Connection, len(m.children))
	for k, v := range m.children {
		out[k] = v
	}
	return out
}

// SetSeedProvider wires the freshest reattach fallback. For private clusters
// snapshot NODE_PEERS; for public, close over the omega refresher's current
// signed root list. Nil disables the seed-list layer.
func (m *Manager) SetSeedProvider(fn func() []string) {
	m.mu.Lock()
	m.seedProvider = fn
	m.mu.Unlock()
}

// SetReattachCallback registers a hook fired after a successful reattach.
// The application uses this to rewire any per-connection state that isn't
// already handled by SetParentDispatch (e.g., metrics labels, logging
// scopes). Parent message dispatch itself is wired by SetParentDispatch.
func (m *Manager) SetReattachCallback(fn func(*ws.Connection)) {
	m.mu.Lock()
	m.onReattach = fn
	m.mu.Unlock()
}

// SetParentDispatch registers the gossip-message handler that Attach and
// every successful reattach install on the parent connection — wired
// inside attach() before the function returns, so the substrate's first
// post-welcome PUT can't be silently dropped by a not-yet-wired OnMessage.
// Pass nil to disable parent-side dispatch (transient still receives via
// HTTP gossip; only WS-tree fan-out is muted).
func (m *Manager) SetParentDispatch(fn func(*gossip.Message)) {
	m.mu.Lock()
	m.parentDispatch = fn
	m.mu.Unlock()
}

// HandleHello processes an incoming hello frame on a freshly-accepted child
// connection. On accept it sends a welcome with the current peer topology and
// registers a close handler that removes the child from the map. On reject
// (capacity, attachments disabled) it sends a goodbye-with-alternatives and
// closes the connection shortly afterward.
func (m *Manager) HandleHello(conn *ws.Connection, hello *ws.HelloPayload) bool {
	// Normalize empty enclave to "default" so the BroadcastToChildren
	// enclave filter cannot be bypassed by a hello that omits the field
	// (the filter skips children whose RemoteEnclave doesn't match the
	// local one; "" used to slip through as "any"). Matches the same
	// normalization the gossip layer applies to peer enclaves.
	if hello.Enclave == "" {
		hello.Enclave = "default"
	}
	m.mu.Lock()
	if m.opts.MaxChildren == 0 {
		m.mu.Unlock()
		logging.Info("Rejecting attachment from %s: attachments disabled (MaxChildren=0)", hello.NodeID)
		m.sendRedirect(conn, hello.Enclave, "at capacity")
		return false
	}
	if m.opts.MaxChildren > 0 && len(m.children) >= m.opts.MaxChildren {
		count := len(m.children)
		m.mu.Unlock()
		logging.Info("Rejecting attachment from %s: at capacity (%d/%d)", hello.NodeID, count, m.opts.MaxChildren)
		m.sendRedirect(conn, hello.Enclave, "at capacity")
		return false
	}
	m.children[hello.NodeID] = conn
	count := len(m.children)
	m.mu.Unlock()

	conn.SetRemote(hello.NodeID, hello.Enclave)

	peers := m.gossip.GetPeers()
	topology := buildWelcomeTopology(peers, m.local)
	welcome := ws.WelcomePayload{
		Topology:     topology,
		YourPosition: ws.WirePosition{Depth: 1, ParentID: string(m.local.ID)},
	}
	if err := conn.SendAttachment(ws.AttachmentTypeWelcome, welcome); err != nil {
		logging.Warn("Send welcome to %s failed: %v", hello.NodeID, err)
	}

	nodeID := hello.NodeID
	conn.AddCloseHandler(func(int, string) {
		m.mu.Lock()
		if cur, ok := m.children[nodeID]; ok && cur == conn {
			delete(m.children, nodeID)
		}
		remaining := len(m.children)
		m.mu.Unlock()
		logging.Info("Transient %s detached (%d remaining)", nodeID, remaining)
	})

	logging.Info("Transient %s attached (enclave: %s, children: %d)", hello.NodeID, hello.Enclave, count)
	return true
}

func (m *Manager) sendRedirect(conn *ws.Connection, requestedEnclave, reason string) {
	alts := m.getAlternativeSubstrates(requestedEnclave)
	goodbye := ws.GoodbyePayload{Reason: reason, AlternativeParents: alts}
	if err := conn.SendAttachment(ws.AttachmentTypeGoodbye, goodbye); err != nil {
		logging.Debug("Send redirect goodbye failed: %v", err)
	}
	go func() {
		time.Sleep(redirectCloseDelay)
		if !conn.IsClosed() {
			conn.Close(1000, "redirected")
		}
	}()
}

// Attach completes the hello/welcome handshake from the transient side over
// conn. On success the connection becomes the active parent; on timeout or
// goodbye-during-handshake it returns an error and conn is left untouched
// (the caller decides whether to close it).
//
// After Attach returns successfully it installs long-lived goodbye and
// close handlers that fire reattach when the parent goes away.
func (m *Manager) Attach(ctx context.Context, conn *ws.Connection) (*ws.WelcomePayload, error) {
	return m.attach(ctx, conn, AttachTimeout)
}

func (m *Manager) attach(ctx context.Context, conn *ws.Connection, timeout time.Duration) (*ws.WelcomePayload, error) {
	hello := ws.HelloPayload{
		NodeID:       string(m.local.ID),
		Enclave:      m.local.Enclave,
		Address:      m.local.Address,
		HTTPPort:     m.local.HTTPPort,
		Capabilities: ws.Capabilities{Inbound: string(m.opts.Inbound)},
	}
	// Install the temporary handlers BEFORE sending hello — a fast substrate
	// can answer with welcome before SendAttachment returns, and missing the
	// event causes the select below to wait the full timeout.
	welcomeCh := make(chan *ws.WelcomePayload, 1)
	rejectedCh := make(chan struct{}, 1)
	removeAttach := conn.AddAttachmentHandler(func(msg *ws.AttachmentMessage) {
		switch msg.Type {
		case ws.AttachmentTypeWelcome:
			var w ws.WelcomePayload
			if err := json.Unmarshal(msg.Payload, &w); err != nil {
				logging.Warn("Welcome payload decode failed: %v", err)
				select {
				case rejectedCh <- struct{}{}:
				default:
				}
				return
			}
			select {
			case welcomeCh <- &w:
			default:
			}
		case ws.AttachmentTypeGoodbye:
			select {
			case rejectedCh <- struct{}{}:
			default:
			}
		}
	})
	removeClose := conn.AddCloseHandler(func(int, string) {
		select {
		case rejectedCh <- struct{}{}:
		default:
		}
	})

	// Install the parent-dispatch handler before sending hello. The WS
	// readLoop is single-threaded and processes frames in order, so once
	// welcome is delivered every subsequent gossip frame is guaranteed
	// to find OnMessage already wired. Without this, a fast substrate can
	// push a PUT into a not-yet-wired handler and the frame is dropped.
	m.mu.Lock()
	dispatch := m.parentDispatch
	m.mu.Unlock()
	if dispatch != nil {
		conn.OnMessage(dispatch)
	}

	if err := conn.SendAttachment(ws.AttachmentTypeHello, hello); err != nil {
		removeAttach()
		removeClose()
		return nil, err
	}

	var welcome *ws.WelcomePayload
	select {
	case welcome = <-welcomeCh:
	case <-rejectedCh:
		removeAttach()
		removeClose()
		return nil, errors.New("substrate attachment failed (goodbye or close)")
	case <-time.After(timeout):
		removeAttach()
		removeClose()
		return nil, errors.New("substrate attachment timed out")
	case <-ctx.Done():
		removeAttach()
		removeClose()
		return nil, ctx.Err()
	}
	removeAttach()
	removeClose()

	// parentDispatch was installed before SendAttachment to close the
	// post-welcome dispatch race; nothing to wire here.
	m.mu.Lock()
	m.parent = conn
	m.role = RoleTransient
	m.lastKnownAlts = buildAltsFromTopology(welcome.Topology, string(m.local.ID))
	m.mu.Unlock()
	conn.SetRemote(welcome.YourPosition.ParentID, m.local.Enclave)

	// Install long-lived handlers. The identity guards prevent a stale
	// handler (from a connection that was replaced by a successful
	// reattach) from clobbering the active parent.
	conn.AddAttachmentHandler(func(msg *ws.AttachmentMessage) {
		if msg.Type != ws.AttachmentTypeGoodbye {
			return
		}
		m.mu.Lock()
		stale := m.parent != conn
		m.mu.Unlock()
		if stale {
			return
		}
		var p ws.GoodbyePayload
		if err := json.Unmarshal(msg.Payload, &p); err == nil {
			logging.Info("Substrate sent goodbye: %s (%d alternatives)", p.Reason, len(p.AlternativeParents))
			m.mu.Lock()
			m.parent = nil
			m.mu.Unlock()
			m.triggerReattach(p.AlternativeParents)
		}
	})
	conn.AddCloseHandler(func(int, string) {
		m.mu.Lock()
		stale := m.parent != conn
		m.mu.Unlock()
		if stale {
			return
		}
		logging.Warn("Substrate attachment to %s lost", conn.RemoteNodeID())
		m.mu.Lock()
		m.parent = nil
		m.mu.Unlock()
		m.triggerReattach(nil)
	})

	logging.Info("Attached to substrate %s (depth %d, topology %d nodes)",
		welcome.YourPosition.ParentID, welcome.YourPosition.Depth, len(welcome.Topology))
	return welcome, nil
}

// triggerReattach is the single-flight entry point for the reattach loop.
// Concurrent invocations (goodbye and close firing back-to-back) collapse
// to one running loop. The loop runs in its own goroutine tracked by m.wg
// so Stop() can wait for clean exit before returning.
func (m *Manager) triggerReattach(supplied []ws.AlternativeParent) {
	if m.stopping.Load() {
		return
	}
	m.mu.Lock()
	if m.reattachInFlight {
		m.mu.Unlock()
		return
	}
	m.reattachInFlight = true
	m.wg.Add(1)
	m.mu.Unlock()

	go func() {
		defer m.wg.Done()
		defer func() {
			m.mu.Lock()
			m.reattachInFlight = false
			m.mu.Unlock()
		}()
		m.reattachLoop(supplied)
	}()
}

func (m *Manager) reattachLoop(supplied []ws.AlternativeParent) {
	backoff := ReattachBackoffInitial
	for !m.stopping.Load() {
		// Layer 1: goodbye-supplied alternatives — single-shot.
		if len(supplied) > 0 {
			if m.tryAlternatives(supplied, SeedConnectTimeout, time.Time{}) {
				return
			}
			supplied = nil
		}

		// Layer 2: cached welcome topology.
		m.mu.Lock()
		cached := append([]ws.AlternativeParent(nil), m.lastKnownAlts...)
		m.mu.Unlock()
		if len(cached) > 0 {
			deadline := time.Now().Add(CachedLayerDeadline)
			if m.tryAlternatives(cached, CachedAltConnectTimeout, deadline) {
				return
			}
		}

		// Layer 3: seed list.
		m.mu.Lock()
		provider := m.seedProvider
		m.mu.Unlock()
		var seeds []string
		if provider != nil {
			seeds = provider()
		}
		seedAlts := make([]ws.AlternativeParent, 0, len(seeds))
		for _, s := range seeds {
			if alt, ok := parseSeedAddress(s); ok {
				seedAlts = append(seedAlts, alt)
			}
		}
		if len(seedAlts) > 0 {
			if m.tryAlternatives(seedAlts, SeedConnectTimeout, time.Time{}) {
				return
			}
		}

		logging.Warn("All reattach paths failed — sleeping %v before retry (local store still serves reads)", backoff)
		if !m.sleep(backoff) {
			return
		}
		backoff *= 2
		if backoff > ReattachBackoffMax {
			backoff = ReattachBackoffMax
		}
	}
}

// tryAlternatives walks alts in order, attempting attach to each. Returns
// true on the first success. Honors stopping and an optional layer deadline.
// Self-matching entries (same address+http_port) are skipped — this is the
// regression guard for #120.
func (m *Manager) tryAlternatives(alts []ws.AlternativeParent, perAttemptTimeout time.Duration, layerDeadline time.Time) bool {
	for _, alt := range alts {
		if m.stopping.Load() {
			return false
		}
		if !layerDeadline.IsZero() && !time.Now().Before(layerDeadline) {
			logging.Warn("Cached-alternatives layer hit deadline; falling through to seed list")
			return false
		}
		if alt.Address == m.local.Address && alt.HTTPPort == m.local.HTTPPort {
			continue
		}
		logging.Info("Attempting reattach to %s (%s:%d)", alt.ID, alt.Address, alt.HTTPPort)
		ctx, cancel := context.WithTimeout(m.stopCtx, perAttemptTimeout)
		conn, err := m.dialer(ctx, alt.Address, alt.HTTPPort, m.opts.ClusterSecret, perAttemptTimeout)
		cancel()
		if err != nil {
			logging.Warn("Reattach to %s failed: %v", alt.ID, err)
			continue
		}
		welcome, err := m.attach(m.stopCtx, conn, perAttemptTimeout)
		if err != nil {
			logging.Warn("Reattach to %s handshake failed: %v", alt.ID, err)
			if !conn.IsClosed() {
				conn.Close(1000, "")
			}
			continue
		}
		// Race guard: the conn might have dropped between welcome and now.
		m.mu.Lock()
		stillActive := m.parent == conn && !conn.IsClosed()
		cb := m.onReattach
		m.mu.Unlock()
		if !stillActive {
			logging.Warn("Reattach to %s dropped before activation; trying next", alt.ID)
			continue
		}
		_ = welcome // attach() already populated lastKnownAlts and installed handlers
		logging.Info("Reattached to %s — gossip resumed", alt.ID)
		if cb != nil {
			cb(conn)
		}
		conn.StartHeartbeat()
		return true
	}
	return false
}

// sleep blocks for d unless the manager is stopped first. Returns false if
// the wait was cut short by stop().
func (m *Manager) sleep(d time.Duration) bool {
	if m.stopping.Load() {
		return false
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return !m.stopping.Load()
	case <-m.stopCh:
		return false
	}
}

// RecordAckRoute remembers that messageID's ACK should be forwarded back to
// conn when it arrives. The mapping is auto-evicted after ttl; call
// ClearAckRoute on quorum success to free the entry sooner.
func (m *Manager) RecordAckRoute(messageID string, conn *ws.Connection, ttl time.Duration) {
	m.mu.Lock()
	if existing, ok := m.ackRouteTimers[messageID]; ok {
		existing.Stop()
	}
	m.ackRoutes[messageID] = conn
	m.ackRouteTimers[messageID] = time.AfterFunc(ttl, func() {
		m.mu.Lock()
		delete(m.ackRoutes, messageID)
		delete(m.ackRouteTimers, messageID)
		m.mu.Unlock()
	})
	m.mu.Unlock()
}

// LookupAckRoute returns the child connection that should receive an ACK for
// messageID, or nil if no route exists (PUT was not relayed through here).
func (m *Manager) LookupAckRoute(messageID string) *ws.Connection {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ackRoutes[messageID]
}

// RouteAck satisfies cluster.AckRouter. If ack.MessageID is in the routing
// table, the ack is written to that child's WS pipe and the route is cleared
// (an originator gets at most one ACK from this substrate per write). Returns
// true if the ACK was routed.
func (m *Manager) RouteAck(ack *gossip.Message) bool {
	conn := m.LookupAckRoute(ack.MessageID)
	if conn == nil || conn.IsClosed() {
		return false
	}
	if err := conn.SendGossip(ack); err != nil {
		logging.Debug("RouteAck: send to %s failed: %v", conn.RemoteNodeID(), err)
		return false
	}
	// Don't ClearAckRoute on success — the substrate's own ACK is the first
	// of potentially several, and the originating transient may still need
	// later ACKs routed (e.g., for quorum > 1). The route auto-evicts after
	// RecordAckRoute's TTL.
	return true
}

// ClearAckRoute removes the ACK route for messageID and stops its eviction
// timer. Called by the cluster layer when the originator has accumulated
// enough ACKs for quorum, or when the message times out from the write side.
func (m *Manager) ClearAckRoute(messageID string) {
	m.mu.Lock()
	if t, ok := m.ackRouteTimers[messageID]; ok {
		t.Stop()
		delete(m.ackRouteTimers, messageID)
	}
	delete(m.ackRoutes, messageID)
	m.mu.Unlock()
}

// BroadcastToChildren fans msg out to every attached child whose enclave
// matches. Substrate uses this to deliver PUT replicas received from the
// HTTP gossip mesh down to its attached transients, so transients see
// other agents' writes in their local store. Errors from a single child do
// not abort the broadcast — best-effort like HTTP gossip.
func (m *Manager) BroadcastToChildren(msg *gossip.Message) {
	m.mu.Lock()
	if len(m.children) == 0 {
		m.mu.Unlock()
		return
	}
	conns := make([]*ws.Connection, 0, len(m.children))
	for _, c := range m.children {
		conns = append(conns, c)
	}
	m.mu.Unlock()

	for _, c := range conns {
		// Enclave gating: substrate must not leak cross-enclave writes to
		// transients. The child's enclave is recorded on the Connection by
		// SetRemote during HandleHello, where empty hello.Enclave is
		// normalized to "default" — so strict inequality is safe (no
		// "" → bypass) and any child whose enclave differs is dropped.
		if c.RemoteEnclave() != m.local.Enclave {
			continue
		}
		if err := c.SendGossip(msg); err != nil {
			logging.Debug("BroadcastToChildren: send to %s failed: %v", c.RemoteNodeID(), err)
		}
	}
}

// SendGoodbyeToChildren broadcasts a goodbye-with-alternatives to every
// attached transient. Used during graceful shutdown so transients can
// reattach within seconds instead of waiting the heartbeat timeout.
func (m *Manager) SendGoodbyeToChildren(reason string) {
	if reason == "" {
		reason = "shutdown"
	}
	m.mu.Lock()
	if len(m.children) == 0 {
		m.mu.Unlock()
		return
	}
	conns := make([]*ws.Connection, 0, len(m.children))
	ids := make([]string, 0, len(m.children))
	for id, c := range m.children {
		conns = append(conns, c)
		ids = append(ids, id)
	}
	m.mu.Unlock()

	alts := m.getAlternativeSubstrates("")
	payload := ws.GoodbyePayload{Reason: reason, AlternativeParents: alts}

	logging.Info("Sending goodbye to %d attached transients (%d alternatives)", len(conns), len(alts))
	for i, c := range conns {
		if err := c.SendAttachment(ws.AttachmentTypeGoodbye, payload); err != nil {
			logging.Debug("Failed to send goodbye to %s: %v", ids[i], err)
		}
	}
}

// GetAlternativeSubstrates returns up to 5 candidate substrates for redirects
// and goodbyes, preferring same-enclave peers. Pass empty enclave to use the
// local node's enclave as the preference.
func (m *Manager) GetAlternativeSubstrates(enclave string) []ws.AlternativeParent {
	return m.getAlternativeSubstrates(enclave)
}

func (m *Manager) getAlternativeSubstrates(enclave string) []ws.AlternativeParent {
	if enclave == "" {
		enclave = m.local.Enclave
	}
	peers := m.gossip.GetPeers()
	var sameEnclave, other []*gossip.Node
	for _, p := range peers {
		if p.Enclave == enclave {
			sameEnclave = append(sameEnclave, p)
		} else {
			other = append(other, p)
		}
	}
	candidates := append(sameEnclave, other...)
	if len(candidates) > 5 {
		candidates = candidates[:5]
	}
	out := make([]ws.AlternativeParent, 0, len(candidates))
	for _, p := range candidates {
		out = append(out, ws.AlternativeParent{
			ID:       string(p.ID),
			Address:  p.Address,
			HTTPPort: p.HTTPPort,
			Enclave:  p.Enclave,
		})
	}
	return out
}

// Stop tears the manager down: signals the reattach loop to exit, sends
// goodbye to children, and closes the active parent connection. Safe to
// call more than once. Children are *not* closed here — the HTTP server
// layer owns those sockets.
func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		m.stopping.Store(true)
		close(m.stopCh)
		m.stopCancel()
		m.SendGoodbyeToChildren("shutdown")
		m.mu.Lock()
		parent := m.parent
		m.parent = nil
		m.children = make(map[string]*ws.Connection)
		for _, t := range m.ackRouteTimers {
			t.Stop()
		}
		m.ackRoutes = make(map[string]*ws.Connection)
		m.ackRouteTimers = make(map[string]*time.Timer)
		m.mu.Unlock()
		if parent != nil && !parent.IsClosed() {
			parent.Close(1000, "shutting down")
		}
		m.wg.Wait()
	})
}

// Stopping reports whether Stop has been invoked. Exposed for tests.
func (m *Manager) Stopping() bool { return m.stopping.Load() }

// LastKnownAlternatives returns a snapshot of the cached topology used as the
// reattach fallback. Exposed for testing.
func (m *Manager) LastKnownAlternatives() []ws.AlternativeParent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ws.AlternativeParent(nil), m.lastKnownAlts...)
}

// buildWelcomeTopology builds the SimpleMessage topology entries advertised
// to a newly-attached transient. The list is the local node plus all known
// peers, each wrapped as a SYNC announcement (matching what the transient
// would learn through HTTP gossip's SYNC propagation path).
func buildWelcomeTopology(peers []*gossip.Node, local *gossip.Node) []gossip.SimpleMessage {
	now := time.Now().Unix()
	out := make([]gossip.SimpleMessage, 0, len(peers)+1)
	nodes := append(peers, local)
	for _, n := range nodes {
		out = append(out, gossip.SimpleMessage{
			Type:      string(gossip.MessageTypeSync),
			From:      string(local.ID),
			Timestamp: now,
			MessageID: "",
			NodeInfo: &gossip.SimpleNodeInfo{
				ID:       string(n.ID),
				Address:  n.Address,
				Port:     n.Port,
				HTTPPort: n.HTTPPort,
				Enclave:  n.Enclave,
			},
		})
	}
	return out
}

// buildAltsFromTopology distills welcome.topology into AlternativeParent
// entries for the reattach cache. Self is excluded — the address+port skip
// inside tryAlternatives is the literal-match safety net, but stripping self
// up front keeps the cache honest.
func buildAltsFromTopology(topology []gossip.SimpleMessage, selfID string) []ws.AlternativeParent {
	out := make([]ws.AlternativeParent, 0, len(topology))
	for _, sync := range topology {
		if sync.NodeInfo == nil {
			continue
		}
		if sync.NodeInfo.ID == selfID {
			continue
		}
		out = append(out, ws.AlternativeParent{
			ID:       sync.NodeInfo.ID,
			Address:  sync.NodeInfo.Address,
			HTTPPort: sync.NodeInfo.HTTPPort,
			Enclave:  sync.NodeInfo.Enclave,
		})
	}
	return out
}

// parseSeedAddress turns "host:port" into an AlternativeParent. Empty host or
// port, missing colon, non-numeric port, or out-of-range port → false. IPv6
// in bracket-less form isn't supported (TS reference behaves the same way).
func parseSeedAddress(seed string) (ws.AlternativeParent, bool) {
	idx := strings.LastIndex(seed, ":")
	if idx <= 0 || idx == len(seed)-1 {
		return ws.AlternativeParent{}, false
	}
	address := seed[:idx]
	port, err := strconv.Atoi(seed[idx+1:])
	if err != nil || port <= 0 || port > 65535 {
		return ws.AlternativeParent{}, false
	}
	return ws.AlternativeParent{
		ID:       "seed-" + seed,
		Address:  address,
		HTTPPort: port,
	}, true
}
