package trust

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// testKeypair generates an ephemeral Ed25519 keypair for tests. The real
// omega pubkey (the package-level placeholder) is never used here.
func testKeypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	return pub, priv
}

func validList(expires int64) *SignedList {
	return &SignedList{
		Version: OmegaVersion,
		Expires: expires,
		Nodes:   []string{"root-c.example:9090", "root-a.example:9090", "root-b.example:9090"},
	}
}

// TestCanonicalSortsAndOrdersFields verifies the canonical payload has the
// fixed field order and lex-sorted nodes regardless of input order.
func TestCanonicalSortsAndOrdersFields(t *testing.T) {
	list := validList(1_900_000_000)
	got := string(list.Canonical())
	want := "v=omega-v1;exp=1900000000;nodes=root-a.example:9090,root-b.example:9090,root-c.example:9090"
	if got != want {
		t.Errorf("canonical mismatch:\n got: %s\nwant: %s", got, want)
	}
}

// TestCanonicalIsStableAcrossInputPermutations — canonical form is identical
// for any input node ordering. This is the property signatures depend on.
func TestCanonicalIsStableAcrossInputPermutations(t *testing.T) {
	base := validList(1_900_000_000)
	baseCanonical := string(base.Canonical())

	shuffled := &SignedList{
		Version: base.Version,
		Expires: base.Expires,
		Nodes:   []string{"root-b.example:9090", "root-c.example:9090", "root-a.example:9090"},
	}
	if string(shuffled.Canonical()) != baseCanonical {
		t.Errorf("canonical form not stable across node permutations")
	}
}

// TestSignVerifyRoundtrip — a list signed with priv verifies with pub.
func TestSignVerifyRoundtrip(t *testing.T) {
	pub, priv := testKeypair(t)
	list := validList(time.Now().Add(1 * time.Hour).Unix())
	list.Sign(priv)

	if err := list.Verify(pub, time.Now()); err != nil {
		t.Fatalf("verify freshly signed list: %v", err)
	}
}

// TestSignVerifyAcrossEncodeParse — a signed list survives encode/parse
// roundtrip and still verifies.
func TestSignVerifyAcrossEncodeParse(t *testing.T) {
	pub, priv := testKeypair(t)
	list := validList(time.Now().Add(1 * time.Hour).Unix())
	list.Sign(priv)

	encoded := list.Encode()
	parsed, err := Parse(encoded)
	if err != nil {
		t.Fatalf("parse encoded list: %v", err)
	}

	if err := parsed.Verify(pub, time.Now()); err != nil {
		t.Fatalf("verify parsed list: %v", err)
	}
}

// TestParseToleratesFieldReordering — tolerate arbitrary field order on the
// wire; canonicalization is what matters for signature.
func TestParseToleratesFieldReordering(t *testing.T) {
	pub, priv := testKeypair(t)
	list := validList(time.Now().Add(1 * time.Hour).Unix())
	list.Sign(priv)

	sigB64 := base64.StdEncoding.EncodeToString(list.Signature)
	reordered := "sig=" + sigB64 + ";nodes=root-a.example:9090,root-b.example:9090,root-c.example:9090;exp=" +
		strconv.FormatInt(list.Expires, 10) + ";v=" + OmegaVersion

	parsed, err := Parse(reordered)
	if err != nil {
		t.Fatalf("parse reordered: %v", err)
	}
	if err := parsed.Verify(pub, time.Now()); err != nil {
		t.Fatalf("verify reordered: %v", err)
	}
}

// TestParseRejectsDuplicateFields — a record with two v= or two sig= fields
// is malformed. Defends against a splicer trying to confuse field resolution.
func TestParseRejectsDuplicateFields(t *testing.T) {
	raw := "v=omega-v1;v=omega-v1;exp=1900000000;nodes=a:9090;sig=" +
		base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	_, err := Parse(raw)
	if !errors.Is(err, errDuplicateField) {
		t.Errorf("want errDuplicateField, got %v", err)
	}
}

// TestParseRejectsMissingFields — each required field is individually
// required.
func TestParseRejectsMissingFields(t *testing.T) {
	cases := map[string]string{
		"missing v":     "exp=1900000000;nodes=a:9090;sig=AAAA",
		"missing exp":   "v=omega-v1;nodes=a:9090;sig=AAAA",
		"missing nodes": "v=omega-v1;exp=1900000000;sig=AAAA",
		"missing sig":   "v=omega-v1;exp=1900000000;nodes=a:9090",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(raw)
			if !errors.Is(err, errMissingField) {
				t.Errorf("want errMissingField, got %v", err)
			}
		})
	}
}

