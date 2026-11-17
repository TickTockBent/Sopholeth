package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"sopholeth/internal/gossip"
)

// Regression tests for #212 (serial inline broadcast) and #167 (synchronous,
// recursive receive path). Fake peers join through /v1/bootstrap exactly as
// in the #212 reproduction.

// blackholePeer accepts TCP connections and never answers.
type blackholePeer struct {
	listener net.Listener
	mu       sync.Mutex
	conns    []net.Conn
}

func newBlackholePeer(t *testing.T) *blackholePeer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	peer := &blackholePeer{listener: listener}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			peer.mu.Lock()
			peer.conns = append(peer.conns, conn)
			peer.mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		peer.mu.Lock()
		defer peer.mu.Unlock()
		for _, conn := range peer.conns {
			conn.Close()
		}
	})
	return peer
}

func (b *blackholePeer) port() int { return b.listener.Addr().(*net.TCPAddr).Port }

// registerFakePeer adds a peer through the unauthenticated bootstrap path.
func registerFakePeer(t *testing.T, node *testNode, peerID string, port int) {
	t.Helper()
	response := node.node.HandleBootstrap(&gossip.BootstrapRequest{
		NodeID: peerID, Address: "127.0.0.1", GossipPort: port, HTTPPort: port,
	})
	if !response.Success {
		t.Fatalf("bootstrap registration of %s rejected", peerID)
	}
}

func waitForValue(t *testing.T, node *testNode, key, want string, timeout time.Duration) time.Duration {
	t.Helper()
	startedAt := time.Now()
	for time.Since(startedAt) < timeout {
		if data, exists := node.node.Get(key); exists && string(data) == want {
			return time.Since(startedAt)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("node %s did not receive %s=%q within %v", node.node.localNode.ID, key, want, timeout)
	return 0
}

// One and two unanswering peers: Put returns on quorum, not after N × 5s,
// every healthy peer receives every write regardless of peer-map order, and
// Stop returns promptly with sends to the dead peers still in flight.
func TestPutWithUnansweringPeers(t *testing.T) {
	for _, deadCount := range []int{1, 2} {
		t.Run(fmt.Sprintf("dead=%d", deadCount), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			nodeA := newTestNode(t, "nodeA", "default", 3)
			nodeB := newTestNode(t, "nodeB", "default", 3)
			nodeC := newTestNode(t, "nodeC", "default", 3)
			nodeAStopped := false
			defer func() {
				if !nodeAStopped {
					nodeA.stop()
				}
			}()
			defer nodeB.stop()
			defer nodeC.stop()
			nodeA.start(t, ctx, nil)
			nodeB.start(t, ctx, []string{nodeA.addr()})
			nodeC.start(t, ctx, []string{nodeA.addr()})
			waitForPeers(t, nodeA, 2, 3*time.Second)

			for i := 0; i < deadCount; i++ {
				registerFakePeer(t, nodeA, fmt.Sprintf("dead-%d", i), newBlackholePeer(t).port())
			}
			waitForPeers(t, nodeA, 2+deadCount, time.Second)

			// Several writes: Go map iteration varies, so the dead peers land
			// at different positions in the peer order across writes.
			for writeIndex := 0; writeIndex < 5; writeIndex++ {
				key := "dead-peer-key-" + strconv.Itoa(writeIndex)
				value := "value-" + strconv.Itoa(writeIndex)
				startedAt := time.Now()
				if err := nodeA.node.Put(ctx, key, []byte(value), 300*time.Second); err != nil {
					t.Fatalf("write %d: Put returned %v", writeIndex, err)
				}
				elapsed := time.Since(startedAt)
				t.Logf("write %d: Put returned in %v", writeIndex, elapsed)
				if elapsed > time.Second {
					t.Fatalf("write %d: Put took %v with %d unanswering peers", writeIndex, elapsed, deadCount)
				}
				waitForValue(t, nodeB, key, value, time.Second)
				waitForValue(t, nodeC, key, value, time.Second)
			}

			startedAt := time.Now()
			nodeAStopped = true
			nodeA.stop()
			if elapsed := time.Since(startedAt); elapsed > time.Second {
				t.Fatalf("Stop took %v with sends to dead peers in flight", elapsed)
			}
		})
	}
}

// A peer that answers after 3s does not delay delivery to a healthy peer
// or the client's answer.
func TestPutWithSlowPeer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	slowPeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer slowPeer.Close()

	nodeA := newTestNode(t, "nodeA", "default", 3)
	nodeB := newTestNode(t, "nodeB", "default", 3)
	defer nodeA.stop()
	defer nodeB.stop()
	nodeA.start(t, ctx, nil)
	nodeB.start(t, ctx, []string{nodeA.addr()})
	waitForPeers(t, nodeA, 1, 3*time.Second)
	registerFakePeer(t, nodeA, "slow", slowPeer.Listener.Addr().(*net.TCPAddr).Port)

	for writeIndex := 0; writeIndex < 3; writeIndex++ {
		key := "slow-peer-key-" + strconv.Itoa(writeIndex)
		startedAt := time.Now()
		if err := nodeA.node.Put(ctx, key, []byte("v"), 300*time.Second); err != nil {
			t.Fatalf("write %d: Put returned %v", writeIndex, err)
		}
		elapsed := time.Since(startedAt)
		t.Logf("write %d: Put returned in %v", writeIndex, elapsed)
		if elapsed > time.Second {
			t.Fatalf("write %d: Put took %v behind a slow peer", writeIndex, elapsed)
		}
		if latency := waitForValue(t, nodeB, key, "v", time.Second); latency > 500*time.Millisecond {
			t.Fatalf("write %d: healthy peer received it after %v", writeIndex, latency)
		}
	}
}

