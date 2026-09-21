// Package ws implements the WebSocket transport for substrate-transient
// attachments described in docs/architecture.md.
//
// Substrate nodes (NODE_INBOUND=true) accept inbound WS connections at
// /v1/ws. Transient nodes (NODE_INBOUND=false, the default) dial a
// substrate's /v1/ws after HTTP bootstrap and stay attached. Gossip
// frames sent over WS carry the same JSON wire format as HTTP gossip;
// a node processes a PUT identically regardless of the arrival transport.
//
// The AttachmentMessage envelope adds three lifecycle types (hello,
// welcome, goodbye) that HTTP gossip does not need.
package ws

import (
	"encoding/json"
	"time"

	"sopholeth/internal/gossip"
)

// AttachmentType identifies the kind of message carried on a substrate-
// transient WebSocket attachment. The gossip-shaped types (put/ack/ping/
// pong/topology_sync) wrap the same payload as the HTTP gossip endpoint.
type AttachmentType string

const (
	AttachmentTypePut          AttachmentType = "put"
	AttachmentTypeAck          AttachmentType = "ack"
	AttachmentTypePing         AttachmentType = "ping"
	AttachmentTypePong         AttachmentType = "pong"
	AttachmentTypeTopologySync AttachmentType = "topology_sync"
	AttachmentTypeHello        AttachmentType = "hello"
	AttachmentTypeWelcome      AttachmentType = "welcome"
	AttachmentTypeGoodbye      AttachmentType = "goodbye"
)

// AttachmentMessage is the on-wire envelope. Payload is preserved as raw
// JSON bytes so the receiver verifies the HMAC against the exact bytes the
// sender signed — re-marshaling on either side risks key-ordering drift.
type AttachmentMessage struct {
	Type      AttachmentType  `json:"type"`
	Signature string          `json:"signature,omitempty"`
	Payload   json.RawMessage `json:"payload"`
}

// Capabilities declares whether the announcing node accepts inbound WS
// attachments. Maps directly to the NODE_INBOUND env var.
type Capabilities struct {
	Inbound string `json:"inbound"` // "true" | "false"
}

// HelloPayload announces a transient attaching to a substrate.
type HelloPayload struct {
	NodeID       string       `json:"node_id"`
	Enclave      string       `json:"enclave"`
	Address      string       `json:"address"`
	HTTPPort     int          `json:"http_port"`
	Capabilities Capabilities `json:"capabilities"`
}

// WirePosition describes the transient's location in the substrate tree.
type WirePosition struct {
	Depth    int    `json:"depth"`
	ParentID string `json:"parent_id"`
}

// WelcomePayload is the substrate's reply to hello. Topology entries are
// SimpleMessage-shaped SYNC announcements so the transient can seed its
// peer map without a separate format.
type WelcomePayload struct {
	Topology     []gossip.SimpleMessage `json:"topology"`
	YourPosition WirePosition           `json:"your_position"`
}

// AlternativeParent is a fallback substrate the transient can attach to
// when its current parent goes away.
type AlternativeParent struct {
	ID       string `json:"id"`
	Address  string `json:"address"`
	HTTPPort int    `json:"http_port"`
	Enclave  string `json:"enclave,omitempty"`
}

// GoodbyePayload is sent by a substrate before shutting down to keep
// transient reattachment latency in the 3-5s range instead of waiting
// the full heartbeat timeout.
type GoodbyePayload struct {
	Reason             string              `json:"reason"`
	AlternativeParents []AlternativeParent `json:"alternative_parents"`
}

// gossipTypeFor maps an internal MessageType to its on-wire AttachmentType.
func gossipTypeFor(t gossip.MessageType) AttachmentType {
	switch t {
	case gossip.MessageTypePut:
		return AttachmentTypePut
	case gossip.MessageTypeAck:
		return AttachmentTypeAck
	case gossip.MessageTypePing:
		return AttachmentTypePing
	case gossip.MessageTypePong:
		return AttachmentTypePong
	case gossip.MessageTypeSync:
		return AttachmentTypeTopologySync
	default:
		return AttachmentTypePut
	}
}

// isGossipType reports whether the AttachmentType carries a gossip Message
// payload (versus a lifecycle hello/welcome/goodbye).
func isGossipType(t AttachmentType) bool {
	switch t {
	case AttachmentTypePut, AttachmentTypeAck,
		AttachmentTypePing, AttachmentTypePong,
		AttachmentTypeTopologySync:
		return true
	}
	return false
}

// messageToWire converts a gossip Message to its SimpleMessage wire form.
// encoding/json base64-encodes []byte automatically — matches the TS
// reference's explicit base64 encoding of the data field.
func messageToWire(msg *gossip.Message) *gossip.SimpleMessage {
	wire := &gossip.SimpleMessage{
		Type:      string(msg.Type),
		From:      string(msg.From),
		To:        string(msg.To),
		Key:       msg.Key,
		Data:      msg.Data,
		TTL:       int32(msg.TTL),
		Timestamp: msg.Timestamp.Unix(),
		MessageID: msg.MessageID,
	}
	if msg.NodeInfo != nil {
		wire.NodeInfo = &gossip.SimpleNodeInfo{
			ID:       string(msg.NodeInfo.ID),
			Address:  msg.NodeInfo.Address,
			Port:     msg.NodeInfo.Port,
			HTTPPort: msg.NodeInfo.HTTPPort,
			Enclave:  msg.NodeInfo.Enclave,
		}
	}
	return wire
}

// wireToMessage is the inverse of messageToWire.
func wireToMessage(wire *gossip.SimpleMessage) *gossip.Message {
	msg := &gossip.Message{
		Type:      gossip.MessageType(wire.Type),
		From:      gossip.NodeID(wire.From),
		To:        gossip.NodeID(wire.To),
		Key:       wire.Key,
		Data:      wire.Data,
		TTL:       int(wire.TTL),
		Timestamp: time.Unix(wire.Timestamp, 0),
		MessageID: wire.MessageID,
	}
	if wire.NodeInfo != nil {
		enclave := wire.NodeInfo.Enclave
		if enclave == "" {
			enclave = "default"
		}
		msg.NodeInfo = &gossip.Node{
			ID:       gossip.NodeID(wire.NodeInfo.ID),
			Address:  wire.NodeInfo.Address,
			Port:     wire.NodeInfo.Port,
			HTTPPort: wire.NodeInfo.HTTPPort,
			Enclave:  enclave,
		}
	}
	return msg
}
