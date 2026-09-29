package storage

import (
	"errors"
	"sync"
	"time"
)

// ErrStoreFull is returned when a write would exceed the configured capacity.
var ErrStoreFull = errors.New("store capacity exceeded")

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

	go store.startCleanupWorker()
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
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.cleanupExpired()
		case <-m.cleanup:
			return
		}
	}
}

func (m *MemoryStore) cleanupExpired() {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	now := m.now()
	for key, entry := range m.data {
		if !now.Before(entry.ExpiresAt) {
			m.currentBytes -= int64(len(entry.Data))
			delete(m.data, key)
			m.publishLocked(Event{Kind: "expire", Entry: StreamEntry{Key: key, Revision: entry.revision}})
		}
	}
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

// Range iterates over all non-expired keys
// The callback function receives the key and remaining TTL in seconds
// If the callback returns false, iteration stops
func (m *MemoryStore) Range(fn func(key string, ttl int) bool) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	now := m.now()
	for key, entry := range m.data {
		if !now.Before(entry.ExpiresAt) {
			continue // Skip expired entries
		}

		remainingTTL := int(entry.ExpiresAt.Sub(now).Seconds())
		if !fn(key, remainingTTL) {
			break
		}
	}
}

// Scan returns all non-expired keys
func (m *MemoryStore) Scan() []string {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	var keys []string
	now := m.now()
	for key, entry := range m.data {
		if now.Before(entry.ExpiresAt) { // Not expired
			keys = append(keys, key)
		}
	}

	return keys
}
