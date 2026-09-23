// Package bootstrap implements authenticated HTTPS bootstrap discovery.
// It is the TUF integration layer for the forthcoming soph omega workflow;
// callers must explicitly supply a public trust bundle and private state path.
package bootstrap

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/theupdateframework/go-tuf/v2/metadata"
	"github.com/theupdateframework/go-tuf/v2/metadata/trustedmetadata"
)

const (
	manifestLimit = 64 << 10
	metadataLimit = 512 << 10
	stateLimit    = 8 << 20
)

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// Bundle contains only public material. Its initial root must arrive through
// an authenticated release or an independently verified operator handoff.
type Bundle struct {
	Schema     int             `json:"schema"`
	Network    string          `json:"network"`
	Repository string          `json:"repository"`
	Root       json.RawMessage `json:"root"`
}

func ParseBundle(data []byte) (Bundle, error) {
	var bundle Bundle
	if len(data) > metadataLimit+4096 {
		return bundle, errors.New("bootstrap: trust bundle is too large")
	}
	if err := strictJSON(data, &bundle); err != nil {
		return bundle, err
	}
	return bundle, bundle.Validate()
}

func (b Bundle) Validate() error {
	if b.Schema != 1 {
		return errors.New("bootstrap: invalid bundle schema or network identity")
	}
	if _, err := ValidateLocation(b.Network, b.Repository); err != nil {
		return err
	}
	return validateRoot(b.Root)
}

// ValidateLocation checks the network identity and normalizes the repository
// before an operator creates keys. Metadata may live at an HTTPS base path;
// node API endpoints remain origins only.
func ValidateLocation(network, repository string) (string, error) {
	if !identifier.MatchString(network) {
		return "", errors.New("bootstrap: invalid network identity")
	}
	location, err := httpsRepository(repository)
	if err != nil {
		return "", fmt.Errorf("bootstrap: repository: %w", err)
	}
	return location, nil
}

// Fingerprint identifies the library's normalized serialization of the
// initial root, so formatting the enclosing bundle cannot change its identity.
// Invalid roots have no fingerprint; callers must Validate before trusting it.
func (b Bundle) Fingerprint() string {
	root, err := metadata.Root().FromBytes(b.Root)
	if err != nil {
		return ""
	}
	data, err := root.ToBytes(false)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// Root identifies a bootstrap node and its authenticated API origin. Gossip
// endpoint authorization is a separate transport integration requirement.
type Root struct {
	ID     string `json:"id"`
	Origin string `json:"origin"`
}

type Manifest struct {
	Schema  int    `json:"schema"`
	Network string `json:"network"`
	Enclave string `json:"enclave"`
	Roots   []Root `json:"roots"`
}

func ParseManifest(data []byte, network string) (Manifest, error) {
	var m Manifest
	if len(data) > manifestLimit {
		return m, errors.New("bootstrap: manifest is too large")
	}
	if err := strictJSON(data, &m); err != nil {
		return m, err
	}
	if m.Schema != 1 || !identifier.MatchString(m.Network) || m.Network != network || m.Enclave != "default" {
		return m, errors.New("bootstrap: unsupported manifest schema, network, or enclave")
	}
	if len(m.Roots) == 0 || len(m.Roots) > 16 {
		return m, errors.New("bootstrap: manifest must contain between 1 and 16 roots")
	}
	ids, origins := map[string]bool{}, map[string]bool{}
	for i, root := range m.Roots {
		origin, err := httpsOrigin(root.Origin)
		if err != nil || !identifier.MatchString(root.ID) || ids[root.ID] || origins[origin] {
			return m, fmt.Errorf("bootstrap: invalid or duplicate root %q", root.ID)
		}
		ids[root.ID], origins[origin] = true, true
		m.Roots[i].Origin = origin
	}
	return m, nil
}

// httpsRepository permits only literal, unreserved path segments. Reject rather
// than clean dot segments, repeated separators, or escaped characters: proxies
// and clients must agree on the exact directory that bounds every download.
// Store no trailing slash, preserving existing origin-only authority bindings.
func httpsRepository(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || strings.ContainsAny(raw, "?#\\%") {
		return "", errors.New("expected an HTTPS repository without credentials, query, fragment, or escapes")
	}
	origin, err := httpsOrigin(u.Scheme + "://" + u.Host)
	if err != nil {
		return "", err
	}
	path := strings.TrimSuffix(u.Path, "/")
	if path != "" {
		if !strings.HasPrefix(path, "/") {
			return "", errors.New("repository path must be absolute")
		}
		for _, segment := range strings.Split(path[1:], "/") {
			if segment == "" || segment == "." || segment == ".." {
				return "", errors.New("repository path must have nonempty, non-traversing segments")
			}
			for _, ch := range segment {
				if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || strings.ContainsRune("-._~", ch)) {
					return "", errors.New("repository path must use ASCII letters, digits, or -._~")
				}
			}
		}
	}
	return origin + path, nil
}

