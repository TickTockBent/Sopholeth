package dashboard

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeNode is a tiny in-memory node: serves /v1/topology, /v1/status,
// and /v1/metrics with operator-supplied payloads. Used by poller tests to
// stand up small clusters without touching the network.
type fakeNode struct {
	srv      *httptest.Server
	id       string
	enclave  string
	peers    []peerEntry
	uptime   string
	memAlloc int64
	gor      int
	metrics  map[string]uint64
	httpPort int
	host     string
}

func startFakeNode(t *testing.T, id, enclave string) *fakeNode {
	t.Helper()
	fn := &fakeNode{id: id, enclave: enclave, uptime: "1h"}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/topology", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"node_id":%q,"enclave":%q,"peers":[`, fn.id, fn.enclave)
		for i, p := range fn.peers {
			if i > 0 {
				w.Write([]byte(","))
			}
			fmt.Fprintf(w, `{"id":%q,"address":%q,"http_port":%d,"enclave":%q}`, p.ID, p.Address, p.HTTPPort, p.Enclave)
		}
		w.Write([]byte(`]}`))
	})
	mux.HandleFunc("/v1/status", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"node_id":%q,"enclave":%q,"uptime":%q,"memory":{"alloc":%d},"goroutines":%d}`,
			fn.id, fn.enclave, fn.uptime, fn.memAlloc, fn.gor)
	})
	mux.HandleFunc("/v1/metrics", func(w http.ResponseWriter, _ *http.Request) {
		for k, v := range fn.metrics {
			fmt.Fprintf(w, "%s %d\n", k, v)
		}
	})
	fn.srv = httptest.NewServer(mux)
	t.Cleanup(fn.srv.Close)

	u, _ := url.Parse(fn.srv.URL)
	host, portStr, _ := strings.Cut(u.Host, ":")
	port, _ := strconv.Atoi(portStr)
	fn.host = host
	fn.httpPort = port
	return fn
}

func TestPollerWalksFullCluster(t *testing.T) {
	a := startFakeNode(t, "node-a", "default")
	b := startFakeNode(t, "node-b", "default")
	c := startFakeNode(t, "node-c", "default")
	// Wire peer awareness — a knows b and c; b knows a and c; c knows a and b.
	a.peers = []peerEntry{
		{ID: "node-b", Address: b.host, HTTPPort: b.httpPort, Enclave: "default"},
		{ID: "node-c", Address: c.host, HTTPPort: c.httpPort, Enclave: "default"},
	}
	b.peers = []peerEntry{
		{ID: "node-a", Address: a.host, HTTPPort: a.httpPort, Enclave: "default"},
		{ID: "node-c", Address: c.host, HTTPPort: c.httpPort, Enclave: "default"},
	}
	c.peers = []peerEntry{
		{ID: "node-a", Address: a.host, HTTPPort: a.httpPort, Enclave: "default"},
		{ID: "node-b", Address: b.host, HTTPPort: b.httpPort, Enclave: "default"},
	}
	a.metrics = map[string]uint64{
		"gossip_peer_joins_total":     5,
		"gossip_peer_evictions_total": 1,
		"gossip_ping_failures_total":  2,
	}

	p := NewPoller(4, 2*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results := p.Walk(ctx, []string{fmt.Sprintf("%s:%d", a.host, a.httpPort)})

	if len(results) != 3 {
		t.Fatalf("expected 3 nodes discovered, got %d (%+v)", len(results), results)
	}
	for _, id := range []string{"node-a", "node-b", "node-c"} {
		r, ok := results[id]
		if !ok {
			t.Errorf("missing %s in walk results", id)
			continue
		}
		if r.unreachable {
			t.Errorf("%s should not be unreachable", id)
		}
	}
	if results["node-a"].metrics.PeerJoinsTotal != 5 {
		t.Errorf("metrics not propagated: %+v", results["node-a"].metrics)
	}
}

func TestPollerMarksUnreachableOnTopologyFailure(t *testing.T) {
	// Use a server that refuses connections — close immediately.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)

	p := NewPoller(2, 1*time.Second)
	results := p.Walk(context.Background(), []string{u.Host})
	if len(results) != 1 {
		t.Fatalf("expected 1 result for unreachable seed, got %d", len(results))
	}
	for _, r := range results {
		if !r.unreachable {
			t.Errorf("expected unreachable=true for 500 response")
		}
	}
}

func TestParseMetricsExtractsCounters(t *testing.T) {
	text := `# HELP gossip_peer_joins_total foo
# TYPE gossip_peer_joins_total counter
gossip_peer_joins_total 12
gossip_peer_evictions_total 3
gossip_ping_failures_total 1
unrelated_metric 999
go_gc_duration_seconds{quantile="0"} 0.001
`
	m := parseMetrics(text)
	if m.PeerJoinsTotal != 12 || m.PeerEvictionsTotal != 3 || m.PingFailuresTotal != 1 {
		t.Errorf("unexpected metrics: %+v", m)
	}
}

func TestNormalizeAddrsDropsGarbage(t *testing.T) {
	out := normalizeAddrs([]string{
		"10.0.0.1:18080",
		"  10.0.0.2:18080  ",
		"bad-line",
		"",
		"10.0.0.3:notaport",
	})
	if len(out) != 2 {
		t.Fatalf("expected 2 valid entries, got %d: %+v", len(out), out)
	}
	if !out["10.0.0.1:18080"] || !out["10.0.0.2:18080"] {
		t.Errorf("missing expected entries: %+v", out)
	}
}
