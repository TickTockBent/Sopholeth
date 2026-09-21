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
	"crypto/ed25519"
	"encoding/base64"
)

const (
	// OmegaVersion is the signing-scheme identifier. See spec section
	// "Protocol Versioning".
	OmegaVersion = "omega-v1"

	// OmegaPubkey is a PLACEHOLDER base64-encoded Ed25519 public key.
	// The real omega public key is generated offline and baked in as part
	// of the 2.1 release cutover (see Phase 6 in the implementation plan).
	// Tests must not rely on this value — they generate ephemeral keypairs
	// and inject them directly.
	OmegaPubkey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
)

// DecodedOmegaPubkey returns OmegaPubkey parsed as an ed25519.PublicKey, or
// an error if the baked-in value is not a valid 32-byte Ed25519 key.
//
// Callers that need to verify signatures at runtime should call this once at
// startup and cache the result.
func DecodedOmegaPubkey() (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(OmegaPubkey)
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, errInvalidPubkeyLength
	}
	return ed25519.PublicKey(raw), nil
}
