package cluster

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"sopholeth/internal/gossip"
	"sopholeth/internal/logging"
	"sopholeth/internal/storage"
)

// ErrQuorumTimeout indicates the write was stored locally but quorum
// confirmation was not received within the timeout window. The data
// will still propagate via gossip — this is not a write failure.
var ErrQuorumTimeout = fmt.Errorf("quorum timeout: stored locally, replication pending")

type ClusterNode struct {
	localNode         *gossip.Node
	protocol          *gossip.Protocol
	store             Store
	replicationFactor int
	writeTimeout      time.Duration
	clusterSecret     string
	httpClient        *http.Client
	writeLimits       WriteLimits // Configured before Start; applies to all stored writes.

	pendingWrites map[string]*WriteOperation
	writesMutex   sync.RWMutex

	// isRoot supports explicit test/private setup. Public nodes install a
	// rootProvider that checks the current verified discovery deadline.
	isRoot       atomic.Bool
	rootProvider func() bool // Configured before Start; checks runtime validity on use.

	// seedProvider returns the current bootstrap seed list. Used by the
	// isolation-recovery loop to re-bootstrap when peer count drops to 0
	// (#85, F5). Public deployments wire this to the omega refresher's
	// current list; private deployments wire it to the static NODE_PEERS
	// list. Nil disables recovery (used for tests and for cases where
	// re-bootstrap doesn't make sense).
	seedProvider func() []string

	// ackRouter, when set, receives ACKs whose To= field doesn't match a
	// known HTTP peer. Substrate nodes use this to forward ACKs back to
	// transient children that originated a relayed PUT (#135).
	ackRouter AckRouter

	// childBroadcaster, when set, receives every successfully-stored PUT
	// so it can fan the replica out to attached transients (#135). Nil
	// outside substrate mode.
	childBroadcaster ChildBroadcaster
}

// AckRouter forwards an ACK to a non-peer originator. The substrate's
// tree manager satisfies this interface — its routing table maps each
// relayed messageId to the originating child connection. Returns true if
// the ACK was routed (caller skips other dispatch paths).
type AckRouter interface {
	RouteAck(ack *gossip.Message) bool
}

// ChildBroadcaster fans a gossip message out to attached transient
// children whose enclave matches the local node. The substrate's tree
// manager satisfies this — see tree.Manager.BroadcastToChildren.
type ChildBroadcaster interface {
	BroadcastToChildren(msg *gossip.Message)
}

// IsolationRecoveryInterval is how often the recovery loop checks for
// the zero-peer condition. Matches the gossip health-check cadence so
// recovery starts within one tick of full eviction.
const IsolationRecoveryInterval = 30 * time.Second

type WriteOperation struct {
	Key           string
	Data          []byte
	TTL           time.Duration
	Confirmations int
	// signalComplete is guarded by signalOnce so the local-quorum path,
	// the gossip-ACK path, and any racing late ACK can all signal once
	// without panicking on close-of-closed-channel. Pendant of the
	// MessageID dedup that keys pendingWrites: there can still be more
	// than one goroutine that observes "quorum reached" within a single
	// write's lifetime.
	Complete   chan bool
	signalOnce sync.Once
	Error      error
}

// markComplete signals the write as quorum-reached at most once. Safe
// to call from any goroutine that observes the quorum threshold.
func (w *WriteOperation) markComplete() {
	w.signalOnce.Do(func() { close(w.Complete) })
}

type Store interface {
	Put(key string, data []byte, ttl time.Duration) error
	Get(key string) ([]byte, bool)
	GetWithMetadata(key string) ([]byte, time.Time, time.Duration, bool) // data, createdAt, originalTTL, exists
	Scan() []string
	// ScanPage returns at most limit live keys in lexicographic order that
	// share prefix and sort strictly after cursor. It lets callers page
	// through the keyspace at page-sized cost instead of full scans (#220).
	ScanPage(prefix, cursor string, limit int) []string
}

