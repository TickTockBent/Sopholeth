package gossip

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
)

// mockTransport is a test transport that can be configured to fail sends.
type mockTransport struct {
	mu             sync.Mutex
	failFor        map[NodeID]bool // peers whose sends should fail
	sendCount      map[NodeID]int
	sentMessages   []*sentMessage // ordered log of all sent messages
	messageHandler func(*Message) error
}

type sentMessage struct {
	To  NodeID
	Msg *Message
}

func newMockTransport() *mockTransport {
	return &mockTransport{
		failFor:   make(map[NodeID]bool),
		sendCount: make(map[NodeID]int),
	}
}

func (t *mockTransport) Start(ctx context.Context) error { return nil }
func (t *mockTransport) Stop() error                     { return nil }

func (t *mockTransport) Send(ctx context.Context, node *Node, msg *Message) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sendCount[node.ID]++
	t.sentMessages = append(t.sentMessages, &sentMessage{To: node.ID, Msg: msg})
	if t.failFor[node.ID] {
		return fmt.Errorf("connection refused")
	}
	return nil
}

func (t *mockTransport) getSentMessages() []*sentMessage {
	t.mu.Lock()
	defer t.mu.Unlock()
	cp := make([]*sentMessage, len(t.sentMessages))
	copy(cp, t.sentMessages)
	return cp
}

func (t *mockTransport) SetMessageHandler(handler func(*Message) error) {
	t.messageHandler = handler
}

func (t *mockTransport) setFail(id NodeID, fail bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.failFor[id] = fail
}

func (t *mockTransport) getSendCount(id NodeID) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sendCount[id]
}

func newTestProtocol() (*Protocol, *mockTransport) {
	localNode := &Node{
		ID:       "local",
		Address:  "localhost",
		Port:     9090,
		HTTPPort: 8080,
		Enclave:  "default",
	}
	p := NewProtocol(localNode, 3, "")
	mt := newMockTransport()
	p.SetTransport(mt)
	return p, mt
}

func TestPeerEvictionAfterConsecutiveFailures(t *testing.T) {
	p, mt := newTestProtocol()

	deadPeer := &Node{ID: "dead-peer", Address: "dead", Port: 9090, HTTPPort: 8080, Enclave: "default"}
	p.addPeer(deadPeer)
	mt.setFail("dead-peer", true)

	// Verify peer exists
	if len(p.GetPeers()) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(p.GetPeers()))
	}

	// Ping should fail but not evict yet (failures < MaxPingFailures)
	for i := 1; i < MaxPingFailures; i++ {
		p.pingPeers(context.Background())
		if count := p.PeerFailureCount("dead-peer"); count != i {
			t.Fatalf("after %d pings, failure count = %d, want %d", i, count, i)
		}
		if len(p.GetPeers()) != 1 {
			t.Fatalf("peer should not be evicted after %d failures", i)
		}
	}

	// One more ping should trigger eviction
	p.pingPeers(context.Background())
	if len(p.GetPeers()) != 0 {
		t.Fatal("peer should be evicted after MaxPingFailures consecutive failures")
	}
}

func TestPongResetsPingFailures(t *testing.T) {
	p, mt := newTestProtocol()

	flakyPeer := &Node{ID: "flaky", Address: "flaky", Port: 9090, HTTPPort: 8080, Enclave: "default"}
	p.addPeer(flakyPeer)
	mt.setFail("flaky", true)

	// Accumulate failures just under the threshold
	for i := 0; i < MaxPingFailures-1; i++ {
		p.pingPeers(context.Background())
	}

	if count := p.PeerFailureCount("flaky"); count != MaxPingFailures-1 {
		t.Fatalf("failure count = %d, want %d", count, MaxPingFailures-1)
	}

	// Simulate receiving a PONG — peer recovered
	pong := &Message{
		Type: MessageTypePong,
		From: "flaky",
		NodeInfo: &Node{
			ID:      "flaky",
			Enclave: "default",
		},
	}
	if err := p.handlePong(pong); err != nil {
		t.Fatalf("handlePong error: %v", err)
	}

	// Failure count should be reset
	if count := p.PeerFailureCount("flaky"); count != 0 {
		t.Fatalf("failure count after PONG = %d, want 0", count)
	}

	// Peer should still exist
	if len(p.GetPeers()) != 1 {
		t.Fatal("peer should not be evicted after PONG reset")
	}
}

