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

	// childBroadcaster, when set, receives every new valid relayed PUT,
	// whether or not the local store kept it (#163), so it can fan the
	// replica out to attached transients (#135). Nil outside substrate mode.
	childBroadcaster ChildBroadcaster

	// relayMetrics is nil until Start enables Prometheus metrics.
	relayMetrics *relayMetrics

	// clock stamps originated messages. It is time.Now outside tests; tests
	// freeze it to show that message IDs do not depend on the clock (#166).
	clock func() time.Time

	// isReplicationPeer reports whether a node ID is currently a same-enclave
	// peer. It is protocol.IsReplicationPeer outside tests; tests replace it
	// to evict a peer mid-write without waiting out health checks (#164).
	isReplicationPeer func(gossip.NodeID) bool
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
	Key  string
	Data []byte
	TTL  time.Duration

	// The fields below are replication bookkeeping for one write (#164).
	// ClusterNode.writesMutex guards them.

	// quorumTarget is fixed when Put starts. A peer joining or leaving
	// mid-write does not move it.
	quorumTarget int
	// countedSenders holds the node IDs whose stored copy has been counted,
	// starting with the local node for the local store. Its size is the
	// confirmation count, so one sender is counted at most once.
	countedSenders map[gossip.NodeID]struct{}
	// replicationPeersAtStart is the set of same-enclave peer IDs in the
	// peer table when Put started, so a peer evicted mid-write still counts.
	replicationPeersAtStart map[gossip.NodeID]struct{}
	// completed is set, and the write removed from pendingWrites, in the
	// same critical section that counts the ACK reaching quorumTarget.
	completed bool

	// Complete is closed once, by markComplete, when quorum is reached.
	Complete   chan bool
	signalOnce sync.Once
	Error      error
}

// markComplete signals the write as quorum-reached at most once.
func (w *WriteOperation) markComplete() {
	w.signalOnce.Do(func() { close(w.Complete) })
}

// confirmations returns how many distinct senders, the local node included,
// have been counted. Call it with writesMutex held.
func (w *WriteOperation) confirmations() int {
	return len(w.countedSenders)
}

type Store interface {
	Put(key string, data []byte, ttl time.Duration) error
	Get(key string) ([]byte, bool)
	GetWithMetadata(key string) ([]byte, time.Time, time.Duration, bool) // data, createdAt, originalTTL, exists
	Scan() []string
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
		clock:             time.Now,
		isReplicationPeer: protocol.IsReplicationPeer,
	}
}

