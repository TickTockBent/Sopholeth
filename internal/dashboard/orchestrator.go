package dashboard

import (
	"context"
	"crypto/ed25519"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"sopholeth/internal/trust"
)

// MaxRootsUnreachableCycles is how many consecutive poll cycles without any
// reachable root the dashboard tolerates before triggering an omega refresh.
// Five at 60s default = ~5 minutes of degradation before reaching for DNS.
const MaxRootsUnreachableCycles = 5

// RootSource captures where the dashboard's current root set came from.
// The distinction matters: omega-derived roots carry the trust chain;
// seed-derived roots bypass it and the snapshot must say so.
type RootSource int

const (
	RootSourceOmega RootSource = iota
	RootSourceSeeds
)

// Orchestrator drives the dashboard's main loop. It owns:
//   - The current snapshot (served to /api/snapshot)
//   - Cycle state (roots-unreachable streak, omega-refresh-failed flag)
//   - The trust.Refresher (only when RootSource == omega)
//   - The poller and builder
//
// Concurrency: snapshot reads happen on every HTTP request; cycle writes
// happen once per poll interval. The snapshot is held behind an atomic
// pointer so readers never block writers and vice versa.
type Orchestrator struct {
	cfg Config

	poller  *Poller
	builder *Builder
	geo     *Geo
	metrics *internalMetrics

	// snapshot holds the current snapshot as a *Snapshot. Atomic so HTTP
	// readers never contend with the poll-cycle writer.
	snapshot atomic.Pointer[Snapshot]

	// rootsMu guards roots and rootsConsecutiveMisses. The set rotates
	// when the omega cache is refreshed or a seed override is applied.
	rootsMu                sync.RWMutex
	roots                  map[string]bool
	rootsConsecutiveMisses int
	source                 RootSource
	currentList            *trust.SignedList
	currentListFetchedAt   time.Time
	omegaRefreshFailedFlag bool
	omegaRefreshFailedMu   sync.Mutex

	// cycleMu serializes poll cycles so a slow walk can't overlap with
	// the next ticker fire and race on rootsConsecutiveMisses. TryLock
	// would let us count skipped cycles, but Go's sync.Mutex doesn't
	// expose that — we just record an atomic skip count instead.
	cycleMu       sync.Mutex
	cyclesSkipped atomic.Uint64

	// lastSuccessfulPoll backs the dashboard_snapshot_age_seconds
	// GaugeFunc — gauges that age between cycles must be computed at
	// scrape time, not poked once per cycle.
	lastSuccessfulPoll atomic.Int64

	refresher *trust.Refresher
	hupCh     chan struct{}
}

// Config bundles the orchestrator's startup parameters. Most fields have
// sensible zero-value defaults; see field comments.
type Config struct {
	// StateDir is where omega.json (root-list.json, per trust.CacheFileName)
	// and snapshot.json live. Must be writable.
	StateDir string

	// PollInterval is how often the orchestrator walks the topology.
	// Zero selects 60s.
	PollInterval time.Duration

	// PollWorkers and PollTimeout are forwarded to the poller. Zero selects
	// DefaultPollWorkers / DefaultPollTimeout.
	PollWorkers int
	PollTimeout time.Duration

	// GeoDBPath is the GeoLite2-Country mmdb path, or empty for no-op geo.
	GeoDBPath string

	// SeedAddresses is the operator's break-glass — when non-empty the
	// dashboard skips omega resolution entirely and treats these as
	// roots. Snapshots gain SeedOverride=true.
	SeedAddresses []string

	// Logger is the destination for orchestrator chatter. nil selects
	// the stdlib default logger.
	Logger *log.Logger

	// OmegaPubkey overrides the baked-in omega public key. Production
	// callers leave this nil; tests inject their own keypair so they
	// can sign synthetic root lists for the cache and DNS paths.
	OmegaPubkey ed25519.PublicKey

	// OmegaDNS overrides the DNS configuration used by trust.FetchSigned
	// and the refresher. Production callers leave this zero-valued
	// (real net.Resolver against the public bootstrap name). Tests inject a
	// stub TXTResolver so Boot() doesn't make live DNS calls.
	OmegaDNS trust.DNSConfig
}

// NewOrchestrator wires up the orchestrator without starting any goroutines.
// Call Boot once before Run.
func NewOrchestrator(cfg Config) *Orchestrator {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 60 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	geo, err := NewGeo(cfg.GeoDBPath)
	if err != nil {
		cfg.Logger.Printf("dashboard: geo init: %v (continuing without)", err)
	}
	o := &Orchestrator{
		cfg:    cfg,
		poller: NewPoller(cfg.PollWorkers, cfg.PollTimeout),
		geo:    geo,
		hupCh:  make(chan struct{}, 1),
	}
	o.metrics = newInternalMetrics(o.lastSuccessfulPoll.Load)
	o.builder = NewBuilder(geo, o.metrics.geoLookupMissesTotal)
	return o
}

