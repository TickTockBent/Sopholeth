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

type encryptedRotationPlan struct {
	Schema       int       `json:"schema"`
	Mode         string    `json:"mode"`
	Fingerprint  string    `json:"fingerprint"`
	RootVersion  int64     `json:"root_version"`
	PreviousRoot string    `json:"previous_root_sha256"`
	Created      time.Time `json:"created"`
	Role         string    `json:"role"`
	RootExpires  time.Time `json:"root_expires,omitzero"`
}
type encryptedGenerationKey struct {
	Schema     int    `json:"schema"`
	Plan       string `json:"plan_sha256"`
	Name       string `json:"name"`
	Public     string `json:"public_key"`
	Ciphertext []byte `json:"encrypted_key"`
}

func encryptedPlanName(version int64) string { return fmt.Sprintf("%d.encrypted-plan.json", version) }
func encryptedGenerationName(version int64, name string) string {
	return fmt.Sprintf("%d.%s-key.age.json", version, name)
}
func (p encryptedRotationPlan) binding(bundle bootstrap.Bundle, name string) keyBinding {
	return keyBinding{Transaction: digest(record(p)), Network: bundle.Network, Repository: bundle.Repository, Name: name, Generation: int(p.RootVersion)}
}
func planFromIntent(intent rotationIntent) encryptedRotationPlan {
	role := "online"
	if intent.Schema == 2 {
		role = "targets"
	}
	if intent.Schema == 3 {
		role = "root"
	}
	return encryptedRotationPlan{1, intent.Mode, intent.Fingerprint, intent.RootVersion, intent.PreviousRoot, intent.Created, role, intent.RootExpires}
}
func (c encryptedGenerationKey) validate(plan encryptedRotationPlan, name string) error {
	if c.Schema != 1 || c.Plan != digest(record(plan)) || c.Name != name || len(c.Ciphertext) == 0 || len(c.Ciphertext) > maxEncryptedKey {
		return errors.New("omega: encrypted generation differs from its preparation")
	}
	_, err := decodePublicKey(c.Public)
	return err
}
func (k *operatorKeys) generationKey(intent rotationIntent, name string) (string, error) {
	if intent.Custody != encryptedCustody || intent.Mode != k.mode() {
		return "", errors.New("omega: signer custody differs from this authority")
	}
	keys, err := membershipStore(k.home, k.bundle.Network, false)
	if err != nil {
		return "", err
	}
	defer keys.close()
	data, err := keys.read(encryptedGenerationName(intent.RootVersion, name))
	if err != nil {
		return "", err
	}
	if digest(data) != intent.KeyDigests[name] {
		return "", errors.New("omega: damaged encrypted signer generation; restore its backup")
	}
	var c encryptedGenerationKey
	if err := decodeRecord(data, &c); err != nil {
		return "", err
	}
	plan := planFromIntent(intent)
	if err := c.validate(plan, name); err != nil {
		return "", err
	}
	public, _ := decodePublicKey(c.Public)
	key, err := metadata.KeyFromPublicKey(public)
	if err != nil {
		return "", err
	}
	expected := intent.TargetsKey
	if intent.Schema == 3 {
		expected = intent.RootKeys[name]
	}
	if !bytes.Equal(record(key), record(expected)) {
		return "", errors.New("omega: encrypted signer public identity differs from handoff")
	}
	return k.unlock(c.Ciphertext, plan.binding(k.bundle, name), c.Public)
}