func (cn *ClusterNode) Start(ctx context.Context, bootstrapAddresses []string) error {
	transport := gossip.NewHTTPTransport(cn.localNode, cn.clusterSecret)
	if err := transport.SetHTTPClient(cn.httpClient); err != nil {
		return fmt.Errorf("gossip HTTP client: %w", err)
	}
	cn.protocol.SetTransport(transport)
	cn.protocol.SetMessageHandler(cn.handleGossipMessage)
	cn.protocol.EnableMetrics()
	cn.relayMetrics = newRelayMetrics()

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
// and the ordinary private/explicit bootstrap behavior. It returns an error,
// and changes nothing, when client disables certificate or hostname
// verification or uses a custom TLS dialer. The same client later backs gossip
// sends; Start verifies that copy once more before any send.
func (cn *ClusterNode) ConfigureBootstrap(client *http.Client, check func(string, []*gossip.Node) error) error {
	if err := cn.protocol.ConfigureBootstrap(client, check); err != nil {
		return err
	}
	cn.httpClient = client
	return nil
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

	// One snapshot of the replication peers fixes this write's quorum target
	// and the peers whose ACKs count even if they are evicted mid-write
	// (#164). Neither moves while the write is pending.
	replicationPeersAtStart := cn.protocol.GetReplicationPeers()
	quorumTarget := quorumForEnclaveSize(len(replicationPeersAtStart)+1, cn.replicationFactor)
	replicationPeerIDsAtStart := make(map[gossip.NodeID]struct{}, len(replicationPeersAtStart))
	for _, peer := range replicationPeersAtStart {
		replicationPeerIDsAtStart[peer.ID] = struct{}{}
	}

	msg := &gossip.Message{
		Type:      gossip.MessageTypePut,
		From:      cn.localNode.ID,
		Key:       key,
		Data:      data,
		TTL:       int(ttl.Seconds()),
		Timestamp: cn.clock(),
		// Opaque and random, never derived from the key or the clock: two
		// writes to one key in one clock tick, on this node or on two nodes,
		// must not share an ID, or peers drop one as a duplicate and ACKs
		// for one count toward the other (#166).
		MessageID: gossip.NewMessageID(),
	}

	writeOp := &WriteOperation{
		Key:          key,
		Data:         data,
		TTL:          ttl,
		quorumTarget: quorumTarget,
		// The local store is the first confirmation, under this node's ID.
		countedSenders:          map[gossip.NodeID]struct{}{cn.localNode.ID: {}},
		replicationPeersAtStart: replicationPeerIDsAtStart,
		Complete:                make(chan bool, 1),
	}

	// Key on MessageID so concurrent writes to the same key don't
	// collide — each write tracks its own quorum independently.
	cn.writesMutex.Lock()
	cn.pendingWrites[msg.MessageID] = writeOp
	cn.writesMutex.Unlock()

	if err := cn.store.Put(key, data, ttl); err != nil {
		cn.removePendingWrite(msg.MessageID, writeOp)
		return fmt.Errorf("local write failed: %w", err)
	}

	// Every accepted write is offered to the enclave, whatever the quorum:
	// quorum only governs what the client is told (#215). The broadcast only
	// queues sends, so the client's answer depends on quorum or the write
	// timeout, never on how fast each peer responds (#212). Delivery is not
	// tied to ctx, so a 202 does not cancel sends to the remaining peers.
	logging.Debug("[%s] Broadcasting PUT for key %s to enclave peers", cn.localNode.ID, key)
	if err := cn.protocol.BroadcastToEnclave(msg); err != nil {
		logging.Warn("[%s] Failed to broadcast write to enclave: %v", cn.localNode.ID, err)
	}

	// The local write is the first confirmation; a quorum of one is met now
	// (single-node enclave, or RF=1).
	if quorumTarget <= 1 {
		cn.removePendingWrite(msg.MessageID, writeOp)
		writeOp.markComplete()
		logging.Debug("Write completed locally (quorum=%d)", quorumTarget)
		return nil
	}

	select {
	case <-writeOp.Complete:
		// countAckLocked removed the write when it counted the last ACK.
		cn.writesMutex.RLock()
		err := writeOp.Error
		cn.writesMutex.RUnlock()
		return err
	case <-time.After(cn.writeTimeout):
		if cn.removePendingWrite(msg.MessageID, writeOp) {
			return nil // the last ACK was counted as the timeout fired
		}
		return ErrQuorumTimeout
	case <-ctx.Done():
		if cn.removePendingWrite(msg.MessageID, writeOp) {
			return nil
		}
		return ctx.Err()
	}
}

// removePendingWrite removes writeOp from pendingWrites under the lock that
// counts ACKs, so each ACK either was counted before the removal or finds the
// write gone and is ignored. It reports whether the write had already reached
// quorum.
func (cn *ClusterNode) removePendingWrite(messageID string, writeOp *WriteOperation) bool {
	cn.writesMutex.Lock()
	defer cn.writesMutex.Unlock()
	if cn.pendingWrites[messageID] == writeOp {
		delete(cn.pendingWrites, messageID)
	}
	return writeOp.completed
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

	// Relaying does not depend on holding a copy (#163). The message is
	// already marked seen, so this node is its only handler on this branch of
	// the epidemic tree: skipping the forwards when the store refuses the
	// value would cut off every downstream peer, including peers with room.
	// Only the ACK depends on the store, because an ACK tells the originator
	// "I hold a copy" and counts toward its quorum.
	storeErr := cn.store.Put(msg.Key, msg.Data, ttl)
	if storeErr != nil {
		cn.relayMetrics.countStoreFailure()
		logging.Debug("[%s] Could not store replicated key %s (%v); relaying without ACK", cn.localNode.ID, msg.Key, storeErr)
	} else {
		logging.Debug("[%s] Successfully stored replicated data for key %s", cn.localNode.ID, msg.Key)
		cn.queueAck(msg)
	}

	// Continue epidemic forwarding to other enclave peers. Like the ACK, the
	// forwards are queued, not sent inline, so this handler returns to the
	// upstream sender without waiting on any peer (#167).
	cn.protocol.ForwardToEnclave(msg)

	// Fan the replica out to attached transient children so they see other
	// agents' writes in their local store. The broadcaster gates on enclave;
	// cross-enclave traffic is dropped.
	if cn.childBroadcaster != nil {
		cn.childBroadcaster.BroadcastToChildren(msg)
	}

	// Report the store failure only after relaying, so the upstream sender's
	// log and the gossip handler's status still say this node holds no copy.
	if storeErr != nil {
		return fmt.Errorf("failed to store replicated data: %w", storeErr)
	}
	return nil
}

// queueAck queues an ACK for msg to its originator. Call it only after the
// local store accepted the value.
func (cn *ClusterNode) queueAck(msg *gossip.Message) {
	ack := &gossip.Message{
		Type:      gossip.MessageTypeAck,
		From:      cn.localNode.ID,
		To:        msg.From,
		Key:       msg.Key,
		MessageID: msg.MessageID,
		Timestamp: time.Now(),
	}

	// The ACK is queued, not sent inline. Waiting on it here chained per-hop
	// timeouts across the fanout tree and piled up handler goroutines (#167).
	if cn.protocol.QueueToPeer(msg.From, ack) {
		logging.Debug("[%s] Queued ACK for key %s to %s", cn.localNode.ID, msg.Key, msg.From)
		return
	}
	// If the originator isn't in the HTTP peer list, it may be a transient
	// child attached via WS. The substrate's AckRouter looks up the
	// originating connection by messageId and writes the ACK to that pipe.
	if cn.ackRouter != nil && cn.ackRouter.RouteAck(ack) {
		logging.Debug("[%s] Routed ACK for key %s back through WS attachment", cn.localNode.ID, msg.Key)
	}
}

// ackOutcome is what handleAckMessage did with an ACK for a pending write.
type ackOutcome int

const (
	ackCounted            ackOutcome = iota // counted; quorum not yet reached
	ackReachedQuorum                        // counted, and it completed the write
	ackFromSelf                             // claims the local node's ID; never counted
	ackNotReplicationPeer                   // sender is not a same-enclave peer, now or at start
	ackDuplicateSender                      // sender already counted for this write
)

func (outcome ackOutcome) String() string {
	switch outcome {
	case ackCounted:
		return "counted"
	case ackReachedQuorum:
		return "counted, quorum reached"
	case ackFromSelf:
		return "ignored: claims the local node's ID"
	case ackNotReplicationPeer:
		return "ignored: sender is not a replication peer"
	case ackDuplicateSender:
		return "ignored: sender already counted"
	}
	return "unknown"
}

func (cn *ClusterNode) handleAckMessage(msg *gossip.Message) error {
	cn.writesMutex.Lock()
	writeOp, pending := cn.pendingWrites[msg.MessageID]
	if pending {
		outcome := cn.countAckLocked(msg.MessageID, writeOp, msg.From)
		cn.writesMutex.Unlock()
		logging.Debug("[%s] ACK for key %s from %s: %s", cn.localNode.ID, msg.Key, msg.From, outcome)
		return nil
	}
	cn.writesMutex.Unlock()

	// Not a pending write of ours: either a write that already completed or
	// timed out (the ACK is ignored), or a PUT we relayed on behalf of a
	// transient child. The substrate's AckRouter table maps messageId →
	// originating WS pipe; if we relayed this message, forward the ACK
	// upstream so the transient's quorum tally advances.
	if cn.ackRouter != nil {
		cn.ackRouter.RouteAck(msg)
	}

	return nil
}

// countAckLocked counts one ACK toward writeOp's fixed quorum target. Call it
// with writesMutex held: the lookup in handleAckMessage, the sender check, the
// duplicate check, the count, the completion decision and the removal from
// pendingWrites are one critical section, so concurrent ACKs complete a write
// exactly once and an ACK after removal is never counted (#164).
//
// A sender counts once per write, and only if it is a replication peer: in
// this node's peer table and enclave now, or when Put started. Above the
// fanout threshold, relays the origin never sent to ACK it directly, so this
// cannot be limited to the peers Put sent to. This is replication
// bookkeeping from the local peer table, not authentication: without
// NODE_CLUSTER_SECRET a sender ID is an unchecked claim, and a host that
// joins the mesh under several IDs can supply several ACKs. It stops double
// counting and ACKs from IDs this node has no peer record for; it does not
// prove independent copies.
//
// The peer check takes the gossip peer-table read lock inside writesMutex.
// The gossip package never calls back into the cluster under that lock, so
// the order cannot invert.
func (cn *ClusterNode) countAckLocked(messageID string, writeOp *WriteOperation, sender gossip.NodeID) ackOutcome {
	if sender == cn.localNode.ID {
		return ackFromSelf
	}
	if _, peerAtStart := writeOp.replicationPeersAtStart[sender]; !peerAtStart && !cn.isReplicationPeer(sender) {
		return ackNotReplicationPeer
	}
	if _, alreadyCounted := writeOp.countedSenders[sender]; alreadyCounted {
		return ackDuplicateSender
	}
	writeOp.countedSenders[sender] = struct{}{}
	if writeOp.confirmations() < writeOp.quorumTarget {
		return ackCounted
	}
	writeOp.completed = true
	delete(cn.pendingWrites, messageID)
	writeOp.markComplete()
	return ackReachedQuorum
}

func (cn *ClusterNode) Scan() []string {
	return cn.store.Scan()
}

func (cn *ClusterNode) HandleBootstrap(req *gossip.BootstrapRequest) *gossip.BootstrapResponse {
	return cn.protocol.HandleBootstrap(req)
}

// quorumForEnclaveSize returns a write's confirmation threshold:
// (min(enclaveNodes, replicationFactor) / 2) + 1, at least 1, where
// enclaveNodes includes the local node. Put computes it once per write.
func quorumForEnclaveSize(enclaveNodes, replicationFactor int) int {
	effective := enclaveNodes
	if replicationFactor < effective {
		effective = replicationFactor
	}
	quorum := (effective / 2) + 1
	if quorum < 1 {
		quorum = 1
	}
	return quorum
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
