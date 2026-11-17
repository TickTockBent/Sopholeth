package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sopholeth/internal/cluster"
	"sopholeth/internal/discovery"
	"sopholeth/internal/gossip"
	"sopholeth/internal/testomega"
	"sopholeth/internal/trust/bootstrap"
)

// This replaces the legacy DNS integration test with the actual HTTPS/TUF
// consumer path. Two roots and an ordinary node check joining and routing;
// three-host operation belongs to the deployment test.
func TestHTTPSDiscoveryNodeJoining(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	newNode := func(id string) (*cluster.ClusterNode, *httptest.Server) {
		cn := cluster.NewClusterNode(id, "127.0.0.1", 9090, 8080, 3, 0, 2*time.Second, "", "default")
		h := &HTTPServer{clusterNode: cn, nodeID: id, network: "public"}
		mux := http.NewServeMux()
		mux.HandleFunc("/v1/bootstrap", h.bootstrapHandler)
		mux.HandleFunc("/v1/gossip/message", h.gossipHandler)
		srv := httptest.NewTLSServer(mux)
		t.Cleanup(srv.Close)
		cn.SetHTTPOrigin(srv.URL) // Advertised TLS port differs from both local fields.
		return cn, srv
	}
	root, rootHTTP := newNode("root-one")
	second, secondHTTP := newNode("root-two")
	leaf, leafHTTP := newNode("ordinary-node")
	roots := []bootstrap.Root{{ID: "root-one", Origin: rootHTTP.URL}, {ID: "root-two", Origin: secondHTTP.URL}, {ID: "three", Origin: "https://127.0.0.1:2"}}
	repo := testomega.New(t, roots)
	open := func(cn *cluster.ClusterNode, id, origin string) *discovery.Session {
		s, err := discovery.Open(ctx, repo.Bundle, filepath.Join(t.TempDir(), "trust"), repo.Server.Client())
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		if err := configurePublicDiscovery(cn, s, id, origin, "default"); err != nil {
			t.Fatal(err)
		}
		if err := cn.ConfigureBootstrap(repo.Server.Client(), publicSeedCheck(s)); err != nil {
			t.Fatal(err)
		}
		return s
	}
	rootDiscovery := open(root, "root-one", rootHTTP.URL)
	leafDiscovery := open(leaf, "ordinary-node", leafHTTP.URL)
	open(second, "root-two", secondHTTP.URL)
	if !root.IsRoot() || leaf.IsRoot() {
		t.Fatal("official root role did not match signed ID and origin")
	}
	if err := root.Start(ctx, nil); err != nil {
		t.Fatal(err)
	}
	defer root.Stop()
	if err := leaf.Start(ctx, leafDiscovery.Seeds()[:1]); err != nil {
		t.Fatal(err)
	}
	defer leaf.Stop()
	if len(leaf.Topology()) != 1 || len(root.Topology()) != 1 {
		t.Fatalf("join failed: %v / %v", leaf.Topology(), root.Topology())
	}
	if leaf.Topology()[0].HTTPOrigin != rootHTTP.URL || root.Topology()[0].HTTPOrigin != leafHTTP.URL {
		t.Fatal("lost advertised HTTPS origins")
	}
	if err := root.Put(ctx, "test", []byte("anonymous"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if data, ok := leaf.Get("test"); !ok || string(data) != "anonymous" {
		t.Fatalf("HTTPS gossip did not replicate: %q", data)
	}
	if err := second.Start(ctx, []string{rootHTTP.URL}); err != nil {
		t.Fatal(err)
	}
	defer second.Stop()
	// An unsigned request must not move another root, an established ordinary
	// peer, or a listed root we have not learned as a peer yet.
	for _, id := range []string{"root-two", "ordinary-node", "three"} {
		resp, err := rootHTTP.Client().Post(rootHTTP.URL+"/v1/bootstrap", "application/json", strings.NewReader(`{"node_id":"`+id+`","http_origin":"http://attacker.invalid:9"}`))
		if err != nil {
			t.Fatal(err)
		}
		var result gossip.BootstrapResponse
		err = json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if err != nil || result.Success {
			t.Fatalf("accepted bootstrap route substitution for %s: %+v, %v", id, result, err)
		}
	}
	if len(root.Topology()) != 2 {
		t.Fatalf("route substitution changed topology: %+v", root.Topology())
	}
	for _, peer := range root.Topology() {
		want := leafHTTP.URL
		if peer.ID == "root-two" {
			want = secondHTTP.URL
		}
		if peer.HTTPOrigin != want || peer.Enclave != "default" {
			t.Fatalf("route substitution changed peer: %+v", peer)
		}
	}
	resp, err := leafHTTP.Client().Post(leafHTTP.URL+"/v1/bootstrap", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("ordinary node claimed root role: %d", resp.StatusCode)
	}
	check := publicSeedCheck(rootDiscovery)
	if err := check(rootHTTP.URL, []*gossip.Node{{ID: "root-one", HTTPOrigin: "http://substitute.invalid", Enclave: "default"}}); err == nil {
		t.Fatal("accepted substituted/downgraded root advertisement")
	}
	if err := check(rootHTTP.URL, []*gossip.Node{{ID: "wrong-root", HTTPOrigin: rootHTTP.URL, Enclave: "default"}}); err == nil {
		t.Fatal("accepted mismatched root ID")
	}
	// A signed directory update, unlike a peer advertisement, may move a root.
	roots[1].Origin = "https://replacement.example"
	repo.Publish(t, roots)
	if err := rootDiscovery.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	for _, peer := range root.Topology() {
		if peer.ID == "root-two" && peer.HTTPOrigin != roots[1].Origin {
			t.Fatalf("verified update did not move root: %+v", peer)
		}
		if peer.ID == "ordinary-node" && peer.HTTPOrigin != leafHTTP.URL {
			t.Fatalf("verified update moved ordinary peer: %+v", peer)
		}
	}
	if root.HandleBootstrap(&gossip.BootstrapRequest{NodeID: "root-two", HTTPOrigin: secondHTTP.URL}).Success {
		t.Fatal("stale unsigned advertisement undid verified root move")
	}
}
