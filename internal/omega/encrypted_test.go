//go:build linux

package omega

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var fastKeyCodec = keyCodec{workFactor: 1}

func encryptedOptions(t *testing.T) InitOptions {
	opts := options(t)
	opts.Encrypted = true
	opts.Passphrase = func(context.Context, bool) ([]byte, error) { return []byte("throwaway test passphrase"), nil }
	return opts
}

func TestEncryptedKeyDefaultAndInputBounds(t *testing.T) {
	codec := defaultKeyCodec()
	binding := keyBinding{Transaction: digest([]byte("transaction")), Network: "rehearsal", Repository: "https://metadata.example.invalid", Name: "root-1", Generation: 1}
	password := []byte("throwaway default-cost test passphrase")
	data, err := codec.create(context.Background(), binding, password)
	must(t, err)
	key, err := codec.unlock(context.Background(), data, binding, password)
	must(t, err)
	if len(key) != ed25519.PrivateKeySize || !bytes.Contains(data, []byte(" 18\n")) {
		t.Fatal("default backend did not use the expected key and scrypt cost")
	}
	clear(key)
	// Reject excessive scrypt work before spending that work, even when the
	// file was otherwise produced by the real backend.
	tooExpensive := bytes.Replace(data, []byte(" 18\n"), []byte(" 30\n"), 1)
	if _, err := codec.unlock(context.Background(), tooExpensive, binding, password); err == nil {
		t.Fatal("accepted excessive scrypt work")
	}
	if _, err := codec.unlock(context.Background(), make([]byte, maxEncryptedKey+1), binding, password); err == nil {
		t.Fatal("accepted oversized key file")
	}
}

func TestEncryptedKeyBindingAndAuthentication(t *testing.T) {
	binding := keyBinding{Transaction: digest([]byte("transaction")), Network: "rehearsal", Repository: "https://metadata.example.invalid", Name: "root-1", Generation: 1}
	password := []byte("throwaway test passphrase")
	data, err := fastKeyCodec.create(context.Background(), binding, password)
	must(t, err)
	for _, kind := range []string{"password", "truncated", "tampered", "transaction", "role", "generation", "network", "repository"} {
		t.Run(kind, func(t *testing.T) {
			candidate, expected, secret := bytes.Clone(data), binding, password
			switch kind {
			case "password":
				secret = []byte("wrong password")
			case "truncated":
				candidate = candidate[:len(candidate)-1]
			case "tampered":
				candidate[len(candidate)-1] ^= 1
			case "transaction":
				expected.Transaction = digest([]byte("other"))
			case "role":
				expected.Name = "targets"
			case "generation":
				expected.Generation++
			case "network":
				expected.Network = "other"
			case "repository":
				expected.Repository = "https://other.example.invalid"
			}
			if _, err := fastKeyCodec.unlock(context.Background(), candidate, expected, secret); err == nil {
				t.Fatal("accepted invalid encrypted key")
			}
		})
	}
}

