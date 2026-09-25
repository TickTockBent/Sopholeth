package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sopholeth/internal/cluster"
	"sopholeth/internal/gossip"
)

func TestHTTPWriteLimits(t *testing.T) {
	s, cleanup := newTestServer(t)
	defer cleanup()
	router := s.Router()
	const maxValue = 100 * 1024
	for _, peer := range []bool{false, true} {
		for _, size := range []int{0, maxValue, maxValue + 1, 800 * 1024} {
			for _, chunked := range []bool{false, true} {
				t.Run(fmt.Sprintf("peer=%t/bytes=%d/chunked=%t", peer, size, chunked), func(t *testing.T) {
					key := fmt.Sprintf("limit-%t-%d-%t", peer, size, chunked)
					data := bytes.Repeat([]byte("x"), size)
					method, path, body := http.MethodPut, "/v1/data/"+key, data
					want := http.StatusCreated
					if peer {
						method, path, want = http.MethodPost, "/v1/gossip/message", http.StatusOK
						var err error
						body, err = json.Marshal(gossip.SimpleMessage{Type: "PUT", From: "stranger", Key: key, Data: data, TTL: 300, MessageID: key})
						if err != nil {
							t.Fatal(err)
						}
					}
					if size > maxValue {
						want = http.StatusRequestEntityTooLarge
					}
					req := httptest.NewRequest(method, path, bytes.NewReader(body))
					if chunked {
						req.ContentLength = -1
						req.TransferEncoding = []string{"chunked"}
					}
					w := httptest.NewRecorder()
					router.ServeHTTP(w, req)
					if w.Code != want {
						t.Fatalf("status = %d, want %d: %s", w.Code, want, w.Body.String())
					}
					stored, exists := s.clusterNode.Get(key)
					if exists != (size <= maxValue) || exists && !bytes.Equal(stored, data) {
						t.Fatal("write admission disagrees with stored data")
					}
				})
			}
		}
	}
}

func TestHTTPConfiguredValueLimit(t *testing.T) {
	limits := cluster.DefaultWriteLimits()
	limits.MaxValueBytes = 101 * 1024
	s, cleanup := newTestServerWithLimits(t, "", limits)
	defer cleanup()
	for _, size := range []int{limits.MaxValueBytes, limits.MaxValueBytes + 1} {
		w := httptest.NewRecorder()
		s.Router().ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/v1/data/configured", bytes.NewReader(make([]byte, size))))
		want := http.StatusCreated
		if size > limits.MaxValueBytes {
			want = http.StatusRequestEntityTooLarge
		}
		if w.Code != want {
			t.Fatalf("size=%d: status=%d, want=%d", size, w.Code, want)
		}
	}
}

func TestHTTPKeyLimit(t *testing.T) {
	s, cleanup := newTestServer(t)
	defer cleanup()
	for _, peer := range []bool{false, true} {
		for _, key := range []string{strings.Repeat("é", 512), strings.Repeat("é", 512) + "x"} {
			t.Run(fmt.Sprintf("peer=%t/bytes=%d", peer, len(key)), func(t *testing.T) {
				method, path, body, want := http.MethodPut, "/v1/data/"+key, []byte{}, http.StatusCreated
				if peer {
					method, path, want = http.MethodPost, "/v1/gossip/message", http.StatusOK
					body, _ = json.Marshal(gossip.SimpleMessage{Type: "PUT", From: "stranger", Key: key, TTL: 300, MessageID: key})
				}
				if len(key) > 1024 {
					want = http.StatusRequestEntityTooLarge
				}
				w := httptest.NewRecorder()
				s.Router().ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewReader(body)))
				_, exists := s.clusterNode.Get(key)
				if w.Code != want || exists != (len(key) <= 1024) {
					t.Fatalf("status=%d, want=%d, stored=%t: %s", w.Code, want, exists, w.Body.String())
				}
			})
		}
	}
}

func TestPeerTTLClamped(t *testing.T) {
	s, cleanup := newTestServer(t)
	defer cleanup()
	for _, tc := range []struct {
		ttl, want int32
	}{{-1, 300}, {0, 300}, {1, 300}, {600, 600}, {315360000, 86400}} {
		key := fmt.Sprintf("ttl-%d", tc.ttl)
		body, err := json.Marshal(gossip.SimpleMessage{Type: "PUT", From: "stranger", Key: key, Data: []byte("data"), TTL: tc.ttl, MessageID: key})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		s.Router().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/gossip/message", bytes.NewReader(body)))
		_, _, ttl, exists := s.clusterNode.GetWithMetadata(key)
		if w.Code != http.StatusOK || !exists || ttl != time.Duration(tc.want)*time.Second {
			t.Fatalf("peer ttl=%d: status=%d, stored=%t, ttl=%s", tc.ttl, w.Code, exists, ttl)
		}
	}
}
