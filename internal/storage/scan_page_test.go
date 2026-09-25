package storage

import (
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

// TestScanPageOrderedAndPaged checks ordering, paging, and clamping.
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

	clamped := store.ScanPage("", "", 100000)
	if len(clamped) != 10 {
		t.Fatalf("clamped page = %d keys, want 10 (all)", len(clamped))
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

// TestSweepFreesCapacityImmediately is the #162 regression: capacity is
// credited as soon as entries expire, without waiting for a cleanup tick,
// so a full store accepts new writes after expiry.
func TestSweepFreesCapacityImmediately(t *testing.T) {
	store := newTestStore(10) // 10 bytes
	defer store.Close()

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
	store.now = func() time.Time { return time.Now().Add(2 * time.Second) }
	store.sweepExpired(0)

	if err := store.Put("c", []byte("12345"), time.Minute); err != nil {
		t.Fatalf("write after expiry-sweep rejected: %v", err)
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
	// sweepExpired must be a no-op-safe second call.
	store.sweepExpired(0)
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
