package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"sopholeth/internal/gossip"
)

// Regression tests for #166: gossip PUT message IDs must be opaque and
// unique across writes, nodes and restarts, whatever the clock says.
//
// Every test freezes the origin clocks, so two writes "in the same clock
// tick" is the deterministic case, not a race the test hopes to hit. On main
// before #166 a PUT's ID was key + UnixNano, so with a frozen clock every
// same-key write shares one ID and these tests fail; with random IDs the
// clock plays no part.

var opaqueMessageIDFormat = regexp.MustCompile(`^[0-9a-f]{32}$`)

// frozenInstant is the single instant every frozen clock reports.
var frozenInstant = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func freezeClock(node *testNode) {
	node.node.clock = func() time.Time { return frozenInstant }
}

// gossipRecord is one gossip message a test node received over HTTP.
type gossipRecord struct {
	messageType gossip.MessageType
	from        gossip.NodeID
	key         string
	messageID   string
}

// gossipLog records the gossip messages a test node receives before its
// ordinary handler processes them.
type gossipLog struct {
	mu      sync.Mutex
	records []gossipRecord
}

// observeGossip replaces node's HTTP handler with the production-equivalent
// one wrapped by a recorder. Call it before start.
func observeGossip(node *testNode) *gossipLog {
	return observeGossipWithOptions(node, false)
}

// observeGossipWithOptions is observeGossip; with ignoreTopology the node
// answers SYNC and SYNC_REQUEST without processing them, so it learns no
// peers beyond the ones a test registers.
func observeGossipWithOptions(node *testNode, ignoreTopology bool) *gossipLog {
	received := &gossipLog{}
	gossipHandler := makeGossipHandler(node.node)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/gossip/message", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var message gossip.SimpleMessage
		if json.Unmarshal(body, &message) == nil {
			received.mu.Lock()
			received.records = append(received.records, gossipRecord{
				messageType: gossip.MessageType(message.Type),
				from:        gossip.NodeID(message.From),
				key:         message.Key,
				messageID:   message.MessageID,
			})
			received.mu.Unlock()
			messageType := gossip.MessageType(message.Type)
			if ignoreTopology && (messageType == gossip.MessageTypeSync || messageType == gossip.MessageTypeSyncRequest) {
				w.WriteHeader(http.StatusOK)
				return
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		gossipHandler(w, r)
	})
	mux.HandleFunc("/v1/bootstrap", makeBootstrapHandler(node.node))
	node.server.Handler = mux
	return received
}

// messageIDs returns the IDs of received messages of messageType from sender
// in arrival order, repeats included.
func (l *gossipLog) messageIDs(messageType gossip.MessageType, sender gossip.NodeID) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var matching []string
	for _, record := range l.records {
		if record.messageType == messageType && record.from == sender {
			matching = append(matching, record.messageID)
		}
	}
	return matching
}

func (l *gossipLog) contains(messageType gossip.MessageType, sender gossip.NodeID, messageID string) bool {
	for _, receivedID := range l.messageIDs(messageType, sender) {
		if receivedID == messageID {
			return true
		}
	}
	return false
}

func distinctCount(messageIDs []string) int {
	distinct := make(map[string]bool, len(messageIDs))
	for _, messageID := range messageIDs {
		distinct[messageID] = true
	}
	return len(distinct)
}

// startObservedCluster starts len(nodeIDs) fully connected nodes with
// replication factor 3, each with a gossip log, the first as bootstrap seed.
func startObservedCluster(t *testing.T, ctx context.Context, nodeIDs ...string) ([]*testNode, []*gossipLog) {
	t.Helper()
	nodes := make([]*testNode, len(nodeIDs))
	logs := make([]*gossipLog, len(nodeIDs))
	for i, nodeID := range nodeIDs {
		nodes[i] = newTestNode(t, nodeID, "default", 3)
		logs[i] = observeGossip(nodes[i])
		t.Cleanup(nodes[i].stop)
	}
	nodes[0].start(t, ctx, nil)
	for _, node := range nodes[1:] {
		node.start(t, ctx, []string{nodes[0].addr()})
	}
	for _, node := range nodes {
		waitForPeers(t, node, len(nodes)-1, 3*time.Second)
	}
	return nodes, logs
}

