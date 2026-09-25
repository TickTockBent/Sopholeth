package storage

import (
	"container/heap"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrStoreFull is returned when a write would exceed the configured capacity.
var ErrStoreFull = errors.New("store capacity exceeded")

const (
	// DefaultSweepInterval is the wait between background sweeper wakeups.
	// The sweeper removes only entries that are already expired, in small
	// batches, so no single wakeup holds the write lock for O(n) work.
	DefaultSweepInterval = 30 * time.Second

	// DefaultSweepBatch caps how many expired entries one sweeper wakeup
	// removes before yielding. Small batches keep each lock hold bounded
	// regardless of how many entries expired at once (#220).
	DefaultSweepBatch = 256

	// DefaultScanPageLimit is the page size ScanPage assumes when the caller
	// passes a non-positive limit.
	DefaultScanPageLimit = 500

	// MaxScanPageLimit caps the page size honored by ScanPage; larger limits
	// are clamped so a single request cannot pull the whole keyspace (#220).
	MaxScanPageLimit = 1000
)

type Entry struct {
	Data      []byte        `json:"data"`
	CreatedAt time.Time     `json:"created_at"`
	TTL       time.Duration `json:"ttl"`
	ExpiresAt time.Time     `json:"expires_at"`
	revision  uint64
}

type MemoryStore struct {
	data         map[string]*Entry
	mutex        sync.RWMutex
	cleanup      chan bool
	maxBytes     int64 // 0 = unlimited
	currentBytes int64
	now          func() time.Time
	sequence     uint64
	subscribers  map[*Subscription]struct{}
	closed       bool

	// keyIndex orders live keys lexicographically so paged and prefix scans
	// cost about the page size instead of a full keyspace sort per request
	// (#220). Mutated only while the write lock is held.
	keyIndex *treapNode

	// expiryHeap is a min-heap of live keys ordered by ExpiresAt (ties broken
	// by the put sequence) so expiry sweeping proceeds incrementally (#220,
	// #162). Mutated only while the write lock is held.
	expiryHeap expiryHeap
}

// NewMemoryStore creates a new store. maxBytes sets the capacity limit in bytes;
// 0 means unlimited. When the limit is reached, writes are rejected with ErrStoreFull.
func NewMemoryStore(maxBytes int64) *MemoryStore {
	store := &MemoryStore{
		data:        make(map[string]*Entry),
		cleanup:     make(chan bool),
		maxBytes:    maxBytes,
		now:         time.Now,
		subscribers: make(map[*Subscription]struct{}),
	}

	go store.sweeper()
	return store
}

func (m *MemoryStore) Put(key string, data []byte, ttl time.Duration) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	newSize := int64(len(data))

	// Account for overwrites: subtract the old entry's size if the key exists
	var oldSize int64
	if existing, exists := m.data[key]; exists {
		oldSize = int64(len(existing.Data))
	}

	if m.maxBytes > 0 && (m.currentBytes-oldSize+newSize) > m.maxBytes {
		return ErrStoreFull
	}

	stored := make([]byte, len(data))
	copy(stored, data)

	now := m.now()
	m.sequence++
	m.data[key] = &Entry{
		Data:      stored,
		CreatedAt: now,
		TTL:       ttl,
		ExpiresAt: now.Add(ttl),
		revision:  m.sequence,
	}

	m.currentBytes = m.currentBytes - oldSize + newSize
	m.keyIndex = treapInsert(m.keyIndex, key, treapPriority(m.sequence))
	heap.Push(&m.expiryHeap, expiryItem{key: key, expiresAt: now.Add(ttl), seq: m.sequence})
	if len(m.subscribers) > 0 {
		m.publishLocked(Event{Kind: "put", Entry: preview(key, m.data[key])})
	}
	return nil
}

func (m *MemoryStore) Get(key string) ([]byte, bool) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	entry, exists := m.data[key]
	if !exists {
		return nil, false
	}

	if !m.now().Before(entry.ExpiresAt) {
		return nil, false
	}

	result := make([]byte, len(entry.Data))
	copy(result, entry.Data)
	return result, true
}

func (m *MemoryStore) GetWithMetadata(key string) ([]byte, time.Time, time.Duration, bool) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	entry, exists := m.data[key]
	if !exists {
		return nil, time.Time{}, 0, false
	}

	if !m.now().Before(entry.ExpiresAt) {
		return nil, time.Time{}, 0, false
	}

	result := make([]byte, len(entry.Data))
	copy(result, entry.Data)
	return result, entry.CreatedAt, entry.TTL, true
}

// sweeper replaces the fixed full-map cleanup pass: on each wakeup it removes
// expired entries in small batches, walking the expiry heap instead of the
// whole keyspace, so no single lock hold grows with the store size (#220).
func (m *MemoryStore) sweeper() {
	timer := time.NewTimer(DefaultSweepInterval)
	defer timer.Stop()
	for {
		select {
		case <-m.cleanup:
			return
		case <-timer.C:
		}
		for m.sweepExpired(DefaultSweepBatch) == DefaultSweepBatch {
		}
		timer.Reset(DefaultSweepInterval)
	}
}

// cleanupExpired removes every entry whose TTL has already elapsed. It is the
// pendant of sweepExpired for callers that want a full pass (tests, shutdown).
func (m *MemoryStore) cleanupExpired() {
	m.sweepExpired(0)
}

