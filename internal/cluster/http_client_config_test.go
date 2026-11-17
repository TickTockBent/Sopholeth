package cluster

import (
	"crypto/tls"
	"net/http"
	"testing"
	"time"
)

// ConfigureBootstrap rejects a client that disables verification when it is
// called, before Start, and leaves the node's configuration unchanged (#285).
func TestConfigureBootstrapRejectsInsecureClient(t *testing.T) {
	clusterNode := NewClusterNode("node", "127.0.0.1", 1, 1, 3, 0, time.Second, "", "default")
	insecureClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	if err := clusterNode.ConfigureBootstrap(insecureClient, nil); err == nil {
		t.Fatal("accepted an InsecureSkipVerify client")
	}
	if clusterNode.httpClient != nil {
		t.Fatal("a rejected client was stored for gossip")
	}
	if err := clusterNode.ConfigureBootstrap(nil, nil); err != nil {
		t.Fatalf("default client rejected: %v", err)
	}
}
