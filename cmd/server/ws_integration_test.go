package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	gws "github.com/gorilla/websocket"

	"sopholeth/internal/cluster"
	"sopholeth/internal/gossip"
	"sopholeth/internal/node"
	"sopholeth/internal/transport/ws"
	"sopholeth/internal/tree"
)

// newSubstrateTestServer spins up an HTTPServer running as a substrate
// (inbound=true) with a single-node cluster behind it. Returns the test
// httptest.Server, the cluster node (for direct store inspection), the
// tree manager (for state assertions), and a cleanup func.
func newSubstrateTestServer(t *testing.T) (*httptest.Server, *cluster.ClusterNode, *tree.Manager, func()) {
	t.Helper()
	cn := cluster.NewClusterNode(
		"substrate-1", "127.0.0.1", 0, 0,
		1, 0, 5*time.Second, "", "default",
	)
	ctx, cancel := context.WithCancel(context.Background())
	if err := cn.Start(ctx, nil); err != nil {
		t.Fatalf("cluster start: %v", err)
	}
	tm := tree.NewManager(
		&gossip.Node{ID: "substrate-1", Address: "127.0.0.1", Port: 0, HTTPPort: 0, Enclave: "default"},
		clusterPeerer{cn: cn},
		tree.Options{Inbound: tree.InboundTrue, MaxChildren: tree.DefaultMaxChildren},
	)
	cn.SetAckRouter(tm)
	cn.SetChildBroadcaster(tm)

	securityMW := node.NewSecurityMiddleware(1000, 2000, 10*1024*1024, false)
	server := &HTTPServer{
		clusterNode: cn,
		treeManager: tm,
		nodeID:      "substrate-1",
		network:     "private",
		minTTL:      300,
		maxTTL:      86400,
		startTime:   time.Now(),
		securityMW:  securityMW,
	}

	outerMux := http.NewServeMux()
	outerMux.HandleFunc("/v1/ws", server.wsHandler)
	outerMux.Handle("/", server.Router())
	srv := httptest.NewServer(outerMux)

	cleanup := func() {
		srv.Close()
		securityMW.Close()
		tm.Stop()
		cn.Stop()
		cancel()
	}
	return srv, cn, tm, cleanup
}

// dialWS opens a raw WS to the given httptest server's /v1/ws.
func dialWS(t *testing.T, srv *httptest.Server) *ws.Connection {
	t.Helper()
	u, _ := url.Parse(srv.URL)
	wsURL := "ws://" + u.Host + "/v1/ws"
	d := &gws.Dialer{HandshakeTimeout: 2 * time.Second}
	raw, resp, err := d.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		t.Fatalf("dial /v1/ws: %v", err)
	}
	return ws.NewConnection(raw, "")
}

