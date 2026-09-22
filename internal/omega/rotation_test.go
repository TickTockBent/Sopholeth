//go:build linux

package omega

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
	"sopholeth/internal/trust/bootstrap"
)

func rotationFixture(t *testing.T) (*publishFixture, RenewOptions, RotateOptions) {
	t.Helper()
	f, _, renew := renewalFixture(t, 0)
	return f, renew, RotateOptions{Home: f.opts.Home, Network: f.opts.Network, RootVersion: 2, Disposable: true, HTTPClient: f.opts.HTTPClient}
}

func fixtureBundle(t *testing.T, f *publishFixture) bootstrap.Bundle {
	t.Helper()
	bundle, err := bootstrap.ParseBundle(file(t, filepath.Join(f.opts.Home, f.opts.Network, "bundle.json")))
	must(t, err)
	return bundle
}

func fixtureClient(t *testing.T, bundle bootstrap.Bundle, hc *http.Client) *bootstrap.Client {
	t.Helper()
	c, err := bootstrap.New(context.Background(), bootstrap.Config{Bundle: bundle, StateDir: filepath.Join(t.TempDir(), "client"), HTTPClient: hc})
	must(t, err)
	return c
}

func recordedRelease(t *testing.T, online RenewOptions, version int64) release {
	t.Helper()
	var r release
	must(t, decodeRecord(file(t, filepath.Join(onlineState(online), releaseName(version))), &r))
	return r
}

func TestOnlineRotationClientsRenewalAndApproval(t *testing.T) {
	f, renewal, opts := rotationFixture(t)
	ctx := context.Background()
	bundle := fixtureBundle(t, f)
	returning := fixtureClient(t, bundle, opts.HTTPClient)
	_, err := returning.Refresh(ctx)
	must(t, err)
	first := recordedRelease(t, renewal, 1)
	authorityPath := filepath.Join(f.opts.Home, f.opts.Network, "authority.json")
	authorityBytes := file(t, authorityPath)
	prepared, err := Rotate(ctx, opts)
	must(t, err)
	if prepared.Rotation.State != "prepared" || prepared.RootVersion != 1 || prepared.Fingerprint != bundle.Fingerprint() {
		t.Fatalf("bad preparation: %+v", prepared)
	}
	assertAbsent(t, filepath.Join(f.opts.Directory, "2.root.json"))
	if !bytes.Equal(first.Timestamp, file(t, filepath.Join(f.opts.Directory, "timestamp.json"))) {
		t.Fatal("preparation published a transition")
	}
	again, err := Rotate(ctx, opts)
	must(t, err)
	if !bytes.Equal(record(prepared.Rotation), record(again.Rotation)) {
		t.Fatal("preparation changed keys or root")
	}
	opts.Apply = prepared.Rotation.RootSHA256
	applied, err := Rotate(ctx, opts)
	must(t, err)
	if applied.Publication != "verified" || applied.Rotation.State != "applied" || applied.RootVersion != 2 ||
		applied.Release.Versions != (bootstrap.Versions{Root: 2, Targets: 1, Snapshot: 2, Timestamp: 2}) || applied.Fingerprint != bundle.Fingerprint() {
		t.Fatalf("bad application: %+v", applied)
	}
	second := recordedRelease(t, renewal, 2)
	if second.Schema != 4 || !bytes.Equal(second.Targets, first.Targets) || !bytes.Equal(second.Manifest, first.Manifest) {
		t.Fatal("rotation changed membership approval")
	}
	for _, c := range []*bootstrap.Client{returning, fixtureClient(t, bundle, opts.HTTPClient)} {
		view, err := c.Refresh(ctx)
		must(t, err)
		if view.Versions != applied.Release.Versions || view.MetadataSHA256["root"] != opts.Apply {
			t.Fatal("client failed to adopt exact successor")
		}
	}
	for _, home := range []string{f.opts.Home, renewal.Home} {
		local, err := Status(ctx, home, opts.Network)
		must(t, err)
		if local.RootVersion != 2 || !bytes.Equal(record(local.Roles), record(applied.Roles)) {
			t.Fatal("status still reported retired assignments")
		}
	}
	_, err = Rotate(ctx, opts)
	must(t, err)
	assertAbsent(t, filepath.Join(onlineState(renewal), "3.release.json"))
	// Runtime renewal must work with the entire offline home unavailable.
	must(t, os.Rename(f.opts.Home, f.opts.Home+"-offline"))
	_, err = renew(ctx, renewal, time.Now().UTC().Add(7*time.Hour), nil)
	must(t, err)
	third := recordedRelease(t, renewal, 3)
	if third.versions().Root != 2 || !bytes.Equal(third.Targets, first.Targets) {
		t.Fatal("renewal lost rotated authority")
	}
	must(t, os.Rename(f.opts.Home+"-offline", f.opts.Home))
	f.opts.Version = 4
	f.changeRoot()
	_, err = publish(ctx, f.opts, time.Now().UTC().Add(8*time.Hour), nil)
	must(t, err)
	fourth := recordedRelease(t, renewal, 4)
	if fourth.Schema != 3 || fourth.versions().Root != 2 || bytes.Equal(fourth.Manifest, first.Manifest) {
		t.Fatal("offline approval failed after rotation")
	}
	// A second transition proves a client absent for several rotations can
	// traverse the retained chain using its unchanged initial bundle.
	opts.RootVersion, opts.Apply = 3, ""
	prepared, err = rotate(ctx, opts, time.Now().UTC().Add(9*time.Hour), nil)
	must(t, err)
	opts.Apply = prepared.Rotation.RootSHA256
	applied, err = rotate(ctx, opts, time.Now().UTC().Add(9*time.Hour), nil)
	must(t, err)
	for _, c := range []*bootstrap.Client{returning, fixtureClient(t, bundle, opts.HTTPClient)} {
		view, err := c.Refresh(ctx)
		must(t, err)
		if view.Versions.Root != 3 || view.Versions.Timestamp != 5 {
			t.Fatal("client failed to cross retained transitions")
		}
	}
	for _, path := range []string{"1.root.json", "2.root.json", "3.root.json"} {
		if len(file(t, filepath.Join(f.opts.Directory, path))) == 0 {
			t.Fatal("missing retained root")
		}
	}
	if !bytes.Equal(authorityBytes, file(t, authorityPath)) || !bytes.Equal(record(bundle), file(t, filepath.Join(f.opts.Home, f.opts.Network, "bundle.json"))) {
		t.Fatal("rotation rewrote original custody or trust bundle")
	}
	// Public files and the operational home must never contain offline keys.
	var a authority
	must(t, decodeRecord(authorityBytes, &a))
	for _, dir := range []string{f.opts.Directory, renewal.Home} {
		must(t, filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			data := file(t, path)
			for _, role := range keyNames[:4] {
				if bytes.Contains(data, []byte(a.Keys[role])) {
					t.Fatalf("offline key leaked to %s", path)
				}
			}
			return nil
		}))
	}
}

