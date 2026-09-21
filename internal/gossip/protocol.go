package gossip

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"sopholeth/internal/logging"
)

// clusterMetrics tracks gossip protocol health for Prometheus.
type clusterMetrics struct {
	peersActive   prometheus.Gauge
	peerEvictions prometheus.Counter
	peerJoins     prometheus.Counter
	pingFailures  prometheus.Counter
}

var (
	sharedMetrics     *clusterMetrics
	sharedMetricsOnce sync.Once
)

func newClusterMetrics() *clusterMetrics {
	sharedMetricsOnce.Do(func() {
		sharedMetrics = &clusterMetrics{
			peersActive: prometheus.NewGauge(prometheus.GaugeOpts{
				Name: "gossip_peers_active",
				Help: "Current number of active peers in the gossip protocol",
			}),
			peerEvictions: prometheus.NewCounter(prometheus.CounterOpts{
				Name: "gossip_peer_evictions_total",
				Help: "Total number of peers evicted due to consecutive ping failures",
			}),
			peerJoins: prometheus.NewCounter(prometheus.CounterOpts{
				Name: "gossip_peer_joins_total",
				Help: "Total number of peers added (initial join or rejoin after eviction)",
			}),
			pingFailures: prometheus.NewCounter(prometheus.CounterOpts{
				Name: "gossip_ping_failures_total",
				Help: "Total number of failed ping attempts to peers",
			}),
		}
		prometheus.MustRegister(sharedMetrics.peersActive, sharedMetrics.peerEvictions, sharedMetrics.peerJoins, sharedMetrics.pingFailures)
	})
	return sharedMetrics
}

type NodeID string

type Node struct {
	ID       NodeID `json:"id"`
	Address  string `json:"address"`
	Port     int    `json:"port"`      // Gossip port
	HTTPPort int    `json:"http_port"` // HTTP API port
	Enclave  string `json:"enclave"`   // Replication boundary (default: "default")
}

func (n *Node) String() string {
	return fmt.Sprintf("%s@%s:%d", n.ID, n.Address, n.Port)
}

