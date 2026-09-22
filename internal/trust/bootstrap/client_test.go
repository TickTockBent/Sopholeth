package bootstrap

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

func (r *repository) bundle() Bundle {
	return Bundle{Schema: 1, Network: "disposable", Repository: r.server.URL, Root: r.initialRoot}
}

func (r *repository) client(dir string) *Client {
	r.t.Helper()
	c, err := New(context.Background(), Config{Bundle: r.bundle(), StateDir: dir, HTTPClient: r.server.Client()})
	check(r.t, err)
	return c
}

func (r *repository) accept(c *Client) View {
	r.t.Helper()
	view, err := c.Refresh(context.Background())
	check(r.t, err)
	if view.Manifest.Network != "disposable" || len(view.Manifest.Roots) != 3 {
		r.t.Fatalf("unexpected accepted manifest: %+v", view)
	}
	return view
}

func TestFreshRestartRenewalAndOfflineExpiry(t *testing.T) {
	r := newRepository(t)
	dir := filepath.Join(t.TempDir(), "state")
	c := r.client(dir)
	first := r.accept(c)
	if first.Versions != (Versions{1, 1, 1, 1}) {
		t.Fatalf("unexpected versions: %+v", first.Versions)
	}
	c = r.client(dir)
	r.accept(c) // An identical timestamp is idempotent.
	delete(r.roleKeys, metadata.ROOT)
	delete(r.roleKeys, metadata.TARGETS)
	r.renew(epoch.Add(time.Hour))
	latest := r.accept(c)
	if latest.Versions.Timestamp != 2 || !latest.Expires.After(first.Expires) {
		t.Fatal("online renewal did not extend the accepted lease")
	}
	r.server.Close()
	c = r.client(dir)
	_, err := c.Current(context.Background())
	check(t, err) // Complete cached state can be verified without any fetch.
	c.now = func() time.Time { return latest.Expires }
	_, err = c.Current(context.Background())
	var expired *metadata.ErrExpiredMetadata
	if !errors.As(err, &expired) {
		t.Fatalf("authority must expire at the deadline while offline: %v", err)
	}
}

func TestPartialProgressPreventsRollbackAfterRestart(t *testing.T) {
	r := newRepository(t)
	dir := filepath.Join(t.TempDir(), "state")
	c := r.client(dir)
	r.accept(c)
	oldTimestamp := r.get("/timestamp.json")
	r.renew(epoch.Add(time.Hour))
	r.put("/2.snapshot.json", nil)
	if _, err := c.Refresh(context.Background()); err == nil {
		t.Fatal("accepted an incomplete publication")
	}
	// Unchanged root/membership permits the old lease until its own expiry.
	view, err := c.Current(context.Background())
	check(t, err)
	if view.Versions.Timestamp != 1 {
		t.Fatal("incomplete publication replaced the active manifest")
	}
	r.put("/timestamp.json", oldTimestamp)
	c = r.client(dir)
	_, err = c.Refresh(context.Background())
	var versionErr *metadata.ErrBadVersionNumber
	if !errors.As(err, &versionErr) {
		t.Fatalf("partial timestamp progress did not survive restart: %v", err)
	}
	r.renew(epoch.Add(2 * time.Hour))
	r.accept(c)
}

func TestRootRotationRevokesLeaseEvenWhenRefreshFails(t *testing.T) {
	r := newRepository(t)
	dir := filepath.Join(t.TempDir(), "state")
	c := r.client(dir)
	r.accept(c)
	oldTimestamp := r.roleKeys[metadata.TIMESTAMP]
	old, next := r.rotate()
	r.put("/2.root.json", signed(t, r.root, append(old[:2:2], next...)...))
	// New root retires every old role key; no successor timestamp is available.
	if _, err := c.Refresh(context.Background()); err == nil {
		t.Fatal("old timestamp unexpectedly verified against successor root")
	}
	c = r.client(dir)
	if _, err := c.Current(context.Background()); !errors.Is(err, ErrNoManifest) {
		t.Fatalf("retired authority's runtime lease remained usable: %v", err)
	}
	r.approve(manifest, epoch.Add(90*24*time.Hour))
	r.renew(epoch)
	view := r.accept(c)
	if view.Versions.Root != 2 {
		t.Fatal("successor root was not persisted")
	}
	r.put("/2.root.json", nil) // Restart must use root 2, not the bundled root.
	r.accept(r.client(dir))
	r.roleKeys[metadata.TIMESTAMP] = oldTimestamp
	r.renew(epoch.Add(time.Hour))
	_, err := c.Refresh(context.Background())
	var unsigned *metadata.ErrUnsignedMetadata
	if !errors.As(err, &unsigned) {
		t.Fatalf("retired online key was not rejected: %v", err)
	}
}

