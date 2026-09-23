//go:build linux

package omega

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"filippo.io/age"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testKeyPassphrase(context.Context, bool) ([]byte, error) {
	return []byte("throwaway encrypted custody passphrase"), nil
}
func encryptedLifecycleFixture(t *testing.T) (*publishFixture, RenewOptions, RotateOptions) {
	t.Helper()
	f := newCustodyPublishFixture(t, time.Now().UTC().Truncate(time.Second), true)
	online := RenewOptions{Home: filepath.Join(t.TempDir(), "online"), Network: f.opts.Network, HTTPClient: f.opts.HTTPClient}
	provision := ProvisionRenewalOptions{Home: f.opts.Home, Network: f.opts.Network, RenewalHome: online.Home, Passphrase: testKeyPassphrase, codec: fastKeyCodec}
	_, err := ProvisionRenewal(context.Background(), provision)
	must(t, err)
	_, err = Publish(context.Background(), f.opts)
	must(t, err)
	opts := RotateOptions{Home: f.opts.Home, Network: f.opts.Network, RootVersion: 2, Passphrase: testKeyPassphrase, codec: fastKeyCodec, HTTPClient: f.opts.HTTPClient}
	return f, online, opts
}
func TestNewPassphraseLength(t *testing.T) {
	binding := keyBinding{Transaction: digest([]byte("test")), Network: "rehearsal", Repository: "https://metadata.example.invalid", Name: "targets", Generation: 1}
	for _, password := range []string{"a", "12345678901", "éééééé"} {
		if _, err := fastKeyCodec.create(context.Background(), binding, []byte(password)); err == nil {
			t.Fatal("short passphrase allocated a key")
		}
	}
	ciphertext, err := fastKeyCodec.create(context.Background(), binding, []byte("éééééééééééé"))
	must(t, err)
	_, err = fastKeyCodec.unlock(context.Background(), ciphertext, binding, []byte("éééééééééééé"))
	must(t, err)
	// A pre-policy encrypted copy with a short password must still unlock.
	identity, err := age.NewScryptIdentity("éééééééééééé")
	must(t, err)
	reader, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
	must(t, err)
	plaintext, err := io.ReadAll(reader)
	must(t, err)
	defer clear(plaintext)
	recipient, err := age.NewScryptRecipient("a")
	must(t, err)
	recipient.SetWorkFactor(1)
	var old bytes.Buffer
	writer, err := age.Encrypt(&old, recipient)
	must(t, err)
	_, err = writer.Write(plaintext)
	must(t, err)
	must(t, writer.Close())
	private, err := fastKeyCodec.unlock(context.Background(), old.Bytes(), binding, []byte("a"))
	must(t, err)
	clear(private)
}

