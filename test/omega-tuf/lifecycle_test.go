// Package omegatuf is an isolated, disposable-authority integration spike.
// None of this fixture is linked into Sopholeth binaries.
package omegatuf

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
	"github.com/theupdateframework/go-tuf/v2/metadata/config"
	"github.com/theupdateframework/go-tuf/v2/metadata/updater"
)

var epoch = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

// Opaque test content, not the proposed production manifest schema.
var manifest = []byte(`{"network":"disposable","enclave":"default","roots":["https://one.example.invalid","https://two.example.invalid","https://three.example.invalid"]}`)

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func keys(t *testing.T, count int) []ed25519.PrivateKey {
	t.Helper()
	result := make([]ed25519.PrivateKey, count)
	for i := range result {
		_, key, err := ed25519.GenerateKey(rand.Reader)
		check(t, err)
		result[i] = key
	}
	return result
}

func signed[T metadata.Roles](t *testing.T, m *metadata.Metadata[T], keys ...ed25519.PrivateKey) []byte {
	t.Helper()
	m.ClearSignatures()
	for _, key := range keys {
		signer, err := signature.LoadSigner(key, crypto.Hash(0))
		check(t, err)
		_, err = m.Sign(signer)
		check(t, err)
	}
	data, err := m.ToBytes(false)
	check(t, err)
	return data
}

func metaFile(version int64, data []byte) *metadata.MetaFiles {
	hash := sha256.Sum256(data)
	return &metadata.MetaFiles{Version: version, Length: int64(len(data)), Hashes: metadata.Hashes{"sha256": hash[:]}}
}

type repository struct {
	t                    *testing.T
	mu                   sync.RWMutex
	files                map[string][]byte
	server               *httptest.Server
	roleKeys             map[string][]ed25519.PrivateKey
	root                 *metadata.Metadata[metadata.RootType]
	initialRoot, targets []byte
	targetPath           string
	targetVersion        int64
	onlineVersion        int64
}

