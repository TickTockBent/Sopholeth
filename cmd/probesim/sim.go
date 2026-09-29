package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"sort"
	"strings"
	"time"

	"sopholeth/internal/client"
)

// network is the slice of the Sopholeth client the simulation uses.
// *client.Client satisfies it; tests and --dry-run use memoryNetwork.
type network interface {
	Put(ctx context.Context, key string, data []byte, ttlSeconds int) (*client.PutResult, error)
	Get(ctx context.Context, key string) (*client.Value, error)
	ListKeys(ctx context.Context, prefix string, limit int, cursor string) (*client.KeyPage, error)
}

type config struct {
	probes         int     // starting population
	maxProbes      int     // replication stops here
	ttlSeconds     int     // TTL for every write
	radiation      float64 // chance of a mutation per action
	mortality      float64 // chance of death per action
	replication    float64 // chance of replication per action
	beaconInterval time.Duration
	lostAfter      time.Duration // beacon silence before contact is declared lost
	skyRefresh     time.Duration // minimum time between listings per key convention
	maxListedKeys  int
	requestTimeout time.Duration
}

func defaultConfig() config {
	ttl := 300
	return config{
		probes:         8,
		maxProbes:      16,
		ttlSeconds:     ttl,
		radiation:      0.01,
		mortality:      0.002,
		replication:    0.01,
		beaconInterval: time.Duration(ttl) * time.Second / 3,
		lostAfter:      2 * time.Minute,
		skyRefresh:     10 * time.Second,
		maxListedKeys:  5000,
		requestTimeout: 10 * time.Second,
	}
}

// simulation runs one probe action per step. Each action makes at most one
// write, so the caller controls network load by pacing steps.
type simulation struct {
	cfg    config
	rng    *rand.Rand
	now    func() time.Time
	log    io.Writer
	relays []network
	probes []*probe
	taken  map[string]bool
	step   int
	next   int // round-robin cursor over living probes

	sky map[string]skyView // listing cache per key convention
}

type skyView struct {
	keys      []string
	refreshed time.Time
}

func newSimulation(cfg config, rng *rand.Rand, now func() time.Time, relays []network, log io.Writer) *simulation {
	sim := &simulation{cfg: cfg, rng: rng, now: now, log: log, relays: relays, taken: map[string]bool{}, sky: map[string]skyView{}}
	for i := 0; i < cfg.probes; i++ {
		sim.add(newProbe(rng, sim.taken, nil))
	}
	return sim
}

func (s *simulation) add(p *probe) {
	p.relay = len(s.probes) % len(s.relays)
	s.probes = append(s.probes, p)
}

func (s *simulation) alive() []*probe {
	var living []*probe
	for _, p := range s.probes {
		if p.alive {
			living = append(living, p)
		}
	}
	return living
}

// Step advances the simulation by one action of the next living probe.
// It returns false once every probe is gone.
func (s *simulation) Step(ctx context.Context) bool {
	living := s.alive()
	if len(living) == 0 {
		return false
	}
	s.step++
	p := living[s.next%len(living)]
	s.next++
	s.act(ctx, p)
	return true
}

func (s *simulation) act(ctx context.Context, p *probe) {
	if s.rng.Float64() < s.cfg.radiation {
		s.logf("☢  %s: %s", p.name, p.irradiate(s.rng))
	}
	if s.rng.Float64() < s.cfg.mortality {
		p.alive = false
		p.findingCount++
		s.write(ctx, p, "final", p.findingKey(), p.lastTransmission())
		s.logf("✝  %s has gone silent; its beacon will expire on its own", p.name)
		return
	}
	if p.lastBeacon.IsZero() || s.now().Sub(p.lastBeacon) >= s.cfg.beaconInterval {
		p.lastBeacon = s.now()
		s.write(ctx, p, "beacon", p.beaconKey(), p.beaconText(s.step))
		return
	}
	sky := s.observe(ctx, p)
	if lostName, silentFor, lost := s.noticeLostContact(p, sky); lost {
		p.findingCount++
		s.write(ctx, p, "lost", p.findingKey(), p.lostContactText(lostName, silentFor))
		return
	}
	if s.rng.Float64() < s.cfg.replication && len(s.probes) < s.cfg.maxProbes {
		child := newProbe(s.rng, s.taken, p)
		s.add(child)
		p.findingCount++
		s.write(ctx, p, "birth", p.findingKey(), p.speak(fmt.Sprintf("%s has built a copy of itself: welcome, %s.", p.signature(), child.name), s.rng))
		return
	}
	roll := s.rng.Float64()
	switch {
	case roll < 0.4 && s.answerInbox(ctx, p, sky):
	case roll < 0.7 && s.greet(ctx, p, sky):
	default:
		p.findingCount++
		s.write(ctx, p, "finding", p.findingKey(), p.findingText(s.rng))
	}
}

