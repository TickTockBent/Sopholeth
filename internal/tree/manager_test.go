package tree

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"sopholeth/internal/gossip"
	"sopholeth/internal/transport/ws"
)

// ---- helpers ----------------------------------------------------------

// fakePeerer satisfies the Peerer interface with a static peer list.
type fakePeerer struct {
	peers []*gossip.Node
	mu    sync.Mutex
}

func (f *fakePeerer) GetPeers() []*gossip.Node {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*gossip.Node, len(f.peers))
	copy(out, f.peers)
	return out
}

func (f *fakePeerer) Add(n *gossip.Node) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.peers = append(f.peers, n)
}

func makeNode(id string, opts ...func(*gossip.Node)) *gossip.Node {
	n := &gossip.Node{
		ID:       gossip.NodeID(id),
		Address:  "127.0.0.1",
		Port:     9090,
		HTTPPort: 8080,
		Enclave:  "default",
	}
	for _, opt := range opts {
		opt(n)
	}
	return n
}

// pairServer hosts a /v1/ws endpoint and exposes both sides of a connection.
type pairServer struct {
	srv       *httptest.Server
	serverCh  chan *ws.Connection
	clientWS  *ws.Connection
	cleanupFn func()
}

func newPair(t *testing.T) *pairServer {
	t.Helper()
	ch := make(chan *ws.Connection, 1)
	srv := httptest.NewServer(ws.Handler("", nil, func(c *ws.Connection) {
		ch <- c
	}))

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/ws"
	u, _ := url.Parse(wsURL)
	d := &websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	raw, resp, err := d.Dial(u.String(), nil)
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		srv.Close()
		t.Fatalf("dial: %v", err)
	}
	clientConn := ws.NewConnection(raw, "")

	p := &pairServer{srv: srv, serverCh: ch, clientWS: clientConn}
	p.cleanupFn = func() {
		clientConn.Close(websocket.CloseNormalClosure, "")
		srv.Close()
	}
	return p
}

// serverConn returns the accepted server-side Connection, waiting up to 2s.
func (p *pairServer) serverConn(t *testing.T) *ws.Connection {
	t.Helper()
	select {
	case c := <-p.serverCh:
		return c
	case <-time.After(2 * time.Second):
		t.Fatalf("server never accepted")
		return nil
	}
}

func (p *pairServer) Address() (string, int) {
	u, _ := url.Parse(p.srv.URL)
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	return host, port
}

// wireHelloHandler installs a server-side handler that runs HandleHello on
// any incoming hello frame, returning the accepted/rejected verdict.
func wireHelloHandler(server *ws.Connection, mgr *Manager) {
	server.AddAttachmentHandler(func(msg *ws.AttachmentMessage) {
		if msg.Type != ws.AttachmentTypeHello {
			return
		}
		var h ws.HelloPayload
		if err := json.Unmarshal(msg.Payload, &h); err != nil {
			return
		}
		mgr.HandleHello(server, &h)
	})
}

// ---- role detection ---------------------------------------------------

func TestRoleSubstrate(t *testing.T) {
	local := makeNode("substrate-1")
	peers := &fakePeerer{}
	m := NewManager(local, peers, Options{Inbound: InboundTrue, MaxChildren: DefaultMaxChildren})
	if m.Role() != RoleSubstrate {
		t.Errorf("role: got %q want substrate", m.Role())
	}
	if !m.IsInboundCapable() {
		t.Error("substrate should be inbound capable")
	}
	if m.Parent() != nil {
		t.Error("substrate must not have a parent")
	}
}

func TestRoleTransient(t *testing.T) {
	local := makeNode("transient-1")
	peers := &fakePeerer{}
	m := NewManager(local, peers, Options{Inbound: InboundFalse, MaxChildren: DefaultMaxChildren})
	if m.Role() != RoleTransient {
		t.Errorf("role: got %q want transient", m.Role())
	}
	if m.IsInboundCapable() {
		t.Error("transient must not be inbound capable")
	}
}

// ---- server-side handshake -------------------------------------------

