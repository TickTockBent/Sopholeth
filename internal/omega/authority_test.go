//go:build linux

package omega

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sopholeth/internal/trust/bootstrap"
)

var testTime = time.Now().UTC().Truncate(time.Second)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func options(t *testing.T) InitOptions {
	t.Helper()
	return InitOptions{Home: filepath.Join(t.TempDir(), "custody"), Network: "rehearsal", Repository: "https://metadata.example.invalid", Disposable: true}
}
func file(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	must(t, err)
	return data
}
func stagePath(o InitOptions) string { return filepath.Join(o.Home, "."+o.Network+".pending") }
func finalPath(o InitOptions) string { return filepath.Join(o.Home, o.Network) }
func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected absent path %s, got %v", path, err)
	}
}

func TestAtomicInitAndIdempotence(t *testing.T) {
	o := options(t)
	first, err := initialize(context.Background(), o, testTime, nil)
	must(t, err)
	if first.State != "initialized" || first.Publication != "not_checked" || len(first.Fingerprint) != 64 {
		t.Fatalf("unexpected report: %+v", first)
	}
	if first.Roles["root"].Threshold != 2 || len(first.Roles["root"].KeyIDs) != 3 {
		t.Fatal("wrong root quorum")
	}
	before := file(t, filepath.Join(finalPath(o), "authority.json"))
	second, err := initialize(context.Background(), o, testTime.Add(time.Hour), nil)
	must(t, err)
	if second.Fingerprint != first.Fingerprint || !bytes.Equal(before, file(t, filepath.Join(finalPath(o), "authority.json"))) {
		t.Fatal("idempotent init changed authority")
	}
	bundle, err := bootstrap.ParseBundle(file(t, filepath.Join(finalPath(o), "bundle.json")))
	must(t, err)
	_, err = bootstrap.New(context.Background(), bootstrap.Config{Bundle: bundle, StateDir: filepath.Join(t.TempDir(), "client")})
	must(t, err)
	assertAbsent(t, stagePath(o))
	for _, name := range authorityFiles {
		info, err := os.Stat(filepath.Join(finalPath(o), name))
		must(t, err)
		if info.Mode().Perm() != 0600 {
			t.Fatalf("unsafe mode for %s", name)
		}
	}
	var secret authority
	must(t, decodeRecord(before, &secret))
	for _, key := range secret.Keys {
		if bytes.Contains(record(first), []byte(key)) {
			t.Fatal("private material in report")
		}
	}
	changed := o
	changed.Repository = "https://other.example.invalid"
	if _, err := Init(context.Background(), changed); err == nil {
		t.Fatal("reconfigured an existing network")
	}
}

func TestEveryInterruptionIsInvisibleOrCompleteAndRecoverable(t *testing.T) {
	phases := []string{"verified", "promoted", "committed"}
	for _, name := range authorityFiles {
		for _, phase := range []string{"written", "synced", "linked", "durable"} {
			phases = append(phases, name+":"+phase)
		}
	}
	for _, phase := range phases {
		t.Run(phase, func(t *testing.T) {
			o := options(t)
			interrupted := errors.New("simulated interruption")
			_, err := initialize(context.Background(), o, testTime, func(at string) error {
				if at == phase {
					return interrupted
				}
				return nil
			})
			if !errors.Is(err, interrupted) {
				t.Fatalf("phase not exercised: %v", err)
			}
			committed := phase == "promoted" || phase == "committed"
			location := stagePath(o)
			if committed {
				location = finalPath(o)
			} else {
				assertAbsent(t, finalPath(o))
			}
			original, readErr := os.ReadFile(filepath.Join(location, "authority.json"))
			if errors.Is(readErr, os.ErrNotExist) {
				original = file(t, filepath.Join(location, "authority.json.pending"))
			} else {
				must(t, readErr)
			}
			report, statusErr := Status(context.Background(), o.Home, o.Network)
			if committed {
				must(t, statusErr)
				if report.State != "initialized" {
					t.Fatal("committed authority is incomplete")
				}
			} else if statusErr == nil || report.State != "pending" || report.Fingerprint != "" {
				t.Fatal("staging became a usable authority")
			}
			recovered, err := initialize(context.Background(), o, testTime.Add(time.Minute), nil)
			must(t, err)
			if recovered.State != "initialized" || !bytes.Equal(original, file(t, filepath.Join(finalPath(o), "authority.json"))) {
				t.Fatal("recovery replaced keys or left incomplete authority")
			}
		})
	}
}

