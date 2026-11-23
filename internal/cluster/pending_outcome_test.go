package cluster

import (
	"context"
	"errors"
	"testing"
	"time"

	"sopholeth/internal/gossip"
	"sopholeth/internal/storage"
)

// Regression tests for #170: when the caller's context ends while the write
// waits for quorum, Put reports the write as stored and pending, because the
// local store already happened. A context that ended before the store, or an
// error before the store, is still a failure.
//
// The origin's peer is a recordingPeer that never ACKs, so quorum stalls, and
// the write timeout is far longer than any test, so only the context ends
// the wait.

const stalledQuorumWriteTimeout = time.Hour

func startStalledQuorumOrigin(t *testing.T, ctx context.Context) (*testNode, map[string]*recordingPeer) {
	t.Helper()
	origin, peers := startAckOrigin(t, ctx, 3, "silent-peer")
	origin.node.writeTimeout = stalledQuorumWriteTimeout
	return origin, peers
}

func expectStoredLocally(t *testing.T, origin *testNode, key, value string) {
	t.Helper()
	stored, exists := origin.node.Get(key)
	if !exists {
		t.Fatalf("origin has no local copy of %s", key)
	}
	if string(stored) != value {
		t.Fatalf("origin holds %q for %s, want %q", stored, key, value)
	}
}

// A deadline shorter than NODE_WRITE_TIMEOUT, such as a handler or proxy
// deadline, ends the wait: the write is pending, not failed.
func TestPutPendingWhenDeadlineEndsStalledQuorum(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	origin, peers := startStalledQuorumOrigin(t, ctx)

	deadlineCtx, cancelDeadline := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancelDeadline()
	err := origin.node.Put(deadlineCtx, "deadline-key", []byte("deadline-value"), 300*time.Second)
	if !errors.Is(err, ErrQuorumTimeout) {
		t.Fatalf("Put returned %v, want %v", err, ErrQuorumTimeout)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Put returned %v, want it to wrap %v", err, context.DeadlineExceeded)
	}
	expectStoredLocally(t, origin, "deadline-key", "deadline-value")
	if messageID := awaitPutMessageID(t, peers, "deadline-key"); messageID == "" {
		t.Fatal("the write was not offered to the peer")
	}
	if pending := pendingWriteCount(origin); pending != 0 {
		t.Fatalf("%d writes still pending after Put returned", pending)
	}
}

// A client that leaves while the write waits for quorum gets the same
// answer: the value is stored and was offered to the enclave.
func TestPutPendingWhenClientCancelsDuringQuorumWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	origin, peers := startStalledQuorumOrigin(t, ctx)

	clientCtx, cancelClient := context.WithCancel(ctx)
	defer cancelClient()
	result := startPut(clientCtx, origin, "cancelled-key")
	awaitPutMessageID(t, peers, "cancelled-key")
	cancelClient()

	select {
	case err := <-result:
		if !errors.Is(err, ErrQuorumTimeout) || !errors.Is(err, context.Canceled) {
			t.Fatalf("Put returned %v, want %v wrapping %v", err, ErrQuorumTimeout, context.Canceled)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Put did not return after the client cancelled")
	}
	expectStoredLocally(t, origin, "cancelled-key", "value-cancelled-key")
}

// A context that ended before Put stored anything is a failure, not
// pending: nothing is stored and nothing is offered to the enclave.
func TestPutFailsWhenContextEndedBeforeStore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	origin, peers := startStalledQuorumOrigin(t, ctx)

	goneCtx, cancelGone := context.WithCancel(ctx)
	cancelGone()
	err := origin.node.Put(goneCtx, "gone-key", []byte("never-stored"), 300*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Put returned %v, want %v", err, context.Canceled)
	}
	if errors.Is(err, ErrQuorumTimeout) {
		t.Fatalf("Put reported %v as pending; nothing was stored", err)
	}
	if _, exists := origin.node.Get("gone-key"); exists {
		t.Fatal("origin stored a write whose caller had already gone")
	}
	if pending := pendingWriteCount(origin); pending != 0 {
		t.Fatalf("%d writes pending for a write that never started", pending)
	}
	time.Sleep(100 * time.Millisecond)
	if received := peers["silent-peer"].countType(gossip.MessageTypePut); received != 0 {
		t.Fatalf("peer received %d PUTs for a write that never started", received)
	}
}

// An error before the store stays a failure even though quorum would stall
// and the context ends while Put runs.
func TestPutFailsOnPreStoreErrorWithStalledQuorum(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	origin := newTestNodeWithStorage(t, "origin", "default", 3, 64)
	origin.node.writeTimeout = stalledQuorumWriteTimeout
	origin.start(t, ctx, nil)
	t.Cleanup(origin.stop)
	peer := newRecordingPeer(t)
	registerFakePeerInEnclave(t, origin, "silent-peer", "default", peer.port())

	deadlineCtx, cancelDeadline := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancelDeadline()
	err := origin.node.Put(deadlineCtx, "too-big", make([]byte, 128), 300*time.Second)
	if !errors.Is(err, storage.ErrStoreFull) {
		t.Fatalf("Put returned %v, want %v", err, storage.ErrStoreFull)
	}
	if errors.Is(err, ErrQuorumTimeout) {
		t.Fatalf("Put reported %v as pending; the store failed", err)
	}
	if _, exists := origin.node.Get("too-big"); exists {
		t.Fatal("a write the store rejected is readable")
	}
}
