// Package omega implements atomic, offline authority initialization for soph.
package omega

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
	"sopholeth/internal/trust/bootstrap"
)

const rootLifetime = 365 * 24 * time.Hour

var networkID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
var keyNames = []string{"root-1", "root-2", "root-3", "targets", "snapshot", "timestamp"}
var authorityFiles = []string{"authority.json", "1.root.json", "bundle.json", "complete.json"}

type InitOptions struct {
	Home       string
	Network    string
	Repository string
	Disposable bool
}

// This private transaction record is durable before signing. All recovery
// derives from these same keys, never fresh randomness after that boundary.
// Schema 1 is disposable-only; production custody requires a new schema.
type authority struct {
	Schema     int               `json:"schema"`
	Mode       string            `json:"mode"`
	Network    string            `json:"network"`
	Repository string            `json:"repository"`
	Created    time.Time         `json:"created"`
	Expires    time.Time         `json:"root_expires"`
	Keys       map[string]string `json:"private_keys"`
}

type completion struct {
	Schema      int               `json:"schema"`
	Fingerprint string            `json:"fingerprint"`
	Files       map[string]string `json:"sha256"`
}

type RoleStatus struct {
	Threshold int      `json:"threshold"`
	KeyIDs    []string `json:"key_ids"`
}

// Report contains public information only. Local initialization cannot report
// publication health; that requires a separate verified publication workflow.
type Report struct {
	Schema          int                   `json:"schema"`
	State           string                `json:"state"`
	Mode            string                `json:"mode,omitempty"`
	Network         string                `json:"network,omitempty"`
	Repository      string                `json:"repository,omitempty"`
	Fingerprint     string                `json:"fingerprint,omitempty"`
	RootVersion     int64                 `json:"root_version,omitempty"`
	RootExpires     time.Time             `json:"root_expires,omitzero"`
	Roles           map[string]RoleStatus `json:"roles,omitempty"`
	Publication     string                `json:"publication"`
	Problem         string                `json:"problem,omitempty"`
	Action          string                `json:"action"`
	Release         *PublicationReport    `json:"release,omitempty"`
	OperationalHome string                `json:"operational_home,omitempty"`
	Rotation        *RotationReport       `json:"rotation,omitempty"`
}

func Init(ctx context.Context, opts InitOptions) (Report, error) {
	return initialize(ctx, opts, time.Now().UTC().Truncate(time.Second), nil)
}

