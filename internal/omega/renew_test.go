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
	"sync"
	"testing"
	"time"

	"sopholeth/internal/trust/bootstrap"
)

func renewalFixture(t *testing.T, age time.Duration) (*publishFixture, ProvisionRenewalOptions, RenewOptions) {
	t.Helper()
	f := newPublishFixture(t)
	_, err := publish(context.Background(), f.opts, time.Now().UTC().Truncate(time.Second).Add(-age), nil)
	if age < 24*time.Hour {
		must(t, err)
	} else if err == nil {
		t.Fatal("expired publication succeeded")
	}
	p := ProvisionRenewalOptions{Home: f.opts.Home, Network: f.opts.Network, RenewalHome: filepath.Join(filepath.Dir(f.opts.Home), "online"), Disposable: true}
	_, err = ProvisionRenewal(context.Background(), p)
	must(t, err)
	return f, p, RenewOptions{Home: p.RenewalHome, Network: p.Network, Disposable: true, HTTPClient: f.opts.HTTPClient}
}
func onlineState(o RenewOptions) string { return filepath.Join(o.Home, o.Network+".publication") }

func TestRenewalWithoutOfflineKeysAndNewMembership(t *testing.T) {
	f, p, opts := renewalFixture(t, 7*time.Hour)
	var authorityRecord authority
	must(t, decodeRecord(file(t, filepath.Join(f.opts.Home, f.opts.Network, "authority.json")), &authorityRecord))
	original := file(t, filepath.Join(f.statePath(), "1.release.json"))
	var first release
	must(t, decodeRecord(original, &first))
	bundle, err := bootstrap.ParseBundle(file(t, filepath.Join(f.opts.Home, f.opts.Network, "bundle.json")))
	must(t, err)
	client, err := bootstrap.New(context.Background(), bootstrap.Config{Bundle: bundle, StateDir: filepath.Join(t.TempDir(), "client"), HTTPClient: opts.HTTPClient})
	must(t, err)
	_, err = client.Refresh(context.Background())
	must(t, err)
	offline := f.opts.Home + "-unmounted"
	must(t, os.Rename(f.opts.Home, offline))
	report, err := Renew(context.Background(), opts)
	must(t, err)
	if report.Publication != "verified" || report.Release.Versions != (bootstrap.Versions{Root: 1, Targets: 1, Snapshot: 2, Timestamp: 2}) || report.Release.RenewalDue {
		t.Fatalf("bad renewal report: %+v", report)
	}
	var second release
	must(t, decodeRecord(file(t, filepath.Join(onlineState(opts), "2.release.json")), &second))
	if !bytes.Equal(first.Targets, second.Targets) || !bytes.Equal(first.Manifest, second.Manifest) {
		t.Fatal("renewal changed offline approval")
	}
	assertAbsent(t, filepath.Join(f.opts.Directory, "2.targets.json"))
	view, err := client.Refresh(context.Background())
	must(t, err)
	if view.Versions != report.Release.Versions {
		t.Fatal("returning client did not adopt renewal")
	}
	f.override("/timestamp.json", first.Timestamp, 0)
	if _, err := client.Refresh(context.Background()); err == nil {
		t.Fatal("returning client accepted rollback")
	}
	f.override("/timestamp.json", nil, 0)
	_, err = Renew(context.Background(), opts)
	must(t, err)
	assertAbsent(t, filepath.Join(onlineState(opts), "3.release.json"))
	local, err := Status(context.Background(), opts.Home, opts.Network)
	must(t, err)
	if local.State != "renewal_ready" || local.Release.Version != 2 || local.OperationalHome != opts.Home {
		t.Fatal("online status failed")
	}
	_, err = VerifyPublication(context.Background(), opts.Home, opts.Network, opts.HTTPClient)
	must(t, err)
	must(t, filepath.WalkDir(opts.Home, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data := file(t, path)
		for _, key := range []string{"root-1", "root-2", "root-3", "targets"} {
			if bytes.Contains(data, []byte(authorityRecord.Keys[key])) {
				t.Fatalf("offline key leaked into %s", path)
			}
		}
		return nil
	}))
	must(t, os.Rename(offline, f.opts.Home))
	// Reprovisioning cannot rewind the journal after the online service ran.
	_, err = ProvisionRenewal(context.Background(), p)
	must(t, err)
	f.opts.Version = 3
	f.changeRoot()
	_, err = Publish(context.Background(), f.opts)
	must(t, err)
	assertAbsent(t, filepath.Join(f.statePath(), "3.release.json"))
	if !bytes.Equal(original, file(t, filepath.Join(f.statePath(), "1.release.json"))) {
		t.Fatal("handoff modified archived history")
	}
	local, err = Status(context.Background(), f.opts.Home, f.opts.Network)
	must(t, err)
	if local.Release.Version != 3 {
		t.Fatal("offline status did not follow operational binding")
	}
	_, err = renew(context.Background(), opts, time.Now().UTC().Add(7*time.Hour), nil)
	must(t, err)
	var fourth release
	must(t, decodeRecord(file(t, filepath.Join(onlineState(opts), "4.release.json")), &fourth))
	if fourth.TargetsVersion != 3 || bytes.Equal(fourth.Manifest, first.Manifest) {
		t.Fatal("renewal did not retain the newer approval")
	}
}