func TestEvictedPeerRejoinsViaBootstrap(t *testing.T) {
	p, mt := newTestProtocol()

	peer := &Node{ID: "rejoiner", Address: "rejoiner", Port: 9090, HTTPPort: 8080, Enclave: "default"}
	p.addPeer(peer)
	mt.setFail("rejoiner", true)

	// Evict via ping failures
	for i := 0; i < MaxPingFailures; i++ {
		p.pingPeers(context.Background())
	}
	if len(p.GetPeers()) != 0 {
		t.Fatal("peer should be evicted")
	}

	// Simulate peer coming back via addPeer (called during bootstrap)
	mt.setFail("rejoiner", false)
	p.addPeer(peer)

	if len(p.GetPeers()) != 1 {
		t.Fatal("peer should be re-added after bootstrap")
	}
	if count := p.PeerFailureCount("rejoiner"); count != 0 {
		t.Fatalf("failure count after re-add = %d, want 0", count)
	}
}

func TestHealthyPeerNotEvicted(t *testing.T) {
	p, _ := newTestProtocol()

	healthyPeer := &Node{ID: "healthy", Address: "healthy", Port: 9090, HTTPPort: 8080, Enclave: "default"}
	p.addPeer(healthyPeer)

	// Ping many times — all succeed
	for i := 0; i < MaxPingFailures*3; i++ {
		p.pingPeers(context.Background())
	}

	if len(p.GetPeers()) != 1 {
		t.Fatal("healthy peer should not be evicted")
	}
	if count := p.PeerFailureCount("healthy"); count != 0 {
		t.Fatalf("failure count for healthy peer = %d, want 0", count)
	}
}

func TestMixedPeerEviction(t *testing.T) {
	p, mt := newTestProtocol()

	alive := &Node{ID: "alive", Address: "alive", Port: 9090, HTTPPort: 8080, Enclave: "default"}
	dead := &Node{ID: "dead", Address: "dead", Port: 9090, HTTPPort: 8080, Enclave: "default"}
	p.addPeer(alive)
	p.addPeer(dead)
	mt.setFail("dead", true)

	for i := 0; i < MaxPingFailures; i++ {
		p.pingPeers(context.Background())
	}

	peers := p.GetPeers()
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer remaining, got %d", len(peers))
	}
	if peers[0].ID != "alive" {
		t.Fatalf("expected 'alive' peer to remain, got %s", peers[0].ID)
	}
}

// counterValue reads the current value from a Prometheus counter.
func counterValue(c interface{ Write(*dto.Metric) error }) float64 {
	var m dto.Metric
	c.Write(&m)
	return m.GetCounter().GetValue()
}

// gaugeValue reads the current value from a Prometheus gauge.
func gaugeValue(g interface{ Write(*dto.Metric) error }) float64 {
	var m dto.Metric
	g.Write(&m)
	return m.GetGauge().GetValue()
}

// --- Topology Sync Tests ---

