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

// A public plan fixes the clock before any private generation is created.
// Each successor signer has a separate immutable offline file. This remains
// disposable custody, not independent production custody on one filesystem.
type rootPreparation struct {
	Schema       int       `json:"schema"`
	Mode         string    `json:"mode"`
	Fingerprint  string    `json:"fingerprint"`
	RootVersion  int64     `json:"root_version"`
	PreviousRoot string    `json:"previous_root_sha256"`
	Created      time.Time `json:"created"`
	Expires      time.Time `json:"root_expires"`
}

func rootPlanName(n int64) string             { return fmt.Sprintf("%d.root-plan.json", n) }
func rootKeyName(n int64, name string) string { return fmt.Sprintf("%d.%s-key.json", n, name) }
func rootExpiry(previous, created time.Time) time.Time {
	expires := created.Add(rootLifetime)
	if !expires.After(previous) {
		expires = previous.Add(time.Second)
	}
	return expires
}

func validateRootKeys(keys map[string]*metadata.Key, old *metadata.Metadata[metadata.RootType]) error {
	if len(keys) != 3 {
		return errors.New("omega: successor requires three root keys")
	}
	seen := map[string]bool{}
	for _, name := range keyNames[:3] {
		key := keys[name]
		if key == nil {
			return errors.New("omega: missing successor root public key")
		}
		public, err := key.ToPublicKey()
		if err != nil {
			return err
		}
		if _, ok := public.(ed25519.PublicKey); !ok {
			return errors.New("omega: root keys must be Ed25519")
		}
		id, err := key.ID()
		if err != nil || seen[id] || old.Signed.Keys[id] != nil {
			return errors.New("omega: successor root keys must be new and distinct")
		}
		seen[id] = true
	}
	return nil
}

func rootSuccessor(old, next *metadata.Metadata[metadata.RootType]) error {
	assignment := next.Signed.Roles[metadata.ROOT]
	previous := old.Signed.Roles[metadata.ROOT]
	if assignment == nil || previous == nil || assignment.Threshold != 2 || len(assignment.KeyIDs) != 3 ||
		previous.Threshold != 2 || len(previous.KeyIDs) != 3 || len(next.Signed.Keys) != 6 ||
		next.Signed.Version != old.Signed.Version+1 || !next.Signed.Expires.After(old.Signed.Expires) {
		return errors.New("omega: root successor must retain a 2-of-3 quorum and extend expiry")
	}
	keys := map[string]*metadata.Key{}
	for i, id := range assignment.KeyIDs {
		key := next.Signed.Keys[id]
		if key == nil {
			return errors.New("omega: root successor key missing")
		}
		computed, err := key.ID()
		if err != nil || computed != id {
			return errors.New("omega: invalid successor root key identity")
		}
		keys[keyNames[i]] = key
	}
	if err := validateRootKeys(keys, old); err != nil {
		return err
	}
	raw, err := next.ToBytes(false)
	if err != nil {
		return err
	}
	normalized, err := metadata.Root().FromBytes(raw)
	if err != nil {
		return err
	}
	for _, id := range assignment.KeyIDs {
		delete(normalized.Signed.Keys, id)
	}
	for _, id := range previous.KeyIDs {
		normalized.Signed.Keys[id] = old.Signed.Keys[id]
	}
	normalized.Signed.Roles[metadata.ROOT] = previous
	normalized.Signed.Version, normalized.Signed.Expires = old.Signed.Version, old.Signed.Expires
	if !bytes.Equal(record(normalized.Signed), record(old.Signed)) {
		return errors.New("omega: root rotation changed unrelated roles or policy")
	}
	return nil
}

// A private preparation reserves its version even before public handoff.
func checkOfflineReservation(home *store, network string, version int64, role string) error {
	names := []string{}
	if role != "targets" {
		names = append(names, membershipKeyName(version))
	}
	if role != "root" {
		names = append(names, rootPlanName(version))
		for _, key := range keyNames[:3] {
			names = append(names, rootKeyName(version, key))
		}
	}
	for _, name := range names {
		for _, suffix := range []string{"", ".pending"} {
			if _, err := home.root.Lstat(network + ".rotations/" + name + suffix); !errors.Is(err, os.ErrNotExist) {
				return errors.New("omega: offline custody reserves this version for another role; retry its original --role")
			}
		}
	}
	return nil
}