type Message struct {
	Type      MessageType `json:"type"`
	From      NodeID      `json:"from"`
	To        NodeID      `json:"to,omitempty"`
	Key       string      `json:"key,omitempty"`
	Data      []byte      `json:"data,omitempty"`
	TTL       int         `json:"ttl,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
	MessageID string      `json:"message_id"`
	// Node information for JOIN messages
	NodeInfo *Node `json:"node_info,omitempty"`
}

type MessageType string

const (
	MessageTypePut  MessageType = "PUT"
	MessageTypeGet  MessageType = "GET"
	MessageTypePing MessageType = "PING"
	MessageTypePong MessageType = "PONG"
	MessageTypeSync MessageType = "SYNC"
	MessageTypeAck  MessageType = "ACK"
)

// MaxPingFailures is the number of consecutive failed health checks before
// a peer is evicted from the peer list. With a 30-second ping interval this
// means a peer is removed after ~90 seconds of unreachability. Evicted peers
// rejoin automatically if they come back online and re-bootstrap.
const MaxPingFailures = 3

// FanoutThreshold is the enclave peer count above which gossip switches from
// full broadcast (O(N)) to probabilistic fanout (O(√N) per hop). Below this
// threshold, every enclave peer receives each message directly.
const FanoutThreshold = 10

// seenMessageTTL is how long a message ID stays in the dedup cache.
// Should be longer than the maximum expected propagation time.
const seenMessageTTL = 60 * time.Second

// maxSeenMessages caps the dedup cache to prevent unbounded growth under
// sustained high write throughput between cleanup cycles.
const maxSeenMessages = 100000

type Protocol struct {
	localNode         *Node
	peers             map[NodeID]*Node
	peerFailures      map[NodeID]int // consecutive ping failures per peer
	peersMutex        sync.RWMutex
	replicationFactor int
	quorumSize        int
	clusterSecret     string
	messageHandler    func(*Message) error
	transport         Transport
	topologyTicker    *time.Ticker
	stopChan          chan struct{}
	metrics           *clusterMetrics      // nil in tests (skip metrics)
	seenMessages      map[string]time.Time // message ID → expiry time (dedup cache)
	seenMutex         sync.Mutex
}

type Transport interface {
	Start(ctx context.Context) error
	Stop() error
	Send(ctx context.Context, node *Node, msg *Message) error
	SetMessageHandler(handler func(*Message) error)
}

func NewProtocol(localNode *Node, replicationFactor int, clusterSecret string) *Protocol {
	quorumSize := (replicationFactor / 2) + 1
	return &Protocol{
		localNode:         localNode,
		peers:             make(map[NodeID]*Node),
		peerFailures:      make(map[NodeID]int),
		replicationFactor: replicationFactor,
		quorumSize:        quorumSize,
		clusterSecret:     clusterSecret,
		stopChan:          make(chan struct{}),
		seenMessages:      make(map[string]time.Time),
	}
}

// EnableMetrics registers Prometheus metrics for cluster health monitoring.
// Call this once during production startup. Tests skip this to avoid
// duplicate metric registration across parallel test runs.
func (p *Protocol) EnableMetrics() {
	p.metrics = newClusterMetrics()
}

func (p *Protocol) SetTransport(transport Transport) {
	p.transport = transport
	// Always use protocol's handleMessage which will delegate to app handler
	transport.SetMessageHandler(p.handleMessage)
}

func (p *Protocol) Start(ctx context.Context) error {
	if p.transport == nil {
		return fmt.Errorf("transport not set")
	}

	if err := p.transport.Start(ctx); err != nil {
		return fmt.Errorf("failed to start transport: %w", err)
	}

	// Create ticker before goroutine to avoid data race with Stop()
	p.topologyTicker = time.NewTicker(30 * time.Second)

	// Start periodic health checks
	go p.startHealthCheck(ctx)

	// Start periodic topology synchronization
	go p.startTopologySync(ctx)

	logging.Info("[%s] Gossip protocol started", p.localNode.ID)
	return nil
}

func (p *Protocol) Stop() error {
	// Stop topology sync
	close(p.stopChan)
	if p.topologyTicker != nil {
		p.topologyTicker.Stop()
	}

	if p.transport != nil {
		return p.transport.Stop()
	}
	return nil
}

// addPeer adds node to the peer set or rejects and warns if node is self.
// Returns true if the node was added or updated, false if rejected.
//
// The self-rejection is the single chokepoint that keeps self out of the
// peer map across all entry points (handleSync, HandleBootstrap,
// performTopologySync, etc.). Callers that previously assumed
// "addPeer always succeeds" are still correct for non-self inputs;
// the warn log surfaces any caller path that ends up trying to add self,
// which is always a bug (#82, #87).
func (p *Protocol) addPeer(node *Node) bool {
	if node.ID == p.localNode.ID {
		logging.Warn("[%s] addPeer rejected: attempted to add self (%s) — caller likely missing a self-filter",
			p.localNode.ID, node.ID)
		return false
	}

	p.peersMutex.Lock()
	p.peers[node.ID] = node
	delete(p.peerFailures, node.ID) // reset failure counter on (re-)add
	peerCount := len(p.peers)
	p.peersMutex.Unlock()

	if p.metrics != nil {
		p.metrics.peersActive.Set(float64(peerCount))
		p.metrics.peerJoins.Inc()
	}
	return true
}

func (p *Protocol) removePeer(nodeID NodeID) {
	p.peersMutex.Lock()
	delete(p.peers, nodeID)
	delete(p.peerFailures, nodeID)
	peerCount := len(p.peers)
	p.peersMutex.Unlock()

	if p.metrics != nil {
		p.metrics.peersActive.Set(float64(peerCount))
	}
}

func (p *Protocol) getPeers() []*Node {
	p.peersMutex.RLock()
	defer p.peersMutex.RUnlock()

	peers := make([]*Node, 0, len(p.peers))
	for _, peer := range p.peers {
		peers = append(peers, peer)
	}
	return peers
}

func (p *Protocol) handleMessage(msg *Message) error {
	// Handle protocol-level messages first
	switch msg.Type {
	case MessageTypePing:
		return p.handlePing(msg)
	case MessageTypePong:
		return p.handlePong(msg)
	case MessageTypeSync:
		return p.handleSync(msg)
	case MessageTypePut, MessageTypeAck:
		// Application-level messages - pass to handler
		if p.messageHandler != nil {
			return p.messageHandler(msg)
		}
		return nil
	default:
		// Unknown message type
		if p.messageHandler != nil {
			return p.messageHandler(msg)
		}
	}
	return nil
}

func (p *Protocol) handlePing(msg *Message) error {
	pong := &Message{
		Type:      MessageTypePong,
		From:      p.localNode.ID,
		To:        msg.From,
		Timestamp: time.Now(),
		MessageID: generateMessageID(),
		NodeInfo:  p.localNode, // Include our identity and enclave membership
	}

	p.peersMutex.RLock()
	peer := p.peers[msg.From]
	p.peersMutex.RUnlock()

	if peer != nil {
		return p.transport.Send(context.Background(), peer, pong)
	}
	return nil
}

func (p *Protocol) handlePong(msg *Message) error {
	p.peersMutex.Lock()
	// Reset failure counter — peer is alive
	delete(p.peerFailures, msg.From)

	// Update peer's enclave membership if included
	if msg.NodeInfo != nil {
		if msg.NodeInfo.Enclave == "" {
			msg.NodeInfo.Enclave = "default"
		}
		if existing, ok := p.peers[msg.NodeInfo.ID]; ok && existing.Enclave != msg.NodeInfo.Enclave {
			existing.Enclave = msg.NodeInfo.Enclave
			logging.Debug("[%s] Updated peer %s enclave to %s via PONG", p.localNode.ID, msg.NodeInfo.ID, msg.NodeInfo.Enclave)
		}
	}
	p.peersMutex.Unlock()
	return nil
}

func (p *Protocol) handleSync(msg *Message) error {
	logging.Debug("[%s] Received SYNC message from %s", p.localNode.ID, msg.From)

	// SYNC messages carry information about a node (either the sender
	// introducing itself, or a peer propagating knowledge of a third node).
	if msg.NodeInfo != nil {
		// Normalize empty enclave to "default" (backwards compat with pre-enclave nodes)
		if msg.NodeInfo.Enclave == "" {
			msg.NodeInfo.Enclave = "default"
		}

		// Don't add ourselves as a peer
		if msg.NodeInfo.ID == p.localNode.ID {
			return nil
		}

		// Check if we already know this peer
		p.peersMutex.RLock()
		existing, exists := p.peers[msg.NodeInfo.ID]
		p.peersMutex.RUnlock()

		if !exists {
			p.addPeer(msg.NodeInfo)
			logging.Info("[%s] Learned about new peer %s (enclave: %s) via SYNC from %s",
				p.localNode.ID, msg.NodeInfo.ID, msg.NodeInfo.Enclave, msg.From)
		} else if existing.Enclave != msg.NodeInfo.Enclave {
			// Update enclave if it changed (e.g., node upgraded and now reports enclave)
			p.addPeer(msg.NodeInfo)
			logging.Info("[%s] Updated peer %s enclave: %s → %s (via SYNC from %s)",
				p.localNode.ID, msg.NodeInfo.ID, existing.Enclave, msg.NodeInfo.Enclave, msg.From)
		} else {
			logging.Debug("[%s] Already know peer %s (SYNC from %s)",
				p.localNode.ID, msg.NodeInfo.ID, msg.From)
		}
	} else {
		logging.Debug("[%s] SYNC message from %s has no NodeInfo", p.localNode.ID, msg.From)
	}

	// Respond with our peer list so the sender can discover peers
	// it doesn't know about yet. Only respond to direct SYNC messages
	// (where From == NodeInfo sender), not to propagated peer info,
	// to prevent amplification loops.
	if msg.NodeInfo != nil && msg.NodeInfo.ID == msg.From {
		p.respondWithPeerList(msg.From)
	}

	return nil
}

// respondWithPeerList sends a SYNC message for each known peer (including
// ourselves) to the given node. This propagates peer knowledge transitively:
// if A knows B and C, but D only knows A, D will learn about B and C when
// A responds to D's SYNC.
func (p *Protocol) respondWithPeerList(targetID NodeID) {
	p.peersMutex.RLock()
	target, exists := p.peers[targetID]
	if !exists {
		p.peersMutex.RUnlock()
		return
	}
	// Snapshot the peer list while holding the lock
	peers := make([]*Node, 0, len(p.peers))
	for _, peer := range p.peers {
		peers = append(peers, peer)
	}
	p.peersMutex.RUnlock()

	// Send a SYNC for each peer we know about (including ourselves)
	allNodes := append(peers, p.localNode)
	for _, node := range allNodes {
		// Don't tell the target about itself
		if node.ID == targetID {
			continue
		}

		syncMsg := &Message{
			Type:      MessageTypeSync,
			From:      p.localNode.ID,
			Timestamp: time.Now(),
			MessageID: generateMessageID(),
			NodeInfo:  node,
		}

		ctx := context.Background()
		if err := p.transport.Send(ctx, target, syncMsg); err != nil {
			logging.Debug("[%s] Failed to send peer info for %s to %s: %v",
				p.localNode.ID, node.ID, targetID, err)
		}
	}
}

func (p *Protocol) startHealthCheck(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.pingPeers(ctx)
			p.cleanupSeenMessages()
		}
	}
}

func (p *Protocol) pingPeers(ctx context.Context) {
	peers := p.getPeers()
	var evictions []NodeID

	for _, peer := range peers {
		ping := &Message{
			Type:      MessageTypePing,
			From:      p.localNode.ID,
			To:        peer.ID,
			Timestamp: time.Now(),
			MessageID: generateMessageID(),
		}
		if err := p.transport.Send(ctx, peer, ping); err != nil {
			p.peersMutex.Lock()
			p.peerFailures[peer.ID]++
			failures := p.peerFailures[peer.ID]
			p.peersMutex.Unlock()

			if p.metrics != nil {
				p.metrics.pingFailures.Inc()
			}

			logging.Warn("[%s] Ping failed for peer %s (%d/%d): %v",
				p.localNode.ID, peer.ID, failures, MaxPingFailures, err)

			if failures >= MaxPingFailures {
				evictions = append(evictions, peer.ID)
			}
		}
	}

	for _, id := range evictions {
		p.removePeer(id)
		if p.metrics != nil {
			p.metrics.peerEvictions.Inc()
		}
		logging.Info("[%s] Evicted peer %s after %d consecutive ping failures",
			p.localNode.ID, id, MaxPingFailures)
	}
}

func (p *Protocol) Send(ctx context.Context, node *Node, msg *Message) error {
	if p.transport == nil {
		return fmt.Errorf("transport not set")
	}
	return p.transport.Send(ctx, node, msg)
}

func (p *Protocol) Broadcast(ctx context.Context, msg *Message) error {
	if p.transport == nil {
		return fmt.Errorf("transport not set")
	}

	// Send to all known peers
	peers := p.getPeers()
	logging.Debug("[%s] Broadcasting %s message to %d peers", p.localNode.ID, msg.Type, len(peers))
	for _, peer := range peers {
		logging.Debug("[%s] Sending %s to peer %s", p.localNode.ID, msg.Type, peer.ID)
		if err := p.transport.Send(ctx, peer, msg); err != nil {
			logging.Warn("[%s] Failed to send to peer %s: %v", p.localNode.ID, peer.ID, err)
			// Continue to other peers even if one fails
		}
	}

	return nil
}

// MarkSeen records a message ID in the dedup cache. Returns true if the
// message was already seen (duplicate), false if it's new.
// If the cache is at capacity, expired entries are evicted first. If still
// full, the oldest half is dropped to make room.
func (p *Protocol) MarkSeen(messageID string) bool {
	p.seenMutex.Lock()
	defer p.seenMutex.Unlock()

	if _, seen := p.seenMessages[messageID]; seen {
		return true
	}

	// Enforce capacity bound
	if len(p.seenMessages) >= maxSeenMessages {
		p.evictSeenLocked()
	}

	p.seenMessages[messageID] = time.Now().Add(seenMessageTTL)
	return false
}

// evictSeenLocked removes expired entries, then drops the oldest half if
// still at capacity. Must be called with seenMutex held.
func (p *Protocol) evictSeenLocked() {
	now := time.Now()
	for id, expiry := range p.seenMessages {
		if now.After(expiry) {
			delete(p.seenMessages, id)
		}
	}

	// If expired cleanup wasn't enough, drop the oldest half
	if len(p.seenMessages) >= maxSeenMessages {
		// Find the median expiry (oldest entries have earliest expiry)
		earliest := now.Add(seenMessageTTL) // start with max possible
		for _, expiry := range p.seenMessages {
			if expiry.Before(earliest) {
				earliest = expiry
			}
		}
		midpoint := earliest.Add(now.Add(seenMessageTTL).Sub(earliest) / 2)

		for id, expiry := range p.seenMessages {
			if expiry.Before(midpoint) {
				delete(p.seenMessages, id)
			}
		}
	}
}

// cleanupSeenMessages removes expired entries from the dedup cache.
func (p *Protocol) cleanupSeenMessages() {
	p.seenMutex.Lock()
	defer p.seenMutex.Unlock()

	now := time.Now()
	for id, expiry := range p.seenMessages {
		if now.After(expiry) {
			delete(p.seenMessages, id)
		}
	}
}

// fanoutSize returns the number of peers to forward to for probabilistic gossip.
// Returns √N (rounded up, minimum 1).
func fanoutSize(peerCount int) int {
	if peerCount <= 0 {
		return 0
	}
	f := int(math.Ceil(math.Sqrt(float64(peerCount))))
	if f < 1 {
		f = 1
	}
	return f
}

// selectRandomPeers picks n random peers from the given list, excluding skipID.
func selectRandomPeers(peers []*Node, n int, skipID NodeID) []*Node {
	// Filter out the node to skip (typically the sender)
	var candidates []*Node
	for _, p := range peers {
		if p.ID != skipID {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) <= n {
		return candidates
	}
	rand.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})
	return candidates[:n]
}

// BroadcastToEnclave sends a message to peers in the same enclave.
// For small enclaves (≤ FanoutThreshold peers), sends to all peers directly.
// For larger enclaves, uses probabilistic fanout: sends to √N random peers,
// which forward to their own √N subset. Deduplication prevents re-processing.
func (p *Protocol) BroadcastToEnclave(ctx context.Context, msg *Message) error {
	if p.transport == nil {
		return fmt.Errorf("transport not set")
	}

	// Mark as seen by the originator so we don't re-forward our own messages
	p.MarkSeen(msg.MessageID)

	peers := p.GetReplicationPeers()

	if len(peers) <= FanoutThreshold {
		// Small enclave: full broadcast (original behavior)
		logging.Debug("[%s] Broadcasting %s to %d enclave peers (%s)", p.localNode.ID, msg.Type, len(peers), p.localNode.Enclave)
		for _, peer := range peers {
			if err := p.transport.Send(ctx, peer, msg); err != nil {
				logging.Warn("[%s] Failed to send to enclave peer %s: %v", p.localNode.ID, peer.ID, err)
			}
		}
	} else {
		// Large enclave: probabilistic fanout
		fanout := fanoutSize(len(peers))
		targets := selectRandomPeers(peers, fanout, "")
		logging.Debug("[%s] Fanout %s to %d/%d enclave peers (%s)", p.localNode.ID, msg.Type, len(targets), len(peers), p.localNode.Enclave)
		for _, peer := range targets {
			if err := p.transport.Send(ctx, peer, msg); err != nil {
				logging.Warn("[%s] Failed to send to enclave peer %s: %v", p.localNode.ID, peer.ID, err)
			}
		}
	}

	return nil
}

// ForwardToEnclave is called by receiving nodes to continue probabilistic gossip.
// It forwards the message to √N random enclave peers (excluding the sender),
// but only if the enclave is above the fanout threshold. For small enclaves,
// the originator already sent to everyone, so no forwarding is needed.
func (p *Protocol) ForwardToEnclave(ctx context.Context, msg *Message) {
	peers := p.GetReplicationPeers()
	if len(peers) <= FanoutThreshold {
		return // originator already sent to all peers
	}

	fanout := fanoutSize(len(peers))
	targets := selectRandomPeers(peers, fanout, msg.From)
	if len(targets) == 0 {
		return
	}

	logging.Debug("[%s] Forwarding %s (key: %s) to %d enclave peers", p.localNode.ID, msg.Type, msg.Key, len(targets))
	for _, peer := range targets {
		if err := p.transport.Send(ctx, peer, msg); err != nil {
			logging.Warn("[%s] Failed to forward to enclave peer %s: %v", p.localNode.ID, peer.ID, err)
		}
	}
}

func (p *Protocol) GetPeers() []*Node {
	return p.getPeers()
}

// GetReplicationPeers returns only peers in the same enclave as the local node.
func (p *Protocol) GetReplicationPeers() []*Node {
	p.peersMutex.RLock()
	defer p.peersMutex.RUnlock()

	var peers []*Node
	for _, peer := range p.peers {
		if peer.Enclave == p.localNode.Enclave {
			peers = append(peers, peer)
		}
	}
	return peers
}

// PeerFailureCount returns the number of consecutive ping failures for a peer.
func (p *Protocol) PeerFailureCount(id NodeID) int {
	p.peersMutex.RLock()
	defer p.peersMutex.RUnlock()
	return p.peerFailures[id]
}

func (p *Protocol) SetMessageHandler(handler func(*Message) error) {
	p.messageHandler = handler
}

// HandleMessage is the public entry point for processing messages
func (p *Protocol) HandleMessage(msg *Message) error {
	return p.handleMessage(msg)
}

var messageCounter uint64

func generateMessageID() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), atomic.AddUint64(&messageCounter, 1))
}

// startTopologySync periodically exchanges peer lists to fix broken topology.
// The ticker is created in Start() before this goroutine is launched.
func (p *Protocol) startTopologySync(ctx context.Context) {
	for {
		select {
		case <-p.topologyTicker.C:
			p.performTopologySync(ctx)
		case <-p.stopChan:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (p *Protocol) performTopologySync(ctx context.Context) {
	p.peersMutex.RLock()
	peerCount := len(p.peers)
	expectedPeers := p.replicationFactor - 1 // Don't count ourselves
	p.peersMutex.RUnlock()

	// Only sync if we have fewer peers than expected
	if peerCount >= expectedPeers {
		return
	}

	logging.Debug("[%s] Topology sync: have %d peers, expected %d - requesting peer lists",
		p.localNode.ID, peerCount, expectedPeers)

	// Send SYNC requests to all known peers to get their peer lists
	msg := &Message{
		Type:      MessageTypeSync,
		From:      p.localNode.ID,
		Timestamp: time.Now(),
		MessageID: generateMessageID(),
		NodeInfo:  p.localNode, // Include our own node info
	}

	// Broadcast to all known peers
	if err := p.Broadcast(ctx, msg); err != nil {
		logging.Warn("[%s] Failed to broadcast topology sync: %v", p.localNode.ID, err)
	}
}