func rehearsalBackupRestore(t *testing.T, f *publishFixture, online RenewOptions, opts RotateOptions) {
	t.Helper()
	ctx := context.Background()
	bundle := fixtureBundle(t, f)
	// A wrong mode cannot relabel existing authority, and a wrong password cannot
	// reserve a signed release. Retry the same version after fixing the password.
	wrong := f.opts
	wrong.Disposable = true
	if r, err := Publish(ctx, wrong); err == nil || r.Problem == "" {
		t.Fatal("production accepted disposable operation")
	}
	badPassword := f.opts
	badPassword.Version++
	badPassword.Passphrase = func(context.Context, bool) ([]byte, error) { return []byte("wrong password"), nil }
	if r, err := Publish(ctx, badPassword); err == nil || r.Problem == "" {
		t.Fatal("wrong signing password was accepted")
	}
	assertAbsent(t, filepath.Join(onlineState(online), releaseName(badPassword.Version)))
	privateBefore := file(t, filepath.Join(f.opts.Home, opts.Network, "authority.json"))
	backups := t.TempDir()
	for i, home := range []string{f.opts.Home, online.Home} {
		backup := filepath.Join(backups, fmt.Sprint(i))
		copyPrivateHome(t, home, backup)
		must(t, os.Rename(home, home+"-unavailable"))
		copyPrivateHome(t, backup, home) // Existing binding paths; no journal rewriting.
	}
	verified, err := checkKeys(ctx, f.opts.Home, opts.Network, testKeyPassphrase, fastKeyCodec)
	must(t, err)
	if verified.Fingerprint != bundle.Fingerprint() || verified.RootVersion != 7 {
		t.Fatalf("restored wrong authority: %+v", verified)
	}
	for _, state := range verified.KeyFiles {
		if state != "verified" {
			t.Fatal("active restored key was not verified")
		}
	}
	retry := ProvisionRenewalOptions{Home: f.opts.Home, Network: opts.Network, RenewalHome: online.Home, Passphrase: func(context.Context, bool) ([]byte, error) {
		t.Fatal("completed provisioning prompted")
		return nil, nil
	}, codec: fastKeyCodec}
	_, err = ProvisionRenewal(ctx, retry)
	must(t, err)
	activeOnline := filepath.Join(online.Home, opts.Network+".online-keys", onlineKeyName(7, "timestamp"))
	must(t, os.Rename(activeOnline, activeOnline+".unavailable"))
	if r, err := ProvisionRenewal(ctx, retry); err == nil || r.Problem == "" {
		t.Fatal("completed provisioning reported ready with an unavailable active key")
	}
	must(t, os.Rename(activeOnline+".unavailable", activeOnline))
	assertCustodySecretSeparation(t, f, online)
	if !bytes.Equal(privateBefore, file(t, filepath.Join(f.opts.Home, opts.Network, "authority.json"))) {
		t.Fatal("restore changed the authority")
	}
	// A deliberate reset creates a new identity in a fresh home and leaves the
	// original registry, publication, and client anchor intact.
	reset := InitOptions{Home: filepath.Join(t.TempDir(), "reset"), Network: "replacement", Repository: f.server.URL, Passphrase: testKeyPassphrase}
	r, err := initializeWithCodec(ctx, reset, time.Now().UTC().Truncate(time.Second), nil, fastKeyCodec)
	must(t, err)
	if r.Mode != "production" || r.Fingerprint == bundle.Fingerprint() {
		t.Fatal("reset reused the previous authority")
	}
}
func copyPrivateHome(t *testing.T, source, destination string) {
	t.Helper()
	must(t, filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		out := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.Mkdir(out, 0700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("unexpected backup entry")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(out, data, 0600)
	}))
}
func assertCustodySecretSeparation(t *testing.T, f *publishFixture, online RenewOptions) {
	t.Helper()
	var p publicAuthority
	must(t, decodeRecord(file(t, filepath.Join(f.opts.Home, f.opts.Network, "authority.json")), &p))
	var operatorSecrets, allSecrets [][]byte
	for _, name := range keyNames {
		path := filepath.Join(f.opts.Home, f.opts.Network, encryptedKeyName(name))
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		must(t, err)
		private, err := fastKeyCodec.unlock(context.Background(), data, p.binding(name), []byte("throwaway encrypted custody passphrase"))
		must(t, err)
		encoded := []byte(base64.StdEncoding.EncodeToString(private))
		clear(private)
		allSecrets = append(allSecrets, encoded)
		if name != "snapshot" && name != "timestamp" {
			operatorSecrets = append(operatorSecrets, encoded)
		}
	}
	must(t, filepath.WalkDir(filepath.Join(f.opts.Home, f.opts.Network+".rotations"), func(path string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".age.json") {
			return nil
		}
		var c encryptedGenerationKey
		if err := decodeRecord(file(t, path), &c); err != nil {
			return err
		}
		version, err := strconv.ParseInt(strings.Split(e.Name(), ".")[0], 10, 64)
		if err != nil {
			return err
		}
		var plan encryptedRotationPlan
		if err := decodeRecord(file(t, filepath.Join(filepath.Dir(path), encryptedPlanName(version))), &plan); err != nil {
			return err
		}
		private, err := fastKeyCodec.unlock(context.Background(), c.Ciphertext, plan.binding(fixtureBundle(t, f), c.Name), []byte("throwaway encrypted custody passphrase"))
		if err != nil {
			return err
		}
		encoded := []byte(base64.StdEncoding.EncodeToString(private))
		clear(private)
		operatorSecrets = append(operatorSecrets, encoded)
		allSecrets = append(allSecrets, encoded)
		return nil
	}))
	must(t, filepath.WalkDir(filepath.Join(online.Home, online.Network+".online-keys"), func(path string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		var c onlineKeyMaterial
		if err := decodeRecord(file(t, path), &c); err != nil {
			return err
		}
		allSecrets = append(allSecrets, []byte(c.Key))
		return nil
	}))
	for _, dir := range []string{f.opts.Home, online.Home, f.opts.Directory} {
		must(t, filepath.WalkDir(dir, func(path string, e os.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			secrets := allSecrets
			if strings.HasPrefix(path, filepath.Join(online.Home, online.Network+".online-keys")+string(os.PathSeparator)) {
				secrets = operatorSecrets
			}
			for _, secret := range secrets {
				if bytes.Contains(data, secret) {
					return fmt.Errorf("private signer leaked into %s", path)
				}
			}
			return nil
		}))
	}
}
