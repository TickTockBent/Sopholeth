package tree

import (
	"testing"
	"time"

	"sopholeth/internal/gossip"
	"sopholeth/internal/transport/ws"
)

// Reverse ACK routing keys on the transient's own message ID: the substrate
// records the route under the ID of the PUT it received over WS, and an ACK
// carrying that ID unchanged comes back down the same pipe (#166).
func TestReverseAckRoutingPreservesOpaqueMessageID(t *testing.T) {
	pair := newPair(t)
	defer pair.cleanupFn()
	substrateConn := pair.serverConn(t)

	manager := NewManager(makeNode("substrate-1"), &fakePeerer{}, Options{Inbound: InboundTrue, MaxChildren: DefaultMaxChildren})
	defer manager.Stop()
	manager.HandleHello(substrateConn, &ws.HelloPayload{NodeID: "transient-1", Enclave: "default"})

	// As cmd/server does for a PUT arriving from an attached transient.
	routed := make(chan string, 1)
	substrateConn.OnMessage(func(message *gossip.Message) {
		if message.Type == gossip.MessageTypePut {
			manager.RecordAckRoute(message.MessageID, substrateConn, 5*time.Second)
			routed <- message.MessageID
		}
	})
	ackReceived := make(chan *gossip.Message, 1)
	pair.clientWS.OnMessage(func(message *gossip.Message) {
		if message.Type == gossip.MessageTypeAck {
			ackReceived <- message
		}
	})

	originalID := gossip.NewMessageID()
	if err := pair.clientWS.SendGossip(&gossip.Message{
		Type:      gossip.MessageTypePut,
		From:      "transient-1",
		Key:       "transient-key",
		Data:      []byte("value"),
		TTL:       300,
		Timestamp: time.Now(),
		MessageID: originalID,
	}); err != nil {
		t.Fatalf("send PUT: %v", err)
	}

	select {
	case recordedID := <-routed:
		if recordedID != originalID {
			t.Fatalf("substrate recorded the route under %q, want %q", recordedID, originalID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("substrate never received the PUT")
	}

	// A replica's ACK for the relayed PUT, addressed to the transient.
	if !manager.RouteAck(&gossip.Message{
		Type:      gossip.MessageTypeAck,
		From:      "replica-1",
		To:        "transient-1",
		Key:       "transient-key",
		Timestamp: time.Now(),
		MessageID: originalID,
	}) {
		t.Fatal("RouteAck found no route for the PUT's message ID")
	}
	select {
	case ack := <-ackReceived:
		if ack.MessageID != originalID {
			t.Fatalf("transient received ACK with message ID %q, want %q", ack.MessageID, originalID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("transient never received the routed ACK")
	}

	if manager.RouteAck(&gossip.Message{Type: gossip.MessageTypeAck, MessageID: gossip.NewMessageID()}) {
		t.Fatal("RouteAck routed an ACK for a message ID it never recorded")
	}
}
