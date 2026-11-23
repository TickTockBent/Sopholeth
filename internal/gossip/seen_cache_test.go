package gossip

import (
	"fmt"
	"testing"
	"time"
)

// manualClock is a test clock that only moves when told to.
type manualClock struct {
	current time.Time
}

func (clock *manualClock) now() time.Time { return clock.current }

func (clock *manualClock) advance(step time.Duration) { clock.current = clock.current.Add(step) }

func newTestSeenCache(capacity int, lifetime time.Duration) (*seenCache, *manualClock) {
	clock := &manualClock{current: time.Unix(1_700_000_000, 0)}
	return newSeenCache(capacity, lifetime, clock.now), clock
}

func seenID(index int) string { return fmt.Sprintf("msg-%d", index) }

// assertHeldExactly checks that the cache holds the IDs in [firstHeld, lastHeld]
// and none of the IDs in [0, firstHeld), without changing the cache.
func assertHeldExactly(t *testing.T, cache *seenCache, firstHeld, lastHeld int) {
	t.Helper()
	for index := 0; index <= lastHeld; index++ {
		_, present := cache.expiryByID[seenID(index)]
		shouldBePresent := index >= firstHeld
		if present != shouldBePresent {
			t.Fatalf("%s present = %v, want %v (expected IDs %d..%d)",
				seenID(index), present, shouldBePresent, firstHeld, lastHeld)
		}
	}
	if cache.len() != lastHeld-firstHeld+1 {
		t.Fatalf("cache len = %d, want %d", cache.len(), lastHeld-firstHeld+1)
	}
}

func assertWithinBounds(t *testing.T, cache *seenCache) {
	t.Helper()
	if cache.len() > cache.capacity {
		t.Fatalf("cache len %d exceeds capacity %d", cache.len(), cache.capacity)
	}
	if len(cache.expiryByID) > cache.capacity {
		t.Fatalf("map holds %d IDs, capacity %d", len(cache.expiryByID), cache.capacity)
	}
	if len(cache.ring) > cache.capacity {
		t.Fatalf("ring has %d slots, capacity %d", len(cache.ring), cache.capacity)
	}
}

// The #168 regression: every ID arrives at the same instant, so every expiry
// is identical. Overflowing must drop only the oldest IDs, one per insert.
func TestSeenCacheSameInstantFillKeepsNewest(t *testing.T) {
	const capacity = 5000
	cache, _ := newTestSeenCache(capacity, time.Minute)

	const overflow = 1234
	for index := 0; index < capacity+overflow; index++ {
		duplicate, evicted := cache.markSeen(seenID(index))
		if duplicate {
			t.Fatalf("%s reported as duplicate on first sight", seenID(index))
		}
		wantEvicted := 0
		if index >= capacity {
			wantEvicted = 1
		}
		if evicted != wantEvicted {
			t.Fatalf("insert %d evicted %d, want %d", index, evicted, wantEvicted)
		}
	}

	assertHeldExactly(t, cache, overflow, capacity+overflow-1)
	assertWithinBounds(t, cache)
}

// IDs arrive in tight clusters a few milliseconds apart, the shape that made
// the old midpoint land past every expiry.
func TestSeenCacheClusteredTimestampsKeepsNewest(t *testing.T) {
	const capacity = 3000
	cache, clock := newTestSeenCache(capacity, time.Minute)

	const clusterSize = 700
	const clusters = 9 // 6300 IDs, more than twice the capacity
	nextIndex := 0
	for cluster := 0; cluster < clusters; cluster++ {
		for member := 0; member < clusterSize; member++ {
			cache.markSeen(seenID(nextIndex))
			nextIndex++
		}
		clock.advance(3 * time.Millisecond)
	}

	assertHeldExactly(t, cache, nextIndex-capacity, nextIndex-1)
	assertWithinBounds(t, cache)

	// Every held ID is still reported as a duplicate: redelivery of an
	// in-flight message is suppressed.
	for index := nextIndex - capacity; index < nextIndex; index++ {
		if duplicate, _ := cache.markSeen(seenID(index)); !duplicate {
			t.Fatalf("%s should be a duplicate", seenID(index))
		}
	}
}

// Sustained pressure well past capacity, with time moving but never far
// enough for anything to expire: the cache always holds exactly the most
// recent capacity IDs, and its memory never grows past capacity.
func TestSeenCacheContinuousPressure(t *testing.T) {
	const capacity = 2048
	cache, clock := newTestSeenCache(capacity, time.Minute)

	const total = capacity * 20
	for index := 0; index < total; index++ {
		cache.markSeen(seenID(index))
		clock.advance(time.Microsecond)

		if index%997 == 0 || index == total-1 {
			assertWithinBounds(t, cache)
			oldestHeld := index - capacity + 1
			if oldestHeld < 0 {
				oldestHeld = 0
			}
			if _, present := cache.expiryByID[seenID(index)]; !present {
				t.Fatalf("newest ID %s missing", seenID(index))
			}
			if _, present := cache.expiryByID[seenID(oldestHeld)]; !present {
				t.Fatalf("oldest retained ID %s missing at insert %d", seenID(oldestHeld), index)
			}
			if oldestHeld > 0 {
				if _, present := cache.expiryByID[seenID(oldestHeld-1)]; present {
					t.Fatalf("%s should have been evicted at insert %d", seenID(oldestHeld-1), index)
				}
			}
		}
	}
	if cache.len() != capacity || len(cache.expiryByID) != capacity {
		t.Fatalf("len = %d, map = %d, want both %d", cache.len(), len(cache.expiryByID), capacity)
	}
}

