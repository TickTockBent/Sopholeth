package cluster

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"sopholeth/internal/gossip"
	"sopholeth/internal/storage"
)

type writeObserver struct {
	acks      int
	forwarded []*gossip.Message
}

func (o *writeObserver) RouteAck(*gossip.Message) bool         { o.acks++; return true }
func (o *writeObserver) BroadcastToChildren(m *gossip.Message) { o.forwarded = append(o.forwarded, m) }

func TestConfiguredWriteLimits(t *testing.T) {
	cn := NewClusterNode("local", "localhost", 0, 8080, 1, 0, time.Second, "", "default")
	t.Cleanup(cn.store.(*storage.MemoryStore).Close)
	limits := WriteLimits{MaxValueBytes: 8, MaxKeyBytes: 16, MinTTLSeconds: 600, MaxTTLSeconds: 3600}
	if err := cn.SetWriteLimits(limits); err != nil {
		t.Fatal(err)
	}
	o := &writeObserver{}
	cn.SetAckRouter(o)
	cn.SetChildBroadcaster(o)

	// Rejections must have no storage, ACK, forwarding, or dedup side effects.
	for _, tc := range []struct {
		key  string
		data []byte
		want error
	}{
		{"value-limit", make([]byte, 9), ErrValueTooLarge},
		{strings.Repeat("k", 17), nil, ErrKeyTooLong},
	} {
		if err := cn.Put(context.Background(), tc.key, tc.data, time.Hour); !errors.Is(err, tc.want) {
			t.Fatalf("local admission: %v, want %v", err, tc.want)
		}
		msg := &gossip.Message{Type: gossip.MessageTypePut, From: "stranger", Key: tc.key, Data: tc.data, TTL: 3600, MessageID: "retry-" + tc.key}
		beforeACK, beforeForward := o.acks, len(o.forwarded)
		if err := cn.HandleGossipMessage(msg); !errors.Is(err, tc.want) {
			t.Fatalf("peer admission: %v, want %v", err, tc.want)
		}
		if _, exists := cn.Get(tc.key); exists || o.acks != beforeACK || len(o.forwarded) != beforeForward || len(cn.pendingWrites) != 0 {
			t.Fatal("rejected write produced side effects")
		}
		msg.Key, msg.Data = "corrected", []byte("valid")
		if err := cn.HandleGossipMessage(msg); err != nil {
			t.Fatal(err)
		}
		if o.acks != beforeACK+1 || len(o.forwarded) != beforeForward+1 {
			t.Fatal("rejection poisoned retry dedup")
		}
	}

	// Apply custom bounds to local and peer writes, with no duration overflow.
	for i, seconds := range []int{-1, 1800, math.MaxInt} {
		want := time.Duration(min(max(seconds, 600), 3600)) * time.Second
		key := fmt.Sprintf("ttl-%d", i)
		msg := &gossip.Message{Type: gossip.MessageTypePut, From: "stranger", Key: key, Data: []byte("12345678"), TTL: seconds, MessageID: key}
		if err := cn.HandleGossipMessage(msg); err != nil {
			t.Fatal(err)
		}
		_, _, ttl, exists := cn.GetWithMetadata(key)
		if !exists || ttl != want || o.forwarded[len(o.forwarded)-1].TTL != int(want/time.Second) || msg.TTL != seconds {
			t.Fatalf("stored/forwarded TTL mismatch for %d seconds", seconds)
		}
		localTTL := time.Duration(seconds) * time.Second
		if seconds == math.MaxInt {
			localTTL = time.Duration(math.MaxInt64)
		}
		if err := cn.Put(context.Background(), key, msg.Data, localTTL); err != nil {
			t.Fatal(err)
		}
		_, _, ttl, _ = cn.GetWithMetadata(key)
		if ttl != want {
			t.Fatalf("local TTL = %s, want %s", ttl, want)
		}
	}
}

func TestWriteLimitConfiguration(t *testing.T) {
	cn := NewClusterNode("local", "localhost", 0, 8080, 1, 0, time.Second, "", "default")
	t.Cleanup(cn.store.(*storage.MemoryStore).Close)
	original := cn.WriteLimits()
	for _, invalid := range []WriteLimits{
		{0, 1024, 300, 86400}, {102400, -1, 300, 86400},
		{102400, 1024, 300, 299}, {102400, 1024, 600, 300},
	} {
		if err := cn.SetWriteLimits(invalid); err == nil || cn.WriteLimits() != original {
			t.Fatalf("invalid policy accepted or partially applied: %+v", invalid)
		}
	}
	original.MinTTLSeconds = 1
	if err := cn.SetWriteLimits(original); err != nil {
		t.Fatal(err)
	}
	if cn.WriteLimits().MinTTLSeconds != 300 {
		t.Fatal("protocol TTL floor bypassed")
	}
}