func TestEncryptedInitPublicInspectionAndBackupRestore(t *testing.T) {
	opts := encryptedOptions(t)
	report, err := initializeWithCodec(context.Background(), opts, testTime, nil, fastKeyCodec)
	must(t, err)
	if report.Custody != encryptedCustody || report.State != "initialized" || report.Roles["root"].Threshold != 2 {
		t.Fatalf("wrong encrypted authority: %+v", report)
	}
	var public publicAuthority
	must(t, decodeRecord(file(t, filepath.Join(finalPath(opts), "authority.json")), &public))
	if public.Schema != 2 || public.Fingerprint != report.Fingerprint {
		t.Fatal("public authority does not bind the initial root")
	}
	backup := filepath.Join(t.TempDir(), "backup")
	must(t, os.Mkdir(backup, 0700))
	must(t, os.Mkdir(filepath.Join(backup, opts.Network), 0700))
	for _, name := range append(append([]string(nil), authorityFiles...), encryptedKeyNames()...) {
		data := file(t, filepath.Join(finalPath(opts), name))
		must(t, os.WriteFile(filepath.Join(backup, opts.Network, name), data, 0600))
	}
	assertAbsent(t, filepath.Join(finalPath(opts), allocationName))
	// Until the lifecycle slice lands, schema 2 cannot create any plaintext
	// publisher state or copy private material into a service home.
	blocked, err := Publish(context.Background(), PublishOptions{Home: opts.Home, Network: opts.Network, Directory: filepath.Join(t.TempDir(), "repository"), Manifest: publicationManifest, Version: 1, Disposable: true})
	if err == nil || blocked.Custody != encryptedCustody || blocked.Problem == "" {
		t.Fatal("encrypted publication was enabled before lifecycle support")
	}
	online := filepath.Join(t.TempDir(), "online")
	blocked, err = ProvisionRenewal(context.Background(), ProvisionRenewalOptions{Home: opts.Home, Network: opts.Network, RenewalHome: online, Disposable: true})
	if err == nil || blocked.Custody != encryptedCustody || blocked.Problem == "" {
		t.Fatal("encrypted custody entered plaintext renewal provisioning")
	}
	assertAbsent(t, online)
	blocked, err = Rotate(context.Background(), RotateOptions{Home: opts.Home, Network: opts.Network, RootVersion: 2, Disposable: true})
	if err == nil || blocked.Custody != encryptedCustody || !strings.Contains(blocked.Action, "inspection/recovery") {
		t.Fatal("encrypted rotation was enabled before lifecycle support")
	}
	for _, name := range keyNames {
		private, err := fastKeyCodec.unlock(context.Background(), file(t, filepath.Join(finalPath(opts), encryptedKeyName(name))), public.binding(name), []byte("throwaway test passphrase"))
		must(t, err)
		for _, publicFile := range authorityFiles {
			if bytes.Contains(file(t, filepath.Join(finalPath(opts), publicFile)), []byte(base64.StdEncoding.EncodeToString(private))) {
				t.Fatal("private key appeared in public authority material")
			}
		}
		clear(private)
		must(t, os.Remove(filepath.Join(finalPath(opts), encryptedKeyName(name))))
	}
	status, err := Status(context.Background(), opts.Home, opts.Network)
	must(t, err)
	if status.State != "initialized" || status.Fingerprint != report.Fingerprint || status.KeyFiles["root-1"] != "missing" {
		t.Fatal("missing key files prevented public inspection")
	}
	opts.Passphrase = func(context.Context, bool) ([]byte, error) {
		t.Fatal("idempotent init requested a password")
		return nil, nil
	}
	failed, err := initializeWithCodec(context.Background(), opts, testTime, nil, fastKeyCodec)
	if err == nil || failed.Problem == "" || failed.Fingerprint != report.Fingerprint {
		t.Fatal("init replaced or concealed unavailable committed keys")
	}
	must(t, os.Rename(opts.Home, opts.Home+"-unavailable"))
	passphrase := encryptedOptions(t).Passphrase
	verified, err := checkKeys(context.Background(), backup, opts.Network, passphrase, fastKeyCodec)
	must(t, err)
	for _, name := range keyNames {
		if verified.KeyFiles[name] != "verified" {
			t.Fatal("restored key not verified")
		}
	}
	badPassword := func(context.Context, bool) ([]byte, error) { return []byte("wrong password"), nil }
	failed, err = checkKeys(context.Background(), backup, opts.Network, badPassword, fastKeyCodec)
	if err == nil || failed.Problem == "" || failed.Fingerprint != report.Fingerprint {
		t.Fatal("backup verification ignored a wrong password")
	}
	must(t, os.WriteFile(filepath.Join(backup, opts.Network, encryptedKeyName("root-1")), []byte("damaged"), 0600))
	status, err = Status(context.Background(), backup, opts.Network)
	must(t, err)
	if status.KeyFiles["root-1"] != "damaged" || status.State != "initialized" {
		t.Fatal("private damage was not separated from public identity")
	}
}

func encryptedKeyNames() []string {
	var names []string
	for _, name := range keyNames {
		names = append(names, encryptedKeyName(name))
	}
	return names
}

