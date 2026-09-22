package storage

import (
	"errors"
	"time"
)

// Streaming is a bounded observation of the current store, with no replay log.
const (
	StreamPreviewBytes    = 4096
	StreamSubscribers     = 8
	StreamQueueSize       = 64
	StreamSnapshotBytes   = 4 << 20
	StreamSnapshotEntries = 4096
	streamEventBytes      = 16 << 10
)

var (
	ErrStreamLimit   = errors.New("stream connection limit reached")
	ErrSnapshotLimit = errors.New("store snapshot exceeds stream limits")
	ErrStoreClosed   = errors.New("store is closed")
)

// StreamEntry owns a preview copy, never the stored payload. Treat previews
// received from a subscription as immutable: subscribers share these copies.
type StreamEntry struct {
	Key        string    `json:"key"`
	Payload    []byte    `json:"payload"`
	Truncated  bool      `json:"truncated"`
	Size       int       `json:"size"`
	TTLSeconds float64   `json:"ttl_seconds"`
	WrittenAt  time.Time `json:"written_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Revision   uint64    `json:"revision,string"`
}

type Snapshot struct {
	Now     time.Time     `json:"now"`
	Entries []StreamEntry `json:"entries"`
}

type Event struct {
	Kind  string
	Entry StreamEntry
}

type Subscription struct {
	store  *MemoryStore
	events chan Event
	done   chan struct{}
}

func (s *Subscription) Events() <-chan Event  { return s.events }
func (s *Subscription) Done() <-chan struct{} { return s.done }
func (s *Subscription) Close() {
	s.store.mutex.Lock()
	defer s.store.mutex.Unlock()
	if _, ok := s.store.subscribers[s]; ok {
		s.store.dropLocked(s)
	}
}

// Subscribe captures the snapshot and registers for subsequent mutations in
// one critical section. Encoding and network I/O happen after the lock is
// released. Oversized snapshots fail as a whole, never silently omit keys.
func (m *MemoryStore) Subscribe() (Snapshot, *Subscription, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.closed {
		return Snapshot{}, nil, ErrStoreClosed
	}
	if len(m.subscribers) >= StreamSubscribers {
		return Snapshot{}, nil, ErrStreamLimit
	}
	snapshot := Snapshot{Now: m.now(), Entries: make([]StreamEntry, 0)}
	budget := 0
	for key, entry := range m.data {
		if !snapshot.Now.Before(entry.ExpiresAt) {
			continue
		}
		size := previewCost(key, len(entry.Data))
		budget += size
		if size > streamEventBytes || budget > StreamSnapshotBytes || len(snapshot.Entries) >= StreamSnapshotEntries {
			return Snapshot{}, nil, ErrSnapshotLimit
		}
		snapshot.Entries = append(snapshot.Entries, preview(key, entry))
	}
	sub := &Subscription{store: m, events: make(chan Event, StreamQueueSize), done: make(chan struct{})}
	m.subscribers[sub] = struct{}{}
	return snapshot, sub, nil
}

// Conservative JSON size estimate: keys can expand sixfold when escaped.
func previewCost(key string, size int) int {
	if size > StreamPreviewBytes {
		size = StreamPreviewBytes
	}
	return len(key)*6 + (size+2)/3*4 + 512
}

func preview(key string, entry *Entry) StreamEntry {
	n := len(entry.Data)
	if n > StreamPreviewBytes {
		n = StreamPreviewBytes
	}
	data := make([]byte, n)
	copy(data, entry.Data[:n])
	return StreamEntry{Key: key, Payload: data, Truncated: n < len(entry.Data), Size: len(entry.Data),
		TTLSeconds: entry.TTL.Seconds(), WrittenAt: entry.CreatedAt, ExpiresAt: entry.ExpiresAt, Revision: entry.revision}
}

func (m *MemoryStore) publishLocked(event Event) {
	for sub := range m.subscribers {
		if previewCost(event.Entry.Key, len(event.Entry.Payload)) > streamEventBytes {
			m.dropLocked(sub)
			continue
		}
		select {
		case sub.events <- event:
		default:
			m.dropLocked(sub)
		}
	}
}

func (m *MemoryStore) dropLocked(sub *Subscription) {
	delete(m.subscribers, sub)
	close(sub.done)
}
