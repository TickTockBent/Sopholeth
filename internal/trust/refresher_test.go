package trust

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	mathrand "math/rand"
	"sync"
	"testing"
	"time"
)

// fakeClock drives the Refresher's time-dependent logic from tests.
// Calls to After return channels that the test fires manually via Advance.
type fakeClock struct {
	mu         sync.Mutex
	now        time.Time
	pending    []pendingAfter
	afterCalls chan struct{} // signalled on every After registration
}

type pendingAfter struct {
	fireAt time.Time
	ch     chan time.Time
}

func newFakeClock(now time.Time) *fakeClock {
	return &fakeClock{
		now:        now,
		afterCalls: make(chan struct{}, 64),
	}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	ch := make(chan time.Time, 1)
	c.pending = append(c.pending, pendingAfter{fireAt: c.now.Add(d), ch: ch})
	c.mu.Unlock()
	select {
	case c.afterCalls <- struct{}{}:
	default:
	}
	return ch
}

// WaitForPending blocks until at least n After calls have been registered
// since the last WaitForPending, or until timeout. Used to eliminate the
// registration race between the test goroutine and the refresher goroutine.
func (c *fakeClock) WaitForPending(n int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	seen := 0
	for seen < n {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return
		}
		select {
		case <-c.afterCalls:
			seen++
		case <-time.After(remaining):
			return
		}
	}
}

// Advance fires all pending After channels whose fireAt is now ≤ c.now+d.
// Returns the number of channels that fired.
func (c *fakeClock) Advance(d time.Duration) int {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var kept []pendingAfter
	var fired int
	for _, p := range c.pending {
		if !p.fireAt.After(c.now) {
			p.ch <- c.now
			fired++
		} else {
			kept = append(kept, p)
		}
	}
	c.pending = kept
	c.mu.Unlock()
	return fired
}

func buildSignedListT(t *testing.T, priv ed25519.PrivateKey, expires int64, nodes []string) *SignedList {
	t.Helper()
	list := &SignedList{
		Version: OmegaVersion,
		Expires: expires,
		Nodes:   nodes,
	}
	list.Sign(priv)
	return list
}

// TestRefresherUpdatesOnSchedule — when the scheduled delay fires and DNS
// returns a fresh list, OnUpdate is called and Current() reflects the new
// list.
func TestRefresherUpdatesOnSchedule(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}

	start := time.Unix(1_800_000_000, 0)
	initial := buildSignedListT(t, priv, start.Add(1*time.Hour).Unix(), []string{"a:9090"})
	refreshed := buildSignedListT(t, priv, start.Add(2*time.Hour).Unix(), []string{"a:9090", "b:9090"})

	clock := newFakeClock(start)
	resolver := &stubResolver{
		records: map[string][]string{
			DefaultBootstrapName:  {"omega=" + DefaultSignedListName},
			DefaultSignedListName: {refreshed.Encode()},
		},
	}

	var updateCount int
	updated := make(chan *SignedList, 1)
	cfg := RefresherConfig{
		Pubkey:   pub,
		CacheDir: t.TempDir(),
		DNS:      DNSConfig{Resolver: resolver},
		Clock:    clock,
		Rand:     mathrand.New(mathrand.NewSource(1)),
		OnUpdate: func(l *SignedList) {
			updateCount++
			updated <- l
		},
		OnError: func(err error) { t.Errorf("unexpected error: %v", err) },
	}

	r := NewRefresher(cfg, initial)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()

	// Fire the first scheduled refresh (at ~54 minutes; advance past it).
	clock.WaitForPending(1, time.Second)
	clock.Advance(1 * time.Hour)

	select {
	case got := <-updated:
		if len(got.Nodes) != 2 {
			t.Errorf("refreshed list node count = %d, want 2", len(got.Nodes))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for OnUpdate")
	}

	cancel()
	<-done
}

