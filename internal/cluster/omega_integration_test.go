package cluster

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"sopholeth/internal/gossip"
	"sopholeth/internal/trust"
)

// This file is the omega-bootstrap end-to-end integration test (#90, F10).
//
// Why a dedicated integration test exists at all:
//
// Every preflight finding F1-F11 was caught by humans running the burn-in,
// not by tests. The unit tests in internal/trust/ cover signed-list parsing
// and resolver-mock paths in isolation, but nothing exercised the *full*
// flow: DNS resolution → list verification → root self-recognition →
// cluster bootstrap → 403-gate on non-roots → symmetric peer convergence.
// F1/F2 (port mismatch) was the canonical example — would have been caught
// the moment a node tried to actually join via a signed list with the
// wrong port semantics.
//
// These tests reuse the testNode harness from integration_test.go but
// register a different /v1/bootstrap handler that mirrors the production
// IsRoot() gate. That gate is what's missing from the existing harness
// (which is shared with non-omega tests where roots aren't a concept).
//
// Lifecycle: each test generates a fresh ed25519 keypair, signs a list
// pointing at the test cluster's actual addresses, configures a
// stubResolver, and walks the production resolution path via
// trust.FetchSigned.

// stubTXTResolver is an in-memory TXTResolver matching the shape used in
// internal/trust/resolver_test.go. Keyed by hostname.
type stubTXTResolver struct {
	records map[string][]string
}

func (s *stubTXTResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	if recs, ok := s.records[name]; ok {
		return recs, nil
	}
	return nil, fmt.Errorf("no TXT record for %s", name)
}

// signTestList builds a signed root list with the given keypair, expiry,
// and node addresses, returning both the SignedList and the canonical
// TXT-record string a DNS resolver would return.
func signTestList(t *testing.T, priv ed25519.PrivateKey, expires int64, nodes []string) (*trust.SignedList, string) {
	t.Helper()
	list := &trust.SignedList{
		Version: trust.OmegaVersion,
		Expires: expires,
		Nodes:   nodes,
	}
	list.Signature = list.Sign(priv)
	return list, list.Encode()
}

// makeOmegaBootstrapHandler mirrors the production /v1/bootstrap handler
// in cmd/server/main.go: returns 403 when the node is not a root, otherwise
// delegates to ClusterNode.HandleBootstrap. The harness in
// integration_test.go skips the gate (it's used by tests where IsRoot is
// not a concept), so this dedicated handler exists for the omega tests.
//
// Intentional difference from production: the real handler gates on
// `network == "public" && !IsRoot()`, allowing private-network nodes
// to answer bootstrap unconditionally. Every node in these tests is
// in omega-resolution mode (effectively public), so the network guard
// is unnecessary; collapsing it to `!IsRoot()` keeps the test handler
// minimal. A future omega-test that exercises the private-network path
// should reinstate the network guard.
func makeOmegaBootstrapHandler(cn *ClusterNode) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cn.IsRoot() {
			http.Error(w, `{"error":"not a bootstrap root"}`, http.StatusForbidden)
			return
		}
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

// newOmegaTestNode wraps newTestNode but installs the omega-aware
// bootstrap handler (with 403 gate) instead of the test default.
type omegaTestNode struct {
	*testNode
}

func newOmegaTestNode(t *testing.T, nodeID string, replicationFactor int) *omegaTestNode {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	cn := NewClusterNode(nodeID, "127.0.0.1", port, port, replicationFactor, 0, 2*time.Second, "", "default")

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/gossip/message", makeGossipHandler(cn))
	mux.HandleFunc("/v1/bootstrap", makeOmegaBootstrapHandler(cn))

	srv := &http.Server{Handler: mux}
	tn := &testNode{node: cn, server: srv, listener: listener, port: port}
	return &omegaTestNode{testNode: tn}
}

