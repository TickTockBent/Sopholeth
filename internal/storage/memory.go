package storage

import (
	"container/heap"
	"errors"
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

	// MaxScanPageLimit caps the page size honored by HTTP keysHandler;
	// defined here as a storage-level reference constant.
	MaxScanPageLimit = 1000
)

type expiryItem struct {
	key       string
	expiresAt time.Time
	index     int
}

type expiryHeap []*expiryItem

func (h expiryHeap) Len() int           { return len(h) }
func (h expiryHeap) Less(i, j int) bool { return h[i].expiresAt.Before(h[j].expiresAt) }
func (h expiryHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}
func (h *expiryHeap) Push(x any) {
	item := x.(*expiryItem)
	item.index = len(*h)
	*h = append(*h, item)
}
func (h *expiryHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil // Avoid memory retention
	item.index = -1
	*h = old[:n-1]
	return item
}

type Entry struct {
	Data      []byte        `json:"data"`
	CreatedAt time.Time     `json:"created_at"`
	TTL       time.Duration `json:"ttl"`
	ExpiresAt time.Time     `json:"expires_at"`
	revision  uint64
	// Recency list, newest write first, so a stream snapshot can take the
	// newest entries without scanning the store (#218).
	key          string
	newer, older *Entry
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
	newest       *Entry

	// keyIndex orders live keys lexicographically so paged and prefix scans
	// cost about the page size instead of a full keyspace sort per request (#220).
	keyIndex *treapNode

	// expiryHeap is a min-heap of live keys ordered by ExpiresAt so expiry sweeping
	// proceeds incrementally (#220, #162).
	expiryHeap expiryHeap

	// expiryIndex maps each live key to its item in expiryHeap, allowing O(log n)
	// in-place updates on overwrite and completely eliminating heap growth/leaks.
	expiryIndex map[string]*expiryItem
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
		expiryIndex: make(map[string]*expiryItem),
	}

	go store.startCleanupWorker()
	return store
}

func (m *MemoryStore) Put(key string, data []byte, ttl time.Duration) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	newSize := int64(len(data))

	// Account for overwrites: subtract the old entry's size if the key exists
	var oldSize int64
	existing, exists := m.data[key]
	if exists {
		oldSize = int64(len(existing.Data))
	}

	// Issue #162: If capacity would be exceeded, sweep expired entries first before rejecting
	if m.maxBytes > 0 && (m.currentBytes-oldSize+newSize) > m.maxBytes {
		m.sweepExpiredLocked(0)
		if existing, exists = m.data[key]; exists {
			oldSize = int64(len(existing.Data))
		} else {
			oldSize = 0
		}
		if (m.currentBytes - oldSize + newSize) > m.maxBytes {
			return ErrStoreFull
		}
	}

	stored := make([]byte, len(data))
	copy(stored, data)

	now := m.now()
	m.sequence++
	expiresAt := now.Add(ttl)

	m.data[key] = &Entry{
		Data:      stored,
		CreatedAt: now,
		TTL:       ttl,
		ExpiresAt: expiresAt,
		revision:  m.sequence,
		key:       key,
	}
	if exists {
		m.unlinkLocked(existing)
	}
	m.linkNewestLocked(m.data[key])
	m.currentBytes = m.currentBytes - oldSize + newSize

	// Update or insert into expiryHeap using expiryIndex
	if expItem, hasExp := m.expiryIndex[key]; hasExp {
		// Overwrite: update expiry time in-place and fix heap position
		expItem.expiresAt = expiresAt
		heap.Fix(&m.expiryHeap, expItem.index)
	} else {
		// New key: insert into treap and push new item to min-heap
		m.keyIndex = treapInsert(m.keyIndex, key, randomTreapPriority())
		newItem := &expiryItem{key: key, expiresAt: expiresAt}
		heap.Push(&m.expiryHeap, newItem)
		m.expiryIndex[key] = newItem
	}

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

func (m *MemoryStore) startCleanupWorker() {
	ticker := time.NewTicker(DefaultSweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.sweepBatch()
		case <-m.cleanup:
			return
		}
	}
}

func (m *MemoryStore) sweepBatch() {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	for m.sweepExpiredLocked(DefaultSweepBatch) == DefaultSweepBatch {
	}
}

// cleanupExpired removes every entry whose TTL has elapsed (full pass).
func (m *MemoryStore) cleanupExpired() {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.sweepExpiredLocked(0)
}

// sweepExpired removes up to max expired entries (or all if max <= 0). Exported for tests/benchmarks.
func (m *MemoryStore) sweepExpired(max int) int {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	return m.sweepExpiredLocked(max)
}

func (m *MemoryStore) sweepExpiredLocked(max int) int {
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
		if now.Before(top.expiresAt) {
			break // Next earliest expiry is in the future
		}

		key := top.key
		heap.Pop(&m.expiryHeap)
		delete(m.expiryIndex, key)

		entry, exists := m.data[key]
		if exists {
			m.currentBytes -= int64(len(entry.Data))
			m.unlinkLocked(entry)
			delete(m.data, key)
			m.keyIndex = treapDelete(m.keyIndex, key)
			m.publishLocked(Event{Kind: "expire", Entry: StreamEntry{Key: key, Revision: entry.revision}})
			removed++
		}
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
// The callback function receives the key and remaining TTL in seconds.
// If the callback returns false, iteration stops.
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
// starting strictly after cursor (or from the beginning when cursor is empty)
// and restricted to keys sharing prefix.
// If limit <= 0, all matching keys are returned without bound (#220).
// If limit > 0, at most limit keys are returned.
func (m *MemoryStore) ScanPage(prefix, cursor string, limit int) []string {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	now := m.now()
	stop := prefixUpperBound(prefix)

	var keys []string
	if limit > 0 {
		keys = make([]string, 0, limit)
	}

	collect := func(key string) bool {
		if stop != "" && key >= stop {
			return false // past the prefix range
		}
		if !strings.HasPrefix(key, prefix) {
			return true
		}
		entry, exists := m.data[key]
		if !exists || !now.Before(entry.ExpiresAt) {
			return true
		}
		keys = append(keys, key)
		if limit > 0 && len(keys) >= limit {
			return false
		}
		return true
	}

	// Seeking optimization: jump directly to max(cursor, prefix)
	if cursor != "" {
		if prefix != "" && cursor < prefix {
			m.keyIndex.walkFromOrEqual(prefix, collect)
		} else {
			m.keyIndex.walkFrom(cursor, collect)
		}
	} else if prefix != "" {
		m.keyIndex.walkFromOrEqual(prefix, collect)
	} else {
		m.keyIndex.walkWhile(collect)
	}

	return keys
}

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

func (m *MemoryStore) linkNewestLocked(entry *Entry) {
	entry.older = m.newest
	if m.newest != nil {
		m.newest.newer = entry
	}
	m.newest = entry
}

func (m *MemoryStore) unlinkLocked(entry *Entry) {
	if entry.newer != nil {
		entry.newer.older = entry.older
	} else {
		m.newest = entry.older
	}
	if entry.older != nil {
		entry.older.newer = entry.newer
	}
	entry.newer, entry.older = nil, nil
}
