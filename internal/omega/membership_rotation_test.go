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

func TestMembershipRotationClientsAndAlternatingGenerations(t *testing.T) {
	f, renewal, opts := rotationFixture(t)
	opts.Role = "targets"
	ctx := context.Background()
	bundle := fixtureBundle(t, f)
	returning := fixtureClient(t, bundle, opts.HTTPClient)
	_, err := returning.Refresh(ctx)
	must(t, err)
	initial := file(t, filepath.Join(opts.Home, opts.Network, "authority.json"))
	prepared, err := Rotate(ctx, opts)
	must(t, err)
	if prepared.Rotation.Role != "targets" || len(prepared.Rotation.Keys) != 1 || len(prepared.Rotation.Keys["targets"]) != 1 {
		t.Fatalf("bad review report: %+v", prepared)
	}
	again, err := Rotate(ctx, opts)
	must(t, err)
	if !bytes.Equal(record(prepared.Rotation), record(again.Rotation)) {
		t.Fatal("preparation changed the reviewed generation")
	}
	assertAbsent(t, filepath.Join(f.opts.Directory, "2.root.json"))
	// An independently approved change during review must be retained, not
	// replaced by the membership that happened to exist at preparation.
	f.opts.Version = 2
	f.changeRoot()
	_, err = Publish(ctx, f.opts)
	must(t, err)
	approval := recordedRelease(t, renewal, 2)
	opts.Apply = prepared.Rotation.RootSHA256
	applied, err := Rotate(ctx, opts)
	must(t, err)
	rotated := recordedRelease(t, renewal, 3)
	if applied.Release.Versions != (bootstrap.Versions{Root: 2, Targets: 3, Snapshot: 3, Timestamp: 3}) ||
		rotated.Schema != 5 || !bytes.Equal(rotated.Manifest, approval.Manifest) || !rotated.ApprovedAt.Equal(approval.Created) {
		t.Fatalf("rotation changed approval or misreported versions: %+v", applied)
	}
	must(t, sameTargetsApproval(approval.Targets, rotated.Targets, 3))
	for _, home := range []string{opts.Home, renewal.Home} {
		report, err := Status(ctx, home, opts.Network)
		must(t, err)
		if report.RootVersion != 2 || report.Rotation.Role != "targets" || report.Rotation.State != "applied" {
			t.Fatalf("status missed membership rotation: %+v", report)
		}
	}
	// Retry cannot create another release or regenerate the initial authority.
	_, err = Rotate(ctx, opts)
	must(t, err)
	assertAbsent(t, filepath.Join(onlineState(renewal), "4.release.json"))
	if !bytes.Equal(initial, file(t, filepath.Join(opts.Home, opts.Network, "authority.json"))) {
		t.Fatal("rotation rewrote the initial authority")
	}
	// The next explicit membership approval selects the new offline key.
	f.opts.Version = 4
	_, err = Publish(ctx, f.opts)
	must(t, err)
	// Online -> targets -> online -> targets must select each role's latest
	// generation independently, even for a client absent throughout.
	for _, role := range []string{"online", "targets"} {
		opts.RootVersion++
		opts.Role, opts.Apply = role, ""
		prepared, err = Rotate(ctx, opts)
		must(t, err)
		opts.Apply = prepared.Rotation.RootSHA256
		applied, err = Rotate(ctx, opts)
		must(t, err)
	}
	for _, client := range []*bootstrap.Client{returning, fixtureClient(t, bundle, opts.HTTPClient)} {
		view, err := client.Refresh(ctx)
		must(t, err)
		if view.Versions != applied.Release.Versions || view.Versions.Root != 4 || view.Versions.Targets != 6 {
			t.Fatalf("client failed retained chain: %+v", view)
		}
	}
	var original authority
	must(t, decodeRecord(initial, &original))
	privateKeys := []string{original.Keys["targets"], original.Keys["root-1"], original.Keys["root-2"], original.Keys["root-3"]}
	for _, version := range []int64{2, 4} {
		var c membershipCustody
		path := filepath.Join(opts.Home, opts.Network+".rotations", membershipKeyName(version))
		must(t, decodeRecord(file(t, path), &c))
		privateKeys = append(privateKeys, c.Key)
		info, err := os.Stat(path)
		must(t, err)
		if info.Mode().Perm() != 0600 {
			t.Fatal("membership private key is not private")
		}
	}
	for _, dir := range []string{renewal.Home, f.opts.Directory} {
		must(t, filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			data := file(t, path)
			for _, private := range privateKeys {
				if bytes.Contains(data, []byte(private)) {
					t.Fatalf("offline key leaked to %s", path)
				}
			}
			return nil
		}))
	}
	// Scheduler neither requires offline custody nor resets the approval clock.
	must(t, os.Rename(opts.Home, opts.Home+"-unmounted"))
	_, err = renew(ctx, renewal, time.Now().UTC().Add(7*time.Hour), nil)
	must(t, err)
	last := recordedRelease(t, renewal, 7)
	if last.versions().Targets != 6 || !last.ApprovedAt.Equal(recordedRelease(t, renewal, 4).Created) {
		t.Fatal("renewal changed the membership generation or approval clock")
	}
}

