package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"sopholeth/internal/cluster"
	"sopholeth/internal/discovery"
	"sopholeth/internal/gossip"
	"sopholeth/internal/logging"
	"sopholeth/internal/trust/bootstrap"
)

func resolveOmegaBootstrap(ctx context.Context) (*discovery.Session, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	b, err := discovery.PublicBundle()
	if err != nil {
		return nil, fmt.Errorf("public bootstrap unavailable (use NODE_NETWORK=private for local development): %w", err)
	}
	dir := os.Getenv("NODE_STATE_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return nil, fmt.Errorf("set NODE_STATE_DIR to a persistent directory owned by the node account")
		}
		dir = filepath.Join(home, ".sopholeth", "state")
	}
	s, err := discovery.Open(ctx, b, filepath.Join(dir, "discovery"), nil)
	if err != nil {
		return nil, err
	}
	err = s.Refresh(ctx)
	if err != nil {
		logging.Warn("Omega refresh failed: %v; checking durable discovery", err)
	} else {
		omegaLastRefreshGauge.SetToCurrentTime()
	}
	if _, currentErr := s.Current(); currentErr != nil {
		return nil, fmt.Errorf("no valid public discovery: %w (refresh: %v)", currentErr, err)
	}
	return s, nil
}

func publicSeedCheck(s *discovery.Session) func(string, []*gossip.Node) error {
	return func(origin string, peers []*gossip.Node) error {
		v, err := s.Current()
		if err != nil {
			return err
		}
		for _, root := range v.Manifest.Roots {
			if root.Origin != origin {
				continue
			}
			if peers == nil {
				return nil
			} // Before the network request.
			for _, peer := range peers {
				if peer == nil || string(peer.ID) != root.ID {
					continue
				}
				advertised, err := peer.Origin()
				if err == nil && advertised == origin && peer.Enclave == v.Manifest.Enclave {
					return nil
				}
			}
			return fmt.Errorf("bootstrap root %s response does not advertise its signed ID, origin and enclave", origin)
		}
		return fmt.Errorf("bootstrap origin %s is no longer in verified discovery", origin)
	}
}

func configurePublicDiscovery(cn *cluster.ClusterNode, s *discovery.Session, id, origin, enclave string) error {
	view, err := s.Current()
	if err != nil {
		return err
	}
	for _, root := range view.Manifest.Roots {
		if root.ID == id && (root.Origin != origin || view.Manifest.Enclave != enclave) {
			return fmt.Errorf("listed root %s requires NODE_HTTP_ORIGIN=%s and NODE_ENCLAVE=%s", id, root.Origin, view.Manifest.Enclave)
		}
	}
	cn.SetRootProvider(func() bool { return s.IsRoot(id, origin, enclave) })
	cn.SetSeedProvider(s.Seeds)
	s.SetViewHandler(func(v bootstrap.View) {
		bindings := make(map[gossip.NodeID]gossip.PeerBinding, len(v.Manifest.Roots))
		for _, root := range v.Manifest.Roots {
			bindings[gossip.NodeID(root.ID)] = gossip.PeerBinding{Origin: root.Origin, Enclave: v.Manifest.Enclave}
		}
		cn.SetRootBindings(bindings)
	})
	return cn.ConfigureBootstrap(nil, publicSeedCheck(s))
}
