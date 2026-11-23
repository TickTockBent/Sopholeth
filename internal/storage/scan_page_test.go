package storage

import (
	"container/heap"
	"fmt"
	"math/rand/v2"
	"sync/atomic"
	"testing"
	"time"
)

// newClockStore is a MemoryStore with a controllable clock, following the
// stream_test.go pattern.
func newClockStore(t *testing.T) (*MemoryStore, *atomic.Int64) {
	t.Helper()
	store := newTestStore(0)
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	store.now = func() time.Time { return time.Unix(0, clock.Load()) }
	t.Cleanup(store.Close)
	return store, &clock
}

// TestScanPageOrderedAndPaged checks ordering, paging, and unbounded queries.
func TestScanPageOrderedAndPaged(t *testing.T) {
	store, _ := newClockStore(t)

	for i := 0; i < 10; i++ {
		if err := store.Put(string(rune('a'+i)), []byte("v"), time.Minute); err != nil {
			t.Fatal(err)
		}
	}

	page := store.ScanPage("", "", 3)
	if len(page) != 3 || page[0] != "a" || page[2] != "c" {
		t.Fatalf("page = %v, want [a b c]", page)
	}

	rest := store.ScanPage("", "c", 100)
	if len(rest) != 7 || rest[0] != "d" {
		t.Fatalf("rest = %v, want d..j", rest)
	}

	// Unbounded (limit <= 0) returns all keys
	unbounded := store.ScanPage("", "", 0)
	if len(unbounded) != 10 {
		t.Fatalf("unbounded page = %d keys, want 10 (all)", len(unbounded))
	}
}

// TestScanPagePrefixRange checks prefix filtering against the ordered index.
func TestScanPagePrefixRange(t *testing.T) {
	store, _ := newClockStore(t)

	for i := 0; i < 5; i++ {
		if err := store.Put("user:"+string(rune('a'+i)), []byte("v"), time.Minute); err != nil {
			t.Fatal(err)
		}
		if err := store.Put("other:"+string(rune('a'+i)), []byte("v"), time.Minute); err != nil {
			t.Fatal(err)
		}
	}

	got := store.ScanPage("user:", "", 100)
	if len(got) != 5 {
		t.Fatalf("prefix scan = %v, want 5 user: keys", got)
	}
	for _, k := range got {
		if !startsWith(k, "user:") {
			t.Fatalf("key %q leaks outside prefix", k)
		}
	}
}

