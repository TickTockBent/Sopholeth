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
	t.Parallel()
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

// Rotate and publish reports list the active generations' key files, the same
// states status shows, never the retired initialization files (#234).
func TestRotateAndPublishReportActiveKeyFiles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, _, opts := encryptedLifecycleFixture(t)
	for opts.RootVersion = 2; opts.RootVersion <= 4; opts.RootVersion++ {
		applyRoot(t, opts)
	}
	must(t, os.Remove(filepath.Join(f.opts.Home, opts.Network+".rotations", encryptedGenerationName(4, "root-1"))))
	want := map[string]string{"root-1": "missing", "root-2": "locked", "root-3": "locked", "targets": "locked", "snapshot": "protected", "timestamp": "protected"}
	assertKeyFiles := func(step string, report Report) {
		t.Helper()
		if !bytes.Equal(record(report.KeyFiles), record(want)) {
			t.Fatalf("%s key_files = %v, want %v", step, report.KeyFiles, want)
		}
		status, err := Status(ctx, f.opts.Home, opts.Network)
		must(t, err)
		if !bytes.Equal(record(report.KeyFiles), record(status.KeyFiles)) {
			t.Fatalf("%s key_files = %v, status shows %v", step, report.KeyFiles, status.KeyFiles)
		}
	}
	status, err := Status(ctx, f.opts.Home, opts.Network)
	must(t, err)
	if !bytes.Equal(record(status.KeyFiles), record(want)) {
		t.Fatalf("status key_files = %v, want %v", status.KeyFiles, want)
	}
	opts.Role = "online"
	prepared, err := Rotate(ctx, opts)
	must(t, err)
	assertKeyFiles("prepare", prepared)
	opts.Apply = prepared.Rotation.RootSHA256
	applied, err := Rotate(ctx, opts)
	must(t, err)
	if applied.Rotation.State != "applied" || applied.RootVersion != 5 {
		t.Fatalf("online rotation did not apply on the surviving root keys: %+v", applied)
	}
	assertKeyFiles("apply", applied)
	f.opts.Version = 6
	f.changeRoot()
	published, err := Publish(ctx, f.opts)
	must(t, err)
	assertKeyFiles("publish", published)

	// The inventory never replaces a failed command's own diagnosis.
	unprepared := opts
	unprepared.RootVersion, unprepared.Apply = 6, digest([]byte("unprepared"))
	failed, err := Rotate(ctx, unprepared)
	if err == nil || failed.Problem != err.Error() || !strings.HasPrefix(failed.Action, "Preserve both custody homes") {
		t.Fatalf("failed rotation lost its diagnosis: %v %+v", err, failed)
	}
	assertKeyFiles("failed rotate", failed)
	skipped := f.opts
	skipped.Version = 8
	failed, err = Publish(ctx, skipped)
	if err == nil || failed.Problem != err.Error() || failed.Publication != "failed" || !strings.HasPrefix(failed.Action, "Preserve publication history") {
		t.Fatalf("failed publish lost its diagnosis: %v %+v", err, failed)
	}
	assertKeyFiles("failed publish", failed)
}

// Check-keys on a restored operator home fails closed when its bound renewal
// home or that home's publication history is unavailable, and says to restore
// that home rather than blaming the passphrase or key files (#235).
func TestCheckKeysPointsAtUnavailableRenewalHome(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, online, opts := encryptedLifecycleFixture(t)
	const keyAction = "Check the passphrase and restore matching encrypted key files"
	restoreAction := "Restore the bound renewal home and its publication history at their original path " + online.Home + ";"

	wrongPassphrase := func(context.Context, bool) ([]byte, error) { return []byte("wrong password"), nil }
	failed, err := checkKeys(ctx, f.opts.Home, opts.Network, wrongPassphrase, fastKeyCodec)
	if err == nil || failed.Problem != err.Error() || !strings.HasPrefix(failed.Action, keyAction) {
		t.Fatalf("wrong passphrase action: %v %+v", err, failed)
	}

	must(t, os.Rename(online.Home, online.Home+"-unavailable"))
	unprompted := func(context.Context, bool) ([]byte, error) {
		t.Fatal("check-keys prompted without its renewal home")
		return nil, nil
	}
	failed, err = checkKeys(ctx, f.opts.Home, opts.Network, unprompted, fastKeyCodec)
	if err == nil || failed.Problem != err.Error() || !strings.Contains(failed.Problem, online.Home) {
		t.Fatalf("missing renewal home problem: %v %+v", err, failed)
	}
	if !strings.HasPrefix(failed.Action, restoreAction) || strings.Contains(failed.Action, "passphrase") {
		t.Fatalf("missing renewal home action: %q", failed.Action)
	}
	for _, state := range failed.KeyFiles {
		if state == "verified" {
			t.Fatal("check-keys verified a key without its renewal home")
		}
	}
	must(t, os.Rename(online.Home+"-unavailable", online.Home))

	history := onlineState(online)
	must(t, os.Rename(history, history+"-unavailable"))
	must(t, os.WriteFile(history, nil, 0600))
	failed, err = checkKeys(ctx, f.opts.Home, opts.Network, testKeyPassphrase, fastKeyCodec)
	if err == nil || failed.Problem != err.Error() || !strings.HasPrefix(failed.Action, restoreAction) {
		t.Fatalf("unopenable publication history: %v %+v", err, failed)
	}
	must(t, os.Remove(history))
	must(t, os.Rename(history+"-unavailable", history))

	// A journal that opens but whose releases cannot be loaded.
	release := filepath.Join(history, releaseName(1))
	recorded := file(t, release)
	must(t, os.WriteFile(release, []byte("{}"), 0600))
	failed, err = checkKeys(ctx, f.opts.Home, opts.Network, testKeyPassphrase, fastKeyCodec)
	if err == nil || failed.Problem != err.Error() || !strings.HasPrefix(failed.Action, restoreAction) {
		t.Fatalf("unloadable publication history: %v %+v", err, failed)
	}
	must(t, os.WriteFile(release, recorded, 0600))

	// A recreated but still empty renewal home is not yet restored.
	must(t, os.Rename(online.Home, online.Home+"-unavailable"))
	must(t, os.Mkdir(online.Home, 0700))
	failed, err = checkKeys(ctx, f.opts.Home, opts.Network, unprompted, fastKeyCodec)
	if err == nil || failed.Problem != err.Error() || !strings.HasPrefix(failed.Action, restoreAction) {
		t.Fatalf("empty renewal home: %v %+v", err, failed)
	}
	must(t, os.RemoveAll(online.Home))
	must(t, os.Rename(online.Home+"-unavailable", online.Home))

	// Waiting for a busy renewal home is not a missing one.
	held, err := openHome(ctx, online.Home, opts.Network, false)
	must(t, err)
	waiting, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	failed, err = checkKeys(waiting, f.opts.Home, opts.Network, unprompted, fastKeyCodec)
	cancel()
	held.close()
	if !errors.Is(err, context.DeadlineExceeded) || strings.HasPrefix(failed.Action, restoreAction) {
		t.Fatalf("lock wait: %v %+v", err, failed)
	}

	_, err = checkKeys(ctx, f.opts.Home, opts.Network, testKeyPassphrase, fastKeyCodec)
	must(t, err)
}

