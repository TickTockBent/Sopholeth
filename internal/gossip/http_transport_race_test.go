package gossip

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Concurrent first sends on a freshly built transport must not race (#285).
// The dispatcher runs several workers per peer, so a node's first gossip
// sends overlap. Run with -race; the race detector is the assertion.
func TestHTTPTransportConcurrentFirstSendsDoNotRace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	serverPort, err := strconv.Atoi(serverURL.Port())
	if err != nil {
		t.Fatal(err)
	}
	peer := &Node{ID: "peer", Address: serverURL.Hostname(), HTTPPort: serverPort}

	transport := NewHTTPTransport(&Node{ID: "local"}, "")
	const concurrentSends = 16
	var sendGroup sync.WaitGroup
	sendErrors := make(chan error, concurrentSends)
	for sendIndex := 0; sendIndex < concurrentSends; sendIndex++ {
		sendGroup.Add(1)
		go func(sendIndex int) {
			defer sendGroup.Done()
			sendErrors <- transport.Send(context.Background(), peer, &Message{
				Type: MessageTypePing, From: "local", Timestamp: time.Now(),
				MessageID: "race-" + strconv.Itoa(sendIndex),
			})
		}(sendIndex)
	}
	sendGroup.Wait()
	close(sendErrors)
	for sendError := range sendErrors {
		if sendError != nil {
			t.Fatal(sendError)
		}
	}
}