func TestMembershipRotationPreparationRecovery(t *testing.T) {
	for _, phase := range []string{"2.targets-key.json:written", "2.targets-key.json:linked", "rotation:offline-key-durable", "2.rotation-intent.json:written", "2.rotation.json:linked"} {
		t.Run(phase, func(t *testing.T) {
			_, renewal, opts := rotationFixture(t)
			opts.Role = "targets"
			stop := errors.New("interrupted")
			_, err := rotate(context.Background(), opts, time.Time{}, func(at string) error {
				if at == phase {
					return stop
				}
				return nil
			})
			if !errors.Is(err, stop) {
				t.Fatalf("missed boundary: %v", err)
			}
			dir := filepath.Join(opts.Home, opts.Network+".rotations")
			path := filepath.Join(dir, membershipKeyName(2))
			before, err := os.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				before, err = os.ReadFile(path + ".pending")
			}
			must(t, err)
			wrong := opts
			wrong.Role = "online"
			if _, err := Rotate(context.Background(), wrong); err == nil {
				t.Fatal("online preparation stole a membership key reservation")
			}
			_, err = Rotate(context.Background(), opts)
			must(t, err)
			if !bytes.Equal(before, file(t, path)) {
				t.Fatal("recovery replaced the offline key")
			}
			for _, dir := range []string{dir, onlineState(renewal)} {
				pending, err := filepath.Glob(filepath.Join(dir, "*.pending"))
				must(t, err)
				if len(pending) != 0 {
					t.Fatalf("pending twins survived: %v", pending)
				}
			}
		})
	}
}

func TestMembershipRotationApplyRecovery(t *testing.T) {
	for _, phase := range []string{"2.rotation-apply.json:written", "rotation:apply-durable", "2.rotation-targets.json:written", "rotation:targets-durable", "2.release.json:written", "public:2.root.json:visible"} {
		t.Run(phase, func(t *testing.T) {
			f, renewal, opts := rotationFixture(t)
			opts.Role = "targets"
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
				t.Fatalf("missed boundary: %v", err)
			}
			if phase == "2.rotation-apply.json:written" || phase == "rotation:apply-durable" {
				report, err := Renew(context.Background(), renewal)
				if err == nil || !strings.Contains(report.Problem, "original rotate --role targets --apply") || !strings.Contains(report.Action, "Mount the offline home") {
					t.Fatalf("scheduler must require the unfinished offline handoff: %+v %v", report, err)
				}
				_, err = Rotate(context.Background(), opts)
				must(t, err)
			} else {
				path := filepath.Join(onlineState(renewal), rotationTargetsName(2))
				before, err := os.ReadFile(path)
				if errors.Is(err, os.ErrNotExist) {
					before, err = os.ReadFile(path + ".pending")
				}
				must(t, err)
				must(t, os.Rename(f.opts.Home, f.opts.Home+"-unmounted"))
				report, err := Renew(context.Background(), renewal)
				must(t, err)
				if report.RootVersion != 2 || report.Release.Versions.Targets != 2 || report.Publication != "verified" {
					t.Fatalf("scheduler failed public handoff: %+v", report)
				}
				if !bytes.Equal(before, file(t, filepath.Join(onlineState(renewal), rotationTargetsName(2)))) {
					t.Fatal("scheduler rewrote offline signed targets")
				}
			}
			assertAbsent(t, filepath.Join(onlineState(renewal), "3.release.json"))
		})
	}
}