// Many concurrent Puts to one key on one node, all in one frozen clock tick,
// get distinct IDs. Each tracks its own quorum, and every peer stores and
// ACKs every one of them: none is dropped as a duplicate.
func TestConcurrentSameKeyPutsGetDistinctMessageIDs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodes, logs := startObservedCluster(t, ctx, "origin", "peer-a", "peer-b")
	origin, originLog := nodes[0], logs[0]
	peerIDs := []gossip.NodeID{"peer-a", "peer-b"}
	freezeClock(origin)

	const writeCount = 32
	const sharedKey = "shared-key"
	putErrors := make([]error, writeCount)
	var writers sync.WaitGroup
	for writer := 0; writer < writeCount; writer++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			value := []byte(fmt.Sprintf("value-%d", writer))
			putErrors[writer] = origin.node.Put(ctx, sharedKey, value, 300*time.Second)
		}()
	}
	writers.Wait()
	for writer, err := range putErrors {
		if err != nil {
			t.Errorf("Put %d returned %v, want quorum", writer, err)
		}
	}

	// Each peer received every write under its own ID. One peer's ACK is
	// enough for quorum, so the other's copy may still be in flight.
	var originIDs []string
	for i, peerID := range peerIDs {
		peerLog := logs[i+1]
		waitUntil(t, 2*time.Second, fmt.Sprintf("%s receives every PUT", peerID), func() bool {
			return len(peerLog.messageIDs(gossip.MessageTypePut, "origin")) >= writeCount
		})
		received := peerLog.messageIDs(gossip.MessageTypePut, "origin")
		if len(received) != writeCount {
			t.Fatalf("%s received %d PUTs from origin, want %d", peerID, len(received), writeCount)
		}
		if distinct := distinctCount(received); distinct != writeCount {
			t.Fatalf("%s received %d PUTs carrying only %d distinct IDs", peerID, writeCount, distinct)
		}
		originIDs = received
	}

	// Every peer stored and ACKed every write, so none was a duplicate. ACKs
	// still in the dispatcher after quorum may arrive late.
	for _, peerID := range peerIDs {
		waitUntil(t, 2*time.Second, fmt.Sprintf("origin receives an ACK from %s for each write", peerID), func() bool {
			acked := originLog.messageIDs(gossip.MessageTypeAck, peerID)
			return len(acked) == writeCount && distinctCount(acked) == writeCount
		})
		for _, messageID := range originIDs {
			if !originLog.contains(gossip.MessageTypeAck, peerID, messageID) {
				t.Fatalf("%s did not ACK write %s", peerID, messageID)
			}
		}
	}

	origin.node.writesMutex.RLock()
	leftover := len(origin.node.pendingWrites)
	origin.node.writesMutex.RUnlock()
	if leftover != 0 {
		t.Fatalf("%d pending writes left after every Put returned", leftover)
	}
}