// Boot resolves the initial root set following the design v3 documented
// order: cached omega list, then DNS, then --seeds, else exit-worthy error.
// `--seeds` is the last-resort break-glass — when a still-valid cache
// exists, it takes precedence even if the operator also supplied seeds, so
// the dashboard's steady-state behavior matches a node that booted from
// the same cache. Boot also loads any persisted snapshot so readers see
// something immediately.
func (o *Orchestrator) Boot(ctx context.Context) error {
	// Load a prior snapshot if one exists. Failures here are non-fatal —
	// a fresh deploy has no snapshot and that's fine.
	if prior, err := LoadSnapshot(o.cfg.StateDir); err == nil && prior != nil {
		prior.Stale = true
		prior.LoadedFromDisk = true
		o.snapshot.Store(prior)
		o.cfg.Logger.Printf("dashboard: loaded prior snapshot (%d nodes, generated %s)",
			len(prior.Nodes), prior.GeneratedAt.Format(time.RFC3339))
	} else if err != nil {
		o.cfg.Logger.Printf("dashboard: load snapshot: %v (continuing without)", err)
	}

	pubkey := o.cfg.OmegaPubkey
	if pubkey == nil {
		baked, err := trust.DecodedOmegaPubkey()
		if err != nil {
			return err
		}
		pubkey = baked
	}

	// 1. Verified cache — preferred. A still-valid cache lets the
	//    dashboard come up without any DNS at all.
	if cached, err := trust.LoadCache(o.cfg.StateDir); err != nil {
		o.cfg.Logger.Printf("dashboard: load omega cache: %v (will try DNS)", err)
	} else if cached != nil {
		if verr := cached.Verify(pubkey, time.Now()); verr == nil {
			fetchedAt := cacheFileMtime(o.cfg.StateDir)
			o.applyRoots(normalizeAddrs(cached.Nodes), RootSourceOmega, cached, fetchedAt)
			o.cfg.Logger.Printf("dashboard: booted from cached omega list (%d roots, expires %s)",
				len(cached.Nodes), time.Unix(cached.Expires, 0).Format(time.RFC3339))
			return nil
		} else {
			o.cfg.Logger.Printf("dashboard: cached omega list failed verification: %v (will try DNS)", verr)
		}
	}

	// 2. DNS resolution against the omega trust chain.
	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	list, dnsErr := trust.FetchSigned(fetchCtx, o.cfg.OmegaDNS, pubkey, time.Now())
	if dnsErr == nil {
		if err := trust.SaveCache(o.cfg.StateDir, list); err != nil {
			o.cfg.Logger.Printf("dashboard: save omega cache: %v (continuing)", err)
		}
		o.applyRoots(normalizeAddrs(list.Nodes), RootSourceOmega, list, time.Now())
		o.cfg.Logger.Printf("dashboard: booted from DNS-fetched omega list (%d roots)", len(list.Nodes))
		return nil
	}
	o.cfg.Logger.Printf("dashboard: omega DNS resolution failed: %v", dnsErr)
	o.markRefreshFailed(true)

	// 3. --seeds operator break-glass — last resort. Trust chain is
	//    bypassed and the snapshot will carry seed_override:true so the
	//    UI banner makes that visible.
	if len(o.cfg.SeedAddresses) > 0 {
		o.applyRoots(normalizeAddrs(o.cfg.SeedAddresses), RootSourceSeeds, nil, time.Time{})
		// Clear omega_refresh_failed: in seeds mode the refresher
		// never runs, so this flag would otherwise stay true forever
		// and double up with the SeedOverride banner. The
		// seed_override flag is the single meaningful signal here.
		o.markRefreshFailed(false)
		o.cfg.Logger.Printf("dashboard: cache and DNS unavailable; booting from %d operator-supplied seeds, trust chain bypassed",
			len(o.cfg.SeedAddresses))
		return nil
	}

	// 4. Exit-worthy: no cache, no DNS, no seeds. Operator must provide
	//    one of the three.
	return dnsErr
}

