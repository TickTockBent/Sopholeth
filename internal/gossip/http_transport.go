package gossip

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"sopholeth/internal/endpoint"
	"sopholeth/internal/logging"
)

// SimpleMessage is the HTTP wire format for gossip messages.
type SimpleMessage struct {
	Type      string `json:"type"`
	From      string `json:"from"`
	To        string `json:"to,omitempty"`
	Key       string `json:"key,omitempty"`
	Data      []byte `json:"data,omitempty"`
	TTL       int32  `json:"ttl,omitempty"`
	Timestamp int64  `json:"timestamp"`
	MessageID string `json:"message_id"`
	// Hops is the number of times a PUT has been forwarded (#152). Omitted
	// when 0, so a message from the originator looks as it did before.
	Hops     int             `json:"hops,omitempty"`
	NodeInfo *SimpleNodeInfo `json:"node_info,omitempty"`
}

// SimpleNodeInfo is the wire format for node information in gossip messages.
type SimpleNodeInfo struct {
	ID         string `json:"id"`
	Address    string `json:"address"`
	Port       int    `json:"port"`
	HTTPPort   int    `json:"http_port"`
	HTTPOrigin string `json:"http_origin,omitempty"`
	Enclave    string `json:"enclave,omitempty"` // Empty treated as "default" for backwards compat
}

// NewSimpleMessage converts msg to its wire form. Every transport and handler
// uses this pair, so a new Message field cannot reach one path and not
// another (#152 added Hops).
func NewSimpleMessage(msg *Message) *SimpleMessage {
	wire := &SimpleMessage{
		Type:      string(msg.Type),
		From:      string(msg.From),
		To:        string(msg.To),
		Key:       msg.Key,
		Data:      msg.Data,
		TTL:       int32(msg.TTL),
		Timestamp: msg.Timestamp.Unix(),
		MessageID: msg.MessageID,
		Hops:      msg.Hops,
	}
	if msg.NodeInfo != nil {
		wire.NodeInfo = &SimpleNodeInfo{
			ID:         string(msg.NodeInfo.ID),
			Address:    msg.NodeInfo.Address,
			Port:       msg.NodeInfo.Port,
			HTTPPort:   msg.NodeInfo.HTTPPort,
			HTTPOrigin: msg.NodeInfo.HTTPOrigin,
			Enclave:    msg.NodeInfo.Enclave,
		}
	}
	return wire
}

// ToMessage is the inverse of NewSimpleMessage. A NodeInfo without an enclave
// gets "default".
func (wire *SimpleMessage) ToMessage() *Message {
	msg := &Message{
		Type:      MessageType(wire.Type),
		From:      NodeID(wire.From),
		To:        NodeID(wire.To),
		Key:       wire.Key,
		Data:      wire.Data,
		TTL:       int(wire.TTL),
		Timestamp: time.Unix(wire.Timestamp, 0),
		MessageID: wire.MessageID,
		Hops:      wire.Hops,
	}
	if wire.NodeInfo != nil {
		enclave := wire.NodeInfo.Enclave
		if enclave == "" {
			enclave = "default"
		}
		msg.NodeInfo = &Node{
			ID:         NodeID(wire.NodeInfo.ID),
			Address:    wire.NodeInfo.Address,
			Port:       wire.NodeInfo.Port,
			HTTPPort:   wire.NodeInfo.HTTPPort,
			HTTPOrigin: wire.NodeInfo.HTTPOrigin,
			Enclave:    enclave,
		}
	}
	return msg
}

// defaultGossipTimeout bounds one gossip send when the caller sets none.
const defaultGossipTimeout = 5 * time.Second

// HTTPTransport implements gossip communication over HTTP
type HTTPTransport struct {
	localNode      *Node
	messageHandler func(*Message) error
	// client is verified by endpoint.Client once, when the transport is built
	// or reconfigured, and reused by every Send. Re-verifying per send would
	// inspect the transport while concurrent sends initialize it (#285).
	client        *http.Client
	clusterSecret string
	mu            sync.RWMutex
}

// NewHTTPTransport creates a new HTTP-based transport.
// If clusterSecret is non-empty, all outgoing messages are HMAC-signed.
// It sends through a private clone of http.DefaultTransport.
func NewHTTPTransport(localNode *Node, clusterSecret string) *HTTPTransport {
	verifiedClient, err := endpoint.Client(&http.Client{Timeout: defaultGossipTimeout})
	if err != nil {
		// A client without a transport gets a fresh default transport,
		// which always passes verification.
		panic(fmt.Sprintf("gossip: default HTTP client failed verification: %v", err))
	}
	return &HTTPTransport{
		localNode:     localNode,
		clusterSecret: clusterSecret,
		client:        verifiedClient,
	}
}

// SetHTTPClient configures CA roots for an HTTPS deployment. Call it before
// Start, never while sends are in flight. The client is verified here, once:
// a client that disables certificate or hostname verification, or uses a
// custom TLS dialer, is rejected with an error and the transport keeps its
// previous client. The stored copy refuses redirects, has no cookie jar, and
// keeps the caller's Timeout (5s when unset). nil keeps the current client.
func (t *HTTPTransport) SetHTTPClient(client *http.Client) error {
	if client == nil {
		return nil
	}
	verifiedClient, err := endpoint.Client(client)
	if err != nil {
		return err
	}
	if verifiedClient.Timeout <= 0 {
		verifiedClient.Timeout = defaultGossipTimeout
	}
	t.client = verifiedClient
	return nil
}

// Start initializes the transport (no-op for HTTP as we use the main HTTP server)
func (t *HTTPTransport) Start(ctx context.Context) error {
	logging.Info("[HTTPTransport] Started for node %s (HTTP port: %d)", t.localNode.ID, t.localNode.HTTPPort)
	return nil
}

// Stop shuts down the transport
func (t *HTTPTransport) Stop() error {
	return nil
}

// Send sends a message to a specific node via HTTP
func (t *HTTPTransport) Send(ctx context.Context, node *Node, msg *Message) error {
	simpleMsg := NewSimpleMessage(msg)

	// Send to the HTTP gossip endpoint
	origin, err := node.Origin()
	if err != nil {
		return err
	}
	url := origin + "/v1/gossip/message"

	jsonData, err := json.Marshal(simpleMsg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if t.clusterSecret != "" {
		req.Header.Set(SignatureHeader, SignBody(t.clusterSecret, jsonData))
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send message to %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("message rejected by %s with status: %d", node.ID, resp.StatusCode)
	}

	logging.Debug("[HTTPTransport] Sent %s message to %s at %s", msg.Type, node.ID, url)
	return nil
}

// SetMessageHandler sets the handler for incoming messages
func (t *HTTPTransport) SetMessageHandler(handler func(*Message) error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.messageHandler = handler
}