func TestRenewalInterruptionsAndTornWrites(t *testing.T) {
	for _, phase := range []string{"2.renewal-intent.json:written", "2.renewal-intent.json:linked", "renewal:intent-durable", "2.release.json:written", "2.release.json:linked", "release:durable", "public:2.snapshot.json:written", "public:2.snapshot.json:visible", "public:timestamp.json:visible", "publication:verified"} {
		t.Run(phase, func(t *testing.T) {
			_, _, opts := renewalFixture(t, 7*time.Hour)
			stop := errors.New("interrupted")
			_, err := renew(context.Background(), opts, time.Now().UTC(), func(at string) error {
				if at == phase {
					return stop
				}
				return nil
			})
			if !errors.Is(err, stop) {
				t.Fatalf("missed phase: %v", err)
			}
			before, err := os.ReadFile(filepath.Join(onlineState(opts), "2.release.json"))
			if errors.Is(err, os.ErrNotExist) {
				before, _ = os.ReadFile(filepath.Join(onlineState(opts), "2.release.json.pending"))
			}
			if phase == "2.release.json:written" {
				must(t, os.WriteFile(filepath.Join(onlineState(opts), "2.release.json.pending"), []byte(`{"schema":`), 0600))
			}
			if phase == "2.renewal-intent.json:written" {
				must(t, os.WriteFile(filepath.Join(onlineState(opts), "2.renewal-intent.json.pending"), []byte(`{"schema":`), 0600))
			}
			_, err = Renew(context.Background(), opts)
			must(t, err)
			if len(before) > 0 && !bytes.Equal(before, file(t, filepath.Join(onlineState(opts), "2.release.json"))) {
				t.Fatal("retry changed prepared renewal")
			}
			assertAbsent(t, filepath.Join(onlineState(opts), "3.release.json"))
			pending, err := filepath.Glob(filepath.Join(onlineState(opts), "*.pending"))
			must(t, err)
			if len(pending) != 0 {
				t.Fatalf("left pending twins: %v", pending)
			}
		})
	}
}

