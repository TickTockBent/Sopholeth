package main

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"sopholeth/internal/client"
	"sopholeth/internal/client/clienttest"
)

type fakeClock struct {
	mu      sync.Mutex
	current time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.current = c.current.Add(d)
	c.mu.Unlock()
}

func quietConfig() config {
	cfg := defaultConfig()
	cfg.radiation, cfg.mortality, cfg.replication = 0, 0, 0
	return cfg
}

func TestEveryStepWritesAtMostOnce(t *testing.T) {
	clock := &fakeClock{current: time.Unix(1_800_000_000, 0)}
	store := newMemoryNetwork(clock.now)
	cfg := defaultConfig()
	cfg.radiation, cfg.mortality, cfg.replication = 0.2, 0.01, 0.05
	sim := newSimulation(cfg, rand.New(rand.NewSource(1)), clock.now, []network{store}, &bytes.Buffer{})
	for i := 0; i < 2000; i++ {
		before := store.puts
		sim.Step(context.Background())
		if writes := store.puts - before; writes > 1 {
			t.Fatalf("step %d made %d writes", i, writes)
		}
		clock.advance(time.Second)
	}
	if store.puts == 0 {
		t.Fatal("no writes")
	}
}

func TestProbesGreetAndReplyThroughKeys(t *testing.T) {
	clock := &fakeClock{current: time.Unix(1_800_000_000, 0)}
	store := newMemoryNetwork(clock.now)
	cfg := quietConfig()
	cfg.probes = 3
	log := &bytes.Buffer{}
	sim := newSimulation(cfg, rand.New(rand.NewSource(3)), clock.now, []network{store}, log)
	for i := 0; i < 300; i++ {
		sim.Step(context.Background())
		clock.advance(time.Second)
	}
	for _, kind := range []string{"beacon ", "greeting ", "reply ", "finding "} {
		if !strings.Contains(log.String(), "  "+kind) {
			t.Fatalf("no %s in log:\n%s", strings.TrimSpace(kind), log.String())
		}
	}
	page, _ := store.ListKeys(context.Background(), "probe:", 0, "")
	for _, key := range page.Keys {
		if !strings.HasPrefix(key, "probe:") || strings.Contains(key, "/") {
			t.Fatalf("unexpected key %q", key)
		}
	}
}

// A drifted probe stays healthy at the network layer but vanishes from the
// others' view; they notice only the absence of its beacon.
func TestConventionDriftLooksLikeLostContact(t *testing.T) {
	clock := &fakeClock{current: time.Unix(1_800_000_000, 0)}
	store := newMemoryNetwork(clock.now)
	cfg := quietConfig()
	cfg.probes = 2
	log := &bytes.Buffer{}
	sim := newSimulation(cfg, rand.New(rand.NewSource(5)), clock.now, []network{store}, log)
	for i := 0; i < 40; i++ {
		sim.Step(context.Background())
		clock.advance(time.Second)
	}
	drifter, observer := sim.probes[0], sim.probes[1]
	if _, seen := observer.lastSeen[drifter.id]; !seen {
		t.Fatalf("%s never saw %s", observer.name, drifter.name)
	}
	drifter.code.convention = "prboe"
	for i := 0; i < 2*cfg.ttlSeconds; i++ {
		sim.Step(context.Background())
		clock.advance(time.Second)
	}
	if !observer.lostContactOf[drifter.id] {
		t.Fatalf("%s did not notice %s drifting away:\n%s", observer.name, drifter.name, log.String())
	}
	if !strings.Contains(log.String(), "prboe:"+drifter.id+":beacon") {
		t.Fatal("drifted probe stopped beaconing")
	}
	if !strings.Contains(log.String(), "no beacon from "+drifter.name) {
		t.Fatal("no lost-contact finding")
	}
}

