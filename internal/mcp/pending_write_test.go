package mcp

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sopholeth/internal/cluster"
	"sopholeth/internal/gossip"
)

// Regression tests for #170: the store tool adds no deadline of its own, so
// a write whose call context ends while quorum is stalled reports
// quorum_status "pending", not an error. A context that ended before the
// store, or a store that failed, is still an error.

// newStalledQuorumMCPServer returns an MCP server whose node has one enclave
// peer that never ACKs, a write timeout of an hour, and a store capacity of
// maxStorageBytes (0 = unlimited).
func newStalledQuorumMCPServer(t *testing.T, maxStorageBytes int64) (*Server, *cluster.ClusterNode) {
	t.Helper()
	silentPeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(silentPeer.Close)
	silentPeerPort := silentPeer.Listener.Addr().(*net.TCPAddr).Port

	clusterNode := cluster.NewClusterNode("mcp-stalled", "localhost", 0, 8080, 3, maxStorageBytes, time.Hour, "", "default")
	ctx, cancel := context.WithCancel(context.Background())
	if err := clusterNode.Start(ctx, nil); err != nil {
		t.Fatalf("failed to start cluster node: %v", err)
	}
	t.Cleanup(func() {
		clusterNode.Stop()
		cancel()
	})
	if response := clusterNode.HandleBootstrap(&gossip.BootstrapRequest{
		NodeID: "silent-peer", Address: "127.0.0.1", GossipPort: silentPeerPort, HTTPPort: silentPeerPort, Enclave: "default",
	}); !response.Success {
		t.Fatal("bootstrap registration of the silent peer rejected")
	}
	return NewServer(clusterNode, strings.NewReader(""), io.Discard, 300, 86400), clusterNode
}

func TestStorePendingWhenContextEndsStalledQuorum(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		endEarly func() (context.Context, context.CancelFunc)
	}{
		{"deadline", func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 300*time.Millisecond)
		}},
		{"client cancel", func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(200*time.Millisecond, cancel)
			return ctx, cancel
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server, clusterNode := newStalledQuorumMCPServer(t, 0)
			ctx, cancel := testCase.endEarly()
			defer cancel()

			payload, err := server.toolStore(ctx, map[string]any{"key": "stalled-key", "data": "stalled-value", "ttl_seconds": 600})
			if err != nil {
				t.Fatalf("store returned error %v, want quorum_status pending", err)
			}
			if status := payload.(map[string]any)["quorum_status"]; status != "pending" {
				t.Fatalf("quorum_status = %v, want pending", status)
			}
			if stored, exists := clusterNode.Get("stalled-key"); !exists || string(stored) != "stalled-value" {
				t.Fatalf("node holds %q (exists=%v), want the stored value", stored, exists)
			}
		})
	}
}

func TestStoreFailsWhenContextEndedBeforeStore(t *testing.T) {
	server, clusterNode := newStalledQuorumMCPServer(t, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := server.toolStore(ctx, map[string]any{"key": "gone-key", "data": "never-stored", "ttl_seconds": 600}); err == nil {
		t.Fatal("store succeeded for a call whose context had already ended")
	}
	if _, exists := clusterNode.Get("gone-key"); exists {
		t.Fatal("node stored a write whose call had already ended")
	}
}

func TestStoreFailsWhenStoreFullWithStalledQuorum(t *testing.T) {
	server, _ := newStalledQuorumMCPServer(t, 64)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	_, err := server.toolStore(ctx, map[string]any{"key": "too-big", "data": strings.Repeat("x", 128), "ttl_seconds": 600})
	if err == nil || !strings.Contains(err.Error(), "storage capacity exceeded") {
		t.Fatalf("store returned %v, want storage capacity exceeded", err)
	}
}