func TestTopologySyncConvergesWithoutReplyLoop(t *testing.T) {
	// Two RF=3 nodes remain under-peered, as after losing a third root.
	// Deliver every outgoing message back through the other protocol so a
	// response that accidentally requests another response cannot go unnoticed.
	nodeA := &Node{ID: "node-a", Address: "a", HTTPPort: 8080, Enclave: "default"}
	nodeB := &Node{ID: "node-b", Address: "b", HTTPPort: 8080, Enclave: "default"}
	protocols := map[NodeID]*Protocol{}
	transports := map[NodeID]*mockTransport{}
	for _, node := range []*Node{nodeA, nodeB} {
		p := NewProtocol(node, 3, "")
		mt := newMockTransport()
		p.SetTransport(mt)
		protocols[node.ID], transports[node.ID] = p, mt
	}
	protocols[nodeA.ID].addPeer(nodeB)
	protocols[nodeB.ID].addPeer(nodeA)

	delivered := map[NodeID]int{}
	drain := func() int {
		t.Helper()
		count := 0
		for {
			progress := false
			for id, mt := range transports {
				for _, sent := range mt.getSentMessages()[delivered[id]:] {
					if count == 16 {
						t.Fatal("SYNC exchange did not terminate within 16 messages")
					}
					delivered[id]++
					count++
					progress = true
					if err := protocols[sent.To].HandleMessage(sent.Msg); err != nil {
						t.Fatal(err)
					}
				}
			}
			if !progress {
				return count
			}
		}
	}

	// Each tick is one request and one terminal self-announcement. A later
	// tick must still work: suppressing all subsequent discovery is no fix.
	for _, id := range []NodeID{nodeA.ID, nodeB.ID, nodeA.ID} {
		protocols[id].performTopologySync(context.Background())
		if count := drain(); count != 2 {
			t.Fatalf("two-node SYNC exchanged %d messages, want 2", count)
		}
	}

	// B learns C; A's next request must discover C as well as B itself.
	nodeC := &Node{ID: "node-c", Address: "c", HTTPPort: 8080, Enclave: "default"}
	protocols[nodeB.ID].addPeer(nodeC)
	protocols[nodeA.ID].performTopologySync(context.Background())
	if count := drain(); count != 3 {
		t.Fatalf("peer discovery exchanged %d messages, want 3", count)
	}
	peers := protocols[nodeA.ID].GetPeers()
	if len(peers) != 2 {
		t.Fatalf("A has %d peers, want B and C", len(peers))
	}
	for _, peer := range peers {
		if peer.ID != nodeB.ID && peer.ID != nodeC.ID {
			t.Fatalf("A learned unexpected peer %s", peer.ID)
		}
	}
	protocols[nodeA.ID].performTopologySync(context.Background())
	if count := drain(); count != 0 {
		t.Fatalf("fully peered node sent %d messages, want none", count)
	}
}

func TestSyncDoesNotAmplifyPropagatedInfo(t *testing.T) {
	// When B receives a SYNC from A that contains C's info (propagated,
	// not direct), B should NOT respond with its full peer list.
	// This prevents amplification loops.
	localB := &Node{ID: "node-b", Address: "b", Port: 9090, HTTPPort: 8080, Enclave: "default"}
	protocolB := NewProtocol(localB, 3, "")
	mtB := newMockTransport()
	protocolB.SetTransport(mtB)

	nodeA := &Node{ID: "node-a", Address: "a", Port: 9090, HTTPPort: 8080, Enclave: "default"}
	protocolB.addPeer(nodeA)

	// A sends a SYNC about C (propagated info — From != NodeInfo.ID)
	nodeC := &Node{ID: "node-c", Address: "c", Port: 9090, HTTPPort: 8080, Enclave: "default"}
	syncMsg := &Message{
		Type:      MessageTypeSync,
		From:      "node-a", // Sender is A
		Timestamp: time.Now(),
		MessageID: "sync-2",
		NodeInfo:  nodeC, // But info is about C — propagated, not direct
	}

	if err := protocolB.handleSync(syncMsg); err != nil {
		t.Fatalf("handleSync error: %v", err)
	}

	// B should have added C as a peer
	if len(protocolB.GetPeers()) != 2 { // A + C
		t.Fatalf("expected 2 peers, got %d", len(protocolB.GetPeers()))
	}

	// B should NOT have sent any SYNC responses (no amplification)
	sent := mtB.getSentMessages()
	for _, sm := range sent {
		if sm.Msg.Type == MessageTypeSync {
			t.Errorf("B should not send SYNC responses for propagated info, but sent to %s about %s",
				sm.To, sm.Msg.NodeInfo.ID)
		}
	}
}

func TestSyncSkipsSelf(t *testing.T) {
	// If a SYNC arrives with our own NodeInfo, we should ignore it
	// (don't add ourselves as a peer).
	p, _ := newTestProtocol() // local node ID is "local"

	syncMsg := &Message{
		Type:      MessageTypeSync,
		From:      "some-peer",
		Timestamp: time.Now(),
		MessageID: "sync-3",
		NodeInfo:  &Node{ID: "local", Address: "localhost", Port: 9090, HTTPPort: 8080, Enclave: "default"},
	}

	if err := p.handleSync(syncMsg); err != nil {
		t.Fatalf("handleSync error: %v", err)
	}

	if len(p.GetPeers()) != 0 {
		t.Fatal("should not add ourselves as a peer via SYNC")
	}
}

// --- Gossip Fanout Tests ---

