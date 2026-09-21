package dashboard

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultPollWorkers is the bounded worker pool size. One slow node must
// not gate the cycle; eight in parallel is plenty for a network of dozens
// and bounds the dashboard's outbound socket count.
const DefaultPollWorkers = 8

// DefaultPollTimeout is the per-request HTTP deadline. Three round trips
// (topology, status, metrics) can take up to 3x this, but worker concurrency
// keeps the cycle bounded. Five seconds matches the cluster's gossip-write
// timeout, so anything slower than that is in degraded territory anyway.
const DefaultPollTimeout = 5 * time.Second

// MaxResponseBytes caps any single HTTP response body the poller will
// read. A malicious node could otherwise serve gigabytes at /v1/metrics
// or /v1/topology and exhaust the dashboard heap. 1 MiB matches the node's
// own server-side request-body limit (MaxRequestSize) — anything beyond
// is degenerate.
const MaxResponseBytes int64 = 1 << 20

// MaxVisitedNodes caps how many distinct addresses one BFS cycle will
// pursue. A malicious node returning thousands of fabricated peers would
// otherwise drive an unbounded number of outbound dials. The cap is
// generous (the public mesh is small), but bounded.
const MaxVisitedNodes = 1024

// nodeAddr is the dashboard's internal pairing of identity + endpoint. The
// public snapshot strips the address (see Builder.Build); this lives only in
// the poller's working set.
type nodeAddr struct {
	ID       string
	Host     string
	HTTPPort int
	Enclave  string
}

func (n nodeAddr) HostPort() string {
	return net.JoinHostPort(n.Host, strconv.Itoa(n.HTTPPort))
}

// pollResult is what one worker produces per node. unreachable means at
// least one of {topology, status} failed and the snapshot should mark this
// node accordingly. peers is the raw topology list (which the BFS uses to
// discover new nodes). metrics may be partially populated when /v1/metrics
// is missing or unparseable — that's logged, not fatal.
type pollResult struct {
	addr        nodeAddr
	unreachable bool
	status      statusResponse
	metrics     NodeMetrics
	peers       []peerEntry
}

// statusResponse is the subset of /v1/status the dashboard consumes. Kept
// minimal so unknown fields are silently dropped — the node may add to
// status over time and the dashboard should not error on that.
type statusResponse struct {
	NodeID  string `json:"node_id"`
	Enclave string `json:"enclave"`
	Network string `json:"network"`
	Uptime  string `json:"uptime"`
	Memory  struct {
		Alloc int64 `json:"alloc"`
	} `json:"memory"`
	Goroutines int `json:"goroutines"`
}

type topologyResponse struct {
	NodeID  string      `json:"node_id"`
	Enclave string      `json:"enclave"`
	Peers   []peerEntry `json:"peers"`
}

type peerEntry struct {
	ID       string `json:"id"`
	Address  string `json:"address"`
	HTTPPort int    `json:"http_port"`
	Enclave  string `json:"enclave"`
}

// Poller walks the network topology BFS-style from a seed set and produces
// per-cycle results that the Builder consumes. Stateless across cycles
// except for HTTP client reuse — every cycle re-walks from the seed set so
// a node that has dropped out of the graph drops out of the snapshot.
type Poller struct {
	client  *http.Client
	workers int
}