func TestProvisionRenewalRecoversHandoff(t *testing.T) {
	for _, phase := range []string{"rehearsal.publisher.json:written", "rehearsal.publisher.json:linked", "renewal:bound", "1.release.json:linked", "renewal:journal-durable", "rehearsal.renewal.json:written", "rehearsal.renewal.json:linked"} {
		t.Run(phase, func(t *testing.T) {
			f := newPublishFixture(t)
			_, err := Publish(context.Background(), f.opts)
			must(t, err)
			p := ProvisionRenewalOptions{Home: f.opts.Home, Network: f.opts.Network, RenewalHome: filepath.Join(filepath.Dir(f.opts.Home), "online"), Disposable: true}
			stop := errors.New("interrupted")
			_, err = provisionRenewal(context.Background(), p, func(at string) error {
				if at == phase {
					return stop
				}
				return nil
			})
			if !errors.Is(err, stop) {
				t.Fatalf("missed handoff phase: %v", err)
			}
			if phase != "rehearsal.renewal.json:linked" {
				if _, err := Publish(context.Background(), f.opts); err == nil {
					t.Fatal("old journal remained usable during handoff")
				}
			}
			_, err = ProvisionRenewal(context.Background(), p)
			must(t, err)
			_, err = Renew(context.Background(), RenewOptions{Home: p.RenewalHome, Network: p.Network, Disposable: true, HTTPClient: f.opts.HTTPClient})
			must(t, err)
			if !bytes.Equal(file(t, filepath.Join(f.statePath(), "1.release.json")), file(t, filepath.Join(p.RenewalHome, p.Network+".publication", "1.release.json"))) {
				t.Fatal("handoff changed prepared release")
			}
		})
	}
}

func TestRenewalExpiryAndPublicationFailure(t *testing.T) {
	for _, kind := range []string{"expired-timestamp", "approval-deadline", "publisher-outage"} {
		t.Run(kind, func(t *testing.T) {
			age := 7 * time.Hour
			if kind == "expired-timestamp" {
				age = 26 * time.Hour
			}
			if kind == "approval-deadline" {
				age = 90*24*time.Hour - time.Hour
			}
			f, _, opts := renewalFixture(t, age)
			must(t, os.Rename(f.opts.Home, f.opts.Home+"-offline"))
			if kind == "publisher-outage" {
				f.override("/timestamp.json", nil, http.StatusServiceUnavailable)
				if _, err := Renew(context.Background(), opts); err == nil {
					t.Fatal("outage reported success")
				}
				if _, err := Renew(context.Background(), opts); err == nil {
					t.Fatal("retry ignored outage")
				}
				assertAbsent(t, filepath.Join(onlineState(opts), "3.release.json"))
				local, err := Status(context.Background(), opts.Home, opts.Network)
				if err == nil || local.Release.LastError == "" {
					t.Fatal("outage was not observable")
				}
				f.override("/timestamp.json", nil, 0)
			}
			report, err := Renew(context.Background(), opts)
			must(t, err)
			if report.Release.Version != 2 {
				t.Fatal("unexpected repair version")
			}
			if kind == "approval-deadline" {
				if !report.Release.Expires["timestamp"].Equal(report.Release.Expires["targets"]) || len(report.Release.Warnings) == 0 {
					t.Fatal("renewal exceeded offline deadline or omitted warning")
				}
				if _, err := renew(context.Background(), opts, time.Now().UTC().Add(2*time.Hour), nil); err == nil {
					t.Fatal("renewed expired offline approval")
				}
				assertAbsent(t, filepath.Join(onlineState(opts), "3.release.json"))
			}
		})
	}
}

func TestRenewalConcurrentAndClockRollback(t *testing.T) {
	_, _, opts := renewalFixture(t, 7*time.Hour)
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for range 3 {
		wg.Go(func() { _, err := Renew(context.Background(), opts); errs <- err })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		must(t, err)
	}
	assertAbsent(t, filepath.Join(onlineState(opts), "3.release.json"))
	if _, err := renew(context.Background(), opts, time.Now().UTC().Add(-time.Hour), nil); err == nil {
		t.Fatal("accepted backward clock")
	}
	assertAbsent(t, filepath.Join(onlineState(opts), "3.renewal-intent.json"))
}