func TestEncryptedInitRecoveryBoundaries(t *testing.T) {
	// Shared link/fsync/promotion and process-death behavior remains covered by
	// the original initializer suite. These are the new encrypted boundaries.
	for _, phase := range []string{allocationName + ":written", "root-1.key.age:written", "encrypted:allocation-retired", "promoted"} {
		t.Run(phase, func(t *testing.T) {
			opts := encryptedOptions(t)
			stop := errors.New("interrupted")
			r, err := initializeWithCodec(context.Background(), opts, testTime, func(at string) error {
				if at == phase {
					return stop
				}
				return nil
			}, fastKeyCodec)
			if !errors.Is(err, stop) {
				t.Fatalf("missed boundary: %v", err)
			}
			var allocation encryptedAllocation
			if phase == allocationName+":written" || phase == "root-1.key.age:written" {
				name := allocationName
				if phase == allocationName+":written" {
					name += ".pending"
				}
				must(t, decodeRecord(file(t, filepath.Join(stagePath(opts), name)), &allocation))
			} else {
				opts.Passphrase = func(context.Context, bool) ([]byte, error) {
					t.Fatal("complete authority asked to unlock again")
					return nil, nil
				}
			}
			if phase == "promoted" && (r.State != "initialized" || r.Fingerprint == "" || r.Problem == "") {
				t.Fatal("post-commit failure concealed the committed identity")
			}
			recovered, err := initializeWithCodec(context.Background(), opts, testTime.Add(time.Minute), nil, fastKeyCodec)
			must(t, err)
			if recovered.State != "initialized" {
				t.Fatal("recovery did not commit")
			}
			for name, encrypted := range allocation.Keys {
				if !bytes.Equal(encrypted, file(t, filepath.Join(finalPath(opts), encryptedKeyName(name)))) {
					t.Fatal("retry replaced an encrypted allocation")
				}
			}
			assertAbsent(t, stagePath(opts))
		})
	}
}

func TestEncryptedFirstWriteAndWrongPasswordRecovery(t *testing.T) {
	for _, kind := range []string{"torn-first-write", "wrong-password", "damaged-committed"} {
		t.Run(kind, func(t *testing.T) {
			opts := encryptedOptions(t)
			phase := allocationName + ":written"
			if kind == "damaged-committed" {
				phase = allocationName + ":durable"
			}
			_, err := initializeWithCodec(context.Background(), opts, testTime, func(at string) error {
				if at == phase {
					return errors.New("stop")
				}
				return nil
			}, fastKeyCodec)
			if err == nil {
				t.Fatal("expected interruption")
			}
			name := allocationName + ".pending"
			if kind == "damaged-committed" {
				name = allocationName
			}
			path := filepath.Join(stagePath(opts), name)
			before := file(t, path)
			if kind != "wrong-password" {
				must(t, os.WriteFile(path, []byte(`{"schema":`), 0600))
			} else {
				opts.Passphrase = func(context.Context, bool) ([]byte, error) { return []byte("wrong"), nil }
			}
			_, err = initializeWithCodec(context.Background(), opts, testTime, nil, fastKeyCodec)
			if kind == "torn-first-write" {
				must(t, err)
				return
			}
			if err == nil {
				t.Fatal("invalid allocation was replaced")
			}
			assertAbsent(t, finalPath(opts))
			if kind == "wrong-password" {
				if !bytes.Equal(before, file(t, filepath.Join(stagePath(opts), allocationName))) {
					t.Fatal("wrong password replaced a complete candidate")
				}
				opts.Passphrase = encryptedOptions(t).Passphrase
				_, err = initializeWithCodec(context.Background(), opts, testTime, nil, fastKeyCodec)
				must(t, err)
			}
		})
	}
}

func TestEncryptedAllocationRetainedUntilKeyFilesVerify(t *testing.T) {
	opts := encryptedOptions(t)
	_, err := initializeWithCodec(context.Background(), opts, testTime, func(phase string) error {
		if phase == "complete.json:durable" {
			return os.WriteFile(filepath.Join(stagePath(opts), encryptedKeyName("root-1")), []byte("damaged"), 0600)
		}
		return nil
	}, fastKeyCodec)
	if err == nil {
		t.Fatal("committed authority with a damaged encrypted key")
	}
	assertAbsent(t, finalPath(opts))
	var allocation encryptedAllocation
	must(t, decodeRecord(file(t, filepath.Join(stagePath(opts), allocationName)), &allocation))
	// Restore the damaged copy from the retained, fixed allocation; recovery
	// must finish the same authority without generating replacement keys.
	must(t, os.WriteFile(filepath.Join(stagePath(opts), encryptedKeyName("root-1")), allocation.Keys["root-1"], 0600))
	_, err = initializeWithCodec(context.Background(), opts, testTime, nil, fastKeyCodec)
	must(t, err)
}
