package gossip

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// messageIDBytes is the number of random bytes in a message ID. 128 bits
// make a collision between any two IDs ever generated on any node
// negligible, without coordination between nodes and without state that a
// restart would reset.
const messageIDBytes = 16

// MessageIDLength is the length of a NewMessageID result: lowercase hex, two
// characters per byte.
const MessageIDLength = 2 * messageIDBytes

// NewMessageID returns a fresh gossip message ID: 128 bits from crypto/rand
// as 32 lowercase hex characters. Only the originator of a message calls it;
// relays, forwards, ACKs and the WS envelope carry the originator's ID
// unchanged.
//
// The ID is opaque. It encodes nothing about the key, the node or the time,
// and receivers only compare it for equality (dedup cache, pending writes,
// ACK routes), so IDs from older nodes in other formats still work.
//
// NewMessageID panics if the system random source fails. There is no
// time-based fallback on purpose: a predictable or repeating ID would
// silently drop writes as duplicates and cross-credit ACKs (#166), which is
// worse than stopping. Since Go 1.24 crypto/rand.Read never returns an
// error (it crashes the process itself if the source fails), so the panic
// only documents the contract in case that ever changes.
func NewMessageID() string {
	var randomBytes [messageIDBytes]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		panic(fmt.Sprintf("gossip: crypto/rand failed generating a message ID: %v", err))
	}
	return hex.EncodeToString(randomBytes[:])
}
