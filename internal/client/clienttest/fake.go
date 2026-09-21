// Package clienttest provides an in-memory stand-in for the node's HTTP API
// so client and CLI tests can run without a cluster. It mirrors the wire
// behavior in docs/api.md: status codes, TTL metadata headers, prefix and
// cursor pagination, and the null-keys serialization quirk.
package clienttest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// FakeNode is a single in-memory node. Its exported fields toggle failure
// modes; set them before issuing requests.
type FakeNode struct {
	mu     sync.Mutex
	values map[string][]byte
	ttls   map[string]int

	// Full makes every PUT answer 507.
	Full bool
	// Pending makes every PUT answer 202 instead of 201.
	Pending bool
	// NullKeys makes /v1/keys answer {"keys":null}.
	NullKeys bool
	// MinTTL and MaxTTL clamp requested TTLs like a real node.
	MinTTL, MaxTTL int
	// Identity reported by /v1/health.
	NodeID, Network, Enclave string

	// Requests records "METHOD /path?query" for every data or keys request.
	Requests []string
}

// New returns a fake node with the node's default TTL bounds and a private
// identity.
func New() *FakeNode {
	return &FakeNode{
		values:  map[string][]byte{},
		ttls:    map[string]int{},
		MinTTL:  300,
		MaxTTL:  86400,
		NodeID:  "fake-1",
		Network: "private",
		Enclave: "default",
	}
}

// Value returns the stored bytes for key, if any.
func (f *FakeNode) Value(key string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.values[key]
	return v, ok
}

// Handler serves the /v1 API.
func (f *FakeNode) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/data/", f.handleData)
	mux.HandleFunc("/v1/keys", f.handleKeys)
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status": "healthy", "node_id": f.NodeID, "network": f.Network, "enclave": f.Enclave,
		})
	})
	mux.HandleFunc("/v1/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"healthy","node_id":%q,"uptime":"1s"}`, f.NodeID)
	})
	mux.HandleFunc("/v1/topology", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"node_id":%q,"enclave":%q,"peers":[]}`, f.NodeID, f.Enclave)
	})
	mux.HandleFunc("/v1/metrics", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "# HELP http_requests_total Total requests.\nhttp_requests_total 1\n")
	})
	return mux
}

func (f *FakeNode) record(r *http.Request) {
	f.Requests = append(f.Requests, r.Method+" "+r.URL.RequestURI())
}

func (f *FakeNode) handleData(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(r)
	key := strings.TrimPrefix(r.URL.Path, "/v1/data/")
	if key == "" || strings.Contains(key, "/") {
		http.Error(w, "key must not contain '/'", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodPut:
		if f.Full {
			http.Error(w, "Node storage capacity exceeded", http.StatusInsufficientStorage)
			return
		}
		body, _ := io.ReadAll(r.Body)
		ttl := 1800
		if h := r.Header.Get("X-TTL"); h != "" {
			if n, err := strconv.Atoi(h); err == nil && n > 0 {
				ttl = n
			}
		}
		if ttl < f.MinTTL {
			ttl = f.MinTTL
		}
		if ttl > f.MaxTTL {
			ttl = f.MaxTTL
		}
		f.values[key] = body
		f.ttls[key] = ttl
		if f.Pending {
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, "Accepted (quorum pending)")
			return
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, "OK")
	case http.MethodGet, http.MethodHead:
		data, ok := f.values[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("X-Created-At", "2026-09-21T12:00:00Z")
		w.Header().Set("X-Original-TTL", strconv.Itoa(f.ttls[key]))
		w.Header().Set("X-Remaining-TTL", strconv.Itoa(f.ttls[key]-10))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			w.Write(data)
		}
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *FakeNode) handleKeys(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(r)
	w.Header().Set("Content-Type", "application/json")
	if f.NullKeys {
		fmt.Fprint(w, `{"keys":null}`)
		return
	}
	prefix := r.URL.Query().Get("prefix")
	keys := []string{}
	for k := range f.values {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		idx := sort.SearchStrings(keys, cursor)
		if idx < len(keys) && keys[idx] == cursor {
			idx++
		}
		keys = keys[idx:]
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	resp := map[string]any{}
	if limit > 0 && len(keys) > limit {
		resp["next_cursor"] = keys[limit-1]
		keys = keys[:limit]
	}
	resp["keys"] = keys
	json.NewEncoder(w).Encode(resp)
}
