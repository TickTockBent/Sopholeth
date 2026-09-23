package omega

import (
	"errors"
	"fmt"
	"os"

	"sopholeth/internal/trust/bootstrap"
)

// Report and check the active generations, not retired initialization keys.
// A nil session inspects public bindings and ciphertext/file digests only.
func inspectEncryptedKeys(home, current, online *store, bundle bootstrap.Bundle, report Report, session *operatorKeys) (Report, error) {
	p, err := current.readPublicAuthority()
	if err != nil {
		return report, err
	}
	var rootGeneration, targetsGeneration, onlineGeneration *rotationIntent
	var latest release
	state, _, err := openPublication(online, bundle, nil, false)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return report, err
	}
	if err == nil {
		defer state.close()
		history, err := loadReleases(state, bundle)
		if err != nil {
			return report, err
		}
		if len(history) > 0 {
			latest = history[len(history)-1]
			if err := applyReleaseRoot(&report, bundle, latest); err != nil {
				return report, err
			}
			intents, err := rotationHistory(state, bundle, latest.currentRoot(bundle))
			if err != nil {
				return report, err
			}
			for i := range intents {
				intent := &intents[i]
				if intent.Custody != encryptedCustody || intent.Mode != p.Mode {
					return report, errors.New("omega: active history has a different custody profile")
				}
				switch intent.Schema {
				case 2:
					targetsGeneration = intent
				case 3:
					rootGeneration = intent
				case 4:
					onlineGeneration = intent
				}
			}
		}
	}
	report.KeyFiles = map[string]string{}
	for _, name := range keyNames {
		var generation *rotationIntent
		switch name {
		case "root-1", "root-2", "root-3":
			generation = rootGeneration
		case "targets":
			generation = targetsGeneration
		default:
			generation = onlineGeneration
		}
		var data []byte
		var readErr error
		expected := p.KeyDigests[name]
		protected := (name == "snapshot" || name == "timestamp") && online != home
		if protected {
			version := int64(1)
			if generation != nil {
				version = generation.RootVersion
				expected = generation.KeyDigests[name]
			} else {
				custody, e := readRenewal(online, bundle.Network)
				if e != nil {
					return report, e
				}
				expected = custody.KeyDigests[name]
			}
			keys, e := onlineKeyStore(online, bundle.Network, false)
			if e != nil {
				readErr = e
			} else {
				data, readErr = keys.read(onlineKeyName(version, name))
				keys.close()
			}
		} else if generation == nil {
			data, readErr = current.read(encryptedKeyName(name))
		} else {
			expected = generation.KeyDigests[name]
			keys, e := membershipStore(home, bundle.Network, false)
			if e != nil {
				readErr = e
			} else {
				data, readErr = keys.read(encryptedGenerationName(generation.RootVersion, name))
				keys.close()
			}
		}
		switch {
		case errors.Is(readErr, os.ErrNotExist):
			report.KeyFiles[name] = "missing"
		case readErr != nil || digest(data) != expected:
			report.KeyFiles[name] = "damaged"
		case protected:
			report.KeyFiles[name] = "protected"
		default:
			report.KeyFiles[name] = "locked"
		}
		if session != nil {
			if report.KeyFiles[name] == "missing" || report.KeyFiles[name] == "damaged" {
				return report, fmt.Errorf("omega: active %s key is %s; restore its generation", name, report.KeyFiles[name])
			}
			if !protected {
				if generation == nil {
					_, err = session.initial(name)
				} else {
					_, err = session.generationKey(*generation, name)
				}
				if err != nil {
					return report, err
				}
				report.KeyFiles[name] = "verified"
			}
		}
	}
	if session != nil && online != home {
		custody, err := readRenewal(online, bundle.Network)
		if err != nil {
			return report, err
		}
		keys, err := custody.activeKeys(online, state, latest)
		if err != nil {
			return report, err
		}
		if err := verifyOnlineKeys(keys, latest.currentRoot(bundle)); err != nil {
			return report, err
		}
		report.KeyFiles["snapshot"], report.KeyFiles["timestamp"] = "verified", "verified"
	}
	return report, nil
}
