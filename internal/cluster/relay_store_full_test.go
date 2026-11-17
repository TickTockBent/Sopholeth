package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"sopholeth/internal/gossip"
)

// Regression tests for #163: a relay whose store is full must still forward
// gossip, must not ACK what it did not store, and must still deduplicate.

// relayStoreCapacity is the full relay's store size. Each test fills it with
// occupantKey, so any new key is refused with storage.ErrStoreFull while an
// overwrite of occupantKey with an equal-sized value still fits.
const (
	relayStoreCapacity = 16
	occupantKey        = "occupant"
)

var occupantValue = strings.Repeat("o", relayStoreCapacity)

// recordingPeer is a fake gossip peer that records every message it receives
// and answers 200. It never forwards, so every PUT it records came straight
// from the node under test.
type recordingPeer struct {
	server   *httptest.Server
	mu       sync.Mutex
	messages []gossip.SimpleMessage
}

func newRecordingPeer(t *testing.T) *recordingPeer {
	t.Helper()
	peer := &recordingPeer{}
	peer.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var received gossip.SimpleMessage
		if err := json.NewDecoder(r.Body).Decode(&received); err == nil {
			peer.mu.Lock()
			peer.messages = append(peer.messages, received)
			peer.mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(peer.server.Close)
	return peer
}

func (p *recordingPeer) port() int { return p.server.Listener.Addr().(*net.TCPAddr).Port }

// count returns how many messages of the given type and ID this peer received
// from sender ("" matches any sender).
func (p *recordingPeer) count(messageType gossip.MessageType, messageID string, sender gossip.NodeID) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	matching := 0
	for _, received := range p.messages {
		if received.Type == string(messageType) && received.MessageID == messageID &&
			(sender == "" || received.From == string(sender)) {
			matching++
		}
	}
	return matching
}

func totalReceived(peers []*recordingPeer, messageType gossip.MessageType, messageID string) int {
	total := 0
	for _, peer := range peers {
		total += peer.count(messageType, messageID, "")
	}
	return total
}

// childRecorder is a ChildBroadcaster that records the message IDs it is
// asked to fan out to attached transients.
type childRecorder struct {
	mu         sync.Mutex
	messageIDs []string
}

func (c *childRecorder) BroadcastToChildren(msg *gossip.Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messageIDs = append(c.messageIDs, msg.MessageID)
}

func (c *childRecorder) count(messageID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	matching := 0
	for _, recorded := range c.messageIDs {
		if recorded == messageID {
			matching++
		}
	}
	return matching
}

func waitUntil(t *testing.T, timeout time.Duration, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !condition() {
		t.Fatalf("timed out after %v waiting until %s", timeout, description)
	}
}

