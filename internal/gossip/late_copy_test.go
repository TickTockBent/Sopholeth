package gossip

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Tests for #152: a copy of a gossip message that arrives late must not start
// a second propagation wave. Three local rules do this: a sender drops a
// queued send older than maxSendAge, the dedup window covers the longest time
// a copy can stay in the mesh, and a node stops forwarding at MaxForwardHops.

// lockedClock is a test clock that only moves when the test advances it.
// Unlike manualClock it is safe to read from dispatcher workers.
type lockedClock struct {
	mu      sync.Mutex
	current time.Time
}

func newLockedClock() *lockedClock {
	return &lockedClock{current: time.Unix(1_700_000_000, 0)}
}

func (c *lockedClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *lockedClock) advance(step time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = c.current.Add(step)
}

// messageRecorder is a Transport that records every message it is asked to
// send and answers at once.
type messageRecorder struct {
	mu   sync.Mutex
	sent []Message
}

func (r *messageRecorder) Start(context.Context) error            { return nil }
func (r *messageRecorder) Stop() error                            { return nil }
func (r *messageRecorder) SetMessageHandler(func(*Message) error) {}
func (r *messageRecorder) Send(_ context.Context, _ *Node, msg *Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, *msg)
	return nil
}

func (r *messageRecorder) messages() []Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Message(nil), r.sent...)
}

// newForwardingProtocol returns a protocol above the fanout threshold, so
// ForwardToEnclave forwards, with a transport that records each send.
func newForwardingProtocol(t *testing.T) (*Protocol, *messageRecorder) {
	t.Helper()
	localNode := &Node{ID: "local", Address: "localhost", Port: 9090, HTTPPort: 8080, Enclave: "default"}
	protocol := NewProtocol(localNode, 3, "")
	recorder := &messageRecorder{}
	protocol.SetTransport(recorder)
	t.Cleanup(func() { protocol.dispatcher.stop() })
	for peerIndex := 0; peerIndex <= FanoutThreshold; peerIndex++ {
		addTestPeer(t, protocol, NodeID(fmt.Sprintf("peer-%d", peerIndex)))
	}
	return protocol, recorder
}

// A forward carries the received hop count plus one. The caller's message,
// which the cluster layer also hands to attached children, is not changed.
func TestForwardCarriesIncrementedHops(t *testing.T) {
	protocol, recorder := newForwardingProtocol(t)

	received := &Message{Type: MessageTypePut, From: "peer-0", Key: "k", MessageID: "forwarded", Hops: 3}
	protocol.ForwardToEnclave(received)
	waitForSendsIdle(t, protocol)

	sent := recorder.messages()
	if want := fanoutSize(FanoutThreshold + 1); len(sent) != want {
		t.Fatalf("forwarded to %d peers, want %d", len(sent), want)
	}
	for _, forwarded := range sent {
		if forwarded.Hops != 4 {
			t.Fatalf("forward carries hops %d, want 4", forwarded.Hops)
		}
		if forwarded.MessageID != "forwarded" || forwarded.From != "peer-0" {
			t.Fatalf("forward changed the message ID or originator: %+v", forwarded)
		}
	}
	if received.Hops != 3 {
		t.Fatalf("ForwardToEnclave changed the caller's message: hops %d", received.Hops)
	}
}

// A message that has been forwarded MaxForwardHops times, or carries a
// negative count, is not forwarded. One just under the limit still is.
func TestForwardStopsAtHopLimit(t *testing.T) {
	protocol, recorder := newForwardingProtocol(t)

	for _, hops := range []int{MaxForwardHops, MaxForwardHops + 5, -1} {
		protocol.ForwardToEnclave(&Message{Type: MessageTypePut, From: "peer-0", MessageID: fmt.Sprintf("stopped-%d", hops), Hops: hops})
	}
	protocol.ForwardToEnclave(&Message{Type: MessageTypePut, From: "peer-0", MessageID: "last-hop", Hops: MaxForwardHops - 1})
	waitForSendsIdle(t, protocol)

	for _, forwarded := range recorder.messages() {
		if forwarded.MessageID != "last-hop" {
			t.Fatalf("forwarded %s with hops %d past the limit", forwarded.MessageID, forwarded.Hops-1)
		}
		if forwarded.Hops != MaxForwardHops {
			t.Fatalf("last forward carries hops %d, want %d", forwarded.Hops, MaxForwardHops)
		}
	}
	if len(recorder.messages()) == 0 {
		t.Fatal("a message one hop under the limit was not forwarded")
	}
}