func TestMembershipRotationRejectsRetiredTargets(t *testing.T) {
	f, renewal, opts := rotationFixture(t)
	opts.Role = "targets"
	ctx := context.Background()
	bundle := fixtureBundle(t, f)
	client := fixtureClient(t, bundle, opts.HTTPClient)
	_, err := client.Refresh(ctx)
	must(t, err)
	prepared, err := Rotate(ctx, opts)
	must(t, err)
	opts.Apply = prepared.Rotation.RootSHA256
	_, err = Rotate(ctx, opts)
	must(t, err)
	_, err = client.Refresh(ctx)
	must(t, err)
	var a authority
	must(t, decodeRecord(file(t, filepath.Join(opts.Home, opts.Network, "authority.json")), &a))
	last := recordedRelease(t, renewal, 2)
	targets, err := metadata.Targets().FromBytes(last.Targets)
	must(t, err)
	targets.Signatures = nil
	forgedTargets, err := signMetadata(targets, a.Keys["targets"])
	must(t, err)
	snapshot, err := metadata.Snapshot().FromBytes(last.Snapshot)
	must(t, err)
	snapshot.Signatures = nil
	snapshot.Signed.Version++
	snapshot.Signed.Meta["targets.json"] = metadataFile(2, forgedTargets)
	signedSnapshot, err := signMetadata(snapshot, a.Keys["snapshot"])
	must(t, err)
	timestamp, err := metadata.Timestamp().FromBytes(last.Timestamp)
	must(t, err)
	timestamp.Signatures = nil
	timestamp.Signed.Version++
	timestamp.Signed.Meta["snapshot.json"] = metadataFile(3, signedSnapshot)
	signedTimestamp, err := signMetadata(timestamp, a.Keys["timestamp"])
	must(t, err)
	f.override("/timestamp.json", signedTimestamp, 0)
	f.override("/3.snapshot.json", signedSnapshot, 0)
	f.override("/2.targets.json", forgedTargets, 0)
	for _, c := range []*bootstrap.Client{client, fixtureClient(t, bundle, opts.HTTPClient)} {
		if _, err := c.Refresh(ctx); err == nil {
			t.Fatal("client accepted the retired membership key with valid online signatures")
		}
	}
}

func TestMembershipRotationPreservesDamagedState(t *testing.T) {
	for _, kind := range []string{"missing-key", "corrupt-key", "changed-pending-handoff", "wrong-role", "bad-approval", "missing-reservation"} {
		t.Run(kind, func(t *testing.T) {
			f, renewal, opts := rotationFixture(t)
			opts.Role = "targets"
			prepared, err := Rotate(context.Background(), opts)
			must(t, err)
			path := filepath.Join(opts.Home, opts.Network+".rotations", membershipKeyName(2))
			switch kind {
			case "missing-key":
				must(t, os.Remove(path))
			case "corrupt-key":
				must(t, os.WriteFile(path, []byte(`{"schema":`), 0600))
			case "changed-pending-handoff":
				path = filepath.Join(onlineState(renewal), rotationIntentName(2))
				var intent rotationIntent
				must(t, decodeRecord(file(t, path), &intent))
				intent.Created = intent.Created.Add(-time.Hour)
				must(t, os.Remove(path))
				path += ".pending"
				must(t, os.WriteFile(path, record(intent), 0600))
			case "wrong-role":
				opts.Role = "online"
			default:
				opts.Apply = prepared.Rotation.RootSHA256
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
				path = filepath.Join(onlineState(renewal), rotationTargetsName(2))
				if kind == "missing-reservation" {
					must(t, os.Remove(filepath.Join(onlineState(renewal), rotationApplyName(2))))
				} else {
					targets, err := metadata.Targets().FromBytes(file(t, path))
					must(t, err)
					targets.Signed.Expires = targets.Signed.Expires.Add(time.Hour)
					var custody membershipCustody
					must(t, decodeRecord(file(t, filepath.Join(opts.Home, opts.Network+".rotations", membershipKeyName(2))), &custody))
					targets.Signatures = nil
					raw, err := signMetadata(targets, custody.Key)
					must(t, err)
					must(t, os.WriteFile(path, raw, 0600))
				}
				if _, err := Renew(context.Background(), renewal); err == nil {
					t.Fatal("scheduler accepted damaged or reauthorized handoff")
				}
			}
			before, readErr := os.ReadFile(path)
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				t.Fatal(readErr)
			}
			if _, err := Rotate(context.Background(), opts); err == nil {
				t.Fatal("damaged or conflicting generation was accepted")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("recovery overwrote damaged state")
			}
			assertAbsent(t, filepath.Join(f.opts.Directory, "2.root.json"))
			assertAbsent(t, filepath.Join(onlineState(renewal), "2.release.json"))
		})
	}
}

