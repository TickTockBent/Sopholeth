package gossip

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"sopholeth/internal/logging"
)

// BootstrapRequest is sent when a node wants to join the cluster
type BootstrapRequest struct {
	NodeID     string `json:"node_id"`
	Address    string `json:"address"`
	GossipPort int    `json:"gossip_port"`
	HTTPPort   int    `json:"http_port"`
	Enclave    string `json:"enclave,omitempty"` // Empty treated as "default"
}

// BootstrapResponse contains the current cluster topology
type BootstrapResponse struct {
	Success bool    `json:"success"`
	Peers   []*Node `json:"peers"`
}

// Bootstrap connects to seed nodes and retrieves the cluster topology.
//
// Contacts ALL seeds (not just the first responder) so that every seed
// in the signed list registers the joining node directly. Stopping at
// the first success leaves later seeds unaware of the joiner until the
// next topology-sync round, producing asymmetric topologies (#82, F3).
//
// Peers from each response are deduplicated by NodeID across all
// seeds; "Discovered peer X" is logged once per peer regardless of how
// many seeds returned it (#82, F4).
func (p *Protocol) Bootstrap(ctx context.Context, seedNodes []string) error {
	logging.Info("[%s] Starting bootstrap process with %d seed nodes", p.localNode.ID, len(seedNodes))

	req := &BootstrapRequest{
		NodeID:     string(p.localNode.ID),
		Address:    p.localNode.Address,
		GossipPort: p.localNode.Port,
		HTTPPort:   p.localNode.HTTPPort,
		Enclave:    p.localNode.Enclave,
	}

	// The omega signed list contains every root, so a root bootstrapping
	// against the list will see its own advertised address in seedNodes.
	// Skip self-bootstrap: it succeeds (we'd be POSTing to our own listener)
	// but pollutes our peer map with self until #87's addPeer self-filter
	// rejects it. Skipping here also avoids a wasted round-trip.
	selfAdvertised := fmt.Sprintf("%s:%d", p.localNode.Address, p.localNode.HTTPPort)

	seen := make(map[NodeID]struct{})
	successfulSeeds := 0

	for _, seed := range seedNodes {
		if seed == selfAdvertised {
			logging.Debug("[%s] Skipping self in seed list (%s)", p.localNode.ID, seed)
			continue
		}
		logging.Debug("[%s] Attempting to bootstrap from %s", p.localNode.ID, seed)

		peers, err := p.sendBootstrapRequest(ctx, seed, req)
		if err != nil {
			logging.Warn("[%s] Failed to bootstrap from %s: %v", p.localNode.ID, seed, err)
			continue
		}
		successfulSeeds++

		for _, peer := range peers {
			if peer.ID == p.localNode.ID {
				continue
			}
			if _, dup := seen[peer.ID]; dup {
				continue
			}
			seen[peer.ID] = struct{}{}
			p.addPeer(peer)
			logging.Info("[%s] Discovered peer %s via bootstrap", p.localNode.ID, peer.ID)
		}
	}

	if successfulSeeds == 0 {
		logging.Info("[%s] No seed nodes available, starting as first node", p.localNode.ID)
		return nil
	}

	logging.Info("[%s] Bootstrap complete: contacted %d/%d seeds, discovered %d unique peers",
		p.localNode.ID, successfulSeeds, len(seedNodes), len(seen))
	return nil
}

func (p *Protocol) sendBootstrapRequest(ctx context.Context, seedAddr string, req *BootstrapRequest) ([]*Node, error) {
	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("http://%s/v1/bootstrap", seedAddr)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.clusterSecret != "" {
		httpReq.Header.Set(SignatureHeader, SignBody(p.clusterSecret, jsonData))
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bootstrap rejected with status: %d", resp.StatusCode)
	}

	var bootstrapResp BootstrapResponse
	if err := json.NewDecoder(resp.Body).Decode(&bootstrapResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if !bootstrapResp.Success {
		return nil, fmt.Errorf("bootstrap failed")
	}

	return bootstrapResp.Peers, nil
}

// HandleBootstrap processes incoming bootstrap requests
func (p *Protocol) HandleBootstrap(req *BootstrapRequest) *BootstrapResponse {
	// Create node info from request
	enclave := req.Enclave
	if enclave == "" {
		enclave = "default"
	}
	newNode := &Node{
		ID:       NodeID(req.NodeID),
		Address:  req.Address,
		Port:     req.GossipPort,
		HTTPPort: req.HTTPPort,
		Enclave:  enclave,
	}

	// Add the new node as a peer. addPeer is a no-op when newNode.ID
	// matches our own (e.g., a node bootstrapping against an omega list
	// that contains itself); the chokepoint warns and the self-skip in
	// Bootstrap normally prevents that path from being reached at all (#87).
	p.addPeer(newNode)
	logging.Info("[%s] Node %s joined via bootstrap", p.localNode.ID, req.NodeID)

	// Notify all existing peers about the new node
	// This ensures all nodes know about each other
	go p.notifyPeersAboutNewNode(newNode)

	// Include self in the peers list — it's how the joiner learns about
	// this seed. The caller's bootstrap loop dedupes peers by NodeID across
	// multiple seeds and filters its own ID, so this is safe and necessary
	// (#82, F4 is addressed on the caller side, not here).
	peers := p.getPeers()
	allPeers := append(peers, p.localNode)

	return &BootstrapResponse{
		Success: true,
		Peers:   allPeers,
	}
}

// notifyPeersAboutNewNode sends a SYNC message to existing peers about a new node
func (p *Protocol) notifyPeersAboutNewNode(newNode *Node) {
	peers := p.getPeers()
	for _, peer := range peers {
		if peer.ID != newNode.ID {
			// Create a SYNC message with the new node info
			msg := &Message{
				Type:      MessageTypeSync,
				From:      p.localNode.ID,
				Timestamp: time.Now(),
				MessageID: generateMessageID(),
				NodeInfo:  newNode,
			}

			// Retry SYNC messages with exponential backoff
			go p.sendSyncWithRetry(peer, msg, newNode.ID, 3)
		}
	}
}

// sendSyncWithRetry attempts to send a SYNC message with exponential backoff retry
func (p *Protocol) sendSyncWithRetry(peer *Node, msg *Message, newNodeID NodeID, maxRetries int) {
	for attempt := 0; attempt < maxRetries; attempt++ {
		if err := p.Send(context.Background(), peer, msg); err != nil {
			if attempt == maxRetries-1 {
				logging.Error("[%s] Failed to notify %s about new node %s after %d attempts: %v",
					p.localNode.ID, peer.ID, newNodeID, maxRetries, err)
			} else {
				// Exponential backoff: 1s, 2s, 4s
				delay := time.Duration(1<<attempt) * time.Second
				logging.Warn("[%s] Failed to notify %s about new node %s (attempt %d/%d), retrying in %v: %v",
					p.localNode.ID, peer.ID, newNodeID, attempt+1, maxRetries, delay, err)
				time.Sleep(delay)
			}
		} else {
			logging.Debug("[%s] Notified %s about new node %s (attempt %d)",
				p.localNode.ID, peer.ID, newNodeID, attempt+1)
			return
		}
	}
}