// sweepExpired removes up to max entries whose TTL has already elapsed; max
// <= 0 removes everything currently expired. It returns the number of entries
// removed. Removals keep the ordered index and the expiry heap consistent and
// publish the same "expire" events as before. Because capacity is freed as
// soon as entries expire rather than on the next full cleanup pass, a
// capacity-capped store no longer rejects writes for logically-empty space
// (#162).
func (m *MemoryStore) sweepExpired(max int) int {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.closed {
		return 0
	}

	now := m.now()
	removed := 0
	for len(m.expiryHeap) > 0 {
		if max > 0 && removed >= max {
			break
		}
		top := m.expiryHeap[0]
		entry, exists := m.data[top.key]
		if !exists || top.seq != entry.revision {
			// Stale heap clone from an overwritten put; skip it.
			heap.Pop(&m.expiryHeap)
			continue
		}
		if now.Before(entry.ExpiresAt) {
			break // nearest expiry is not due yet
		}
		heap.Pop(&m.expiryHeap)
		m.currentBytes -= int64(len(entry.Data))
		delete(m.data, top.key)
		m.keyIndex = treapDelete(m.keyIndex, top.key)
		m.publishLocked(Event{Kind: "expire", Entry: StreamEntry{Key: top.key, Revision: entry.revision}})
		removed++
	}
	return removed
}

func (m *MemoryStore) Close() {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if !m.closed {
		m.closed = true
		close(m.cleanup)
		for sub := range m.subscribers {
			m.dropLocked(sub)
		}
	}
}

// GetStats returns storage statistics
func (m *MemoryStore) GetStats() (int, int64) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	count := len(m.data)
	var totalSize int64

	for _, entry := range m.data {
		totalSize += int64(len(entry.Data))
	}

	return count, totalSize
}

// Range iterates over all non-expired keys in lexicographic order.
// The callback function receives the key and remaining TTL in seconds
// If the callback returns false, iteration stops
func (m *MemoryStore) Range(fn func(key string, ttl int) bool) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	now := m.now()
	m.keyIndex.walkWhile(func(key string) bool {
		entry, exists := m.data[key]
		if !exists || !now.Before(entry.ExpiresAt) {
			return true // Skip expired entries
		}

		remainingTTL := int(entry.ExpiresAt.Sub(now).Seconds())
		return fn(key, remainingTTL)
	})
}

// Scan returns all non-expired keys in lexicographic order.
func (m *MemoryStore) Scan() []string {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	now := m.now()
	var keys []string
	m.keyIndex.walkWhile(func(key string) bool {
		if entry, exists := m.data[key]; exists && now.Before(entry.ExpiresAt) {
			keys = append(keys, key)
		}
		return true
	})

	return keys
}

// ScanPage returns at most limit non-expired keys in lexicographic order,
// starting strictly after cursor (or from the beginning when cursor is
// empty) and restricted to keys sharing prefix. Non-positive limits fall
// back to DefaultScanPageLimit; limits above MaxScanPageLimit are clamped.
// The store lock is held only for the index walk, so the cost of a request
// tracks the page size rather than the whole keyspace (#220).
func (m *MemoryStore) ScanPage(prefix, cursor string, limit int) []string {
	if limit <= 0 {
		limit = DefaultScanPageLimit
	}
	if limit > MaxScanPageLimit {
		limit = MaxScanPageLimit
	}

	m.mutex.RLock()
	defer m.mutex.RUnlock()

	now := m.now()
	stop := prefixUpperBound(prefix)

	keys := make([]string, 0, limit)
	collect := func(key string) bool {
		if stop != "" && key >= stop {
			return false // lexicographic order: past the prefix range
		}
		if !strings.HasPrefix(key, prefix) {
			return true // not inside the prefix range yet
		}
		entry, exists := m.data[key]
		if !exists || !now.Before(entry.ExpiresAt) {
			return true
		}
		keys = append(keys, key)
		return len(keys) < limit
	}

	if cursor == "" {
		m.keyIndex.walkWhile(collect)
	} else {
		// walkFrom is read-only, so it is safe under the read lock alongside
		// concurrent walkers; keys strictly after the cursor are yielded in
		// ascending order without restructuring the index.
		m.keyIndex.walkFrom(cursor, collect)
	}
	return keys
}

// prefixUpperBound returns the smallest key that sorts after every key
// sharing the given prefix, or "" when the prefix covers the whole space.
func prefixUpperBound(prefix string) string {
	if prefix == "" {
		return ""
	}
	b := []byte(prefix)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < 0xff {
			b[i]++
			return string(b[:i+1])
		}
	}
	return ""
}

// expiryHeap orders live keys by expiry time so sweeping can proceed
// incrementally instead of walking the whole map under the write lock.
type expiryHeap []expiryItem

type expiryItem struct {
	key       string
	expiresAt time.Time
	seq       uint64
}

func (h expiryHeap) Len() int { return len(h) }
func (h expiryHeap) Less(i, j int) bool {
	if h[i].expiresAt.Equal(h[j].expiresAt) {
		return h[i].seq < h[j].seq
	}
	return h[i].expiresAt.Before(h[j].expiresAt)
}
func (h expiryHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *expiryHeap) Push(x any)   { *h = append(*h, x.(expiryItem)) }
func (h *expiryHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// sortKeys orders keys lexicographically. Retained for callers outside the
// storage package that build key lists themselves.
func sortKeys(keys []string) { sort.Strings(keys) }