func initialize(ctx context.Context, opts InitOptions, now time.Time, hook func(string) error) (Report, error) {
	if !opts.Disposable {
		return Report{}, errors.New("omega: --disposable is required until production custody and recovery are implemented")
	}
	if !networkID.MatchString(opts.Network) {
		return Report{}, errors.New("omega: network must be 1–64 ASCII letters, digits, underscores or hyphens, starting with a letter or digit")
	}
	repository, err := bootstrap.ValidateLocation(opts.Network, opts.Repository)
	if err != nil {
		return Report{}, err
	}
	opts.Repository = repository
	home, err := openHome(ctx, opts.Home, opts.Network, true)
	if err != nil {
		return Report{}, err
	}
	defer home.close()
	home.hook = hook
	// Idempotence is by network, not by invocation. Never create new material
	// when an authority slot exists, including damaged or expired authorities.
	if current, err := home.subdir(opts.Network); err == nil {
		defer current.close()
		report, err := current.inspect(now)
		if err != nil {
			return report, err
		}
		if report.Network != opts.Network || report.Repository != repository {
			err := errors.New("omega: network already has an authority with different configuration")
			report.Problem = err.Error()
			report.Action = "Check --network and --repository against the existing authority; do not replace its material."
			return report, err
		}
		// A prior caller may have died after rename but before the parent fsync.
		if err := home.syncDir(); err != nil {
			return Report{}, err
		}
		return report, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Report{}, err
	}
	if err := rejectOrphanedOperationalState(home, opts.Network); err != nil {
		return failedReport(err), err
	}
	stageName := "." + opts.Network + ".pending"
	if err := home.root.Mkdir(stageName, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return Report{}, err
	}
	if err := home.syncDir(); err != nil {
		return Report{}, err
	}
	stage, err := home.subdir(stageName)
	if err != nil {
		return Report{}, err
	}
	defer stage.close()
	if err := stage.checkEntries(true); err != nil {
		return Report{}, err
	}
	planned, err := stage.prepareAuthority(opts, now)
	if err != nil {
		return Report{}, err
	}
	if !now.Before(planned.Expires) {
		return Report{}, errors.New("omega: staged authority has expired; preserve its keys for explicit recovery, never initialize a replacement")
	}
	outputs, err := buildOutputs(planned)
	if err != nil {
		return Report{}, err
	}
	for _, name := range []string{"1.root.json", "bundle.json"} {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		if err := stage.install(name, outputs[name]); err != nil {
			return Report{}, err
		}
	}
	receipt, _, err := stage.verify()
	if err != nil {
		return Report{}, err
	}
	if err := stage.install("complete.json", record(receipt)); err != nil {
		return Report{}, err
	}
	if err := stage.cleanPending(); err != nil {
		return Report{}, err
	}
	report, err := stage.inspect(now)
	if err != nil {
		return Report{}, err
	}
	if err := stage.phase("verified"); err != nil {
		return Report{}, err
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	// Every byte and the staging directory are durable and verified. Publish
	// the directory atomically, without replacing any existing network slot.
	if err := home.promote(stageName, opts.Network); err != nil {
		return Report{}, err
	}
	if err := home.phase("promoted"); err != nil {
		return Report{}, err
	}
	if err := home.syncDir(); err != nil {
		return Report{}, fmt.Errorf("omega: authority is complete but commit durability is unconfirmed; rerun the same init command: %w", err)
	}
	if err := home.phase("committed"); err != nil {
		return Report{}, err
	}
	return report, nil
}

func (s *store) prepareAuthority(opts InitOptions, now time.Time) (authority, error) {
	var planned authority
	data, err := s.read("authority.json")
	if errors.Is(err, os.ErrNotExist) {
		// No signing or public output can precede the durable private record.
		for _, name := range authorityFiles[1:] {
			for _, candidate := range []string{name, name + ".pending"} {
				if _, err := s.root.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
					return planned, errors.New("omega: private transaction record is missing from established staging; restore recovery material")
				}
			}
		}
		pending, pendingErr := s.read("authority.json.pending")
		if pendingErr != nil && !errors.Is(pendingErr, os.ErrNotExist) {
			return planned, pendingErr
		}
		// A fully written candidate is recovered byte-for-byte. A torn first write
		// has never authorized anything: it may be discarded before any signing.
		if pendingErr == nil && json.Valid(pending) {
			if err := decodeRecord(pending, &planned); err != nil {
				return authority{}, errors.New("omega: invalid pending authority record; preserve it for recovery")
			}
			if err := planned.validate(); err != nil {
				return authority{}, err
			}
			data = pending
		} else {
			planned = authority{Schema: 1, Mode: "disposable", Network: opts.Network, Repository: opts.Repository, Created: now, Expires: now.Add(rootLifetime), Keys: map[string]string{}}
			for _, name := range keyNames {
				_, key, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					return planned, err
				}
				planned.Keys[name] = base64.StdEncoding.EncodeToString(key)
			}
			data = record(planned)
		}
	} else if err != nil {
		return planned, err
	}
	if err := decodeRecord(data, &planned); err != nil {
		return authority{}, errors.New("omega: invalid private authority record; restore recovery material")
	}
	if err := planned.validate(); err != nil {
		return authority{}, err
	}
	if planned.Network != opts.Network || planned.Repository != opts.Repository {
		return authority{}, errors.New("omega: initialization already began with different configuration; preserve its staging directory")
	}
	if err := s.install("authority.json", data); err != nil {
		return authority{}, err
	}
	// A restart after link must also confirm directory durability before signing.
	if err := s.syncDir(); err != nil {
		return authority{}, err
	}
	return planned, nil
}

