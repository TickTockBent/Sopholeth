package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"sopholeth/internal/gossip"
)

// testNode wraps a ClusterNode with its HTTP server for integration testing.
type testNode struct {
	node     *ClusterNode
	server   *http.Server
	listener net.Listener
	port     int
}

// newTestNode creates a ClusterNode with an HTTP server on a random port.
// The server handles /v1/gossip/message and /v1/bootstrap exactly as the
// production server does, so the full wire protocol is exercised.
func newTestNode(t *testing.T, nodeID, enclave string, replicationFactor int) *testNode {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	cn := NewClusterNode(nodeID, "127.0.0.1", port, port, replicationFactor, 0, 2*time.Second, "", enclave)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/gossip/message", makeGossipHandler(cn))
	mux.HandleFunc("/v1/bootstrap", makeBootstrapHandler(cn))

	srv := &http.Server{Handler: mux}
	return &testNode{node: cn, server: srv, listener: listener, port: port}
}

func (tn *testNode) start(t *testing.T, ctx context.Context, bootstrapAddrs []string) {
	t.Helper()
	go tn.server.Serve(tn.listener)
	if err := tn.node.Start(ctx, bootstrapAddrs); err != nil {
		t.Fatalf("failed to start node %s: %v", tn.node.localNode.ID, err)
	}
}

func (tn *testNode) stop() {
	tn.node.Stop()
	tn.server.Close()
}

func (tn *testNode) addr() string {
	return fmt.Sprintf("127.0.0.1:%d", tn.port)
}

// makeGossipHandler replicates the production gossip message handler.
func makeGossipHandler(cn *ClusterNode) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		var simpleMsg gossip.SimpleMessage
		if err := json.Unmarshal(body, &simpleMsg); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		msg := &gossip.Message{
			Type:      gossip.MessageType(simpleMsg.Type),
			From:      gossip.NodeID(simpleMsg.From),
			To:        gossip.NodeID(simpleMsg.To),
			Key:       simpleMsg.Key,
			Data:      simpleMsg.Data,
			TTL:       int(simpleMsg.TTL),
			Timestamp: time.Unix(simpleMsg.Timestamp, 0),
			MessageID: simpleMsg.MessageID,
		}

		if simpleMsg.NodeInfo != nil {
			enclave := simpleMsg.NodeInfo.Enclave
			if enclave == "" {
				enclave = "default"
			}
			msg.NodeInfo = &gossip.Node{
				ID:       gossip.NodeID(simpleMsg.NodeInfo.ID),
				Address:  simpleMsg.NodeInfo.Address,
				Port:     simpleMsg.NodeInfo.Port,
				HTTPPort: simpleMsg.NodeInfo.HTTPPort,
				Enclave:  enclave,
			}
		}

		if err := cn.HandleGossipMessage(msg); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
	}
}

// makeBootstrapHandler replicates the production bootstrap handler.
func makeBootstrapHandler(cn *ClusterNode) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req gossip.BootstrapRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		resp := cn.HandleBootstrap(&req)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// waitForPeers polls until the node has the expected number of peers or timeout.
func waitForPeers(t *testing.T, tn *testNode, expectedPeers int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(tn.node.Topology()) >= expectedPeers {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("node %s: expected %d peers, got %d after %v",
		tn.node.localNode.ID, expectedPeers, len(tn.node.Topology()), timeout)
}

// --- Tests ---

func TestBootstrapPeerDiscovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	node1 := newTestNode(t, "node1", "default", 3)
	node2 := newTestNode(t, "node2", "default", 3)
	defer node1.stop()
	defer node2.stop()

	// Start node1 as first node (no peers)
	node1.start(t, ctx, nil)

	// Start node2 with node1 as bootstrap peer
	node2.start(t, ctx, []string{node1.addr()})

	// node2 should discover node1 via bootstrap
	waitForPeers(t, node2, 1, 3*time.Second)

	// node1 should discover node2 via the bootstrap handshake
	waitForPeers(t, node1, 1, 3*time.Second)

	// Verify peer identities
	node1Peers := node1.node.Topology()
	if node1Peers[0].ID != "node2" {
		t.Fatalf("node1 expected peer 'node2', got '%s'", node1Peers[0].ID)
	}

	node2Peers := node2.node.Topology()
	if node2Peers[0].ID != "node1" {
		t.Fatalf("node2 expected peer 'node1', got '%s'", node2Peers[0].ID)
	}
}

