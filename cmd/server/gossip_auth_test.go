package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"sopholeth/internal/gossip"
)

// Exercise both HTTP clients against the actual authenticated server routes.
func TestAuthenticatedHTTPClients(t *testing.T) {
	const secret = "test-cluster-secret"
	for _, client := range []string{"gossip", "bootstrap"} {
		t.Run(client, func(t *testing.T) {
			server, cleanup := newTestServerWithSecret(t, secret)
			defer cleanup()
			router := server.Router()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && r.Header.Get("X-Gossip-Signature") == "" {
					t.Error("client omitted X-Gossip-Signature")
				}
				router.ServeHTTP(w, r)
			}))
			defer srv.Close()
			seed := strings.TrimPrefix(srv.URL, "http://")
			host, portText, err := net.SplitHostPort(seed)
			if err != nil {
				t.Fatal(err)
			}
			port, err := strconv.Atoi(portText)
			if err != nil {
				t.Fatal(err)
			}
			local := &gossip.Node{ID: "joiner", Address: "127.0.0.1", Enclave: "default"}
			if client == "bootstrap" {
				protocol := gossip.NewProtocol(local, 1, secret)
				if err := protocol.Bootstrap(context.Background(), []string{seed}); err != nil {
					t.Fatal(err)
				}
				peers := protocol.GetPeers()
				if len(peers) != 1 || peers[0].ID != "test-node" {
					t.Fatalf("authenticated bootstrap did not discover seed: %v", peers)
				}
				return
			}

			transport := gossip.NewHTTPTransport(local, secret)
			err = transport.Send(context.Background(), &gossip.Node{ID: "test-node", Address: host, HTTPPort: port}, &gossip.Message{
				Type: gossip.MessageTypePut, From: local.ID, Key: "signed-key",
				Data: []byte("signed-value"), TTL: 300, Timestamp: time.Now(), MessageID: "signed-put",
			})
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.Get(srv.URL + "/v1/data/signed-key")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil || resp.StatusCode != http.StatusOK || string(body) != "signed-value" {
				t.Fatalf("replicated value: status=%d body=%q err=%v", resp.StatusCode, body, err)
			}
		})
	}
}

func TestHTTPGossipRejectsInvalidSignatures(t *testing.T) {
	const secret = "test-cluster-secret"
	server, cleanup := newTestServerWithSecret(t, secret)
	defer cleanup()
	router := server.Router()
	for _, path := range []string{"/v1/gossip/message", "/v1/bootstrap"} {
		for _, tc := range []struct {
			name, signature string
		}{
			{"missing", ""},
			{"wrong secret", gossip.SignBody("wrong-secret", []byte(`{}`))},
			{"tampered body", gossip.SignBody(secret, []byte(`{"node_id":"other"}`))},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
				req.Header.Set("X-Gossip-Signature", tc.signature)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				if w.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want 403: %s", w.Code, w.Body.String())
				}
			})
		}
	}
}
