package trust

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// Default DNS names for the public network. Overridable for tests and for
// future spec revisions that migrate the records.
const (
	DefaultBootstrapName  = "_bootstrap.sopholeth.io"
	DefaultSignedListName = "_omega.sopholeth.io"
)

// TXTResolver is the minimal interface this package needs from a DNS
// resolver. net.Resolver satisfies it. Tests use an in-memory
// implementation to avoid network dependencies.
type TXTResolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// DNSConfig is a bundle of knobs for the bootstrap DNS lookup. Zero values
// select sensible defaults: the standard net.Resolver and the public-network
// bootstrap name from the 2.1 spec.
type DNSConfig struct {
	Resolver      TXTResolver
	BootstrapName string
}

func (c DNSConfig) resolver() TXTResolver {
	if c.Resolver != nil {
		return c.Resolver
	}
	return net.DefaultResolver
}

func (c DNSConfig) bootstrapName() string {
	if c.BootstrapName != "" {
		return c.BootstrapName
	}
	return DefaultBootstrapName
}

var (
	errNoBootstrapRecord = errors.New("trust: bootstrap TXT record has no omega= entry")
	errEmptyTXT          = errors.New("trust: TXT lookup returned no usable records")
)

// FetchSigned resolves the signed root list over DNS and verifies it against
// pubkey. It performs two lookups: first DefaultBootstrapName (or whatever
// cfg.BootstrapName overrides it to) for an `omega=<target>` indirection,
// then the target for the actual signed record.
//
// On success, returns a verified list — callers may trust list.Nodes
// directly. On any failure (DNS, parse, version, expiration, signature),
// returns a descriptive error and no list.
//
// Note on multi-string TXT records: DNS TXT records can be split across
// multiple 255-byte "character-strings" on the wire. Go's net.Resolver
// already concatenates those into one string per record before returning,
// so each element of the []string LookupTXT returns is already a complete
// record.
func FetchSigned(ctx context.Context, cfg DNSConfig, pubkey ed25519.PublicKey, now time.Time) (*SignedList, error) {
	if err := validateOmegaPubkey(pubkey); err != nil {
		return nil, err
	}
	r := cfg.resolver()
	bootstrapName := cfg.bootstrapName()

	indirection, err := lookupSingleTXT(ctx, r, bootstrapName)
	if err != nil {
		return nil, fmt.Errorf("lookup %s: %w", bootstrapName, err)
	}
	target, err := parseBootstrapIndirection(indirection)
	if err != nil {
		return nil, err
	}

	signedRaw, err := lookupSingleTXT(ctx, r, target)
	if err != nil {
		return nil, fmt.Errorf("lookup %s: %w", target, err)
	}

	list, err := Parse(signedRaw)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", target, err)
	}
	if err := list.Verify(pubkey, now); err != nil {
		return nil, fmt.Errorf("verify %s: %w", target, err)
	}
	return list, nil
}

// lookupSingleTXT returns the first non-empty TXT record for name. Each
// element of the []string returned by net.Resolver.LookupTXT is already a
// complete record (the stdlib concatenates multi-string splits before
// returning). Multiple records under the same name are unusual for our use
// case; we take the first to keep behavior deterministic.
func lookupSingleTXT(ctx context.Context, r TXTResolver, name string) (string, error) {
	records, err := r.LookupTXT(ctx, name)
	if err != nil {
		return "", err
	}
	for _, rec := range records {
		rec = strings.TrimSpace(rec)
		if rec != "" {
			return rec, nil
		}
	}
	return "", errEmptyTXT
}

// parseBootstrapIndirection parses a bootstrap TXT value of the
// form `omega=<target-name>`. The indirection layer exists so future spec
// revisions can migrate the signed-record hostname without changing the
// baked-in name.
func parseBootstrapIndirection(raw string) (string, error) {
	for _, field := range strings.Split(raw, fieldSeparator) {
		key, value, ok := strings.Cut(strings.TrimSpace(field), kvSeparator)
		if !ok {
			continue
		}
		if strings.TrimSpace(key) == "omega" {
			target := strings.TrimSpace(value)
			if target == "" {
				return "", errNoBootstrapRecord
			}
			return target, nil
		}
	}
	return "", errNoBootstrapRecord
}
