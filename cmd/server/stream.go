package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"sopholeth/internal/storage"
)

const streamWriteTimeout = 5 * time.Second

func (s *HTTPServer) streamHandler(w http.ResponseWriter, r *http.Request) {
	if s.streamDisabled {
		http.Error(w, "stream disabled on this node", http.StatusNotFound)
		return
	}
	if s.streamActive.Add(1) > storage.StreamSubscribers {
		s.streamActive.Add(-1)
		w.Header().Set("Retry-After", "5")
		http.Error(w, storage.ErrStreamLimit.Error(), http.StatusServiceUnavailable)
		return
	}
	defer s.streamActive.Add(-1)
	snapshot, sub, err := s.clusterNode.Subscribe()
	if err != nil {
		w.Header().Set("Retry-After", "5")
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer sub.Close()
	controller := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	// No event IDs: reconnect always starts a new snapshot, never replays history.
	write := func(kind string, data any) error {
		body, err := json.Marshal(data)
		if err != nil {
			return err
		}
		if err := controller.SetWriteDeadline(time.Now().Add(streamWriteTimeout)); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, body); err != nil {
			return err
		}
		return controller.Flush()
	}
	defer controller.SetWriteDeadline(time.Time{})
	if err := write("snapshot", struct {
		storage.Snapshot
		Node    string `json:"node"`
		Enclave string `json:"enclave"`
	}{snapshot, s.nodeID, s.clusterNode.Enclave()}); err != nil {
		return
	}
	snapshot.Entries = nil
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		// Prioritize overflow/shutdown over draining an obsolete queue.
		select {
		case <-sub.Done():
			return
		default:
		}
		select {
		case <-r.Context().Done():
			return
		case <-s.streamDone:
			return
		case <-sub.Done():
			return
		case event := <-sub.Events():
			var body any
			if event.Kind == "put" {
				body = map[string]any{"entry": event.Entry, "node": s.nodeID, "now": time.Now()}
			} else {
				body = map[string]any{"key": event.Entry.Key, "revision": strconv.FormatUint(event.Entry.Revision, 10), "node": s.nodeID}
			}
			if err := write(event.Kind, body); err != nil {
				return
			}
		case <-keepalive.C:
			if err := write("clock", map[string]any{"now": time.Now()}); err != nil {
				return
			}
		}
	}
}
