package dashboard

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"sopholeth/internal/trust"
)

// stubTXTResolver is an in-memory dns.TXTResolver. Each entry is either a
// canned response or an error; absence is treated as NXDOMAIN.
type stubTXTResolver struct {
	records map[string][]string
	err     map[string]error
}

func (s *stubTXTResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	if err, ok := s.err[name]; ok {
		return nil, err
	}
	if recs, ok := s.records[name]; ok {
		return recs, nil
	}
	return nil, errors.New("nxdomain")
}

func newSignedList(t *testing.T, priv ed25519.PrivateKey, nodes []string, expires time.Time) *trust.SignedList {
	t.Helper()
	list := &trust.SignedList{
		Version: trust.OmegaVersion,
		Expires: expires.Unix(),
		Nodes:   nodes,
	}
	list.Sign(priv)
	return list
}

// TestApplyRootsPreservesSource verifies the small invariant that
// applyRoots stores the source it was given. The boot-order logic in
// Boot() relies on this — Boot is the single point of decision for which
// source the orchestrator runs under.
func TestApplyRootsPreservesSource(t *testing.T) {
	o := NewOrchestrator(Config{StateDir: t.TempDir()})
	o.applyRoots(map[string]bool{"10.0.0.1:18080": true}, RootSourceOmega, nil, time.Now())
	if o.source != RootSourceOmega {
		t.Fatalf("applyRoots should set source=omega, got %v", o.source)
	}
	o.applyRoots(map[string]bool{"10.0.0.2:18080": true}, RootSourceSeeds, nil, time.Time{})
	if o.source != RootSourceSeeds {
		t.Fatalf("applyRoots should set source=seeds, got %v", o.source)
	}
}

// TestBootValidCacheBeatsSeeds verifies the v3 boot-order invariant: a
// still-valid cache is preferred over --seeds even when both are present.
// Without this guarantee, an operator who left --seeds in their service
// file would silently bypass the trust chain even after cache resolution
// succeeded — exactly the regression v1's first review caught.
func TestBootValidCacheBeatsSeeds(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()

	// Plant a fresh, signed cache that the orchestrator can verify.
	cached := newSignedList(t, priv, []string{"cache-root.example:9090"}, time.Now().Add(1*time.Hour))
	if err := trust.SaveCache(dir, cached); err != nil {
		t.Fatalf("save cache: %v", err)
	}

	o := NewOrchestrator(Config{
		StateDir:      dir,
		OmegaPubkey:   pub,
		SeedAddresses: []string{"10.0.0.99:18080"}, // would-be seeds
		// OmegaDNS left zero — Boot must not reach DNS at all because
		// the cache short-circuits. If the boot order were ever
		// re-inverted, the test would either flake on DNS or fall
		// through to seeds and fail the source check below.
	})
	if err := o.Boot(context.Background()); err != nil {
		t.Fatalf("boot: %v", err)
	}
	if o.source != RootSourceOmega {
		t.Errorf("expected source=omega from valid cache, got %v", o.source)
	}
	if _, ok := o.roots["cache-root.example:9090"]; !ok {
		t.Errorf("cache root not adopted: %+v", o.roots)
	}
}

// TestBootDNSBeatsSeedsWhenCacheAbsent verifies the second step of the
// boot order: with no cache, DNS resolution is attempted before falling
// through to --seeds.
func TestBootDNSBeatsSeedsWhenCacheAbsent(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	list := newSignedList(t, priv, []string{"dns-root.example:9090"}, time.Now().Add(1*time.Hour))

	resolver := &stubTXTResolver{
		records: map[string][]string{
			trust.DefaultBootstrapName:  {"omega=" + trust.DefaultSignedListName},
			trust.DefaultSignedListName: {list.Encode()},
		},
	}

	o := NewOrchestrator(Config{
		StateDir:      t.TempDir(),
		OmegaPubkey:   pub,
		OmegaDNS:      trust.DNSConfig{Resolver: resolver},
		SeedAddresses: []string{"10.0.0.99:18080"},
	})
	if err := o.Boot(context.Background()); err != nil {
		t.Fatalf("boot: %v", err)
	}
	if o.source != RootSourceOmega {
		t.Errorf("expected source=omega from DNS resolution, got %v", o.source)
	}
	if _, ok := o.roots["dns-root.example:9090"]; !ok {
		t.Errorf("DNS-resolved root not adopted: %+v", o.roots)
	}
}

// TestBootSeedsAreLastResort verifies the third step: with no cache and
// DNS failing, --seeds is consulted as the operator's break-glass and the
// snapshot carries SeedOverride. Also verifies omega_refresh_failed is
// cleared on the seeds path so the UI doesn't double-banner.
func TestBootSeedsAreLastResort(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	resolver := &stubTXTResolver{
		err: map[string]error{trust.DefaultBootstrapName: errors.New("dns down")},
	}

	o := NewOrchestrator(Config{
		StateDir:      t.TempDir(),
		OmegaPubkey:   pub,
		OmegaDNS:      trust.DNSConfig{Resolver: resolver},
		SeedAddresses: []string{"10.0.0.99:18080"},
	})
	if err := o.Boot(context.Background()); err != nil {
		t.Fatalf("boot: %v", err)
	}
	if o.source != RootSourceSeeds {
		t.Errorf("expected source=seeds after cache+DNS fail, got %v", o.source)
	}
	if o.refreshFailed() {
		t.Errorf("omega_refresh_failed should be cleared on the seeds success path; UI would otherwise double-banner alongside seed_override")
	}
}

