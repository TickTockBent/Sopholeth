package trust

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"
)

// stubResolver is an in-memory TXTResolver for tests. Keyed by hostname.
type stubResolver struct {
	records map[string][]string
	err     map[string]error
}

func (s *stubResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	if err, ok := s.err[name]; ok {
		return nil, err
	}
	return s.records[name], nil
}

func TestFetchSignedHappyPath(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	list := &SignedList{
		Version: OmegaVersion,
		Expires: time.Now().Add(1 * time.Hour).Unix(),
		Nodes:   []string{"root-a.example:9090", "root-b.example:9090"},
	}
	list.Sign(priv)

	resolver := &stubResolver{
		records: map[string][]string{
			DefaultBootstrapName:  {"omega=" + DefaultSignedListName},
			DefaultSignedListName: {list.Encode()},
		},
	}

	got, err := FetchSigned(context.Background(), DNSConfig{Resolver: resolver}, pub, time.Now())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(got.Nodes) != 2 {
		t.Errorf("nodes length = %d, want 2", len(got.Nodes))
	}
}

func TestFetchSignedHandlesIndirectionMissing(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	resolver := &stubResolver{
		records: map[string][]string{
			DefaultBootstrapName: {"something-else=value"},
		},
	}
	_, err := FetchSigned(context.Background(), DNSConfig{Resolver: resolver}, pub, time.Now())
	if !errors.Is(err, errNoBootstrapRecord) {
		t.Errorf("want errNoBootstrapRecord, got %v", err)
	}
}

func TestFetchSignedPropagatesVerifyFailure(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	list := &SignedList{
		Version: OmegaVersion,
		Expires: time.Now().Add(-1 * time.Minute).Unix(), // expired
		Nodes:   []string{"a:9090"},
	}
	list.Sign(priv)

	resolver := &stubResolver{
		records: map[string][]string{
			DefaultBootstrapName:  {"omega=" + DefaultSignedListName},
			DefaultSignedListName: {list.Encode()},
		},
	}
	_, err := FetchSigned(context.Background(), DNSConfig{Resolver: resolver}, pub, time.Now())
	if !errors.Is(err, errExpiredList) {
		t.Errorf("want errExpiredList, got %v", err)
	}
}

func TestFetchSignedPropagatesSignatureMismatch(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	attackerPub, _, _ := ed25519.GenerateKey(rand.Reader)

	list := &SignedList{
		Version: OmegaVersion,
		Expires: time.Now().Add(1 * time.Hour).Unix(),
		Nodes:   []string{"a:9090"},
	}
	list.Sign(priv) // signed by legitimate key

	resolver := &stubResolver{
		records: map[string][]string{
			DefaultBootstrapName:  {"omega=" + DefaultSignedListName},
			DefaultSignedListName: {list.Encode()},
		},
	}
	// Attacker's pubkey does not match the signing key.
	_, err := FetchSigned(context.Background(), DNSConfig{Resolver: resolver}, attackerPub, time.Now())
	if !errors.Is(err, errBadSignature) {
		t.Errorf("want errBadSignature, got %v", err)
	}
}

func TestFetchSignedDNSError(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	sentinel := errors.New("dns down")
	resolver := &stubResolver{
		err: map[string]error{DefaultBootstrapName: sentinel},
	}
	_, err := FetchSigned(context.Background(), DNSConfig{Resolver: resolver}, pub, time.Now())
	if !errors.Is(err, sentinel) {
		t.Errorf("want sentinel, got %v", err)
	}
}