func newRepository(t *testing.T) *repository {
	t.Helper()
	r := &repository{t: t, files: map[string][]byte{}, roleKeys: map[string][]ed25519.PrivateKey{}}
	r.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.RLock()
		data, ok := r.files[req.URL.Path]
		r.mu.RUnlock()
		if !ok {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(r.server.Close)
	r.root = metadata.Root(epoch.Add(365 * 24 * time.Hour))
	r.root.Signed.ConsistentSnapshot = true
	for _, role := range metadata.TOP_LEVEL_ROLE_NAMES {
		count := 1
		if role == metadata.ROOT {
			count = 3
		}
		r.setRole(role, keys(t, count))
	}
	r.initialRoot = signed(t, r.root, r.roleKeys[metadata.ROOT][:2]...)
	r.put("/1.root.json", r.initialRoot)
	r.approve(manifest, epoch.Add(90*24*time.Hour))
	r.renew(epoch)
	return r
}

func (r *repository) put(path string, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if data == nil {
		delete(r.files, path)
	} else {
		r.files[path] = bytes.Clone(data)
	}
}

func (r *repository) get(path string) []byte {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return bytes.Clone(r.files[path])
}

func (r *repository) setRole(role string, newKeys []ed25519.PrivateKey) {
	r.root.Signed.Roles[role] = &metadata.Role{Threshold: 1}
	if role == metadata.ROOT {
		r.root.Signed.Roles[role].Threshold = 2
	}
	for _, private := range newKeys {
		key, err := metadata.KeyFromPublicKey(private.Public())
		check(r.t, err)
		check(r.t, r.root.Signed.AddKey(key, role))
	}
	r.roleKeys[role] = newKeys
}

// approve requires the membership key. renew only requires the two online keys.
func (r *repository) approve(data []byte, expires time.Time) {
	r.targetVersion++
	target, err := metadata.TargetFile().FromBytes("bootstrap.json", data, "sha256")
	check(r.t, err)
	targets := metadata.Targets(expires)
	targets.Signed.Version = r.targetVersion
	targets.Signed.Targets["bootstrap.json"] = target
	r.targets = signed(r.t, targets, r.roleKeys[metadata.TARGETS]...)
	r.targetPath = fmt.Sprintf("/targets/%x.bootstrap.json", []byte(target.Hashes["sha256"]))
	r.put(r.targetPath, data)
	r.put(fmt.Sprintf("/%d.targets.json", r.targetVersion), r.targets)
}

func (r *repository) renew(now time.Time) {
	r.onlineVersion++
	snapshot := metadata.Snapshot(now.Add(7 * 24 * time.Hour))
	snapshot.Signed.Version = r.onlineVersion
	snapshot.Signed.Meta["targets.json"] = metaFile(r.targetVersion, r.targets)
	snapshotBytes := signed(r.t, snapshot, r.roleKeys[metadata.SNAPSHOT]...)
	r.put(fmt.Sprintf("/%d.snapshot.json", r.onlineVersion), snapshotBytes)
	timestamp := metadata.Timestamp(now.Add(24 * time.Hour))
	timestamp.Signed.Version = r.onlineVersion
	timestamp.Signed.Meta["snapshot.json"] = metaFile(r.onlineVersion, snapshotBytes)
	// Mutable timestamp is always published last.
	r.put("/timestamp.json", signed(r.t, timestamp, r.roleKeys[metadata.TIMESTAMP]...))
}

// rotate builds one numbered transition; callers choose its signatures so
// negative cases exercise the actual library's old-and-new threshold checks.
func (r *repository) rotate() (oldQuorum, newQuorum []ed25519.PrivateKey) {
	oldQuorum = r.roleKeys[metadata.ROOT][:2]
	r.root.Signed.Version++
	r.root.Signed.Expires = epoch.Add(2 * 365 * 24 * time.Hour)
	r.root.Signed.Keys = map[string]*metadata.Key{}
	for _, role := range metadata.TOP_LEVEL_ROLE_NAMES {
		count := 1
		if role == metadata.ROOT {
			count = 3
		}
		r.setRole(role, keys(r.t, count))
	}
	return oldQuorum, r.roleKeys[metadata.ROOT][:2]
}

// openClient demonstrates the required root handoff on restart. It is not a
// production state store: no crash journal, locking, or corruption recovery.
func (r *repository) openClient(state string, now time.Time) (*updater.Updater, error) {
	rootPath := filepath.Join(state, "metadata", "root.json")
	root, err := os.ReadFile(rootPath)
	if errors.Is(err, os.ErrNotExist) {
		if _, statErr := os.Stat(state); !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("existing state has no trusted root: %w", err)
		}
		root = r.initialRoot
	} else if err != nil {
		return nil, err
	}
	cfg, err := config.New(r.server.URL, root)
	if err != nil {
		return nil, err
	}
	cfg.LocalMetadataDir = filepath.Join(state, "metadata")
	cfg.LocalTargetsDir = filepath.Join(state, "targets")
	// httptest's client trusts only its test CA; TLS verification stays enabled.
	check(r.t, cfg.SetDefaultFetcherHTTPClient(r.server.Client()))
	check(r.t, cfg.SetDefaultFetcherRetry(time.Millisecond, 1))
	u, err := updater.New(cfg)
	if err == nil {
		u.UnsafeSetRefTime(now) // Test-only clock, never a production option.
	}
	return u, err
}

func (r *repository) client(state string, now time.Time) *updater.Updater {
	r.t.Helper()
	u, err := r.openClient(state, now)
	check(r.t, err)
	return u
}

func (r *repository) accept(state string, now time.Time) *updater.Updater {
	r.t.Helper()
	u := r.client(state, now)
	check(r.t, u.Refresh())
	target, err := u.GetTargetInfo("bootstrap.json")
	check(r.t, err)
	_, data, err := u.DownloadTarget(target, "", "")
	check(r.t, err)
	if !bytes.Equal(data, manifest) {
		r.t.Fatalf("unexpected manifest: %s", data)
	}
	return u
}