func TestAuthorityTransitionThresholdsAndReturningClient(t *testing.T) {
	for _, mode := range []string{"old-only", "new-only", "one-old", "one-new", "both"} {
		t.Run(mode, func(t *testing.T) {
			r := newRepository(t)
			old, next := r.rotate()
			var signing []ed25519.PrivateKey
			switch mode {
			case "old-only":
				signing = old
			case "new-only":
				signing = next
			case "one-old":
				signing = append(old[:1:1], next...)
			case "one-new":
				signing = append(old[:2:2], next[:1]...)
			case "both":
				signing = append(old[:2:2], next...)
			}
			r.put("/2.root.json", signed(t, r.root, signing...))
			r.approve(manifest, epoch.Add(90*24*time.Hour))
			r.renew(epoch)
			c := r.client(filepath.Join(t.TempDir(), "state"))
			_, err := c.Refresh(context.Background())
			if mode == "both" {
				check(t, err)
			} else {
				var unsigned *metadata.ErrUnsignedMetadata
				if !errors.As(err, &unsigned) {
					t.Fatalf("wanted signature threshold rejection: %v", err)
				}
			}
		})
	}
	t.Run("expired starting and intermediate roots", func(t *testing.T) {
		r := newRepository(t)
		r.root.Signed.Expires = epoch.Add(-time.Hour)
		r.initialRoot = signed(t, r.root, r.roleKeys[metadata.ROOT][:2]...)
		for version := 2; version <= 3; version++ {
			old, next := r.rotate()
			if version == 2 {
				r.root.Signed.Expires = epoch.Add(-time.Hour)
			}
			r.put(fmt.Sprintf("/%d.root.json", version), signed(t, r.root, append(old[:2:2], next...)...))
		}
		r.approve(manifest, epoch.Add(90*24*time.Hour))
		r.renew(epoch)
		if view := r.accept(r.client(filepath.Join(t.TempDir(), "state"))); view.Versions.Root != 3 {
			t.Fatal("returning client did not recover through retained history")
		}
		r.put("/2.root.json", nil)
		_, err := r.client(filepath.Join(t.TempDir(), "stranded")).Refresh(context.Background())
		var expired *metadata.ErrExpiredMetadata
		if !errors.As(err, &expired) {
			t.Fatalf("missing transition did not strand old authority: %v", err)
		}
	})
}

func TestSnapshotRollbackInsideNewTimestamp(t *testing.T) {
	r := newRepository(t)
	c := r.client(filepath.Join(t.TempDir(), "state"))
	old, err := metadata.Timestamp().FromBytes(r.get("/timestamp.json"))
	check(t, err)
	r.renew(epoch)
	r.accept(c)
	r.renew(epoch)
	latest, err := metadata.Timestamp().FromBytes(r.get("/timestamp.json"))
	check(t, err)
	latest.Signed.Meta["snapshot.json"] = old.Signed.Meta["snapshot.json"]
	r.put("/timestamp.json", signed(t, latest, r.roleKeys[metadata.TIMESTAMP]...))
	_, err = c.Refresh(context.Background())
	var versionErr *metadata.ErrBadVersionNumber
	if !errors.As(err, &versionErr) {
		t.Fatalf("signed snapshot rollback accepted: %v", err)
	}
}

func TestMembershipChangeRevokesLeaseBeforeTargetAcceptance(t *testing.T) {
	r := newRepository(t)
	c := r.client(filepath.Join(t.TempDir(), "state"))
	r.accept(c)
	r.approve([]byte(`{"schema":99}`), epoch.Add(time.Hour))
	r.renew(epoch)
	if _, err := c.Refresh(context.Background()); err == nil {
		t.Fatal("invalid signed manifest accepted")
	}
	if _, err := c.Current(context.Background()); !errors.Is(err, ErrNoManifest) {
		t.Fatalf("old membership remained active after new approval: %v", err)
	}
}

