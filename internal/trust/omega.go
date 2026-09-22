// Package trust holds the baked-in trust anchors for the public network.
//
// OmegaPubkey is the root of trust for DNS-delivered bootstrap data. The
// corresponding private key is held offline by the network operator and is
// used only to sign root lists published via DNS TXT records (see
// docs/discovery.md). The private key is never deployed to any
// node and never transmitted over any network.
//
// OmegaVersion identifies the signing scheme. Future spec revisions that
// change the signed-record format bump this version. A running binary rejects
// any signed list whose version does not match its compiled-in OmegaVersion.
package trust

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
)

const (
	// OmegaVersion is the signing-scheme identifier. See spec section
	// "Protocol Versioning".
	OmegaVersion = "omega-v1"

	// OmegaPubkey is the base64-encoded Ed25519 public discovery anchor.
	// Empty deliberately disables public discovery until the production
	// authority is established. Tests inject disposable keys explicitly.
	OmegaPubkey = ""
)

var (
	errUnconfiguredAnchor  = errors.New("trust: omega trust anchor is not configured; use a release with a configured public authority")
	errInvalidPubkeyLength = errors.New("trust: omega public key must be 32 bytes")
)

// ErrUnconfiguredAnchor identifies an unset key or the retired all-zero
// placeholder. Neither can authorize public discovery, including caches.
func ErrUnconfiguredAnchor() error { return errUnconfiguredAnchor }

// DecodedOmegaPubkey returns OmegaPubkey parsed as an ed25519.PublicKey, or
// an error if it is unset, malformed, or the retired all-zero placeholder.
//
// Callers that need to verify signatures at runtime should call this once at
// startup and cache the result.
func DecodedOmegaPubkey() (ed25519.PublicKey, error) {
	return decodeOmegaPubkey(OmegaPubkey)
}

func decodeOmegaPubkey(encoded string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("trust: decode omega public key: %w", err)
	}
	if err := validateOmegaPubkey(raw); err != nil {
		return nil, err
	}
	return ed25519.PublicKey(raw), nil
}

func validateOmegaPubkey(pubkey ed25519.PublicKey) error {
	if len(pubkey) == 0 {
		return errUnconfiguredAnchor
	}
	if len(pubkey) != ed25519.PublicKeySize {
		return errInvalidPubkeyLength
	}
	var placeholder [ed25519.PublicKeySize]byte
	if bytes.Equal(pubkey, placeholder[:]) {
		return errUnconfiguredAnchor
	}
	return nil
}
