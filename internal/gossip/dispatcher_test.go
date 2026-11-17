package gossip

import (
	"context"
	"fmt"
	"math/rand"
	"runtime"
	"sync"
	"testing"
	"time"
)

// peerBehavior describes how scriptedTransport answers sends to one peer.
type peerBehavior struct {
	dead  bool          // accept the send and never answer; honor ctx only
	delay time.Duration // answer after this long (slow peer)
}

// scriptedTransport records deliveries and simulates dead and slow peers.
// Every send honors its context, like the HTTP transport.
type scriptedTransport struct {
	mu              sync.Mutex
	behaviors       map[NodeID]peerBehavior
	deliveredAt     map[NodeID][]time.Time
	inFlight        int
	maxInFlight     int
	inFlightPerPeer map[NodeID]int
	maxPerPeer      int
	cancelledSends  int
}

func newScriptedTransport() *scriptedTransport {
	return &scriptedTransport{
		behaviors:       make(map[NodeID]peerBehavior),
		deliveredAt:     make(map[NodeID][]time.Time),
		inFlightPerPeer: make(map[NodeID]int),
	}
}

func (st *scriptedTransport) Start(ctx context.Context) error        { return nil }
func (st *scriptedTransport) Stop() error                            { return nil }
func (st *scriptedTransport) SetMessageHandler(func(*Message) error) {}
func (st *scriptedTransport) setBehavior(id NodeID, behavior peerBehavior) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.behaviors[id] = behavior
}

func (st *scriptedTransport) Send(ctx context.Context, node *Node, msg *Message) error {
	st.mu.Lock()
	behavior := st.behaviors[node.ID]
	st.inFlight++
	st.inFlightPerPeer[node.ID]++
	if st.inFlight > st.maxInFlight {
		st.maxInFlight = st.inFlight
	}
	if st.inFlightPerPeer[node.ID] > st.maxPerPeer {
		st.maxPerPeer = st.inFlightPerPeer[node.ID]
	}
	st.mu.Unlock()

	defer func() {
		st.mu.Lock()
		st.inFlight--
		st.inFlightPerPeer[node.ID]--
		st.mu.Unlock()
	}()

	var answer <-chan time.Time
	if !behavior.dead {
		answer = time.After(behavior.delay)
	}
	select {
	case <-answer:
		st.mu.Lock()
		st.deliveredAt[node.ID] = append(st.deliveredAt[node.ID], time.Now())
		st.mu.Unlock()
		return nil
	case <-ctx.Done():
		st.mu.Lock()
		st.cancelledSends++
		st.mu.Unlock()
		return ctx.Err()
	}
}

func (st *scriptedTransport) deliveries(id NodeID) []time.Time {
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]time.Time(nil), st.deliveredAt[id]...)
}

func (st *scriptedTransport) snapshot() (inFlight, maxInFlight, maxPerPeer, cancelled int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.inFlight, st.maxInFlight, st.maxPerPeer, st.cancelledSends
}

func newScriptedProtocol(t *testing.T, queueDepth, maxConcurrent int, sendTimeout time.Duration) (*Protocol, *scriptedTransport) {
	t.Helper()
	localNode := &Node{ID: "local", Address: "localhost", Port: 9090, HTTPPort: 8080, Enclave: "default"}
	protocol := NewProtocol(localNode, 3, "")
	protocol.setSendLimits(queueDepth, maxConcurrent, sendTimeout)
	transport := newScriptedTransport()
	protocol.SetTransport(transport)
	t.Cleanup(func() { protocol.dispatcher.stop() })
	return protocol, transport
}

func addTestPeer(t *testing.T, protocol *Protocol, id NodeID) {
	t.Helper()
	if !protocol.addPeer(&Node{ID: id, Address: string(id), Port: 9090, HTTPPort: 8080, Enclave: "default"}) {
		t.Fatalf("addPeer(%s) rejected", id)
	}
}

// waitForSendsIdle waits until nothing is queued or in flight.
func waitForSendsIdle(t *testing.T, protocol *Protocol) {
	t.Helper()
	waitUntil(t, 2*time.Second, "sends idle", func() bool { return protocol.dispatcher.pending.Load() == 0 })
}

func waitUntil(t *testing.T, timeout time.Duration, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", timeout, what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func putMessage(id string) *Message {
	return &Message{Type: MessageTypePut, From: "local", Key: "k-" + id, MessageID: id}
}