func TestOnlineRotationPreparationRecovery(t *testing.T) {
	for _, phase := range []string{"2.rotation-intent.json:written", "2.rotation-intent.json:linked", "rotation:keys-durable", "2.rotation.json:written", "2.rotation.json:linked"} {
		t.Run(phase, func(t *testing.T) {
			f, renewal, opts := rotationFixture(t)
			stop := errors.New("interrupted")
			_, err := rotate(context.Background(), opts, time.Time{}, func(at string) error {
				if at == phase {
					return stop
				}
				return nil
			})
			if !errors.Is(err, stop) {
				t.Fatalf("missed phase: %v", err)
			}
			var before []byte
			for _, name := range []string{"2.rotation-intent.json", "2.rotation-intent.json.pending"} {
				if data, err := os.ReadFile(filepath.Join(onlineState(renewal), name)); err == nil {
					before = data
					break
				}
			}
			if phase == "2.rotation.json:written" {
				must(t, os.WriteFile(filepath.Join(onlineState(renewal), "2.rotation.json.pending"), []byte(`{"schema":`), 0600))
			}
			prepared, err := Rotate(context.Background(), opts)
			must(t, err)
			if prepared.Rotation.State != "prepared" || !bytes.Equal(before, file(t, filepath.Join(onlineState(renewal), "2.rotation-intent.json"))) {
				t.Fatal("recovery replaced prepared keys")
			}
			assertAbsent(t, filepath.Join(f.opts.Directory, "2.root.json"))
			pending, err := filepath.Glob(filepath.Join(onlineState(renewal), "*.pending"))
			must(t, err)
			if len(pending) != 0 {
				t.Fatalf("pending twins survived: %v", pending)
			}
		})
	}
}

