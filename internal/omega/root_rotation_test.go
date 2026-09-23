//go:build linux

package omega

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
	"sopholeth/internal/trust/bootstrap"
)

func applyRoot(t *testing.T, opts RotateOptions) Report {
	t.Helper()
	opts.Role = "root"
	prepared, err := Rotate(context.Background(), opts)
	must(t, err)
	opts.Apply, opts.RenewApproval = prepared.Rotation.RootSHA256, true
	applied, err := Rotate(context.Background(), opts)
	must(t, err)
	return applied
}

func TestRootRotationQuorumsAndAlternatingRoles(t *testing.T) {
	f, online, opts := rotationFixture(t)
	ctx := context.Background()
	bundle := fixtureBundle(t, f)
	returning := fixtureClient(t, bundle, opts.HTTPClient)
	_, err := returning.Refresh(ctx)
	must(t, err)
	initial := file(t, filepath.Join(f.opts.Home, opts.Network, "authority.json"))
	first := recordedRelease(t, online, 1)
	for _, role := range []string{"root", "online", "targets", "root", "targets", "online"} {
		opts.Role, opts.Apply, opts.RenewApproval = role, "", false
		prepared, err := Rotate(ctx, opts)
		must(t, err)
		before := file(t, filepath.Join(f.opts.Directory, "timestamp.json"))
		again, err := Rotate(ctx, opts)
		must(t, err)
		if !bytes.Equal(record(prepared.Rotation), record(again.Rotation)) || !bytes.Equal(before, file(t, filepath.Join(f.opts.Directory, "timestamp.json"))) {
			t.Fatal("preparation changed keys or publication")
		}
		opts.Apply = prepared.Rotation.RootSHA256
		if role == "root" {
			if _, err := Rotate(ctx, opts); err == nil {
				t.Fatal("root apply omitted approval consent")
			}
			opts.RenewApproval = true
		}
		applied, err := Rotate(ctx, opts)
		must(t, err)
		if applied.RootVersion != opts.RootVersion || applied.Publication != "verified" || applied.Rotation.Role != role || applied.Fingerprint != bundle.Fingerprint() {
			t.Fatalf("bad application: %+v", applied)
		}
		if role == "root" && (!applied.RootExpires.After(first.Created.Add(rootLifetime)) || applied.Roles["root"].Threshold != 2 || len(applied.Rotation.Keys["root"]) != 3) {
			t.Fatal("root policy or review fields missing")
		}
		_, err = Rotate(ctx, opts)
		must(t, err)
		if role == "root" {
			r := recordedRelease(t, online, opts.RootVersion)
			if r.Schema != 6 || !bytes.Equal(r.Manifest, first.Manifest) || !r.approvalTime().Equal(r.Created) {
				t.Fatal("root transition did not explicitly reapprove unchanged membership")
			}
		}
		opts.RootVersion++
	}
	for _, client := range []*bootstrap.Client{returning, fixtureClient(t, bundle, opts.HTTPClient)} {
		view, err := client.Refresh(ctx)
		must(t, err)
		if view.Versions.Root != 7 {
			t.Fatal("client failed to cross retained authority and role transitions")
		}
	}
	if !bytes.Equal(initial, file(t, filepath.Join(f.opts.Home, opts.Network, "authority.json"))) {
		t.Fatal("root rotation changed initialization")
	}
	f.opts.Version = 8
	f.changeRoot()
	_, err = Publish(ctx, f.opts)
	must(t, err)
	must(t, os.Rename(f.opts.Home, f.opts.Home+"-offline"))
	_, err = renew(ctx, online, time.Now().UTC().Add(7*time.Hour), nil)
	must(t, err)
}