func NewClusterNode(nodeID string, address string, gossipPort int, httpPort int, replicationFactor int, maxStorageBytes int64, writeTimeout time.Duration, clusterSecret string, enclave string) *ClusterNode {
	if enclave == "" {
		enclave = "default"
	}
	localNode := &gossip.Node{
		ID:       gossip.NodeID(nodeID),
		Address:  address,
		Port:     gossipPort,
		HTTPPort: httpPort,
		Enclave:  enclave,
	}

	protocol := gossip.NewProtocol(localNode, replicationFactor, clusterSecret)

	return &ClusterNode{
		localNode:         localNode,
		protocol:          protocol,
		store:             storage.NewMemoryStore(maxStorageBytes),
		replicationFactor: replicationFactor,
		writeTimeout:      writeTimeout,
		clusterSecret:     clusterSecret,
		writeLimits:       DefaultWriteLimits(),
		pendingWrites:     make(map[string]*WriteOperation),
	}
}

func (cn *ClusterNode) Start(ctx context.Context, bootstrapAddresses []string) error {
	transport := gossip.NewHTTPTransport(cn.localNode, cn.clusterSecret)
	if cn.httpClient != nil {
		transport.SetHTTPClient(cn.httpClient)
	}
	cn.protocol.SetTransport(transport)
	cn.protocol.SetMessageHandler(cn.handleGossipMessage)
	cn.protocol.EnableMetrics()

	// Start the gossip protocol
	if err := cn.protocol.Start(ctx); err != nil {
		return fmt.Errorf("failed to start gossip protocol: %w", err)
	}

	// Bootstrap from seed nodes
	if len(bootstrapAddresses) > 0 {
		logging.Info("[%s] Bootstrapping from %d seed nodes", cn.localNode.ID, len(bootstrapAddresses))
		if err := cn.protocol.Bootstrap(ctx, bootstrapAddresses); err != nil {
			// Bootstrap failure is not fatal - we might be the first node
			logging.Warn("[%s] Bootstrap completed with warning: %v", cn.localNode.ID, err)
		}
	} else {
		logging.Info("[%s] Starting as first node (no bootstrap addresses)", cn.localNode.ID)
	}

	go cn.runIsolationRecovery(ctx)

	return nil
}

// SetSeedProvider wires a callback that returns the current bootstrap
// seed list. The isolation-recovery loop calls it whenever peer count
// drops to 0 to attempt re-bootstrap. Nil disables recovery (#85, F5).
func (cn *ClusterNode) SetSeedProvider(p func() []string) {
	cn.seedProvider = p
}

// SetAckRouter installs the AckRouter used to deliver ACKs back to
// non-peer originators (i.e., transient children attached via WebSocket).
// Substrate nodes wire their tree.Manager here; transient nodes leave it
// nil since their originator is themselves and ACKs come through the
// pendingWrites path.
func (cn *ClusterNode) SetAckRouter(r AckRouter) {
	cn.ackRouter = r
}

// SetChildBroadcaster installs the ChildBroadcaster used to fan stored
// PUTs out to attached transient children. Nil disables WS receive-path
// fan-out (#135 phase 5).
func (cn *ClusterNode) SetChildBroadcaster(b ChildBroadcaster) {
	cn.childBroadcaster = b
}

// runIsolationRecovery polls peer count on IsolationRecoveryInterval
// and triggers re-bootstrap when the node is fully isolated (#85, F5).
//
// Why polling rather than event-driven from removePeer: multi-eviction
// in a single pingPeers tick can fire several removals back-to-back, and
// triggering from each would either spawn duplicate recoveries or
// require single-flight machinery. Polling also catches isolation that
// happens via paths other than ping eviction.
func (cn *ClusterNode) runIsolationRecovery(ctx context.Context) {
	ticker := time.NewTicker(IsolationRecoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cn.CheckIsolationAndRecover(ctx)
		}
	}
}

