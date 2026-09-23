package omega

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
	"sopholeth/internal/trust/bootstrap"
)

// Only online private keys enter the operational journal. The initial custody
// record and every generation remain immutable; the release history selects
// the active generation. These formats are disposable-only.
type rotationIntent struct {
	Schema       int               `json:"schema"`
	Mode         string            `json:"mode"`
	Fingerprint  string            `json:"fingerprint"`
	RootVersion  int64             `json:"root_version"`
	PreviousRoot string            `json:"previous_root_sha256"`
	Created      time.Time         `json:"created"`
	Keys         map[string]string `json:"private_keys"`
	// Schema 2 is a public targets-key handoff. Its private key stays offline.
	TargetsKey *metadata.Key `json:"targets_key,omitempty"`
	// Schema 3 hands off only public root keys and the reviewed expiration.
	RootKeys    map[string]*metadata.Key `json:"root_keys,omitempty"`
	RootExpires time.Time                `json:"root_expires,omitzero"`
}

type rotationRecord struct {
	Schema      int    `json:"schema"`
	Fingerprint string `json:"fingerprint"`
	RootVersion int64  `json:"root_version"`
	Intent      string `json:"intent_sha256"`
	Root        []byte `json:"root"`
}

type rotationApply struct {
	Schema        int       `json:"schema"`
	Version       int64     `json:"version"`
	Previous      string    `json:"previous_sha256"`
	RootVersion   int64     `json:"root_version"`
	RootSHA256    string    `json:"root_sha256"`
	Created       time.Time `json:"created"`
	TimestampDays int       `json:"timestamp_days,omitempty"`
}

func prepareRotation(state *store, bundle bootstrap.Bundle, previous []byte, version int64, a authority, now time.Time) (rotationRecord, error) {
	var r rotationRecord
	var intent rotationIntent
	name := rotationIntentName(version)
	data, err := state.read(name)
	committed := err == nil
	if errors.Is(err, os.ErrNotExist) {
		data, err = state.read(name + ".pending")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return r, err
	}
	if err == nil && json.Valid(data) {
		if err := decodeRecord(data, &intent); err != nil {
			return r, err
		}
	} else {
		if committed {
			return r, errors.New("omega: corrupt committed rotation intent; restore it")
		}
		for _, output := range []string{rotationName(version), rotationName(version) + ".pending"} {
			if _, err := state.root.Lstat(output); !errors.Is(err, os.ErrNotExist) {
				return r, errors.New("omega: rotation custody missing or torn after signing; restore it")
			}
		}
		intent = rotationIntent{Schema: 1, Mode: "disposable", Fingerprint: bundle.Fingerprint(), RootVersion: version,
			PreviousRoot: digest(previous), Created: now.Truncate(time.Second), Keys: map[string]string{}}
		for _, role := range []string{"snapshot", "timestamp"} {
			_, key, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				return r, err
			}
			intent.Keys[role] = base64.StdEncoding.EncodeToString(key)
		}
	}
	if err := intent.validate(bundle, previous, version); err != nil {
		return r, err
	}
	if intent.Schema != 1 {
		return r, errors.New("omega: root version is reserved for another rotation role; retry its original --role")
	}
	return finishRotation(state, bundle, previous, version, a, now, intent)
}

