// Command probesim simulates a scattered fleet of space probes that talk to
// each other only through Sopholeth: beacons, findings, greetings, and
// replies, all with TTLs. Radiation slowly corrupts their codebases. Some
// start shouting, some become obsessed, and some drift to a different key
// convention and quietly fall out of contact while remaining perfectly
// healthy at the network layer.
//
// It is a traffic generator with a sense of humor, not a physics model:
// light lag and relativity appear only as flavor text.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"sopholeth/internal/client"
)

// The public testnet roots, matching sites/soph.stream/config.json.
const defaultNodes = "https://kraid.sopholeth.io,https://ridley.sopholeth.io,https://motherbrain.sopholeth.io"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "probesim:", err)
		os.Exit(2)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	cfg := defaultConfig()
	flags := flag.NewFlagSet("probesim", flag.ContinueOnError)
	flags.SetOutput(stderr)
	nodes := flags.String("nodes", defaultNodes, "comma-separated node endpoints; probes relay through them round-robin")
	rate := flags.Float64("rate", 1, "probe actions per second across the whole fleet (at most one write each)")
	duration := flags.Duration("duration", 0, "stop after this long (0 runs until interrupted)")
	seed := flags.Int64("seed", 0, "random seed (0 picks one from the clock)")
	dryRun := flags.Bool("dry-run", false, "simulate against an in-memory store instead of real nodes")
	flags.IntVar(&cfg.probes, "probes", cfg.probes, "starting number of probes")
	flags.IntVar(&cfg.maxProbes, "max-probes", cfg.maxProbes, "cap on living probes for self-replication")
	flags.IntVar(&cfg.ttlSeconds, "ttl", cfg.ttlSeconds, "TTL in seconds for every write")
	flags.Float64Var(&cfg.radiation, "radiation", cfg.radiation, "chance per action of a codebase mutation")
	flags.Float64Var(&cfg.mortality, "mortality", cfg.mortality, "chance per action that a probe goes silent")
	flags.Float64Var(&cfg.replication, "replication", cfg.replication, "chance per action that a probe builds a copy of itself")
	if err := flags.Parse(args); err != nil {
		return err
	}
	switch {
	case *rate <= 0:
		return fmt.Errorf("--rate must be positive")
	case cfg.probes < 1 || cfg.maxProbes < cfg.probes:
		return fmt.Errorf("--probes must be at least 1 and no more than --max-probes")
	case cfg.ttlSeconds < 1:
		return fmt.Errorf("--ttl must be positive")
	}
	// Refresh beacons well inside the TTL, and declare contact lost only
	// after a beacon has had time to expire.
	cfg.beaconInterval = time.Duration(cfg.ttlSeconds) * time.Second / 3
	cfg.lostAfter = time.Duration(cfg.ttlSeconds) * time.Second / 2
	if *seed == 0 {
		*seed = time.Now().UnixNano()
	}

	var relays []network
	var endpoints []string
	if *dryRun {
		relays, endpoints = []network{newMemoryNetwork(time.Now)}, []string{"memory"}
	} else {
		httpClient := &http.Client{Timeout: cfg.requestTimeout}
		for _, raw := range strings.Split(*nodes, ",") {
			if raw = strings.TrimSpace(raw); raw == "" {
				continue
			}
			endpoint, err := client.NormalizeEndpoint(raw)
			if err != nil {
				return fmt.Errorf("--nodes: %v", err)
			}
			relays = append(relays, client.New(endpoint, httpClient))
			endpoints = append(endpoints, endpoint)
		}
		if len(relays) == 0 {
			return fmt.Errorf("--nodes is empty")
		}
	}

	sim := newSimulation(cfg, rand.New(rand.NewSource(*seed)), time.Now, relays, stdout)
	fmt.Fprintf(stderr, "probesim: %d probes, %.2g actions/s, TTL %ds, seed %d, relays %s\n",
		cfg.probes, *rate, cfg.ttlSeconds, *seed, strings.Join(endpoints, ", "))
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}
	ticker := time.NewTicker(time.Duration(float64(time.Second) / *rate))
	defer ticker.Stop()
	for {
		sim.Step(ctx)
		select {
		case <-ctx.Done():
			summarize(sim, stderr)
			return nil
		case <-ticker.C:
		}
	}
}

func summarize(sim *simulation, out io.Writer) {
	fmt.Fprintf(out, "\nprobesim: %d actions\n", sim.step)
	for _, p := range sim.probes {
		state := "alive"
		if !p.alive {
			state = "silent"
		}
		traits := []string{}
		if p.code.convention != sharedConvention {
			traits = append(traits, "drifted to "+p.code.convention+":")
		}
		if p.code.shouts {
			traits = append(traits, "shouts")
		}
		if p.code.stutters {
			traits = append(traits, "stutters")
		}
		if p.code.typoRate > 0 {
			traits = append(traits, fmt.Sprintf("%.0f%% corrupted", p.code.typoRate*100))
		}
		if p.code.obsession != "" {
			traits = append(traits, "obsessed with "+p.code.obsession)
		}
		if p.code.delusion != "" {
			traits = append(traits, "believes it is "+p.code.delusion)
		}
		if p.code.poetic {
			traits = append(traits, "poetic")
		}
		fmt.Fprintf(out, "  %-16s %-6s mutations %d  %s\n", p.name, state, p.code.mutations, strings.Join(traits, ", "))
	}
}

// memoryNetwork is a single in-process store with TTLs, for --dry-run and
// tests.
type memoryNetwork struct {
	mu      sync.Mutex
	now     func() time.Time
	values  map[string][]byte
	expires map[string]time.Time
	puts    int
}

func newMemoryNetwork(now func() time.Time) *memoryNetwork {
	return &memoryNetwork{now: now, values: map[string][]byte{}, expires: map[string]time.Time{}}
}

func (m *memoryNetwork) Put(_ context.Context, key string, data []byte, ttlSeconds int) (*client.PutResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[key] = append([]byte(nil), data...)
	m.expires[key] = m.now().Add(time.Duration(ttlSeconds) * time.Second)
	m.puts++
	return &client.PutResult{Key: key, Status: client.WriteConfirmed, RequestedTTL: ttlSeconds}, nil
}

func (m *memoryNetwork) Get(_ context.Context, key string) (*client.Value, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.liveLocked(key) {
		return nil, client.ErrNotFound
	}
	return &client.Value{Metadata: client.Metadata{Key: key}, Data: append([]byte(nil), m.values[key]...)}, nil
}

func (m *memoryNetwork) ListKeys(_ context.Context, prefix string, limit int, cursor string) (*client.KeyPage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []string
	for key := range m.values {
		if strings.HasPrefix(key, prefix) && key > cursor && m.liveLocked(key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	page := &client.KeyPage{Keys: keys}
	if limit > 0 && len(keys) > limit {
		page.Keys, page.NextCursor = keys[:limit], keys[limit-1]
	}
	return page, nil
}

func (m *memoryNetwork) liveLocked(key string) bool {
	expires, ok := m.expires[key]
	return ok && m.now().Before(expires)
}