// CheckIsolationAndRecover runs one isolation-recovery cycle. Returns
// true if a re-bootstrap was attempted AND it found at least one peer.
// Public so tests can drive recovery deterministically without timers.
func (cn *ClusterNode) CheckIsolationAndRecover(ctx context.Context) bool {
	if len(cn.protocol.GetPeers()) > 0 {
		return false
	}
	if cn.seedProvider == nil {
		return false
	}
	seeds := cn.seedProvider()
	if len(seeds) == 0 {
		return false
	}
	logging.Warn("[%s] Isolated (0 peers) — re-bootstrapping against %d seeds",
		cn.localNode.ID, len(seeds))
	if err := cn.protocol.Bootstrap(ctx, seeds); err != nil {
		logging.Warn("[%s] Re-bootstrap failed: %v", cn.localNode.ID, err)
		return false
	}
	after := len(cn.protocol.GetPeers())
	if after > 0 {
		logging.Info("[%s] Re-bootstrap recovered %d peers", cn.localNode.ID, after)
		return true
	}
	logging.Warn("[%s] Re-bootstrap completed but discovered no peers; will retry next cycle",
		cn.localNode.ID)
	return false
}

func (cn *ClusterNode) Stop() error {
	return cn.protocol.Stop()
}

// IsRoot reports whether this node's advertised address is present in the
// currently-trusted signed root list. Used by the HTTP server to gate
// bootstrap responses — non-roots return 403 rather than handing out peer
// topology to arbitrary callers.
func (cn *ClusterNode) IsRoot() bool {
	if cn.rootProvider != nil {
		return cn.rootProvider()
	}
	return cn.isRoot.Load()
}

// SetRootProvider installs the expiring discovery role, independent of peer
// membership. Configure it before Start, alongside the recovery seed provider.
func (cn *ClusterNode) SetRootProvider(provider func() bool) { cn.rootProvider = provider }

// SetRootBindings updates routing pins from verified discovery, independently
// of the expiring root role and bootstrap seed providers.
func (cn *ClusterNode) SetRootBindings(bindings map[gossip.NodeID]gossip.PeerBinding) {
	cn.protocol.SetRootBindings(bindings)
}

// SetHTTPOrigin sets the externally reachable API/gossip origin before Start.
// Listener ports may differ when a TLS proxy fronts the node.
func (cn *ClusterNode) SetHTTPOrigin(origin string) { cn.localNode.HTTPOrigin = origin }

// ConfigureBootstrap must be called before Start. nil uses the system CA store
// and the ordinary private/explicit bootstrap behavior.
func (cn *ClusterNode) ConfigureBootstrap(client *http.Client, check func(string, []*gossip.Node) error) {
	cn.httpClient = client
	cn.protocol.ConfigureBootstrap(client, check)
}

// SetRoot updates the root flag. Call after each verified refresh of the
// omega signed root list.
func (cn *ClusterNode) SetRoot(v bool) {
	cn.isRoot.Store(v)
}

func (cn *ClusterNode) Put(ctx context.Context, key string, data []byte, ttl time.Duration) error {
	if err := cn.validateWrite(key, data); err != nil {
		return err
	}
	ttl = cn.clampTTL(int64(ttl / time.Second))
	quorum := cn.quorumSize()

	msg := &gossip.Message{
		Type:      gossip.MessageTypePut,
		From:      cn.localNode.ID,
		Key:       key,
		Data:      data,
		TTL:       int(ttl.Seconds()),
		Timestamp: time.Now(),
		MessageID: fmt.Sprintf("%s-%d", key, time.Now().UnixNano()),
	}

	writeOp := &WriteOperation{
		Key:           key,
		Data:          data,
		TTL:           ttl,
		Confirmations: 1, // Count local write
		Complete:      make(chan bool, 1),
	}

	// Key on MessageID so concurrent writes to the same key don't
	// collide — each write tracks its own quorum independently.
	cn.writesMutex.Lock()
	cn.pendingWrites[msg.MessageID] = writeOp
	cn.writesMutex.Unlock()

	if err := cn.store.Put(key, data, ttl); err != nil {
		cn.writesMutex.Lock()
		delete(cn.pendingWrites, msg.MessageID)
		cn.writesMutex.Unlock()
		return fmt.Errorf("local write failed: %w", err)
	}

	// Check if local write is sufficient for quorum (single node or single-node enclave)
	if writeOp.Confirmations >= quorum {
		cn.writesMutex.Lock()
		delete(cn.pendingWrites, msg.MessageID)
		cn.writesMutex.Unlock()
		writeOp.markComplete()
		logging.Debug("Write completed locally (quorum=%d, confirmations=%d)", quorum, writeOp.Confirmations)
		return nil
	}

	logging.Debug("[%s] Broadcasting PUT for key %s to enclave peers", cn.localNode.ID, key)
	if err := cn.protocol.BroadcastToEnclave(ctx, msg); err != nil {
		logging.Warn("[%s] Failed to broadcast write to enclave: %v", cn.localNode.ID, err)
	}

	select {
	case <-writeOp.Complete:
		cn.writesMutex.Lock()
		err := writeOp.Error
		delete(cn.pendingWrites, msg.MessageID)
		cn.writesMutex.Unlock()
		return err
	case <-time.After(cn.writeTimeout):
		cn.writesMutex.Lock()
		delete(cn.pendingWrites, msg.MessageID)
		cn.writesMutex.Unlock()
		return ErrQuorumTimeout
	case <-ctx.Done():
		cn.writesMutex.Lock()
		delete(cn.pendingWrites, msg.MessageID)
		cn.writesMutex.Unlock()
		return ctx.Err()
	}
}