// Two origin nodes write one key at the same frozen instant. Their writes get
// distinct IDs and both replicate: the observer stores and ACKs both, and
// each origin stores and ACKs the other's write instead of taking it for its
// own. With key + timestamp IDs, each origin drops the other's write as a
// duplicate of its own and the observer keeps only the first.
func TestSameKeyPutsFromTwoOriginsInOneInstantBothReplicate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodes, logs := startObservedCluster(t, ctx, "origin-a", "origin-b", "observer")
	originA, originB := nodes[0], nodes[1]
	logA, logB, observerLog := logs[0], logs[1], logs[2]
	freezeClock(originA)
	freezeClock(originB)

	const sharedKey = "shared-key"
	putErrors := make([]error, 2)
	var writers sync.WaitGroup
	for writer, origin := range []*testNode{originA, originB} {
		writers.Add(1)
		go func() {
			defer writers.Done()
			putErrors[writer] = origin.node.Put(ctx, sharedKey, []byte(fmt.Sprintf("from-%d", writer)), 300*time.Second)
		}()
	}
	writers.Wait()
	for writer, err := range putErrors {
		if err != nil {
			t.Fatalf("Put from origin %d returned %v, want quorum", writer, err)
		}
	}

	// Either origin's quorum may come from the other origin's ACK, so the
	// observer's copies may still be in flight.
	waitUntil(t, 2*time.Second, "observer receives both PUTs", func() bool {
		return len(observerLog.messageIDs(gossip.MessageTypePut, "origin-a")) >= 1 &&
			len(observerLog.messageIDs(gossip.MessageTypePut, "origin-b")) >= 1
	})
	idsFromA := observerLog.messageIDs(gossip.MessageTypePut, "origin-a")
	idsFromB := observerLog.messageIDs(gossip.MessageTypePut, "origin-b")
	if len(idsFromA) != 1 || len(idsFromB) != 1 {
		t.Fatalf("observer received %d PUTs from origin-a and %d from origin-b, want 1 each", len(idsFromA), len(idsFromB))
	}
	writeA, writeB := idsFromA[0], idsFromB[0]
	if writeA == writeB {
		t.Fatalf("both origins used message ID %q for writes in the same instant", writeA)
	}

	waitUntil(t, 2*time.Second, "every node ACKs both writes", func() bool {
		return logA.contains(gossip.MessageTypeAck, "observer", writeA) &&
			logB.contains(gossip.MessageTypeAck, "observer", writeB) &&
			logA.contains(gossip.MessageTypeAck, "origin-b", writeA) &&
			logB.contains(gossip.MessageTypeAck, "origin-a", writeB)
	})
	// No ACK is credited to the wrong write.
	for _, acked := range logA.messageIDs(gossip.MessageTypeAck, "observer") {
		if acked != writeA {
			t.Fatalf("origin-a received an ACK for %q, which it did not write", acked)
		}
	}
	for _, acked := range logB.messageIDs(gossip.MessageTypeAck, "observer") {
		if acked != writeB {
			t.Fatalf("origin-b received an ACK for %q, which it did not write", acked)
		}
	}
}

// A node rebuilt under the same ID with the same frozen clock, as after a
// restart, does not reissue any message ID it used before.
func TestPutMessageIDsDoNotRepeatAcrossRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	peer := newRecordingPeer(t)
	const batchSize = 8
	const sharedKey = "shared-key"
	for generation := 1; generation <= 2; generation++ {
		// Replication factor 1: Put confirms on its local write and still
		// sends to the peer (#215), so no ACK is needed.
		node := newTestNode(t, "restarting-node", "default", 1)
		freezeClock(node)
		node.start(t, ctx, nil)
		registerFakePeer(t, node, "recording-peer", peer.port())
		for i := 0; i < batchSize; i++ {
			if err := node.node.Put(ctx, sharedKey, []byte("value"), 300*time.Second); err != nil {
				node.stop()
				t.Fatalf("generation %d: Put returned %v", generation, err)
			}
		}
		waitUntil(t, 2*time.Second, fmt.Sprintf("generation %d PUTs arrive", generation), func() bool {
			return peer.countType(gossip.MessageTypePut) == generation*batchSize
		})
		node.stop()
	}

	received := peer.messageIDsOfType(gossip.MessageTypePut)
	if distinct := distinctCount(received); distinct != len(received) {
		t.Fatalf("%d PUTs across a restart carried only %d distinct IDs", len(received), distinct)
	}
}