func TestTargetTamperingAndOnlineRollback(t *testing.T) {
	r := newRepository(t)
	c := r.client(filepath.Join(t.TempDir(), "state"))
	oldTargets := bytes.Clone(r.targets)
	r.approve(manifest, epoch.Add(90*24*time.Hour))
	r.renew(epoch)
	r.accept(c)
	r.targets, r.targetVersion = oldTargets, 1
	r.renew(epoch)
	_, err := c.Refresh(context.Background())
	var versionErr *metadata.ErrBadVersionNumber
	if !errors.As(err, &versionErr) {
		t.Fatalf("online signers rolled back approved membership: %v", err)
	}
	r.approve(manifest, epoch.Add(90*24*time.Hour))
	r.renew(epoch)
	tampered := bytes.Clone(manifest)
	tampered[0] ^= 1
	r.put(r.targetPath, tampered)
	_, err = c.Refresh(context.Background())
	var hashErr *metadata.ErrLengthOrHashMismatch
	if !errors.As(err, &hashErr) {
		t.Fatalf("wanted target hash rejection: %v", err)
	}
}

func TestEveryRoleExpires(t *testing.T) {
	for _, role := range metadata.TOP_LEVEL_ROLE_NAMES {
		t.Run(role, func(t *testing.T) {
			r := newRepository(t)
			switch role {
			case metadata.ROOT:
				r.root.Signed.Expires = epoch.Add(-time.Second)
				r.initialRoot = signed(t, r.root, r.roleKeys[metadata.ROOT][:2]...)
			case metadata.TARGETS:
				r.approve(manifest, epoch.Add(-time.Second))
				r.renew(epoch)
			case metadata.TIMESTAMP:
				r.renew(epoch.Add(-25 * time.Hour))
			case metadata.SNAPSHOT:
				r.renew(epoch.Add(-8 * 24 * time.Hour))
				stamp, err := metadata.Timestamp().FromBytes(r.get("/timestamp.json"))
				check(t, err)
				stamp.Signed.Expires = epoch.Add(time.Hour)
				r.put("/timestamp.json", signed(t, stamp, r.roleKeys[metadata.TIMESTAMP]...))
			}
			c := r.client(filepath.Join(t.TempDir(), "state"))
			_, err := c.Refresh(context.Background())
			var expired *metadata.ErrExpiredMetadata
			if !errors.As(err, &expired) {
				t.Fatalf("wanted %s expiration: %v", role, err)
			}
		})
	}
}

func TestStateCorruptionAndNetworkIsolation(t *testing.T) {
	for _, mode := range []string{"missing", "truncated", "checksum", "permissions", "symlink", "network", "repository", "anchor"} {
		t.Run(mode, func(t *testing.T) {
			r := newRepository(t)
			dir := filepath.Join(t.TempDir(), "state")
			r.accept(r.client(dir))
			path := filepath.Join(dir, "state.json")
			bundle := r.bundle()
			switch mode {
			case "missing":
				check(t, os.Remove(path))
			case "truncated":
				check(t, os.WriteFile(path, []byte("{"), 0600))
			case "checksum":
				data, err := os.ReadFile(path)
				check(t, err)
				var e envelope
				check(t, json.Unmarshal(data, &e))
				e.Payload = append(e.Payload, ' ')
				data, err = json.Marshal(e)
				check(t, err)
				check(t, os.WriteFile(path, data, 0600))
			case "permissions":
				check(t, os.Chmod(path, 0644))
			case "symlink":
				check(t, os.Rename(path, path+".old"))
				check(t, os.Symlink(path+".old", path))
			case "network":
				bundle.Network = "different"
			case "repository":
				bundle.Repository = "https://elsewhere.example.invalid"
			case "anchor":
				bundle.Root = newRepository(t).initialRoot
			}
			_, err := New(context.Background(), Config{Bundle: bundle, StateDir: dir, HTTPClient: r.server.Client()})
			if !errors.Is(err, ErrState) {
				t.Fatalf("wanted explicit state recovery error: %v", err)
			}
		})
	}
}

func TestInterruptedCommitsDoNotReturnNewAuthority(t *testing.T) {
	for _, phase := range []string{"written", "synced", "renamed", "committed"} {
		t.Run(phase, func(t *testing.T) {
			r := newRepository(t)
			dir := filepath.Join(t.TempDir(), "state")
			c := r.client(dir)
			r.accept(c)
			oldTimestamp := r.get("/timestamp.json")
			r.renew(epoch)
			interrupted := errors.New("simulated interruption")
			c.hook = func(stage string) error {
				if stage == phase {
					return interrupted
				}
				return nil
			}
			view, err := c.Refresh(context.Background())
			if !errors.Is(err, interrupted) || view.Manifest.Roots != nil {
				t.Fatalf("interrupted commit exposed new authority: %+v, %v", view, err)
			}
			c = r.client(dir)
			r.put("/timestamp.json", oldTimestamp)
			_, err = c.Refresh(context.Background())
			if phase == "written" || phase == "synced" {
				check(t, err) // No new metadata had been committed.
			} else {
				var versionErr *metadata.ErrBadVersionNumber
				if !errors.As(err, &versionErr) {
					t.Fatalf("committed progress rolled back: %v", err)
				}
			}
		})
	}
}