// observe lists the keys under the probe's own convention, through its relay.
// A drifted probe only sees probes that drifted the same way.
func (s *simulation) observe(ctx context.Context, p *probe) []string {
	convention := p.code.convention
	if view, ok := s.sky[convention]; ok && s.now().Sub(view.refreshed) < s.cfg.skyRefresh {
		return view.keys
	}
	var keys []string
	cursor := ""
	for len(keys) < s.cfg.maxListedKeys {
		requestCtx, cancel := context.WithTimeout(ctx, s.cfg.requestTimeout)
		page, err := s.relays[p.relay].ListKeys(requestCtx, convention+":", 1000, cursor)
		cancel()
		if err != nil {
			s.logf("   %s: could not list %s:* (%v)", p.name, convention, err)
			return s.sky[convention].keys
		}
		keys = append(keys, page.Keys...)
		if page.NextCursor == "" || page.NextCursor == cursor {
			break
		}
		cursor = page.NextCursor
	}
	s.sky[convention] = skyView{keys: keys, refreshed: s.now()}
	return keys
}

// visibleBeacons maps probe ids to beacon keys this probe can currently see.
func visibleBeacons(p *probe, sky []string) map[string]string {
	beacons := map[string]string{}
	for _, key := range sky {
		parts := strings.Split(key, ":")
		if len(parts) == 3 && parts[0] == p.code.convention && parts[2] == "beacon" && parts[1] != p.id {
			beacons[parts[1]] = key
		}
	}
	return beacons
}

// noticeLostContact reports one probe whose beacon this probe used to see and
// no longer does. It knows nothing about why: death, drift, or a partition
// all look the same from here.
func (s *simulation) noticeLostContact(p *probe, sky []string) (string, time.Duration, bool) {
	beacons := visibleBeacons(p, sky)
	for id := range beacons {
		p.lastSeen[id] = s.now()
		delete(p.lostContactOf, id) // back in contact
	}
	var candidates []string
	for id, seen := range p.lastSeen {
		if _, visible := beacons[id]; !visible && !p.lostContactOf[id] && s.now().Sub(seen) >= s.cfg.lostAfter {
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 0 {
		return "", 0, false
	}
	sort.Strings(candidates)
	lostID := candidates[0]
	p.lostContactOf[lostID] = true
	return s.displayName(lostID), s.now().Sub(p.lastSeen[lostID]), true
}

func (s *simulation) greet(ctx context.Context, p *probe, sky []string) bool {
	beacons := visibleBeacons(p, sky)
	if len(beacons) == 0 {
		return false
	}
	ids := make([]string, 0, len(beacons))
	for id := range beacons {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	recipientID := ids[s.rng.Intn(len(ids))]
	p.messageCount++
	s.write(ctx, p, "greeting", p.inboxKeyFor(recipientID), p.greetingText(s.rng, s.displayName(recipientID)))
	return true
}

func (s *simulation) answerInbox(ctx context.Context, p *probe, sky []string) bool {
	prefix := p.inboxPrefix()
	for _, key := range sky {
		if !strings.HasPrefix(key, prefix) || p.handledInbox[key] {
			continue
		}
		p.handledInbox[key] = true
		senderID := strings.SplitN(strings.TrimPrefix(key, prefix), ":", 2)[0]
		requestCtx, cancel := context.WithTimeout(ctx, s.cfg.requestTimeout)
		value, err := s.relays[p.relay].Get(requestCtx, key)
		cancel()
		if err != nil {
			// Expired or not replicated here yet. Absence is normal.
			continue
		}
		p.messageCount++
		s.write(ctx, p, "reply", p.inboxKeyFor(senderID), p.replyText(s.rng, s.displayName(senderID), string(value.Data)))
		return true
	}
	return false
}

func (s *simulation) displayName(id string) string {
	for _, p := range s.probes {
		if p.id == id {
			return p.name
		}
	}
	return id
}

func (s *simulation) write(ctx context.Context, p *probe, kind, key, text string) {
	requestCtx, cancel := context.WithTimeout(ctx, s.cfg.requestTimeout)
	defer cancel()
	result, err := s.relays[p.relay].Put(requestCtx, key, []byte(text), s.cfg.ttlSeconds)
	var outcome string
	switch {
	case err == nil:
		outcome = string(result.Status)
	case errors.Is(err, context.Canceled):
		return
	default:
		outcome = "failed: " + err.Error()
	}
	s.logf("%-8s %-44s %-9s %s", kind, key, outcome, text)
}

func (s *simulation) logf(format string, args ...any) {
	fmt.Fprintf(s.log, "%s  "+format+"\n", append([]any{s.now().Format("15:04:05")}, args...)...)
}