func readRootPrivate(keys *store, intent rotationIntent, name string) (string, error) {
	data, err := keys.read(rootKeyName(intent.RootVersion, name))
	if err != nil {
		return "", err
	}
	var c membershipCustody
	if err := decodeRecord(data, &c); err != nil {
		return "", err
	}
	if c.Schema != 1 || c.Mode != "disposable" || c.Fingerprint != intent.Fingerprint || c.RootVersion != intent.RootVersion ||
		c.PreviousRoot != intent.PreviousRoot || !c.Created.Equal(intent.Created) {
		return "", errors.New("omega: root signer differs from its generation")
	}
	private, err := decodeKey(c.Key)
	if err != nil {
		return "", err
	}
	public, err := metadata.KeyFromPublicKey(private.Public())
	if err != nil {
		return "", err
	}
	if !bytes.Equal(record(public), record(intent.RootKeys[name])) {
		return "", errors.New("omega: root signer differs from prepared public key")
	}
	return c.Key, nil
}

func rootGenerationKeys(home *store, bundle bootstrap.Bundle, intent rotationIntent) (map[string]string, error) {
	keys, err := membershipStore(home, bundle.Network, false)
	if err != nil {
		return nil, err
	}
	defer keys.close()
	out := map[string]string{}
	for _, name := range keyNames[:3] {
		private, err := readRootPrivate(keys, intent, name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[name] = private
	}
	if len(out) < 2 {
		return nil, errors.New("omega: root quorum unavailable; restore protected signer backups, never generate replacement keys for this version")
	}
	return out, nil
}

func activeRootKeys(home, state *store, bundle bootstrap.Bundle, previous []byte, a authority) (map[string]string, error) {
	root, err := metadata.Root().FromBytes(previous)
	if err != nil {
		return nil, err
	}
	raw := bundle.Root
	var generation rotationIntent
	for version := int64(2); version <= root.Signed.Version; version++ {
		r, intent, err := readRotation(state, bundle, raw, version)
		if err != nil {
			return nil, err
		}
		if intent.Schema == 3 {
			generation = intent
		}
		raw = r.Root
	}
	if !bytes.Equal(raw, previous) {
		return nil, errors.New("omega: root custody chain differs from history")
	}
	if generation.Schema == 3 {
		return rootGenerationKeys(home, bundle, generation)
	}
	out := map[string]string{}
	for _, name := range keyNames[:3] {
		out[name] = a.Keys[name]
	}
	return out, nil
}

func prepareRootRotation(home, state *store, bundle bootstrap.Bundle, previous []byte, version int64, a authority, now time.Time) (rotationRecord, error) {
	var empty rotationRecord
	var existing *rotationIntent
	for _, name := range []string{rotationIntentName(version), rotationIntentName(version) + ".pending"} {
		data, err := state.read(name)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return empty, err
		}
		if err == nil && json.Valid(data) {
			var intent rotationIntent
			if err := decodeRecord(data, &intent); err != nil {
				return empty, err
			}
			if intent.Schema != 3 {
				return empty, errors.New("omega: root version belongs to another rotation role")
			}
			if err := intent.validate(bundle, previous, version); err != nil {
				return empty, err
			}
			if existing != nil && !bytes.Equal(record(*existing), record(intent)) {
				return empty, errors.New("omega: root handoff records disagree")
			}
			existing = &intent
		}
	}
	keys, err := membershipStore(home, bundle.Network, true)
	if err != nil {
		return empty, err
	}
	defer keys.close()
	root, err := metadata.Root().FromBytes(previous)
	if err != nil {
		return empty, err
	}
	planName := rootPlanName(version)
	data, err := keys.read(planName)
	committed := err == nil
	if errors.Is(err, os.ErrNotExist) {
		data, err = keys.read(planName + ".pending")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return empty, err
	}
	var plan rootPreparation
	if err == nil && json.Valid(data) {
		if err := decodeRecord(data, &plan); err != nil {
			return empty, err
		}
	} else {
		if committed || existing != nil {
			return empty, errors.New("omega: root preparation damaged; restore it")
		}
		for _, name := range keyNames[:3] {
			for _, suffix := range []string{"", ".pending"} {
				if _, err := keys.root.Lstat(rootKeyName(version, name) + suffix); !errors.Is(err, os.ErrNotExist) {
					return empty, errors.New("omega: root preparation missing after key creation; restore it")
				}
			}
		}
		for _, name := range []string{rotationIntentName(version), rotationIntentName(version) + ".pending", rotationName(version), rotationName(version) + ".pending"} {
			if _, err := state.root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
				return empty, errors.New("omega: root preparation missing after handoff; restore it")
			}
		}
		created := now.Truncate(time.Second)
		plan = rootPreparation{1, "disposable", bundle.Fingerprint(), version, digest(previous), created, rootExpiry(root.Signed.Expires, created)}
	}
	if plan.Schema != 1 || plan.Mode != "disposable" || plan.Fingerprint != bundle.Fingerprint() || plan.RootVersion != version || plan.PreviousRoot != digest(previous) ||
		plan.Created.IsZero() || now.Before(plan.Created) || !plan.Expires.Equal(rootExpiry(root.Signed.Expires, plan.Created)) {
		return empty, errors.New("omega: invalid root preparation or clock")
	}
	if err := keys.install(planName, record(plan)); err != nil {
		return empty, err
	}
	if err := keys.syncDir(); err != nil {
		return empty, err
	}
	if err := keys.phase("rotation:root-plan-durable"); err != nil {
		return empty, err
	}
	intent := rotationIntent{Schema: 3, Mode: "disposable", Fingerprint: plan.Fingerprint, RootVersion: version,
		PreviousRoot: plan.PreviousRoot, Created: plan.Created, RootExpires: plan.Expires, RootKeys: map[string]*metadata.Key{}}
	var signers []string
	for _, name := range keyNames[:3] {
		fileName := rootKeyName(version, name)
		data, err := keys.read(fileName)
		committed := err == nil
		if errors.Is(err, os.ErrNotExist) {
			data, err = keys.read(fileName + ".pending")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return empty, err
		}
		if errors.Is(err, os.ErrNotExist) && existing != nil {
			intent.RootKeys[name] = existing.RootKeys[name]
			continue // One unavailable signer is recoverable; never replace it.
		}
		var c membershipCustody
		if err == nil && json.Valid(data) {
			if err := decodeRecord(data, &c); err != nil {
				return empty, err
			}
		} else {
			if committed || existing != nil {
				return empty, errors.New("omega: damaged root signer; restore its generation")
			}
			// Even a torn public handoff means every private key was durable.
			for _, output := range []string{rotationIntentName(version), rotationIntentName(version) + ".pending", rotationName(version), rotationName(version) + ".pending"} {
				if _, err := state.root.Lstat(output); !errors.Is(err, os.ErrNotExist) {
					return empty, errors.New("omega: root signer missing after handoff; restore it")
				}
			}
			_, private, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				return empty, err
			}
			c = membershipCustody{1, "disposable", plan.Fingerprint, version, plan.PreviousRoot, plan.Created, base64.StdEncoding.EncodeToString(private)}
		}
		if c.Schema != 1 || c.Mode != "disposable" || c.Fingerprint != plan.Fingerprint || c.RootVersion != version || c.PreviousRoot != plan.PreviousRoot || !c.Created.Equal(plan.Created) {
			return empty, errors.New("omega: root signer record differs from preparation")
		}
		private, err := decodeKey(c.Key)
		if err != nil {
			return empty, err
		}
		public, err := metadata.KeyFromPublicKey(private.Public())
		if err != nil {
			return empty, err
		}
		intent.RootKeys[name] = public
		if existing != nil && !bytes.Equal(record(existing.RootKeys[name]), record(public)) {
			return empty, errors.New("omega: root signer differs from public handoff")
		}
		if err := keys.install(fileName, record(c)); err != nil {
			return empty, err
		}
		if err := keys.syncDir(); err != nil {
			return empty, err
		}
		signers = append(signers, c.Key)
	}
	if err := intent.validate(bundle, previous, version); err != nil {
		return empty, err
	}
	if existing != nil && !bytes.Equal(record(intent), record(*existing)) {
		return empty, errors.New("omega: root handoff differs from offline preparation")
	}
	if len(signers) < 2 {
		return empty, errors.New("omega: successor root quorum unavailable; restore its keys")
	}
	if err := keys.phase("rotation:root-keys-durable"); err != nil {
		return empty, err
	}
	r, err := finishRotation(state, bundle, previous, version, a, now, intent, signers...)
	if err != nil {
		return empty, err
	}
	return r, cleanPendingTwins(keys, ".", keys.read)
}