// TestWSAttachHandshake exercises the hello/welcome handshake against the
// real HTTPServer wiring — verifies that /v1/ws is routed, the tree
// manager handles hello, and welcome reaches the client.
func TestWSAttachHandshake(t *testing.T) {
	srv, _, tm, cleanup := newSubstrateTestServer(t)
	defer cleanup()

	client := dialWS(t, srv)
	defer client.Close(gws.CloseNormalClosure, "")

	welcomeCh := make(chan *ws.WelcomePayload, 1)
	client.AddAttachmentHandler(func(msg *ws.AttachmentMessage) {
		if msg.Type != ws.AttachmentTypeWelcome {
			return
		}
		var w ws.WelcomePayload
		if err := json.Unmarshal(msg.Payload, &w); err == nil {
			welcomeCh <- &w
		}
	})

	hello := ws.HelloPayload{
		NodeID: "transient-A", Enclave: "default",
		Address: "10.0.0.1", HTTPPort: 0,
		Capabilities: ws.Capabilities{Inbound: "false"},
	}
	if err := client.SendAttachment(ws.AttachmentTypeHello, hello); err != nil {
		t.Fatalf("send hello: %v", err)
	}

	select {
	case w := <-welcomeCh:
		if w.YourPosition.ParentID != "substrate-1" {
			t.Errorf("parent_id: %q", w.YourPosition.ParentID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no welcome received")
	}

	// Wait briefly for child registration (HandleHello may complete after
	// the welcome was sent — both happen in the same goroutine; in practice
	// child is registered before SendAttachment returns, but be defensive).
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if tm.HasChild("transient-A") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !tm.HasChild("transient-A") {
		t.Errorf("tm.HasChild(transient-A) = false; child not registered")
	}
}

// TestWSPutStoresLocallyAndAcks exercises the relay round-trip end-to-end:
//   - Client sends PUT over WS
//   - Substrate stores it (relayed-PUT enclave-peer fanout is exercised
//     elsewhere via the existing HTTP gossip tests; we verify the local
//     store side here)
//   - Substrate sends an ACK over WS to the originator
//
// quorum=1 because the substrate is a single-node cluster — its local
// store IS the quorum.
func TestWSPutStoresLocallyAndAcks(t *testing.T) {
	srv, cn, _, cleanup := newSubstrateTestServer(t)
	defer cleanup()

	client := dialWS(t, srv)
	defer client.Close(gws.CloseNormalClosure, "")

	ackCh := make(chan *gossip.Message, 1)
	client.OnMessage(func(m *gossip.Message) {
		if m.Type == gossip.MessageTypeAck {
			ackCh <- m
		}
	})

	// Hello + wait for welcome.
	helloDone := make(chan struct{}, 1)
	client.AddAttachmentHandler(func(msg *ws.AttachmentMessage) {
		if msg.Type == ws.AttachmentTypeWelcome {
			select {
			case helloDone <- struct{}{}:
			default:
			}
		}
	})
	if err := client.SendAttachment(ws.AttachmentTypeHello, ws.HelloPayload{
		NodeID: "transient-A", Enclave: "default",
		Capabilities: ws.Capabilities{Inbound: "false"},
	}); err != nil {
		t.Fatalf("hello: %v", err)
	}
	<-helloDone

	// PUT over WS.
	put := &gossip.Message{
		Type:      gossip.MessageTypePut,
		From:      "transient-A",
		Key:       "relayed-key",
		Data:      []byte("relayed-value"),
		TTL:       300,
		Timestamp: time.Now(),
		MessageID: "ws-put-1",
	}
	if err := client.SendGossip(put); err != nil {
		t.Fatalf("send put: %v", err)
	}

	// Substrate should ACK back over WS.
	select {
	case ack := <-ackCh:
		if ack.MessageID != "ws-put-1" {
			t.Errorf("ack messageId: %q want ws-put-1", ack.MessageID)
		}
		if ack.Key != "relayed-key" {
			t.Errorf("ack key: %q", ack.Key)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no ACK received over WS")
	}

	// And the substrate's local store must contain the data — confirming
	// the relayed write hit the existing PUT handler path.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if data, ok := cn.Get("relayed-key"); ok && string(data) == "relayed-value" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if data, ok := cn.Get("relayed-key"); !ok || string(data) != "relayed-value" {
		t.Errorf("substrate store: data=%q ok=%v", data, ok)
	}
}

// TestWSReceivePathFanout exercises phase 5: when the substrate stores a
// PUT (e.g., received via HTTP gossip from an enclave peer), it fans the
// replica out over WS to attached transients. Here we drive
// HandleGossipMessage directly (simulating an HTTP arrival) and verify
// the attached WS client receives the PUT.
func TestWSReceivePathFanout(t *testing.T) {
	srv, cn, _, cleanup := newSubstrateTestServer(t)
	defer cleanup()

	client := dialWS(t, srv)
	defer client.Close(gws.CloseNormalClosure, "")

	gotPut := make(chan *gossip.Message, 1)
	client.OnMessage(func(m *gossip.Message) {
		if m.Type == gossip.MessageTypePut {
			gotPut <- m
		}
	})

	welcomeCh := make(chan struct{}, 1)
	client.AddAttachmentHandler(func(msg *ws.AttachmentMessage) {
		if msg.Type == ws.AttachmentTypeWelcome {
			select {
			case welcomeCh <- struct{}{}:
			default:
			}
		}
	})
	if err := client.SendAttachment(ws.AttachmentTypeHello, ws.HelloPayload{
		NodeID: "transient-B", Enclave: "default",
		Capabilities: ws.Capabilities{Inbound: "false"},
	}); err != nil {
		t.Fatalf("hello: %v", err)
	}
	<-welcomeCh

	// Simulate a PUT arriving from another enclave peer via HTTP gossip.
	msg := &gossip.Message{
		Type:      gossip.MessageTypePut,
		From:      "some-other-peer",
		Key:       "other-agents-key",
		Data:      []byte("other-agents-value"),
		TTL:       300,
		Timestamp: time.Now(),
		MessageID: "http-put-1",
	}
	if err := cn.HandleGossipMessage(msg); err != nil {
		t.Fatalf("HandleGossipMessage: %v", err)
	}

	select {
	case r := <-gotPut:
		if r.MessageID != "http-put-1" {
			t.Errorf("MessageID: %q", r.MessageID)
		}
		if r.Key != "other-agents-key" {
			t.Errorf("Key: %q", r.Key)
		}
		if string(r.Data) != "other-agents-value" {
			t.Errorf("Data: %q", r.Data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("attached transient never received fan-out")
	}
}

// TestWSRejectIfTransient verifies that nodes with inbound=false return
// 404 on /v1/ws — transients shouldn't expose the endpoint at all.
func TestWSRejectIfTransient(t *testing.T) {
	cn := cluster.NewClusterNode("transient-1", "127.0.0.1", 0, 0, 1, 0, 5*time.Second, "", "default")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := cn.Start(ctx, nil); err != nil {
		t.Fatalf("cluster: %v", err)
	}
	defer cn.Stop()

	tm := tree.NewManager(
		&gossip.Node{ID: "transient-1", Address: "127.0.0.1", Enclave: "default"},
		clusterPeerer{cn: cn},
		tree.Options{Inbound: tree.InboundFalse, MaxChildren: 0},
	)
	defer tm.Stop()

	securityMW := node.NewSecurityMiddleware(1000, 2000, 10*1024*1024, false)
	defer securityMW.Close()

	server := &HTTPServer{
		clusterNode: cn,
		treeManager: tm,
		nodeID:      "transient-1",
		network:     "private",
		minTTL:      300,
		maxTTL:      86400,
		startTime:   time.Now(),
		securityMW:  securityMW,
	}

	outerMux := http.NewServeMux()
	outerMux.HandleFunc("/v1/ws", server.wsHandler)
	outerMux.Handle("/", server.Router())
	srv := httptest.NewServer(outerMux)
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	wsURL := "ws://" + u.Host + "/v1/ws"
	_, resp, err := (&gws.Dialer{HandshakeTimeout: 2 * time.Second}).Dial(wsURL, nil)
	if err == nil {
		t.Fatal("expected dial to fail on transient /v1/ws")
	}
	if resp != nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("status: %d want 404", resp.StatusCode)
		}
	} else if !strings.Contains(err.Error(), "404") {
		t.Errorf("err: %v want 404-ish", err)
	}
}
