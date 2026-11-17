package ws

import (
	"testing"
	"time"

	"sopholeth/internal/gossip"
)

// The WS envelope carries the originator's opaque message ID unchanged, for
// the PUT and for its ACK, both in the wire conversion and over a real
// connection (#166).
func TestEnvelopePreservesOpaqueMessageID(t *testing.T) {
	server, client, cleanup := newPair(t, "test-secret")
	defer cleanup()

	received := make(chan *gossip.Message, 2)
	server.OnMessage(func(m *gossip.Message) { received <- m })

	for _, messageType := range []gossip.MessageType{gossip.MessageTypePut, gossip.MessageTypeAck} {
		messageID := gossip.NewMessageID()
		sent := sampleMessage(func(m *gossip.Message) {
			m.Type = messageType
			m.MessageID = messageID
		})

		if roundTripped := wireToMessage(messageToWire(sent)); roundTripped.MessageID != messageID {
			t.Fatalf("%s wire conversion: message_id %q, want %q", messageType, roundTripped.MessageID, messageID)
		}

		if err := client.SendGossip(sent); err != nil {
			t.Fatalf("%s send: %v", messageType, err)
		}
		got := waitOnMessage(t, received, 2*time.Second)
		if got.Type != messageType || got.MessageID != messageID {
			t.Fatalf("received %s with message_id %q, want %s with %q", got.Type, got.MessageID, messageType, messageID)
		}
	}
}
