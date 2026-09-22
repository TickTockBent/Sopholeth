package omega

import (
	"bytes"
	"crypto"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
	"sopholeth/internal/trust/bootstrap"
)

const maxReleases = 10000

// A release contains public bytes only. Once durable, these exact bytes are
// used for all retries; signing clocks and keys cannot change a numbered file.
type release struct {
	Schema         int       `json:"schema"`
	Version        int64     `json:"version"`
	Previous       string    `json:"previous_sha256"`
	Fingerprint    string    `json:"fingerprint"`
	Created        time.Time `json:"created"`
	Manifest       []byte    `json:"manifest"`
	Targets        []byte    `json:"targets"`
	Snapshot       []byte    `json:"snapshot"`
	Timestamp      []byte    `json:"timestamp"`
	TargetsVersion int64     `json:"targets_version,omitempty"`
	ApprovedAt     time.Time `json:"approved_at,omitzero"`
	Roots          [][]byte  `json:"roots,omitempty"`
}

func digest(data []byte) string        { return fmt.Sprintf("%x", sha256.Sum256(data)) }
func releaseName(version int64) string { return fmt.Sprintf("%d.release.json", version) }
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func approvedManifest(data []byte, network string) ([]byte, error) {
	m, err := bootstrap.ParseManifest(data, network)
	if err != nil {
		return nil, err
	}
	if len(m.Roots) != 3 {
		return nil, errors.New("omega: first-network publication requires exactly three distinct roots")
	}
	return record(m), nil
}

func signMetadata[T metadata.Roles](m *metadata.Metadata[T], encoded string) ([]byte, error) {
	key, err := decodeKey(encoded)
	if err != nil {
		return nil, err
	}
	signer, err := signature.LoadSigner(key, crypto.Hash(0))
	if err != nil {
		return nil, err
	}
	if _, err := m.Sign(signer); err != nil {
		return nil, err
	}
	return m.ToBytes(false)
}
func metadataFile(version int64, data []byte) *metadata.MetaFiles {
	hash := sha256.Sum256(data)
	return &metadata.MetaFiles{Version: version, Length: int64(len(data)), Hashes: metadata.Hashes{"sha256": hash[:]}}
}

func prepareRelease(a authority, bundle bootstrap.Bundle, version int64, previous string, manifest []byte, now time.Time, roots ...[]byte) (release, error) {
	r := release{Schema: 1, Version: version, Previous: previous, Fingerprint: bundle.Fingerprint(), Created: now, Manifest: manifest, Roots: roots}
	if len(roots) > 0 {
		r.Schema = 3
	}
	if a.Expires.Sub(now) < 24*time.Hour {
		return r, errors.New("omega: publication requires at least 24 hours of root validity; arrange a root ceremony")
	}
	target, err := metadata.TargetFile().FromBytes("bootstrap.json", manifest, "sha256")
	if err != nil {
		return r, err
	}
	targets := metadata.Targets(minTime(now.Add(90*24*time.Hour), a.Expires))
	targets.Signed.Version = version
	targets.Signed.Targets["bootstrap.json"] = target
	r.Targets, err = signMetadata(targets, a.Keys["targets"])
	if err != nil {
		return r, err
	}
	snapshot := metadata.Snapshot(minTime(now.Add(7*24*time.Hour), a.Expires))
	snapshot.Signed.Version = version
	snapshot.Signed.Meta["targets.json"] = metadataFile(version, r.Targets)
	r.Snapshot, err = signMetadata(snapshot, a.Keys["snapshot"])
	if err != nil {
		return r, err
	}
	timestamp := metadata.Timestamp(minTime(now.Add(24*time.Hour), a.Expires))
	timestamp.Signed.Version = version
	timestamp.Signed.Meta["snapshot.json"] = metadataFile(version, r.Snapshot)
	r.Timestamp, err = signMetadata(timestamp, a.Keys["timestamp"])
	if err != nil {
		return r, err
	}
	_, _, err = r.validate(bundle)
	return r, err
}

