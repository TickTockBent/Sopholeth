package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sopholeth/internal/trust"
)

// TestDecodePrivateKeyRoundtrip — keygen output decodes back to a working key.
func TestDecodePrivateKeyRoundtrip(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString(priv) + "\n"

	got, err := decodePrivateKey(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != ed25519.PrivateKeySize {
		t.Errorf("size = %d, want %d", len(got), ed25519.PrivateKeySize)
	}
}

// TestDecodePrivateKeyWrongLength — truncated keys are rejected rather than
// silently producing garbage signatures.
func TestDecodePrivateKeyWrongLength(t *testing.T) {
	short := base64.StdEncoding.EncodeToString([]byte("too short"))
	_, err := decodePrivateKey(short)
	if err == nil {
		t.Fatalf("expected error for short key, got nil")
	}
}

// TestSignFlowRoundtrip — simulate `keygen` writing a private key and
// `sign` reading it + emitting a TXT record line that parses and verifies.
// This is the end-to-end operator workflow.
func TestSignFlowRoundtrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	dir := t.TempDir()
	privPath := filepath.Join(dir, "omega-v1.key")
	if err := os.WriteFile(privPath, []byte(base64.StdEncoding.EncodeToString(priv)+"\n"), 0o600); err != nil {
		t.Fatalf("write priv: %v", err)
	}

	// Build the same list the sign subcommand would build.
	loadedBytes, err := os.ReadFile(privPath)
	if err != nil {
		t.Fatalf("read priv: %v", err)
	}
	loadedPriv, err := decodePrivateKey(string(loadedBytes))
	if err != nil {
		t.Fatalf("decode priv: %v", err)
	}

	list := &trust.SignedList{
		Version: trust.OmegaVersion,
		Expires: time.Now().Add(1 * time.Hour).Unix(),
		Nodes:   splitCSV("root-a.example:9090,root-b.example:9090"),
	}
	list.Sign(loadedPriv)

	// The TXT payload must parse and verify under the original pubkey.
	parsed, err := trust.Parse(list.Encode())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := parsed.Verify(pub, time.Now()); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

// TestKeygenWritesProtectedPrivateKey — the private-key file is created
// with mode 0600 so a shared signing machine doesn't leak it.
func TestKeygenWritesProtectedPrivateKey(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses mode bits")
	}

	dir := t.TempDir()
	privPath := filepath.Join(dir, "omega-v1.key")
	pubPath := filepath.Join(dir, "omega-v1.pub")

	if err := runKeygen([]string{"--out-private", privPath, "--out-public", pubPath}); err != nil {
		t.Fatalf("keygen: %v", err)
	}

	info, err := os.Stat(privPath)
	if err != nil {
		t.Fatalf("stat priv: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("priv-key mode = %o, want 0600", perm)
	}
}

// TestSplitCSVTrimsWhitespace — operator-friendly input handling.
func TestSplitCSVTrimsWhitespace(t *testing.T) {
	got := splitCSV("  a:9090 , b:9090 ,, c:9090  ")
	want := []string{"a:9090", "b:9090", "c:9090"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %v want %v", got, want)
	}
}
