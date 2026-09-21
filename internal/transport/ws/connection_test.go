package ws

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
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
)

// ---- helpers ----------------------------------------------------------

func newPair(t *testing.T, clusterSecret string) (server *Connection, client *Connection, cleanup func()) {
	t.Helper()
	var serverConn *Connection
	ready := make(chan struct{})

	srv := httptest.NewServer(Handler(clusterSecret, nil, func(c *Connection) {
		serverConn = c
		close(ready)
	}))

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/ws"
	u, err := url.Parse(wsURL)
	if err != nil {
		srv.Close()
		t.Fatalf("parse ws url: %v", err)
	}
	dialer := &websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	clientWS, resp, err := dialer.Dial(u.String(), nil)
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		srv.Close()
		t.Fatalf("dial: %v", err)
	}
	clientConn := NewConnection(clientWS, clusterSecret)

	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		clientConn.Close(websocket.CloseNormalClosure, "")
		srv.Close()
		t.Fatalf("server side never accepted")
	}

	return serverConn, clientConn, func() {
		clientConn.Close(websocket.CloseNormalClosure, "")
		serverConn.Close(websocket.CloseNormalClosure, "")
		srv.Close()
	}
}

func sampleMessage(overrides func(*gossip.Message)) *gossip.Message {
	m := &gossip.Message{
		Type:      gossip.MessageTypePut,
		From:      "node-a",
		Key:       "test-key",
		Data:      []byte("hello world"),
		TTL:       300,
		Timestamp: time.Unix(1735689600, 0),
		MessageID: "test-msg-1",
	}
	if overrides != nil {
		overrides(m)
	}
	return m
}

// waitOnMessage blocks until ch receives or the timeout fires.
func waitOnMessage[T any](t *testing.T, ch <-chan T, timeout time.Duration) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for message")
		var zero T
		return zero
	}
}

// writeRaw bypasses the AttachmentMessage envelope to inject arbitrary bytes.
// White-box helper for testing rejection of malformed frames.
func (c *Connection) writeRaw(t *testing.T, data []byte) {
	t.Helper()
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(time.Second))
	if err := c.ws.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("writeRaw: %v", err)
	}
}

// ---- gossip round-trip tests -----------------------------------------

func TestSendAndReceiveGossipMessage(t *testing.T) {
	server, client, cleanup := newPair(t, "")
	defer cleanup()

	got := make(chan *gossip.Message, 1)
	server.OnMessage(func(m *gossip.Message) { got <- m })

	if err := client.SendGossip(sampleMessage(nil)); err != nil {
		t.Fatalf("send: %v", err)
	}
	m := waitOnMessage(t, got, 2*time.Second)

	if m.Type != gossip.MessageTypePut {
		t.Errorf("type: got %q want PUT", m.Type)
	}
	if m.From != "node-a" {
		t.Errorf("from: got %q", m.From)
	}
	if m.Key != "test-key" {
		t.Errorf("key: got %q", m.Key)
	}
	if string(m.Data) != "hello world" {
		t.Errorf("data: got %q", m.Data)
	}
	if m.TTL != 300 {
		t.Errorf("ttl: got %d", m.TTL)
	}
	if m.MessageID != "test-msg-1" {
		t.Errorf("message_id: got %q", m.MessageID)
	}
}

func TestAllMessageTypesRoundTrip(t *testing.T) {
	server, client, cleanup := newPair(t, "")
	defer cleanup()

	for _, mt := range []gossip.MessageType{
		gossip.MessageTypePut,
		gossip.MessageTypeAck,
		gossip.MessageTypePing,
		gossip.MessageTypePong,
		gossip.MessageTypeSync,
	} {
		t.Run(string(mt), func(t *testing.T) {
			got := make(chan *gossip.Message, 1)
			server.OnMessage(func(m *gossip.Message) { got <- m })

			id := "msg-" + string(mt)
			msg := sampleMessage(func(m *gossip.Message) {
				m.Type = mt
				m.MessageID = id
			})
			if err := client.SendGossip(msg); err != nil {
				t.Fatalf("send: %v", err)
			}
			r := waitOnMessage(t, got, 2*time.Second)
			if r.Type != mt {
				t.Errorf("type: got %q want %q", r.Type, mt)
			}
			if r.MessageID != id {
				t.Errorf("id: got %q want %q", r.MessageID, id)
			}
		})
	}
}