// One public clock/role reservation precedes every private generation. Complete
// key candidates survive retries unchanged; damage after handoff never allocates
// a replacement. Only encrypted operator files stay in the authority home.
func prepareEncryptedRotation(k *operatorKeys, state *store, previous []byte, version int64, role string, now time.Time) (rotationRecord, error) {
	var empty rotationRecord
	var existing *rotationIntent
	handedOff := false
	for _, output := range []string{rotationIntentName(version), rotationName(version)} {
		for _, suffix := range []string{"", ".pending"} {
			if _, err := state.root.Lstat(output + suffix); !errors.Is(err, os.ErrNotExist) {
				handedOff = true
			}
		}
	}
	for _, name := range []string{rotationIntentName(version), rotationIntentName(version) + ".pending"} {
		data, err := state.read(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return empty, err
		}
		if !json.Valid(data) && name != rotationIntentName(version) {
			continue
		}
		var intent rotationIntent
		if err := decodeRecord(data, &intent); err != nil {
			return empty, err
		}
		if err := intent.validate(k.bundle, previous, version); err != nil {
			return empty, err
		}
		if intent.Custody != encryptedCustody || intent.Mode != k.mode() || planFromIntent(intent).Role != role {
			return empty, errors.New("omega: version already belongs to another custody profile or role")
		}
		if existing != nil && !bytes.Equal(record(*existing), record(intent)) {
			return empty, errors.New("omega: conflicting rotation handoff records")
		}
		existing = &intent
	}
	// A complete preparation is an inspection/retry, not another signing session.
	if r, intent, err := readRotation(state, k.bundle, previous, version); err == nil {
		if intent.Custody != encryptedCustody || intent.Mode != k.mode() || planFromIntent(intent).Role != role {
			return empty, errors.New("omega: prepared rotation belongs to another custody profile or role")
		}
		return r, cleanPendingTwins(state, ".", state.read)
	}
	keys, err := membershipStore(k.home, k.bundle.Network, true)
	if err != nil {
		return empty, err
	}
	defer keys.close()
	plan, err := loadEncryptedPlan(k, keys, state, previous, version, role, now, existing)
	if err != nil {
		return empty, err
	}
	rootKeys, err := k.roots(state, previous)
	if err != nil {
		return empty, err
	}
	intent := rotationIntent{Schema: 4, Mode: k.mode(), Custody: encryptedCustody, Fingerprint: k.bundle.Fingerprint(), RootVersion: version, PreviousRoot: digest(previous), Created: plan.Created, KeyDigests: map[string]string{}}
	names := []string{"snapshot", "timestamp"}
	if role == "root" {
		intent.Schema = 3
		intent.RootKeys = map[string]*metadata.Key{}
		intent.RootExpires = plan.RootExpires
		names = keyNames[:3]
	}
	if role == "targets" {
		intent.Schema = 2
		names = []string{"targets"}
	}
	if role == "online" {
		intent.OnlineKeys = map[string]*metadata.Key{}
	}
	var successorKeys []string
	for _, name := range names {
		var private string
		var public *metadata.Key
		var hash string
		if role == "online" {
			private, public, hash, err = prepareOnlineGeneration(k, state, plan, name, handedOff)
		} else {
			private, public, hash, err = k.prepareGenerationKey(keys, plan, name, handedOff)
			if errors.Is(err, os.ErrNotExist) && role == "root" && existing != nil {
				// The public handoff fixes the unavailable third key; keep that identity.
				intent.RootKeys[name] = existing.RootKeys[name]
				intent.KeyDigests[name] = existing.KeyDigests[name]
				continue
			}
		}
		if err != nil {
			return empty, err
		}
		intent.KeyDigests[name] = hash
		if role == "root" {
			intent.RootKeys[name] = public
			successorKeys = append(successorKeys, private)
		} else if role == "targets" {
			intent.TargetsKey = public
		} else {
			intent.OnlineKeys[name] = public
		}
	}
	if err := intent.validate(k.bundle, previous, version); err != nil {
		return empty, err
	}
	if existing != nil && !bytes.Equal(record(*existing), record(intent)) {
		return empty, errors.New("omega: fixed generation differs from public rotation handoff")
	}
	if role == "root" && len(successorKeys) < 2 {
		return empty, errors.New("omega: successor root quorum unavailable; restore its encrypted backups")
	}
	if err := keys.phase("rotation:encrypted-keys-durable"); err != nil {
		return empty, err
	}
	result, err := finishRotation(state, k.bundle, previous, version, authority{Keys: rootKeys}, now, intent, successorKeys...)
	if err != nil {
		return empty, err
	}
	if err := cleanPendingTwins(keys, ".", keys.read); err != nil {
		return empty, err
	}
	return result, nil
}
func loadEncryptedPlan(k *operatorKeys, keys, state *store, previous []byte, version int64, role string, now time.Time, existing *rotationIntent) (encryptedRotationPlan, error) {
	var p encryptedRotationPlan
	name := encryptedPlanName(version)
	data, err := keys.read(name)
	committed := err == nil
	if errors.Is(err, os.ErrNotExist) {
		data, err = keys.read(name + ".pending")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return p, err
	}
	root, parseErr := metadata.Root().FromBytes(previous)
	if parseErr != nil {
		return p, parseErr
	}
	if err == nil && json.Valid(data) {
		if err := decodeRecord(data, &p); err != nil {
			return p, err
		}
	} else {
		if committed || existing != nil {
			return p, errors.New("omega: damaged encrypted preparation; restore it")
		}
		// Before a plan exists, no key or public handoff for this version may exist.
		entries, err := directoryNames(keys)
		if err != nil {
			return p, err
		}
		prefix := fmt.Sprintf("%d.", version)
		for _, entry := range entries {
			if len(entry) >= len(prefix) && entry[:len(prefix)] == prefix && entry != name+".pending" {
				return p, errors.New("omega: generation exists without its encrypted preparation; restore it")
			}
		}
		for _, output := range []string{rotationIntentName(version), rotationName(version)} {
			for _, suffix := range []string{"", ".pending"} {
				if _, err := state.root.Lstat(output + suffix); !errors.Is(err, os.ErrNotExist) {
					return p, errors.New("omega: handoff exists without encrypted preparation; restore it")
				}
			}
		}
		for _, name := range []string{"snapshot", "timestamp"} {
			for _, suffix := range []string{"", ".pending"} {
				if _, err := state.keyHome.root.Lstat(k.bundle.Network + ".online-keys/" + onlineKeyName(version, name) + suffix); !errors.Is(err, os.ErrNotExist) {
					return p, errors.New("omega: online generation exists without its preparation; restore it")
				}
			}
		}
		p = encryptedRotationPlan{Schema: 1, Mode: k.mode(), Fingerprint: k.bundle.Fingerprint(), RootVersion: version, PreviousRoot: digest(previous), Created: now.Truncate(time.Second), Role: role}
		if role == "root" {
			p.RootExpires = rootExpiry(root.Signed.Expires, p.Created)
		}
	}
	if p.Schema != 1 || p.Mode != k.mode() || p.Fingerprint != k.bundle.Fingerprint() || p.RootVersion != version || p.PreviousRoot != digest(previous) || p.Role != role || p.Created.IsZero() || now.Before(p.Created) || (role == "root" && !p.RootExpires.Equal(rootExpiry(root.Signed.Expires, p.Created))) || (role != "root" && !p.RootExpires.IsZero()) {
		return p, errors.New("omega: encrypted preparation differs from authority, role, or clock")
	}
	if existing != nil && !bytes.Equal(record(p), record(planFromIntent(*existing))) {
		return p, errors.New("omega: encrypted preparation differs from public handoff")
	}
	if err := keys.install(name, record(p)); err != nil {
		return p, err
	}
	if err := keys.syncDir(); err != nil {
		return p, err
	}
	return p, keys.phase("rotation:encrypted-plan-durable")
}
func (k *operatorKeys) prepareGenerationKey(keys *store, plan encryptedRotationPlan, name string, handedOff bool) (string, *metadata.Key, string, error) {
	filename := encryptedGenerationName(plan.RootVersion, name)
	data, err := keys.read(filename)
	committed := err == nil
	if errors.Is(err, os.ErrNotExist) {
		data, err = keys.read(filename + ".pending")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", nil, "", err
	}
	if errors.Is(err, os.ErrNotExist) && handedOff {
		return "", nil, "", os.ErrNotExist
	}
	var c encryptedGenerationKey
	var encoded string
	if err == nil && json.Valid(data) {
		if err := decodeRecord(data, &c); err != nil {
			return "", nil, "", err
		}
	} else {
		if committed || handedOff {
			return "", nil, "", errors.New("omega: damaged encrypted generation; restore its allocated key")
		}
		password, err := k.secret()
		if err != nil {
			return "", nil, "", err
		}
		ciphertext, err := k.codec.create(k.ctx, plan.binding(k.bundle, name), password)
		if err != nil {
			return "", nil, "", err
		}
		private, err := k.codec.unlock(k.ctx, ciphertext, plan.binding(k.bundle, name), password)
		if err != nil {
			return "", nil, "", err
		}
		c = encryptedGenerationKey{1, digest(record(plan)), name, base64.StdEncoding.EncodeToString(private.Public().(ed25519.PublicKey)), ciphertext}
		encoded = base64.StdEncoding.EncodeToString(private)
		clear(private)
	}
	if err := c.validate(plan, name); err != nil {
		return "", nil, "", err
	}
	if encoded == "" {
		encoded, err = k.unlock(c.Ciphertext, plan.binding(k.bundle, name), c.Public)
		if err != nil {
			return "", nil, "", err
		}
	}
	publicBytes, _ := decodePublicKey(c.Public)
	public, err := metadata.KeyFromPublicKey(publicBytes)
	if err != nil {
		return "", nil, "", err
	}
	data = record(c)
	if err := keys.install(filename, data); err != nil {
		return "", nil, "", err
	}
	if err := keys.syncDir(); err != nil {
		return "", nil, "", err
	}
	stored, err := keys.read(filename)
	if err != nil {
		return "", nil, "", err
	}
	if !bytes.Equal(stored, data) {
		return "", nil, "", errors.New("omega: encrypted generation failed read-back verification")
	}
	return encoded, public, digest(data), nil
}
func prepareOnlineGeneration(k *operatorKeys, state *store, plan encryptedRotationPlan, role string, handedOff bool) (string, *metadata.Key, string, error) {
	keys, err := onlineKeyStore(state.keyHome, k.bundle.Network, true)
	if err != nil {
		return "", nil, "", err
	}
	defer keys.close()
	name := onlineKeyName(plan.RootVersion, role)
	data, err := keys.read(name)
	committed := err == nil
	if errors.Is(err, os.ErrNotExist) {
		data, err = keys.read(name + ".pending")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", nil, "", err
	}
	var c onlineKeyMaterial
	if err == nil && json.Valid(data) {
		if err := decodeRecord(data, &c); err != nil {
			return "", nil, "", err
		}
	} else {
		if committed || handedOff {
			return "", nil, "", errors.New("omega: online generation missing or damaged after handoff; restore it")
		}
		_, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return "", nil, "", err
		}
		c = onlineKeyMaterial{1, k.mode(), k.bundle.Fingerprint(), plan.RootVersion, plan.PreviousRoot, role, base64.StdEncoding.EncodeToString(private)}
		clear(private)
	}
	if err := c.validate(k.bundle, k.mode(), plan.RootVersion, plan.PreviousRoot, role); err != nil {
		return "", nil, "", err
	}
	private, err := decodeKey(c.Key)
	if err != nil {
		return "", nil, "", err
	}
	public, err := metadata.KeyFromPublicKey(private.Public())
	clear(private)
	if err != nil {
		return "", nil, "", err
	}
	data = record(c)
	if err := keys.install(name, data); err != nil {
		return "", nil, "", err
	}
	if err := keys.syncDir(); err != nil {
		return "", nil, "", err
	}
	if err := cleanPendingTwins(keys, ".", keys.read); err != nil {
		return "", nil, "", err
	}
	verified, err := keys.read(name)
	if err != nil {
		return "", nil, "", err
	}
	if !bytes.Equal(verified, data) {
		return "", nil, "", errors.New("omega: online key read-back differs; restore it before handoff")
	}
	return c.Key, public, digest(data), nil
}