// TestBootExitsWhenNothingAvailable verifies the fourth step: no cache,
// no DNS, no seeds → return non-zero so the operator's start-script
// surfaces the failure rather than the dashboard quietly serving nothing.
func TestBootExitsWhenNothingAvailable(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	resolver := &stubTXTResolver{
		err: map[string]error{trust.DefaultBootstrapName: errors.New("dns down")},
	}
	o := NewOrchestrator(Config{
		StateDir:    t.TempDir(),
		OmegaPubkey: pub,
		OmegaDNS:    trust.DNSConfig{Resolver: resolver},
	})
	if err := o.Boot(context.Background()); err == nil {
		t.Error("expected Boot to return error when cache, DNS, and seeds are all unavailable")
	}
}

// TestConcurrentCyclesAreSerialized verifies that a slow cycle does not
// race with the next tick. We start two cycles in quick succession; the
// second must skip because the first holds cycleMu.
func TestConcurrentCyclesAreSerialized(t *testing.T) {
	srv := slowFakeNode(t, 200*time.Millisecond)
	defer srv.Close()

	dir := t.TempDir()
	o := NewOrchestrator(Config{
		StateDir:     dir,
		PollInterval: 1 * time.Second,
		PollWorkers:  1,
		PollTimeout:  2 * time.Second,
	})
	host, port := srvHostPort(t, srv)
	o.applyRoots(map[string]bool{fmt.Sprintf("%s:%d", host, port): true}, RootSourceSeeds, nil, time.Time{})

	ctx := context.Background()
	go o.cycle(ctx)
	// Brief sleep so the first cycle is in flight before the second.
	time.Sleep(20 * time.Millisecond)
	o.cycle(ctx) // expected to skip

	if o.cyclesSkipped.Load() != 1 {
		t.Errorf("expected 1 cycle skip, got %d", o.cyclesSkipped.Load())
	}
}

// TestBootLoadsPriorSnapshotAsStale verifies snapshot persistence is
// loaded with stale=true and loaded_from_disk=true on cold start. The DNS
// path is stubbed so the test is hermetic — a live DNS lookup would add
// 10s wall-clock in environments without external resolution.
func TestBootLoadsPriorSnapshotAsStale(t *testing.T) {
	dir := t.TempDir()
	prior := &Snapshot{
		GeneratedAt: time.Now().Add(-1 * time.Hour),
		Stats:       Stats{Nodes: 1},
		Nodes:       []Node{{ID: "node-old"}},
	}
	if err := SaveSnapshot(dir, prior); err != nil {
		t.Fatal(err)
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	resolver := &stubTXTResolver{
		err: map[string]error{trust.DefaultBootstrapName: errors.New("stubbed: no dns")},
	}
	o := NewOrchestrator(Config{
		StateDir:      dir,
		OmegaPubkey:   pub,
		OmegaDNS:      trust.DNSConfig{Resolver: resolver},
		SeedAddresses: []string{"127.0.0.1:1"},
	})
	// Boot will try cache (none) → DNS (stub fails fast) → seeds (succeed).
	// We only care about the prior-snapshot-load behavior, which runs
	// before any of that.
	if err := o.Boot(context.Background()); err != nil {
		t.Fatalf("boot: %v", err)
	}

	loaded := o.Snapshot()
	if loaded == nil || loaded.Nodes[0].ID != "node-old" {
		t.Fatalf("prior snapshot not loaded: %+v", loaded)
	}
	if !loaded.Stale || !loaded.LoadedFromDisk {
		t.Errorf("expected stale=true loaded_from_disk=true, got stale=%v lfd=%v", loaded.Stale, loaded.LoadedFromDisk)
	}
}

func slowFakeNode(t *testing.T, delay time.Duration) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/topology", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(delay)
		fmt.Fprintf(w, `{"node_id":"slow","enclave":"default","peers":[]}`)
	})
	mux.HandleFunc("/v1/status", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"node_id":"slow","enclave":"default","uptime":"1h","memory":{"alloc":0},"goroutines":1}`)
	})
	mux.HandleFunc("/v1/metrics", func(w http.ResponseWriter, _ *http.Request) {})
	return httptest.NewServer(mux)
}

func srvHostPort(t *testing.T, srv *httptest.Server) (string, int) {
	t.Helper()
	u, _ := url.Parse(srv.URL)
	host, portStr, _ := strings.Cut(u.Host, ":")
	port, _ := strconv.Atoi(portStr)
	return host, port
}