func TestPreservesBinaryData(t *testing.T) {
	server, client, cleanup := newPair(t, "")
	defer cleanup()

	binary := []byte{0x00, 0xff, 0x42, 0xde, 0xad, 0xbe, 0xef}
	got := make(chan *gossip.Message, 1)
	server.OnMessage(func(m *gossip.Message) { got <- m })

	msg := sampleMessage(func(m *gossip.Message) { m.Data = binary })
	if err := client.SendGossip(msg); err != nil {
		t.Fatalf("send: %v", err)
	}
	r := waitOnMessage(t, got, 2*time.Second)
	if !bytes.Equal(r.Data, binary) {
		t.Errorf("binary mismatch: got %x want %x", r.Data, binary)
	}
}

func TestPreservesNodeInfoInSync(t *testing.T) {
	server, client, cleanup := newPair(t, "")
	defer cleanup()

	got := make(chan *gossip.Message, 1)
	server.OnMessage(func(m *gossip.Message) { got <- m })

	msg := sampleMessage(func(m *gossip.Message) {
		m.Type = gossip.MessageTypeSync
		m.NodeInfo = &gossip.Node{
			ID:       "node-a",
			Address:  "192.168.1.1",
			Port:     9090,
			HTTPPort: 8080,
			Enclave:  "acme-corp",
		}
	})
	if err := client.SendGossip(msg); err != nil {
		t.Fatalf("send: %v", err)
	}
	r := waitOnMessage(t, got, 2*time.Second)
	if r.NodeInfo == nil {
		t.Fatal("NodeInfo missing")
	}
	if r.NodeInfo.ID != "node-a" {
		t.Errorf("id: got %q", r.NodeInfo.ID)
	}
	if r.NodeInfo.Enclave != "acme-corp" {
		t.Errorf("enclave: got %q", r.NodeInfo.Enclave)
	}
	if r.NodeInfo.HTTPPort != 8080 {
		t.Errorf("http_port: got %d", r.NodeInfo.HTTPPort)
	}
}

// ---- wire format parity with HTTP gossip ------------------------------

func TestWirePayloadMatchesHTTPGossipFormat(t *testing.T) {
	// The AttachmentMessage payload for a gossip frame must marshal to the
	// same bytes that the HTTP gossip endpoint expects to receive. This is
	// the invariant that lets handlers process WS and HTTP frames identically.
	msg := sampleMessage(nil)
	wire := messageToWire(msg)
	wsPayload, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal wire: %v", err)
	}

	// Reconstruct what the HTTP transport would send for the same Message.
	httpWire := &gossip.SimpleMessage{
		Type:      string(msg.Type),
		From:      string(msg.From),
		Key:       msg.Key,
		Data:      msg.Data,
		TTL:       int32(msg.TTL),
		Timestamp: msg.Timestamp.Unix(),
		MessageID: msg.MessageID,
	}
	httpBytes, err := json.Marshal(httpWire)
	if err != nil {
		t.Fatalf("marshal http: %v", err)
	}
	if !bytes.Equal(wsPayload, httpBytes) {
		t.Errorf("wire mismatch:\nws:   %s\nhttp: %s", wsPayload, httpBytes)
	}
}

// ---- bidirectional ----------------------------------------------------

func TestBidirectionalGossip(t *testing.T) {
	server, client, cleanup := newPair(t, "")
	defer cleanup()

	srvGot := make(chan *gossip.Message, 1)
	cliGot := make(chan *gossip.Message, 1)
	server.OnMessage(func(m *gossip.Message) { srvGot <- m })
	client.OnMessage(func(m *gossip.Message) { cliGot <- m })

	if err := client.SendGossip(sampleMessage(func(m *gossip.Message) {
		m.From = "client"
		m.MessageID = "c1"
	})); err != nil {
		t.Fatalf("client send: %v", err)
	}
	if err := server.SendGossip(sampleMessage(func(m *gossip.Message) {
		m.From = "server"
		m.MessageID = "s1"
	})); err != nil {
		t.Fatalf("server send: %v", err)
	}

	if m := waitOnMessage(t, srvGot, 2*time.Second); m.From != "client" {
		t.Errorf("server received from %q", m.From)
	}
	if m := waitOnMessage(t, cliGot, 2*time.Second); m.From != "server" {
		t.Errorf("client received from %q", m.From)
	}
}