func TestFanoutSize(t *testing.T) {
	tests := []struct {
		peerCount int
		expected  int
	}{
		{0, 0},
		{1, 1},
		{4, 2},   // √4 = 2
		{9, 3},   // √9 = 3
		{10, 4},  // √10 ≈ 3.16 → ceil = 4
		{25, 5},  // √25 = 5
		{100, 10}, // √100 = 10
		{50, 8},  // √50 ≈ 7.07 → ceil = 8
	}
	for _, tc := range tests {
		got := fanoutSize(tc.peerCount)
		if got != tc.expected {
			t.Errorf("fanoutSize(%d) = %d, want %d", tc.peerCount, got, tc.expected)
		}
	}
}

func TestSelectRandomPeers(t *testing.T) {
	peers := make([]*Node, 10)
	for i := 0; i < 10; i++ {
		peers[i] = &Node{ID: NodeID(fmt.Sprintf("peer-%d", i))}
	}

	// Select fewer than available — should get exactly n
	selected := selectRandomPeers(peers, 3, "")
	if len(selected) != 3 {
		t.Fatalf("expected 3 peers, got %d", len(selected))
	}

	// Select more than available — should get all
	selected = selectRandomPeers(peers, 20, "")
	if len(selected) != 10 {
		t.Fatalf("expected 10 peers (all), got %d", len(selected))
	}

	// Skip a specific peer
	selected = selectRandomPeers(peers, 20, "peer-5")
	if len(selected) != 9 {
		t.Fatalf("expected 9 peers (skipping peer-5), got %d", len(selected))
	}
	for _, p := range selected {
		if p.ID == "peer-5" {
			t.Fatal("peer-5 should have been skipped")
		}
	}
}

func TestMarkSeenDedup(t *testing.T) {
	p, _ := newTestProtocol()

	// First call: message is new
	if seen := p.MarkSeen("msg-1"); seen {
		t.Fatal("first MarkSeen should return false (new message)")
	}

	// Second call: message is duplicate
	if seen := p.MarkSeen("msg-1"); !seen {
		t.Fatal("second MarkSeen should return true (duplicate)")
	}

	// Different message: new
	if seen := p.MarkSeen("msg-2"); seen {
		t.Fatal("different message should return false (new)")
	}
}

func TestSeenMessagesCacheBounded(t *testing.T) {
	p, _ := newTestProtocol()

	// Fill the cache to capacity
	for i := 0; i < maxSeenMessages; i++ {
		p.MarkSeen(fmt.Sprintf("msg-%d", i))
	}

	// Verify cache is at capacity
	p.seenMutex.Lock()
	sizeAtCap := len(p.seenMessages)
	p.seenMutex.Unlock()
	if sizeAtCap != maxSeenMessages {
		t.Fatalf("cache size = %d, want %d", sizeAtCap, maxSeenMessages)
	}

	// Add one more — should trigger eviction and stay bounded
	p.MarkSeen("overflow-msg")

	p.seenMutex.Lock()
	sizeAfter := len(p.seenMessages)
	p.seenMutex.Unlock()
	if sizeAfter >= maxSeenMessages {
		t.Fatalf("cache size after eviction = %d, should be < %d", sizeAfter, maxSeenMessages)
	}

	// The new message should still be present
	if !p.MarkSeen("overflow-msg") {
		t.Fatal("overflow-msg should be in the cache after eviction")
	}
}

func TestBroadcastToEnclaveFullBroadcastBelowThreshold(t *testing.T) {
	p, mt := newTestProtocol()

	// Add fewer peers than FanoutThreshold — all in same enclave
	for i := 0; i < FanoutThreshold-1; i++ {
		peer := &Node{
			ID:      NodeID(fmt.Sprintf("peer-%d", i)),
			Address: fmt.Sprintf("peer-%d", i),
			Port:    9090,
			HTTPPort: 8080,
			Enclave: "default",
		}
		p.addPeer(peer)
	}

	msg := &Message{
		Type:      MessageTypePut,
		From:      p.localNode.ID,
		Key:       "test-key",
		MessageID: "test-msg-1",
	}

	if err := p.BroadcastToEnclave(msg); err != nil {
		t.Fatalf("BroadcastToEnclave error: %v", err)
	}
	waitForSendsIdle(t, p)

	// Every peer should have received exactly one send
	for i := 0; i < FanoutThreshold-1; i++ {
		id := NodeID(fmt.Sprintf("peer-%d", i))
		count := mt.getSendCount(id)
		if count != 1 {
			t.Errorf("peer %s received %d sends, want 1", id, count)
		}
	}
}