// cacheFileMtime reads the modification time of the omega cache file so
// the dashboard reports an honest "last refreshed" timestamp across
// restarts. Falls back to zero time on stat failure — the snapshot then
// omits OmegaRefreshedAt, which is more truthful than reporting "now".
func cacheFileMtime(dir string) time.Time {
	info, err := os.Stat(dir + "/" + trust.CacheFileName)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// Run blocks driving the poll loop, the optional omega refresher, and the
// SIGHUP handler. Returns when ctx is cancelled.
func (o *Orchestrator) Run(ctx context.Context) {
	// Stand up the trust.Refresher only when we're operating under the
	// real trust chain. Seed-override skips it by design. Pubkey-decode
	// failure is non-fatal: we skip the refresher and let the
	// poll loop continue serving whatever the cache provided. A real
	// failure here means the binary's baked-in pubkey is malformed,
	// which is a build-time problem, not a runtime one.
	if o.source == RootSourceOmega && o.currentList != nil {
		pubkey := o.cfg.OmegaPubkey
		if pubkey == nil {
			baked, err := trust.DecodedOmegaPubkey()
			if err != nil {
				o.cfg.Logger.Printf("dashboard: decode omega pubkey: %v (running without refresher)", err)
			} else {
				pubkey = baked
			}
		}
		if pubkey != nil {
			o.refresher = trust.NewRefresher(trust.RefresherConfig{
				Pubkey:   pubkey,
				CacheDir: o.cfg.StateDir,
				DNS:      o.cfg.OmegaDNS,
				OnUpdate: o.onOmegaUpdate,
				OnError: func(err error) {
					o.cfg.Logger.Printf("dashboard: omega refresh: %v", err)
					o.markRefreshFailed(true)
					o.metrics.omegaRefreshFailures.Inc()
				},
			}, o.currentList)
			go o.refresher.Run(ctx)
		}
	}

	ticker := time.NewTicker(o.cfg.PollInterval)
	defer ticker.Stop()

	// Kick off the first poll immediately so /api/snapshot isn't 60s
	// behind on cold-start when there's no prior snapshot to display.
	o.cycle(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.cycle(ctx)
		case <-o.hupCh:
			o.cfg.Logger.Println("dashboard: SIGHUP — refreshing omega + reloading geo DB")
			if o.refresher != nil {
				o.refresher.Trigger()
			}
			if o.geo != nil && o.cfg.GeoDBPath != "" {
				if err := o.geo.Reload(o.cfg.GeoDBPath); err != nil {
					o.cfg.Logger.Printf("dashboard: geo reload: %v (keeping previous DB)", err)
				} else {
					o.cfg.Logger.Println("dashboard: geo DB reloaded")
				}
			}
			// Also kick a poll so the operator's manual intervention
			// produces an immediate snapshot update.
			o.cycle(ctx)
		}
	}
}

// SignalHUP is called from the signal handler in main. Coalesces extra HUPs.
func (o *Orchestrator) SignalHUP() {
	select {
	case o.hupCh <- struct{}{}:
	default:
	}
}

// Snapshot returns the most recently published snapshot. Nil only between
// orchestrator construction and the first successful cycle (or prior-disk
// snapshot load), so HTTP handlers should null-check before serving.
func (o *Orchestrator) Snapshot() *Snapshot {
	return o.snapshot.Load()
}

// MetricsRegistry exposes the internal Prometheus registry so the HTTP
// server can mount it at /internal/metrics.
func (o *Orchestrator) MetricsRegistry() *prometheus.Registry {
	return o.metrics.registry
}