// postGossipPut delivers a PUT to node's gossip endpoint as peer sender would
// and returns the HTTP status.
func postGossipPut(t *testing.T, node *testNode, sender gossip.NodeID, key string, value []byte, messageID string) int {
	t.Helper()
	body, err := json.Marshal(gossip.SimpleMessage{
		Type:      string(gossip.MessageTypePut),
		From:      string(sender),
		Key:       key,
		Data:      value,
		TTL:       300,
		Timestamp: time.Now().Unix(),
		MessageID: messageID,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	response, err := http.Post("http://"+node.addr()+"/v1/gossip/message", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST gossip: %v", err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	return response.StatusCode
}

// startFullRelay starts a relay whose store holds occupantKey and has no room
// for any other key.
func startFullRelay(t *testing.T, ctx context.Context) (*testNode, *childRecorder) {
	t.Helper()
	relay := newTestNodeWithStorage(t, "relay", "default", 3, relayStoreCapacity)
	children := &childRecorder{}
	relay.node.SetChildBroadcaster(children)
	relay.start(t, ctx, nil)
	t.Cleanup(relay.stop)
	if err := relay.node.store.Put(occupantKey, []byte(occupantValue), 300*time.Second); err != nil {
		t.Fatalf("fill relay store: %v", err)
	}
	return relay, children
}

func expectedFanout(replicationPeers int) int {
	return int(math.Ceil(math.Sqrt(float64(replicationPeers))))
}

// relayStoreFailures reads gossip_relay_store_failed_total from the default
// registry, where Start registers it. Tests in this package run serially.
func relayStoreFailures(t *testing.T) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() == "gossip_relay_store_failed_total" {
			return family.GetMetric()[0].GetCounter().GetValue()
		}
	}
	t.Fatal("gossip_relay_store_failed_total is not registered")
	return 0
}

// Above the fanout threshold, a relay that cannot store a PUT still forwards
// it, so downstream peers with room store it; the relay sends no ACK, and
// its gossip handler still reports the store failure. On main before #163
// the relay returned before forwarding, and no downstream peer got the value.
func TestFullRelayForwardsAboveFanoutThreshold(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	relay, children := startFullRelay(t, ctx)

	origin := newRecordingPeer(t)
	registerFakePeer(t, relay, "origin", origin.port())

	downstreamCount := gossip.FanoutThreshold + 1
	downstreamNodes := make([]*testNode, downstreamCount)
	for i := range downstreamNodes {
		downstream := newTestNode(t, fmt.Sprintf("downstream-%d", i), "default", 3)
		downstream.start(t, ctx, nil)
		t.Cleanup(downstream.stop)
		registerFakePeer(t, relay, string(downstream.node.localNode.ID), downstream.port)
		downstreamNodes[i] = downstream
	}
	replicationPeers := downstreamCount + 1
	waitForPeers(t, relay, replicationPeers, time.Second)

	const relayedKey, relayedValue, relayedMessageID = "relayed-key", "relayed-value", "relayed-message"
	if status := postGossipPut(t, relay, "origin", relayedKey, []byte(relayedValue), relayedMessageID); status != http.StatusInternalServerError {
		t.Fatalf("relay answered %d for a PUT it could not store, want %d", status, http.StatusInternalServerError)
	}
	if _, exists := relay.node.Get(relayedKey); exists {
		t.Fatal("full relay stored the relayed key")
	}
	// The relay forwards to √N of its peers, all downstream nodes with room.
	// Downstream nodes may forward further once they learn each other.
	wantHolders := expectedFanout(replicationPeers)
	waitUntil(t, 2*time.Second, fmt.Sprintf("%d downstream nodes hold %s", wantHolders, relayedKey), func() bool {
		holders := 0
		for _, downstream := range downstreamNodes {
			if data, exists := downstream.node.Get(relayedKey); exists && string(data) == relayedValue {
				holders++
			}
		}
		return holders >= wantHolders
	})
	if broadcasts := children.count(relayedMessageID); broadcasts != 1 {
		t.Fatalf("relay fanned the PUT out to children %d times, want 1", broadcasts)
	}

	// Sentinel: an overwrite of occupantKey fits, so the relay ACKs it. It
	// queues behind any ACK for the relayed message on the origin's queue.
	if status := postGossipPut(t, relay, "origin", occupantKey, []byte(occupantValue), "sentinel-message"); status != http.StatusOK {
		t.Fatalf("relay answered %d for a PUT that fits", status)
	}
	waitUntil(t, 2*time.Second, "origin receives the sentinel ACK", func() bool {
		return origin.count(gossip.MessageTypeAck, "sentinel-message", "relay") == 1
	})
	if acks := origin.count(gossip.MessageTypeAck, relayedMessageID, "relay"); acks != 0 {
		t.Fatalf("full relay sent %d ACKs for a PUT it did not store", acks)
	}
}

// Below the fanout threshold, a peer that cannot store a write sends no ACK,
// so the originator's quorum does not count it. With two nodes and RF 3 the
// quorum is 2: the local write plus the peer's ACK. A control write that
// fits reaches quorum; a write that does not fit times out instead.
func TestFullPeerDoesNotCountTowardQuorum(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	originNode := newTestNode(t, "origin", "default", 3)
	originNode.node.writeTimeout = time.Second
	fullPeer := newTestNodeWithStorage(t, "full-peer", "default", 3, relayStoreCapacity)
	defer originNode.stop()
	defer fullPeer.stop()
	originNode.start(t, ctx, nil)
	fullPeer.start(t, ctx, []string{originNode.addr()})
	waitForPeers(t, originNode, 1, 3*time.Second)
	waitForPeers(t, fullPeer, 1, 3*time.Second)

	if err := originNode.node.Put(ctx, occupantKey, []byte(occupantValue), 300*time.Second); err != nil {
		t.Fatalf("control write: Put returned %v, want quorum through the peer's ACK", err)
	}
	waitForValue(t, fullPeer, occupantKey, occupantValue, time.Second)

	const overflowKey = "overflow-key"
	err := originNode.node.Put(ctx, overflowKey, []byte("does-not-fit"), 300*time.Second)
	if !errors.Is(err, ErrQuorumTimeout) {
		t.Fatalf("write the peer could not store: Put returned %v, want %v", err, ErrQuorumTimeout)
	}
	if _, exists := originNode.node.Get(overflowKey); !exists {
		t.Fatal("origin did not keep its local copy")
	}
	if _, exists := fullPeer.node.Get(overflowKey); exists {
		t.Fatal("full peer stored a value beyond its capacity")
	}
}

// A redelivered copy of a PUT the full relay already handled is dropped as
// a duplicate: no second forward, no second child fan-out, no second store
// attempt. Fake peers record exactly what the relay sent them.
func TestFullRelayDeduplicatesRedelivery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	relay, children := startFullRelay(t, ctx)

	replicationPeers := gossip.FanoutThreshold + 2
	peers := make([]*recordingPeer, replicationPeers)
	for i := range peers {
		peers[i] = newRecordingPeer(t)
		registerFakePeer(t, relay, fmt.Sprintf("peer-%d", i), peers[i].port())
	}
	waitForPeers(t, relay, replicationPeers, time.Second)
	wantForwards := expectedFanout(replicationPeers)
	failuresBefore := relayStoreFailures(t)

	const relayedMessageID = "relayed-message"
	if status := postGossipPut(t, relay, "peer-0", "relayed-key", []byte("relayed-value"), relayedMessageID); status != http.StatusInternalServerError {
		t.Fatalf("first delivery: relay answered %d, want %d", status, http.StatusInternalServerError)
	}
	waitUntil(t, 2*time.Second, fmt.Sprintf("%d peers receive the forward", wantForwards), func() bool {
		return totalReceived(peers, gossip.MessageTypePut, relayedMessageID) == wantForwards
	})
	failuresAfterFirst := relayStoreFailures(t)
	if failures := failuresAfterFirst - failuresBefore; failures != 1 {
		t.Fatalf("first delivery: gossip_relay_store_failed_total rose by %v, want 1", failures)
	}

	// The same message by another route. A 200 means the duplicate check
	// answered: a second store attempt would fail and answer 500.
	if status := postGossipPut(t, relay, "peer-1", "relayed-key", []byte("relayed-value"), relayedMessageID); status != http.StatusOK {
		t.Fatalf("redelivery: relay answered %d, want %d (duplicate)", status, http.StatusOK)
	}
	if failures := relayStoreFailures(t) - failuresAfterFirst; failures != 0 {
		t.Fatalf("redelivery reached the store: gossip_relay_store_failed_total rose by %v", failures)
	}

	// Sentinel: a fresh PUT that fits is forwarded to √N peers. Once those
	// arrive, any second forward of the redelivered message, queued earlier,
	// would have had its chance to arrive too.
	if status := postGossipPut(t, relay, "peer-0", occupantKey, []byte(occupantValue), "sentinel-message"); status != http.StatusOK {
		t.Fatalf("sentinel: relay answered %d", status)
	}
	waitUntil(t, 2*time.Second, "sentinel forwards arrive", func() bool {
		return totalReceived(peers, gossip.MessageTypePut, "sentinel-message") == wantForwards
	})
	if forwards := totalReceived(peers, gossip.MessageTypePut, relayedMessageID); forwards != wantForwards {
		t.Fatalf("relay forwarded the message %d times across both deliveries, want %d", forwards, wantForwards)
	}
	if broadcasts := children.count(relayedMessageID); broadcasts != 1 {
		t.Fatalf("relay fanned the message out to children %d times, want 1", broadcasts)
	}
	if acks := totalReceived(peers, gossip.MessageTypeAck, relayedMessageID); acks != 0 {
		t.Fatalf("full relay sent %d ACKs for a message it did not store", acks)
	}
}