// ---- attachment messages (hello / welcome / goodbye) -----------------

func TestHelloRoundTrip(t *testing.T) {
	server, client, cleanup := newPair(t, "")
	defer cleanup()

	got := make(chan *AttachmentMessage, 1)
	server.AddAttachmentHandler(func(m *AttachmentMessage) { got <- m })

	hello := HelloPayload{
		NodeID:       "mcp-node-1",
		Enclave:      "acme-corp",
		Address:      "192.168.1.50",
		HTTPPort:     8080,
		Capabilities: Capabilities{Inbound: "false"},
	}
	if err := client.SendAttachment(AttachmentTypeHello, hello); err != nil {
		t.Fatalf("send: %v", err)
	}
	r := waitOnMessage(t, got, 2*time.Second)
	if r.Type != AttachmentTypeHello {
		t.Errorf("type: got %q", r.Type)
	}
	var p HelloPayload
	if err := json.Unmarshal(r.Payload, &p); err != nil {
		t.Fatalf("payload decode: %v", err)
	}
	if p.NodeID != "mcp-node-1" {
		t.Errorf("node_id: got %q", p.NodeID)
	}
	if p.Enclave != "acme-corp" {
		t.Errorf("enclave: got %q", p.Enclave)
	}
	if p.Capabilities.Inbound != "false" {
		t.Errorf("inbound: got %q", p.Capabilities.Inbound)
	}
}

func TestGoodbyeRoundTrip(t *testing.T) {
	server, client, cleanup := newPair(t, "")
	defer cleanup()

	got := make(chan *AttachmentMessage, 1)
	client.AddAttachmentHandler(func(m *AttachmentMessage) { got <- m })

	bye := GoodbyePayload{
		Reason: "shutdown",
		AlternativeParents: []AlternativeParent{
			{ID: "cloud-1", Address: "10.0.0.1", HTTPPort: 8080, Enclave: "default"},
			{ID: "cloud-2", Address: "10.0.0.2", HTTPPort: 8080},
		},
	}
	if err := server.SendAttachment(AttachmentTypeGoodbye, bye); err != nil {
		t.Fatalf("send: %v", err)
	}
	r := waitOnMessage(t, got, 2*time.Second)
	if r.Type != AttachmentTypeGoodbye {
		t.Errorf("type: got %q", r.Type)
	}
	var p GoodbyePayload
	if err := json.Unmarshal(r.Payload, &p); err != nil {
		t.Fatalf("payload decode: %v", err)
	}
	if p.Reason != "shutdown" {
		t.Errorf("reason: got %q", p.Reason)
	}
	if len(p.AlternativeParents) != 2 {
		t.Errorf("alts: got %d", len(p.AlternativeParents))
	}
	if p.AlternativeParents[0].ID != "cloud-1" {
		t.Errorf("alt[0].id: got %q", p.AlternativeParents[0].ID)
	}
}

func TestWelcomeRoundTrip(t *testing.T) {
	server, client, cleanup := newPair(t, "")
	defer cleanup()

	got := make(chan *AttachmentMessage, 1)
	client.AddAttachmentHandler(func(m *AttachmentMessage) { got <- m })

	welcome := WelcomePayload{
		Topology: []gossip.SimpleMessage{
			{Type: "SYNC", From: "sub-1", MessageID: "topo-1", Timestamp: 1, NodeInfo: &gossip.SimpleNodeInfo{
				ID: "peer-x", Address: "10.0.0.5", Port: 9090, HTTPPort: 8080, Enclave: "default",
			}},
		},
		YourPosition: WirePosition{Depth: 1, ParentID: "sub-1"},
	}
	if err := server.SendAttachment(AttachmentTypeWelcome, welcome); err != nil {
		t.Fatalf("send: %v", err)
	}
	r := waitOnMessage(t, got, 2*time.Second)
	if r.Type != AttachmentTypeWelcome {
		t.Errorf("type: got %q", r.Type)
	}
	var p WelcomePayload
	if err := json.Unmarshal(r.Payload, &p); err != nil {
		t.Fatalf("payload decode: %v", err)
	}
	if p.YourPosition.ParentID != "sub-1" || p.YourPosition.Depth != 1 {
		t.Errorf("position: got %+v", p.YourPosition)
	}
	if len(p.Topology) != 1 || p.Topology[0].NodeInfo == nil || p.Topology[0].NodeInfo.ID != "peer-x" {
		t.Errorf("topology decode mismatch: %+v", p.Topology)
	}
}