func TestOnlineRotationApplyRecovery(t *testing.T) {
	for _, phase := range []string{"2.rotation-apply.json:written", "2.rotation-apply.json:linked", "rotation:apply-durable", "2.release.json:written", "2.release.json:linked", "release:durable", "public:2.snapshot.json:visible", "public:2.root.json:written", "public:2.root.json:visible", "public:timestamp.json:visible", "publication:verified"} {
		t.Run(phase, func(t *testing.T) {
			f, renewal, opts := rotationFixture(t)
			prepared, err := Rotate(context.Background(), opts)
			must(t, err)
			opts.Apply = prepared.Rotation.RootSHA256
			stop := errors.New("interrupted")
			_, err = rotate(context.Background(), opts, time.Time{}, func(at string) error {
				if at == phase {
					return stop
				}
				return nil
			})
			if !errors.Is(err, stop) {
				t.Fatalf("missed phase: %v", err)
			}
			var before []byte
			for _, name := range []string{"2.release.json", "2.release.json.pending"} {
				if data, err := os.ReadFile(filepath.Join(onlineState(renewal), name)); err == nil {
					before = data
					break
				}
			}
			if phase == "2.release.json:written" {
				must(t, os.WriteFile(filepath.Join(onlineState(renewal), "2.release.json.pending"), []byte(`{"schema":`), 0600))
			}
			must(t, os.Rename(f.opts.Home, f.opts.Home+"-offline"))
			report, err := Renew(context.Background(), renewal)
			must(t, err)
			if report.Publication != "verified" || report.Release.Version != 2 || report.RootVersion != 2 {
				t.Fatalf("bad rotation recovery: %+v", report)
			}
			if before != nil && !bytes.Equal(before, file(t, filepath.Join(onlineState(renewal), "2.release.json"))) {
				t.Fatal("retry changed reserved release")
			}
			assertAbsent(t, filepath.Join(onlineState(renewal), "3.release.json"))
			for _, dir := range []string{onlineState(renewal), f.opts.Directory} {
				pending, err := filepath.Glob(filepath.Join(dir, "*.pending"))
				must(t, err)
				if len(pending) != 0 {
					t.Fatalf("pending twins survived: %v", pending)
				}
			}
		})
	}
}

func TestOnlineRotationRejectsUnreviewedAndReservedApply(t *testing.T) {
	for _, kind := range []string{"unprepared", "wrong-digest", "skipped-root", "pending-approval", "pending-renewal"} {
		t.Run(kind, func(t *testing.T) {
			f, renewal, opts := rotationFixture(t)
			if kind != "unprepared" {
				prepared, err := Rotate(context.Background(), opts)
				must(t, err)
				opts.Apply = prepared.Rotation.RootSHA256
			} else {
				opts.Apply = strings.Repeat("0", 64)
			}
			switch kind {
			case "wrong-digest":
				opts.Apply = strings.Repeat("0", 64)
			case "skipped-root":
				opts.RootVersion = 3
			case "pending-approval":
				must(t, os.WriteFile(filepath.Join(onlineState(renewal), "2.release.json.pending"), []byte(`{"schema":`), 0600))
			case "pending-renewal":
				must(t, os.WriteFile(filepath.Join(onlineState(renewal), "2.renewal-intent.json.pending"), []byte(`{"schema":`), 0600))
			}
			report, err := Rotate(context.Background(), opts)
			if err == nil || report.Problem == "" || report.Action == "" {
				t.Fatal("unsafe apply succeeded or lacked actionable report")
			}
			assertAbsent(t, filepath.Join(onlineState(renewal), "2.rotation-apply.json"))
			assertAbsent(t, filepath.Join(f.opts.Directory, "2.root.json"))
		})
	}
}

