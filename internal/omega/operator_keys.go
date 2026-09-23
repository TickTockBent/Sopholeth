package omega

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"os"

	"github.com/theupdateframework/go-tuf/v2/metadata"
	"sopholeth/internal/trust/bootstrap"
)

type PassphraseFunc func(context.Context, bool) ([]byte, error)

func custodyMode(disposable bool) string {
	if disposable {
		return "disposable"
	}
	return "production"
}
func validCustodyMode(mode string) bool { return mode == "disposable" || mode == "production" }
func requireCustodyMode(mode string, disposable bool) error {
	if mode != custodyMode(disposable) {
		return errors.New("omega: --disposable must match the existing authority's mode; custody cannot be relabeled")
	}
	return nil
}
func selectedCodec(c keyCodec) keyCodec {
	if c.workFactor == 0 {
		return defaultKeyCodec()
	}
	return c
}

// A command-local key session unlocks only the active signers it needs. No
// decrypted authority record is serialized or handed to the renewal service.
type operatorKeys struct {
	ctx           context.Context
	home, current *store
	bundle        bootstrap.Bundle
	public        *publicAuthority
	legacy        authority
	prompt        PassphraseFunc
	password      []byte
	codec         keyCodec
}

func openOperatorKeys(ctx context.Context, home, current *store, bundle bootstrap.Bundle, prompt PassphraseFunc, codec keyCodec) (*operatorKeys, error) {
	k := &operatorKeys{ctx: ctx, home: home, current: current, bundle: bundle, prompt: prompt, codec: selectedCodec(codec)}
	data, err := current.read("authority.json")
	if err != nil {
		return nil, err
	}
	if authoritySchema(data) == 2 {
		p, err := current.readPublicAuthority()
		if err != nil {
			return nil, err
		}
		k.public = &p
	} else if err := decodeRecord(data, &k.legacy); err != nil {
		return nil, err
	}
	return k, nil
}
func (k *operatorKeys) close() { clear(k.password); k.password = nil }
func (k *operatorKeys) mode() string {
	if k.public != nil {
		return k.public.Mode
	}
	return k.legacy.Mode
}
func (k *operatorKeys) secret() ([]byte, error) {
	if k.password == nil {
		if k.prompt == nil {
			return nil, errors.New("omega: signing requires an interactive key passphrase")
		}
		password, err := k.prompt(k.ctx, false)
		if err != nil {
			return nil, err
		}
		k.password = password
	}
	return k.password, nil
}
func (k *operatorKeys) unlock(data []byte, binding keyBinding, expected string) (string, error) {
	password, err := k.secret()
	if err != nil {
		return "", err
	}
	private, err := k.codec.unlock(k.ctx, data, binding, password)
	if err != nil {
		return "", fmt.Errorf("omega: unlock %s generation %d: %w", binding.Name, binding.Generation, err)
	}
	defer clear(private)
	if base64.StdEncoding.EncodeToString(private.Public().(ed25519.PublicKey)) != expected {
		return "", errors.New("omega: encrypted signer differs from the authorized public key")
	}
	return base64.StdEncoding.EncodeToString(private), nil
}
func (k *operatorKeys) initial(name string) (string, error) {
	if k.public == nil {
		return k.legacy.Keys[name], nil
	}
	data, err := k.current.read(encryptedKeyName(name))
	if err != nil {
		return "", err
	}
	if digest(data) != k.public.KeyDigests[name] {
		return "", fmt.Errorf("omega: damaged %s key; restore its encrypted backup", name)
	}
	return k.unlock(data, k.public.binding(name), k.public.Keys[name])
}

// Validate public handoffs without requiring any retained private generation.
func rotationHistory(state *store, bundle bootstrap.Bundle, previous []byte) ([]rotationIntent, error) {
	root, err := metadata.Root().FromBytes(previous)
	if err != nil {
		return nil, err
	}
	raw := bundle.Root
	var intents []rotationIntent
	for v := int64(2); v <= root.Signed.Version; v++ {
		r, intent, err := readRotation(state, bundle, raw, v)
		if err != nil {
			return nil, err
		}
		intents = append(intents, intent)
		raw = r.Root
	}
	if !bytes.Equal(raw, previous) {
		return nil, errors.New("omega: rotation history differs from the active root")
	}
	return intents, nil
}
func (k *operatorKeys) roots(state *store, previous []byte) (map[string]string, error) {
	if k.public == nil {
		return activeRootKeys(k.home, state, k.bundle, previous, k.legacy)
	}
	history, err := rotationHistory(state, k.bundle, previous)
	if err != nil {
		return nil, err
	}
	var generation *rotationIntent
	for i := range history {
		if history[i].Custody != encryptedCustody || history[i].Mode != k.mode() {
			return nil, errors.New("omega: rotation custody profile differs from authority")
		}
		if history[i].Schema == 3 {
			generation = &history[i]
		}
	}
	keys := map[string]string{}
	for _, name := range keyNames[:3] {
		var key string
		if generation == nil {
			key, err = k.initial(name)
		} else {
			key, err = k.generationKey(*generation, name)
		}
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		keys[name] = key
		if len(keys) == 2 {
			break
		}
	}
	if len(keys) < 2 {
		return nil, errors.New("omega: current root quorum unavailable; restore matching encrypted backups")
	}
	return keys, nil
}
func (k *operatorKeys) membership(state *store, latest release) (string, error) {
	if k.public == nil {
		return activeMembershipKey(k.home, state, k.bundle, latest, k.legacy.Keys["targets"])
	}
	history, err := rotationHistory(state, k.bundle, latest.currentRoot(k.bundle))
	if err != nil {
		return "", err
	}
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Custody != encryptedCustody || history[i].Mode != k.mode() {
			return "", errors.New("omega: rotation custody profile differs from authority")
		}
		if history[i].Schema == 2 {
			return k.generationKey(history[i], "targets")
		}
	}
	return k.initial("targets")
}
