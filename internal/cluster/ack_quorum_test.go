package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"sopholeth/internal/gossip"
)

// Regression tests for #164: the origin counts each sender's ACK at most once
// per write, only from replication peers (same-enclave peers in its own peer
// table now, or when the write started), never under its own ID, and against
// a quorum target fixed when the write started.
//
// The origin is a real node behind its real gossip HTTP handler. Its peers
// are recordingPeers: they receive PUTs and never ACK, so every ACK the
// origin counts is one the test posted, over HTTP, as a peer would.

// ackTestWriteTimeout bounds the writes that are expected to end in 202.
const ackTestWriteTimeout = 400 * time.Millisecond

// registerFakePeerInEnclave adds a peer in enclave through the bootstrap path.
func registerFakePeerInEnclave(t *testing.T, node *testNode, peerID, enclave string, port int) {
	t.Helper()
	response := node.node.HandleBootstrap(&gossip.BootstrapRequest{
		NodeID: peerID, Address: "127.0.0.1", GossipPort: port, HTTPPort: port, Enclave: enclave,
	})
	if !response.Success {
		t.Fatalf("bootstrap registration of %s rejected", peerID)
	}
}

// startAckOrigin starts an origin with the given replication factor and a
// recording peer, in the default enclave, for each peer ID.
func startAckOrigin(t *testing.T, ctx context.Context, replicationFactor int, peerIDs ...string) (*testNode, map[string]*recordingPeer) {
	t.Helper()
	origin := newTestNode(t, "origin", "default", replicationFactor)
	origin.node.writeTimeout = ackTestWriteTimeout
	origin.start(t, ctx, nil)
	t.Cleanup(origin.stop)
	peers := make(map[string]*recordingPeer, len(peerIDs))
	for _, peerID := range peerIDs {
		peer := newRecordingPeer(t)
		registerFakePeerInEnclave(t, origin, peerID, "default", peer.port())
		peers[peerID] = peer
	}
	return origin, peers
}