func TestWriteReplicationAndQuorum(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	node1 := newTestNode(t, "node1", "default", 3)
	node2 := newTestNode(t, "node2", "default", 3)
	defer node1.stop()
	defer node2.stop()

	node1.start(t, ctx, nil)
	node2.start(t, ctx, []string{node1.addr()})
	waitForPeers(t, node1, 1, 3*time.Second)
	waitForPeers(t, node2, 1, 3*time.Second)

	// Write on node1 — should replicate to node2 and achieve quorum
	// With 2 nodes and replication=3: effective=2, quorum=2 (1 local + 1 ACK)
	err := node1.node.Put(ctx, "test-key", []byte("hello cluster"), 300*time.Second)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// node1 should have the data (wrote locally)
	data, exists := node1.node.Get("test-key")
	if !exists {
		t.Fatal("node1 should have test-key")
	}
	if string(data) != "hello cluster" {
		t.Fatalf("node1 data = %q, want %q", string(data), "hello cluster")
	}

	// node2 should have the data (replicated via gossip)
	// Small delay for async replication
	time.Sleep(200 * time.Millisecond)
	data, exists = node2.node.Get("test-key")
	if !exists {
		t.Fatal("node2 should have test-key (replicated)")
	}
	if string(data) != "hello cluster" {
		t.Fatalf("node2 data = %q, want %q", string(data), "hello cluster")
	}
}

func TestEnclaveIsolation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// node1 and node2 in enclave-a, node3 in enclave-b
	node1 := newTestNode(t, "node1", "enclave-a", 3)
	node2 := newTestNode(t, "node2", "enclave-a", 3)
	node3 := newTestNode(t, "node3", "enclave-b", 3)
	defer node1.stop()
	defer node2.stop()
	defer node3.stop()

	node1.start(t, ctx, nil)
	node2.start(t, ctx, []string{node1.addr()})
	node3.start(t, ctx, []string{node1.addr()})

	// All nodes should discover each other (topology crosses enclaves)
	waitForPeers(t, node1, 2, 3*time.Second)
	waitForPeers(t, node2, 2, 3*time.Second)
	waitForPeers(t, node3, 2, 3*time.Second)

	// Write on node1 (enclave-a) — should replicate to node2 (enclave-a) only
	err := node1.node.Put(ctx, "enclave-key", []byte("enclave-a data"), 300*time.Second)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	// node2 (same enclave) should have the data
	data, exists := node2.node.Get("enclave-key")
	if !exists {
		t.Fatal("node2 (enclave-a) should have enclave-key")
	}
	if string(data) != "enclave-a data" {
		t.Fatalf("node2 data = %q, want %q", string(data), "enclave-a data")
	}

	// node3 (different enclave) should NOT have the data
	_, exists = node3.node.Get("enclave-key")
	if exists {
		t.Fatal("node3 (enclave-b) should NOT have enclave-key — enclave isolation violated")
	}

	// Write on node3 (enclave-b) — should NOT replicate to enclave-a nodes
	err = node3.node.Put(ctx, "enclave-b-key", []byte("enclave-b data"), 300*time.Second)
	if err != nil {
		t.Fatalf("Put on node3 failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	_, exists = node1.node.Get("enclave-b-key")
	if exists {
		t.Fatal("node1 (enclave-a) should NOT have enclave-b-key")
	}
	_, exists = node2.node.Get("enclave-b-key")
	if exists {
		t.Fatal("node2 (enclave-a) should NOT have enclave-b-key")
	}
}

func TestQuorumTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	node1 := newTestNode(t, "node1", "default", 3)
	node2 := newTestNode(t, "node2", "default", 3)
	defer node1.stop()
	defer node2.stop()

	node1.start(t, ctx, nil)
	node2.start(t, ctx, []string{node1.addr()})
	waitForPeers(t, node1, 1, 3*time.Second)

	// Kill node2's HTTP server so ACKs can't come back
	node2.server.Close()

	// Write on node1 — should store locally but fail quorum (no ACK from node2)
	err := node1.node.Put(ctx, "timeout-key", []byte("will timeout"), 300*time.Second)
	if err != ErrQuorumTimeout {
		t.Fatalf("expected ErrQuorumTimeout, got: %v", err)
	}

	// Data should still be stored locally on node1
	data, exists := node1.node.Get("timeout-key")
	if !exists {
		t.Fatal("node1 should have timeout-key (stored locally)")
	}
	if string(data) != "will timeout" {
		t.Fatalf("node1 data = %q, want %q", string(data), "will timeout")
	}
}

func TestSingleNodeWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	node1 := newTestNode(t, "solo", "default", 3)
	defer node1.stop()

	node1.start(t, ctx, nil)

	// Single node, no peers: quorum=1 (just self), write completes immediately
	err := node1.node.Put(ctx, "solo-key", []byte("solo data"), 300*time.Second)
	if err != nil {
		t.Fatalf("single-node Put failed: %v", err)
	}

	data, exists := node1.node.Get("solo-key")
	if !exists {
		t.Fatal("solo node should have solo-key")
	}
	if string(data) != "solo data" {
		t.Fatalf("data = %q, want %q", string(data), "solo data")
	}
}

func TestThreeNodeBootstrapTopology(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	node1 := newTestNode(t, "node1", "default", 3)
	node2 := newTestNode(t, "node2", "default", 3)
	node3 := newTestNode(t, "node3", "default", 3)
	defer node1.stop()
	defer node2.stop()
	defer node3.stop()

	node1.start(t, ctx, nil)
	node2.start(t, ctx, []string{node1.addr()})
	// node3 bootstraps from node1 — should discover node2 via SYNC propagation
	node3.start(t, ctx, []string{node1.addr()})

	// Wait for full mesh: each node should see 2 peers
	waitForPeers(t, node1, 2, 5*time.Second)
	waitForPeers(t, node2, 2, 5*time.Second)
	waitForPeers(t, node3, 2, 5*time.Second)
}

func TestWriteReplicationToThreeNodes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	node1 := newTestNode(t, "node1", "default", 3)
	node2 := newTestNode(t, "node2", "default", 3)
	node3 := newTestNode(t, "node3", "default", 3)
	defer node1.stop()
	defer node2.stop()
	defer node3.stop()

	node1.start(t, ctx, nil)
	node2.start(t, ctx, []string{node1.addr()})
	node3.start(t, ctx, []string{node1.addr()})
	waitForPeers(t, node1, 2, 5*time.Second)
	waitForPeers(t, node2, 2, 5*time.Second)
	waitForPeers(t, node3, 2, 5*time.Second)

	// Write on node1 — replication factor 3 with 3 nodes: quorum=2
	err := node1.node.Put(ctx, "replicated", []byte("three-way"), 300*time.Second)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	time.Sleep(300 * time.Millisecond)

	// All three nodes should have the data
	for _, tn := range []*testNode{node1, node2, node3} {
		data, exists := tn.node.Get("replicated")
		if !exists {
			t.Fatalf("node %s should have 'replicated' key", tn.node.localNode.ID)
		}
		if string(data) != "three-way" {
			t.Fatalf("node %s data = %q, want %q", tn.node.localNode.ID, string(data), "three-way")
		}
	}
}

func TestSameKeyGossipUsesArrivalOrderNotTimestamp(t *testing.T) {
	node := NewClusterNode("receiver", "127.0.0.1", 0, 0, 1, 0, time.Second, "", "default")

	newer := &gossip.Message{
		Type:      gossip.MessageTypePut,
		From:      "node-newer",
		Key:       "collision",
		Data:      []byte("newer-by-timestamp"),
		TTL:       300,
		Timestamp: time.Now(),
		MessageID: "collision-newer",
	}
	older := &gossip.Message{
		Type:      gossip.MessageTypePut,
		From:      "node-older",
		Key:       "collision",
		Data:      []byte("older-by-timestamp"),
		TTL:       300,
		Timestamp: time.Now().Add(-time.Hour),
		MessageID: "collision-older",
	}

	if err := node.HandleGossipMessage(newer); err != nil {
		t.Fatalf("handle newer PUT: %v", err)
	}
	if err := node.HandleGossipMessage(older); err != nil {
		t.Fatalf("handle later-arriving older PUT: %v", err)
	}

	data, exists := node.Get("collision")
	if !exists {
		t.Fatal("collision key missing")
	}
	if got := string(data); got != "older-by-timestamp" {
		t.Fatalf("collision value = %q, want last-arriving value %q", got, "older-by-timestamp")
	}
}
