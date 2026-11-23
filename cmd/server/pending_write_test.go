package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sopholeth/internal/cluster"
	"sopholeth/internal/gossip"
	"sopholeth/internal/node"
)

// Regression tests for #170: the PUT handler adds no deadline of its own, so
// a write whose request context ends while quorum is stalled is answered 202
// (stored, pending), not 500. A request that ended before the store, or a
// store that failed, is still an error.

// newStalledQuorumServer returns a server whose node has one enclave peer
// that accepts gossip and never ACKs, a write timeout of an hour, and a
// store capacity of maxStorageBytes (0 = unlimited). Every write waits for
// quorum until its request context ends.
func newStalledQuorumServer(t *testing.T, maxStorageBytes int64) *HTTPServer {
	t.Helper()
	return newStalledQuorumServerWithWriteTimeout(t, maxStorageBytes, time.Hour)
}

func newStalledQuorumServerWithWriteTimeout(t *testing.T, maxStorageBytes int64, writeTimeout time.Duration) *HTTPServer {
	t.Helper()
	silentPeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(silentPeer.Close)
	silentPeerPort := silentPeer.Listener.Addr().(*net.TCPAddr).Port

	clusterNode := cluster.NewClusterNode("stalled-node", "localhost", 0, 8080, 3, maxStorageBytes, writeTimeout, "", "default")
	ctx, cancel := context.WithCancel(context.Background())
	if err := clusterNode.Start(ctx, nil); err != nil {
		t.Fatalf("failed to start cluster node: %v", err)
	}
	if response := clusterNode.HandleBootstrap(&gossip.BootstrapRequest{
		NodeID: "silent-peer", Address: "127.0.0.1", GossipPort: silentPeerPort, HTTPPort: silentPeerPort, Enclave: "default",
	}); !response.Success {
		t.Fatal("bootstrap registration of the silent peer rejected")
	}

	securityMW := node.NewSecurityMiddleware(1000, 2000, 10*1024*1024, false)
	t.Cleanup(func() {
		securityMW.Close()
		clusterNode.Stop()
		cancel()
	})
	return &HTTPServer{
		clusterNode: clusterNode,
		nodeID:      "stalled-node",
		network:     "private",
		minTTL:      clusterNode.WriteLimits().MinTTLSeconds,
		maxTTL:      clusterNode.WriteLimits().MaxTTLSeconds,
		startTime:   time.Now(),
		securityMW:  securityMW,
	}
}

func putWithContext(server *HTTPServer, ctx context.Context, key, value string) *httptest.ResponseRecorder {
	request := httptest.NewRequest("PUT", "/v1/data/"+key, strings.NewReader(value)).WithContext(ctx)
	request.Header.Set("X-TTL", "600")
	recorder := httptest.NewRecorder()
	server.Router().ServeHTTP(recorder, request)
	return recorder
}

func expectNodeHolds(t *testing.T, server *HTTPServer, key, value string) {
	t.Helper()
	stored, exists := server.clusterNode.Get(key)
	if !exists || string(stored) != value {
		t.Fatalf("node holds %q (exists=%v) for %s, want %q", stored, exists, key, value)
	}
}

// A request deadline shorter than NODE_WRITE_TIMEOUT ends the quorum wait:
// the answer is 202, and the value is stored.
func TestPutAcceptedWhenRequestDeadlineEndsStalledQuorum(t *testing.T) {
	server := newStalledQuorumServer(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	recorder := putWithContext(server, ctx, "deadline-key", "deadline-value")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", recorder.Code, recorder.Body.String())
	}
	expectNodeHolds(t, server, "deadline-key", "deadline-value")
}

// A client that disconnects while the write waits for quorum is answered
// 202 too (it may never read it): the value was stored.
func TestPutAcceptedWhenClientCancelsDuringQuorumWait(t *testing.T) {
	server := newStalledQuorumServer(t, 0)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	defer cancel()

	recorder := putWithContext(server, ctx, "cancelled-key", "cancelled-value")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", recorder.Code, recorder.Body.String())
	}
	expectNodeHolds(t, server, "cancelled-key", "cancelled-value")
}

// A request that ended before the store is an error, and nothing is stored.
func TestPutFailsWhenRequestEndedBeforeStore(t *testing.T) {
	server := newStalledQuorumServer(t, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	recorder := putWithContext(server, ctx, "gone-key", "never-stored")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if _, exists := server.clusterNode.Get("gone-key"); exists {
		t.Fatal("node stored a write whose request had already ended")
	}
}

// A store that fails is answered 507, not 202, even with quorum stalled and
// a request deadline that will fire.
func TestPutStoreFullWithStalledQuorum(t *testing.T) {
	server := newStalledQuorumServer(t, 64)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	recorder := putWithContext(server, ctx, "too-big", strings.Repeat("x", 128))
	if recorder.Code != http.StatusInsufficientStorage {
		t.Fatalf("expected 507, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if _, exists := server.clusterNode.Get("too-big"); exists {
		t.Fatal("a write the store rejected is readable")
	}
}

// Over a real listener and the full middleware chain, a write whose quorum
// stalls until NODE_WRITE_TIMEOUT is answered 202, even when that timeout is
// longer than the request timeout every other route has.
func TestPutAcceptedOverListenerWhenWriteTimeoutOutlastsRequestTimeout(t *testing.T) {
	server := newStalledQuorumServerWithWriteTimeout(t, 0, 400*time.Millisecond)
	server.requestTimeout = 100 * time.Millisecond
	listener := httptest.NewServer(server.Router())
	defer listener.Close()

	request, err := http.NewRequest("PUT", listener.URL+"/v1/data/listener-key", strings.NewReader("listener-value"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("expected 202, got %d: %s", response.StatusCode, body)
	}
	expectNodeHolds(t, server, "listener-key", "listener-value")
}

// PUTs skip the request timeout, so their body read has its own deadline: an
// upload that stalls is cut off, and nothing is stored.
func TestPutBodyReadDeadlineCutsOffStalledUpload(t *testing.T) {
	server := newStalledQuorumServer(t, 0)
	server.putBodyReadTimeout = 200 * time.Millisecond
	listener := httptest.NewServer(server.Router())
	defer listener.Close()

	connection, err := net.Dial("tcp", listener.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	// Promise 100 bytes, send 5, then stall.
	fmt.Fprintf(connection, "PUT /v1/data/slow-key HTTP/1.1\r\nHost: test\r\nContent-Length: 100\r\n\r\nhello")

	connection.SetReadDeadline(time.Now().Add(5 * time.Second))
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err == nil {
		response.Body.Close()
		if response.StatusCode < 400 {
			t.Fatalf("stalled upload answered %d, want an error", response.StatusCode)
		}
	} else if netErr, isNetErr := err.(net.Error); isNetErr && netErr.Timeout() {
		t.Fatal("server did not cut off the stalled upload")
	}
	if _, exists := server.clusterNode.Get("slow-key"); exists {
		t.Fatal("a value from an incomplete upload was stored")
	}
}