// One and two unanswering peers, inserted in shuffled order among healthy
// peers: the broadcast returns at once and every healthy peer gets the write
// promptly, whatever the peer order.
func TestBroadcastDeadPeersDoNotDelayHealthyPeers(t *testing.T) {
	for _, deadCount := range []int{1, 2} {
		for round := 0; round < 5; round++ {
			t.Run(fmt.Sprintf("dead=%d/round=%d", deadCount, round), func(t *testing.T) {
				protocol, transport := newScriptedProtocol(t, DefaultSendQueueDepth, DefaultMaxConcurrentSends, DefaultSendTimeout)

				var peerIDs []NodeID
				for i := 0; i < deadCount; i++ {
					deadID := NodeID(fmt.Sprintf("dead-%d", i))
					transport.setBehavior(deadID, peerBehavior{dead: true})
					peerIDs = append(peerIDs, deadID)
				}
				healthyIDs := []NodeID{"healthy-0", "healthy-1", "healthy-2"}
				peerIDs = append(peerIDs, healthyIDs...)
				rand.Shuffle(len(peerIDs), func(i, j int) { peerIDs[i], peerIDs[j] = peerIDs[j], peerIDs[i] })
				for _, id := range peerIDs {
					addTestPeer(t, protocol, id)
				}

				startedAt := time.Now()
				if err := protocol.BroadcastToEnclave(putMessage("m1")); err != nil {
					t.Fatalf("BroadcastToEnclave: %v", err)
				}
				if elapsed := time.Since(startedAt); elapsed > 50*time.Millisecond {
					t.Fatalf("BroadcastToEnclave took %v; it must only queue", elapsed)
				}

				for _, id := range healthyIDs {
					waitUntil(t, 500*time.Millisecond, "delivery to "+string(id), func() bool {
						return len(transport.deliveries(id)) == 1
					})
				}
			})
		}
	}
}

// A slow peer (answers after 2s) does not delay delivery to healthy peers,
// and still receives the write itself.
func TestBroadcastSlowPeerDoesNotDelayHealthyPeers(t *testing.T) {
	protocol, transport := newScriptedProtocol(t, DefaultSendQueueDepth, DefaultMaxConcurrentSends, DefaultSendTimeout)
	transport.setBehavior("slow", peerBehavior{delay: 2 * time.Second})
	for _, id := range []NodeID{"slow", "healthy-0", "healthy-1"} {
		addTestPeer(t, protocol, id)
	}

	startedAt := time.Now()
	if err := protocol.BroadcastToEnclave(putMessage("m1")); err != nil {
		t.Fatalf("BroadcastToEnclave: %v", err)
	}
	for _, id := range []NodeID{"healthy-0", "healthy-1"} {
		waitUntil(t, 500*time.Millisecond, "delivery to "+string(id), func() bool {
			return len(transport.deliveries(id)) == 1
		})
		if latency := transport.deliveries(id)[0].Sub(startedAt); latency > 250*time.Millisecond {
			t.Fatalf("delivery to %s took %v behind a slow peer", id, latency)
		}
	}
	waitUntil(t, 4*time.Second, "delivery to slow peer", func() bool {
		return len(transport.deliveries("slow")) == 1
	})
}

// Above the fanout threshold, forwarding to dead targets returns at once.
func TestForwardAboveThresholdDoesNotWaitOnDeadTargets(t *testing.T) {
	protocol, transport := newScriptedProtocol(t, DefaultSendQueueDepth, DefaultMaxConcurrentSends, DefaultSendTimeout)
	peerCount := FanoutThreshold + 6
	for i := 0; i < peerCount; i++ {
		id := NodeID(fmt.Sprintf("dead-%d", i))
		transport.setBehavior(id, peerBehavior{dead: true})
		addTestPeer(t, protocol, id)
	}

	startedAt := time.Now()
	protocol.ForwardToEnclave(&Message{Type: MessageTypePut, From: "dead-0", Key: "k", MessageID: "fwd-1"})
	if elapsed := time.Since(startedAt); elapsed > 50*time.Millisecond {
		t.Fatalf("ForwardToEnclave took %v; it must only queue", elapsed)
	}
	waitUntil(t, time.Second, "forwards in flight", func() bool {
		inFlight, _, _, _ := transport.snapshot()
		return inFlight == fanoutSize(peerCount)
	})
}

// A burst toward a dead peer fills its bounded queue and then drops and
// counts, rather than queuing or spawning without limit.
func TestSendQueueFullDropsAndCounts(t *testing.T) {
	const queueDepth = 8
	const burst = 500
	protocol, transport := newScriptedProtocol(t, queueDepth, DefaultMaxConcurrentSends, DefaultSendTimeout)
	transport.setBehavior("dead", peerBehavior{dead: true})
	addTestPeer(t, protocol, "dead")

	// Let the worker take the first message so the count below is exact.
	protocol.QueueToPeer("dead", putMessage("first"))
	waitUntil(t, time.Second, "first send in flight", func() bool {
		inFlight, _, _, _ := transport.snapshot()
		return inFlight == 1
	})

	goroutinesBefore := runtime.NumGoroutine()
	for i := 0; i < burst; i++ {
		if !protocol.QueueToPeer("dead", putMessage(fmt.Sprintf("burst-%d", i))) {
			t.Fatal("QueueToPeer reported a known peer as unknown")
		}
	}
	if grown := runtime.NumGoroutine() - goroutinesBefore; grown > 2 {
		t.Fatalf("goroutines grew by %d during a burst to one peer", grown)
	}
	if dropped := protocol.dispatcher.dropped.Load(); dropped != burst-queueDepth {
		t.Fatalf("dropped = %d, want %d", dropped, burst-queueDepth)
	}
	if pending := protocol.dispatcher.pending.Load(); pending != queueDepth+1 {
		t.Fatalf("pending = %d, want %d (queue depth plus one in flight)", pending, queueDepth+1)
	}
}

