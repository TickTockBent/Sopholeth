package storage

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStreamSnapshotAndMutations(t *testing.T) {
	m := NewMemoryStore(0)
	defer m.Close()
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	m.mutex.Lock()
	m.now = func() time.Time { return time.Unix(0, clock.Load()) }
	m.mutex.Unlock()
	large := bytes.Repeat([]byte{0xff}, StreamPreviewBytes+1)
	m.Put("large", large, time.Minute)
	m.Put("empty", nil, time.Minute)
	m.Put("expired", nil, -time.Second)
	snapshot, sub, err := m.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if len(snapshot.Entries) != 2 {
		t.Fatalf("snapshot: %+v", snapshot)
	}
	for _, entry := range snapshot.Entries {
		if entry.Key == "large" && (!entry.Truncated || len(entry.Payload) != StreamPreviewBytes || entry.Size != len(large)) {
			t.Fatal(entry)
		}
		if entry.Key == "empty" && (entry.Payload == nil || entry.Truncated || entry.Size != 0) {
			t.Fatal(entry)
		}
	}
	m.Put("key", []byte("old"), time.Second)
	old := <-sub.Events()
	clock.Add(int64(2 * time.Second))
	m.cleanupExpired()
	// The pre-snapshot expired value also produces an advisory expiry.
	removed := map[string]bool{}
	for i := 0; i < 2; i++ {
		event := <-sub.Events()
		removed[event.Entry.Key] = true
		if event.Entry.Key == "key" {
			if event.Kind != "expire" || event.Entry.Revision != old.Entry.Revision {
				t.Fatal(event)
			}
		}
	}
	if !removed["key"] || !removed["expired"] {
		t.Fatal(removed)
	}
	m.Put("key", []byte("replacement"), time.Minute)
	fresh := <-sub.Events()
	if fresh.Kind != "put" || fresh.Entry.Revision <= old.Entry.Revision || string(fresh.Entry.Payload) != "replacement" {
		t.Fatal(fresh)
	}
	m.cleanupExpired()
	select {
	case event := <-sub.Events():
		t.Fatalf("replacement incorrectly expired: %+v", event)
	default:
	}
	if got, _ := m.Get("large"); !bytes.Equal(got, large) {
		t.Fatal("preview changed stored payload")
	}
}

func TestStreamSnapshotHandoffDuringWrites(t *testing.T) {
	for attempt := 0; attempt < 50; attempt++ {
		m := NewMemoryStore(0)
		done := make(chan struct{})
		go func() {
			defer close(done)
			for i := 0; i < 32; i++ {
				m.Put(fmt.Sprint(i), []byte("value"), time.Minute)
			}
		}()
		snapshot, sub, err := m.Subscribe()
		if err != nil {
			t.Fatal(err)
		}
		<-done
		seen := map[string]bool{}
		for _, entry := range snapshot.Entries {
			seen[entry.Key] = true
		}
		for len(sub.events) > 0 {
			event := <-sub.Events()
			if seen[event.Entry.Key] {
				t.Fatal("write appeared in both snapshot and event queue")
			}
			seen[event.Entry.Key] = true
		}
		if len(seen) != 32 {
			t.Fatalf("lost writes at handoff: %d", len(seen))
		}
		sub.Close()
		m.Close()
	}
}

