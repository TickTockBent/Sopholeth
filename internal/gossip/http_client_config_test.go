package gossip

import (
	"crypto/tls"
	"net/http"
	"testing"
	"time"
)

// Verification moved to configuration time (#285): an unsafe client is
// rejected when it is installed, not on the first send.

func insecureClient() *http.Client {
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
}

func TestNewHTTPTransportBuildsVerifiedPrivateClient(t *testing.T) {
	transport := NewHTTPTransport(&Node{ID: "local"}, "")
	if transport.client.Timeout != defaultGossipTimeout {
		t.Fatalf("timeout = %v, want %v", transport.client.Timeout, defaultGossipTimeout)
	}
	if transport.client.Transport == nil || transport.client.Transport == http.DefaultTransport {
		t.Fatal("gossip client must use a private transport, not http.DefaultTransport")
	}
	if transport.client.CheckRedirect == nil || transport.client.Jar != nil {
		t.Fatal("gossip client must refuse redirects and carry no cookie jar")
	}
}

func TestSetHTTPClientRejectsUnsafeClient(t *testing.T) {
	transport := NewHTTPTransport(&Node{ID: "local"}, "")
	previousClient := transport.client
	if err := transport.SetHTTPClient(insecureClient()); err == nil {
		t.Fatal("accepted an InsecureSkipVerify client")
	}
	if transport.client != previousClient {
		t.Fatal("a rejected client replaced the verified one")
	}
	if err := transport.SetHTTPClient(nil); err != nil || transport.client != previousClient {
		t.Fatalf("nil client: err=%v, changed=%v", err, transport.client != previousClient)
	}
}

func TestSetHTTPClientKeepsTimeoutAndDefaultsIt(t *testing.T) {
	transport := NewHTTPTransport(&Node{ID: "local"}, "")
	if err := transport.SetHTTPClient(&http.Client{Timeout: 9 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if transport.client.Timeout != 9*time.Second {
		t.Fatalf("timeout = %v, want the caller's 9s", transport.client.Timeout)
	}
	if transport.client.Transport == nil || transport.client.Transport == http.DefaultTransport {
		t.Fatal("caller client without a transport must get a private one")
	}
	if err := transport.SetHTTPClient(&http.Client{}); err != nil {
		t.Fatal(err)
	}
	if transport.client.Timeout != defaultGossipTimeout {
		t.Fatalf("timeout = %v, want default %v", transport.client.Timeout, defaultGossipTimeout)
	}
}

func TestConfigureBootstrapRejectsUnsafeClient(t *testing.T) {
	protocol, _ := newTestProtocol()
	previousClient := protocol.bootstrapClient
	if previousClient == nil || previousClient.Transport == nil || previousClient.Transport == http.DefaultTransport {
		t.Fatal("NewProtocol must build a verified bootstrap client with a private transport")
	}
	if err := protocol.ConfigureBootstrap(insecureClient(), func(string, []*Node) error { return nil }); err == nil {
		t.Fatal("accepted an InsecureSkipVerify bootstrap client")
	}
	if protocol.bootstrapClient != previousClient || protocol.checkSeed != nil {
		t.Fatal("a rejected configuration changed the bootstrap client or seed check")
	}
	if err := protocol.ConfigureBootstrap(&http.Client{Timeout: time.Minute}, nil); err != nil {
		t.Fatal(err)
	}
	if protocol.bootstrapClient.Timeout != bootstrapTimeout {
		t.Fatalf("bootstrap timeout = %v, want %v", protocol.bootstrapClient.Timeout, bootstrapTimeout)
	}
}