func TestHandleHelloAcceptsAndSendsWelcome(t *testing.T) {
	p := newPair(t)
	defer p.cleanupFn()
	srv := p.serverConn(t)

	local := makeNode("substrate-1")
	peers := &fakePeerer{peers: []*gossip.Node{
		makeNode("peer-1", func(n *gossip.Node) { n.Address = "10.0.0.2"; n.HTTPPort = 8081 }),
	}}
	m := NewManager(local, peers, Options{Inbound: InboundTrue, MaxChildren: DefaultMaxChildren})
	defer m.Stop()

	gotWelcome := make(chan ws.WelcomePayload, 1)
	p.clientWS.AddAttachmentHandler(func(msg *ws.AttachmentMessage) {
		if msg.Type != ws.AttachmentTypeWelcome {
			return
		}
		var w ws.WelcomePayload
		if err := json.Unmarshal(msg.Payload, &w); err != nil {
			t.Errorf("decode welcome: %v", err)
			return
		}
		gotWelcome <- w
	})

	hello := &ws.HelloPayload{
		NodeID: "transient-1", Enclave: "default",
		Address: "192.168.1.100", HTTPPort: 8080,
		Capabilities: ws.Capabilities{Inbound: "false"},
	}
	if !m.HandleHello(srv, hello) {
		t.Fatal("HandleHello rejected unexpectedly")
	}

	select {
	case w := <-gotWelcome:
		if w.YourPosition.ParentID != "substrate-1" {
			t.Errorf("parent_id: %q", w.YourPosition.ParentID)
		}
		if w.YourPosition.Depth != 1 {
			t.Errorf("depth: %d", w.YourPosition.Depth)
		}
		if len(w.Topology) != 2 {
			t.Errorf("topology len: %d want 2", len(w.Topology))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no welcome received")
	}
}

func TestHandleHelloRegistersChild(t *testing.T) {
	p := newPair(t)
	defer p.cleanupFn()
	srv := p.serverConn(t)

	m := NewManager(makeNode("substrate-1"), &fakePeerer{}, Options{Inbound: InboundTrue, MaxChildren: DefaultMaxChildren})
	defer m.Stop()

	hello := &ws.HelloPayload{
		NodeID: "transient-1", Enclave: "default",
		Address: "192.168.1.100", HTTPPort: 8080,
		Capabilities: ws.Capabilities{Inbound: "false"},
	}
	if !m.HandleHello(srv, hello) {
		t.Fatal("HandleHello rejected")
	}
	if m.ChildCount() != 1 {
		t.Errorf("child count: %d want 1", m.ChildCount())
	}
	if !m.HasChild("transient-1") {
		t.Error("transient-1 not registered as child")
	}
	if srv.RemoteNodeID() != "transient-1" {
		t.Errorf("remoteNodeID: %q", srv.RemoteNodeID())
	}
	if srv.RemoteEnclave() != "default" {
		t.Errorf("remoteEnclave: %q", srv.RemoteEnclave())
	}
}

func TestChildRemovedOnConnectionClose(t *testing.T) {
	p := newPair(t)
	defer p.cleanupFn()
	srv := p.serverConn(t)

	m := NewManager(makeNode("substrate-1"), &fakePeerer{}, Options{Inbound: InboundTrue, MaxChildren: DefaultMaxChildren})
	defer m.Stop()

	hello := &ws.HelloPayload{NodeID: "transient-1", Enclave: "default", Capabilities: ws.Capabilities{Inbound: "false"}}
	m.HandleHello(srv, hello)
	if m.ChildCount() != 1 {
		t.Fatalf("setup: child count %d", m.ChildCount())
	}

	p.clientWS.Close(websocket.CloseNormalClosure, "")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if m.ChildCount() == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("child still present after close: count %d", m.ChildCount())
}

// ---- max children -----------------------------------------------------

func TestRejectsAtCapacity(t *testing.T) {
	p1 := newPair(t)
	defer p1.cleanupFn()
	srv1 := p1.serverConn(t)

	m := NewManager(makeNode("substrate-1"), &fakePeerer{}, Options{Inbound: InboundTrue, MaxChildren: 1})
	defer m.Stop()

	if !m.HandleHello(srv1, &ws.HelloPayload{NodeID: "t1", Enclave: "default"}) {
		t.Fatal("first hello rejected")
	}
	if m.ChildCount() != 1 {
		t.Fatalf("after first: count %d", m.ChildCount())
	}

	p2 := newPair(t)
	defer p2.cleanupFn()
	srv2 := p2.serverConn(t)

	gotBye := make(chan ws.GoodbyePayload, 1)
	p2.clientWS.AddAttachmentHandler(func(msg *ws.AttachmentMessage) {
		if msg.Type != ws.AttachmentTypeGoodbye {
			return
		}
		var g ws.GoodbyePayload
		if err := json.Unmarshal(msg.Payload, &g); err == nil {
			gotBye <- g
		}
	})

	if m.HandleHello(srv2, &ws.HelloPayload{NodeID: "t2", Enclave: "default"}) {
		t.Fatal("second hello unexpectedly accepted")
	}
	if m.ChildCount() != 1 {
		t.Errorf("after reject: count %d want 1", m.ChildCount())
	}
	select {
	case g := <-gotBye:
		if g.Reason != "at capacity" {
			t.Errorf("reason: %q", g.Reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no goodbye received")
	}
}

func TestMaxChildrenZeroRejectsAll(t *testing.T) {
	p := newPair(t)
	defer p.cleanupFn()
	srv := p.serverConn(t)

	m := NewManager(makeNode("substrate-1"), &fakePeerer{}, Options{Inbound: InboundTrue, MaxChildren: 0})
	defer m.Stop()

	gotBye := make(chan ws.GoodbyePayload, 1)
	p.clientWS.AddAttachmentHandler(func(msg *ws.AttachmentMessage) {
		if msg.Type != ws.AttachmentTypeGoodbye {
			return
		}
		var g ws.GoodbyePayload
		_ = json.Unmarshal(msg.Payload, &g)
		gotBye <- g
	})

	if m.HandleHello(srv, &ws.HelloPayload{NodeID: "t1", Enclave: "default"}) {
		t.Fatal("hello unexpectedly accepted with MaxChildren=0")
	}
	select {
	case g := <-gotBye:
		if g.Reason != "at capacity" {
			t.Errorf("reason: %q", g.Reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no goodbye received")
	}
}

// ---- transient-side attach -------------------------------------------

func TestAttachCompletesHandshake(t *testing.T) {
	p := newPair(t)
	defer p.cleanupFn()
	srv := p.serverConn(t)

	subMgr := NewManager(makeNode("substrate-1"), &fakePeerer{}, Options{Inbound: InboundTrue, MaxChildren: DefaultMaxChildren})
	defer subMgr.Stop()
	wireHelloHandler(srv, subMgr)

	tranMgr := NewManager(
		makeNode("transient-1", func(n *gossip.Node) { n.Address = "192.168.1.100" }),
		&fakePeerer{},
		Options{Inbound: InboundFalse, MaxChildren: 0},
	)
	defer tranMgr.Stop()

	welcome, err := tranMgr.Attach(context.Background(), p.clientWS)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if welcome.YourPosition.ParentID != "substrate-1" {
		t.Errorf("parent_id: %q", welcome.YourPosition.ParentID)
	}
	if welcome.YourPosition.Depth != 1 {
		t.Errorf("depth: %d", welcome.YourPosition.Depth)
	}
	if tranMgr.Role() != RoleTransient {
		t.Errorf("role: %q", tranMgr.Role())
	}
	if tranMgr.Parent() != p.clientWS {
		t.Error("parent not registered after attach")
	}
}

func TestAttachTimesOut(t *testing.T) {
	p := newPair(t)
	defer p.cleanupFn()
	_ = p.serverConn(t) // no welcome-emitting handler installed

	tranMgr := NewManager(makeNode("transient-1"), &fakePeerer{}, Options{Inbound: InboundFalse})
	defer tranMgr.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := tranMgr.attach(ctx, p.clientWS, 500*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if tranMgr.Parent() != nil {
		t.Error("parent set despite timeout")
	}
}

// ---- alternative substrates ------------------------------------------

func TestGetAlternativeSubstratesPrefersSameEnclave(t *testing.T) {
	local := makeNode("substrate-1")
	peers := &fakePeerer{}
	peers.Add(makeNode("same-1", func(n *gossip.Node) { n.Enclave = "default" }))
	peers.Add(makeNode("other-1", func(n *gossip.Node) { n.Enclave = "other" }))
	peers.Add(makeNode("same-2", func(n *gossip.Node) { n.Enclave = "default" }))

	m := NewManager(local, peers, Options{Inbound: InboundTrue, MaxChildren: DefaultMaxChildren})
	defer m.Stop()

	alts := m.GetAlternativeSubstrates("default")
	if len(alts) != 3 {
		t.Fatalf("alt count: %d", len(alts))
	}
	if alts[0].ID != "same-1" || alts[1].ID != "same-2" {
		t.Errorf("same-enclave alts not first: %+v", alts)
	}
	if alts[2].ID != "other-1" {
		t.Errorf("other-enclave alt not last: %+v", alts)
	}
}

func TestGetAlternativeSubstratesLimitsToFive(t *testing.T) {
	local := makeNode("substrate-1")
	peers := &fakePeerer{}
	for i := 0; i < 10; i++ {
		peers.Add(makeNode("peer-" + strconv.Itoa(i)))
	}
	m := NewManager(local, peers, Options{Inbound: InboundTrue, MaxChildren: DefaultMaxChildren})
	defer m.Stop()

	if alts := m.GetAlternativeSubstrates(""); len(alts) != 5 {
		t.Errorf("alt count: %d want 5", len(alts))
	}
}

// ---- goodbye on shutdown ---------------------------------------------

func TestSendGoodbyeToChildren(t *testing.T) {
	p := newPair(t)
	defer p.cleanupFn()
	srv := p.serverConn(t)

	m := NewManager(makeNode("substrate-1"), &fakePeerer{}, Options{Inbound: InboundTrue, MaxChildren: DefaultMaxChildren})
	defer m.Stop()

	gotBye := make(chan ws.GoodbyePayload, 1)
	p.clientWS.AddAttachmentHandler(func(msg *ws.AttachmentMessage) {
		if msg.Type == ws.AttachmentTypeGoodbye {
			var g ws.GoodbyePayload
			_ = json.Unmarshal(msg.Payload, &g)
			gotBye <- g
		}
	})

	hello := &ws.HelloPayload{NodeID: "t1", Enclave: "default"}
	if !m.HandleHello(srv, hello) {
		t.Fatal("hello rejected")
	}

	m.SendGoodbyeToChildren("")
	select {
	case g := <-gotBye:
		if g.Reason != "shutdown" {
			t.Errorf("reason: %q want shutdown", g.Reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no goodbye received")
	}
}

func TestParentClearedAfterGoodbye(t *testing.T) {
	p := newPair(t)
	defer p.cleanupFn()
	srv := p.serverConn(t)

	subMgr := NewManager(makeNode("substrate-1"), &fakePeerer{}, Options{Inbound: InboundTrue, MaxChildren: DefaultMaxChildren})
	defer subMgr.Stop()
	wireHelloHandler(srv, subMgr)

	tranMgr := NewManager(
		makeNode("transient-1", func(n *gossip.Node) { n.Address = "192.168.1.100" }),
		&fakePeerer{},
		Options{Inbound: InboundFalse},
	)
	defer tranMgr.Stop()
	// Empty seed provider keeps reattach loop spinning harmlessly with no
	// candidates — what we care about is the parent clear.
	tranMgr.SetSeedProvider(func() []string { return nil })

	if _, err := tranMgr.Attach(context.Background(), p.clientWS); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if tranMgr.Parent() == nil {
		t.Fatal("parent not set after attach")
	}

	subMgr.SendGoodbyeToChildren("maintenance")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if tranMgr.Parent() == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("parent not cleared after goodbye")
}

// ---- topology caching --------------------------------------------------

func TestAttachCachesTopologyExcludingSelf(t *testing.T) {
	p := newPair(t)
	defer p.cleanupFn()
	srv := p.serverConn(t)

	subPeers := &fakePeerer{}
	subPeers.Add(makeNode("peer-a", func(n *gossip.Node) { n.HTTPPort = 8001 }))
	subPeers.Add(makeNode("peer-b", func(n *gossip.Node) { n.HTTPPort = 8002 }))
	subMgr := NewManager(makeNode("substrate-1"), subPeers, Options{Inbound: InboundTrue, MaxChildren: DefaultMaxChildren})
	defer subMgr.Stop()
	wireHelloHandler(srv, subMgr)

	tranMgr := NewManager(
		makeNode("transient-1", func(n *gossip.Node) { n.Address = "10.0.0.1" }),
		&fakePeerer{},
		Options{Inbound: InboundFalse},
	)
	defer tranMgr.Stop()

	if _, err := tranMgr.Attach(context.Background(), p.clientWS); err != nil {
		t.Fatalf("attach: %v", err)
	}

	cached := tranMgr.LastKnownAlternatives()
	gotIDs := make([]string, 0, len(cached))
	for _, a := range cached {
		gotIDs = append(gotIDs, a.ID)
	}
	want := []string{"peer-a", "peer-b", "substrate-1"}
	// order is insertion order; sort for stable comparison
	sortStrings(gotIDs)
	if !equalStrings(gotIDs, want) {
		t.Errorf("cached alts: got %v want %v", gotIDs, want)
	}
}

// ---- seed parsing ----------------------------------------------------

func TestParseSeedAddress(t *testing.T) {
	cases := []struct {
		in   string
		ok   bool
		addr string
		port int
	}{
		{"10.0.0.5:8080", true, "10.0.0.5", 8080},
		{"host.example:443", true, "host.example", 443},
		{"no-colon", false, "", 0},
		{":8080", false, "", 0},
		{"host:", false, "", 0},
		{"host:notaport", false, "", 0},
		{"host:0", false, "", 0},
		{"host:99999", false, "", 0},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			alt, ok := parseSeedAddress(c.in)
			if ok != c.ok {
				t.Fatalf("ok: got %v want %v", ok, c.ok)
			}
			if !ok {
				return
			}
			if alt.Address != c.addr || alt.HTTPPort != c.port {
				t.Errorf("got %s:%d want %s:%d", alt.Address, alt.HTTPPort, c.addr, c.port)
			}
			if alt.ID != "seed-"+c.in {
				t.Errorf("id: %q", alt.ID)
			}
		})
	}
}

// ---- stale-connection close guard (matches tree.test.ts "stale conn" case) -----

func TestStaleConnectionCloseDoesNotClobberParent(t *testing.T) {
	pA := newPair(t)
	defer pA.cleanupFn()
	srvA := pA.serverConn(t)
	subA := NewManager(makeNode("substrate-a"), &fakePeerer{}, Options{Inbound: InboundTrue, MaxChildren: DefaultMaxChildren})
	defer subA.Stop()
	wireHelloHandler(srvA, subA)

	pB := newPair(t)
	defer pB.cleanupFn()
	srvB := pB.serverConn(t)
	subB := NewManager(makeNode("substrate-b"), &fakePeerer{}, Options{Inbound: InboundTrue, MaxChildren: DefaultMaxChildren})
	defer subB.Stop()
	wireHelloHandler(srvB, subB)

	tranMgr := NewManager(
		makeNode("transient-1", func(n *gossip.Node) { n.Address = "10.0.0.1" }),
		&fakePeerer{},
		Options{Inbound: InboundFalse},
	)
	defer tranMgr.Stop()
	tranMgr.SetSeedProvider(func() []string { return nil })

	if _, err := tranMgr.Attach(context.Background(), pA.clientWS); err != nil {
		t.Fatalf("attach A: %v", err)
	}
	if tranMgr.Parent() != pA.clientWS {
		t.Fatal("parent != A after first attach")
	}
	if _, err := tranMgr.Attach(context.Background(), pB.clientWS); err != nil {
		t.Fatalf("attach B: %v", err)
	}
	if tranMgr.Parent() != pB.clientWS {
		t.Fatal("parent != B after second attach")
	}

	// Closing A should not clobber the active parent (B).
	pA.clientWS.Close(websocket.CloseNormalClosure, "")
	time.Sleep(80 * time.Millisecond)
	if tranMgr.Parent() != pB.clientWS {
		t.Errorf("stale close clobbered active parent: now %v want B", tranMgr.Parent())
	}
}

// ---- self-skip (#120 regression) -------------------------------------

// TestTryAlternativesSkipsSelf is the timing-assertion regression for #120.
// If self-skip works the call returns false almost instantly. If it's
// broken, the dialer would try a 5-second connect to the unreachable
// self-address and the elapsed time would explode.
func TestTryAlternativesSkipsSelf(t *testing.T) {
	local := makeNode("self", func(n *gossip.Node) {
		n.Address = "10.0.10.104"
		n.HTTPPort = 18080
	})
	var dialerCalls atomic.Int32
	dialer := Dialer(func(ctx context.Context, address string, port int, secret string, timeout time.Duration) (*ws.Connection, error) {
		dialerCalls.Add(1)
		// If self-skip is broken this would be called and stall for
		// `timeout`. Return error fast so test failure is fast too.
		return nil, errors.New("dialer should not have been called")
	})
	m := NewManager(local, &fakePeerer{}, Options{Inbound: InboundFalse, Dialer: dialer})
	defer m.Stop()

	alts := []ws.AlternativeParent{
		{ID: "self-seed", Address: "10.0.10.104", HTTPPort: 18080},
	}
	start := time.Now()
	ok := m.tryAlternatives(alts, 5*time.Second, time.Time{})
	elapsed := time.Since(start)

	if ok {
		t.Error("tryAlternatives unexpectedly succeeded against self-only alts")
	}
	if dialerCalls.Load() != 0 {
		t.Errorf("dialer called %d times; self-skip is broken", dialerCalls.Load())
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("self-skip too slow: %v (want < 500ms)", elapsed)
	}
}

// ---- ungraceful disconnect → seed fallback ---------------------------

func TestUngracefulCloseTriggersReattachLoop(t *testing.T) {
	p := newPair(t)
	defer p.cleanupFn()
	srv := p.serverConn(t)

	subMgr := NewManager(makeNode("substrate-1"), &fakePeerer{}, Options{Inbound: InboundTrue, MaxChildren: DefaultMaxChildren})
	defer subMgr.Stop()
	wireHelloHandler(srv, subMgr)

	// Use a dialer that always fails fast so the reattach loop reaches the
	// seed provider quickly.
	dialer := Dialer(func(ctx context.Context, address string, port int, secret string, timeout time.Duration) (*ws.Connection, error) {
		return nil, errors.New("unreachable")
	})

	var seedCalls atomic.Int32
	seedCh := make(chan struct{}, 1)

	tranMgr := NewManager(
		makeNode("transient-1", func(n *gossip.Node) { n.Address = "10.0.0.1" }),
		&fakePeerer{},
		Options{Inbound: InboundFalse, Dialer: dialer},
	)
	defer tranMgr.Stop()
	tranMgr.SetSeedProvider(func() []string {
		seedCalls.Add(1)
		select {
		case seedCh <- struct{}{}:
		default:
		}
		return nil
	})

	if _, err := tranMgr.Attach(context.Background(), p.clientWS); err != nil {
		t.Fatalf("attach: %v", err)
	}

	// Ungracefully close the parent.
	p.clientWS.Close(websocket.CloseNormalClosure, "")

	select {
	case <-seedCh:
		// seed provider was hit — reattach loop is running
	case <-time.After(5 * time.Second):
		t.Fatalf("seed provider never called (calls=%d)", seedCalls.Load())
	}
}

// ---- stop() unblocks pending backoff sleeps --------------------------

func TestStopUnblocksSleep(t *testing.T) {
	m := NewManager(makeNode("t-1"), &fakePeerer{}, Options{Inbound: InboundFalse})

	done := make(chan bool, 1)
	go func() { done <- m.sleep(60 * time.Second) }()

	time.Sleep(10 * time.Millisecond)
	m.Stop()

	select {
	case ok := <-done:
		if ok {
			t.Error("sleep returned true (not interrupted)")
		}
	case <-time.After(time.Second):
		t.Fatal("Stop() did not wake sleep within 1s")
	}
	if !m.Stopping() {
		t.Error("Stopping() returned false after Stop()")
	}
}

// ---- ACK routing (phase-4 prep) --------------------------------------

func TestRecordAndLookupAckRoute(t *testing.T) {
	p := newPair(t)
	defer p.cleanupFn()
	srv := p.serverConn(t)

	m := NewManager(makeNode("substrate-1"), &fakePeerer{}, Options{Inbound: InboundTrue, MaxChildren: DefaultMaxChildren})
	defer m.Stop()

	m.RecordAckRoute("msg-1", srv, 5*time.Second)
	if got := m.LookupAckRoute("msg-1"); got != srv {
		t.Errorf("LookupAckRoute: got %v want %v", got, srv)
	}
	if got := m.LookupAckRoute("msg-unknown"); got != nil {
		t.Errorf("LookupAckRoute(unknown): got %v want nil", got)
	}
}

func TestAckRouteAutoEvicts(t *testing.T) {
	p := newPair(t)
	defer p.cleanupFn()
	srv := p.serverConn(t)

	m := NewManager(makeNode("substrate-1"), &fakePeerer{}, Options{Inbound: InboundTrue, MaxChildren: DefaultMaxChildren})
	defer m.Stop()

	m.RecordAckRoute("msg-1", srv, 80*time.Millisecond)
	if got := m.LookupAckRoute("msg-1"); got != srv {
		t.Fatalf("LookupAckRoute before TTL: got %v want %v", got, srv)
	}
	time.Sleep(200 * time.Millisecond)
	if got := m.LookupAckRoute("msg-1"); got != nil {
		t.Errorf("LookupAckRoute after TTL: got %v want nil", got)
	}
}

func TestClearAckRoute(t *testing.T) {
	p := newPair(t)
	defer p.cleanupFn()
	srv := p.serverConn(t)

	m := NewManager(makeNode("substrate-1"), &fakePeerer{}, Options{Inbound: InboundTrue, MaxChildren: DefaultMaxChildren})
	defer m.Stop()

	m.RecordAckRoute("msg-1", srv, 5*time.Second)
	if m.LookupAckRoute("msg-1") == nil {
		t.Fatal("setup: route missing")
	}
	m.ClearAckRoute("msg-1")
	if got := m.LookupAckRoute("msg-1"); got != nil {
		t.Errorf("LookupAckRoute after Clear: got %v want nil", got)
	}
}

// ---- child broadcast (phase-5 prep) ----------------------------------

func TestBroadcastToChildren(t *testing.T) {
	p1 := newPair(t)
	defer p1.cleanupFn()
	srv1 := p1.serverConn(t)
	p2 := newPair(t)
	defer p2.cleanupFn()
	srv2 := p2.serverConn(t)

	m := NewManager(makeNode("substrate-1"), &fakePeerer{}, Options{Inbound: InboundTrue, MaxChildren: DefaultMaxChildren})
	defer m.Stop()

	m.HandleHello(srv1, &ws.HelloPayload{NodeID: "t1", Enclave: "default"})
	m.HandleHello(srv2, &ws.HelloPayload{NodeID: "t2", Enclave: "default"})

	got1 := make(chan *gossip.Message, 1)
	got2 := make(chan *gossip.Message, 1)
	p1.clientWS.OnMessage(func(msg *gossip.Message) { got1 <- msg })
	p2.clientWS.OnMessage(func(msg *gossip.Message) { got2 <- msg })

	msg := &gossip.Message{
		Type:      gossip.MessageTypePut,
		From:      "peer-x",
		Key:       "k",
		Data:      []byte("v"),
		TTL:       60,
		Timestamp: time.Now(),
		MessageID: "broadcast-test",
	}
	m.BroadcastToChildren(msg)

	for i, ch := range []<-chan *gossip.Message{got1, got2} {
		select {
		case r := <-ch:
			if r.MessageID != "broadcast-test" {
				t.Errorf("child %d: id=%q want broadcast-test", i+1, r.MessageID)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("child %d never received broadcast", i+1)
		}
	}
}

// TestEmptyEnclaveNormalizedOnHello — review finding #4: a hello with an
// empty Enclave field used to slip past the BroadcastToChildren filter
// (which only skipped when enclave was non-empty AND mismatched). After
// normalization the substrate runs in "default" and the child registers
// as "default", so the filter still admits in-enclave traffic but a
// substrate in a NON-default enclave will correctly skip the empty-enclave
// child.
func TestEmptyEnclaveNormalizedOnHello(t *testing.T) {
	p := newPair(t)
	defer p.cleanupFn()
	srv := p.serverConn(t)

	// Substrate in a non-default enclave.
	m := NewManager(
		makeNode("substrate-1", func(n *gossip.Node) { n.Enclave = "alpha" }),
		&fakePeerer{},
		Options{Inbound: InboundTrue, MaxChildren: DefaultMaxChildren},
	)
	defer m.Stop()

	// Hello with empty enclave (the bypass case).
	if !m.HandleHello(srv, &ws.HelloPayload{NodeID: "t1", Enclave: ""}) {
		t.Fatal("HandleHello rejected hello with empty enclave")
	}
	if got := srv.RemoteEnclave(); got != "default" {
		t.Errorf("RemoteEnclave: got %q want default (normalization)", got)
	}

	// Substrate enclave is "alpha"; the child normalized to "default" —
	// the filter must drop the broadcast.
	var calls atomic.Int32
	p.clientWS.OnMessage(func(*gossip.Message) { calls.Add(1) })

	msg := &gossip.Message{
		Type:      gossip.MessageTypePut,
		From:      "alpha-peer",
		Key:       "k",
		Data:      []byte("v"),
		TTL:       60,
		Timestamp: time.Now(),
		MessageID: "empty-enclave",
	}
	m.BroadcastToChildren(msg)
	time.Sleep(100 * time.Millisecond)
	if n := calls.Load(); n != 0 {
		t.Errorf("empty-enclave child received broadcast in non-default substrate: %d times", n)
	}
}

func TestBroadcastToChildrenSkipsOtherEnclave(t *testing.T) {
	p := newPair(t)
	defer p.cleanupFn()
	srv := p.serverConn(t)

	m := NewManager(makeNode("substrate-1"), &fakePeerer{}, Options{Inbound: InboundTrue, MaxChildren: DefaultMaxChildren})
	defer m.Stop()

	m.HandleHello(srv, &ws.HelloPayload{NodeID: "t1", Enclave: "other-enclave"})

	var calls atomic.Int32
	p.clientWS.OnMessage(func(*gossip.Message) { calls.Add(1) })

	msg := &gossip.Message{
		Type:      gossip.MessageTypePut,
		From:      "peer-x",
		Key:       "k",
		Data:      []byte("v"),
		TTL:       60,
		Timestamp: time.Now(),
		MessageID: "cross-enclave",
	}
	m.BroadcastToChildren(msg)

	time.Sleep(100 * time.Millisecond)
	if n := calls.Load(); n != 0 {
		t.Errorf("child in other enclave received broadcast: %d times", n)
	}
}

// ---- helpers ----------------------------------------------------------

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