// TestParseRejectsMalformedExp — exp must parse as int64.
func TestParseRejectsMalformedExp(t *testing.T) {
	raw := "v=omega-v1;exp=not-a-number;nodes=a:9090;sig=AAAA"
	_, err := Parse(raw)
	if !errors.Is(err, errMalformedField) {
		t.Errorf("want errMalformedField, got %v", err)
	}
}

// TestParseRejectsMalformedSignature — sig must decode as base64.
func TestParseRejectsMalformedSignature(t *testing.T) {
	raw := "v=omega-v1;exp=1900000000;nodes=a:9090;sig=!!!not-base64!!!"
	_, err := Parse(raw)
	if !errors.Is(err, errMalformedSignature) {
		t.Errorf("want errMalformedSignature, got %v", err)
	}
}

// TestParseRejectsEmptyNodes — nodes= with empty value is rejected.
func TestParseRejectsEmptyNodes(t *testing.T) {
	raw := "v=omega-v1;exp=1900000000;nodes=;sig=AAAA"
	_, err := Parse(raw)
	if !errors.Is(err, errEmptyNodes) {
		t.Errorf("want errEmptyNodes, got %v", err)
	}
}

// TestParseRejectsUnknownFields — omega-v1 is a strict wire format.
// Unknown fields are rejected rather than silently dropped, because the
// signature only covers Canonical() (which includes just the known
// fields); accepting unknowns would admit unauthenticated data into an
// authenticated record. Forward-compatible extensions must bump the
// omega version.
func TestParseRejectsUnknownFields(t *testing.T) {
	_, priv := testKeypair(t)
	list := validList(time.Now().Add(1 * time.Hour).Unix())
	list.Sign(priv)

	raw := list.Encode() + ";future_field=whatever"
	_, err := Parse(raw)
	if !errors.Is(err, errUnknownField) {
		t.Errorf("want errUnknownField, got %v", err)
	}
}

// TestVerifyRejectsVersionMismatch — a signed list built for a future
// omega-v2 is rejected by an omega-v1 binary.
func TestVerifyRejectsVersionMismatch(t *testing.T) {
	pub, priv := testKeypair(t)
	list := validList(time.Now().Add(1 * time.Hour).Unix())
	list.Version = "omega-v2"
	list.Sign(priv)

	err := list.Verify(pub, time.Now())
	if !errors.Is(err, errVersionMismatch) {
		t.Errorf("want errVersionMismatch, got %v", err)
	}
}

// TestVerifyRejectsExpiredList — exp at or before now is invalid.
func TestVerifyRejectsExpiredList(t *testing.T) {
	pub, priv := testKeypair(t)
	expiresAt := time.Now().Add(-1 * time.Second).Unix()
	list := validList(expiresAt)
	list.Sign(priv)

	err := list.Verify(pub, time.Now())
	if !errors.Is(err, errExpiredList) {
		t.Errorf("want errExpiredList, got %v", err)
	}
}

// TestVerifyRejectsTamperedNodes — flipping a node address invalidates the
// signature. This is the whole point.
func TestVerifyRejectsTamperedNodes(t *testing.T) {
	pub, priv := testKeypair(t)
	list := validList(time.Now().Add(1 * time.Hour).Unix())
	list.Sign(priv)

	list.Nodes[0] = "attacker.example:9090"

	err := list.Verify(pub, time.Now())
	if !errors.Is(err, errBadSignature) {
		t.Errorf("want errBadSignature, got %v", err)
	}
}

// TestVerifyRejectsTamperedExpiration — moving exp forward without re-signing
// is detected.
func TestVerifyRejectsTamperedExpiration(t *testing.T) {
	pub, priv := testKeypair(t)
	list := validList(time.Now().Add(1 * time.Hour).Unix())
	list.Sign(priv)

	list.Expires += 86400

	err := list.Verify(pub, time.Now())
	if !errors.Is(err, errBadSignature) {
		t.Errorf("want errBadSignature, got %v", err)
	}
}