// Receive side above the fanout threshold: the inbound gossip handler stores
// the value and returns while every forward target is unanswering.
func TestInboundPutReturnsPromptlyAboveFanoutThreshold(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	receiver := newTestNode(t, "receiver", "default", 3)
	defer receiver.stop()
	receiver.start(t, ctx, nil)

	deadPeerCount := gossip.FanoutThreshold + 2
	for i := 0; i < deadPeerCount; i++ {
		registerFakePeer(t, receiver, fmt.Sprintf("dead-%d", i), newBlackholePeer(t).port())
	}
	waitForPeers(t, receiver, deadPeerCount, time.Second)

	for writeIndex := 0; writeIndex < 3; writeIndex++ {
		key := "inbound-key-" + strconv.Itoa(writeIndex)
		body, err := json.Marshal(gossip.SimpleMessage{
			Type:      string(gossip.MessageTypePut),
			From:      "dead-0",
			Key:       key,
			Data:      []byte("forwarded"),
			TTL:       300,
			Timestamp: time.Now().Unix(),
			MessageID: "inbound-" + strconv.Itoa(writeIndex),
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		startedAt := time.Now()
		response, err := http.Post("http://"+receiver.addr()+"/v1/gossip/message", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST gossip: %v", err)
		}
		response.Body.Close()
		elapsed := time.Since(startedAt)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("gossip handler status = %d", response.StatusCode)
		}
		t.Logf("write %d: inbound PUT answered in %v", writeIndex, elapsed)
		if elapsed > 500*time.Millisecond {
			t.Fatalf("inbound PUT took %v with unanswering forward targets", elapsed)
		}
		if data, exists := receiver.node.Get(key); !exists || string(data) != "forwarded" {
			t.Fatalf("receiver did not store %s before answering", key)
		}
	}
}

// #215: a quorum of one (RF=1) confirms locally but still replicates to the
// reachable enclave peers.
func TestQuorumOfOneWriteStillReplicates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeA := newTestNode(t, "nodeA", "default", 1)
	nodeB := newTestNode(t, "nodeB", "default", 1)
	defer nodeA.stop()
	defer nodeB.stop()
	nodeA.start(t, ctx, nil)
	nodeB.start(t, ctx, []string{nodeA.addr()})
	waitForPeers(t, nodeA, 1, 3*time.Second)

	if err := nodeA.node.Put(ctx, "rf1-key", []byte("replicated"), 300*time.Second); err != nil {
		t.Fatalf("Put: %v", err)
	}
	waitForValue(t, nodeB, "rf1-key", "replicated", 2*time.Second)
}