func TestOnlineRotationRejectsRetiredKeysAndBrokenChain(t *testing.T) {
	f, renewal, opts := rotationFixture(t)
	ctx := context.Background()
	bundle := fixtureBundle(t, f)
	returning := fixtureClient(t, bundle, opts.HTTPClient)
	_, err := returning.Refresh(ctx)
	must(t, err)
	prepared, err := Rotate(ctx, opts)
	must(t, err)
	opts.Apply = prepared.Rotation.RootSHA256
	_, err = Rotate(ctx, opts)
	must(t, err)
	_, err = returning.Refresh(ctx)
	must(t, err)
	first, second := recordedRelease(t, renewal, 1), recordedRelease(t, renewal, 2)
	var a authority
	must(t, decodeRecord(file(t, filepath.Join(f.opts.Home, f.opts.Network, "authority.json")), &a))
	oldTimestamp, err := metadata.Timestamp().FromBytes(second.Timestamp)
	must(t, err)
	oldTimestamp.Signed.Version++
	oldTimestamp.Signatures = nil
	forgedTimestamp, err := signMetadata(oldTimestamp, a.Keys["timestamp"])
	must(t, err)
	oldSnapshot, err := metadata.Snapshot().FromBytes(second.Snapshot)
	must(t, err)
	oldSnapshot.Signatures = nil
	forgedSnapshot, err := signMetadata(oldSnapshot, a.Keys["snapshot"])
	must(t, err)
	var intent rotationIntent
	must(t, decodeRecord(file(t, filepath.Join(onlineState(renewal), "2.rotation-intent.json")), &intent))
	legitimateTimestamp, err := metadata.Timestamp().FromBytes(second.Timestamp)
	must(t, err)
	legitimateTimestamp.Signed.Version++
	legitimateTimestamp.Signed.Meta["snapshot.json"] = metadataFile(2, forgedSnapshot)
	legitimateTimestamp.Signatures = nil
	timestampForBadSnapshot, err := signMetadata(legitimateTimestamp, intent.Keys["timestamp"])
	must(t, err)
	for _, kind := range []string{"missing-root", "unauthorized-root", "retired-timestamp", "retired-snapshot", "rollback", "incomplete-snapshot"} {
		t.Run(kind, func(t *testing.T) {
			f.override("/timestamp.json", nil, 0)
			f.override("/2.snapshot.json", nil, 0)
			f.override("/2.root.json", nil, 0)
			switch kind {
			case "missing-root":
				f.override("/2.root.json", nil, http.StatusNotFound)
			case "unauthorized-root":
				root, err := metadata.Root().FromBytes(second.Roots[0])
				must(t, err)
				root.Signatures = root.Signatures[:1]
				raw, err := root.ToBytes(false)
				must(t, err)
				f.override("/2.root.json", raw, 0)
			case "retired-timestamp":
				f.override("/timestamp.json", forgedTimestamp, 0)
			case "retired-snapshot":
				f.override("/timestamp.json", timestampForBadSnapshot, 0)
				f.override("/2.snapshot.json", forgedSnapshot, 0)
			case "rollback":
				f.override("/timestamp.json", first.Timestamp, 0)
			case "incomplete-snapshot":
				f.override("/2.snapshot.json", nil, http.StatusNotFound)
			}
			client := fixtureClient(t, bundle, opts.HTTPClient)
			if _, err := client.Refresh(ctx); err == nil {
				t.Fatal("fresh client accepted unauthorized or incomplete successor")
			}
			if kind == "rollback" || kind == "retired-timestamp" {
				if _, err := returning.Refresh(ctx); err == nil {
					t.Fatal("returning client accepted retired keys")
				}
			}
		})
	}
}