func TestConcurrentInitializersShareOneAuthority(t *testing.T) {
	o := options(t)
	var wg sync.WaitGroup
	reports := make(chan Report, 12)
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Go(func() { report, err := Init(context.Background(), o); reports <- report; errs <- err })
	}
	wg.Wait()
	close(reports)
	close(errs)
	for err := range errs {
		must(t, err)
	}
	id := ""
	for report := range reports {
		if id == "" {
			id = report.Fingerprint
		}
		if report.Fingerprint != id {
			t.Fatal("concurrent init created different authorities")
		}
	}
	alias := filepath.Join(filepath.Dir(o.Home), "alias")
	must(t, os.Symlink(filepath.Dir(o.Home), alias))
	o.Home = filepath.Join(alias, filepath.Base(o.Home))
	report, err := Init(context.Background(), o)
	must(t, err)
	if report.Fingerprint != id {
		t.Fatal("parent path alias bypassed network lock")
	}
}

func TestDamagedCommittedAuthorityIsNeverRegenerated(t *testing.T) {
	for _, name := range authorityFiles {
		t.Run(name, func(t *testing.T) {
			o := options(t)
			_, err := Init(context.Background(), o)
			must(t, err)
			original := file(t, filepath.Join(finalPath(o), "authority.json"))
			must(t, os.Remove(filepath.Join(finalPath(o), name)))
			if _, err := Init(context.Background(), o); err == nil {
				t.Fatal("recreated missing committed material")
			}
			assertAbsent(t, filepath.Join(finalPath(o), name))
			assertAbsent(t, stagePath(o))
			if name != "authority.json" && !bytes.Equal(original, file(t, filepath.Join(finalPath(o), "authority.json"))) {
				t.Fatal("replaced authority")
			}
		})
	}
}

func TestTornStagingWritesRecoverWithoutPartialAuthority(t *testing.T) {
	for _, name := range authorityFiles {
		t.Run(name, func(t *testing.T) {
			o := options(t)
			_, err := initialize(context.Background(), o, testTime, func(at string) error {
				if at == name+":written" {
					return errors.New("interrupted")
				}
				return nil
			})
			if err == nil {
				t.Fatal("expected interruption")
			}
			var original []byte
			if name != "authority.json" {
				original = file(t, filepath.Join(stagePath(o), "authority.json"))
			}
			must(t, os.WriteFile(filepath.Join(stagePath(o), name+".pending"), []byte("torn"), 0600))
			assertAbsent(t, finalPath(o))
			_, err = Init(context.Background(), o)
			must(t, err)
			if original != nil && !bytes.Equal(original, file(t, filepath.Join(finalPath(o), "authority.json"))) {
				t.Fatal("torn public output replaced authority")
			}
		})
	}
}

func TestCommittedPrivateRecordCorruptionStopsRecovery(t *testing.T) {
	for _, damage := range []string{"public-half", "duplicate-key", "missing", "wrong-network", "duplicate-json"} {
		t.Run(damage, func(t *testing.T) {
			o := options(t)
			_, err := initialize(context.Background(), o, testTime, func(at string) error {
				if at == "bundle.json:durable" {
					return errors.New("interrupted")
				}
				return nil
			})
			if err == nil {
				t.Fatal("expected interruption")
			}
			path := filepath.Join(stagePath(o), "authority.json")
			original := file(t, path)
			var a authority
			must(t, decodeRecord(original, &a))
			switch damage {
			case "public-half":
				key, err := base64.StdEncoding.DecodeString(a.Keys["root-1"])
				must(t, err)
				key[63] ^= 1
				a.Keys["root-1"] = base64.StdEncoding.EncodeToString(key)
			case "duplicate-key":
				a.Keys["timestamp"] = a.Keys["snapshot"]
			case "wrong-network":
				a.Network = "other"
			}
			changed := record(a)
			if damage == "duplicate-json" {
				changed = bytes.Replace(changed, []byte(`"schema":1`), []byte(`"schema":1,"schema":1`), 1)
			}
			if damage == "missing" {
				must(t, os.Remove(path))
			} else {
				must(t, os.WriteFile(path, changed, 0600))
			}
			if _, err := Init(context.Background(), o); err == nil {
				t.Fatal("recovered damaged authority by replacing it")
			}
			assertAbsent(t, finalPath(o))
			if damage != "missing" && !bytes.Equal(changed, file(t, path)) {
				t.Fatal("modified damaged keys")
			}
		})
	}
}