// resolveAndApply walks the production omega-bootstrap path: fetch+verify
// a signed list via the injected resolver, then mark the local node as a
// root iff its advertised address appears in the list. Returns the seed
// list extracted from the verified record.
func (otn *omegaTestNode) resolveAndApply(t *testing.T, ctx context.Context, resolver trust.TXTResolver, pub ed25519.PublicKey) []string {
	t.Helper()
	cfg := trust.DNSConfig{
		Resolver:      resolver,
		BootstrapName: "_bootstrap.test.invalid",
	}
	list, err := trust.FetchSigned(ctx, cfg, pub, time.Now())
	if err != nil {
		t.Fatalf("[%s] FetchSigned failed: %v", otn.node.localNode.ID, err)
	}
	selfAdvertised := otn.addr()
	for _, n := range list.Nodes {
		if n == selfAdvertised {
			otn.node.SetRoot(true)
			return list.Nodes
		}
	}
	otn.node.SetRoot(false)
	return list.Nodes
}

// buildResolver returns a stubTXTResolver wired with the bootstrap
// indirection record + the actual signed list at the target name.
func buildResolver(bootstrapName, omegaTarget, signedListRecord string) *stubTXTResolver {
	return &stubTXTResolver{
		records: map[string][]string{
			bootstrapName: {fmt.Sprintf("omega=%s", omegaTarget)},
			omegaTarget:   {signedListRecord},
		},
	}
}

// TestOmegaBootstrap_RootsConvergeAndNonRootJoins is the load-bearing
// integration test for #90. Three nodes:
//
//   - root-a, root-b: addresses appear in the signed list, become roots
//   - leaf-c:         not in the list, joins by bootstrapping from roots
//
// Asserts the production flow:
//  1. FetchSigned resolves and verifies the signed list via DNS stub.
//  2. Self-recognition correctly marks root-a/root-b as roots and leaf-c
//     as a non-root.
//  3. Each node's Bootstrap call against the seed list (= the signed
//     list's Nodes) succeeds.
//  4. After convergence, every node has the other two as peers.
//
// What this test catches and what it doesn't, re F1/F2:
//
//   - It catches spec-level regressions (signed-list addresses pointing
//     at the wrong endpoint shape — e.g. host:gossip-port when the
//     binary expects host:http-port). The whole composition would fail
//     to converge.
//   - It does NOT catch a code-level port-variable swap in main.go,
//     because the testNode harness uses the same ephemeral port for
//     both gossipPort and httpPort. If someone changed `selfAdvertised`
//     in main.go from `httpPort` back to `gossipPort`, this test would
//     still pass because the two are numerically equal in the harness.
//     A separate test that constructs nodes with distinct gossip/http
//     ports would be required to lock that down.
//
// Even with that caveat, this test exercises composition no prior test
// did — that's why the burn-in F-findings went undetected until live
// startup.
func TestOmegaBootstrap_RootsConvergeAndNonRootJoins(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}

	a := newOmegaTestNode(t, "root-a", 3)
	b := newOmegaTestNode(t, "root-b", 3)
	c := newOmegaTestNode(t, "leaf-c", 3)
	defer a.stop()
	defer b.stop()
	defer c.stop()

	go a.server.Serve(a.listener)
	go b.server.Serve(b.listener)
	go c.server.Serve(c.listener)

	// The signed list contains only roots a and b. leaf-c will discover
	// it's not a root and bootstrap from the roots without becoming one.
	roots := []string{a.addr(), b.addr()}
	_, signedRecord := signTestList(t, priv, time.Now().Add(time.Hour).Unix(), roots)

	bootstrapName := "_bootstrap.test.invalid"
	omegaTarget := "_omega.test.invalid"
	resolver := buildResolver(bootstrapName, omegaTarget, signedRecord)

	// Apply root status to every node before any node calls Start. In
	// production all roots come up before bootstrap traffic flows; doing
	// the resolve+SetRoot phase first here matches that ordering rather
	// than letting the first starter race against a peer that hasn't yet
	// flipped IsRoot=true.
	seedsByNode := make([][]string, 0, 3)
	for _, otn := range []*omegaTestNode{a, b, c} {
		seedsByNode = append(seedsByNode, otn.resolveAndApply(t, ctx, resolver, pub))
	}
	for i, otn := range []*omegaTestNode{a, b, c} {
		if err := otn.node.Start(ctx, seedsByNode[i]); err != nil {
			t.Fatalf("[%s] Start failed: %v", otn.node.localNode.ID, err)
		}
	}

	if !a.node.IsRoot() {
		t.Errorf("expected root-a to be a root")
	}
	if !b.node.IsRoot() {
		t.Errorf("expected root-b to be a root")
	}
	if c.node.IsRoot() {
		t.Errorf("expected leaf-c to NOT be a root")
	}

	waitForPeers(t, a.testNode, 2, 3*time.Second)
	waitForPeers(t, b.testNode, 2, 3*time.Second)
	waitForPeers(t, c.testNode, 2, 3*time.Second)
}