// Delayed delivery: a message that waits in a peer's queue longer than
// maxSendAge is dropped unsent and counted. A message queued after the delay
// is sent normally.
func TestQueuedSendOlderThanMaxAgeIsDropped(t *testing.T) {
	clock := newLockedClock()
	protocol, transport := newScriptedProtocol(t, sendLimits{
		sendsPerPeer: 1,
		maxSendAge:   15 * time.Second,
		now:          clock.now,
	})
	addTestPeer(t, protocol, "slow")
	transport.setBehavior("slow", peerBehavior{delay: 200 * time.Millisecond})

	// The first send occupies the peer's only worker; the second waits.
	protocol.QueueToPeer("slow", putMessage("in-flight"))
	waitUntil(t, time.Second, "first send in flight", func() bool {
		inFlight, _, _, _ := transport.snapshot()
		return inFlight == 1
	})
	protocol.QueueToPeer("slow", putMessage("waited-too-long"))
	clock.advance(16 * time.Second)
	protocol.QueueToPeer("slow", putMessage("queued-after-delay"))
	waitForSendsIdle(t, protocol)

	if _, delivered := transport.deliveredAtByID("waited-too-long"); delivered {
		t.Fatal("a send that waited past maxSendAge was delivered")
	}
	if expired := protocol.dispatcher.expired.Load(); expired != 1 {
		t.Fatalf("expired = %d, want 1", expired)
	}
	for _, messageID := range []string{"in-flight", "queued-after-delay"} {
		if _, delivered := transport.deliveredAtByID(messageID); !delivered {
			t.Fatalf("%s was not delivered", messageID)
		}
	}
}

// The dedup window covers the longest time a copy can stay in the mesh: one
// sender's maximum delay for each hop, plus the originator's.
func TestSeenWindowCoversEveryHop(t *testing.T) {
	longestLateCopy := time.Duration(MaxForwardHops+1) * (DefaultMaxSendAge + DefaultSendTimeout)
	if seenMessageTTL < longestLateCopy {
		t.Fatalf("seenMessageTTL %v is shorter than the longest late copy %v", seenMessageTTL, longestLateCopy)
	}
}

// A copy redelivered after the old 60-second window, but inside the current
// one, is still a duplicate. After the window it is new again.
func TestRedeliveryAfterOldWindowIsStillDuplicate(t *testing.T) {
	clock := newLockedClock()
	protocol := NewProtocol(&Node{ID: "local", Address: "localhost", Port: 9090, HTTPPort: 8080}, 3, "")
	protocol.seenMessages = newSeenCache(maxSeenMessages, seenMessageTTL, clock.now)

	if protocol.MarkSeen("late") {
		t.Fatal("first delivery reported as a duplicate")
	}
	clock.advance(61 * time.Second)
	if !protocol.MarkSeen("late") {
		t.Fatal("redelivery 61s later was not a duplicate")
	}
	clock.advance(seenMessageTTL)
	if protocol.MarkSeen("late") {
		t.Fatal("redelivery after the window was still a duplicate")
	}
}

// meshNode is one node of an in-memory mesh used to follow a message's
// lineage. Its handler does what the cluster layer does with a PUT: skip a
// duplicate, otherwise forward.
type meshNode struct {
	protocol *Protocol
	// forgetful nodes never remember a message ID, the limit of dedup
	// pressure (#168): every copy looks new.
	forgetful bool
}

// meshTransport delivers a send to the target node's handler.
type meshTransport struct {
	nodes      map[NodeID]*meshNode
	deliveries *atomic.Int64
	maxHops    *atomic.Int64
}

func (m *meshTransport) Start(context.Context) error            { return nil }
func (m *meshTransport) Stop() error                            { return nil }
func (m *meshTransport) SetMessageHandler(func(*Message) error) {}
func (m *meshTransport) Send(_ context.Context, node *Node, msg *Message) error {
	m.deliveries.Add(1)
	for {
		highest := m.maxHops.Load()
		if int64(msg.Hops) <= highest || m.maxHops.CompareAndSwap(highest, int64(msg.Hops)) {
			break
		}
	}
	target := m.nodes[node.ID]
	received := *msg
	if !target.forgetful && target.protocol.MarkSeen(received.MessageID) {
		return nil
	}
	target.protocol.ForwardToEnclave(&received)
	return nil
}

// newMesh builds a fully connected enclave of nodeCount nodes.
func newMesh(t *testing.T, nodeCount int, forgetful bool) (map[NodeID]*meshNode, *meshTransport) {
	t.Helper()
	transport := &meshTransport{nodes: make(map[NodeID]*meshNode), deliveries: &atomic.Int64{}, maxHops: &atomic.Int64{}}
	allNodes := make([]*Node, nodeCount)
	for nodeIndex := range allNodes {
		allNodes[nodeIndex] = &Node{ID: NodeID(fmt.Sprintf("n%d", nodeIndex)), Address: fmt.Sprintf("n%d", nodeIndex), Port: 9090, HTTPPort: 8080, Enclave: "default"}
	}
	for _, local := range allNodes {
		protocol := NewProtocol(local, 3, "")
		protocol.SetTransport(transport)
		t.Cleanup(func() { protocol.dispatcher.stop() })
		for _, peer := range allNodes {
			if peer.ID != local.ID {
				protocol.addPeer(peer)
			}
		}
		transport.nodes[local.ID] = &meshNode{protocol: protocol, forgetful: forgetful}
	}
	return transport.nodes, transport
}