func TestOnlineRotationInterruptedClientRevocation(t *testing.T) {
	f, renewal, opts := rotationFixture(t)
	ctx := context.Background()
	bundle := fixtureBundle(t, f)
	clientDir := filepath.Join(t.TempDir(), "returning")
	config := bootstrap.Config{Bundle: bundle, StateDir: clientDir, HTTPClient: opts.HTTPClient}
	client, err := bootstrap.New(ctx, config)
	must(t, err)
	_, err = client.Refresh(ctx)
	must(t, err)
	prepared, err := Rotate(ctx, opts)
	must(t, err)
	opts.Apply = prepared.Rotation.RootSHA256
	stop := errors.New("root visible, timestamp not yet replaced")
	_, err = rotate(ctx, opts, time.Time{}, func(at string) error {
		if at == "public:2.root.json:visible" {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
	if _, err := client.Refresh(ctx); err == nil {
		t.Fatal("accepted old timestamp after authenticating new root")
	}
	client, err = bootstrap.New(ctx, config)
	must(t, err)
	if _, err := client.Current(ctx); err == nil {
		t.Fatal("restart restored a lease under retired keys")
	}
	must(t, os.Rename(f.opts.Home, f.opts.Home+"-offline"))
	_, err = Renew(ctx, renewal)
	must(t, err)
	view, err := client.Refresh(ctx)
	must(t, err)
	if view.Versions.Root != 2 || view.Versions.Timestamp != 2 {
		t.Fatal("returning client could not recover")
	}
}

func TestOnlineRotationExpiredReservationRecovery(t *testing.T) {
	for _, kind := range []string{"timestamp", "membership", "torn-apply"} {
		t.Run(kind, func(t *testing.T) {
			age, advance := time.Duration(0), 25*time.Hour
			if kind != "timestamp" {
				age, advance = 90*24*time.Hour-time.Hour, 2*time.Hour
			}
			f, _, renewal := renewalFixture(t, age)
			opts := RotateOptions{Home: f.opts.Home, Network: f.opts.Network, RootVersion: 2, Disposable: true, HTTPClient: f.opts.HTTPClient}
			prepared, err := Rotate(context.Background(), opts)
			must(t, err)
			opts.Apply = prepared.Rotation.RootSHA256
			phase := "rotation:apply-durable"
			if kind == "torn-apply" {
				phase = "2.rotation-apply.json:written"
			}
			stop := errors.New("stop")
			_, err = rotate(context.Background(), opts, time.Time{}, func(at string) error {
				if at == phase {
					return stop
				}
				return nil
			})
			if !errors.Is(err, stop) {
				t.Fatal(err)
			}
			now := time.Now().UTC().Add(advance)
			before, beforeErr := os.ReadFile(filepath.Join(f.opts.Directory, "timestamp.json"))
			if beforeErr != nil && !errors.Is(beforeErr, os.ErrNotExist) {
				t.Fatal(beforeErr)
			}
			f.opts.Version = 3
			if kind == "torn-apply" {
				must(t, os.WriteFile(filepath.Join(onlineState(renewal), "2.rotation-apply.json.pending"), nil, 0600))
				if _, err := rotate(context.Background(), opts, now, nil); err == nil {
					t.Fatal("expired approval allowed apply")
				}
				assertAbsent(t, filepath.Join(onlineState(renewal), "2.rotation-apply.json.pending"))
				assertAbsent(t, filepath.Join(onlineState(renewal), "2.release.json"))
				f.opts.Version = 2
			} else {
				if _, err := renew(context.Background(), renewal, now, nil); err == nil {
					t.Fatal("expired rotation reservation reported success")
				}
				_ = recordedRelease(t, renewal, 2)
			}
			after, afterErr := os.ReadFile(filepath.Join(f.opts.Directory, "timestamp.json"))
			if afterErr != nil && !errors.Is(afterErr, os.ErrNotExist) {
				t.Fatal(afterErr)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("expired timestamp was published")
			}
			if kind == "timestamp" {
				report, err := renew(context.Background(), renewal, now.Add(time.Second), nil)
				must(t, err)
				if report.Release.Version != 3 || report.RootVersion != 2 {
					t.Fatal("higher repair lost successor or version")
				}
			} else {
				f.changeRoot()
				_, err := publish(context.Background(), f.opts, now.Add(time.Second), nil)
				must(t, err)
				if kind == "torn-apply" {
					// Prepared root 2 is still reusable after the new approval.
					report, err := rotate(context.Background(), opts, now.Add(2*time.Second), nil)
					must(t, err)
					if report.Release.Version != 3 || report.Release.Versions.Targets != 2 {
						t.Fatal("apply did not preserve the latest approval")
					}
				}
			}
		})
	}
}

func TestOnlineRotationClockAndConcurrentRetries(t *testing.T) {
	f, renewal, opts := rotationFixture(t)
	preparedAt := time.Now().UTC().Add(time.Hour)
	prepared, err := rotate(context.Background(), opts, preparedAt, nil)
	must(t, err)
	opts.Apply = prepared.Rotation.RootSHA256
	if _, err := Rotate(context.Background(), opts); err == nil {
		t.Fatal("backward clock reserved an unusable rotation")
	}
	assertAbsent(t, filepath.Join(onlineState(renewal), "2.rotation-apply.json"))
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			_, err := rotate(context.Background(), opts, preparedAt.Add(time.Second), nil)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		must(t, err)
	}
	assertAbsent(t, filepath.Join(onlineState(renewal), "3.release.json"))
	if !bytes.Equal(file(t, filepath.Join(f.opts.Directory, "2.root.json")), recordedRelease(t, renewal, 2).Roots[0]) {
		t.Fatal("concurrent retries changed root")
	}
}

func TestOnlineRotationProcessDeathRecovery(t *testing.T) {
	if os.Getenv("SOPH_ROTATION_TEST_CHILD") == "1" {
		home := os.Getenv("SOPH_ROTATION_TEST_HOME")
		opts := RotateOptions{Home: home, Network: "rehearsal", RootVersion: 2, Apply: os.Getenv("SOPH_ROTATION_TEST_DIGEST"), Disposable: true}
		_, err := rotate(context.Background(), opts, time.Time{}, func(at string) error {
			if at == os.Getenv("SOPH_ROTATION_TEST_PHASE") {
				if err := os.WriteFile(filepath.Join(home, "ready"), []byte("ready"), 0600); err != nil {
					return err
				}
				for {
					time.Sleep(time.Second)
				}
			}
			return nil
		})
		must(t, err)
		return
	}
	for _, phase := range []string{"rotation:apply-durable", "public:2.root.json:visible"} {
		t.Run(phase, func(t *testing.T) {
			f, renewal, opts := rotationFixture(t)
			prepared, err := Rotate(context.Background(), opts)
			must(t, err)
			cmd := exec.Command(os.Args[0], "-test.run=^TestOnlineRotationProcessDeathRecovery$")
			cmd.Env = append(os.Environ(), "SOPH_ROTATION_TEST_CHILD=1", "SOPH_ROTATION_TEST_HOME="+opts.Home,
				"SOPH_ROTATION_TEST_DIGEST="+prepared.Rotation.RootSHA256, "SOPH_ROTATION_TEST_PHASE="+phase)
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			must(t, cmd.Start())
			defer func() { _ = cmd.Process.Kill() }()
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(opts.Home, "ready")); err == nil {
					break
				}
				if time.Now().After(deadline) {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					t.Fatalf("child missed boundary: %s", output.String())
				}
				time.Sleep(10 * time.Millisecond)
			}
			must(t, cmd.Process.Kill())
			_ = cmd.Wait()
			must(t, os.Rename(f.opts.Home, f.opts.Home+"-offline"))
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			report, err := Renew(ctx, renewal)
			must(t, err)
			if report.RootVersion != 2 || report.Release.Version != 2 || report.Publication != "verified" {
				t.Fatalf("bad process-death recovery: %+v", report)
			}
		})
	}
}

