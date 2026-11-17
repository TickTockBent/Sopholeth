package omega

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"sopholeth/internal/trust/bootstrap"
)

// Report and check the active generations, not retired initialization keys.
// A nil session inspects public bindings and ciphertext/file digests only.
//
// A failure before every active file has a state drops the caller's states,
// which describe the initialization generation, and records why the inventory
// is incomplete. The returned KeyFiles then hold only files this inspection
// reached, so a report never shows retired generations as active (#234).
func inspectEncryptedKeys(home, current, online *store, bundle bootstrap.Bundle, report Report, session *operatorKeys) (result Report, err error) {
	report.KeyFiles, report.KeyFilesError = nil, ""
	inventoried := false
	defer func() {
		if err != nil && !inventoried {
			result.KeyFilesError = err.Error()
		}
	}()
	p, err := current.readPublicAuthority()
	if err != nil {
		return report, err
	}
	var rootGeneration, targetsGeneration, onlineGeneration *rotationIntent
	var latest release
	historyUnavailable := func(err error) error {
		if online == home {
			return err
		}
		return renewalUnavailableError{home: online.root.Name(), err: err}
	}
	state, _, err := openPublication(online, bundle, nil, false)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return report, historyUnavailable(err)
	}
	var history []release
	if err == nil {
		defer state.close()
		history, err = loadReleases(state, bundle)
		if err != nil {
			return report, historyUnavailable(err)
		}
	}
	// A renewal home provisioned before its first publish has no releases, and
	// its active keys are the initial ones. Once keys have rotated, an absent or
	// empty journal would select retired keys, so key verification fails closed.
	if len(history) == 0 && session != nil && online != home {
		rotated, evidenceErr := rotationRecorded(home, online, bundle.Network)
		if evidenceErr != nil {
			return report, historyUnavailable(evidenceErr)
		}
		if rotated {
			return report, historyUnavailable(fmt.Errorf("omega: publication history %s has no releases, but key rotations are recorded; restore it", filepath.Join(online.root.Name(), bundle.Network+".publication")))
		}
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
	inventoried = true
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

// Rotate refuses to run without published history in the bound renewal home,
// and only Rotate writes the authority's rotation records or online keys past
// generation 1. Either one proves the journal existed.
func rotationRecorded(home, online *store, network string) (bool, error) {
	if _, err := home.root.Lstat(network + ".rotations"); err == nil {
		return true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	keys, err := onlineKeyStore(online, network, false)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer keys.close()
	names, err := directoryNames(keys)
	if err != nil {
		return false, err
	}
	for _, name := range names {
		if m := onlineKeyFile.FindStringSubmatch(name); m != nil && m[1] != "1" {
			return true, nil
		}
	}
	return false, nil
}

var onlineKeyFile = regexp.MustCompile(`^([0-9]+)\.(snapshot|timestamp)-key\.json(\.pending)?$`)

// Reports from commands that change active generations list the same key file
// states status shows. The inventory is informational only: it never changes
// the command's outcome, Problem or Action. An inspection failure replaces the
// initialization-generation states with the files it reached and the reason it
// stopped, as status does.
func reportActiveKeyFiles(report *Report, home, current, online *store, bundle bootstrap.Bundle) {
	if report.Custody != encryptedCustody {
		return
	}
	inspected, _ := inspectEncryptedKeys(home, current, online, bundle, Report{}, nil)
	report.KeyFiles, report.KeyFilesError = inspected.KeyFiles, inspected.KeyFilesError
}