func TestRootRotationExpiredAuthorityRecovery(t *testing.T) {
	ctx := context.Background()
	created := time.Now().UTC().Truncate(time.Second).Add(-366 * 24 * time.Hour)
	f := newPublishFixtureAt(t, created)
	// The historical release is valid at its recorded clock but never served now.
	_, err := publish(ctx, f.opts, created.Add(time.Hour), nil)
	if err == nil {
		t.Fatal("expired history was published")
	}
	_, err = ProvisionRenewal(ctx, ProvisionRenewalOptions{Home: f.opts.Home, Network: f.opts.Network, RenewalHome: filepath.Join(filepath.Dir(f.opts.Home), "online"), Disposable: true})
	must(t, err)
	online := RenewOptions{Home: filepath.Join(filepath.Dir(f.opts.Home), "online"), Network: f.opts.Network, Disposable: true, HTTPClient: f.opts.HTTPClient}
	if _, err := Renew(ctx, online); err == nil {
		t.Fatal("scheduler revived an expired authority")
	}
	opts := RotateOptions{Home: f.opts.Home, Network: f.opts.Network, RootVersion: 2, Disposable: true, HTTPClient: f.opts.HTTPClient}
	applied := applyRoot(t, opts)
	if applied.State != "initialized" || applied.Problem != "" || applied.RootExpires.Before(time.Now().Add(364*24*time.Hour)) {
		t.Fatalf("expiry recovery failed: %+v", applied)
	}
	provisioned, err := ProvisionRenewal(ctx, ProvisionRenewalOptions{Home: f.opts.Home, Network: f.opts.Network, RenewalHome: online.Home, Disposable: true})
	must(t, err)
	if provisioned.State != "initialized" || provisioned.Problem != "" || provisioned.RootVersion != 2 {
		t.Fatal("repeated provisioning reported the expired initial anchor")
	}
	for _, home := range []string{f.opts.Home, online.Home} {
		report, err := VerifyPublication(ctx, home, opts.Network, opts.HTTPClient)
		must(t, err)
		if report.Problem != "" || report.RootVersion != 2 {
			t.Fatal("status still rejected expired initial anchor")
		}
	}
	view, err := fixtureClient(t, fixtureBundle(t, f), opts.HTTPClient).Refresh(ctx)
	must(t, err)
	if view.Versions.Root != 2 {
		t.Fatal("fresh client could not cross expired anchor")
	}
	f.opts.Version = 3
	_, err = Publish(ctx, f.opts)
	must(t, err) // Approval must use the active root expiry, not authority.json's.
	must(t, os.Rename(f.opts.Home, f.opts.Home+"-offline"))
	_, err = renew(ctx, online, time.Now().UTC().Add(7*time.Hour), nil)
	must(t, err)
}

func TestRootRotationSingleSignerLoss(t *testing.T) {
	for _, missing := range keyNames[:3] {
		t.Run(missing, func(t *testing.T) {
			f, online, opts := rotationFixture(t)
			applyRoot(t, opts)
			path := filepath.Join(f.opts.Home, opts.Network+".rotations", rootKeyName(2, missing))
			must(t, os.Remove(path))
			opts.RootVersion = 3
			applyRoot(t, opts)
			assertAbsent(t, path)
			view, err := fixtureClient(t, fixtureBundle(t, f), opts.HTTPClient).Refresh(context.Background())
			must(t, err)
			if view.Versions.Root != 3 {
				t.Fatal("surviving quorum did not authorize replacement")
			}
			for _, name := range keyNames[:2] {
				must(t, os.Remove(filepath.Join(f.opts.Home, opts.Network+".rotations", rootKeyName(3, name))))
			}
			opts.RootVersion, opts.Role = 4, "root"
			if _, err := Rotate(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "quorum") {
				t.Fatalf("lost quorum accepted: %v", err)
			}
			assertAbsent(t, filepath.Join(onlineState(online), rotationIntentName(4)))
		})
	}
}

func TestRootRotationPreparationRecovery(t *testing.T) {
	for _, phase := range []string{"2.root-plan.json:written", "2.root-plan.json:linked", "rotation:root-plan-durable", "2.root-1-key.json:written", "2.root-2-key.json:linked", "rotation:root-keys-durable", "2.rotation-intent.json:written", "rotation:keys-durable", "2.rotation.json:written", "2.rotation.json:linked"} {
		t.Run(phase, func(t *testing.T) {
			f, online, opts := rotationFixture(t)
			opts.Role = "root"
			stop := errors.New("stop")
			_, err := rotate(context.Background(), opts, time.Time{}, func(at string) error {
				if at == phase {
					return stop
				}
				return nil
			})
			if !errors.Is(err, stop) {
				t.Fatalf("missed boundary: %v", err)
			}
			// A different role cannot steal an offline-only root reservation.
			wrong := opts
			wrong.Role = "targets"
			if _, err := Rotate(context.Background(), wrong); err == nil {
				t.Fatal("role stole reserved version")
			}
			preserved := map[string][]byte{}
			keysDir := filepath.Join(f.opts.Home, opts.Network+".rotations")
			must(t, filepath.WalkDir(keysDir, func(path string, entry os.DirEntry, err error) error {
				if err != nil || entry.IsDir() {
					return err
				}
				preserved[strings.TrimSuffix(path, ".pending")] = file(t, path)
				return nil
			}))
			prepared, err := Rotate(context.Background(), opts)
			must(t, err)
			for path, data := range preserved {
				if !bytes.Equal(data, file(t, path)) {
					t.Fatal("retry replaced private generation")
				}
			}
			if prepared.Rotation.State != "prepared" {
				t.Fatal("recovery failed")
			}
			for _, dir := range []string{keysDir, onlineState(online)} {
				pending, err := filepath.Glob(filepath.Join(dir, "*.pending"))
				must(t, err)
				if len(pending) != 0 {
					t.Fatalf("pending twins remain: %v", pending)
				}
			}
			assertAbsent(t, filepath.Join(f.opts.Directory, "2.root.json"))
		})
	}
}

