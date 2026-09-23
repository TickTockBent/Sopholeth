package omega

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
	"sopholeth/internal/trust/bootstrap"
)

var errOfflineHandoff = errors.New("omega: offline targets handoff incomplete")

// Immutable disposable generations live beside the original offline authority,
// never in the publication journal or the scheduler's custody home.
type membershipCustody struct {
	Schema       int       `json:"schema"`
	Mode         string    `json:"mode"`
	Fingerprint  string    `json:"fingerprint"`
	RootVersion  int64     `json:"root_version"`
	PreviousRoot string    `json:"previous_root_sha256"`
	Created      time.Time `json:"created"`
	Key          string    `json:"private_key"`
}

func membershipKeyName(version int64) string { return fmt.Sprintf("%d.targets-key.json", version) }
func rotationTargetsName(version int64) string {
	return fmt.Sprintf("%d.rotation-targets.json", version)
}

func membershipStore(home *store, network string, create bool) (*store, error) {
	name := network + ".rotations"
	if create {
		if err := home.root.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if err := home.syncDir(); err != nil {
			return nil, err
		}
	}
	return home.subdir(name)
}

func (c membershipCustody) intent(bundle bootstrap.Bundle, previous []byte, version int64) (rotationIntent, error) {
	var out rotationIntent
	if c.Schema != 1 || c.Mode != "disposable" {
		return out, errors.New("omega: invalid disposable membership custody")
	}
	private, err := decodeKey(c.Key)
	if err != nil {
		return out, errors.New("omega: invalid membership private key; restore its generation")
	}
	key, err := metadata.KeyFromPublicKey(private.Public())
	if err != nil {
		return out, err
	}
	out = rotationIntent{Schema: 2, Mode: c.Mode, Fingerprint: c.Fingerprint,
		RootVersion: c.RootVersion, PreviousRoot: c.PreviousRoot, Created: c.Created, TargetsKey: key}
	return out, out.validate(bundle, previous, version)
}

func prepareMembershipRotation(home, state *store, bundle bootstrap.Bundle, previous []byte, version int64, a authority, now time.Time) (rotationRecord, error) {
	var empty rotationRecord
	for _, name := range []string{rotationIntentName(version), rotationIntentName(version) + ".pending"} {
		data, err := state.read(name)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return empty, err
		}
		if err == nil && json.Valid(data) {
			var existing rotationIntent
			if err := decodeRecord(data, &existing); err != nil {
				return empty, err
			}
			if existing.Schema != 2 {
				return empty, errors.New("omega: root version is reserved for another rotation role; retry its original --role")
			}
		}
	}
	keys, err := membershipStore(home, bundle.Network, true)
	if err != nil {
		return empty, err
	}
	defer keys.close()
	name := membershipKeyName(version)
	data, err := keys.read(name)
	committed := err == nil
	if errors.Is(err, os.ErrNotExist) {
		data, err = keys.read(name + ".pending")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return empty, err
	}
	var c membershipCustody
	if err == nil && json.Valid(data) {
		if err := decodeRecord(data, &c); err != nil {
			return empty, err
		}
	} else {
		if committed {
			return empty, errors.New("omega: corrupt committed membership custody; restore it")
		}
		for _, output := range []string{rotationIntentName(version), rotationIntentName(version) + ".pending", rotationName(version), rotationName(version) + ".pending"} {
			if _, err := state.root.Lstat(output); !errors.Is(err, os.ErrNotExist) {
				return empty, errors.New("omega: membership custody missing or torn after handoff; restore it")
			}
		}
		_, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return empty, err
		}
		c = membershipCustody{Schema: 1, Mode: "disposable", Fingerprint: bundle.Fingerprint(),
			RootVersion: version, PreviousRoot: digest(previous), Created: now.Truncate(time.Second),
			Key: base64.StdEncoding.EncodeToString(private)}
	}
	intent, err := c.intent(bundle, previous, version)
	if err != nil {
		return empty, err
	}
	for _, name := range []string{rotationIntentName(version), rotationIntentName(version) + ".pending"} {
		data, err := state.read(name)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return empty, err
		}
		if err == nil && json.Valid(data) && !bytes.Equal(data, record(intent)) {
			return empty, errors.New("omega: public handoff differs from offline membership custody; restore the matching records")
		}
	}
	if now.Before(c.Created) {
		return empty, errors.New("omega: clock predates membership key preparation")
	}
	if err := keys.install(name, record(c)); err != nil {
		return empty, err
	}
	if err := keys.syncDir(); err != nil {
		return empty, err
	}
	if err := keys.phase("rotation:offline-key-durable"); err != nil {
		return empty, err
	}
	r, err := finishRotation(state, bundle, previous, version, a, now, intent)
	if err != nil {
		return empty, err
	}
	return r, cleanPendingTwins(keys, ".", keys.read)
}

func membershipPrivateKey(home, state *store, bundle bootstrap.Bundle, previous []byte, version int64) (string, error) {
	_, intent, err := readRotation(state, bundle, previous, version)
	if err != nil {
		return "", err
	}
	keys, err := membershipStore(home, bundle.Network, false)
	if err != nil {
		return "", err
	}
	defer keys.close()
	data, err := keys.read(membershipKeyName(version))
	if err != nil {
		return "", err
	}
	var c membershipCustody
	if err := decodeRecord(data, &c); err != nil {
		return "", err
	}
	expected, err := c.intent(bundle, previous, version)
	if err != nil {
		return "", err
	}
	if !bytes.Equal(record(expected), record(intent)) {
		return "", errors.New("omega: offline membership custody differs from prepared transition")
	}
	return c.Key, nil
}

