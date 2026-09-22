package trust

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

// This signature uses the identity point for R and zero for S. No private
// key is involved. The old all-zero anchor accepts some of these messages
// under Go 1.22; all must be rejected regardless of the stdlib's behavior.
func TestRejectsForgedPlaceholder(t *testing.T) {
	placeholder := make(ed25519.PublicKey, ed25519.PublicKeySize)
	signature := make([]byte, ed25519.SignatureSize)
	signature[0] = 1
	now := time.Unix(1_900_000_000, 0)
	for i := int64(0); i < 256; i++ {
		list := &SignedList{
			Version: OmegaVersion, Expires: 2_000_000_000 + i,
			Nodes: []string{"untrusted.invalid:8080"}, Signature: signature,
		}
		if err := list.Verify(placeholder, now); !errors.Is(err, ErrUnconfiguredAnchor()) {
			t.Fatalf("variant %d: want unconfigured anchor, got %v", i, err)
		}
		resolver := &stubResolver{records: map[string][]string{
			DefaultBootstrapName:  {"omega=" + DefaultSignedListName},
			DefaultSignedListName: {list.Encode()},
		}}
		if got, err := FetchSigned(context.Background(), DNSConfig{Resolver: resolver}, placeholder, now); err == nil || got != nil {
			t.Fatalf("accepted forged DNS metadata (variant %d): %v, %v", i, got, err)
		}
		dir := t.TempDir()
		if err := SaveCache(dir, list); err != nil {
			t.Fatal(err)
		}
		cached, err := LoadCache(dir)
		if err != nil || cached == nil {
			t.Fatalf("load forged cache: %v, %v", cached, err)
		}
		if err := cached.Verify(placeholder, now); !errors.Is(err, ErrUnconfiguredAnchor()) {
			t.Fatalf("cached variant %d: want unconfigured anchor, got %v", i, err)
		}
	}
}

func TestDecodeOmegaPubkey(t *testing.T) {
	for _, encoded := range []string{"", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="} {
		if key, err := decodeOmegaPubkey(encoded); key != nil || !errors.Is(err, ErrUnconfiguredAnchor()) {
			t.Errorf("%q: want unconfigured anchor, got %x, %v", encoded, key, err)
		}
	}
	for _, encoded := range []string{"not base64", "AA==", base64.StdEncoding.EncodeToString(make([]byte, 33))} {
		if key, err := decodeOmegaPubkey(encoded); key != nil || err == nil {
			t.Errorf("%q: accepted malformed anchor", encoded)
		}
	}
	pub, priv := testKeypair(t)
	key, err := decodeOmegaPubkey(base64.StdEncoding.EncodeToString(pub))
	if err != nil || !key.Equal(pub) {
		t.Fatalf("generated anchor: %x, %v", key, err)
	}
	list := validList(time.Now().Add(time.Hour).Unix())
	list.Sign(priv)
	if err := list.Verify(key, time.Now()); err != nil {
		t.Fatalf("generated anchor must remain usable: %v", err)
	}
}

type forbiddenResolver struct{ t *testing.T }

func (r forbiddenResolver) LookupTXT(context.Context, string) ([]string, error) {
	r.t.Fatal("DNS must not be consulted with an unconfigured or malformed anchor")
	return nil, nil
}

func TestFetchRejectsAnchorBeforeDNS(t *testing.T) {
	for _, key := range []ed25519.PublicKey{nil, {}, make([]byte, 32), {1, 2}} {
		list, err := FetchSigned(context.Background(), DNSConfig{Resolver: forbiddenResolver{t}}, key, time.Now())
		if list != nil || err == nil {
			t.Fatalf("invalid anchor %x accepted: %v, %v", key, list, err)
		}
	}
}

// This explicit release gate is skipped by ordinary development tests.
// make check-public-release always supplies the variable, including when
// empty, so missing release configuration fails instead of skipping.
func TestPublicReleaseAnchor(t *testing.T) {
	expected, enabled := os.LookupEnv("OMEGA_EXPECTED_SHA256")
	if !enabled {
		t.Skip("run make check-public-release with OMEGA_EXPECTED_SHA256")
	}
	if err := checkReleaseAnchor(OmegaPubkey, expected); err != nil {
		t.Fatal(err)
	}
}

func checkReleaseAnchor(encoded, expected string) error {
	want, err := hex.DecodeString(expected)
	if err != nil || len(want) != sha256.Size {
		return fmt.Errorf("OMEGA_EXPECTED_SHA256 must be the independently verified 64-hex-digit authority fingerprint")
	}
	key, err := decodeOmegaPubkey(encoded)
	if err != nil {
		return err
	}
	got := sha256.Sum256(key)
	if fmt.Sprintf("%x", got) != hex.EncodeToString(want) {
		return fmt.Errorf("compiled omega fingerprint %x does not match expected %s", got, expected)
	}
	return nil
}

func TestReleaseFingerprintValidation(t *testing.T) {
	pub, _ := testKeypair(t)
	encoded := base64.StdEncoding.EncodeToString(pub)
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(pub))
	if err := checkReleaseAnchor(encoded, fingerprint); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ key, fingerprint string }{
		{encoded, ""}, {encoded, "not hex"}, {encoded, "00"},
		{encoded, fmt.Sprintf("%064x", 0)},
		{"", fingerprint}, {"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", fingerprint},
		{"AA==", fingerprint}, {"not base64", fingerprint},
	} {
		if err := checkReleaseAnchor(tc.key, tc.fingerprint); err == nil {
			t.Errorf("accepted invalid release key/fingerprint: %+v", tc)
		}
	}
}