// Expiry still works: once the lifetime passes, IDs are dropped and a
// redelivered ID is new again. Expiry is not counted as a capacity eviction.
func TestSeenCacheExpiry(t *testing.T) {
	cache, clock := newTestSeenCache(100, time.Minute)

	for index := 0; index < 50; index++ {
		cache.markSeen(seenID(index))
	}
	clock.advance(30 * time.Second)
	for index := 50; index < 80; index++ {
		cache.markSeen(seenID(index))
	}

	clock.advance(30 * time.Second) // first 50 are now exactly at expiry
	cache.cleanup()
	assertHeldExactly(t, cache, 50, 79)

	duplicate, evicted := cache.markSeen(seenID(0))
	if duplicate || evicted != 0 {
		t.Fatalf("expired ID: duplicate=%v evicted=%d, want false 0", duplicate, evicted)
	}

	clock.advance(time.Minute)
	cache.cleanup()
	if cache.len() != 0 || len(cache.expiryByID) != 0 {
		t.Fatalf("after full expiry: len=%d map=%d, want 0", cache.len(), len(cache.expiryByID))
	}
}

// Expiry happens on insert too, so a cache that is full of expired IDs makes
// room without counting a capacity eviction.
func TestSeenCacheExpiredEntriesMakeRoomFirst(t *testing.T) {
	cache, clock := newTestSeenCache(10, time.Minute)
	for index := 0; index < 10; index++ {
		cache.markSeen(seenID(index))
	}
	clock.advance(2 * time.Minute)

	_, evicted := cache.markSeen("fresh")
	if evicted != 0 {
		t.Fatalf("evicted = %d, want 0: expired IDs should make room", evicted)
	}
	if cache.len() != 1 {
		t.Fatalf("len = %d, want 1", cache.len())
	}
}

// The ring starts small and grows only as needed, never past capacity.
func TestSeenCacheRingGrowsLazily(t *testing.T) {
	const capacity = 5000
	cache, _ := newTestSeenCache(capacity, time.Minute)
	if len(cache.ring) != 0 {
		t.Fatalf("new cache allocated %d ring slots", len(cache.ring))
	}
	cache.markSeen("first")
	if len(cache.ring) != initialSeenRingSize {
		t.Fatalf("ring = %d slots after one insert, want %d", len(cache.ring), initialSeenRingSize)
	}
	for index := 0; index < capacity*3; index++ {
		cache.markSeen(seenID(index))
	}
	if len(cache.ring) != capacity {
		t.Fatalf("ring = %d slots, want capped at %d", len(cache.ring), capacity)
	}
}

// Growing while the ring is wrapped keeps entries in insertion order.
func TestSeenCacheGrowPreservesOrderWhenWrapped(t *testing.T) {
	cache, clock := newTestSeenCache(10_000, time.Second)

	// Fill the initial ring, expire half from the front, then refill so the
	// tail wraps around before the ring has to grow.
	for index := 0; index < initialSeenRingSize/2; index++ {
		cache.markSeen(seenID(index))
	}
	clock.advance(time.Second)
	for index := initialSeenRingSize / 2; index < initialSeenRingSize; index++ {
		cache.markSeen(seenID(index)) // drops the expired first half on the way
	}
	if cache.head == 0 {
		t.Fatal("test setup: expected a non-zero head before wrapping")
	}
	nextIndex := initialSeenRingSize
	for ; nextIndex < initialSeenRingSize*2; nextIndex++ {
		cache.markSeen(seenID(nextIndex))
	}
	if len(cache.ring) <= initialSeenRingSize {
		t.Fatalf("test setup: ring did not grow (%d slots)", len(cache.ring))
	}

	for offset := 0; offset < cache.count; offset++ {
		want := seenID(initialSeenRingSize/2 + offset)
		got := cache.ring[(cache.head+offset)%len(cache.ring)].messageID
		if got != want {
			t.Fatalf("ring position %d = %s, want %s", offset, got, want)
		}
	}
	assertWithinBounds(t, cache)
}

// A clock step backwards must not break bookkeeping: a stale ID is treated
// as new and the cache stays within bounds.
func TestSeenCacheClockStepBackwards(t *testing.T) {
	cache, clock := newTestSeenCache(4, time.Minute)
	clock.advance(time.Hour)
	cache.markSeen("a") // expires an hour from the base time plus a minute
	clock.advance(-time.Hour)
	cache.markSeen("b") // expires earlier than "a" but sits behind it

	clock.advance(time.Hour + 30*time.Second)
	// "b" is expired but stuck behind unexpired "a".
	if duplicate, _ := cache.markSeen("b"); duplicate {
		t.Fatal("expired b should be new")
	}
	for _, id := range []string{"c", "d", "e", "f"} {
		cache.markSeen(id)
	}
	assertWithinBounds(t, cache)
	if duplicate, _ := cache.markSeen("f"); !duplicate {
		t.Fatal("f should be a duplicate")
	}
}