// TestVerifyRejectsWrongPubkey — a list signed by key A does not verify
// under key B.
func TestVerifyRejectsWrongPubkey(t *testing.T) {
	_, privA := testKeypair(t)
	pubB, _ := testKeypair(t)

	list := validList(time.Now().Add(1 * time.Hour).Unix())
	list.Sign(privA)

	err := list.Verify(pubB, time.Now())
	if !errors.Is(err, errBadSignature) {
		t.Errorf("want errBadSignature, got %v", err)
	}
}

// TestVerifyRejectsVersionSubstringVariants — exact-match only. "omega-v11"
// and "omega-v1 " (trailing space) both claim the omega-v1 scheme loosely,
// but neither is the string the binary recognizes. Rejected.
func TestVerifyRejectsVersionSubstringVariants(t *testing.T) {
	pub, priv := testKeypair(t)
	for _, v := range []string{"omega-v11", "omega-v1 ", " omega-v1", "omega-v10"} {
		t.Run(v, func(t *testing.T) {
			list := validList(time.Now().Add(1 * time.Hour).Unix())
			list.Version = v
			list.Sign(priv)
			err := list.Verify(pub, time.Now())
			if !errors.Is(err, errVersionMismatch) {
				t.Errorf("want errVersionMismatch, got %v", err)
			}
		})
	}
}

// TestParseTrimsWhitespaceBetweenNodes — operator-side whitespace in the
// comma-separated node list is tolerated. The canonical form is still
// whitespace-free, so signatures remain valid end-to-end.
func TestParseTrimsWhitespaceBetweenNodes(t *testing.T) {
	_, priv := testKeypair(t)
	list := validList(time.Now().Add(1 * time.Hour).Unix())
	list.Sign(priv)

	// Re-emit with interstitial whitespace in the nodes field. A strict
	// parser must still produce the same Nodes slice.
	wireWithSpaces := "v=omega-v1;exp=" +
		strconv.FormatInt(list.Expires, 10) +
		";nodes= root-a.example:9090 , root-b.example:9090 , root-c.example:9090 ;sig=" +
		base64.StdEncoding.EncodeToString(list.Signature)

	parsed, err := Parse(wireWithSpaces)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"root-a.example:9090", "root-b.example:9090", "root-c.example:9090"}
	if len(parsed.Nodes) != len(want) {
		t.Fatalf("nodes length = %d, want %d", len(parsed.Nodes), len(want))
	}
	for i := range want {
		if parsed.Nodes[i] != want[i] {
			t.Errorf("Nodes[%d] = %q, want %q", i, parsed.Nodes[i], want[i])
		}
	}
}

// TestVerifyRejectsWrongPubkeyLength — guards against a corrupted baked-in
// value being silently accepted.
func TestVerifyRejectsWrongPubkeyLength(t *testing.T) {
	_, priv := testKeypair(t)
	list := validList(time.Now().Add(1 * time.Hour).Unix())
	list.Sign(priv)

	err := list.Verify(ed25519.PublicKey{0x00, 0x01}, time.Now())
	if !errors.Is(err, errInvalidPubkeyLength) {
		t.Errorf("want errInvalidPubkeyLength, got %v", err)
	}
}

// An ordinary build may have no public authority. A configured release must
// pass validation, and its identity is checked by TestPublicReleaseAnchor.
func TestDecodedOmegaPubkeyShape(t *testing.T) {
	pub, err := DecodedOmegaPubkey()
	if OmegaPubkey == "" {
		if pub != nil || !errors.Is(err, ErrUnconfiguredAnchor()) {
			t.Fatalf("unset anchor should disable public discovery: %x, %v", pub, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("decode baked-in pubkey: %v", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		t.Errorf("baked-in pubkey length = %d, want %d", len(pub), ed25519.PublicKeySize)
	}
}

// TestEncodeRoundTrip — Encode produces the exact canonical form plus a
// trailing sig field, parseable back.
func TestEncodeRoundTrip(t *testing.T) {
	_, priv := testKeypair(t)
	list := validList(1_900_000_000)
	list.Sign(priv)

	encoded := list.Encode()
	// Canonical form is a prefix of the encoded record.
	if !strings.HasPrefix(encoded, string(list.Canonical())+";sig=") {
		t.Errorf("encoded form does not start with canonical+sig separator: %s", encoded)
	}

	parsed, err := Parse(encoded)
	if err != nil {
		t.Fatalf("parse encoded: %v", err)
	}
	if parsed.Version != list.Version || parsed.Expires != list.Expires {
		t.Errorf("roundtrip fields differ: got %+v want %+v", parsed, list)
	}
}