// NewPoller constructs a poller with sensible defaults. workers <= 0
// selects DefaultPollWorkers; requestTimeout <= 0 selects DefaultPollTimeout.
// The underlying http.Client reuses connections per Go's standard transport
// pooling — a long-lived dashboard against a small cluster keeps a handful
// of warm connections.
func NewPoller(workers int, requestTimeout time.Duration) *Poller {
	if workers <= 0 {
		workers = DefaultPollWorkers
	}
	if requestTimeout <= 0 {
		requestTimeout = DefaultPollTimeout
	}
	return &Poller{
		client: &http.Client{
			Timeout: requestTimeout,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 4,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		workers: workers,
	}
}

// Walk performs one full BFS cycle starting from seeds. Returns one entry
// per discovered node — including unreachable ones — keyed by node ID. The
// caller (Builder.Build) is responsible for converting these results into
// the snapshot's public projection.
//
// Seeds are addresses ("host:port"). The first successful /v1/topology
// reply assigns the node its canonical ID; later peer entries that match
// either ID or address are coalesced. This matches how the cluster itself
// reasons about identity (id is authoritative once known).
func (p *Poller) Walk(ctx context.Context, seeds []string) map[string]pollResult {
	if len(seeds) == 0 {
		return map[string]pollResult{}
	}

	queue := make(chan nodeAddr, 256)
	results := make(chan pollResult, 64)

	var (
		mu      sync.Mutex
		visited = map[string]bool{} // keyed by host:port
		pending int
	)

	enqueue := func(a nodeAddr) {
		key := a.HostPort()
		mu.Lock()
		if visited[key] {
			mu.Unlock()
			return
		}
		if len(visited) >= MaxVisitedNodes {
			// Cap is generous (the public mesh is small). Hitting it
			// usually means a node served fabricated peer entries.
			// Silently dropping is the right move — the orchestrator
			// publishes the partial graph and the cycle continues.
			mu.Unlock()
			return
		}
		visited[key] = true
		pending++
		mu.Unlock()
		select {
		case queue <- a:
		case <-ctx.Done():
		}
	}

	// Seed the queue with placeholder addresses; the ID is filled in by
	// the first successful topology call.
	for _, seed := range seeds {
		host, port, err := splitHostPort(seed)
		if err != nil {
			continue
		}
		enqueue(nodeAddr{Host: host, HTTPPort: port})
	}

	var workersWg sync.WaitGroup
	for i := 0; i < p.workers; i++ {
		workersWg.Add(1)
		go func() {
			defer workersWg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case addr, ok := <-queue:
					if !ok {
						return
					}
					res := p.pollOne(ctx, addr)
					select {
					case results <- res:
					case <-ctx.Done():
						return
					}
					for _, peer := range res.peers {
						if peer.Address == "" || peer.HTTPPort == 0 {
							continue
						}
						enqueue(nodeAddr{
							ID:       peer.ID,
							Host:     peer.Address,
							HTTPPort: peer.HTTPPort,
							Enclave:  peer.Enclave,
						})
					}
					mu.Lock()
					pending--
					done := pending == 0
					mu.Unlock()
					if done {
						close(queue)
						return
					}
				}
			}
		}()
	}

	collected := map[string]pollResult{}
	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		for r := range results {
			id := r.addr.ID
			if id == "" {
				id = r.addr.HostPort()
			}
			collected[id] = r
		}
	}()

	workersWg.Wait()
	close(results)
	<-collectorDone
	return collected
}

// pollOne hits /v1/topology then /v1/status then /v1/metrics for a single
// node. A topology failure marks the node unreachable; status/metrics
// failures populate what they can and continue. The 5s per-request
// timeout (set on the shared client) bounds the worst-case wait.
func (p *Poller) pollOne(ctx context.Context, addr nodeAddr) pollResult {
	base := fmt.Sprintf("http://%s", addr.HostPort())
	res := pollResult{addr: addr}

	topo, err := fetchJSON[topologyResponse](ctx, p.client, base+"/v1/topology")
	if err != nil {
		res.unreachable = true
		return res
	}
	if topo.NodeID != "" {
		res.addr.ID = topo.NodeID
	}
	if topo.Enclave != "" {
		res.addr.Enclave = topo.Enclave
	}
	res.peers = topo.Peers

	status, err := fetchJSON[statusResponse](ctx, p.client, base+"/v1/status")
	if err == nil {
		res.status = status
		if status.NodeID != "" {
			res.addr.ID = status.NodeID
		}
		if status.Enclave != "" {
			res.addr.Enclave = status.Enclave
		}
	}

	if metricsText, err := fetchText(ctx, p.client, base+"/v1/metrics"); err == nil {
		res.metrics = parseMetrics(metricsText)
	}

	return res
}

func fetchJSON[T any](ctx context.Context, client *http.Client, url string) (T, error) {
	var zero T
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return zero, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return zero, fmt.Errorf("http %d", resp.StatusCode)
	}
	// Same cap as fetchText — bound the JSON parser against a malicious
	// or runaway node. Honest topology/status responses are kilobytes;
	// 1 MiB is two orders of magnitude past that.
	var out T
	if err := json.NewDecoder(io.LimitReader(resp.Body, MaxResponseBytes)).Decode(&out); err != nil {
		return zero, err
	}
	return out, nil
}

func fetchText(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}
	// Cap the read to MaxResponseBytes — a malicious node could otherwise
	// serve gigabytes here and exhaust the dashboard heap. The cap is
	// well above any honest /v1/metrics or /v1/topology response.
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// parseMetrics extracts the three counters the dashboard cares about from a
// Prometheus exposition. We do not pull in the full Prometheus parser
// because the surface is tiny and stable: three `gossip_*` counters in a
// well-formed text exposition. Anything malformed falls through to zero.
func parseMetrics(text string) NodeMetrics {
	var m NodeMetrics
	scan := bufio.NewScanner(strings.NewReader(text))
	for scan.Scan() {
		line := scan.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		name, value, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		// Strip optional labels — the metrics we read have none, but
		// be defensive in case that changes.
		if idx := strings.Index(name, "{"); idx >= 0 {
			name = name[:idx]
		}
		num, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			continue
		}
		u := uint64(num)
		switch name {
		case "gossip_peer_joins_total":
			m.PeerJoinsTotal = u
		case "gossip_peer_evictions_total":
			m.PeerEvictionsTotal = u
		case "gossip_ping_failures_total":
			m.PingFailuresTotal = u
		}
	}
	return m
}

func splitHostPort(s string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, err
	}
	return host, port, nil
}