func (cn *ClusterNode) Get(key string) ([]byte, bool) {
	return cn.store.Get(key)
}

func (cn *ClusterNode) GetWithMetadata(key string) ([]byte, time.Time, time.Duration, bool) {
	return cn.store.GetWithMetadata(key)
}

func (cn *ClusterNode) HandleGossipMessage(msg *gossip.Message) error {
	// Route protocol messages to the protocol handler
	switch msg.Type {
	case gossip.MessageTypePing, gossip.MessageTypePong, gossip.MessageTypeSync, gossip.MessageTypeSyncRequest:
		// Let the protocol handle its own messages
		return cn.protocol.HandleMessage(msg)
	default:
		// Application messages
		return cn.handleGossipMessage(msg)
	}
}

func (cn *ClusterNode) handleGossipMessage(msg *gossip.Message) error {
	// First let the protocol handle system messages
	switch msg.Type {
	case gossip.MessageTypePing, gossip.MessageTypePong, gossip.MessageTypeSync, gossip.MessageTypeSyncRequest:
		logging.Warn("[%s] Unexpected %s message in cluster handler", cn.localNode.ID, msg.Type)
		return nil
	case gossip.MessageTypePut:
		return cn.handlePutMessage(msg)
	case gossip.MessageTypeAck:
		return cn.handleAckMessage(msg)
	}
	return nil
}

func (cn *ClusterNode) handlePutMessage(msg *gossip.Message) error {
	if err := cn.validateWrite(msg.Key, msg.Data); err != nil {
		return err
	}
	ttl := cn.clampTTL(int64(msg.TTL))
	// Forward the accepted TTL to gossip peers and attached children. Do not
	// mutate the caller's message, which may also be used by another consumer.
	accepted := *msg
	accepted.TTL = int(ttl / time.Second)
	msg = &accepted

	// Dedup: if we've already processed this message, skip it.
	// MarkSeen returns true if it was already seen.
	if cn.protocol.MarkSeen(msg.MessageID) {
		logging.Debug("[%s] Skipping duplicate PUT for key %s (msg %s)", cn.localNode.ID, msg.Key, msg.MessageID)
		return nil
	}

	logging.Debug("[%s] Received PUT message for key %s from %s", cn.localNode.ID, msg.Key, msg.From)
	if err := cn.store.Put(msg.Key, msg.Data, ttl); err != nil {
		return fmt.Errorf("failed to store replicated data: %w", err)
	}
	logging.Debug("[%s] Successfully stored replicated data for key %s", cn.localNode.ID, msg.Key)

	// Send ACK directly to the originator
	ack := &gossip.Message{
		Type:      gossip.MessageTypeAck,
		From:      cn.localNode.ID,
		To:        msg.From,
		Key:       msg.Key,
		MessageID: msg.MessageID,
		Timestamp: time.Now(),
	}

	cn.writesMutex.RLock()
	peers := cn.protocol.GetPeers()
	cn.writesMutex.RUnlock()

	delivered := false
	for _, peer := range peers {
		if peer.ID == msg.From {
			logging.Debug("[%s] Sending ACK for key %s to %s", cn.localNode.ID, msg.Key, peer.ID)
			cn.protocol.Send(context.Background(), peer, ack)
			delivered = true
			break
		}
	}
	// If the originator isn't in the HTTP peer list, it may be a transient
	// child attached via WS. The substrate's AckRouter looks up the
	// originating connection by messageId and writes the ACK to that pipe.
	if !delivered && cn.ackRouter != nil {
		if cn.ackRouter.RouteAck(ack) {
			logging.Debug("[%s] Routed ACK for key %s back through WS attachment", cn.localNode.ID, msg.Key)
		}
	}

	// Continue epidemic forwarding to other enclave peers
	cn.protocol.ForwardToEnclave(context.Background(), msg)

	// Fan the stored replica out to attached transient children so they
	// see other agents' writes in their local store. The broadcaster
	// gates on enclave; cross-enclave traffic is dropped.
	if cn.childBroadcaster != nil {
		cn.childBroadcaster.BroadcastToChildren(msg)
	}

	return nil
}