func TestUnsafePathsAndInvalidOptionsDoNotGenerateKeys(t *testing.T) {
	for _, kind := range []string{"network", "repository", "production", "home-symlink", "home-mode", "parent-mode", "slot-file", "slot-symlink", "lock-symlink", "stage-symlink"} {
		t.Run(kind, func(t *testing.T) {
			o := options(t)
			switch kind {
			case "network":
				o.Network = "../escape"
			case "repository":
				o.Repository = "https://metadata.example.invalid/omega/"
			case "production":
				o.Disposable = false
			case "home-symlink":
				must(t, os.Symlink(t.TempDir(), o.Home))
			case "home-mode":
				must(t, os.Mkdir(o.Home, 0755))
			case "parent-mode":
				must(t, os.Chmod(filepath.Dir(o.Home), 0777))
			default:
				must(t, os.Mkdir(o.Home, 0700))
				switch kind {
				case "slot-file":
					must(t, os.WriteFile(finalPath(o), []byte("keep"), 0600))
				case "slot-symlink":
					must(t, os.Symlink(t.TempDir(), finalPath(o)))
				case "lock-symlink":
					must(t, os.Symlink(filepath.Join(t.TempDir(), "missing"), filepath.Join(o.Home, o.Network+".lock")))
				case "stage-symlink":
					must(t, os.Symlink(t.TempDir(), stagePath(o)))
				}
			}
			if _, err := Init(context.Background(), o); err == nil {
				t.Fatal("accepted unsafe initialization")
			}
		})
	}
}

func TestExistingSlotCannotBeClobberedAtCommit(t *testing.T) {
	o := options(t)
	_, err := initialize(context.Background(), o, testTime, func(at string) error {
		if at == "verified" {
			return os.Mkdir(finalPath(o), 0700)
		}
		return nil
	})
	if err == nil {
		t.Fatal("overwrote a concurrently created slot")
	}
	entries, err := os.ReadDir(finalPath(o))
	must(t, err)
	if len(entries) != 0 {
		t.Fatal("modified preexisting slot")
	}
	if _, err := os.Stat(stagePath(o)); err != nil {
		t.Fatal("lost staged authority")
	}
}

func TestRootExpiryAndCanceledLock(t *testing.T) {
	o := options(t)
	first, err := initialize(context.Background(), o, testTime, nil)
	must(t, err)
	if report, err := initialize(context.Background(), o, first.RootExpires, nil); err == nil || report.State != "expired" {
		t.Fatal("expiry equality must fail")
	}
	home, err := openHome(context.Background(), o.Home, o.Network, false)
	must(t, err)
	defer home.close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := Init(ctx, o); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock cancellation: %v", err)
	}
}

