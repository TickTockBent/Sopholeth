package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"sopholeth/internal/gossip"
)

// End-to-end checks for #152 over the HTTP gossip path: the hop count
// survives the wire, a relay stores and ACKs every copy it accepts but stops
// forwarding at gossip.MaxForwardHops, and overwrites still propagate.

// postGossipPutWithHops is postGossipPut with an explicit hop count.
func postGossipPutWithHops(t *testing.T, node *testNode, sender gossip.NodeID, key string, value []byte, messageID string, hops int) int {
	t.Helper()
	body, err := json.Marshal(gossip.SimpleMessage{
		Type:      string(gossip.MessageTypePut),
		From:      string(sender),
		Key:       key,
		Data:      value,
		TTL:       300,
		Timestamp: time.Now().Unix(),
		MessageID: messageID,
		Hops:      hops,
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

// hopsReceived returns the hop counts on every PUT with messageID the peers
// received.
func hopsReceived(peers []*recordingPeer, messageID string) []int {
	var hopCounts []int
	for _, peer := range peers {
		peer.mu.Lock()
		for _, received := range peer.messages {
			if received.Type == string(gossip.MessageTypePut) && received.MessageID == messageID {
				hopCounts = append(hopCounts, received.Hops)
			}
		}
		peer.mu.Unlock()
	}
	return hopCounts
}

// startForwardingRelay starts a relay above the fanout threshold whose peers
// are recording fakes; peers[0] plays the originator.
func startForwardingRelay(t *testing.T, ctx context.Context) (*testNode, []*recordingPeer, int) {
	t.Helper()
	relay := newTestNode(t, "relay", "default", 3)
	relay.start(t, ctx, nil)
	t.Cleanup(relay.stop)

	replicationPeers := gossip.FanoutThreshold + 2
	peers := make([]*recordingPeer, replicationPeers)
	for peerIndex := range peers {
		peers[peerIndex] = newRecordingPeer(t)
		registerFakePeer(t, relay, fmt.Sprintf("peer-%d", peerIndex), peers[peerIndex].port())
	}
	waitForPeers(t, relay, replicationPeers, time.Second)
	return relay, peers, expectedFanout(replicationPeers)
}

func TestRelayForwardsWithIncrementedHopsUntilLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	relay, peers, wantForwards := startForwardingRelay(t, ctx)

	// A copy below the limit is stored, ACKed and forwarded with hops + 1.
	if status := postGossipPutWithHops(t, relay, "peer-0", "mid-key", []byte("mid"), "mid-hop", 2); status != http.StatusOK {
		t.Fatalf("mid-hop copy: relay answered %d", status)
	}
	waitUntil(t, 2*time.Second, "mid-hop forwards arrive", func() bool {
		return len(hopsReceived(peers, "mid-hop")) == wantForwards
	})
	for _, hops := range hopsReceived(peers, "mid-hop") {
		if hops != 3 {
			t.Fatalf("forward carried hops %d on the wire, want 3", hops)
		}
	}

	// A copy at the limit is stored and ACKed, but goes no further.
	if status := postGossipPutWithHops(t, relay, "peer-0", "last-key", []byte("last"), "at-limit", gossip.MaxForwardHops); status != http.StatusOK {
		t.Fatalf("at-limit copy: relay answered %d", status)
	}
	if data, exists := relay.node.Get("last-key"); !exists || string(data) != "last" {
		t.Fatal("relay did not store a copy at the hop limit")
	}
	waitUntil(t, 2*time.Second, "ACK for the at-limit copy", func() bool {
		return peers[0].count(gossip.MessageTypeAck, "at-limit", "relay") == 1
	})

	// Sentinel: forwards of a later message arrive after any forward of the
	// at-limit copy would have.
	if status := postGossipPut(t, relay, "peer-0", "sentinel-key", []byte("s"), "sentinel"); status != http.StatusOK {
		t.Fatalf("sentinel: relay answered %d", status)
	}
	waitUntil(t, 2*time.Second, "sentinel forwards arrive", func() bool {
		return totalReceived(peers, gossip.MessageTypePut, "sentinel") == wantForwards
	})
	if forwards := totalReceived(peers, gossip.MessageTypePut, "at-limit"); forwards != 0 {
		t.Fatalf("relay forwarded a copy at the hop limit %d times", forwards)
	}
}

// An overwrite is a new write with a new message ID, never a late copy: both
// writes to one key are stored and forwarded, and the relay holds the second.
func TestRelayForwardsBothWritesToOneKey(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	relay, peers, wantForwards := startForwardingRelay(t, ctx)

	for writeIndex, messageID := range []string{"first-write", "second-write"} {
		value := []byte(fmt.Sprintf("value-%d", writeIndex))
		if status := postGossipPut(t, relay, "peer-0", "shared-key", value, messageID); status != http.StatusOK {
			t.Fatalf("%s: relay answered %d", messageID, status)
		}
	}
	waitUntil(t, 2*time.Second, "both writes forwarded", func() bool {
		return totalReceived(peers, gossip.MessageTypePut, "first-write") == wantForwards &&
			totalReceived(peers, gossip.MessageTypePut, "second-write") == wantForwards
	})
	if data, _ := relay.node.Get("shared-key"); string(data) != "value-1" {
		t.Fatalf("relay holds %q, want the second write", data)
	}
}