func TestFreshClientAndIdempotentRestart(t *testing.T) {
	r := newRepository(t)
	state := filepath.Join(t.TempDir(), "client")
	r.accept(state, epoch)
	r.accept(state, epoch.Add(time.Hour))
}

func TestOnlineRenewalWithoutOfflineKeys(t *testing.T) {
	r := newRepository(t)
	state := filepath.Join(t.TempDir(), "client")
	r.accept(state, epoch)
	approved := bytes.Clone(r.targets)
	delete(r.roleKeys, metadata.ROOT)
	delete(r.roleKeys, metadata.TARGETS)
	r.renew(epoch.Add(23 * time.Hour))
	u := r.accept(state, epoch.Add(25*time.Hour))
	if u.GetTrustedMetadataSet().Timestamp.Signed.Version != 2 || !bytes.Equal(approved, r.targets) {
		t.Fatal("renewal must advance freshness without changing membership approval")
	}
}

func TestRollbackAfterRestart(t *testing.T) {
	r := newRepository(t)
	state := filepath.Join(t.TempDir(), "client")
	oldTimestamp := r.get("/timestamp.json")
	r.accept(state, epoch)
	r.renew(epoch.Add(time.Hour))
	r.accept(state, epoch.Add(2*time.Hour))
	r.put("/timestamp.json", oldTimestamp)
	err := r.client(state, epoch.Add(3*time.Hour)).Refresh()
	var versionErr *metadata.ErrBadVersionNumber
	if !errors.As(err, &versionErr) {
		t.Fatalf("wanted rollback rejection, got %v", err)
	}
}

func TestOnlineSignersCannotRollbackAcceptedVersions(t *testing.T) {
	for _, role := range []string{metadata.SNAPSHOT, metadata.TARGETS} {
		t.Run(role, func(t *testing.T) {
			r := newRepository(t)
			state := filepath.Join(t.TempDir(), "client")
			oldTargets := bytes.Clone(r.targets)
			oldStamp, err := metadata.Timestamp().FromBytes(r.get("/timestamp.json"))
			check(t, err)
			r.approve(manifest, epoch.Add(90*24*time.Hour))
			r.renew(epoch.Add(time.Hour))
			r.accept(state, epoch.Add(time.Hour))
			delete(r.roleKeys, metadata.ROOT)
			delete(r.roleKeys, metadata.TARGETS)
			if role == metadata.TARGETS {
				// Online keys can sign new freshness metadata, but cannot make
				// a returning client accept an older membership approval.
				r.targets, r.targetVersion = oldTargets, 1
			}
			r.renew(epoch.Add(2 * time.Hour))
			if role == metadata.SNAPSHOT {
				stamp, err := metadata.Timestamp().FromBytes(r.get("/timestamp.json"))
				check(t, err)
				stamp.Signed.Meta["snapshot.json"] = oldStamp.Signed.Meta["snapshot.json"]
				r.put("/timestamp.json", signed(t, stamp, r.roleKeys[metadata.TIMESTAMP]...))
			}
			err = r.client(state, epoch.Add(2*time.Hour)).Refresh()
			var versionErr *metadata.ErrBadVersionNumber
			if !errors.As(err, &versionErr) {
				t.Fatalf("wanted %s rollback rejection, got %v", role, err)
			}
		})
	}
}

func TestRootRotationRequiresBothQuorums(t *testing.T) {
	for _, mode := range []string{"old-only", "new-only", "one-old", "one-new", "both"} {
		t.Run(mode, func(t *testing.T) {
			r := newRepository(t)
			old, next := r.rotate()
			var signingKeys []ed25519.PrivateKey
			switch mode {
			case "old-only":
				signingKeys = old
			case "new-only":
				signingKeys = next
			case "one-old":
				signingKeys = append(old[:1:1], next...)
			case "one-new":
				signingKeys = append(old[:2:2], next[:1]...)
			case "both":
				signingKeys = append(old[:2:2], next...)
			}
			r.put("/2.root.json", signed(t, r.root, signingKeys...))
			r.approve(manifest, epoch.Add(90*24*time.Hour))
			r.renew(epoch)
			state := filepath.Join(t.TempDir(), "client")
			if mode == "both" {
				u := r.accept(state, epoch)
				if u.GetTrustedMetadataSet().Root.Signed.Version != 2 {
					t.Fatal("root transition not adopted")
				}
			} else {
				err := r.client(state, epoch).Refresh()
				var unsigned *metadata.ErrUnsignedMetadata
				if !errors.As(err, &unsigned) {
					t.Fatalf("wanted signature threshold rejection, got %v", err)
				}
			}
		})
	}
}