// waitForMeshIdle waits until no node has a send queued or in flight. A
// handler queues its forwards before its own send finishes, so an idle mesh
// stays idle.
func waitForMeshIdle(t *testing.T, nodes map[NodeID]*meshNode) {
	t.Helper()
	waitUntil(t, 5*time.Second, "mesh idle", func() bool {
		for _, node := range nodes {
			if node.protocol.dispatcher.pending.Load() != 0 {
				return false
			}
		}
		return true
	})
}

// With dedup working, a write spreads in one wave: every node forwards it at
// most once, no copy goes past the hop limit, and the enclave is covered.
func TestWaveForwardsOncePerNodeWithinHopLimit(t *testing.T) {
	const nodeCount = 40
	nodes, transport := newMesh(t, nodeCount, false)

	origin := nodes["n0"].protocol
	if err := origin.BroadcastToEnclave(&Message{Type: MessageTypePut, From: "n0", MessageID: "wave"}); err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	waitForMeshIdle(t, nodes)

	reached := 0
	for id, node := range nodes {
		node.protocol.seenMutex.Lock()
		_, seen := node.protocol.seenMessages.expiryByID["wave"]
		node.protocol.seenMutex.Unlock()
		if seen && id != "n0" {
			reached++
		}
	}
	// √N fanout misses each node with probability about e^-√N (about 0.2%
	// here), with or without the hop limit. Three misses in one wave would
	// mean something cut the wave short.
	if reached < nodeCount-3 {
		t.Fatalf("wave reached %d of %d nodes", reached, nodeCount-1)
	}
	// The originator sends to f peers and each reached node forwards to at
	// most f, once: a node that forwarded twice would exceed this.
	fanout := int64(fanoutSize(nodeCount - 1))
	if deliveries, ceiling := transport.deliveries.Load(), fanout*int64(reached+1); deliveries > ceiling {
		t.Fatalf("%d deliveries for %d reached nodes, more than one forward each (ceiling %d)", deliveries, reached, ceiling)
	}
	if deepest := transport.maxHops.Load(); deepest > MaxForwardHops {
		t.Fatalf("a copy carried hops %d, past the limit %d", deepest, MaxForwardHops)
	}
}

// Dedup pressure at its limit: no node remembers any message ID, so every
// copy looks new and is forwarded again. Without a hop limit this never ends.
// With it, a late copy that re-enters at hop MaxForwardHops-2 produces exactly
// f + f² further deliveries and stops.
func TestHopLimitEndsLineageWithoutDedup(t *testing.T) {
	const nodeCount = FanoutThreshold + 2 // 11 peers each, fanout 4
	nodes, transport := newMesh(t, nodeCount, true)

	lateCopy := &Message{Type: MessageTypePut, From: "n0", MessageID: "late-copy", Hops: MaxForwardHops - 2}
	nodes["n1"].protocol.ForwardToEnclave(lateCopy)
	waitForMeshIdle(t, nodes)

	fanout := int64(fanoutSize(nodeCount - 1))
	if deliveries, want := transport.deliveries.Load(), fanout+fanout*fanout; deliveries != want {
		t.Fatalf("late copy produced %d deliveries, want %d", deliveries, want)
	}
	if deepest := transport.maxHops.Load(); deepest != MaxForwardHops {
		t.Fatalf("deepest copy carried hops %d, want %d", deepest, MaxForwardHops)
	}
}

// The hop count survives the wire form both ways, and a message from an
// older node, which has no hops field, reads as hop 0.
func TestHopsSurviveWireForm(t *testing.T) {
	original := &Message{Type: MessageTypePut, From: "a", Key: "k", MessageID: "id", TTL: 300, Hops: 5}
	encoded, err := json.Marshal(NewSimpleMessage(original))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire SimpleMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded := wire.ToMessage(); decoded.Hops != 5 {
		t.Fatalf("hops after round trip = %d, want 5", decoded.Hops)
	}

	fromOriginator, _ := json.Marshal(NewSimpleMessage(&Message{Type: MessageTypePut, MessageID: "origin"}))
	if strings.Contains(string(fromOriginator), "hops") {
		t.Fatalf("a hop-0 message carries a hops field: %s", fromOriginator)
	}
	var legacy SimpleMessage
	if err := json.Unmarshal([]byte(`{"type":"PUT","from":"old","message_id":"legacy"}`), &legacy); err != nil {
		t.Fatalf("unmarshal legacy: %v", err)
	}
	if legacy.ToMessage().Hops != 0 {
		t.Fatal("a message without a hops field did not read as hop 0")
	}
}
