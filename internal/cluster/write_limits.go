package cluster

import (
	"errors"
	"fmt"
	"math"
	"time"
)

const MinTTLSeconds = 300
const DefaultMaxTTLSeconds = 86400

var ErrValueTooLarge = errors.New("value too large")
var ErrKeyTooLong = errors.New("key too long")

// WriteLimits is a node's admission policy, not a protocol size limit. Peers
// with different policies may reject each other's writes. Configure before Start.
type WriteLimits struct {
	MaxValueBytes int
	MaxKeyBytes   int
	MinTTLSeconds int
	MaxTTLSeconds int
}

func DefaultWriteLimits() WriteLimits {
	return WriteLimits{MaxValueBytes: 100 * 1024, MaxKeyBytes: 1024, MinTTLSeconds: MinTTLSeconds, MaxTTLSeconds: DefaultMaxTTLSeconds}
}

func (cn *ClusterNode) SetWriteLimits(limits WriteLimits) error {
	if limits.MaxValueBytes <= 0 || limits.MaxKeyBytes <= 0 {
		return errors.New("NODE_MAX_VALUE_BYTES and NODE_MAX_KEY_BYTES must be positive")
	}
	// Normalize the minimum to the protocol floor. The maximum must fit the
	// signed 32-bit seconds field in the gossip wire format.
	limits.MinTTLSeconds = max(limits.MinTTLSeconds, MinTTLSeconds)
	if limits.MaxTTLSeconds < limits.MinTTLSeconds || int64(limits.MaxTTLSeconds) > math.MaxInt32 {
		return fmt.Errorf("TTL bounds require %d <= NODE_MIN_TTL <= NODE_MAX_TTL <= %d seconds", MinTTLSeconds, math.MaxInt32)
	}
	cn.writeLimits = limits
	return nil
}

func (cn *ClusterNode) WriteLimits() WriteLimits { return cn.writeLimits }

// ValidateKey is also used before reading a client request's body.
func (cn *ClusterNode) ValidateKey(key string) error {
	if len(key) > cn.writeLimits.MaxKeyBytes {
		return fmt.Errorf("%w: limit is %d bytes", ErrKeyTooLong, cn.writeLimits.MaxKeyBytes)
	}
	return nil
}

func (cn *ClusterNode) validateWrite(key string, data []byte) error {
	if err := cn.ValidateKey(key); err != nil {
		return err
	}
	if len(data) > cn.writeLimits.MaxValueBytes {
		return fmt.Errorf("%w: limit is %d bytes", ErrValueTooLarge, cn.writeLimits.MaxValueBytes)
	}
	return nil
}

// Clamp integer seconds before multiplying into a Duration, including peer
// values that would overflow if converted first. All accepted TTLs are seconds.
func (cn *ClusterNode) clampTTL(seconds int64) time.Duration {
	seconds = min(max(seconds, int64(cn.writeLimits.MinTTLSeconds)), int64(cn.writeLimits.MaxTTLSeconds))
	return time.Duration(seconds) * time.Second
}