func TestConcurrentClientsAndCancelledLockWait(t *testing.T) {
	r := newRepository(t)
	dir := filepath.Join(t.TempDir(), "state")
	c := r.client(dir)
	r.accept(c)
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range 6 {
		other := r.client(dir)
		wg.Add(1)
		go func() { defer wg.Done(); _, err := other.Refresh(context.Background()); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		check(t, err)
	}
	s, _, err := openStore(context.Background(), dir, false)
	check(t, err)
	defer s.close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.Current(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock wait ignored cancellation: %v", err)
	}
}

func TestProcessLockReleasedAfterDeath(t *testing.T) {
	if dir := os.Getenv("SOPH_TEST_LOCK_DIR"); dir != "" {
		s, _, err := openStore(context.Background(), dir, false)
		check(t, err)
		defer s.close()
		fmt.Println("locked")
		time.Sleep(time.Minute)
		return
	}
	r := newRepository(t)
	dir := filepath.Join(t.TempDir(), "state")
	c := r.client(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessLockReleasedAfterDeath$")
	cmd.Env = append(os.Environ(), "SOPH_TEST_LOCK_DIR="+dir)
	stdout, err := cmd.StdoutPipe()
	check(t, err)
	cmd.Stderr = os.Stderr
	check(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "locked" {
		t.Fatal("child did not acquire lock")
	}
	waitCtx, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stop()
	if _, err := c.Refresh(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("child lock ignored: %v", err)
	}
	check(t, cmd.Process.Kill())
	_ = cmd.Wait()
	r.accept(c) // Kernel releases the lock; there is no stale PID lock to delete.
}

func TestFetcherRedirectLengthTLSAndCancellation(t *testing.T) {
	for _, mode := range []string{"redirect", "length", "chunked", "tls", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			var redirected bool
			destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { redirected = true }))
			defer destination.Close()
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch mode {
				case "redirect":
					http.Redirect(w, req, destination.URL, http.StatusFound)
				case "length":
					_, _ = w.Write([]byte(strings.Repeat("x", 32)))
				case "chunked":
					w.(http.Flusher).Flush()
					_, _ = w.Write([]byte(strings.Repeat("x", 32)))
				case "cancel":
					<-req.Context().Done()
				}
			}))
			defer server.Close()
			hc := server.Client()
			hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			if mode == "tls" {
				hc = &http.Client{}
			}
			timeout := 3 * time.Second
			if mode == "cancel" {
				timeout = 50 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			f := boundedFetcher{ctx: ctx, client: hc, origin: server.URL, checkpoint: func() error { return nil }}
			_, err := f.DownloadFile(server.URL+"/timestamp.json", 16, 0)
			if err == nil {
				t.Fatal("unsafe download accepted")
			}
			if mode == "tls" {
				var untrusted x509.UnknownAuthorityError
				if !errors.As(err, &untrusted) {
					t.Fatalf("wanted untrusted TLS certificate rejection: %v", err)
				}
			}
			if redirected {
				t.Fatal("followed redirect outside the repository")
			}
		})
	}
}

func TestNewRejectsTLSBypassesAndIncompleteInitialization(t *testing.T) {
	r := newRepository(t)
	for _, tlsConfig := range []*tls.Config{{InsecureSkipVerify: true}, {ServerName: "another.example.invalid"}} {
		dir := filepath.Join(t.TempDir(), "state")
		hc := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig}}
		if _, err := New(context.Background(), Config{Bundle: r.bundle(), StateDir: dir, HTTPClient: hc}); err == nil {
			t.Fatal("TLS bypass was accepted")
		}
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("invalid transport initialized trust state")
		}
	}
	dir := t.TempDir()
	check(t, os.WriteFile(filepath.Join(dir, "state.pending"), []byte("interrupted"), 0600))
	if _, err := New(context.Background(), Config{Bundle: r.bundle(), StateDir: dir}); !errors.Is(err, ErrState) {
		t.Fatalf("incomplete initialization silently reset: %v", err)
	}
}