func (o *Orchestrator) cycle(ctx context.Context) {
	// Serialize cycles. A walk that runs longer than PollInterval would
	// otherwise overlap with the next ticker fire and race on
	// rootsConsecutiveMisses. Skipping (not queueing) keeps the
	// dashboard from falling further behind under load.
	if !o.cycleMu.TryLock() {
		o.cyclesSkipped.Add(1)
		o.cfg.Logger.Printf("dashboard: skipping cycle — previous cycle still in flight (%d total skips)",
			o.cyclesSkipped.Load())
		return
	}
	defer o.cycleMu.Unlock()

	o.metrics.pollsTotal.Inc()

	o.rootsMu.RLock()
	seeds := make([]string, 0, len(o.roots))
	for addr := range o.roots {
		seeds = append(seeds, addr)
	}
	rootsSnapshotted := make(map[string]bool, len(o.roots))
	for k, v := range o.roots {
		rootsSnapshotted[k] = v
	}
	source := o.source
	currentList := o.currentList
	currentListFetchedAt := o.currentListFetchedAt
	o.rootsMu.RUnlock()

	// Leave 5s headroom against the ticker so the next cycle never has
	// to fight this one for cycleMu — a 60s interval becomes a 55s walk
	// budget. The bounded worker pool plus per-request timeout normally
	// keeps cycles well under this.
	walkBudget := o.cfg.PollInterval - 5*time.Second
	if walkBudget <= 0 {
		walkBudget = o.cfg.PollInterval
	}
	pollCtx, cancel := context.WithTimeout(ctx, walkBudget)
	defer cancel()

	results := o.poller.Walk(pollCtx, seeds)

	// Compute roots-reachable bookkeeping before publishing.
	rootsReachable := 0
	unreachableNodes := 0
	for _, r := range results {
		if r.unreachable {
			unreachableNodes++
			continue
		}
		if rootsSnapshotted[r.addr.HostPort()] {
			rootsReachable++
		}
	}

	o.rootsMu.Lock()
	if rootsReachable == 0 && len(seeds) > 0 {
		o.rootsConsecutiveMisses++
	} else {
		o.rootsConsecutiveMisses = 0
	}
	missesNow := o.rootsConsecutiveMisses
	o.rootsMu.Unlock()

	if missesNow >= MaxRootsUnreachableCycles && source == RootSourceOmega && o.refresher != nil {
		o.cfg.Logger.Printf("dashboard: %d consecutive cycles with no reachable root — triggering omega refresh", missesNow)
		o.refresher.Trigger()
	}

	in := BuildInput{
		Results:            results,
		Roots:              rootsSnapshotted,
		SeedOverride:       source == RootSourceSeeds,
		RootsUnreachable:   rootsReachable == 0 && len(seeds) > 0,
		OmegaRefreshFailed: o.refreshFailed(),
		Now:                time.Now(),
	}
	if currentList != nil {
		expires := time.Unix(currentList.Expires, 0)
		in.OmegaExpiresAt = &expires
		o.metrics.omegaExpiresUnix.Set(float64(expires.Unix()))
		if !currentListFetchedAt.IsZero() {
			refreshed := currentListFetchedAt
			in.OmegaRefreshedAt = &refreshed
			o.metrics.omegaRefreshUnix.Set(float64(refreshed.Unix()))
		}
	}

	snap := o.builder.Build(in)
	snap.Stale = len(results) == 0

	if len(results) == 0 {
		o.metrics.pollsFailedTotal.Inc()
		// Don't overwrite a populated snapshot with an empty one — keep
		// serving the last-known graph with stale=true.
		if prior := o.snapshot.Load(); prior != nil && len(prior.Nodes) > 0 {
			prior.Stale = true
			o.snapshot.Store(prior)
			return
		}
	}

	o.metrics.nodesUnreachable.Set(float64(unreachableNodes))
	o.lastSuccessfulPoll.Store(time.Now().Unix())
	o.snapshot.Store(snap)

	if err := SaveSnapshot(o.cfg.StateDir, snap); err != nil {
		o.cfg.Logger.Printf("dashboard: save snapshot: %v", err)
	}
}

func (o *Orchestrator) onOmegaUpdate(list *trust.SignedList) {
	o.applyRoots(normalizeAddrs(list.Nodes), RootSourceOmega, list, time.Now())
	o.markRefreshFailed(false)
	o.cfg.Logger.Printf("dashboard: omega list refreshed (%d roots, expires %s)",
		len(list.Nodes), time.Unix(list.Expires, 0).Format(time.RFC3339))
}

func (o *Orchestrator) applyRoots(addrs map[string]bool, source RootSource, list *trust.SignedList, fetchedAt time.Time) {
	o.rootsMu.Lock()
	defer o.rootsMu.Unlock()
	o.roots = addrs
	o.source = source
	o.currentList = list
	o.currentListFetchedAt = fetchedAt
	o.rootsConsecutiveMisses = 0
}

func (o *Orchestrator) markRefreshFailed(v bool) {
	o.omegaRefreshFailedMu.Lock()
	o.omegaRefreshFailedFlag = v
	o.omegaRefreshFailedMu.Unlock()
}

func (o *Orchestrator) refreshFailed() bool {
	o.omegaRefreshFailedMu.Lock()
	defer o.omegaRefreshFailedMu.Unlock()
	return o.omegaRefreshFailedFlag
}

func normalizeAddrs(in []string) map[string]bool {
	out := make(map[string]bool, len(in))
	for _, a := range in {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		// Validate parseability; silently drop garbage. We never want
		// to brick the dashboard on one malformed seed.
		host, portStr, err := splitHostPortRaw(a)
		if err != nil {
			continue
		}
		if _, err := strconv.Atoi(portStr); err != nil {
			continue
		}
		out[hostPortJoin(host, portStr)] = true
	}
	return out
}

// splitHostPortRaw is a stdlib-equivalent split that tolerates IPv6 literals
// and rejects bare hostnames without ports. Lives here so the trust-package
// dependency stays a one-way edge.
func splitHostPortRaw(s string) (host, port string, err error) {
	// net.SplitHostPort handles IPv6 literals in [brackets]:port too.
	h, p, e := splitHostPort(s)
	if e != nil {
		return "", "", e
	}
	return h, strconv.Itoa(p), nil
}

func hostPortJoin(host, port string) string {
	// Re-join via strings.Builder to canonicalize; net.JoinHostPort
	// adds [] around IPv6.
	var b strings.Builder
	if strings.ContainsRune(host, ':') {
		b.WriteByte('[')
		b.WriteString(host)
		b.WriteByte(']')
	} else {
		b.WriteString(host)
	}
	b.WriteByte(':')
	b.WriteString(port)
	return b.String()
}