func TestOnlineRotationPreservesDamagedState(t *testing.T) {
	for _, kind := range []string{"missing-keys", "changed-keys", "pending-keys", "pending-root", "pending-apply", "missing-apply", "unknown-public-root"} {
		t.Run(kind, func(t *testing.T) {
			f, renewal, opts := rotationFixture(t)
			state := onlineState(renewal)
			var path string
			stop := errors.New("stop")
			if kind == "pending-keys" || kind == "pending-root" {
				phase := "2.rotation-intent.json:written"
				path = filepath.Join(state, "2.rotation-intent.json.pending")
				if kind == "pending-root" {
					phase = "2.rotation.json:written"
					path = filepath.Join(state, "2.rotation.json.pending")
				}
				_, err := rotate(context.Background(), opts, time.Time{}, func(at string) error {
					if at == phase {
						return stop
					}
					return nil
				})
				if !errors.Is(err, stop) {
					t.Fatal(err)
				}
				if kind == "pending-keys" {
					var intent rotationIntent
					must(t, decodeRecord(file(t, path), &intent))
					intent.Fingerprint = "wrong"
					must(t, os.WriteFile(path, record(intent), 0600))
				} else {
					var r rotationRecord
					must(t, decodeRecord(file(t, path), &r))
					r.RootVersion++
					must(t, os.WriteFile(path, record(r), 0600))
				}
			} else {
				prepared, err := Rotate(context.Background(), opts)
				must(t, err)
				opts.Apply = prepared.Rotation.RootSHA256
				switch kind {
				case "missing-keys":
					must(t, os.Remove(filepath.Join(state, "2.rotation-intent.json")))
					opts.Apply = "" // Even preparation may not generate replacement keys.
				case "changed-keys":
					path = filepath.Join(state, "2.rotation-intent.json")
					var intent rotationIntent
					must(t, decodeRecord(file(t, path), &intent))
					intent.Keys["snapshot"] = intent.Keys["timestamp"]
					must(t, os.WriteFile(path, record(intent), 0600))
				case "pending-apply", "missing-apply":
					phase := "2.rotation-apply.json:written"
					path = filepath.Join(state, "2.rotation-apply.json.pending")
					if kind == "missing-apply" {
						phase = "2.release.json:written"
						path = filepath.Join(state, "2.release.json.pending")
					}
					_, err := rotate(context.Background(), opts, time.Time{}, func(at string) error {
						if at == phase {
							return stop
						}
						return nil
					})
					if !errors.Is(err, stop) {
						t.Fatal(err)
					}
					if kind == "missing-apply" {
						must(t, os.Remove(filepath.Join(state, "2.rotation-apply.json")))
					} else {
						var intent rotationApply
						must(t, decodeRecord(file(t, path), &intent))
						intent.RootSHA256 = "wrong"
						must(t, os.WriteFile(path, record(intent), 0600))
					}
				case "unknown-public-root":
					path = filepath.Join(f.opts.Directory, "3.root.json")
					must(t, os.WriteFile(path, fixtureBundle(t, f).Root, 0644))
				}
			}
			var before []byte
			if path != "" {
				before = file(t, path)
			}
			if _, err := Rotate(context.Background(), opts); err == nil {
				t.Fatal("damaged or conflicting rotation state was accepted")
			}
			if path != "" && !bytes.Equal(before, file(t, path)) {
				t.Fatal("damaged state was replaced")
			}
			assertAbsent(t, filepath.Join(state, "2.release.json"))
			assertAbsent(t, filepath.Join(f.opts.Directory, "2.root.json"))
		})
	}
}