func TestStreamOverflowAndLimits(t *testing.T) {
	m := NewMemoryStore(0)
	defer m.Close()
	_, sub, _ := m.Subscribe()
	for i := 0; i <= StreamQueueSize; i++ {
		if err := m.Put("key", []byte(fmt.Sprint(i)), time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-sub.Done():
	default:
		t.Fatal("slow subscriber not dropped")
	}
	snapshot, fresh, err := m.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	if string(snapshot.Entries[0].Payload) != fmt.Sprint(StreamQueueSize) {
		t.Fatal("write blocked by full queue")
	}
	fresh.Close()
	for i := 0; i < StreamSubscribers; i++ {
		_, s, err := m.Subscribe()
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
	}
	if _, _, err := m.Subscribe(); !errors.Is(err, ErrStreamLimit) {
		t.Fatal(err)
	}
	m.Close()
	m.Close()
	if _, _, err := m.Subscribe(); !errors.Is(err, ErrStoreClosed) {
		t.Fatal(err)
	}
}

func TestStreamSnapshotKeepsNewestWithinBounds(t *testing.T) {
	for _, mode := range []string{"entries", "bytes"} {
		t.Run(mode, func(t *testing.T) {
			m := NewMemoryStore(0)
			defer m.Close()
			total := StreamSnapshotEntries + 10
			payload := []byte(nil)
			if mode == "bytes" {
				total, payload = 800, make([]byte, StreamPreviewBytes)
			}
			for i := 0; i < total; i++ {
				m.Put(fmt.Sprintf("%05d", i), payload, time.Minute)
			}
			m.Put("expired", nil, -time.Second)
			snapshot, sub, err := m.Subscribe()
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			kept := len(snapshot.Entries)
			if kept == 0 || kept >= total || snapshot.Omitted != total-kept {
				t.Fatalf("kept %d of %d, omitted %d", kept, total, snapshot.Omitted)
			}
			if mode == "entries" && kept != StreamSnapshotEntries {
				t.Fatalf("kept %d, want %d", kept, StreamSnapshotEntries)
			}
			// Newest first, and exactly the most recent writes.
			for i, entry := range snapshot.Entries {
				if want := fmt.Sprintf("%05d", total-1-i); entry.Key != want {
					t.Fatalf("entry %d is %q, want %q", i, entry.Key, want)
				}
			}
			m.Put("after", nil, time.Minute)
			if event := <-sub.Events(); event.Entry.Key != "after" {
				t.Fatal(event)
			}
		})
	}
}

func TestStreamOversizedEntryDoesNotAffectViewers(t *testing.T) {
	m := NewMemoryStore(0)
	defer m.Close()
	oversized := strings.Repeat("k", streamEventBytes)
	m.Put(oversized, nil, time.Minute)
	m.Put("small", []byte("v"), time.Minute)
	snapshot, sub, err := m.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Key != "small" || snapshot.Omitted != 1 {
		t.Fatalf("%+v", snapshot)
	}
	// A live write of the oversized key is skipped; the viewer stays
	// subscribed and receives the next write.
	m.Put(oversized, []byte("again"), time.Minute)
	m.Put("next", nil, time.Minute)
	select {
	case event := <-sub.Events():
		if event.Entry.Key != "next" {
			t.Fatal(event.Kind, len(event.Entry.Key))
		}
	case <-sub.Done():
		t.Fatal("oversized write disconnected the viewer")
	}
}

func TestRejectedWriteHasNoEvent(t *testing.T) {
	m := NewMemoryStore(1)
	defer m.Close()
	_, sub, _ := m.Subscribe()
	defer sub.Close()
	if err := m.Put("key", []byte("too large"), time.Minute); !errors.Is(err, ErrStoreFull) {
		t.Fatal(err)
	}
	select {
	case event := <-sub.Events():
		t.Fatal(event)
	default:
	}
}

func BenchmarkPutWithStream(b *testing.B) {
	for _, viewers := range []int{0, 1, StreamSubscribers} {
		b.Run(fmt.Sprintf("viewers=%d", viewers), func(b *testing.B) {
			m := NewMemoryStore(0)
			defer m.Close()
			subs := make([]*Subscription, viewers)
			for i := range subs {
				_, subs[i], _ = m.Subscribe()
			}
			payload := make([]byte, StreamPreviewBytes)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m.Put("key", payload, time.Minute)
				for _, sub := range subs {
					<-sub.Events()
				}
			}
		})
	}
}

// The recency list must mirror the store through overwrites and sweeps.
func TestRecencyListTracksStore(t *testing.T) {
	m := NewMemoryStore(0)
	defer m.Close()
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	m.mutex.Lock()
	m.now = func() time.Time { return time.Unix(0, clock.Load()) }
	m.mutex.Unlock()
	for i := 0; i < 2000; i++ {
		ttl := time.Minute
		if i%3 == 0 {
			ttl = time.Second
		}
		m.Put(fmt.Sprint(i%700), nil, ttl)
	}
	check := func() {
		m.mutex.RLock()
		defer m.mutex.RUnlock()
		count, last := 0, uint64(0)
		for entry := m.newest; entry != nil; entry = entry.older {
			if m.data[entry.key] != entry || (entry.newer != nil && entry.newer.older != entry) || (count > 0 && entry.revision >= last) {
				t.Fatalf("list out of sync at %q", entry.key)
			}
			last = entry.revision
			count++
		}
		if count != len(m.data) {
			t.Fatalf("list has %d entries, store has %d", count, len(m.data))
		}
	}
	check()
	clock.Add(int64(2 * time.Second))
	m.cleanupExpired()
	check()
}