// The global cap bounds sends in flight across peers, and each peer has at
// most one send in flight.
func TestSendConcurrencyBounds(t *testing.T) {
	const maxConcurrent = 3
	protocol, transport := newScriptedProtocol(t, DefaultSendQueueDepth, maxConcurrent, DefaultSendTimeout)
	for i := 0; i < 8; i++ {
		id := NodeID(fmt.Sprintf("dead-%d", i))
		transport.setBehavior(id, peerBehavior{dead: true})
		addTestPeer(t, protocol, id)
	}
	for i := 0; i < 4; i++ {
		if err := protocol.BroadcastToEnclave(putMessage(fmt.Sprintf("m%d", i))); err != nil {
			t.Fatalf("BroadcastToEnclave: %v", err)
		}
	}
	waitUntil(t, time.Second, "global cap reached", func() bool {
		inFlight, _, _, _ := transport.snapshot()
		return inFlight == maxConcurrent
	})
	time.Sleep(50 * time.Millisecond)
	_, maxInFlight, maxPerPeer, _ := transport.snapshot()
	if maxInFlight != maxConcurrent {
		t.Fatalf("max in flight = %d, want %d", maxInFlight, maxConcurrent)
	}
	if maxPerPeer != 1 {
		t.Fatalf("max in flight to one peer = %d, want 1", maxPerPeer)
	}
}

// Each send has its own deadline: a dead peer's sends time out one by one
// instead of blocking its queue forever.
func TestSendDeadlinePerMessage(t *testing.T) {
	protocol, transport := newScriptedProtocol(t, DefaultSendQueueDepth, DefaultMaxConcurrentSends, 50*time.Millisecond)
	transport.setBehavior("dead", peerBehavior{dead: true})
	addTestPeer(t, protocol, "dead")
	for i := 0; i < 3; i++ {
		protocol.QueueToPeer("dead", putMessage(fmt.Sprintf("m%d", i)))
	}
	waitUntil(t, time.Second, "all three sends to time out", func() bool {
		_, _, _, cancelled := transport.snapshot()
		return cancelled == 3
	})
	waitForSendsIdle(t, protocol)
}

// Stop aborts in-flight sends, discards queued ones, and leaves no workers.
func TestStopAbortsInFlightSendsWithoutLeaks(t *testing.T) {
	goroutinesBefore := runtime.NumGoroutine()
	protocol, transport := newScriptedProtocol(t, DefaultSendQueueDepth, DefaultMaxConcurrentSends, DefaultSendTimeout)
	for i := 0; i < 5; i++ {
		id := NodeID(fmt.Sprintf("dead-%d", i))
		transport.setBehavior(id, peerBehavior{dead: true})
		addTestPeer(t, protocol, id)
	}
	for i := 0; i < 10; i++ {
		if err := protocol.BroadcastToEnclave(putMessage(fmt.Sprintf("m%d", i))); err != nil {
			t.Fatalf("BroadcastToEnclave: %v", err)
		}
	}
	waitUntil(t, time.Second, "sends in flight", func() bool {
		inFlight, _, _, _ := transport.snapshot()
		return inFlight == 5
	})

	startedAt := time.Now()
	if err := protocol.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("Stop took %v with sends in flight", elapsed)
	}
	if pending := protocol.dispatcher.pending.Load(); pending != 0 {
		t.Fatalf("pending after Stop = %d, want 0", pending)
	}
	waitUntil(t, time.Second, "goroutines to return to baseline", func() bool {
		return runtime.NumGoroutine() <= goroutinesBefore
	})
	protocol.QueueToPeer("dead-0", putMessage("after-stop"))
	if pending := protocol.dispatcher.pending.Load(); pending != 0 {
		t.Fatalf("a message was queued after Stop (pending = %d)", pending)
	}
}

// Removing a peer stops its worker and aborts its in-flight send.
func TestRemovePeerStopsItsWorker(t *testing.T) {
	protocol, transport := newScriptedProtocol(t, DefaultSendQueueDepth, DefaultMaxConcurrentSends, DefaultSendTimeout)
	transport.setBehavior("dead", peerBehavior{dead: true})
	addTestPeer(t, protocol, "dead")
	for i := 0; i < 4; i++ {
		protocol.QueueToPeer("dead", putMessage(fmt.Sprintf("m%d", i)))
	}
	waitUntil(t, time.Second, "send in flight", func() bool {
		inFlight, _, _, _ := transport.snapshot()
		return inFlight == 1
	})

	protocol.removePeer("dead")
	waitForSendsIdle(t, protocol)
	if _, _, _, cancelled := transport.snapshot(); cancelled != 1 {
		t.Fatalf("cancelled sends = %d, want 1", cancelled)
	}
	if protocol.QueueToPeer("dead", putMessage("late")) {
		t.Fatal("QueueToPeer accepted a removed peer")
	}
}