func TestMembershipRotationTornHandoffAndExpiredRecovery(t *testing.T) {
	f, renewal, opts := rotationFixture(t)
	opts.Role = "targets"
	prepared, err := Rotate(context.Background(), opts)
	must(t, err)
	opts.Apply = prepared.Rotation.RootSHA256
	stop := errors.New("stop")
	_, err = rotate(context.Background(), opts, time.Time{}, func(at string) error {
		if at == "2.rotation-targets.json:written" {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
	path := filepath.Join(onlineState(renewal), rotationTargetsName(2)+".pending")
	expected := file(t, path)
	must(t, os.WriteFile(path, []byte(`{"signed":`), 0600))
	if _, err := Renew(context.Background(), renewal); err == nil {
		t.Fatal("scheduler reconstructed an offline signature")
	}
	// After a day the recorded release must finish privately, then a higher
	// online release repairs freshness. It cannot rewrite the reserved clock.
	later := time.Now().UTC().Add(25 * time.Hour)
	before := file(t, filepath.Join(f.opts.Directory, "timestamp.json"))
	if _, err := rotate(context.Background(), opts, later, nil); err == nil {
		t.Fatal("expired apply reported successful publication")
	}
	if !bytes.Equal(expected, file(t, strings.TrimSuffix(path, ".pending"))) || !bytes.Equal(before, file(t, filepath.Join(f.opts.Directory, "timestamp.json"))) {
		t.Fatal("recovery changed the signed handoff or served an expired timestamp")
	}
	must(t, os.Rename(opts.Home, opts.Home+"-unmounted"))
	report, err := renew(context.Background(), renewal, later, nil)
	must(t, err)
	if report.RootVersion != 2 || report.Release.Versions.Targets != 2 || report.Release.Version != 3 {
		t.Fatalf("wrong expiry repair: %+v", report)
	}
}

func TestMembershipRotationLostGenerationRecovery(t *testing.T) {
	f, renewal, opts := rotationFixture(t)
	opts.Role = "targets"
	prepared, err := Rotate(context.Background(), opts)
	must(t, err)
	opts.Apply = prepared.Rotation.RootSHA256
	_, err = Rotate(context.Background(), opts)
	must(t, err)
	path := filepath.Join(opts.Home, opts.Network+".rotations", membershipKeyName(2))
	must(t, os.Remove(path))
	f.opts.Version = 3
	if _, err := Publish(context.Background(), f.opts); err == nil {
		t.Fatal("approval fell back to the retired membership key")
	}
	// With a still-valid approval and intact initial authority/journal, the
	// root quorum can authorize another generation without the lost key.
	opts.RootVersion, opts.Apply = 3, ""
	prepared, err = Rotate(context.Background(), opts)
	must(t, err)
	opts.Apply = prepared.Rotation.RootSHA256
	_, err = Rotate(context.Background(), opts)
	must(t, err)
	f.opts.Version = 4
	_, err = Publish(context.Background(), f.opts)
	must(t, err)
	last := recordedRelease(t, renewal, 4)
	if last.versions().Root != 3 {
		t.Fatal("recovery replaced the network authority")
	}
	assertAbsent(t, path)
}