func TestBroadcastToEnclaveFanoutAboveThreshold(t *testing.T) {
	p, mt := newTestProtocol()

	peerCount := FanoutThreshold + 5 // 15 peers
	for i := 0; i < peerCount; i++ {
		peer := &Node{
			ID:      NodeID(fmt.Sprintf("peer-%d", i)),
			Address: fmt.Sprintf("peer-%d", i),
			Port:    9090,
			HTTPPort: 8080,
			Enclave: "default",
		}
		p.addPeer(peer)
	}

	msg := &Message{
		Type:      MessageTypePut,
		From:      p.localNode.ID,
		Key:       "test-key",
		MessageID: "test-msg-2",
	}

	if err := p.BroadcastToEnclave(msg); err != nil {
		t.Fatalf("BroadcastToEnclave error: %v", err)
	}
	waitForSendsIdle(t, p)

	// Count total sends — should be √15 ≈ 4 (ceil), not 15
	totalSends := 0
	for i := 0; i < peerCount; i++ {
		id := NodeID(fmt.Sprintf("peer-%d", i))
		totalSends += mt.getSendCount(id)
	}

	expectedFanout := fanoutSize(peerCount)
	if totalSends != expectedFanout {
		t.Fatalf("total sends = %d, want %d (fanout of %d peers)", totalSends, expectedFanout, peerCount)
	}
}

func TestForwardToEnclaveSkipsBelowThreshold(t *testing.T) {
	p, mt := newTestProtocol()

	// Add fewer peers than threshold
	for i := 0; i < 3; i++ {
		peer := &Node{
			ID:      NodeID(fmt.Sprintf("peer-%d", i)),
			Address: fmt.Sprintf("peer-%d", i),
			Port:    9090,
			HTTPPort: 8080,
			Enclave: "default",
		}
		p.addPeer(peer)
	}

	msg := &Message{
		Type:      MessageTypePut,
		From:      "some-sender",
		Key:       "test-key",
		MessageID: "test-msg-3",
	}

	p.ForwardToEnclave(msg)
	waitForSendsIdle(t, p)

	// No sends — originator already sent to all peers directly
	for i := 0; i < 3; i++ {
		id := NodeID(fmt.Sprintf("peer-%d", i))
		if count := mt.getSendCount(id); count != 0 {
			t.Errorf("peer %s received %d sends, want 0 (below threshold, no forwarding)", id, count)
		}
	}
}

func TestForwardToEnclaveAboveThreshold(t *testing.T) {
	p, mt := newTestProtocol()

	peerCount := FanoutThreshold + 5
	for i := 0; i < peerCount; i++ {
		peer := &Node{
			ID:      NodeID(fmt.Sprintf("peer-%d", i)),
			Address: fmt.Sprintf("peer-%d", i),
			Port:    9090,
			HTTPPort: 8080,
			Enclave: "default",
		}
		p.addPeer(peer)
	}

	msg := &Message{
		Type:      MessageTypePut,
		From:      "peer-0", // sender is one of the peers
		Key:       "test-key",
		MessageID: "test-msg-4",
	}

	p.ForwardToEnclave(msg)
	waitForSendsIdle(t, p)

	// Should have sent to √N peers, excluding the sender (peer-0)
	totalSends := 0
	for i := 0; i < peerCount; i++ {
		id := NodeID(fmt.Sprintf("peer-%d", i))
		totalSends += mt.getSendCount(id)
	}

	// peer-0 (the sender) should not have received a forward
	if count := mt.getSendCount("peer-0"); count != 0 {
		t.Errorf("sender peer-0 received %d forwards, want 0", count)
	}

	expectedFanout := fanoutSize(peerCount)
	if totalSends != expectedFanout {
		t.Fatalf("total forwards = %d, want %d (fanout of %d peers, excluding sender)", totalSends, expectedFanout, peerCount)
	}
}