func TestHelloDoesNotFireOnMessage(t *testing.T) {
	server, client, cleanup := newPair(t, "")
	defer cleanup()

	var msgCalls atomic.Int32
	server.OnMessage(func(*gossip.Message) { msgCalls.Add(1) })

	attachCh := make(chan struct{}, 1)
	server.AddAttachmentHandler(func(*AttachmentMessage) { attachCh <- struct{}{} })

	hello := HelloPayload{
		NodeID: "n1", Enclave: "default", Address: "127.0.0.1",
		HTTPPort: 8080, Capabilities: Capabilities{Inbound: "false"},
	}
	if err := client.SendAttachment(AttachmentTypeHello, hello); err != nil {
		t.Fatalf("send: %v", err)
	}
	<-attachCh
	time.Sleep(50 * time.Millisecond)
	if n := msgCalls.Load(); n != 0 {
		t.Errorf("OnMessage fired for lifecycle frame: %d times", n)
	}
}

// ---- close and post-close behavior -----------------------------------

func TestReportsClosedStateAfterClose(t *testing.T) {
	server, client, cleanup := newPair(t, "")
	defer cleanup()

	if client.IsClosed() {
		t.Fatal("client closed before Close()")
	}

	srvClosed := make(chan struct{}, 1)
	server.AddCloseHandler(func(int, string) { srvClosed <- struct{}{} })

	client.Close(websocket.CloseNormalClosure, "")

	select {
	case <-srvClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("server never observed close")
	}
	if !client.IsClosed() {
		t.Error("client.IsClosed() still false")
	}
}

func TestSendAfterCloseIsSilent(t *testing.T) {
	_, client, cleanup := newPair(t, "")
	defer cleanup()

	client.Close(websocket.CloseNormalClosure, "")

	// Neither call should panic or return an error.
	if err := client.SendGossip(sampleMessage(nil)); err != nil {
		t.Errorf("SendGossip after close: %v", err)
	}
	if err := client.SendAttachment(AttachmentTypeHello, HelloPayload{
		NodeID: "n", Enclave: "d", Address: "x", HTTPPort: 0,
		Capabilities: Capabilities{Inbound: "false"},
	}); err != nil {
		t.Errorf("SendAttachment after close: %v", err)
	}
}

// ---- invalid frames ---------------------------------------------------

func TestIgnoresInvalidJSON(t *testing.T) {
	server, client, cleanup := newPair(t, "")
	defer cleanup()

	var calls atomic.Int32
	server.AddAttachmentHandler(func(*AttachmentMessage) { calls.Add(1) })

	client.writeRaw(t, []byte("not json {{{"))
	time.Sleep(100 * time.Millisecond)
	if n := calls.Load(); n != 0 {
		t.Errorf("attachment fired for invalid JSON: %d times", n)
	}
}

func TestIgnoresMessagesMissingTypeOrPayload(t *testing.T) {
	server, client, cleanup := newPair(t, "")
	defer cleanup()

	var calls atomic.Int32
	server.AddAttachmentHandler(func(*AttachmentMessage) { calls.Add(1) })

	client.writeRaw(t, []byte(`{"type":"put"}`)) // missing payload
	client.writeRaw(t, []byte(`{"payload":{}}`)) // missing type
	time.Sleep(100 * time.Millisecond)
	if n := calls.Load(); n != 0 {
		t.Errorf("attachment fired for malformed envelope: %d times", n)
	}
}

// ---- HMAC -------------------------------------------------------------

func TestHMACAcceptsValidSignature(t *testing.T) {
	server, client, cleanup := newPair(t, "test-secret-42")
	defer cleanup()

	got := make(chan *gossip.Message, 1)
	server.OnMessage(func(m *gossip.Message) { got <- m })

	if err := client.SendGossip(sampleMessage(nil)); err != nil {
		t.Fatalf("send: %v", err)
	}
	m := waitOnMessage(t, got, 2*time.Second)
	if m.Type != gossip.MessageTypePut || m.Key != "test-key" {
		t.Errorf("unexpected message: %+v", m)
	}
}