func Status(ctx context.Context, homePath, network string) (Report, error) {
	return status(ctx, homePath, network, false, nil)
}

func VerifyPublication(ctx context.Context, homePath, network string, httpClient *http.Client) (Report, error) {
	return status(ctx, homePath, network, true, httpClient)
}

func status(ctx context.Context, homePath, network string, verify bool, httpClient *http.Client) (Report, error) {
	if !networkID.MatchString(network) {
		err := errors.New("omega: invalid network identity")
		return failedReport(err), err
	}
	home, err := openHome(ctx, homePath, network, false)
	if errors.Is(err, os.ErrNotExist) {
		return absentReport(network)
	}
	if err != nil {
		return failedReport(err), err
	}
	defer home.close()
	if c, err := readRenewal(home, network); err == nil {
		report, err := inspectBundle(c.Bundle, time.Now().UTC())
		report.OperationalHome = home.root.Name()
		if err != nil {
			return report, err
		}
		report.State = "renewal_ready"
		return inspectPublication(ctx, home, c.Bundle, report, verify, httpClient)
	} else if !errors.Is(err, os.ErrNotExist) {
		return failedReport(err), err
	}
	current, err := home.subdir(network)
	if errors.Is(err, os.ErrNotExist) {
		if err := rejectOrphanedOperationalState(home, network); err != nil {
			return failedReport(err), err
		}
		report, err := absentReport(network)
		if _, stageErr := home.root.Lstat("." + network + ".pending"); stageErr == nil {
			report.State = "pending"
			report.Action = "Rerun the same soph omega init command to recover and finish the pending transaction."
		} else if !errors.Is(stageErr, os.ErrNotExist) {
			return failedReport(stageErr), stageErr
		}
		return report, err
	}
	if err != nil {
		return failedReport(err), err
	}
	defer current.close()
	report, err := current.inspect(time.Now().UTC())
	if err == nil && report.Network != network {
		err = errors.New("omega: authority belongs to a different network")
		return failedReport(err), err
	}
	if err != nil {
		return report, err
	}
	_, bundle, err := current.verify()
	if err != nil {
		return report, err
	}
	operational, cleanup, err := operationalHome(ctx, home, bundle)
	if err != nil {
		return publicationFailure(report, err)
	}
	defer cleanup()
	report.OperationalHome = operational.root.Name()
	return inspectPublication(ctx, operational, bundle, report, verify, httpClient)
}

func absentReport(network string) (Report, error) {
	err := errors.New("omega: no committed authority for this network")
	return Report{Schema: 1, Network: network, State: "absent", Publication: "not_checked", Problem: err.Error(), Action: "Initialize this network with soph omega init."}, err
}

func failedReport(err error) Report {
	return Report{Schema: 1, State: "invalid", Publication: "not_checked", Problem: err.Error(), Action: "Preserve authority material and restore verified recovery material; never initialize a replacement."}
}

func (s *store) inspect(now time.Time) (Report, error) {
	if err := s.checkEntries(false); err != nil {
		return failedReport(err), err
	}
	expected, bundle, err := s.verify()
	if err != nil {
		return failedReport(err), err
	}
	done, err := s.read("complete.json")
	if err != nil {
		return failedReport(err), err
	}
	var stored completion
	if err := decodeRecord(done, &stored); err != nil || !bytes.Equal(record(expected), record(stored)) {
		err := errors.New("omega: completion receipt does not match authority material")
		return failedReport(err), err
	}
	return inspectBundle(bundle, now)
}