func TestMutationsChangeSpeech(t *testing.T) {
	rng := rand.New(rand.NewSource(9))
	p := newProbe(rng, map[string]bool{}, nil)
	p.code.shouts = true
	if got := p.speak("hello there", rng); got != "HELLO THERE" {
		t.Fatal(got)
	}
	if !looksGarbled(p.speak("a perfectly calm message", rng)) {
		t.Fatal("shouting not noticed")
	}
	if looksGarbled("Greetings, Kepler-7. Your transmissions reach me clearly.") {
		t.Fatal("plain greeting judged garbled")
	}
	for i := 0; i < 50; i++ {
		if drifted := driftConvention(rng, sharedConvention); drifted == sharedConvention || strings.ContainsAny(drifted, ":/ ") {
			t.Fatalf("bad drift %q", drifted)
		}
	}
	child := newProbe(rng, map[string]bool{p.id: true}, p)
	if !child.code.shouts || child.parentID != p.id || !strings.HasPrefix(child.name, p.name) {
		t.Fatalf("child did not inherit: %+v", child)
	}
}

// Dead probes don't count against the replication cap, and an extinct
// population is replaced by a fresh launch instead of ending the run.
func TestPopulationOutlivesItsProbes(t *testing.T) {
	clock := &fakeClock{current: time.Unix(1_800_000_000, 0)}
	store := newMemoryNetwork(clock.now)
	cfg := defaultConfig()
	cfg.probes, cfg.maxProbes = 2, 3
	cfg.radiation, cfg.mortality, cfg.replication = 0, 0.05, 0.2
	var log bytes.Buffer
	sim := newSimulation(cfg, rand.New(rand.NewSource(5)), clock.now, []network{store}, &log)
	for i := 0; i < 3000; i++ {
		sim.Step(context.Background())
		if living := len(sim.alive()); living > cfg.maxProbes {
			t.Fatalf("step %d: %d living probes", i, living)
		}
		clock.advance(time.Second)
	}
	if births := strings.Count(log.String(), "birth "); births <= cfg.maxProbes {
		t.Fatalf("only %d births; dead probes are holding replication at the cap", births)
	}
	if !strings.Contains(log.String(), "launches from home") {
		t.Fatal("population never went extinct; raise mortality so the relaunch path runs")
	}
}

// The two clock readings in a greeting always print differently.
func TestClockReadingsDisagree(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for _, clockRate := range []float64{0.97, 0.999, 1, 1.001, 1.03} {
		p := newProbe(rng, map[string]bool{}, nil)
		p.clockRate = clockRate
		for i := 0; i < 500; i++ {
			yourYears, myYears := p.clockReadings(0.5 + rng.Float64()*6)
			if fmt.Sprintf("%.1f", yourYears) == fmt.Sprintf("%.1f", myYears) {
				t.Fatalf("rate %v: clocks agree at %.1f", clockRate, yourYears)
			}
		}
	}
}

// The real client works against a node over HTTP, including drifted and
// uppercase key conventions.
func TestAgainstHTTPNode(t *testing.T) {
	node := clienttest.New()
	server := httptest.NewServer(node.Handler())
	defer server.Close()
	clock := &fakeClock{current: time.Unix(1_800_000_000, 0)}
	cfg := quietConfig()
	cfg.probes = 3
	log := &bytes.Buffer{}
	sim := newSimulation(cfg, rand.New(rand.NewSource(2)), clock.now, []network{client.New(server.URL, server.Client())}, log)
	sim.probes[2].code.convention = "PROBE"
	for i := 0; i < 60; i++ {
		sim.Step(context.Background())
		clock.advance(time.Second)
	}
	if strings.Contains(log.String(), "failed:") {
		t.Fatalf("writes failed:\n%s", log.String())
	}
	if _, ok := node.Value("PROBE:" + sim.probes[2].id + ":beacon"); !ok {
		t.Fatal("drifted beacon not stored")
	}
}

func TestRunDryRun(t *testing.T) {
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := run(ctx, []string{"--dry-run", "--rate", "200", "--duration", "300ms", "--seed", "4"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "beacon") || !strings.Contains(stderr.String(), "probesim:") {
		t.Fatalf("stdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
	for _, args := range [][]string{{"--rate", "0"}, {"--probes", "20"}, {"--nodes", " , "}} {
		if err := run(ctx, append(args, "--duration", "1ms"), &stdout, &stderr); err == nil {
			t.Fatalf("%v accepted", args)
		}
	}
}