func TestHMACRejectsTamperedSignature(t *testing.T) {
	server, client, cleanup := newPair(t, "test-secret-42")
	defer cleanup()

	var calls atomic.Int32
	server.OnMessage(func(*gossip.Message) { calls.Add(1) })

	wire := messageToWire(sampleMessage(nil))
	wireBytes, _ := json.Marshal(wire)
	env := AttachmentMessage{
		Type:      AttachmentTypePut,
		Signature: strings.Repeat("deadbeef", 8),
		Payload:   wireBytes,
	}
	frame, _ := json.Marshal(env)
	client.writeRaw(t, frame)

	time.Sleep(100 * time.Millisecond)
	if n := calls.Load(); n != 0 {
		t.Errorf("OnMessage fired for tampered frame: %d times", n)
	}
}

func TestHMACRejectsMissingSignature(t *testing.T) {
	server, client, cleanup := newPair(t, "test-secret-42")
	defer cleanup()

	var calls atomic.Int32
	server.OnMessage(func(*gossip.Message) { calls.Add(1) })

	wire := messageToWire(sampleMessage(nil))
	wireBytes, _ := json.Marshal(wire)
	env := AttachmentMessage{Type: AttachmentTypePut, Payload: wireBytes}
	frame, _ := json.Marshal(env)
	client.writeRaw(t, frame)

	time.Sleep(100 * time.Millisecond)
	if n := calls.Load(); n != 0 {
		t.Errorf("OnMessage fired for unsigned frame: %d times", n)
	}
}

func TestNoSigningWithoutSecret(t *testing.T) {
	// Capture the raw bytes that hit the server-side WS by registering an
	// onMessage handler — the AttachmentMessage's Signature field tells us
	// whether the sender added one.
	server, client, cleanup := newPair(t, "")
	defer cleanup()

	got := make(chan *AttachmentMessage, 1)
	server.AddAttachmentHandler(func(m *AttachmentMessage) { got <- m })

	if err := client.SendGossip(sampleMessage(nil)); err != nil {
		t.Fatalf("send: %v", err)
	}
	r := waitOnMessage(t, got, 2*time.Second)
	if r.Signature != "" {
		t.Errorf("unexpected signature on no-secret frame: %q", r.Signature)
	}
}

// ---- heartbeat --------------------------------------------------------

func newPairWithHeartbeat(t *testing.T, period time.Duration) (*Connection, *Connection, func()) {
	t.Helper()
	server, client, cleanup := newPair(t, "")
	server.heartbeatPeriod = period
	client.heartbeatPeriod = period
	return server, client, cleanup
}

// pingCountingConn wraps a Connection and counts ping frames received by the
// underlying gorilla.*Conn — used to verify the heartbeat actually sends pings.
type pingCountingHook struct {
	count atomic.Int32
}

func installPingHook(t *testing.T, c *Connection) *pingCountingHook {
	t.Helper()
	h := &pingCountingHook{}
	c.ws.SetPingHandler(func(appData string) error {
		h.count.Add(1)
		// Respond with pong as gorilla's default ping handler would.
		_ = c.ws.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(time.Second))
		return nil
	})
	return h
}

func TestHeartbeatSendsPings(t *testing.T) {
	server, client, cleanup := newPairWithHeartbeat(t, 50*time.Millisecond)
	defer cleanup()

	hook := installPingHook(t, server)
	client.StartHeartbeat()
	defer client.StopHeartbeat()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hook.count.Load() >= 2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("expected >= 2 pings, got %d", hook.count.Load())
}