func inspectBundle(bundle bootstrap.Bundle, now time.Time) (Report, error) {
	root, err := metadata.Root().FromBytes(bundle.Root)
	if err != nil {
		return failedReport(err), err
	}
	report := Report{Schema: 1, State: "initialized", Mode: "disposable", Network: bundle.Network, Repository: bundle.Repository,
		Fingerprint: bundle.Fingerprint(), RootVersion: root.Signed.Version, RootExpires: root.Signed.Expires,
		Publication: "not_checked", Roles: map[string]RoleStatus{}, Action: "Back up the private custody home; use soph omega publish for a disposable repository."}
	for _, name := range metadata.TOP_LEVEL_ROLE_NAMES {
		role := root.Signed.Roles[name]
		report.Roles[name] = RoleStatus{Threshold: role.Threshold, KeyIDs: role.KeyIDs}
	}
	if !now.Before(root.Signed.Expires) {
		report.State = "expired"
		report.Action = "Root approval has expired; preserve the existing authority for a root ceremony."
		err := errors.New("omega: root metadata has expired")
		report.Problem = err.Error()
		return report, err
	}
	return report, nil
}

func (p authority) validate() error {
	repository, err := bootstrap.ValidateLocation(p.Network, p.Repository)
	if err != nil {
		return err
	}
	if p.Schema != 1 || p.Mode != "disposable" || !networkID.MatchString(p.Network) || repository != p.Repository || p.Created.IsZero() || !p.Expires.Equal(p.Created.Add(rootLifetime)) || len(p.Keys) != len(keyNames) {
		return errors.New("omega: invalid authority record")
	}
	seen := map[string]bool{}
	for _, name := range keyNames {
		key, err := decodeKey(p.Keys[name])
		if err != nil {
			return fmt.Errorf("omega: invalid %s key in private authority record", name)
		}
		public := string(key[ed25519.SeedSize:])
		if seen[public] {
			return errors.New("omega: signing roles require six distinct keys")
		}
		seen[public] = true
	}
	return nil
}

func decodeKey(encoded string) (ed25519.PrivateKey, error) {
	key, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(key) != ed25519.PrivateKeySize || base64.StdEncoding.EncodeToString(key) != encoded {
		return nil, errors.New("invalid private key encoding")
	}
	if !bytes.Equal(key, ed25519.NewKeyFromSeed(key[:ed25519.SeedSize])) {
		return nil, errors.New("inconsistent Ed25519 private/public key")
	}
	return ed25519.PrivateKey(key), nil
}

func buildOutputs(planned authority) (map[string][]byte, error) {
	if err := planned.validate(); err != nil {
		return nil, err
	}
	root := metadata.Root(planned.Expires)
	root.Signed.ConsistentSnapshot = true
	root.Signed.Roles[metadata.ROOT].Threshold = 2
	for _, name := range keyNames {
		private, _ := decodeKey(planned.Keys[name])
		key, err := metadata.KeyFromPublicKey(private.Public())
		if err != nil {
			return nil, err
		}
		role := name
		if len(name) > 4 && name[:4] == "root" {
			role = metadata.ROOT
		}
		if err := root.Signed.AddKey(key, role); err != nil {
			return nil, err
		}
	}
	for _, name := range keyNames[:2] {
		private, _ := decodeKey(planned.Keys[name])
		signer, err := signature.LoadSigner(private, crypto.Hash(0))
		if err != nil {
			return nil, err
		}
		if _, err := root.Sign(signer); err != nil {
			return nil, err
		}
	}
	raw, err := root.ToBytes(false)
	if err != nil {
		return nil, err
	}
	bundle := bootstrap.Bundle{Schema: 1, Network: planned.Network, Repository: planned.Repository, Root: raw}
	encoded := record(bundle)
	if _, err := bootstrap.ParseBundle(encoded); err != nil {
		return nil, fmt.Errorf("omega: verify emitted bundle: %w", err)
	}
	return map[string][]byte{"1.root.json": raw, "bundle.json": encoded}, nil
}

