package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sopholeth/internal/client"
	"sopholeth/internal/gossip"
	"sopholeth/internal/storage"
)

func readEvent(t *testing.T, reader *bufio.Reader) (string, map[string]json.RawMessage) {
	t.Helper()
	var kind string
	var data map[string]json.RawMessage
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(line, "event: ") {
			kind = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
		}
		if strings.HasPrefix(line, "data: ") {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data); err != nil {
				t.Fatal(err)
			}
		}
		if line == "\n" && kind != "" {
			return kind, data
		}
	}
}

func TestStreamThroughRouter(t *testing.T) {
	s, cleanup := newTestServer(t)
	defer cleanup()
	s.streamDone = make(chan struct{})
	srv := httptest.NewServer(s.Router())
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := client.New(srv.URL, srv.Client())
	if _, err := c.Put(ctx, "before", []byte("initial"), 300); err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/v1/stream", nil)
	request.Header.Set("Origin", "http://localhost:8181")
	response, err := srv.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "text/event-stream" || response.Header.Get("Access-Control-Allow-Origin") != "http://localhost:8181" {
		t.Fatal(response.Status, response.Header)
	}
	reader := bufio.NewReader(response.Body)
	kind, data := readEvent(t, reader)
	var entries []storage.StreamEntry
	json.Unmarshal(data["entries"], &entries)
	if kind != "snapshot" || len(entries) != 1 || string(entries[0].Payload) != "initial" || string(data["node"]) != `"test-node"` {
		t.Fatal(kind, data)
	}
	if _, err := c.Put(ctx, "before", []byte("overwrite"), 600); err != nil {
		t.Fatal(err)
	}
	kind, data = readEvent(t, reader)
	var entry storage.StreamEntry
	json.Unmarshal(data["entry"], &entry)
	if kind != "put" || string(entry.Payload) != "overwrite" || entry.TTLSeconds != 600 || entry.Revision <= entries[0].Revision {
		t.Fatal(kind, entry)
	}
	if err := s.clusterNode.HandleGossipMessage(&gossip.Message{Type: gossip.MessageTypePut, Key: "replica", Data: []byte("from peer"), TTL: 300, MessageID: "stream-replicated", From: "peer"}); err != nil {
		t.Fatal(err)
	}
	kind, data = readEvent(t, reader)
	json.Unmarshal(data["entry"], &entry)
	if kind != "put" || entry.Key != "replica" || string(entry.Payload) != "from peer" {
		t.Fatal(kind, entry)
	}
	close(s.streamDone)
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatalf("shutdown did not close stream cleanly: %v", err)
	}
}

func TestStreamDisabledAndConnectionLimit(t *testing.T) {
	s, cleanup := newTestServer(t)
	defer cleanup()
	s.streamDisabled = true
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, httptest.NewRequest("GET", "/v1/stream", nil))
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
	s.streamDisabled = false
	s.streamActive.Store(storage.StreamSubscribers)
	w = httptest.NewRecorder()
	s.Router().ServeHTTP(w, httptest.NewRequest("GET", "/v1/stream", nil))
	if w.Code != 503 || s.streamActive.Load() != storage.StreamSubscribers {
		t.Fatal(w.Code, s.streamActive.Load())
	}
}