// postGossipAck delivers an ACK claiming to come from sender to node's gossip
// endpoint and returns the HTTP status. The handler processes the ACK before
// it answers, so the ACK has been counted or ignored when this returns.
func postGossipAck(t *testing.T, node *testNode, sender gossip.NodeID, key, messageID string) int {
	t.Helper()
	body, err := json.Marshal(gossip.SimpleMessage{
		Type:      string(gossip.MessageTypeAck),
		From:      string(sender),
		To:        string(node.node.localNode.ID),
		Key:       key,
		Timestamp: time.Now().Unix(),
		MessageID: messageID,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	response, err := http.Post("http://"+node.addr()+"/v1/gossip/message", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST gossip ACK: %v", err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	return response.StatusCode
}

func ackFrom(t *testing.T, node *testNode, sender gossip.NodeID, key, messageID string) {
	t.Helper()
	if status := postGossipAck(t, node, sender, key, messageID); status != http.StatusOK {
		t.Fatalf("ACK from %s answered %d, want 200", sender, status)
	}
}

// startPut runs origin.Put in the background and returns its result channel.
func startPut(ctx context.Context, origin *testNode, key string) <-chan error {
	result := make(chan error, 1)
	go func() {
		result <- origin.node.Put(ctx, key, []byte("value-"+key), 300*time.Second)
	}()
	return result
}

// putMessageID returns the ID of the first PUT for key this peer received
// from the origin, or "".
func (p *recordingPeer) putMessageID(key string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, received := range p.messages {
		if received.Type == string(gossip.MessageTypePut) && received.Key == key && received.From == "origin" {
			return received.MessageID
		}
	}
	return ""
}

// awaitPutMessageID waits until any of peers has received the PUT for key
// and returns its message ID.
func awaitPutMessageID(t *testing.T, peers map[string]*recordingPeer, key string) string {
	t.Helper()
	var messageID string
	waitUntil(t, 2*time.Second, "a peer receives the PUT for "+key, func() bool {
		for _, peer := range peers {
			if id := peer.putMessageID(key); id != "" {
				messageID = id
				return true
			}
		}
		return false
	})
	return messageID
}

func expectPutResult(t *testing.T, result <-chan error, want error, description string) {
	t.Helper()
	select {
	case err := <-result:
		if !errors.Is(err, want) {
			t.Fatalf("%s: Put returned %v, want %v", description, err, want)
		}
	case <-time.After(ackTestWriteTimeout + 3*time.Second):
		t.Fatalf("%s: Put did not return", description)
	}
}

func pendingWriteCount(node *testNode) int {
	node.node.writesMutex.RLock()
	defer node.node.writesMutex.RUnlock()
	return len(node.node.pendingWrites)
}

func isPending(node *testNode, messageID string) bool {
	node.node.writesMutex.RLock()
	defer node.node.writesMutex.RUnlock()
	_, pending := node.node.pendingWrites[messageID]
	return pending
}

// With RF 5 and four peers the quorum is 3: the local copy and two peers.
// One peer ACKing twice is one replica, so the write must end in 202. On main
// before #164 the second ACK was counted again and the write reported 201.
// The control write shows two distinct peers do reach quorum.
func TestDuplicateAckFromOnePeerCountsOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	origin, peers := startAckOrigin(t, ctx, 5, "peer-a", "peer-b", "peer-c", "peer-d")

	result := startPut(ctx, origin, "duplicate")
	messageID := awaitPutMessageID(t, peers, "duplicate")
	ackFrom(t, origin, "peer-a", "duplicate", messageID)
	ackFrom(t, origin, "peer-a", "duplicate", messageID)
	expectPutResult(t, result, ErrQuorumTimeout, "one peer ACKing twice")

	control := startPut(ctx, origin, "distinct")
	controlID := awaitPutMessageID(t, peers, "distinct")
	ackFrom(t, origin, "peer-a", "distinct", controlID)
	ackFrom(t, origin, "peer-a", "distinct", controlID)
	ackFrom(t, origin, "peer-b", "distinct", controlID)
	expectPutResult(t, control, nil, "two distinct peers")
}

// With RF 3 and two peers the quorum is 2, so any one counted ACK completes
// the write. An ACK from an ID with no record in the origin's peer table is
// not counted; one from a peer is.
func TestAckFromUnknownSenderIsNotCounted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	origin, peers := startAckOrigin(t, ctx, 3, "peer-a", "peer-b")

	result := startPut(ctx, origin, "unknown")
	messageID := awaitPutMessageID(t, peers, "unknown")
	ackFrom(t, origin, "stranger", "unknown", messageID)
	ackFrom(t, origin, "", "unknown", messageID)
	expectPutResult(t, result, ErrQuorumTimeout, "ACKs from unknown senders")

	control := startPut(ctx, origin, "known")
	controlID := awaitPutMessageID(t, peers, "known")
	ackFrom(t, origin, "peer-a", "known", controlID)
	expectPutResult(t, control, nil, "ACK from a replication peer")
}

// A peer in the origin's table but in another enclave is not a replication
// peer: its ACK is not counted.
func TestAckFromOtherEnclaveIsNotCounted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	origin, peers := startAckOrigin(t, ctx, 3, "peer-a")
	otherEnclavePeer := newRecordingPeer(t)
	registerFakePeerInEnclave(t, origin, "blue-peer", "blue", otherEnclavePeer.port())
	if len(origin.node.Topology()) != 2 {
		t.Fatalf("origin knows %d peers, want 2", len(origin.node.Topology()))
	}

	result := startPut(ctx, origin, "cross-enclave")
	messageID := awaitPutMessageID(t, peers, "cross-enclave")
	ackFrom(t, origin, "blue-peer", "cross-enclave", messageID)
	expectPutResult(t, result, ErrQuorumTimeout, "ACK from another enclave")
	if otherEnclavePeer.putMessageID("cross-enclave") != "" {
		t.Fatal("the PUT reached a peer in another enclave")
	}
}

// The local store is the origin's own vote. An ACK claiming the origin's ID
// is not a second one.
func TestAckClaimingOwnIDIsNotCounted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	origin, peers := startAckOrigin(t, ctx, 3, "peer-a", "peer-b")

	result := startPut(ctx, origin, "self")
	messageID := awaitPutMessageID(t, peers, "self")
	ackFrom(t, origin, "origin", "self", messageID)
	expectPutResult(t, result, ErrQuorumTimeout, "ACK claiming the origin's ID")
}