func activeMembershipKey(home, state *store, bundle bootstrap.Bundle, latest release, initial string) (string, error) {
	for i := len(latest.Roots) - 1; i >= 0; i-- {
		previous := bundle.Root
		if i > 0 {
			previous = latest.Roots[i-1]
		}
		r, intent, err := readRotation(state, bundle, previous, int64(i)+2)
		if err != nil {
			return "", err
		}
		if !bytes.Equal(r.Root, latest.Roots[i]) {
			return "", errors.New("omega: membership generation differs from release history")
		}
		if intent.Schema == 2 {
			return membershipPrivateKey(home, state, bundle, previous, int64(i)+2)
		}
	}
	return initial, nil
}

// The apply reservation fixes the release and signing clock before offline
// signing. Once these public bytes are durable, the scheduler can finish the
// transition without opening the offline home or extending membership approval.
func prepareRotationTargets(home, state *store, bundle bootstrap.Bundle, latest release, r rotationRecord, now time.Time) error {
	name := rotationApplyName(latest.Version + 1)
	data, err := state.read(name)
	if errors.Is(err, os.ErrNotExist) {
		data, err = state.read(name + ".pending")
	}
	if err != nil {
		return err
	}
	var intent rotationApply
	if err := decodeRecord(data, &intent); err != nil {
		return err
	}
	_, preparation, err := readRotation(state, bundle, latest.currentRoot(bundle), r.RootVersion)
	if err != nil {
		return err
	}
	if !validTimestampDays(intent.TimestampDays) || intent.Schema != 1 || intent.Version != latest.Version+1 || intent.Version > maxReleases || intent.Previous != digest(record(latest)) ||
		intent.RootVersion != latest.versions().Root+1 || intent.RootVersion != r.RootVersion || intent.RootSHA256 != digest(r.Root) ||
		intent.Created.IsZero() || intent.Created.Before(latest.Created) || intent.Created.Before(preparation.Created) || now.Before(intent.Created) {
		return errors.New("omega: targets apply reservation differs from history, transition, or clock")
	}
	if err := state.install(name, data); err != nil {
		return err
	}
	if err := state.syncDir(); err != nil {
		return err
	}
	if err := state.phase("rotation:apply-durable"); err != nil {
		return err
	}
	name = rotationTargetsName(intent.Version)
	for _, candidate := range []string{name, name + ".pending"} {
		data, err := state.read(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if candidate != name && !json.Valid(data) {
			continue
		}
		if err := validateRotationTargets(data, latest, r, intent.Version, intent.Created); err != nil {
			return err
		}
		return state.install(name, data)
	}
	var private string
	if preparation.Schema == 3 {
		// Root application explicitly reapproves the unchanged membership using
		// its current offline signer. Root expiry cannot renew it implicitly.
		current, openErr := home.subdir(bundle.Network)
		if openErr != nil {
			return openErr
		}
		defer current.close()
		data, readErr := current.read("authority.json")
		if readErr != nil {
			return readErr
		}
		var a authority
		if err := decodeRecord(data, &a); err != nil {
			return err
		}
		private, err = activeMembershipKey(home, state, bundle, latest, a.Keys["targets"])
	} else {
		private, err = membershipPrivateKey(home, state, bundle, latest.currentRoot(bundle), r.RootVersion)
	}
	if err != nil {
		return err
	}
	targets, err := metadata.Targets().FromBytes(latest.Targets)
	if err != nil {
		return err
	}
	targets.Signatures = nil
	targets.Signed.Version = intent.Version
	if preparation.Schema == 3 {
		targets.Signed.Expires = minTime(intent.Created.Add(90*24*time.Hour), preparation.RootExpires)
	}
	raw, err := signMetadata(targets, private)
	if err != nil {
		return err
	}
	if err := validateRotationTargets(raw, latest, r, intent.Version, intent.Created); err != nil {
		return err
	}
	if err := state.install(name, raw); err != nil {
		return err
	}
	if err := state.syncDir(); err != nil {
		return err
	}
	return state.phase("rotation:targets-durable")
}

func sameTargetsApproval(previous, next []byte, version int64) error {
	old, err := metadata.Targets().FromBytes(previous)
	if err != nil {
		return err
	}
	targets, err := metadata.Targets().FromBytes(next)
	if err != nil {
		return err
	}
	if targets.Signed.Version != version {
		return errors.New("omega: targets handoff has the wrong release version")
	}
	targets.Signed.Version = old.Signed.Version
	if !bytes.Equal(record(targets.Signed), record(old.Signed)) {
		return errors.New("omega: targets rotation changed membership approval or its expiration")
	}
	return nil
}

func validateRotationTargets(data []byte, latest release, r rotationRecord, version int64, created time.Time) error {
	root, err := metadata.Root().FromBytes(r.Root)
	if err != nil {
		return err
	}
	targets, err := metadata.Targets().FromBytes(data)
	if err != nil {
		return err
	}
	if err := continuedTargetsApproval(latest.Targets, data, version, created, root); err != nil {
		return err
	}
	return root.VerifyDelegate(metadata.TARGETS, targets)
}

func readRotationTargets(state *store, latest release, r rotationRecord, version int64, created time.Time) ([]byte, error) {
	name := rotationTargetsName(version)
	data, err := state.read(name)
	if errors.Is(err, os.ErrNotExist) {
		data, err = state.read(name + ".pending")
	}
	if err != nil {
		return nil, err
	}
	if err := validateRotationTargets(data, latest, r, version, created); err != nil {
		return nil, err
	}
	if err := state.install(name, data); err != nil {
		return nil, err
	}
	return data, state.syncDir()
}
