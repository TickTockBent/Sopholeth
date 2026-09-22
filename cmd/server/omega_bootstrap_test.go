package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"

	"sopholeth/internal/trust"
)

func TestResolveOmegaBootstrapRejectsUnconfiguredAnchor(t *testing.T) {
	if trust.OmegaPubkey != "" {
		t.Skip("requires an unconfigured build")
	}
	dir := t.TempDir()
	t.Setenv("NODE_CACHE_DIR", dir)
	signature := make([]byte, ed25519.SignatureSize)
	signature[0] = 1
	// A record accepted by the old placeholder under Go 1.22.
	forged := &trust.SignedList{
		Version: trust.OmegaVersion, Expires: 2_000_000_007,
		Nodes: []string{"untrusted.invalid:8080"}, Signature: signature,
	}
	if err := trust.SaveCache(dir, forged); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Never depend on external DNS, even if this regresses.
	list, err := resolveOmegaBootstrap(ctx)
	if list != nil || !errors.Is(err, trust.ErrUnconfiguredAnchor()) {
		t.Fatalf("want unconfigured authority, got %v, %v", list, err)
	}
	if !strings.Contains(err.Error(), "NODE_NETWORK=private") {
		t.Fatalf("missing development guidance: %v", err)
	}
}