// httpsOrigin rejects URL ambiguity and normalizes equivalent origins for
// duplicate detection. Private addresses are permitted for explicit rehearsal
// bundles; the caller cannot silently substitute a network or trust bundle.
func httpsOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" ||
		(u.Path != "" && u.Path != "/") || u.RawPath != "" || strings.ContainsAny(raw, "?#\\") {
		return "", errors.New("expected an HTTPS origin without credentials, query, fragment, or path")
	}
	host := strings.ToLower(u.Hostname())
	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.Zone() != "" {
			return "", errors.New("scoped addresses are not supported")
		}
		host = addr.String()
	} else {
		if len(host) > 253 {
			return "", errors.New("hostname is too long")
		}
		for _, label := range strings.Split(host, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return "", errors.New("invalid hostname")
			}
			for _, ch := range label {
				if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
					return "", errors.New("hostname must use ASCII DNS labels or an IP address")
				}
			}
		}
	}
	port := u.Port()
	if strings.HasSuffix(u.Host, ":") {
		return "", errors.New("empty port")
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", errors.New("invalid port")
		}
		port = strconv.Itoa(n)
		if port == "443" {
			port = ""
		}
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return "https://" + host, nil
}

func validateRoot(data []byte) error {
	if len(data) == 0 || len(data) > metadataLimit {
		return errors.New("bootstrap: root bundle is unset or too large")
	}
	r, err := metadata.Root().FromBytes(data)
	if err != nil {
		return fmt.Errorf("bootstrap: parse root: %w", err)
	}
	if r.Signed.Version < 1 || r.Signed.Expires.IsZero() || !r.Signed.ConsistentSnapshot {
		return errors.New("bootstrap: root requires a positive version, expiry, and consistent snapshots")
	}
	for _, name := range metadata.TOP_LEVEL_ROLE_NAMES {
		role := r.Signed.Roles[name]
		if role == nil || role.Threshold < 1 || role.Threshold > len(role.KeyIDs) {
			return fmt.Errorf("bootstrap: invalid %s role", name)
		}
		for _, id := range role.KeyIDs {
			key := r.Signed.Keys[id]
			if key == nil || key.Type != metadata.KeyTypeEd25519 || key.Scheme != metadata.KeySchemeEd25519 {
				return errors.New("bootstrap: roles require Ed25519 keys")
			}
			_, err := key.ToPublicKey()
			if err != nil {
				return err
			}
			// Key parsing validates length. Explicitly reject the retired zero
			// anchor even if a future verification implementation accepts it.
			if key.Value.PublicKey == strings.Repeat("0", 64) {
				return errors.New("bootstrap: zero trust anchor is forbidden")
			}
		}
	}
	_, err = trustedmetadata.New(data)
	return err
}

// strictJSON rejects duplicate fields as well as unknown fields and trailing
// values. This applies to our schemas, not to TUF's extensible metadata schema.
func strictJSON(data []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	var value func(int) error
	value = func(depth int) error {
		if depth > 32 {
			return errors.New("JSON nesting limit exceeded")
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		if delim, ok := token.(json.Delim); ok {
			seen := map[string]bool{}
			for d.More() {
				if delim == '{' {
					field, err := d.Token()
					if err != nil {
						return err
					}
					name, ok := field.(string)
					if !ok || seen[name] {
						return errors.New("duplicate or invalid JSON field")
					}
					seen[name] = true
				}
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := value(0); err != nil {
		return fmt.Errorf("bootstrap: invalid JSON: %w", err)
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("bootstrap: trailing JSON data")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(dst)
}