// Above the fanout threshold the origin sends to √N peers, and relays ACK the
// origin directly. An ACK from a same-enclave peer the origin did not send
// to counts.
func TestAckFromRelayedPeerAboveFanoutThresholdCounts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peerIDs := make([]string, gossip.FanoutThreshold+1)
	for i := range peerIDs {
		peerIDs[i] = fmt.Sprintf("peer-%02d", i)
	}
	origin, peers := startAckOrigin(t, ctx, 3, peerIDs...)

	result := startPut(ctx, origin, "relayed")
	directTargets := expectedFanout(len(peerIDs))
	waitUntil(t, 2*time.Second, "the origin's fanout delivers", func() bool {
		delivered := 0
		for _, peer := range peers {
			if peer.putMessageID("relayed") != "" {
				delivered++
			}
		}
		return delivered == directTargets
	})
	messageID := awaitPutMessageID(t, peers, "relayed")
	relayedPeerID := ""
	for peerID, peer := range peers {
		if peer.putMessageID("relayed") == "" {
			relayedPeerID = peerID
			break
		}
	}
	if relayedPeerID == "" {
		t.Fatal("every peer received the PUT directly; no relayed peer to test")
	}
	ackFrom(t, origin, gossip.NodeID(relayedPeerID), "relayed", messageID)
	expectPutResult(t, result, nil, "ACK from a peer the origin did not send to")
}

// An ACK for a write that timed out or completed finds nothing pending: it is
// ignored, does not panic, and does not bring the write back.
func TestLateAckIsIgnored(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	origin, peers := startAckOrigin(t, ctx, 3, "peer-a", "peer-b")

	timedOut := startPut(ctx, origin, "timed-out")
	timedOutID := awaitPutMessageID(t, peers, "timed-out")
	expectPutResult(t, timedOut, ErrQuorumTimeout, "write with no ACKs")
	ackFrom(t, origin, "peer-a", "timed-out", timedOutID)
	ackFrom(t, origin, "peer-b", "timed-out", timedOutID)
	if isPending(origin, timedOutID) {
		t.Fatal("a late ACK resurrected a timed-out write")
	}

	completed := startPut(ctx, origin, "completed")
	completedID := awaitPutMessageID(t, peers, "completed")
	ackFrom(t, origin, "peer-a", "completed", completedID)
	expectPutResult(t, completed, nil, "write with one peer ACK")
	ackFrom(t, origin, "peer-b", "completed", completedID)
	ackFrom(t, origin, "peer-a", "completed", completedID)
	if isPending(origin, completedID) {
		t.Fatal("a late ACK resurrected a completed write")
	}
	if leftover := pendingWriteCount(origin); leftover != 0 {
		t.Fatalf("%d pending writes left after every Put returned", leftover)
	}
}

// A peer that joins after the write started does not raise its quorum
// target. With RF 5 and two peers the target is 2; two more peers join
// mid-write, which would make a fresh target 3, and one ACK from a newly
// joined peer still completes the write. On main before #164 the target was
// recomputed on every ACK, so this write timed out.
func TestPeerJoiningMidWriteDoesNotRaiseQuorum(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	origin, peers := startAckOrigin(t, ctx, 5, "peer-a", "peer-b")

	result := startPut(ctx, origin, "join")
	messageID := awaitPutMessageID(t, peers, "join")
	for _, joinerID := range []string{"joiner-c", "joiner-d"} {
		registerFakePeerInEnclave(t, origin, joinerID, "default", newRecordingPeer(t).port())
	}
	if replicationPeers := len(origin.node.protocol.GetReplicationPeers()); replicationPeers != 4 {
		t.Fatalf("origin has %d replication peers, want 4", replicationPeers)
	}
	// A new peer's ACK counts: it is a replication peer at ACK time.
	ackFrom(t, origin, "joiner-c", "join", messageID)
	expectPutResult(t, result, nil, "one ACK against the target fixed at start")
}