func TestHeartbeatTerminatesAfterMaxMissed(t *testing.T) {
	server, client, cleanup := newPairWithHeartbeat(t, 30*time.Millisecond)
	defer cleanup()

	// Silence the server's pong replies by replacing the ping handler with
	// a no-op. The client will never receive pongs and should self-terminate
	// after MaxMissedPongs intervals.
	server.ws.SetPingHandler(func(string) error { return nil })

	closed := make(chan struct{}, 1)
	client.AddCloseHandler(func(int, string) { closed <- struct{}{} })

	client.StartHeartbeat()
	defer client.StopHeartbeat()

	// Allow MaxMissedPongs+1 ticks plus slack.
	timeout := time.Duration(MaxMissedPongs+2) * 30 * time.Millisecond * 3
	select {
	case <-closed:
	case <-time.After(timeout):
		t.Fatalf("heartbeat did not terminate after missed pongs (%v)", timeout)
	}
	if !client.IsClosed() {
		t.Error("client.IsClosed() false after heartbeat termination")
	}
}

func TestHeartbeatResetsCounterOnPong(t *testing.T) {
	server, client, cleanup := newPairWithHeartbeat(t, 30*time.Millisecond)
	defer cleanup()

	// Server responds to pings as normal (default handler). The client
	// should continue to receive pongs and never terminate.
	installPingHook(t, server)

	closed := make(chan struct{}, 1)
	client.AddCloseHandler(func(int, string) { closed <- struct{}{} })

	client.StartHeartbeat()
	defer client.StopHeartbeat()

	select {
	case <-closed:
		t.Fatal("client terminated even though pongs were arriving")
	case <-time.After(time.Duration(MaxMissedPongs+2) * 30 * time.Millisecond):
		// expected — connection remained healthy
	}
}

func TestStopHeartbeatOnClose(t *testing.T) {
	_, client, cleanup := newPairWithHeartbeat(t, 20*time.Millisecond)
	defer cleanup()

	client.StartHeartbeat()
	client.Close(websocket.CloseNormalClosure, "")
	// If the heartbeat goroutine kept running, it would panic on write to a
	// closed conn or busy-loop. Sleep gives it time to react.
	time.Sleep(80 * time.Millisecond)
}

// ---- ConnectToSubstrate factory --------------------------------------

func TestConnectToSubstrateConnectsToV1WS(t *testing.T) {
	accepted := make(chan struct{}, 1)
	mux := http.NewServeMux()
	mux.Handle("/v1/ws", Handler("", nil, func(*Connection) { accepted <- struct{}{} }))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	host, port := splitHostPort(t, u.Host)

	conn, err := ConnectToSubstrate(context.Background(), host, port, "", time.Second)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(websocket.CloseNormalClosure, "")

	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("server never accepted /v1/ws")
	}
	if conn.IsClosed() {
		t.Error("conn already closed")
	}
}

func TestConnectToSubstrateTimesOut(t *testing.T) {
	// TCP listener that accepts but never speaks HTTP, so the WS handshake
	// stalls until the dial timeout fires.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		// Block — but keep the request alive by sleeping past the dial timeout.
		time.Sleep(time.Second)
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	host, port := splitHostPort(t, u.Host)

	_, err := ConnectToSubstrate(context.Background(), host, port, "", 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestConnectToSubstrateExchangesGossip(t *testing.T) {
	got := make(chan *gossip.Message, 1)
	var serverConn *Connection
	var mu sync.Mutex

	mux := http.NewServeMux()
	mux.Handle("/v1/ws", Handler("", nil, func(c *Connection) {
		mu.Lock()
		serverConn = c
		mu.Unlock()
		c.OnMessage(func(m *gossip.Message) { got <- m })
	}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	host, port := splitHostPort(t, u.Host)

	client, err := ConnectToSubstrate(context.Background(), host, port, "", time.Second)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close(websocket.CloseNormalClosure, "")

	if err := client.SendGossip(sampleMessage(nil)); err != nil {
		t.Fatalf("send: %v", err)
	}
	r := waitOnMessage(t, got, 2*time.Second)
	if r.Type != gossip.MessageTypePut || r.Key != "test-key" {
		t.Errorf("unexpected message: %+v", r)
	}
	mu.Lock()
	if serverConn != nil {
		serverConn.Close(websocket.CloseNormalClosure, "")
	}
	mu.Unlock()
}

// ---- helpers ----------------------------------------------------------

func splitHostPort(t *testing.T, hostPort string) (string, int) {
	t.Helper()
	idx := strings.LastIndex(hostPort, ":")
	if idx == -1 {
		t.Fatalf("no port in %q", hostPort)
	}
	host := hostPort[:idx]
	port, err := strconv.Atoi(hostPort[idx+1:])
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	return host, port
}
