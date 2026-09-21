package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sopholeth/internal/cluster"
	"sopholeth/internal/node"
)

// newPublicTestServer builds an HTTPServer in public-network mode so the
// bootstrap-gate logic engages. Root status is controllable via the
// returned *cluster.ClusterNode.
func newPublicTestServer(t *testing.T) (*HTTPServer, *cluster.ClusterNode, func()) {
	t.Helper()

	cn := cluster.NewClusterNode(
		"public-test-node", "localhost", 0, 0,
		1,
		0,
		5*time.Second,
		"",
		"default",
	)

	ctx, cancel := context.WithCancel(context.Background())
	if err := cn.Start(ctx, nil); err != nil {
		t.Fatalf("start cluster: %v", err)
	}

	securityMW := node.NewSecurityMiddleware(1000, 2000, 10*1024*1024, false)

	server := &HTTPServer{
		clusterNode: cn,
		nodeID:      "public-test-node",
		network:     "public",
		minTTL:      300,
		maxTTL:      86400,
		startTime:   time.Now(),
		securityMW:  securityMW,
	}

	cleanup := func() {
		securityMW.Close()
		cn.Stop()
		cancel()
	}
	return server, cn, cleanup
}

// TestBootstrapHandler_NonRootReturns403 — a public-network node that has
// not been marked as root must refuse bootstrap requests. This is the
// cornerstone of the 2.1 trust model: only signed-list members hand out
// peer topology.
func TestBootstrapHandler_NonRootReturns403(t *testing.T) {
	server, cn, cleanup := newPublicTestServer(t)
	defer cleanup()

	// Default: not a root.
	if cn.IsRoot() {
		t.Fatalf("expected IsRoot()=false by default")
	}

	body := strings.NewReader(`{"node_id":"joiner","address":"x","gossip_port":9090,"http_port":8080}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/bootstrap", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.bootstrapHandler(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp["error"] != "not a bootstrap root" {
		t.Errorf("unexpected error body: %+v", resp)
	}
}

// TestBootstrapHandler_RootAnswers — once a node is marked root, it
// processes bootstrap requests normally.
func TestBootstrapHandler_RootAnswers(t *testing.T) {
	server, cn, cleanup := newPublicTestServer(t)
	defer cleanup()

	cn.SetRoot(true)

	body := strings.NewReader(`{"node_id":"joiner","address":"x","gossip_port":9090,"http_port":8080}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/bootstrap", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.bootstrapHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
}

// TestBootstrapHandler_PrivateNetworkBypass — private-network nodes skip
// the root check entirely. They drive peer discovery via NODE_PEERS, not
// the signed list, so the gate doesn't apply.
func TestBootstrapHandler_PrivateNetworkBypass(t *testing.T) {
	server, cleanup := newTestServer(t) // default: network="private"
	defer cleanup()

	body := strings.NewReader(`{"node_id":"joiner","address":"x","gossip_port":9090,"http_port":8080}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/bootstrap", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.bootstrapHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("private-network bootstrap status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
}
