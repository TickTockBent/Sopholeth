package gossip

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// SignatureHeader carries the body HMAC on HTTP gossip and bootstrap requests.
const SignatureHeader = "X-Gossip-Signature"

// SignBody computes an HMAC-SHA256 signature of body using secret.
func SignBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyBody checks that signature is a valid HMAC-SHA256 of body.
func VerifyBody(secret string, body []byte, signature string) bool {
	expected := SignBody(secret, body)
	return hmac.Equal([]byte(expected), []byte(signature))
}
