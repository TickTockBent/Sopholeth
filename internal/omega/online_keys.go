package omega

import (
	"errors"
	"fmt"
	"os"

	"sopholeth/internal/trust/bootstrap"
)

// Service-owned files contain only snapshot/timestamp secrets. Public custody
// records and rotation intents bind their exact bytes without embedding them.
type onlineKeyMaterial struct {
	Schema       int    `json:"schema"`
	Mode         string `json:"mode"`
	Fingerprint  string `json:"fingerprint"`
	Generation   int64  `json:"generation"`
	PreviousRoot string `json:"previous_root_sha256"`
	Role         string `json:"role"`
	Key          string `json:"private_key"`
}

func onlineKeyName(version int64, role string) string {
	return fmt.Sprintf("%d.%s-key.json", version, role)
}
func onlineKeyStore(home *store, network string, create bool) (*store, error) {
	if home == nil {
		return nil, errors.New("omega: online key home is unavailable")
	}
	name := network + ".online-keys"
	if create {
		if err := home.mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if err := home.syncDir(); err != nil {
			return nil, err
		}
	}
	return home.subdir(name)
}
func (c onlineKeyMaterial) validate(bundle bootstrap.Bundle, mode string, generation int64, previous, role string) error {
	if c.Schema != 1 || !validCustodyMode(mode) || c.Mode != mode || c.Fingerprint != bundle.Fingerprint() || c.Generation != generation || c.PreviousRoot != previous || c.Role != role || (role != "snapshot" && role != "timestamp") {
		return errors.New("omega: online key file differs from its authority or generation")
	}
	_, err := decodeKey(c.Key)
	return err
}
func installOnlineKeys(home *store, bundle bootstrap.Bundle, mode string, generation int64, previous string, keys map[string]string) (map[string]string, error) {
	out, err := onlineKeyStore(home, bundle.Network, true)
	if err != nil {
		return nil, err
	}
	defer out.close()
	hashes := map[string]string{}
	for _, role := range []string{"snapshot", "timestamp"} {
		c := onlineKeyMaterial{1, mode, bundle.Fingerprint(), generation, previous, role, keys[role]}
		if err := c.validate(bundle, mode, generation, previous, role); err != nil {
			return nil, err
		}
		data := record(c)
		if err := out.install(onlineKeyName(generation, role), data); err != nil {
			return nil, err
		}
		hashes[role] = digest(data)
	}
	if _, err := readOnlineKeys(home, bundle, mode, generation, previous, hashes); err != nil {
		return nil, err
	}
	if err := cleanPendingTwins(out, ".", out.read); err != nil {
		return nil, err
	}
	return hashes, out.syncDir()
}
func readOnlineKeys(home *store, bundle bootstrap.Bundle, mode string, generation int64, previous string, hashes map[string]string) (map[string]string, error) {
	if len(hashes) != 2 {
		return nil, errors.New("omega: missing online key digests")
	}
	in, err := onlineKeyStore(home, bundle.Network, false)
	if err != nil {
		return nil, err
	}
	defer in.close()
	keys := map[string]string{}
	for _, role := range []string{"snapshot", "timestamp"} {
		data, err := in.read(onlineKeyName(generation, role))
		if err != nil {
			return nil, err
		}
		if digest(data) != hashes[role] {
			return nil, errors.New("omega: damaged online key file; restore its generation")
		}
		var c onlineKeyMaterial
		if err := decodeRecord(data, &c); err != nil {
			return nil, err
		}
		if err := c.validate(bundle, mode, generation, previous, role); err != nil {
			return nil, err
		}
		keys[role] = c.Key
	}
	return keys, nil
}
func (c renewalCustody) initialKeys(home *store) (map[string]string, error) {
	if c.Schema == 1 {
		return c.Keys, nil
	}
	keys, err := readOnlineKeys(home, c.Bundle, c.Mode, 1, "", c.KeyDigests)
	if err != nil {
		return nil, err
	}
	return keys, verifyOnlineKeys(keys, c.Bundle.Root)
}
func (c renewalCustody) activeKeys(home, state *store, latest release) (map[string]string, error) {
	intents, err := rotationHistory(state, c.Bundle, latest.currentRoot(c.Bundle))
	if err != nil {
		return nil, err
	}
	for _, intent := range intents {
		if intent.Mode != c.Mode || intent.Custody != c.Custody {
			return nil, errors.New("omega: renewal history has a different custody profile")
		}
	}
	keys, err := activeOnlineKeys(state, c.Bundle, latest, nil)
	if err != nil || keys != nil {
		return keys, err
	}
	return c.initialKeys(home)
}
