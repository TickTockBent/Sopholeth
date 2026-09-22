package bootstrap

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
)

var epoch = time.Now().UTC().Truncate(time.Second)

var manifest = []byte(`{"schema":1,"network":"disposable","enclave":"default","roots":[{"id":"one","origin":"https://one.example.invalid"},{"id":"two","origin":"https://two.example.invalid"},{"id":"three","origin":"https://three.example.invalid"}]}`)

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
