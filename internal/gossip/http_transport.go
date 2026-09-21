package gossip

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"sopholeth/internal/logging"
)

// SimpleMessage is the HTTP wire format for gossip messages.
type SimpleMessage struct {
	Type      string          `json:"type"`
	From      string          `json:"from"`
	To        string          `json:"to,omitempty"`
	Key       string          `json:"key,omitempty"`
	Data      []byte          `json:"data,omitempty"`
	TTL       int32           `json:"ttl,omitempty"`
	Timestamp int64           `json:"timestamp"`
	MessageID string          `json:"message_id"`
	NodeInfo  *SimpleNodeInfo `json:"node_info,omitempty"`
}

// SimpleNodeInfo is the wire format for node information in gossip messages.
type SimpleNodeInfo struct {
	ID       string `json:"id"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	HTTPPort int    `json:"http_port"`
	Enclave  string `json:"enclave,omitempty"` // Empty treated as "default" for backwards compat
}

// HTTPTransport implements gossip communication over HTTP
type HTTPTransport struct {
	localNode      *Node
	messageHandler func(*Message) error
	client         *http.Client
	clusterSecret  string
	mu             sync.RWMutex
}

// NewHTTPTransport creates a new HTTP-based transport.
// If clusterSecret is non-empty, all outgoing messages are HMAC-signed.
func NewHTTPTransport(localNode *Node, clusterSecret string) *HTTPTransport {
	return &HTTPTransport{
		localNode:     localNode,
		clusterSecret: clusterSecret,
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
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
	// Convert to SimpleMessage for HTTP transport
	simpleMsg := &SimpleMessage{
		Type:      string(msg.Type),
		From:      string(msg.From),
		To:        string(msg.To),
		Key:       msg.Key,
		Data:      msg.Data,
		TTL:       int32(msg.TTL),
		Timestamp: msg.Timestamp.Unix(),
		MessageID: msg.MessageID,
	}

	// Include NodeInfo if present
	if msg.NodeInfo != nil {
		simpleMsg.NodeInfo = &SimpleNodeInfo{
			ID:       string(msg.NodeInfo.ID),
			Address:  msg.NodeInfo.Address,
			Port:     msg.NodeInfo.Port,
			HTTPPort: msg.NodeInfo.HTTPPort,
			Enclave:  msg.NodeInfo.Enclave,
		}
	}

	// Send to the HTTP gossip endpoint
	url := fmt.Sprintf("http://%s:%d/v1/gossip/message", node.Address, node.HTTPPort)

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