// TestScanPageExcludesExpired checks that expired keys are skipped.
func TestScanPageExcludesExpired(t *testing.T) {
	store, clock := newClockStore(t)

	for _, k := range []string{"e1", "e2", "keep"} {
		if err := store.Put(k, []byte("v"), time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	clock.Add(int64(2 * time.Minute))

	got := store.ScanPage("", "", 100)
	if len(got) != 0 {
		t.Fatalf("after expiry ScanPage = %v, want empty", got)
	}
}

// TestScanPageAfterOverwrite checks the cursor against a re-put key: the
// page must start strictly after the cursor even when the cursor key was
// refreshed after earlier keys.
func TestScanPageAfterOverwrite(t *testing.T) {
	store, _ := newClockStore(t)

	for _, k := range []string{"m1", "m2", "m3"} {
		if err := store.Put(k, []byte("v"), time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Put("m1", []byte("v2"), time.Minute); err != nil {
		t.Fatal(err)
	}

	got := store.ScanPage("", "m1", 100)
	if len(got) != 2 || got[0] != "m2" || got[1] != "m3" {
		t.Fatalf("page after m1 = %v, want [m2 m3]", got)
	}
}

// TestSweepExpiredRemovesOnlyExpired checks incremental sweeping: only
// entries past their TTL disappear, live ones stay.
func TestSweepExpiredRemovesOnlyExpired(t *testing.T) {
	store, clock := newClockStore(t)

	if err := store.Put("gone", []byte("v"), time.Second); err != nil {
		t.Fatal(err)
	}
	if err := store.Put("stay", []byte("v"), time.Hour); err != nil {
		t.Fatal(err)
	}
	clock.Add(int64(2 * time.Second))

	if removed := store.sweepExpired(0); removed != 1 {
		t.Fatalf("sweepExpired removed %d, want 1", removed)
	}
	if _, ok := store.Get("gone"); ok {
		t.Fatal("expired entry survived sweep")
	}
	if _, ok := store.Get("stay"); !ok {
		t.Fatal("live entry removed by sweep")
	}
}

// TestSweepExpiredBatchCap checks the batch cap: one sweep removes at most
// max entries even when more are due.
func TestSweepExpiredBatchCap(t *testing.T) {
	store, clock := newClockStore(t)

	for i := 0; i < 10; i++ {
		if err := store.Put(string(rune('a'+i)), []byte("v"), time.Second); err != nil {
			t.Fatal(err)
		}
	}
	clock.Add(int64(2 * time.Second))

	if removed := store.sweepExpired(3); removed != 3 {
		t.Fatalf("capped sweep removed %d, want 3", removed)
	}
	// Remaining expired entries are picked up by the next sweep.
	if removed := store.sweepExpired(0); removed != 7 {
		t.Fatalf("final sweep removed %d, want 7", removed)
	}
}

// TestSweepFreesCapacityImmediately is the #162 regression test.
func TestSweepFreesCapacityImmediately(t *testing.T) {
	store := newTestStore(10) // 10 bytes
	defer store.Close()

	now := time.Unix(1000000000, 0)
	store.now = func() time.Time { return now }

	if err := store.Put("a", []byte("12345"), time.Second); err != nil {
		t.Fatal(err)
	}
	if err := store.Put("b", []byte("12345"), time.Second); err != nil {
		t.Fatal(err)
	}
	if err := store.Put("c", []byte("12345"), time.Second); err != ErrStoreFull {
		t.Fatalf("expected ErrStoreFull, got %v", err)
	}

	// Advance the fake clock past the TTLs; sweep through the public knob.
	now = now.Add(2 * time.Second)
	store.sweepExpired(0)

	if err := store.Put("c", []byte("12345"), time.Minute); err != nil {
		t.Fatalf("write after expiry-sweep rejected: %v", err)
	}
}

// TestPutReclaimsExpiredOnCapacityExceeded verifies #162 is fixed directly in Put:
// when capacity is exceeded, Put automatically reclaims expired entries without
// needing an external sweep call or 30s tick.
func TestPutReclaimsExpiredOnCapacityExceeded(t *testing.T) {
	store := newTestStore(10) // 10 bytes
	defer store.Close()

	now := time.Unix(1000000000, 0)
	store.now = func() time.Time { return now }

	if err := store.Put("a", []byte("12345"), time.Second); err != nil {
		t.Fatal(err)
	}
	if err := store.Put("b", []byte("12345"), time.Second); err != nil {
		t.Fatal(err)
	}

	// Advance time by 2 seconds so "a" and "b" are logically expired
	now = now.Add(2 * time.Second)

	// Writing "c" should succeed because Put reclaims expired space automatically
	if err := store.Put("c", []byte("12345"), time.Minute); err != nil {
		t.Fatalf("Put failed to reclaim expired capacity: %v", err)
	}
}

// TestPutReclaimBounded verifies that Put only reclaims up to DefaultSweepBatch
// entries and stops early once the write fits (#162, #220).
func TestPutReclaimBounded(t *testing.T) {
	store := newTestStore(100) // 100 bytes
	defer store.Close()

	now := time.Unix(1000000000, 0)
	store.now = func() time.Time { return now }

	// Write 10 keys of 10 bytes each = 100 bytes (full capacity)
	for i := 0; i < 10; i++ {
		if err := store.Put(string(rune('a'+i)), []byte("0123456789"), time.Second); err != nil {
			t.Fatal(err)
		}
	}

	// Advance time past expiry
	now = now.Add(2 * time.Second)

	// Writing 20 bytes should only pop 2 expired entries (20 bytes), not all 10 entries!
	if err := store.Put("newkey", []byte("01234567890123456789"), time.Minute); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	store.mutex.RLock()
	remaining := len(store.data)
	store.mutex.RUnlock()

	// 10 initial - 2 reclaimed + 1 new = 9 entries remaining
	if remaining != 9 {
		t.Fatalf("expected 9 entries remaining (bounded reclaim), got %d", remaining)
	}
}

// TestPutOverwriteExpiredSelfEviction verifies the edge case where the key being overwritten
// is expired and popped during capacity reclamation.
func TestPutOverwriteExpiredSelfEviction(t *testing.T) {
	t.Run("self-evicted key fails cleanly if prospective size > maxBytes", func(t *testing.T) {
		store := newTestStore(10)
		defer store.Close()

		now := time.Unix(1000000, 0)
		store.now = func() time.Time { return now }

		if err := store.Put("k1", []byte("1234567890"), time.Second); err != nil {
			t.Fatal(err)
		}
		now = now.Add(2 * time.Second)

		// Overwrite k1 with 15 bytes (> 10 maxBytes)
		err := store.Put("k1", []byte("012345678901234"), time.Minute)
		if err != ErrStoreFull {
			t.Fatalf("expected ErrStoreFull, got %v", err)
		}

		// Store should be cleanly empty with 0 currentBytes
		store.mutex.RLock()
		curBytes := store.currentBytes
		dataLen := len(store.data)
		heapLen := len(store.expiryHeap)
		store.mutex.RUnlock()

		if curBytes != 0 || dataLen != 0 || heapLen != 0 {
			t.Fatalf("store not empty after self-eviction: bytes=%d dataLen=%d heapLen=%d",
				curBytes, dataLen, heapLen)
		}
	})

	t.Run("self-evicted key re-inserted cleanly when second key popped", func(t *testing.T) {
		store := newTestStore(10)
		defer store.Close()

		now := time.Unix(1000000, 0)
		store.now = func() time.Time { return now }

		_ = store.Put("k1", []byte("12345"), time.Second)
		_ = store.Put("k2", []byte("67890"), 2*time.Second)
		now = now.Add(3 * time.Second)

		// Overwrite k1 with 8 bytes; pops k1 and k2, then inserts k1 as new entry
		err := store.Put("k1", []byte("12345678"), time.Minute)
		if err != nil {
			t.Fatalf("Put k1 failed: %v", err)
		}

		store.mutex.RLock()
		curBytes := store.currentBytes
		dataLen := len(store.data)
		entry := store.data["k1"]
		store.mutex.RUnlock()

		if curBytes != 8 || dataLen != 1 || entry == nil || entry.heapIndex != 0 {
			t.Fatalf("unexpected state: curBytes=%d dataLen=%d entry=%v", curBytes, dataLen, entry)
		}
	})
}

// TestExpiryHeapNoLeakOnOverwrite directly checks Problem 1:
// Overwriting a single key 10,000 times must not grow the heap.
func TestExpiryHeapNoLeakOnOverwrite(t *testing.T) {
	store, _ := newClockStore(t)

	for i := 0; i < 10000; i++ {
		if err := store.Put("hotkey", []byte("payload"), time.Hour); err != nil {
			t.Fatal(err)
		}
	}

	store.mutex.RLock()
	heapLen := len(store.expiryHeap)
	dataLen := len(store.data)
	entry := store.data["hotkey"]
	store.mutex.RUnlock()

	if heapLen != 1 {
		t.Fatalf("expiryHeap length = %d after 10000 overwrites, want 1", heapLen)
	}
	if dataLen != 1 {
		t.Fatalf("data length = %d after 10000 overwrites, want 1", dataLen)
	}
	if entry == nil || entry.heapIndex != 0 {
		t.Fatalf("expected entry with heapIndex 0, got %v", entry)
	}
}

// TestScanKeepsExpiredUntilSwept documents the visibility contract: Scan
// skips expired entries even before the sweeper runs.
func TestScanKeepsExpiredUntilSwept(t *testing.T) {
	store, clock := newClockStore(t)

	_ = store.Put("x", []byte("v"), time.Second)
	clock.Add(int64(2 * time.Second))
	if got := store.Scan(); len(got) != 0 {
		t.Fatalf("Scan = %v after expiry, want empty", got)
	}
	store.sweepExpired(0)
}

// TestTreapInvariantsAndRotations verifies BST and priority heap properties.
func TestTreapInvariantsAndRotations(t *testing.T) {
	var root *treapNode
	const n = 1000
	keys := make([]string, n)
	for i := 0; i < n; i++ {
		keys[i] = fmt.Sprintf("k%05d", i)
		root = treapInsert(root, keys[i], randomTreapPriority())
	}

	validateTreapTree(t, root, "", "", false, false)

	// Verify all keys are traversed in order
	idx := 0
	root.walkWhile(func(k string) bool {
		if k != keys[idx] {
			t.Fatalf("in-order violation at %d: got %q, want %q", idx, k, keys[idx])
		}
		idx++
		return true
	})
	if idx != n {
		t.Fatalf("walkWhile visited %d, want %d", idx, n)
	}

	// Delete half the keys and re-verify invariants
	for i := 0; i < n; i += 2 {
		root = treapDelete(root, keys[i])
	}
	validateTreapTree(t, root, "", "", false, false)
}

// TestTreapWalkFromCorrectness verifies walkFrom with inclusive=true/false.
func TestTreapWalkFromCorrectness(t *testing.T) {
	var root *treapNode
	items := []string{"b", "d", "f", "h", "j"}
	for _, k := range items {
		root = treapInsert(root, k, randomTreapPriority())
	}

	// inclusive=true at exact key
	var got []string
	root.walkFrom("d", true, func(k string) bool { got = append(got, k); return true })
	if fmt.Sprint(got) != "[d f h j]" {
		t.Fatalf("walkFrom('d', true) = %v, want [d f h j]", got)
	}

	// inclusive=false at exact key (cursor pagination)
	got = nil
	root.walkFrom("d", false, func(k string) bool { got = append(got, k); return true })
	if fmt.Sprint(got) != "[f h j]" {
		t.Fatalf("walkFrom('d', false) = %v, want [f h j]", got)
	}

	// between keys
	got = nil
	root.walkFrom("e", true, func(k string) bool { got = append(got, k); return true })
	if fmt.Sprint(got) != "[f h j]" {
		t.Fatalf("walkFrom('e', true) = %v, want [f h j]", got)
	}
}

// TestExpiryHeapDirectInvariants verifies container/heap methods and heapIndex tracking.
func TestExpiryHeapDirectInvariants(t *testing.T) {
	var h expiryHeap
	now := time.Now()
	const n = 100
	entries := make([]*Entry, n)

	for i := 0; i < n; i++ {
		e := &Entry{
			key:       fmt.Sprintf("k%d", i),
			ExpiresAt: now.Add(time.Duration(rand.IntN(1000)) * time.Second),
		}
		entries[i] = e
		heap.Push(&h, e)
	}

	for i, e := range h {
		if e.heapIndex != i {
			t.Fatalf("heapIndex out of sync at %d: got %d", i, e.heapIndex)
		}
	}

	// Update in place and heap.Fix
	for i := 0; i < n; i++ {
		entries[i].ExpiresAt = now.Add(time.Duration(rand.IntN(1000)) * time.Second)
		heap.Fix(&h, entries[i].heapIndex)
	}

	// Pop all
	var prev time.Time
	for h.Len() > 0 {
		e := heap.Pop(&h).(*Entry)
		if e.heapIndex != -1 {
			t.Fatalf("popped entry heapIndex = %d, want -1", e.heapIndex)
		}
		if !prev.IsZero() && e.ExpiresAt.Before(prev) {
			t.Fatalf("heap pop out of order: prev %v, cur %v", prev, e.ExpiresAt)
		}
		prev = e.ExpiresAt
	}
}

func validateTreapTree(t *testing.T, node *treapNode, minKey, maxKey string, hasMin, hasMax bool) {
	t.Helper()
	if node == nil {
		return
	}
	if hasMin && node.key <= minKey {
		t.Fatalf("BST violation: %q <= min %q", node.key, minKey)
	}
	if hasMax && node.key >= maxKey {
		t.Fatalf("BST violation: %q >= max %q", node.key, maxKey)
	}
	if node.left != nil && node.left.priority > node.priority {
		t.Fatalf("Heap violation: left priority %d > node %d", node.left.priority, node.priority)
	}
	if node.right != nil && node.right.priority > node.priority {
		t.Fatalf("Heap violation: right priority %d > node %d", node.right.priority, node.priority)
	}
	validateTreapTree(t, node.left, minKey, node.key, hasMin, true)
	validateTreapTree(t, node.right, node.key, maxKey, true, hasMax)
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
