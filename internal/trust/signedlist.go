package trust

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Spec field names and separators. See docs/discovery.md
// "Signed Root List: DNS Format" and "Signature Format".
const (
	fieldVersion = "v"
	fieldExpires = "exp"
	fieldNodes   = "nodes"
	fieldSig     = "sig"

	fieldSeparator = ";"
	kvSeparator    = "="
	nodeSeparator  = ","
)

var (
	errMissingField       = errors.New("trust: signed list missing required field")
	errDuplicateField     = errors.New("trust: signed list has duplicate field")
	errMalformedField     = errors.New("trust: signed list has malformed field")
	errMalformedSignature = errors.New("trust: signed list signature is not valid base64")
	errExpiredList        = errors.New("trust: signed list has expired")
	errVersionMismatch    = errors.New("trust: signed list version does not match binary")
	errBadSignature       = errors.New("trust: signed list signature verification failed")
	errEmptyNodes         = errors.New("trust: signed list contains no nodes")
	errUnknownField       = errors.New("trust: signed list has unknown field")
)

// SignedList is a verified-or-unverified parsed root list. Construct one via
// Parse (for records received over DNS) or by setting the fields directly
// (for signing tools). Verify must be called before trusting Nodes.
type SignedList struct {
	// Version must equal OmegaVersion at verification time.
	Version string

	// Expires is a Unix timestamp (seconds). The list is invalid at or
	// after this time.
	Expires int64

	// Nodes are advertised addresses ("host:http-port") of the root
	// nodes. Stored in the order received; Canonical() sorts them.
	// See docs/discovery.md for the rationale (#82, F1/F2).
	Nodes []string

	// Signature is the Ed25519 signature over Canonical(). Empty on a
	// freshly constructed list that has not yet been signed.
	Signature []byte
}

// Parse deserializes a TXT-record payload of the form
//
//	v=omega-v1;exp=1735689600;nodes=a:9090,b:9090;sig=<base64>
//
// Field order is tolerated on input but normalized on output via Canonical.
// Duplicate fields are rejected.
func Parse(raw string) (*SignedList, error) {
	if raw == "" {
		return nil, fmt.Errorf("%w: empty record", errMalformedField)
	}

	seen := make(map[string]struct{}, 4)
	var list SignedList
	var hasVersion, hasExpires, hasNodes, hasSig bool

	for _, part := range strings.Split(raw, fieldSeparator) {
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, kvSeparator)
		if !ok {
			return nil, fmt.Errorf("%w: %q", errMalformedField, part)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		if _, dup := seen[key]; dup {
			return nil, fmt.Errorf("%w: %s", errDuplicateField, key)
		}
		seen[key] = struct{}{}

		switch key {
		case fieldVersion:
			list.Version = value
			hasVersion = true
		case fieldExpires:
			exp, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%w: exp=%q", errMalformedField, value)
			}
			list.Expires = exp
			hasExpires = true
		case fieldNodes:
			if value == "" {
				return nil, errEmptyNodes
			}
			list.Nodes = splitAndTrim(value, nodeSeparator)
			if len(list.Nodes) == 0 {
				return nil, errEmptyNodes
			}
			hasNodes = true
		case fieldSig:
			sig, err := base64.StdEncoding.DecodeString(value)
			if err != nil {
				return nil, errMalformedSignature
			}
			list.Signature = sig
			hasSig = true
		default:
			// omega-v1 is a strict wire format: unknown fields are
			// rejected rather than silently dropped. The signature
			// only covers Canonical() — which contains the known
			// fields — so accepting unknowns would admit
			// unauthenticated data into an otherwise-authenticated
			// record. Any future forward-compatible extension must
			// bump OmegaVersion.
			return nil, fmt.Errorf("%w: %s", errUnknownField, key)
		}
	}

	switch {
	case !hasVersion:
		return nil, fmt.Errorf("%w: %s", errMissingField, fieldVersion)
	case !hasExpires:
		return nil, fmt.Errorf("%w: %s", errMissingField, fieldExpires)
	case !hasNodes:
		return nil, fmt.Errorf("%w: %s", errMissingField, fieldNodes)
	case !hasSig:
		return nil, fmt.Errorf("%w: %s", errMissingField, fieldSig)
	}

	return &list, nil
}

// Canonical returns the signed-payload bytes: fixed field order, nodes
// lex-sorted, no whitespace, no signature. This is the exact byte sequence
// that Sign signs and Verify verifies.
func (s *SignedList) Canonical() []byte {
	nodes := make([]string, len(s.Nodes))
	copy(nodes, s.Nodes)
	sort.Strings(nodes)

	var b strings.Builder
	b.WriteString(fieldVersion)
	b.WriteString(kvSeparator)
	b.WriteString(s.Version)
	b.WriteString(fieldSeparator)
	b.WriteString(fieldExpires)
	b.WriteString(kvSeparator)
	b.WriteString(strconv.FormatInt(s.Expires, 10))
	b.WriteString(fieldSeparator)
	b.WriteString(fieldNodes)
	b.WriteString(kvSeparator)
	b.WriteString(strings.Join(nodes, nodeSeparator))
	return []byte(b.String())
}

// Encode produces the full TXT-record value including the base64-encoded
// signature. Intended for use by the omega signing tool.
func (s *SignedList) Encode() string {
	return string(s.Canonical()) + fieldSeparator + fieldSig + kvSeparator +
		base64.StdEncoding.EncodeToString(s.Signature)
}

// Sign computes the signature over Canonical() using priv, stores it on s,
// and returns it.
func (s *SignedList) Sign(priv ed25519.PrivateKey) []byte {
	sig := ed25519.Sign(priv, s.Canonical())
	s.Signature = sig
	return sig
}

// Verify validates version, expiration, and signature against pubkey. It
// reports the first failure encountered. Callers MUST check the returned
// error before trusting s.Nodes.
//
// now is the reference time for expiration checks (usually time.Now()).
// Separate argument so tests can inject deterministic clocks.
func (s *SignedList) Verify(pubkey ed25519.PublicKey, now time.Time) error {
	if err := validateOmegaPubkey(pubkey); err != nil {
		return err
	}
	if s.Version != OmegaVersion {
		return fmt.Errorf("%w: got %q want %q", errVersionMismatch, s.Version, OmegaVersion)
	}
	if now.Unix() >= s.Expires {
		return fmt.Errorf("%w: exp=%d now=%d", errExpiredList, s.Expires, now.Unix())
	}
	if len(s.Nodes) == 0 {
		return errEmptyNodes
	}
	// Explicit length guard on top of ed25519.Verify (which also returns
	// false for wrong-size signatures). Distinguishing a malformed record
	// from a tampered record helps operators debug: DNS truncation shows
	// up as errMalformedSignature, whereas real tampering shows up as
	// errBadSignature.
	if len(s.Signature) != ed25519.SignatureSize {
		return errMalformedSignature
	}
	if !ed25519.Verify(pubkey, s.Canonical(), s.Signature) {
		return errBadSignature
	}
	return nil
}

// Sentinel-error accessors for callers that want to branch on failure mode
// without importing package-private identifiers.

// ErrVersionMismatch indicates a signed list whose v= field does not match
// the binary's compiled-in OmegaVersion.
func ErrVersionMismatch() error { return errVersionMismatch }

// ErrExpired indicates a signed list whose exp= timestamp is in the past.
func ErrExpired() error { return errExpiredList }

// ErrBadSignature indicates a signed list whose signature does not verify
// against the provided public key.
func ErrBadSignature() error { return errBadSignature }

func splitAndTrim(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