func TestOnlineRotationRejectsOfflinePolicyChanges(t *testing.T) {
	f, renewal, opts := rotationFixture(t)
	_, err := Rotate(context.Background(), opts)
	must(t, err)
	var prepared rotationRecord
	must(t, decodeRecord(file(t, filepath.Join(onlineState(renewal), "2.rotation.json")), &prepared))
	var intent rotationIntent
	must(t, decodeRecord(file(t, filepath.Join(onlineState(renewal), "2.rotation-intent.json")), &intent))
	var a authority
	must(t, decodeRecord(file(t, filepath.Join(f.opts.Home, f.opts.Network, "authority.json")), &a))
	bundle := fixtureBundle(t, f)
	for _, kind := range []string{"expiry", "root-threshold", "targets-threshold", "consistent-snapshot", "retained-old-key"} {
		t.Run(kind, func(t *testing.T) {
			r := prepared
			root, err := metadata.Root().FromBytes(prepared.Root)
			must(t, err)
			switch kind {
			case "expiry":
				root.Signed.Expires = root.Signed.Expires.Add(time.Hour)
			case "root-threshold":
				root.Signed.Roles["root"].Threshold = 1
			case "targets-threshold":
				root.Signed.Roles["targets"].Threshold = 2
			case "consistent-snapshot":
				root.Signed.ConsistentSnapshot = false
			case "retained-old-key":
				old, err := metadata.Root().FromBytes(bundle.Root)
				must(t, err)
				id := old.Signed.Roles["timestamp"].KeyIDs[0]
				must(t, root.Signed.AddKey(old.Signed.Keys[id], "timestamp"))
			}
			root.Signatures = nil
			_, err = signMetadata(root, a.Keys["root-1"])
			must(t, err)
			r.Root, err = signMetadata(root, a.Keys["root-2"])
			must(t, err)
			if err := r.validate(bundle, bundle.Root, intent); err == nil {
				t.Fatal("online-only rotation accepted a broader policy change")
			}
		})
	}
}