// Verify stored signatures and exact references without signing again. The
// creation time permits inspection of historical releases after they expire;
// publication and live verification separately enforce the current deadline.
func (r release) validate(bundle bootstrap.Bundle) (bootstrap.Manifest, map[string]time.Time, error) {
	var empty bootstrap.Manifest
	fail := func(err error) (bootstrap.Manifest, map[string]time.Time, error) { return empty, nil, err }
	if r.Schema < 1 || r.Schema > 4 || (r.Schema > 2) != (len(r.Roots) > 0) || r.Version < 1 || r.Version > maxReleases || r.Fingerprint != bundle.Fingerprint() || r.Created.IsZero() {
		return fail(errors.New("omega: invalid release identity or schema"))
	}
	canonical, err := approvedManifest(r.Manifest, bundle.Network)
	if err != nil {
		return fail(err)
	}
	if !bytes.Equal(canonical, r.Manifest) {
		return fail(errors.New("omega: release manifest is not canonical"))
	}
	tm, err := trustedRoots(bundle, r.Roots)
	if err != nil {
		return fail(err)
	}
	tm.RefTime = r.Created
	if _, err := tm.UpdateTimestamp(r.Timestamp); err != nil {
		return fail(err)
	}
	if _, err := tm.UpdateSnapshot(r.Snapshot, false); err != nil {
		return fail(err)
	}
	targets, err := tm.UpdateDelegatedTargets(r.Targets, metadata.TARGETS, metadata.ROOT)
	if err != nil {
		return fail(err)
	}
	target := targets.Signed.Targets["bootstrap.json"]
	if target == nil || len(targets.Signed.Targets) != 1 || len(tm.Snapshot.Signed.Meta) != 1 || len(tm.Timestamp.Signed.Meta) != 1 ||
		tm.Timestamp.Signed.Version != r.Version || tm.Snapshot.Signed.Version != r.Version || targets.Signed.Version != r.versions().Targets {
		return fail(errors.New("omega: release metadata does not match the approved version and target"))
	}
	if err := target.VerifyLengthHashes(r.Manifest); err != nil {
		return fail(err)
	}
	if !bytes.Equal(record(tm.Snapshot.Signed.Meta["targets.json"]), record(metadataFile(r.versions().Targets, r.Targets))) ||
		!bytes.Equal(record(tm.Timestamp.Signed.Meta["snapshot.json"]), record(metadataFile(r.Version, r.Snapshot))) {
		return fail(errors.New("omega: release metadata references do not match the exact signed bytes"))
	}
	expires := map[string]time.Time{"root": tm.Root.Signed.Expires, "targets": targets.Signed.Expires, "snapshot": tm.Snapshot.Signed.Expires, "timestamp": tm.Timestamp.Signed.Expires}
	approvedAt := r.approvalTime()
	if !r.isRenewal() && (r.TargetsVersion != 0 || !r.ApprovedAt.IsZero()) {
		return fail(errors.New("omega: approval contains renewal-only fields"))
	}
	if r.isRenewal() && (r.TargetsVersion < 1 || r.TargetsVersion >= r.Version || approvedAt.IsZero() || r.Created.Before(approvedAt)) {
		return fail(errors.New("omega: invalid renewal approval identity"))
	}
	deadline := expires["root"]
	if r.isRenewal() {
		deadline = minTime(deadline, expires["targets"])
	}
	if !expires["targets"].Equal(minTime(approvedAt.Add(90*24*time.Hour), expires["root"])) ||
		!expires["snapshot"].Equal(minTime(r.Created.Add(7*24*time.Hour), deadline)) ||
		!expires["timestamp"].Equal(minTime(r.Created.Add(24*time.Hour), deadline)) {
		return fail(errors.New("omega: release expiration policy mismatch"))
	}
	if !r.isRenewal() && expires["root"].Sub(r.Created) < 24*time.Hour {
		return fail(errors.New("omega: release approved too close to root expiration"))
	}
	m, err := bootstrap.ParseManifest(r.Manifest, bundle.Network)
	return m, expires, err
}