// A PUT's ID is opaque: 32 lowercase hex characters that do not embed the key.
func TestPutMessageIDIsOpaque(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	peer := newRecordingPeer(t)
	node := newTestNode(t, "origin", "default", 1)
	node.start(t, ctx, nil)
	defer node.stop()
	registerFakePeer(t, node, "recording-peer", peer.port())

	const key = "abcdef0123456789"
	if err := node.node.Put(ctx, key, []byte("value"), 300*time.Second); err != nil {
		t.Fatalf("Put returned %v", err)
	}
	waitUntil(t, 2*time.Second, "the PUT arrives", func() bool {
		return peer.countType(gossip.MessageTypePut) == 1
	})
	messageID := peer.messageIDsOfType(gossip.MessageTypePut)[0]
	if !opaqueMessageIDFormat.MatchString(messageID) {
		t.Fatalf("PUT message ID %q is not 32 lowercase hex characters", messageID)
	}
	if strings.Contains(messageID, key) {
		t.Fatalf("PUT message ID %q contains its key %q", messageID, key)
	}
}

// The originator's ID survives every hop: the relay's epidemic forwards and
// its ACK carry it unchanged over HTTP, and the originator's Put reaches
// quorum by matching that ACK.
func TestMessageIDPreservedThroughRelayForwardAndAck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The origin knows only the relay, so the relay is its sole target. It
	// ignores the relay's announcements of the downstream peers, and
	// replication factor 2 keeps it from asking the relay for more peers.
	origin := newTestNode(t, "origin", "default", 2)
	originLog := observeGossipWithOptions(origin, true)
	relay := newTestNode(t, "relay", "default", 3)
	relayLog := observeGossip(relay)
	origin.start(t, ctx, nil)
	defer origin.stop()
	relay.start(t, ctx, nil)
	defer relay.stop()

	// The relay is above the fanout threshold, so it forwards.
	downstream := make([]*recordingPeer, gossip.FanoutThreshold+1)
	for i := range downstream {
		downstream[i] = newRecordingPeer(t)
		registerFakePeer(t, relay, fmt.Sprintf("downstream-%d", i), downstream[i].port())
	}
	registerFakePeer(t, relay, "origin", origin.port)
	registerFakePeer(t, origin, "relay", relay.port)
	waitForPeers(t, relay, len(downstream)+1, time.Second)

	if err := origin.node.Put(ctx, "relayed-key", []byte("relayed-value"), 300*time.Second); err != nil {
		t.Fatalf("Put returned %v, want quorum through the relay's ACK", err)
	}

	putsAtRelay := relayLog.messageIDs(gossip.MessageTypePut, "origin")
	if len(putsAtRelay) != 1 {
		t.Fatalf("relay received %d PUTs from origin, want 1", len(putsAtRelay))
	}
	originalID := putsAtRelay[0]
	if !opaqueMessageIDFormat.MatchString(originalID) {
		t.Fatalf("origin sent message ID %q, want 32 lowercase hex characters", originalID)
	}
	if acks := originLog.messageIDs(gossip.MessageTypeAck, "relay"); len(acks) != 1 || acks[0] != originalID {
		t.Fatalf("origin received ACKs %v from the relay, want exactly [%s]", acks, originalID)
	}

	wantForwards := expectedFanout(len(downstream) + 1)
	waitUntil(t, 2*time.Second, fmt.Sprintf("%d downstream peers receive the forward", wantForwards), func() bool {
		return totalReceived(downstream, gossip.MessageTypePut, originalID) == wantForwards
	})
	for i, peer := range downstream {
		for _, forwardedID := range peer.messageIDsOfType(gossip.MessageTypePut) {
			if forwardedID != originalID {
				t.Fatalf("downstream-%d received a forward with ID %q, want %q", i, forwardedID, originalID)
			}
		}
	}
}

func (p *recordingPeer) countType(messageType gossip.MessageType) int {
	return len(p.messageIDsOfType(messageType))
}

// messageIDsOfType returns the IDs of received messages of messageType in
// arrival order, repeats included.
func (p *recordingPeer) messageIDsOfType(messageType gossip.MessageType) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var matching []string
	for _, received := range p.messages {
		if received.Type == string(messageType) {
			matching = append(matching, received.MessageID)
		}
	}
	return matching
}