// Kill a real initializer with the network lock held, then recover in another
// process. This covers OS lock release, not merely a returned Go error.
func TestProcessDeathRecovery(t *testing.T) {
	if os.Getenv("SOPH_OMEGA_TEST_CHILD") == "1" {
		o := InitOptions{Home: os.Getenv("SOPH_OMEGA_TEST_HOME"), Network: "rehearsal", Repository: "https://metadata.example.invalid", Disposable: true}
		_, err := initialize(context.Background(), o, testTime, func(at string) error {
			if at == os.Getenv("SOPH_OMEGA_TEST_PHASE") {
				if err := os.WriteFile(filepath.Join(o.Home, "ready"), []byte("ready"), 0600); err != nil {
					return err
				}
				for {
					time.Sleep(time.Second)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	for _, phase := range []string{"authority.json:durable", "verified", "promoted"} {
		t.Run(phase, func(t *testing.T) {
			o := options(t)
			cmd := exec.Command(os.Args[0], "-test.run=^TestProcessDeathRecovery$")
			cmd.Env = append(os.Environ(), "SOPH_OMEGA_TEST_CHILD=1", "SOPH_OMEGA_TEST_HOME="+o.Home, "SOPH_OMEGA_TEST_PHASE="+phase)
			var output bytes.Buffer
			cmd.Stdout = &output
			cmd.Stderr = &output
			must(t, cmd.Start())
			defer func() { _ = cmd.Process.Kill() }()
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(o.Home, "ready")); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("child did not reach commit boundary")
				}
				time.Sleep(10 * time.Millisecond)
			}
			location := stagePath(o)
			if phase == "promoted" {
				location = finalPath(o)
			}
			original := file(t, filepath.Join(location, "authority.json"))
			must(t, cmd.Process.Kill())
			_ = cmd.Wait()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := Init(ctx, o)
			must(t, err)
			if !bytes.Equal(original, file(t, filepath.Join(finalPath(o), "authority.json"))) {
				t.Fatal("process recovery changed keys")
			}
		})
	}
}

func TestNoSecretInCorruptionErrors(t *testing.T) {
	o := options(t)
	_, err := Init(context.Background(), o)
	must(t, err)
	data := file(t, filepath.Join(finalPath(o), "authority.json"))
	var a authority
	must(t, decodeRecord(data, &a))
	a.Keys["root-1"] = "SECRET-NOT-LOGGED"
	must(t, os.WriteFile(filepath.Join(finalPath(o), "authority.json"), record(a), 0600))
	report, err := Status(context.Background(), o.Home, o.Network)
	if err == nil || strings.Contains(err.Error(), "SECRET-NOT-LOGGED") || bytes.Contains(record(report), []byte("SECRET-NOT-LOGGED")) {
		t.Fatal("secret leaked or corruption accepted")
	}
}

func TestCompletePendingRecordIsNeverReplaced(t *testing.T) {
	o := options(t)
	_, err := initialize(context.Background(), o, testTime, func(at string) error {
		if at == "authority.json:written" {
			return errors.New("interrupted")
		}
		return nil
	})
	if err == nil {
		t.Fatal("expected interruption")
	}
	path := filepath.Join(stagePath(o), "authority.json.pending")
	var a authority
	must(t, decodeRecord(file(t, path), &a))
	a.Keys["root-1"] = "invalid-private-key"
	damaged := record(a)
	must(t, os.WriteFile(path, damaged, 0600))
	if _, err := Init(context.Background(), o); err == nil {
		t.Fatal("regenerated a fully recorded pending key set")
	}
	if !bytes.Equal(damaged, file(t, path)) {
		t.Fatal("overwrote invalid pending keys")
	}
	assertAbsent(t, finalPath(o))
}

func TestStagingPermissionsAndPublicOutputCannotBeBypassed(t *testing.T) {
	for _, kind := range []string{"private-mode", "public-mode", "public-symlink", "public-mismatch", "unknown-entry"} {
		t.Run(kind, func(t *testing.T) {
			o := options(t)
			_, err := initialize(context.Background(), o, testTime, func(at string) error {
				if at == "bundle.json:durable" {
					return errors.New("interrupted")
				}
				return nil
			})
			if err == nil {
				t.Fatal("expected interruption")
			}
			switch kind {
			case "private-mode":
				must(t, os.Chmod(filepath.Join(stagePath(o), "authority.json"), 0644))
			case "public-mode":
				must(t, os.Chmod(filepath.Join(stagePath(o), "bundle.json"), 0644))
			case "public-symlink":
				must(t, os.Remove(filepath.Join(stagePath(o), "bundle.json")))
				must(t, os.Symlink(filepath.Join(t.TempDir(), "absent"), filepath.Join(stagePath(o), "bundle.json")))
			case "public-mismatch":
				must(t, os.WriteFile(filepath.Join(stagePath(o), "bundle.json"), []byte("{}"), 0600))
			case "unknown-entry":
				must(t, os.WriteFile(filepath.Join(stagePath(o), "unexpected"), []byte("keep"), 0600))
			}
			if _, err := Init(context.Background(), o); err == nil {
				t.Fatal("accepted unsafe or inconsistent staging")
			}
			assertAbsent(t, finalPath(o))
		})
	}
}