// TestRefresherRetainsListOnFailure — a failed refresh must NOT clear
// Current(). Callers depend on this: even if DNS is down, the root flag
// stays accurate until the underlying list expires.
func TestRefresherRetainsListOnFailure(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	start := time.Unix(1_800_000_000, 0)
	initial := buildSignedListT(t, priv, start.Add(1*time.Hour).Unix(), []string{"a:9090"})

	clock := newFakeClock(start)
	sentinel := errors.New("simulated DNS failure")
	resolver := &stubResolver{
		err: map[string]error{DefaultBootstrapName: sentinel},
	}

	errCh := make(chan error, 4)
	cfg := RefresherConfig{
		Pubkey:   pub,
		CacheDir: t.TempDir(),
		DNS:      DNSConfig{Resolver: resolver},
		Clock:    clock,
		Rand:     mathrand.New(mathrand.NewSource(1)),
		OnUpdate: func(*SignedList) { t.Error("OnUpdate called on a failing refresh") },
		OnError:  func(err error) { errCh <- err },
	}

	r := NewRefresher(cfg, initial)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go r.Run(ctx)

	clock.WaitForPending(1, time.Second)
	clock.Advance(1 * time.Hour) // triggers the scheduled refresh

	select {
	case err := <-errCh:
		if !errors.Is(err, sentinel) {
			t.Errorf("OnError got %v, want sentinel", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for OnError")
	}

	// Current list is unchanged.
	if got := r.Current(); got == nil || got.Expires != initial.Expires {
		t.Errorf("Current() changed after failure: got %+v", got)
	}
}

// TestRefresherTriggerForcesImmediateRefresh — Trigger() wakes the loop
// without waiting for the scheduled deadline.
func TestRefresherTriggerForcesImmediateRefresh(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	start := time.Unix(1_800_000_000, 0)
	initial := buildSignedListT(t, priv, start.Add(24*time.Hour).Unix(), []string{"a:9090"})
	fresh := buildSignedListT(t, priv, start.Add(48*time.Hour).Unix(), []string{"a:9090", "b:9090"})

	clock := newFakeClock(start)
	resolver := &stubResolver{
		records: map[string][]string{
			DefaultBootstrapName:  {"omega=" + DefaultSignedListName},
			DefaultSignedListName: {fresh.Encode()},
		},
	}

	updates := make(chan *SignedList, 1)
	cfg := RefresherConfig{
		Pubkey:   pub,
		CacheDir: t.TempDir(),
		DNS:      DNSConfig{Resolver: resolver},
		Clock:    clock,
		Rand:     mathrand.New(mathrand.NewSource(1)),
		OnUpdate: func(l *SignedList) { updates <- l },
		OnError:  func(err error) { t.Errorf("unexpected error: %v", err) },
	}

	r := NewRefresher(cfg, initial)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	// Force an immediate refresh without advancing the clock.
	r.Trigger()

	select {
	case l := <-updates:
		if len(l.Nodes) != 2 {
			t.Errorf("triggered list nodes = %d, want 2", len(l.Nodes))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Trigger did not cause a refresh")
	}
}

// TestNextDelayScalesWithRemaining — the scheduled delay should be
// approximately RefreshRotationFraction * remaining, within ±jitter.
func TestNextDelayScalesWithRemaining(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_ = pub
	start := time.Unix(1_800_000_000, 0)
	lifetime := 1 * time.Hour
	initial := buildSignedListT(t, priv, start.Add(lifetime).Unix(), []string{"a:9090"})

	clock := newFakeClock(start)
	r := &Refresher{
		cfg: RefresherConfig{
			Clock: clock,
			Rand:  mathrand.New(mathrand.NewSource(1)),
		},
		current: initial,
	}

	got := r.nextDelay()
	expected := time.Duration(float64(lifetime) * RefreshRotationFraction)
	jitterBand := time.Duration(float64(expected) * RefreshJitter)
	if got < expected-jitterBand || got > expected+jitterBand {
		t.Errorf("nextDelay = %s, want %s±%s", got, expected, jitterBand)
	}
}

// TestNextDelayZeroWhenExpired — if somehow the list is already expired,
// schedule immediate refresh rather than sleeping.
func TestNextDelayZeroWhenExpired(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_ = pub
	start := time.Unix(1_800_000_000, 0)
	expired := buildSignedListT(t, priv, start.Add(-1*time.Minute).Unix(), []string{"a:9090"})
	clock := newFakeClock(start)
	r := &Refresher{
		cfg:     RefresherConfig{Clock: clock, Rand: mathrand.New(mathrand.NewSource(1))},
		current: expired,
	}
	if got := r.nextDelay(); got != 0 {
		t.Errorf("nextDelay on expired list = %s, want 0", got)
	}
}
