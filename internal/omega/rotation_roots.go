package omega

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/theupdateframework/go-tuf/v2/metadata"
	"github.com/theupdateframework/go-tuf/v2/metadata/trustedmetadata"
	"sopholeth/internal/trust/bootstrap"
)

func rotationName(n int64) string       { return fmt.Sprintf("%d.rotation.json", n) }
func rotationIntentName(n int64) string { return fmt.Sprintf("%d.rotation-intent.json", n) }
func rotationApplyName(n int64) string  { return fmt.Sprintf("%d.rotation-apply.json", n) }

// Validate the full chain with TUF's old-and-new threshold checks, then enforce
// this slice's narrower policy: only the two online assignments may change.
func trustedRoots(bundle bootstrap.Bundle, roots [][]byte) (*trustedmetadata.TrustedMetadata, error) {
	if len(roots) >= maxRootVersion {
		return nil, errors.New("omega: disposable root transition limit reached")
	}
	tm, err := trustedmetadata.New(bundle.Root)
	if err != nil {
		return nil, err
	}
	for _, raw := range roots {
		previous := tm.Root
		next, err := tm.UpdateRoot(raw)
		if err != nil {
			return nil, err
		}
		if err := onlineSuccessor(previous, next); err != nil {
			return nil, err
		}
	}
	return tm, nil
}

func onlineSuccessor(previous, next *metadata.Metadata[metadata.RootType]) error {
	// Work on a copy: normalization must never mutate the authenticated root.
	copyBytes, err := next.ToBytes(false)
	if err != nil {
		return err
	}
	copyRoot, err := metadata.Root().FromBytes(copyBytes)
	if err != nil {
		return err
	}
	if next.Signed.Version != previous.Signed.Version+1 || len(next.Signed.Keys) != 6 {
		return errors.New("omega: invalid online root successor")
	}
	for _, role := range []string{"snapshot", "timestamp"} {
		assignment := next.Signed.Roles[role]
		old := previous.Signed.Roles[role]
		if assignment == nil || old == nil || assignment.Threshold != 1 || len(assignment.KeyIDs) != 1 || len(old.KeyIDs) != 1 {
			return errors.New("omega: online rotation requires distinct single-key roles")
		}
		id := assignment.KeyIDs[0]
		key := next.Signed.Keys[id]
		if key == nil || previous.Signed.Keys[id] != nil {
			return errors.New("omega: online rotation must replace both keys with fresh keys")
		}
		public, err := key.ToPublicKey()
		if err != nil {
			return err
		}
		if _, ok := public.(ed25519.PublicKey); !ok {
			return errors.New("omega: online rotation requires Ed25519 keys")
		}
		keyID, err := key.ID()
		if err != nil || keyID != id {
			return errors.New("omega: invalid online successor key identity")
		}
		delete(copyRoot.Signed.Keys, id)
		copyRoot.Signed.Keys[old.KeyIDs[0]] = previous.Signed.Keys[old.KeyIDs[0]]
		copyRoot.Signed.Roles[role] = old
	}
	copyRoot.Signed.Version = previous.Signed.Version
	if !bytes.Equal(record(copyRoot.Signed), record(previous.Signed)) {
		return errors.New("omega: online rotation changed offline authority, expiry, or root policy")
	}
	return nil
}

func (intent rotationIntent) validate(bundle bootstrap.Bundle, previous []byte, version int64) error {
	if intent.Schema != 1 || intent.Mode != "disposable" || intent.Fingerprint != bundle.Fingerprint() ||
		intent.RootVersion != version || version < 2 || version > maxRootVersion || intent.PreviousRoot != digest(previous) ||
		intent.Created.IsZero() || len(intent.Keys) != 2 {
		return errors.New("omega: invalid rotation intent; preserve the journal")
	}
	root, err := metadata.Root().FromBytes(previous)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, role := range []string{"snapshot", "timestamp"} {
		key, err := decodeKey(intent.Keys[role])
		if err != nil {
			return fmt.Errorf("omega: invalid rotation %s key", role)
		}
		public, err := metadata.KeyFromPublicKey(key.Public())
		if err != nil {
			return err
		}
		id, err := public.ID()
		if err != nil || root.Signed.Keys[id] != nil || seen[id] {
			return errors.New("omega: rotation keys must be new and distinct")
		}
		seen[id] = true
	}
	return nil
}

func readRotation(state *store, bundle bootstrap.Bundle, previous []byte, version int64) (rotationRecord, rotationIntent, error) {
	var r rotationRecord
	var intent rotationIntent
	data, err := state.read(rotationIntentName(version))
	if err != nil {
		return r, intent, err
	}
	if err := decodeRecord(data, &intent); err != nil {
		return r, intent, err
	}
	if err := intent.validate(bundle, previous, version); err != nil {
		return r, intent, err
	}
	data, err = state.read(rotationName(version))
	if err != nil {
		return r, intent, err
	}
	if err := decodeRecord(data, &r); err != nil {
		return r, intent, err
	}
	return r, intent, r.validate(bundle, previous, intent)
}

func (r rotationRecord) validate(bundle bootstrap.Bundle, previous []byte, intent rotationIntent) error {
	if r.Schema != 1 || r.Fingerprint != bundle.Fingerprint() || r.RootVersion != intent.RootVersion || r.Intent != digest(record(intent)) {
		return errors.New("omega: rotation record does not match durable custody")
	}
	tm, err := trustedmetadata.New(previous)
	if err != nil {
		return err
	}
	old := tm.Root
	next, err := tm.UpdateRoot(r.Root)
	if err != nil {
		return err
	}
	if err := onlineSuccessor(old, next); err != nil {
		return err
	}
	if err := verifyOnlineKeys(intent.Keys, r.Root); err != nil {
		return err
	}
	return nil
}

func activeOnlineKeys(state *store, bundle bootstrap.Bundle, latest release, initial map[string]string) (map[string]string, error) {
	if len(latest.Roots) == 0 {
		return initial, nil
	}
	previous := bundle.Root
	if len(latest.Roots) > 1 {
		previous = latest.Roots[len(latest.Roots)-2]
	}
	r, intent, err := readRotation(state, bundle, previous, latest.versions().Root)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(r.Root, latest.currentRoot(bundle)) {
		return nil, errors.New("omega: active renewal custody differs from release history")
	}
	return intent.Keys, nil
}

func rotationReport(r rotationRecord, previous []byte, state string) *RotationReport {
	old, _ := metadata.Root().FromBytes(previous)
	next, _ := metadata.Root().FromBytes(r.Root)
	out := &RotationReport{State: state, RootVersion: r.RootVersion, RootSHA256: digest(r.Root),
		Replaces: map[string][]string{}, Keys: map[string][]string{}}
	for _, role := range []string{"snapshot", "timestamp"} {
		out.Replaces[role] = old.Signed.Roles[role].KeyIDs
		out.Keys[role] = next.Signed.Roles[role].KeyIDs
	}
	return out
}