func TestReturningClientAndPersistedRoot(t *testing.T) {
	r := newRepository(t)
	for version := int64(2); version <= 3; version++ {
		old, next := r.rotate()
		if version == 2 {
			// An expired intermediate root can still authenticate its successor.
			r.root.Signed.Expires = epoch.Add(-time.Hour)
		}
		r.put(fmt.Sprintf("/%d.root.json", version), signed(t, r.root, append(old[:2:2], next...)...))
	}
	r.approve(manifest, epoch.Add(500*24*time.Hour))
	r.renew(epoch.Add(400 * 24 * time.Hour))
	state := filepath.Join(t.TempDir(), "client")
	u := r.accept(state, epoch.Add(400*24*time.Hour)) // Initial root has expired, too.
	if u.GetTrustedMetadataSet().Root.Signed.Version != 3 {
		t.Fatal("returning client did not traverse both transitions")
	}
	r.put("/2.root.json", nil)
	// Already-updated clients restart at root 3, even if older history is gone.
	r.accept(state, epoch.Add(400*24*time.Hour))
	// Removing a transition strands old clients. Production must retain it.
	err := r.client(filepath.Join(t.TempDir(), "stale"), epoch.Add(400*24*time.Hour)).Refresh()
	var expired *metadata.ErrExpiredMetadata
	if !errors.As(err, &expired) {
		t.Fatalf("wanted expired stranded root rejection, got %v", err)
	}
}

func TestRetiredOnlineKeyCannotAuthorizeAfterRotation(t *testing.T) {
	r := newRepository(t)
	oldTimestampKey := r.roleKeys[metadata.TIMESTAMP]
	state := filepath.Join(t.TempDir(), "client")
	r.accept(state, epoch)
	old, next := r.rotate()
	r.put("/2.root.json", signed(t, r.root, append(old[:2:2], next...)...))
	r.approve(manifest, epoch.Add(90*24*time.Hour))
	r.renew(epoch)
	r.accept(state, epoch)
	r.roleKeys[metadata.TIMESTAMP] = oldTimestampKey
	r.renew(epoch.Add(time.Hour))
	err := r.client(state, epoch.Add(time.Hour)).Refresh()
	var unsigned *metadata.ErrUnsignedMetadata
	if !errors.As(err, &unsigned) {
		t.Fatalf("wanted retired signer rejection, got %v", err)
	}
}

func TestInterruptedPublicationAndRecovery(t *testing.T) {
	r := newRepository(t)
	state := filepath.Join(t.TempDir(), "client")
	r.accept(state, epoch)
	oldTimestamp := r.get("/timestamp.json")
	r.renew(epoch.Add(time.Hour))
	snapshot := r.get("/2.snapshot.json")
	r.put("/2.snapshot.json", nil) // Timestamp points at an unavailable object.
	err := r.client(state, epoch.Add(time.Hour)).Refresh()
	var downloadErr *metadata.ErrDownloadHTTP
	if !errors.As(err, &downloadErr) || downloadErr.StatusCode != http.StatusNotFound {
		t.Fatalf("wanted missing snapshot failure, got %v", err)
	}
	// A partial update persisted timestamp 2. Publishing timestamp 1 again is
	// not recovery: finish the release, or publish a higher numbered repair.
	r.put("/timestamp.json", oldTimestamp)
	err = r.client(state, epoch.Add(time.Hour)).Refresh()
	var versionErr *metadata.ErrBadVersionNumber
	if !errors.As(err, &versionErr) {
		t.Fatalf("partial progress was lost across restart: %v", err)
	}
	r.put("/2.snapshot.json", snapshot)
	r.renew(epoch.Add(2 * time.Hour))
	r.accept(state, epoch.Add(2*time.Hour))
}