// A PUT that fails validateWrite (value or key over the limit) is rejected
// before the store, the ACK and the forwards, even above the fanout
// threshold, so an over-limit message does not spread.
func TestInvalidRelayedPutIsNeitherStoredNorForwarded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	relay := newTestNode(t, "relay", "default", 3)
	if err := relay.node.SetWriteLimits(WriteLimits{MaxValueBytes: 16, MaxKeyBytes: 16, MinTTLSeconds: MinTTLSeconds, MaxTTLSeconds: DefaultMaxTTLSeconds}); err != nil {
		t.Fatalf("SetWriteLimits: %v", err)
	}
	children := &childRecorder{}
	relay.node.SetChildBroadcaster(children)
	relay.start(t, ctx, nil)
	defer relay.stop()

	replicationPeers := gossip.FanoutThreshold + 2
	peers := make([]*recordingPeer, replicationPeers)
	for i := range peers {
		peers[i] = newRecordingPeer(t)
		registerFakePeer(t, relay, fmt.Sprintf("peer-%d", i), peers[i].port())
	}
	waitForPeers(t, relay, replicationPeers, time.Second)
	wantForwards := expectedFanout(replicationPeers)

	invalidPuts := []struct {
		name, key, value, messageID string
	}{
		{"value too large", "valid-key", strings.Repeat("v", 17), "oversized-value"},
		{"key too long", strings.Repeat("k", 17), "value", "oversized-key"},
	}
	for _, invalid := range invalidPuts {
		if status := postGossipPut(t, relay, "peer-0", invalid.key, []byte(invalid.value), invalid.messageID); status != http.StatusInternalServerError {
			t.Fatalf("%s: relay answered %d, want %d", invalid.name, status, http.StatusInternalServerError)
		}
		if _, exists := relay.node.Get(invalid.key); exists {
			t.Fatalf("%s: relay stored the rejected PUT", invalid.name)
		}
	}

	// Sentinel: a valid PUT is forwarded to √N peers, queued after anything
	// the rejected PUTs could have queued.
	if status := postGossipPut(t, relay, "peer-0", "valid-key", []byte("value"), "sentinel-message"); status != http.StatusOK {
		t.Fatalf("sentinel: relay answered %d", status)
	}
	waitUntil(t, 2*time.Second, "sentinel forwards arrive", func() bool {
		return totalReceived(peers, gossip.MessageTypePut, "sentinel-message") == wantForwards
	})
	for _, invalid := range invalidPuts {
		if forwards := totalReceived(peers, gossip.MessageTypePut, invalid.messageID); forwards != 0 {
			t.Fatalf("%s: relay forwarded the rejected PUT to %d peers", invalid.name, forwards)
		}
		if acks := totalReceived(peers, gossip.MessageTypeAck, invalid.messageID); acks != 0 {
			t.Fatalf("%s: relay ACKed the rejected PUT", invalid.name)
		}
		if broadcasts := children.count(invalid.messageID); broadcasts != 0 {
			t.Fatalf("%s: relay fanned the rejected PUT out to children", invalid.name)
		}
	}
}