func (cn *ClusterNode) handleAckMessage(msg *gossip.Message) error {
	cn.writesMutex.Lock()
	writeOp, exists := cn.pendingWrites[msg.MessageID]
	cn.writesMutex.Unlock()

	if exists {
		cn.writesMutex.Lock()
		writeOp.Confirmations++
		reached := writeOp.Confirmations >= cn.quorumSize()
		cn.writesMutex.Unlock()
		if reached {
			writeOp.markComplete()
		}
		return nil
	}

	// Not our pending write — possibly an ACK for a PUT we relayed on
	// behalf of a transient child. The substrate's AckRouter table maps
	// messageId → originating WS pipe; if we relayed this message, forward
	// the ACK upstream so the transient's quorum tally advances.
	if cn.ackRouter != nil {
		cn.ackRouter.RouteAck(msg)
	}

	return nil
}

func (cn *ClusterNode) Scan() []string {
	return cn.store.Scan()
}

// ScanPage pages through live keys in lexicographic order: at most limit
// keys that share prefix and sort strictly after cursor. Delegates to the
// store's ordered index so request cost tracks the page size (#220).
func (cn *ClusterNode) ScanPage(prefix, cursor string, limit int) []string {
	return cn.store.ScanPage(prefix, cursor, limit)
}

func (cn *ClusterNode) HandleBootstrap(req *gossip.BootstrapRequest) *gossip.BootstrapResponse {
	return cn.protocol.HandleBootstrap(req)
}

// quorumSize calculates the current quorum based on enclave peer count.
// Quorum = (min(enclaveNodes, replicationFactor) / 2) + 1
// where enclaveNodes includes the local node.
func (cn *ClusterNode) quorumSize() int {
	enclaveNodes := len(cn.protocol.GetReplicationPeers()) + 1 // +1 for self
	effective := enclaveNodes
	if cn.replicationFactor < effective {
		effective = cn.replicationFactor
	}
	q := (effective / 2) + 1
	if q < 1 {
		q = 1
	}
	return q
}

// ClusterSecret returns the configured cluster secret (empty string if open mode).
func (cn *ClusterNode) ClusterSecret() string {
	return cn.clusterSecret
}

// Enclave returns this node's enclave name.
func (cn *ClusterNode) Enclave() string {
	return cn.localNode.Enclave
}

// WriteTimeout returns the configured quorum write timeout. Used by
// WS-attached substrates to size the ACK-route eviction window so the
// route is preserved at least as long as the originator waits for ACKs.
func (cn *ClusterNode) WriteTimeout() time.Duration {
	return cn.writeTimeout
}

// Topology returns the full peer list with enclave membership.
func (cn *ClusterNode) Topology() []*gossip.Node {
	return cn.protocol.GetPeers()
}