func TestRenewalProcessDeathRecovery(t *testing.T) {
	if os.Getenv("SOPH_RENEW_TEST_CHILD") == "1" {
		home := os.Getenv("SOPH_RENEW_TEST_HOME")
		_, err := renew(context.Background(), RenewOptions{Home: home, Network: "rehearsal", Disposable: true}, time.Time{}, func(at string) error {
			if at == os.Getenv("SOPH_RENEW_TEST_PHASE") {
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
	for _, phase := range []string{"renewal:intent-durable", "public:timestamp.json:visible"} {
		t.Run(phase, func(t *testing.T) {
			f, _, opts := renewalFixture(t, 7*time.Hour)
			must(t, os.Rename(f.opts.Home, f.opts.Home+"-offline"))
			cmd := exec.Command(os.Args[0], "-test.run=^TestRenewalProcessDeathRecovery$")
			cmd.Env = append(os.Environ(), "SOPH_RENEW_TEST_CHILD=1", "SOPH_RENEW_TEST_HOME="+opts.Home, "SOPH_RENEW_TEST_PHASE="+phase)
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
			before, _ := os.ReadFile(filepath.Join(onlineState(opts), "2.release.json"))
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			r, err := Renew(ctx, opts)
			must(t, err)
			if r.Release.Version != 2 {
				t.Fatal("recovery advanced beyond interrupted renewal")
			}
			if len(before) > 0 && !bytes.Equal(before, file(t, filepath.Join(onlineState(opts), "2.release.json"))) {
				t.Fatal("recovery changed signed bytes")
			}
		})
	}
}

func TestRenewalRefusesChangedCustodyAndPendingApproval(t *testing.T) {
	for _, kind := range []string{"offline-key", "wrong-role-key", "different-home", "pending-approval", "pending-torn-approval", "tampered-pending-renewal"} {
		t.Run(kind, func(t *testing.T) {
			f, p, opts := renewalFixture(t, 7*time.Hour)
			keyPath := filepath.Join(opts.Home, opts.Network+".renewal.json")
			switch kind {
			case "offline-key", "wrong-role-key":
				var c renewalCustody
				must(t, decodeRecord(file(t, keyPath), &c))
				if kind == "offline-key" {
					c.Keys["targets"] = c.Keys["snapshot"]
				} else {
					c.Keys["snapshot"] = c.Keys["timestamp"]
				}
				must(t, os.WriteFile(keyPath, record(c), 0600))
			case "different-home":
				p.RenewalHome = filepath.Join(filepath.Dir(p.RenewalHome), "other")
				if _, err := ProvisionRenewal(context.Background(), p); err == nil {
					t.Fatal("changed bound operational home")
				}
				assertAbsent(t, p.RenewalHome)
				return
			case "pending-approval", "pending-torn-approval":
				f.opts.Version = 2
				f.changeRoot()
				_, err := publish(context.Background(), f.opts, time.Now().UTC().Truncate(time.Second), func(at string) error {
					if at == "2.release.json:written" {
						return errors.New("stop")
					}
					return nil
				})
				if err == nil {
					t.Fatal("expected interrupted approval")
				}
				if kind == "pending-torn-approval" {
					must(t, os.WriteFile(filepath.Join(onlineState(opts), "2.release.json.pending"), []byte(`{"schema":`), 0600))
				}
			case "tampered-pending-renewal":
				_, err := renew(context.Background(), opts, time.Time{}, func(at string) error {
					if at == "2.release.json:written" {
						return errors.New("stop")
					}
					return nil
				})
				if err == nil {
					t.Fatal("expected interruption")
				}
				path := filepath.Join(onlineState(opts), "2.release.json.pending")
				var r release
				must(t, decodeRecord(file(t, path), &r))
				r.Fingerprint = "wrong"
				must(t, os.WriteFile(path, record(r), 0600))
			}
			if _, err := Renew(context.Background(), opts); err == nil {
				t.Fatal("renewal accepted conflicting or unsafe input")
			}
			assertAbsent(t, filepath.Join(onlineState(opts), "2.release.json"))
		})
	}
}

func TestRenewalHomeCannotInitializeReplacementAuthority(t *testing.T) {
	for _, missingKey := range []bool{false, true} {
		f, _, opts := renewalFixture(t, 7*time.Hour)
		if missingKey {
			must(t, os.Remove(filepath.Join(opts.Home, opts.Network+".renewal.json")))
			r, err := Status(context.Background(), opts.Home, opts.Network)
			if err == nil || r.State != "invalid" {
				t.Fatal("lost online custody was reported as a new authority slot")
			}
		}
		_, err := Init(context.Background(), InitOptions{Home: opts.Home, Network: opts.Network, Repository: f.server.URL, Disposable: true})
		if err == nil {
			t.Fatal("initialized replacement authority in operational home")
		}
		assertAbsent(t, filepath.Join(opts.Home, opts.Network))
	}
}

func TestProvisionPreservesConflictingPendingCustody(t *testing.T) {
	f := newPublishFixture(t)
	p := ProvisionRenewalOptions{Home: f.opts.Home, Network: f.opts.Network, RenewalHome: filepath.Join(filepath.Dir(f.opts.Home), "online"), Disposable: true}
	_, err := provisionRenewal(context.Background(), p, func(at string) error {
		if at == "rehearsal.renewal.json:written" {
			return errors.New("stop")
		}
		return nil
	})
	if err == nil {
		t.Fatal("expected interruption")
	}
	path := filepath.Join(p.RenewalHome, "rehearsal.renewal.json.pending")
	var c renewalCustody
	must(t, decodeRecord(file(t, path), &c))
	c.Bundle.Network = "other"
	before := record(c)
	must(t, os.WriteFile(path, before, 0600))
	if _, err := ProvisionRenewal(context.Background(), p); err == nil {
		t.Fatal("replaced conflicting complete pending custody")
	}
	if !bytes.Equal(before, file(t, path)) {
		t.Fatal("changed conflicting custody")
	}
	assertAbsent(t, filepath.Join(p.RenewalHome, "rehearsal.renewal.json"))
}

func TestExpiredMembershipDoesNotStrandRenewalReservation(t *testing.T) {
	for _, kind := range []string{"complete-intent", "torn-intent", "expired-timestamp"} {
		t.Run(kind, func(t *testing.T) {
			age, advance := 90*24*time.Hour-time.Hour, 2*time.Hour
			if kind == "expired-timestamp" {
				age, advance = 7*time.Hour, 25*time.Hour
			}
			f, _, opts := renewalFixture(t, age)
			phase := "renewal:intent-durable"
			if kind == "torn-intent" {
				phase = "2.renewal-intent.json:written"
			}
			_, err := renew(context.Background(), opts, time.Time{}, func(at string) error {
				if at == phase {
					return errors.New("stop")
				}
				return nil
			})
			if err == nil {
				t.Fatal("expected interruption")
			}
			if kind == "torn-intent" {
				must(t, os.WriteFile(filepath.Join(onlineState(opts), "2.renewal-intent.json.pending"), []byte(`{"schema":`), 0600))
			}
			now := time.Now().UTC().Add(advance)
			before, _ := os.ReadFile(filepath.Join(f.opts.Directory, "timestamp.json"))
			if _, err := renew(context.Background(), opts, now, nil); err == nil {
				t.Fatal("expired reservation reported success")
			}
			after, _ := os.ReadFile(filepath.Join(f.opts.Directory, "timestamp.json"))
			if !bytes.Equal(before, after) {
				t.Fatal("recovery published expired metadata")
			}
			f.opts.Version = 3
			if kind == "torn-intent" {
				f.opts.Version = 2
				assertAbsent(t, filepath.Join(onlineState(opts), "2.renewal-intent.json.pending"))
				assertAbsent(t, filepath.Join(onlineState(opts), "2.release.json"))
			} else {
				_ = file(t, filepath.Join(onlineState(opts), "2.release.json"))
			}
			if kind == "expired-timestamp" {
				r, err := renew(context.Background(), opts, now.Add(time.Second), nil)
				must(t, err)
				if r.Release.Version != 3 {
					t.Fatal("renewal did not publish higher repair")
				}
			} else {
				// A new offline approval is no longer blocked by the old intent.
				f.changeRoot()
				_, err := Publish(context.Background(), f.opts)
				must(t, err)
			}
		})
	}
}