// A renewal home provisioned before its first publish has no journal and its
// initial keys are active. After a rotation a lost journal would make
// check-keys verify retired keys, so it fails closed instead (#235).
func TestCheckKeysRequiresHistoryAfterRotation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	unpublished := newCustodyPublishFixture(t, time.Now().UTC().Truncate(time.Second), true)
	renewalHome := filepath.Join(t.TempDir(), "online")
	_, err := ProvisionRenewal(ctx, ProvisionRenewalOptions{Home: unpublished.opts.Home, Network: unpublished.opts.Network, RenewalHome: renewalHome, Passphrase: testKeyPassphrase, codec: fastKeyCodec})
	must(t, err)
	assertAbsent(t, filepath.Join(renewalHome, unpublished.opts.Network+".publication"))
	verified, err := checkKeys(ctx, unpublished.opts.Home, unpublished.opts.Network, testKeyPassphrase, fastKeyCodec)
	must(t, err)
	for name, state := range verified.KeyFiles {
		if state != "verified" {
			t.Fatalf("never-published %s key is %s", name, state)
		}
	}

	f, online, opts := encryptedLifecycleFixture(t)
	opts.Role = "online"
	prepared, err := Rotate(ctx, opts)
	must(t, err)
	opts.Apply = prepared.Rotation.RootSHA256
	_, err = Rotate(ctx, opts)
	must(t, err)
	history := onlineState(online)
	rotations := filepath.Join(f.opts.Home, opts.Network+".rotations")
	restoreAction := "Restore the bound renewal home and its publication history at their original path " + online.Home + ";"
	must(t, os.Rename(history, history+"-unavailable"))
	// Either the authority's rotation records or the rotated online keys
	// prove the journal existed.
	for _, step := range []string{"authority rotation records", "rotated online keys"} {
		failed, err := checkKeys(ctx, f.opts.Home, opts.Network, testKeyPassphrase, fastKeyCodec)
		if err == nil || failed.Problem != err.Error() || !strings.Contains(failed.Problem, history) || !strings.HasPrefix(failed.Action, restoreAction) {
			t.Fatalf("%s: lost journal did not fail closed: %v %+v", step, err, failed)
		}
		for name, state := range failed.KeyFiles {
			if state == "verified" {
				t.Fatalf("%s: verified %s without its history", step, name)
			}
		}
		if step == "authority rotation records" {
			must(t, os.Rename(rotations, rotations+"-unavailable"))
		}
	}
	must(t, os.Rename(rotations+"-unavailable", rotations))
	// A journal that opens but records no releases is no better.
	must(t, os.Mkdir(history, 0700))
	must(t, os.WriteFile(filepath.Join(history, "binding.json"), file(t, filepath.Join(history+"-unavailable", "binding.json")), 0600))
	failed, err := checkKeys(ctx, f.opts.Home, opts.Network, testKeyPassphrase, fastKeyCodec)
	if err == nil || failed.Problem != err.Error() || !strings.Contains(failed.Problem, history) || !strings.HasPrefix(failed.Action, restoreAction) {
		t.Fatalf("empty journal did not fail closed: %v %+v", err, failed)
	}
	for name, state := range failed.KeyFiles {
		if state == "verified" {
			t.Fatalf("empty journal: verified %s without its history", name)
		}
	}
	must(t, os.RemoveAll(history))
	must(t, os.Rename(history+"-unavailable", history))
	verified, err = checkKeys(ctx, f.opts.Home, opts.Network, testKeyPassphrase, fastKeyCodec)
	must(t, err)
	if verified.RootVersion != 2 {
		t.Fatalf("restored history root = %d, want 2", verified.RootVersion)
	}
}