func finishRotation(state *store, bundle bootstrap.Bundle, previous []byte, version int64, a authority, now time.Time, intent rotationIntent, successorKeys ...string) (rotationRecord, error) {
	var r rotationRecord
	if now.Before(intent.Created) {
		return r, errors.New("omega: clock predates rotation preparation")
	}
	if err := state.install(rotationIntentName(version), record(intent)); err != nil {
		return r, err
	}
	if err := state.syncDir(); err != nil {
		return r, err
	}
	if err := state.phase("rotation:keys-durable"); err != nil {
		return r, err
	}
	// Never regenerate signed output that already exists, including a complete
	// pending write. Retries inspect its signatures and reuse the exact bytes.
	for _, candidate := range []string{rotationName(version), rotationName(version) + ".pending"} {
		data, err := state.read(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return r, err
		}
		if candidate != rotationName(version) && !json.Valid(data) {
			continue
		}
		if err := decodeRecord(data, &r); err != nil {
			return r, err
		}
		if err := r.validate(bundle, previous, intent); err != nil {
			return r, err
		}
		if err := state.install(rotationName(version), data); err != nil {
			return r, err
		}
		if err := state.syncDir(); err != nil {
			return r, err
		}
		return r, cleanPendingTwins(state, ".", state.read)
	}
	root, err := metadata.Root().FromBytes(previous)
	if err != nil {
		return r, err
	}
	root.Signed.Version = version
	root.Signatures = nil
	if intent.Schema == 3 {
		for _, id := range append([]string(nil), root.Signed.Roles[metadata.ROOT].KeyIDs...) {
			if err := root.Signed.RevokeKey(id, metadata.ROOT); err != nil {
				return r, err
			}
		}
		for _, name := range keyNames[:3] {
			if err := root.Signed.AddKey(intent.RootKeys[name], metadata.ROOT); err != nil {
				return r, err
			}
		}
		root.Signed.Expires = intent.RootExpires
	}
	for _, role := range intent.roles() {
		if err := root.Signed.RevokeKey(root.Signed.Roles[role].KeyIDs[0], role); err != nil {
			return r, err
		}
		key, err := intent.publicKey(role)
		if err != nil {
			return r, err
		}
		if err := root.Signed.AddKey(key, role); err != nil {
			return r, err
		}
	}
	var signers []string
	for _, name := range keyNames[:3] {
		if a.Keys[name] != "" && len(signers) < 2 {
			signers = append(signers, a.Keys[name])
		}
	}
	if len(signers) != 2 {
		return r, errors.New("omega: current root quorum unavailable; restore protected backups")
	}
	if intent.Schema == 3 {
		if len(successorKeys) < 2 {
			return r, errors.New("omega: successor root quorum unavailable")
		}
		signers = append(signers, successorKeys[:2]...)
	}
	for _, encoded := range signers {
		private, err := decodeKey(encoded)
		if err != nil {
			return r, err
		}
		signer, err := signature.LoadSigner(private, crypto.Hash(0))
		if err != nil {
			return r, err
		}
		if _, err := root.Sign(signer); err != nil {
			return r, err
		}
	}
	raw, err := root.ToBytes(false)
	if err != nil {
		return r, err
	}
	r = rotationRecord{Schema: 1, Fingerprint: bundle.Fingerprint(), RootVersion: version, Intent: digest(record(intent)), Root: raw}
	if err := r.validate(bundle, previous, intent); err != nil {
		return r, err
	}
	if err := state.install(rotationName(version), record(r)); err != nil {
		return r, err
	}
	if err := state.syncDir(); err != nil {
		return r, err
	}
	if _, _, err := readRotation(state, bundle, previous, version); err != nil {
		return r, err
	}
	return r, cleanPendingTwins(state, ".", state.read)
}