func TestRootRotationApplyRecovery(t *testing.T) {
	for _, phase := range []string{"2.rotation-apply.json:written", "rotation:apply-durable", "2.rotation-targets.json:written", "rotation:targets-durable", "2.release.json:written", "2.release.json:linked", "public:2.targets.json:visible", "public:2.root.json:visible", "public:timestamp.json:visible", "publication:verified"} {
		t.Run(phase, func(t *testing.T) {
			f, online, opts := rotationFixture(t)
			opts.Role = "root"
			prepared, err := Rotate(context.Background(), opts)
			must(t, err)
			opts.Apply, opts.RenewApproval = prepared.Rotation.RootSHA256, true
			stop := errors.New("stop")
			_, err = rotate(context.Background(), opts, time.Time{}, func(at string) error {
				if at == phase {
					return stop
				}
				return nil
			})
			if !errors.Is(err, stop) {
				t.Fatalf("missed boundary: %v", err)
			}
			if phase == "2.rotation-apply.json:written" || phase == "rotation:apply-durable" {
				if _, err := Renew(context.Background(), online); !errors.Is(err, errOfflineHandoff) {
					t.Fatalf("scheduler bypassed missing approval: %v", err)
				}
				_, err = Rotate(context.Background(), opts)
				must(t, err)
			}
			must(t, os.Rename(f.opts.Home, f.opts.Home+"-offline"))
			report, err := Renew(context.Background(), online)
			must(t, err)
			if report.Publication != "verified" || report.RootVersion != 2 || report.Release.Version != 2 {
				t.Fatalf("bad recovered application: %+v", report)
			}
		})
	}
}

func TestRootRotationThresholdsAndCustodyIsolation(t *testing.T) {
	f, online, opts := rotationFixture(t)
	opts.Role = "root"
	_, err := Rotate(context.Background(), opts)
	must(t, err)
	var r rotationRecord
	must(t, decodeRecord(file(t, filepath.Join(onlineState(online), rotationName(2))), &r))
	var intent rotationIntent
	must(t, decodeRecord(file(t, filepath.Join(onlineState(online), rotationIntentName(2))), &intent))
	bundle := fixtureBundle(t, f)
	root, err := metadata.Root().FromBytes(r.Root)
	must(t, err)
	if len(root.Signatures) != 4 {
		t.Fatal("expected two old and two new signatures")
	}
	for _, keep := range [][]int{{0, 1}, {2, 3}, {0, 2, 3}, {0, 1, 2}} {
		copyRoot, err := metadata.Root().FromBytes(r.Root)
		must(t, err)
		copyRoot.Signatures = nil
		for _, i := range keep {
			copyRoot.Signatures = append(copyRoot.Signatures, root.Signatures[i])
		}
		bad := r
		bad.Root, err = copyRoot.ToBytes(false)
		must(t, err)
		if err := bad.validate(bundle, bundle.Root, intent); err == nil {
			t.Fatalf("accepted insufficient quorum %v", keep)
		}
	}
	var privateKeys []string
	for _, name := range keyNames[:3] {
		var c membershipCustody
		must(t, decodeRecord(file(t, filepath.Join(f.opts.Home, opts.Network+".rotations", rootKeyName(2, name))), &c))
		privateKeys = append(privateKeys, c.Key)
	}
	for _, dir := range []string{online.Home, f.opts.Directory} {
		must(t, filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			data := file(t, path)
			for _, key := range privateKeys {
				if bytes.Contains(data, []byte(key)) {
					t.Fatalf("root private key leaked into %s", path)
				}
			}
			return nil
		}))
	}
}