func TestBroadcastToEnclaveOnlySameEnclave(t *testing.T) {
	p, mt := newTestProtocol() // local node is in "default" enclave

	// Add peers in different enclaves
	sameEnclave := &Node{ID: "same", Address: "same", Port: 9090, HTTPPort: 8080, Enclave: "default"}
	diffEnclave := &Node{ID: "diff", Address: "diff", Port: 9090, HTTPPort: 8080, Enclave: "other"}
	p.addPeer(sameEnclave)
	p.addPeer(diffEnclave)

	msg := &Message{
		Type:      MessageTypePut,
		From:      p.localNode.ID,
		Key:       "enclave-test",
		MessageID: "test-msg-5",
	}

	p.BroadcastToEnclave(msg)
	waitForSendsIdle(t, p)

	// Same enclave peer should receive the message
	if count := mt.getSendCount("same"); count != 1 {
		t.Errorf("same-enclave peer received %d sends, want 1", count)
	}
	// Different enclave peer should NOT receive the message
	if count := mt.getSendCount("diff"); count != 0 {
		t.Errorf("different-enclave peer received %d sends, want 0", count)
	}
}

func TestEvictionMetrics(t *testing.T) {
	p, mt := newTestProtocol()
	p.EnableMetrics()

	peer := &Node{ID: "doomed", Address: "doomed", Port: 9090, HTTPPort: 8080, Enclave: "default"}
	p.addPeer(peer)
	mt.setFail("doomed", true)

	// Verify active peers gauge
	if v := gaugeValue(p.metrics.peersActive); v != 1 {
		t.Fatalf("peersActive after addPeer = %v, want 1", v)
	}

	evictionsBefore := counterValue(p.metrics.peerEvictions)
	pingFailuresBefore := counterValue(p.metrics.pingFailures)

	// Evict the peer
	for i := 0; i < MaxPingFailures; i++ {
		p.pingPeers(context.Background())
	}

	// Check eviction counter incremented
	evictionsAfter := counterValue(p.metrics.peerEvictions)
	if evictionsAfter-evictionsBefore != 1 {
		t.Fatalf("peerEvictions delta = %v, want 1", evictionsAfter-evictionsBefore)
	}

	// Check ping failures counter incremented (MaxPingFailures times)
	pingFailuresAfter := counterValue(p.metrics.pingFailures)
	if pingFailuresAfter-pingFailuresBefore != float64(MaxPingFailures) {
		t.Fatalf("pingFailures delta = %v, want %d", pingFailuresAfter-pingFailuresBefore, MaxPingFailures)
	}

	// Check active peers gauge is now 0
	if v := gaugeValue(p.metrics.peersActive); v != 0 {
		t.Fatalf("peersActive after eviction = %v, want 0", v)
	}

	// Re-add the peer — should increment joins counter
	joinsBefore := counterValue(p.metrics.peerJoins)
	mt.setFail("doomed", false)
	p.addPeer(peer)

	joinsAfter := counterValue(p.metrics.peerJoins)
	if joinsAfter-joinsBefore != 1 {
		t.Fatalf("peerJoins delta = %v, want 1", joinsAfter-joinsBefore)
	}

	// Active peers gauge should be back to 1
	if v := gaugeValue(p.metrics.peersActive); v != 1 {
		t.Fatalf("peersActive after rejoin = %v, want 1", v)
	}
}

// IsReplicationPeer matches GetReplicationPeers: same-enclave peers in the
// table, not other enclaves, unknown IDs, the local node, or evicted peers.
func TestIsReplicationPeer(t *testing.T) {
	p, mt := newTestProtocol() // local node is in "default" enclave
	p.addPeer(&Node{ID: "same", Address: "same", Port: 9090, HTTPPort: 8080, Enclave: "default"})
	p.addPeer(&Node{ID: "diff", Address: "diff", Port: 9090, HTTPPort: 8080, Enclave: "other"})

	for id, want := range map[NodeID]bool{"same": true, "diff": false, "unknown": false, p.localNode.ID: false} {
		if got := p.IsReplicationPeer(id); got != want {
			t.Errorf("IsReplicationPeer(%q) = %v, want %v", id, got, want)
		}
	}

	mt.setFail("same", true)
	for i := 0; i < MaxPingFailures; i++ {
		p.pingPeers(context.Background())
	}
	if p.IsReplicationPeer("same") {
		t.Error("evicted peer still reported as a replication peer")
	}
}
