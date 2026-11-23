package gossip

import "time"

// seenEntry is one message ID in the dedup cache with the time it stops
// counting as a duplicate.
type seenEntry struct {
	messageID string
	expiry    time.Time
}

// seenCache is the gossip dedup cache: a bounded, insertion-ordered set of
// message IDs (#168).
//
// Every ID gets the same lifetime, so insertion order is expiry order. The
// IDs sit in a ring buffer oldest first, with a map for lookup. Expired IDs
// are dropped from the front of the ring. When the cache is full, the single
// oldest ID is dropped to make room for the new one, so the cache always holds
// the most recent IDs it was given, however tightly they are clustered in
// time. The previous policy guessed a time midpoint and could drop every
// entry at once, reopening messages still in flight for a second store, ACK
// and forward.
//
// Memory is bounded by capacity: the map never holds more than capacity IDs
// and the ring never grows past capacity slots. The ring starts small and
// doubles as needed, so an idle node does not pay for a full-size buffer.
//
// seenCache is not safe for concurrent use; the caller holds a lock.
type seenCache struct {
	capacity int
	lifetime time.Duration
	now      func() time.Time

	expiryByID map[string]time.Time
	ring       []seenEntry // oldest entry at ring[head]
	head       int
	count      int
}

const initialSeenRingSize = 1024

func newSeenCache(capacity int, lifetime time.Duration, now func() time.Time) *seenCache {
	if capacity < 1 {
		capacity = 1
	}
	if now == nil {
		now = time.Now
	}
	return &seenCache{
		capacity:   capacity,
		lifetime:   lifetime,
		now:        now,
		expiryByID: make(map[string]time.Time),
	}
}

// markSeen records messageID. It returns true if the ID was already present
// and unexpired (a duplicate), and false if it is new. evictedForCapacity is
// the number of unexpired IDs dropped to make room, which is 0 or 1.
func (c *seenCache) markSeen(messageID string) (duplicate bool, evictedForCapacity int) {
	currentTime := c.now()
	c.dropExpired(currentTime)

	if expiry, present := c.expiryByID[messageID]; present {
		if currentTime.Before(expiry) {
			return true, 0
		}
		// Only reachable if the clock stepped backwards and left an expired
		// ID behind a newer one. Treat it as new; the stale ring slot is
		// skipped when it reaches the front.
		delete(c.expiryByID, messageID)
	}

	if c.count >= c.capacity {
		c.dropOldest()
		evictedForCapacity = 1
	}
	c.push(seenEntry{messageID: messageID, expiry: currentTime.Add(c.lifetime)})
	return false, evictedForCapacity
}

// dropExpired removes expired IDs from the front of the ring. Cost is
// proportional to the number removed.
func (c *seenCache) dropExpired(currentTime time.Time) {
	for c.count > 0 {
		oldest := c.ring[c.head]
		if currentTime.Before(oldest.expiry) {
			return
		}
		c.dropOldest()
	}
}

// dropOldest removes the entry at the front of the ring.
func (c *seenCache) dropOldest() {
	oldest := c.ring[c.head]
	c.ring[c.head] = seenEntry{} // release the ID string
	c.head = (c.head + 1) % len(c.ring)
	c.count--
	// The map entry may belong to a newer slot for the same ID (see
	// markSeen); only delete it if this slot is the one it records.
	if expiry, present := c.expiryByID[oldest.messageID]; present && expiry.Equal(oldest.expiry) {
		delete(c.expiryByID, oldest.messageID)
	}
}

func (c *seenCache) push(entry seenEntry) {
	if c.count == len(c.ring) {
		c.grow()
	}
	tail := (c.head + c.count) % len(c.ring)
	c.ring[tail] = entry
	c.count++
	c.expiryByID[entry.messageID] = entry.expiry
}

// grow doubles the ring, capped at capacity, and unwraps it so head is 0.
func (c *seenCache) grow() {
	newSize := len(c.ring) * 2
	if newSize == 0 {
		newSize = initialSeenRingSize
	}
	if newSize > c.capacity {
		newSize = c.capacity
	}
	grown := make([]seenEntry, newSize)
	for offset := 0; offset < c.count; offset++ {
		grown[offset] = c.ring[(c.head+offset)%len(c.ring)]
	}
	c.ring = grown
	c.head = 0
}

// len reports the number of IDs held.
func (c *seenCache) len() int {
	return c.count
}

// cleanup drops expired IDs. markSeen already does this on every call; the
// periodic cleanup releases memory on a node that has gone quiet.
func (c *seenCache) cleanup() {
	c.dropExpired(c.now())
}
