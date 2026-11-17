package cluster

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"sopholeth/internal/gossip"
)

// #164 tests that inspect the write's bookkeeping or replace the node's
// peer check. They use fixtures from ack_quorum_test.go.

// pendingWriteOperation returns the pending write for messageID.
func pendingWriteOperation(t *testing.T, node *testNode, messageID string) *WriteOperation {
	t.Helper()
	node.node.writesMutex.RLock()
	defer node.node.writesMutex.RUnlock()
	writeOp, pending := node.node.pendingWrites[messageID]
	if !pending {
		t.Fatalf("write %s is not pending", messageID)
	}
	return writeOp
}

// Eight peers ACK at once against a quorum of 5 (RF 9: the local copy and
// four peers). Exactly the target is counted: the ACK that reaches it
// completes and removes the write in the same critical section, and every
// later ACK finds nothing pending.
func TestConcurrentAcksCompleteWriteOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peerIDs := make([]string, 8)
	for i := range peerIDs {
		peerIDs[i] = fmt.Sprintf("peer-%d", i)
	}
	origin, peers := startAckOrigin(t, ctx, 9, peerIDs...)

	result := startPut(ctx, origin, "concurrent")
	messageID := awaitPutMessageID(t, peers, "concurrent")
	writeOp := pendingWriteOperation(t, origin, messageID)

	start := make(chan struct{})
	var ackers sync.WaitGroup
	for _, peerID := range peerIDs {
		for repeat := 0; repeat < 2; repeat++ {
			ackers.Add(1)
			go func() {
				defer ackers.Done()
				<-start
				if status := postGossipAck(t, origin, gossip.NodeID(peerID), "concurrent", messageID); status != http.StatusOK {
					t.Errorf("ACK from %s answered %d, want 200", peerID, status)
				}
			}()
		}
	}
	close(start)
	ackers.Wait()
	expectPutResult(t, result, nil, "concurrent ACKs from eight peers")

	origin.node.writesMutex.RLock()
	defer origin.node.writesMutex.RUnlock()
	if writeOp.quorumTarget != 5 {
		t.Fatalf("quorum target %d, want 5", writeOp.quorumTarget)
	}
	if counted := writeOp.confirmations(); counted != writeOp.quorumTarget {
		t.Fatalf("counted %d senders, want exactly the target %d", counted, writeOp.quorumTarget)
	}
	if !writeOp.completed {
		t.Fatal("write not marked completed")
	}
	if _, pending := origin.node.pendingWrites[messageID]; pending {
		t.Fatal("completed write still pending")
	}
}

// A peer evicted after the write started still counts: it was a
// replication peer in the snapshot Put took. The eviction is simulated by
// replacing the node's current peer check, since real eviction takes three
// 30-second health checks. A sender in neither the snapshot nor the current
// table still does not count.
func TestPeerEvictedMidWriteStillCounts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	origin, peers := startAckOrigin(t, ctx, 3, "peer-a", "peer-b")
	// The ACK path reads the check under writesMutex, so replace it there.
	var peerAEvicted atomic.Bool
	origin.node.writesMutex.Lock()
	currentPeerCheck := origin.node.isReplicationPeer
	origin.node.isReplicationPeer = func(id gossip.NodeID) bool {
		if id == "peer-a" && peerAEvicted.Load() {
			return false
		}
		return currentPeerCheck(id)
	}
	origin.node.writesMutex.Unlock()

	result := startPut(ctx, origin, "evicted")
	messageID := awaitPutMessageID(t, peers, "evicted")
	peerAEvicted.Store(true)
	ackFrom(t, origin, "stranger", "evicted", messageID)
	if !isPending(origin, messageID) {
		t.Fatal("an ACK from a stranger completed the write")
	}
	ackFrom(t, origin, "peer-a", "evicted", messageID)
	expectPutResult(t, result, nil, "ACK from a peer evicted mid-write")
}