func TestTargetTampering(t *testing.T) {
	r := newRepository(t)
	u := r.client(filepath.Join(t.TempDir(), "client"), epoch)
	check(t, u.Refresh())
	target, err := u.GetTargetInfo("bootstrap.json")
	check(t, err)
	tampered := bytes.Clone(manifest)
	tampered[0] ^= 1 // Same length, different digest.
	r.put(r.targetPath, tampered)
	_, _, err = u.DownloadTarget(target, "", "")
	var hashErr *metadata.ErrLengthOrHashMismatch
	if !errors.As(err, &hashErr) {
		t.Fatalf("wanted target hash rejection, got %v", err)
	}
}

func TestEachRoleExpires(t *testing.T) {
	for _, role := range metadata.TOP_LEVEL_ROLE_NAMES {
		t.Run(role, func(t *testing.T) {
			r := newRepository(t)
			now := epoch.Add(2 * time.Hour)
			switch role {
			case metadata.ROOT:
				r.root.Signed.Expires = epoch.Add(time.Hour)
				r.initialRoot = signed(t, r.root, r.roleKeys[metadata.ROOT][:2]...)
			case metadata.TARGETS:
				r.approve(manifest, epoch.Add(time.Hour))
				r.renew(epoch)
			case metadata.SNAPSHOT:
				// Give the timestamp a longer life than its referenced snapshot.
				stamp, err := metadata.Timestamp().FromBytes(r.get("/timestamp.json"))
				check(t, err)
				stamp.Signed.Expires = epoch.Add(10 * 24 * time.Hour)
				r.put("/timestamp.json", signed(t, stamp, r.roleKeys[metadata.TIMESTAMP]...))
				now = epoch.Add(8 * 24 * time.Hour)
			case metadata.TIMESTAMP:
				now = epoch.Add(25 * time.Hour)
			}
			err := r.client(filepath.Join(t.TempDir(), "client"), now).Refresh()
			var expired *metadata.ErrExpiredMetadata
			if !errors.As(err, &expired) {
				t.Fatalf("wanted %s expiration, got %v", role, err)
			}
		})
	}
}

func TestRuntimeLeaseMustExpireIndependently(t *testing.T) {
	r := newRepository(t)
	state := filepath.Join(t.TempDir(), "client")
	u := r.accept(state, epoch)
	trusted := u.GetTrustedMetadataSet()
	deadline := trusted.Root.Signed.Expires
	for _, expiry := range []time.Time{trusted.Timestamp.Signed.Expires, trusted.Snapshot.Signed.Expires, trusted.Targets[metadata.TARGETS].Signed.Expires} {
		if expiry.Before(deadline) {
			deadline = expiry
		}
	}
	if !deadline.Equal(epoch.Add(24 * time.Hour)) {
		t.Fatalf("unexpected lease deadline: %s", deadline)
	}
	r.put("/timestamp.json", nil)
	if err := r.client(state, deadline).Refresh(); err == nil {
		t.Fatal("wanted refresh failure")
	}
	// A retained updater still returns cached target information: the library
	// does not revoke a node's runtime permissions when wall time advances.
	_, err := u.GetTargetInfo("bootstrap.json")
	check(t, err)
}

func TestExistingStateDoesNotFallBackToBundledRoot(t *testing.T) {
	for _, mode := range []string{"missing", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			r := newRepository(t)
			state := filepath.Join(t.TempDir(), "client")
			r.accept(state, epoch)
			rootPath := filepath.Join(state, "metadata", "root.json")
			if mode == "missing" {
				check(t, os.Remove(rootPath))
			} else {
				check(t, os.WriteFile(rootPath, []byte("broken"), 0600))
			}
			if _, err := r.openClient(state, epoch); err == nil {
				t.Fatal("existing state silently reset to the bundled root")
			}
		})
	}
}