func reserveRotationApply(state *store, bundle bootstrap.Bundle, latest release, rotation rotationRecord, now time.Time) error {
	_, preparation, err := readRotation(state, bundle, latest.currentRoot(bundle), rotation.RootVersion)
	if err != nil {
		return err
	}
	if now.Before(preparation.Created) {
		return errors.New("omega: clock predates rotation preparation")
	}
	version := latest.Version + 1
	if version > maxReleases {
		return errors.New("omega: publication journal version limit reached")
	}
	name := rotationApplyName(version)
	data, err := state.read(name)
	committed := err == nil
	if errors.Is(err, os.ErrNotExist) {
		data, err = state.read(name + ".pending")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	hadPending := err == nil
	if err == nil && json.Valid(data) {
		// resumeRotationApply validates identity and uses this exact clock.
		return nil
	}
	if committed {
		return errors.New("omega: corrupt committed rotation apply intent; restore it")
	}
	for _, reserved := range []string{releaseName(version) + ".pending", renewalIntentName(version), renewalIntentName(version) + ".pending", rotationTargetsName(version), rotationTargetsName(version) + ".pending"} {
		if _, err := state.root.Lstat(reserved); !errors.Is(err, os.ErrNotExist) {
			return errors.New("omega: next release already reserved; finish its original publish command before applying rotation")
		}
	}
	_, expires, err := latest.validate(bundle)
	if err != nil {
		return err
	}
	if preparation.Schema == 3 {
		if preparation.RootExpires.Sub(now) < 24*time.Hour {
			return errors.New("omega: prepared successor needs at least 24 hours of root validity")
		}
	} else if !now.Before(expires["targets"]) || !now.Before(expires["root"]) {
		// No signed release can follow a torn, uncommitted apply reservation.
		// Release that empty reservation so a new offline approval can proceed.
		if hadPending {
			if err := state.root.Remove(name + ".pending"); err != nil {
				return err
			}
			if err := state.syncDir(); err != nil {
				return err
			}
		}
		return errors.New("omega: offline approval expired; renew offline approval before applying rotation")
	}
	if receipt, err := readVerification(state, latest); err == nil {
		if now.Before(receipt.CheckedAt) {
			return errors.New("omega: clock moved backward since the last publication check")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	intent := rotationApply{Schema: 1, Version: version, Previous: digest(record(latest)), RootVersion: rotation.RootVersion,
		RootSHA256: digest(rotation.Root), Created: now.Truncate(time.Second), TimestampDays: timestampDays}
	return state.install(name, record(intent))
}

// The scheduler can finish an authorized apply without the offline authority.
// Even an expired reserved release is completed privately before a higher
// repair; it is never served with an expired timestamp.
func resumeRotationApply(ctx context.Context, state *store, bundle bootstrap.Bundle, history []release, now time.Time, initialKeys map[string]string) ([]release, bool, error) {
	latest := history[len(history)-1]
	name := rotationApplyName(latest.Version + 1)
	data, err := state.read(name)
	if errors.Is(err, os.ErrNotExist) {
		data, err = state.read(name + ".pending")
	}
	if errors.Is(err, os.ErrNotExist) {
		return history, false, nil
	}
	if err != nil {
		return history, false, err
	}
	var intent rotationApply
	if err := decodeRecord(data, &intent); err != nil {
		return history, false, fmt.Errorf("omega: invalid rotation apply intent; rerun rotate with its original --apply digest or restore the journal: %w", err)
	}
	if !validTimestampDays(intent.TimestampDays) || intent.Schema != 1 || intent.Version != latest.Version+1 || intent.Version > maxReleases || intent.Previous != digest(record(latest)) ||
		intent.RootVersion != latest.versions().Root+1 || intent.Created.IsZero() || intent.Created.Before(latest.Created) || now.Before(intent.Created) {
		return history, false, errors.New("omega: rotation apply intent differs from history or clock predates it")
	}
	r, keys, err := readRotation(state, bundle, latest.currentRoot(bundle), intent.RootVersion)
	if err != nil {
		return history, false, err
	}
	if intent.RootSHA256 != digest(r.Root) || intent.Created.Before(keys.Created) {
		return history, false, errors.New("omega: rotation apply intent differs from prepared transition")
	}
	for _, reserved := range []string{renewalIntentName(intent.Version), renewalIntentName(intent.Version) + ".pending"} {
		if _, err := state.root.Lstat(reserved); !errors.Is(err, os.ErrNotExist) {
			return history, false, errors.New("omega: rotation and renewal reservations conflict; restore the journal")
		}
	}
	if err := state.install(name, data); err != nil {
		return history, false, err
	}
	if err := state.syncDir(); err != nil {
		return history, false, err
	}
	if err := state.phase("rotation:apply-durable"); err != nil {
		return history, false, err
	}
	roots := append(append([][]byte(nil), latest.Roots...), r.Root)
	var next release
	if keys.Schema == 2 || keys.Schema == 3 {
		onlineKeys, keyErr := activeOnlineKeys(state, bundle, latest, initialKeys)
		if keyErr != nil {
			return history, false, keyErr
		}
		targets, targetErr := readRotationTargets(state, latest, r, intent.Version, intent.Created)
		if targetErr != nil {
			role := "targets"
			if keys.Schema == 3 {
				role = "root"
			}
			return history, false, fmt.Errorf("%w: finish with the original rotate --role %s --apply command and offline home (--renew-approval for root): %w", errOfflineHandoff, role, targetErr)
		}
		if keys.Schema == 3 {
			next, err = prepareContinuedRelease(onlineKeys, bundle, latest, intent.Created, roots, targets, intent.TimestampDays)
		} else {
			next, err = prepareMembershipRelease(onlineKeys, bundle, latest, intent.Created, roots, targets, intent.TimestampDays)
		}
	} else {
		next, err = prepareRenewalWithRoots(keys.Keys, bundle, latest, intent.Created, roots, intent.TimestampDays)
	}
	if err != nil {
		return history, false, err
	}
	if err := next.follows(latest); err != nil {
		return history, false, err
	}
	if pending, err := state.read(releaseName(next.Version) + ".pending"); err == nil && json.Valid(pending) && !bytes.Equal(pending, record(next)) {
		return history, false, errors.New("omega: pending release differs from rotation intent; preserve the journal")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return history, false, err
	}
	if err := ctx.Err(); err != nil {
		return history, false, err
	}
	if err := state.install(releaseName(next.Version), record(next)); err != nil {
		return history, false, err
	}
	return append(history, next), true, nil
}

func applyReleaseRoot(report *Report, bundle bootstrap.Bundle, r release) error {
	root, err := metadata.Root().FromBytes(r.currentRoot(bundle))
	if err != nil {
		return err
	}
	report.RootVersion = root.Signed.Version
	report.RootExpires = root.Signed.Expires
	if report.Problem == errRootExpired.Error() && time.Now().Before(report.RootExpires) {
		report.Problem = ""
		if report.State == "expired" {
			report.State = "initialized"
		}
	}
	if report.Roles == nil {
		report.Roles = map[string]RoleStatus{}
	}
	for _, role := range metadata.TOP_LEVEL_ROLE_NAMES {
		assignment := root.Signed.Roles[role]
		report.Roles[role] = RoleStatus{Threshold: assignment.Threshold, KeyIDs: assignment.KeyIDs}
	}
	return nil
}

func appliedRotationState(state *store, history []release, version int64) (string, error) {
	for i := len(history) - 1; i >= 0; i-- {
		r := history[i]
		if r.versions().Root < version {
			break
		}
		v, err := readVerification(state, r)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if !v.VerifiedAt.IsZero() {
			return "applied", nil
		}
	}
	return "applying", nil
}

func inspectRotation(state *store, bundle bootstrap.Bundle, history []release) (*RotationReport, error) {
	latest := history[len(history)-1]
	version := latest.versions().Root
	if version > 1 {
		if _, err := activeOnlineKeys(state, bundle, latest, nil); err != nil {
			return nil, err
		}
	}
	// Pending preparation takes precedence; the main report still describes
	// the current root's active assignments separately.
	if _, err := state.root.Lstat(rotationName(version + 1)); err == nil {
		r, _, err := readRotation(state, bundle, latest.currentRoot(bundle), version+1)
		if err != nil {
			return nil, err
		}
		report := rotationReport(r, latest.currentRoot(bundle), "prepared")
		for _, name := range []string{rotationApplyName(latest.Version + 1), rotationApplyName(latest.Version+1) + ".pending"} {
			if _, err := state.read(name); err == nil {
				report.State = "applying"
			} else if !errors.Is(err, os.ErrNotExist) {
				return report, err
			}
		}
		return report, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, name := range []string{rotationIntentName(version + 1), rotationIntentName(version+1) + ".pending", rotationName(version+1) + ".pending"} {
		if _, err := state.read(name); err == nil {
			return &RotationReport{State: "preparing", RootVersion: version + 1}, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if version == 1 {
		return nil, nil
	}
	previous := bundle.Root
	if version > 2 {
		previous = latest.Roots[version-3]
	}
	r, _, err := readRotation(state, bundle, previous, version)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(r.Root, latest.currentRoot(bundle)) {
		return nil, errors.New("omega: active rotation differs from release history")
	}
	phase, err := appliedRotationState(state, history, version)
	if err != nil {
		return nil, err
	}
	return rotationReport(r, previous, phase), nil
}