// TestOmegaBootstrap_NonRootRejectsWith403 verifies the gate that protects
// non-roots from answering bootstrap requests. A node that's not in the
// signed list must return 403 even when its /v1/bootstrap endpoint is
// reachable. Without this gate, an attacker could spin up a rogue node,
// have it advertise itself, and have it harvest peer topology from
// callers that mistakenly used it as a seed.
func TestOmegaBootstrap_NonRootRejectsWith403(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}

	leaf := newOmegaTestNode(t, "leaf", 3)
	defer leaf.stop()
	go leaf.server.Serve(leaf.listener)

	// Signed list contains only some other address, not leaf.
	_, signedRecord := signTestList(t, priv, time.Now().Add(time.Hour).Unix(), []string{"127.0.0.1:65535"})
	resolver := buildResolver("_bootstrap.test.invalid", "_omega.test.invalid", signedRecord)

	leaf.resolveAndApply(t, ctx, resolver, pub)
	if leaf.node.IsRoot() {
		t.Fatal("leaf should not be a root")
	}
	if err := leaf.node.Start(ctx, nil); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	body := []byte(`{"node_id":"intruder","address":"1.2.3.4","gossip_port":9090,"http_port":8080}`)
	resp, err := http.Post(fmt.Sprintf("http://%s/v1/bootstrap", leaf.addr()),
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/bootstrap failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 from non-root /v1/bootstrap, got %d", resp.StatusCode)
	}
}

// TestOmegaBootstrap_TamperedListRejected verifies the binary's trust
// anchor catches signature mismatches. If an attacker swaps the published
// signed list with one signed by a different key, FetchSigned must fail
// and the node must refuse to start with that list.
func TestOmegaBootstrap_TamperedListRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// The "real" omega keypair (what the binary trusts).
	realPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("real keypair: %v", err)
	}
	// The attacker's keypair (what the tampered list is signed with).
	_, attackerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("attacker keypair: %v", err)
	}

	_, tamperedRecord := signTestList(t, attackerPriv, time.Now().Add(time.Hour).Unix(), []string{"127.0.0.1:9999"})
	resolver := buildResolver("_bootstrap.test.invalid", "_omega.test.invalid", tamperedRecord)

	cfg := trust.DNSConfig{Resolver: resolver, BootstrapName: "_bootstrap.test.invalid"}
	_, err = trust.FetchSigned(ctx, cfg, realPub, time.Now())
	if err == nil {
		t.Fatal("FetchSigned accepted a list signed by a different key; trust anchor is broken")
	}
	if !strings.Contains(err.Error(), "verify") && !strings.Contains(err.Error(), "signature") {
		t.Fatalf("error should reference verification/signature, got: %v", err)
	}
}

// TestOmegaBootstrap_ExpiredListRejected verifies the binary refuses to
// trust a signed list whose expiration has passed, even with a valid
// signature. Without this, a compromised-and-rotated key could remain
// trusted indefinitely via stale records.
func TestOmegaBootstrap_ExpiredListRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}

	// Expired one second ago, signature is otherwise valid.
	_, expiredRecord := signTestList(t, priv, time.Now().Add(-time.Second).Unix(), []string{"127.0.0.1:9999"})
	resolver := buildResolver("_bootstrap.test.invalid", "_omega.test.invalid", expiredRecord)

	cfg := trust.DNSConfig{Resolver: resolver, BootstrapName: "_bootstrap.test.invalid"}
	_, err = trust.FetchSigned(ctx, cfg, pub, time.Now())
	if err == nil {
		t.Fatal("FetchSigned accepted an expired list; freshness gate is broken")
	}
}