func (s *store) verify() (completion, bootstrap.Bundle, error) {
	data, err := s.read("authority.json")
	if err != nil {
		return completion{}, bootstrap.Bundle{}, err
	}
	var planned authority
	if err := decodeRecord(data, &planned); err != nil {
		return completion{}, bootstrap.Bundle{}, errors.New("omega: invalid private authority record")
	}
	if err := planned.validate(); err != nil {
		return completion{}, bootstrap.Bundle{}, err
	}
	files := map[string][]byte{"authority.json": data}
	for _, name := range []string{"1.root.json", "bundle.json"} {
		actual, err := s.read(name)
		if err != nil {
			return completion{}, bootstrap.Bundle{}, err
		}
		files[name] = actual
	}
	bundle, err := bootstrap.ParseBundle(files["bundle.json"])
	if err != nil {
		return completion{}, bundle, err
	}
	if err := verifyAuthorityRoot(planned, bundle, files["1.root.json"]); err != nil {
		return completion{}, bundle, err
	}
	receipt := completion{Schema: 1, Fingerprint: bundle.Fingerprint(), Files: map[string]string{}}
	for name, data := range files {
		receipt.Files[name] = fmt.Sprintf("%x", sha256.Sum256(data))
	}
	return receipt, bundle, nil
}

// Inspect existing signatures and role assignments. Status never signs new
// metadata or compares against a root regenerated with library defaults.
func verifyAuthorityRoot(planned authority, bundle bootstrap.Bundle, raw []byte) error {
	if bundle.Network != planned.Network || bundle.Repository != planned.Repository || !bytes.Equal(raw, bundle.Root) {
		return errors.New("omega: public bundle does not match the authority or initial root")
	}
	root, err := metadata.Root().FromBytes(raw)
	if err != nil {
		return err
	}
	if root.Signed.Version != 1 || !root.Signed.Expires.Equal(planned.Expires) || len(root.Signed.Keys) != 6 || len(root.Signed.Roles) != 4 {
		return errors.New("omega: initial root policy does not match the authority")
	}
	for _, roleName := range metadata.TOP_LEVEL_ROLE_NAMES {
		names, threshold := []string{roleName}, 1
		if roleName == metadata.ROOT {
			names, threshold = keyNames[:3], 2
		}
		role := root.Signed.Roles[roleName]
		if role == nil || role.Threshold != threshold || len(role.KeyIDs) != len(names) {
			return errors.New("omega: root role quorum does not match the authority")
		}
		expected := map[string]bool{}
		for _, name := range names {
			private, _ := decodeKey(planned.Keys[name])
			expected[string(private[ed25519.SeedSize:])] = true
		}
		for _, id := range role.KeyIDs {
			key := root.Signed.Keys[id]
			if key == nil {
				return errors.New("omega: missing root role key")
			}
			public, err := key.ToPublicKey()
			if err != nil {
				return err
			}
			edKey, ok := public.(ed25519.PublicKey)
			if !ok || !expected[string(edKey)] {
				return errors.New("omega: root role key does not match private authority material")
			}
			delete(expected, string(edKey))
		}
	}
	return nil
}

func (s *store) checkEntries(pending bool) error {
	dir, err := s.root.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := dir.ReadDir(-1)
	err = errors.Join(readErr, dir.Close())
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, name := range authorityFiles {
		allowed[name] = true
		if pending {
			allowed[name+".pending"] = true
		}
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return fmt.Errorf("omega: unexpected authority entry %q", entry.Name())
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if err := safeFile(info); err != nil {
			return fmt.Errorf("%s: %w", entry.Name(), err)
		}
	}
	return nil
}

func record(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	} // Fixed serializable record types only.
	return append(data, '\n')
}

// Immutable canonical records reject unknown/duplicate fields and edits.
func decodeRecord(data []byte, value any) error {
	if err := json.Unmarshal(data, value); err != nil {
		return errors.New("invalid operator record")
	}
	if !bytes.Equal(data, record(value)) {
		return errors.New("noncanonical operator record")
	}
	return nil
}
