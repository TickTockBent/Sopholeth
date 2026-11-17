package gossip

import (
	"context"
	"regexp"
	"sync"
	"testing"
)

// Message IDs are opaque random tokens (#166): 32 lowercase hex characters.
var messageIDFormat = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestNewMessageIDFormat(t *testing.T) {
	if MessageIDLength != 32 {
		t.Fatalf("MessageIDLength = %d, want 32", MessageIDLength)
	}
	for i := 0; i < 1000; i++ {
		messageID := NewMessageID()
		if !messageIDFormat.MatchString(messageID) {
			t.Fatalf("NewMessageID() = %q, want 32 lowercase hex characters", messageID)
		}
	}
}

// IDs drawn concurrently, many within one clock tick, never repeat.
func TestNewMessageIDsAreDistinct(t *testing.T) {
	const goroutineCount, idsPerGoroutine = 8, 2000
	generated := make([][]string, goroutineCount)
	var workers sync.WaitGroup
	for worker := range generated {
		workers.Add(1)
		go func() {
			defer workers.Done()
			batch := make([]string, idsPerGoroutine)
			for i := range batch {
				batch[i] = NewMessageID()
			}
			generated[worker] = batch
		}()
	}
	workers.Wait()

	seenIDs := make(map[string]bool, goroutineCount*idsPerGoroutine)
	for _, batch := range generated {
		for _, messageID := range batch {
			if seenIDs[messageID] {
				t.Fatalf("NewMessageID repeated %q", messageID)
			}
			seenIDs[messageID] = true
		}
	}
}

// protocolMessageIDs has p originate PINGs and a SYNC peer list, the
// messages that used generateMessageID before #166, and returns their IDs.
func protocolMessageIDs(t *testing.T, p *Protocol, transport *mockTransport) []string {
	t.Helper()
	for _, peerID := range []NodeID{"peer-a", "peer-b", "peer-c"} {
		p.addPeer(&Node{ID: peerID, Address: string(peerID), Port: 9090, HTTPPort: 8080, Enclave: "default"})
	}
	p.pingPeers(context.Background())
	p.respondWithPeerList("peer-a")

	sent := transport.getSentMessages()
	if len(sent) == 0 {
		t.Fatal("protocol sent no messages")
	}
	messageIDs := make([]string, 0, len(sent))
	for _, sentMessage := range sent {
		messageIDs = append(messageIDs, sentMessage.Msg.MessageID)
	}
	return messageIDs
}

// Protocol-originated messages carry opaque IDs, and a rebuilt protocol (as
// after a restart) does not reissue any earlier ID. The old generator was
// UnixNano plus a process-local counter that restarted at zero.
func TestProtocolMessageIDsAreOpaqueAndSurviveRebuild(t *testing.T) {
	seenIDs := make(map[string]bool)
	for generation := 0; generation < 2; generation++ {
		p, transport := newTestProtocol()
		for _, messageID := range protocolMessageIDs(t, p, transport) {
			if !messageIDFormat.MatchString(messageID) {
				t.Fatalf("generation %d: protocol message ID %q is not 32 lowercase hex characters", generation, messageID)
			}
			if seenIDs[messageID] {
				t.Fatalf("generation %d: protocol reissued message ID %q", generation, messageID)
			}
			seenIDs[messageID] = true
		}
	}
}
