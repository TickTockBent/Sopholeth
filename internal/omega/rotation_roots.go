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
// our narrower policy: either the two online assignments or targets may change.
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
		if _, err := successorRole(previous, next); err != nil {
			return nil, err
		}
	}
	return tm, nil
}

func successorRole(previous, next *metadata.Metadata[metadata.RootType]) (string, error) {
	if err := roleSuccessor(previous, next, []string{"snapshot", "timestamp"}); err == nil {
		return "online", nil
	}
	if err := roleSuccessor(previous, next, []string{"targets"}); err == nil {
		return "targets", nil
	}
	return "", errors.New("omega: successor must rotate online or targets keys without changing root authority, expiry, or policy")
}

func roleSuccessor(previous, next *metadata.Metadata[metadata.RootType], roles []string) error {
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
		return errors.New("omega: invalid role-key root successor")
	}
	for _, role := range roles {
		assignment := next.Signed.Roles[role]
		old := previous.Signed.Roles[role]
		if assignment == nil || old == nil || assignment.Threshold != 1 || len(assignment.KeyIDs) != 1 || len(old.KeyIDs) != 1 {
			return errors.New("omega: rotation requires distinct single-key roles")
		}
		id := assignment.KeyIDs[0]
		key := next.Signed.Keys[id]
		if key == nil || previous.Signed.Keys[id] != nil {
			return errors.New("omega: rotation must replace each selected key with a fresh key")
		}
		public, err := key.ToPublicKey()
		if err != nil {
			return err
		}
		if _, ok := public.(ed25519.PublicKey); !ok {
			return errors.New("omega: rotation requires Ed25519 keys")
		}
		keyID, err := key.ID()
		if err != nil || keyID != id {
			return errors.New("omega: invalid successor key identity")
		}
		delete(copyRoot.Signed.Keys, id)
		copyRoot.Signed.Keys[old.KeyIDs[0]] = previous.Signed.Keys[old.KeyIDs[0]]
		copyRoot.Signed.Roles[role] = old
	}
	copyRoot.Signed.Version = previous.Signed.Version
	if !bytes.Equal(record(copyRoot.Signed), record(previous.Signed)) {
		return errors.New("omega: rotation changed unselected authority, expiry, or root policy")
	}
	return nil
}

func (intent rotationIntent) validate(bundle bootstrap.Bundle, previous []byte, version int64) error {
	if (intent.Schema != 1 && intent.Schema != 2) || intent.Mode != "disposable" || intent.Fingerprint != bundle.Fingerprint() ||
		intent.RootVersion != version || version < 2 || version > maxRootVersion || intent.PreviousRoot != digest(previous) ||
		intent.Created.IsZero() ||
		(intent.Schema == 1 && (len(intent.Keys) != 2 || intent.TargetsKey != nil)) ||
		(intent.Schema == 2 && (len(intent.Keys) != 0 || intent.TargetsKey == nil)) {
		return errors.New("omega: invalid rotation intent; preserve the journal")
	}
	root, err := metadata.Root().FromBytes(previous)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, role := range intent.roles() {
		public, err := intent.publicKey(role)
		if err != nil {
			return err
		}
		key, err := public.ToPublicKey()
		if err != nil {
			return err
		}
		if _, ok := key.(ed25519.PublicKey); !ok {
			return errors.New("omega: rotation requires Ed25519 keys")
		}
		id, err := public.ID()
		if err != nil || root.Signed.Keys[id] != nil || seen[id] {
			return errors.New("omega: rotation keys must be new and distinct")
		}
		seen[id] = true
	}
	return nil
}

func (intent rotationIntent) roles() []string {
	if intent.Schema == 2 {
		return []string{"targets"}
	}
	return []string{"snapshot", "timestamp"}
}

func (intent rotationIntent) publicKey(role string) (*metadata.Key, error) {
	if intent.Schema == 2 && role == "targets" && intent.TargetsKey != nil {
		return intent.TargetsKey, nil
	}
	key, err := decodeKey(intent.Keys[role])
	if err != nil {
		return nil, fmt.Errorf("omega: invalid rotation %s key", role)
	}
	return metadata.KeyFromPublicKey(key.Public())
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
	if err := roleSuccessor(old, next, intent.roles()); err != nil {
		return err
	}
	for _, role := range intent.roles() {
		key, err := intent.publicKey(role)
		if err != nil {
			return err
		}
		id, err := key.ID()
		if err != nil || next.Signed.Roles[role].KeyIDs[0] != id {
			return errors.New("omega: successor differs from prepared keys")
		}
	}
	return nil
}

func activeOnlineKeys(state *store, bundle bootstrap.Bundle, latest release, initial map[string]string) (map[string]string, error) {
	for i := len(latest.Roots) - 1; i >= 0; i-- {
		previous := bundle.Root
		if i > 0 {
			previous = latest.Roots[i-1]
		}
		r, intent, err := readRotation(state, bundle, previous, int64(i)+2)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(r.Root, latest.Roots[i]) {
			return nil, errors.New("omega: renewal custody differs from release history")
		}
		if intent.Schema == 1 {
			return intent.Keys, verifyOnlineKeys(intent.Keys, latest.currentRoot(bundle))
		}
	}
	if initial != nil {
		return initial, verifyOnlineKeys(initial, latest.currentRoot(bundle))
	}
	return nil, nil // Inspection without private initial keys; the root chain verified.
}

func rotationReport(r rotationRecord, previous []byte, state string) *RotationReport {
	old, _ := metadata.Root().FromBytes(previous)
	next, _ := metadata.Root().FromBytes(r.Root)
	role, _ := successorRole(old, next)
	out := &RotationReport{State: state, Role: role, RootVersion: r.RootVersion, RootSHA256: digest(r.Root),
		Replaces: map[string][]string{}, Keys: map[string][]string{}}
	for _, name := range []string{"targets", "snapshot", "timestamp"} {
		if !bytes.Equal(record(old.Signed.Roles[name]), record(next.Signed.Roles[name])) {
			out.Replaces[name] = old.Signed.Roles[name].KeyIDs
			out.Keys[name] = next.Signed.Roles[name].KeyIDs
		}
	}
	return out
}