func TestRootRotationExpiredApplyAndOfflineRepair(t *testing.T) {
	f, online, opts := rotationFixture(t)
	opts.Role = "root"
	prepared, err := Rotate(context.Background(), opts)
	must(t, err)
	opts.Apply, opts.RenewApproval = prepared.Rotation.RootSHA256, true
	stop := errors.New("stop")
	_, err = rotate(context.Background(), opts, time.Time{}, func(at string) error {
		if at == "rotation:targets-durable" {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
	reserved := file(t, filepath.Join(onlineState(online), rotationTargetsName(2)))
	before := file(t, filepath.Join(f.opts.Directory, "timestamp.json"))
	must(t, os.Rename(f.opts.Home, f.opts.Home+"-offline"))
	later := time.Now().UTC().Add(25 * time.Hour)
	if _, err := renew(context.Background(), online, later, nil); err == nil {
		t.Fatal("expired reserved release was published")
	}
	if !bytes.Equal(before, file(t, filepath.Join(f.opts.Directory, "timestamp.json"))) {
		t.Fatal("expired recovery changed public timestamp")
	}
	report, err := renew(context.Background(), online, later, nil)
	must(t, err)
	r := recordedRelease(t, online, 3)
	if report.RootVersion != 2 || report.Release.Version != 3 || !bytes.Equal(r.Targets, reserved) || !r.ApprovedAt.Equal(recordedRelease(t, online, 2).Created) {
		t.Fatal("higher renewal changed offline approval")
	}
}

func TestRootRotationRejectsDamagedCustodyAndSignedPolicyChanges(t *testing.T) {
	for _, kind := range []string{"missing-plan", "changed-signer", "missing-quorum", "expiry", "threshold", "targets", "consistent-snapshot"} {
		t.Run(kind, func(t *testing.T) {
			f, online, opts := rotationFixture(t)
			opts.Role = "root"
			_, err := Rotate(context.Background(), opts)
			must(t, err)
			keyDir := filepath.Join(f.opts.Home, opts.Network+".rotations")
			if kind == "missing-plan" || kind == "changed-signer" || kind == "missing-quorum" {
				switch kind {
				case "missing-plan":
					must(t, os.Remove(filepath.Join(keyDir, rootPlanName(2))))
				case "changed-signer":
					must(t, os.WriteFile(filepath.Join(keyDir, rootKeyName(2, "root-1")), file(t, filepath.Join(keyDir, rootKeyName(2, "root-2"))), 0600))
				case "missing-quorum":
					for _, name := range keyNames[:2] {
						must(t, os.Remove(filepath.Join(keyDir, rootKeyName(2, name))))
					}
				}
				if _, err := Rotate(context.Background(), opts); err == nil {
					t.Fatal("damaged custody accepted")
				}
				assertAbsent(t, filepath.Join(f.opts.Directory, "2.root.json"))
				return
			}
			var r rotationRecord
			must(t, decodeRecord(file(t, filepath.Join(onlineState(online), rotationName(2))), &r))
			var intent rotationIntent
			must(t, decodeRecord(file(t, filepath.Join(onlineState(online), rotationIntentName(2))), &intent))
			root, err := metadata.Root().FromBytes(r.Root)
			must(t, err)
			switch kind {
			case "expiry":
				root.Signed.Expires = root.Signed.Expires.Add(time.Hour)
			case "threshold":
				root.Signed.Roles[metadata.ROOT].Threshold = 1
			case "targets":
				root.Signed.Roles[metadata.TARGETS].Threshold = 2
			case "consistent-snapshot":
				root.Signed.ConsistentSnapshot = false
			}
			root.Signatures = nil
			var a authority
			must(t, decodeRecord(file(t, filepath.Join(f.opts.Home, opts.Network, "authority.json")), &a))
			for _, name := range keyNames[:2] {
				_, err := signMetadata(root, a.Keys[name])
				must(t, err)
				var c membershipCustody
				must(t, decodeRecord(file(t, filepath.Join(keyDir, rootKeyName(2, name))), &c))
				_, err = signMetadata(root, c.Key)
				must(t, err)
			}
			r.Root, err = root.ToBytes(false)
			must(t, err)
			bundle := fixtureBundle(t, f)
			if err := r.validate(bundle, bundle.Root, intent); err == nil {
				t.Fatal("both quorums bypassed local policy or reviewed expiry")
			}
		})
	}
}