func (r release) objects(bundle bootstrap.Bundle) []publicObject {
	objects := []publicObject{
		{"1.root.json", bundle.Root},
		{"targets/" + digest(r.Manifest) + ".bootstrap.json", r.Manifest},
		{fmt.Sprintf("%d.targets.json", r.versions().Targets), r.Targets},
		{fmt.Sprintf("%d.snapshot.json", r.Version), r.Snapshot},
	}
	// Successor dependencies precede the root transition; timestamp is written
	// last by the publisher. Every numbered root remains available forever.
	for i, root := range r.Roots {
		objects = append(objects, publicObject{fmt.Sprintf("%d.root.json", i+2), root})
	}
	return objects
}

func (r release) isRenewal() bool { return r.Schema == 2 || r.Schema == 4 }

func (r release) currentRoot(bundle bootstrap.Bundle) []byte {
	if len(r.Roots) > 0 {
		return r.Roots[len(r.Roots)-1]
	}
	return bundle.Root
}

func (r release) versions() bootstrap.Versions {
	targets := r.Version
	if r.isRenewal() {
		targets = r.TargetsVersion
	}
	return bootstrap.Versions{Root: int64(len(r.Roots)) + 1, Targets: targets, Snapshot: r.Version, Timestamp: r.Version}
}

func (r release) approvalTime() time.Time {
	if r.isRenewal() {
		return r.ApprovedAt
	}
	return r.Created
}

// Renewal preserves the exact offline-approved targets and manifest. Only the
// two online roles advance; neither can extend the offline approval deadline.
func prepareRenewal(keys map[string]string, bundle bootstrap.Bundle, previous release, now time.Time) (release, error) {
	return prepareRenewalWithRoots(keys, bundle, previous, now, previous.Roots)
}

func prepareRenewalWithRoots(keys map[string]string, bundle bootstrap.Bundle, previous release, now time.Time, roots [][]byte) (release, error) {
	r := release{Schema: 2, Version: previous.Version + 1, Previous: digest(record(previous)), Fingerprint: bundle.Fingerprint(),
		Created: now.Truncate(time.Second), Manifest: previous.Manifest, Targets: previous.Targets,
		TargetsVersion: previous.versions().Targets, ApprovedAt: previous.approvalTime(), Roots: roots}
	if len(roots) > 0 {
		r.Schema = 4
	}
	_, expires, err := previous.validate(bundle)
	if err != nil {
		return r, err
	}
	deadline := minTime(expires["root"], expires["targets"])
	if now.Before(previous.Created) || !now.Before(deadline) {
		return r, errors.New("omega: clock predates history or offline approval expired; renewal cannot change offline approval")
	}
	snapshot := metadata.Snapshot(minTime(r.Created.Add(7*24*time.Hour), deadline))
	snapshot.Signed.Version = r.Version
	snapshot.Signed.Meta["targets.json"] = metadataFile(r.TargetsVersion, r.Targets)
	r.Snapshot, err = signMetadata(snapshot, keys["snapshot"])
	if err != nil {
		return r, err
	}
	timestamp := metadata.Timestamp(minTime(r.Created.Add(24*time.Hour), deadline))
	timestamp.Signed.Version = r.Version
	timestamp.Signed.Meta["snapshot.json"] = metadataFile(r.Version, r.Snapshot)
	r.Timestamp, err = signMetadata(timestamp, keys["timestamp"])
	if err != nil {
		return r, err
	}
	_, _, err = r.validate(bundle)
	return r, err
}

func (r release) follows(previous release) error {
	if r.Created.Before(previous.Created) {
		return errors.New("omega: release clock moved backward")
	}
	if len(r.Roots) < len(previous.Roots) || len(r.Roots) > len(previous.Roots)+1 ||
		(len(r.Roots) > len(previous.Roots) && !r.isRenewal()) {
		return errors.New("omega: release root chain rolled back or skipped a transition")
	}
	for i := range previous.Roots {
		if !bytes.Equal(r.Roots[i], previous.Roots[i]) {
			return errors.New("omega: release changed an established root transition")
		}
	}
	if r.isRenewal() && (r.TargetsVersion != previous.versions().Targets || !r.ApprovedAt.Equal(previous.approvalTime()) ||
		!bytes.Equal(r.Targets, previous.Targets) || !bytes.Equal(r.Manifest, previous.Manifest)) {
		return errors.New("omega: renewal changed offline-approved membership")
	}
	return nil
}
