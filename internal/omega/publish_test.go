//go:build linux

package omega

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"sopholeth/internal/trust/bootstrap"
)

var publicationManifest = []byte(`{"schema":1,"network":"rehearsal","enclave":"default","roots":[{"id":"one","origin":"https://one.example.invalid"},{"id":"two","origin":"https://two.example.invalid"},{"id":"three","origin":"https://three.example.invalid"}]}`)

type publishFixture struct {
	opts      PublishOptions
	server    *httptest.Server
	mu        sync.Mutex
	overrides map[string][]byte
	codes     map[string]int
	requests  []string
}

func newPublishFixture(t *testing.T) *publishFixture {
	return newPublishFixtureAt(t, time.Now().UTC().Truncate(time.Second))
}

func newPublishFixtureAt(t *testing.T, created time.Time) *publishFixture {
	return newCustodyPublishFixture(t, created, false)
}

func newCustodyPublishFixture(t *testing.T, created time.Time, encrypted bool) *publishFixture {
	t.Helper()
	base := t.TempDir()
	f := &publishFixture{opts: PublishOptions{Home: filepath.Join(base, "custody"), Directory: filepath.Join(base, "repository"), Network: "rehearsal", Version: 1, Manifest: publicationManifest, Disposable: true}, overrides: map[string][]byte{}, codes: map[string]int{}}
	basePath := ""
	// Reuse the encrypted lifecycle for hosted-path coverage.
	if encrypted {
		basePath = "/omega"
	}
	files := http.FileServer(http.Dir(f.opts.Directory))
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, basePath+"/") {
			http.NotFound(w, r)
			return
		}
		r.URL.Path = strings.TrimPrefix(r.URL.Path, basePath)
		f.mu.Lock()
		f.requests = append(f.requests, r.URL.Path)
		data, override := f.overrides[r.URL.Path]
		code := f.codes[r.URL.Path]
		f.mu.Unlock()
		if code != 0 {
			w.WriteHeader(code)
			return
		}
		if override {
			_, _ = w.Write(data)
			return
		}
		files.ServeHTTP(w, r)
	}))
	t.Cleanup(f.server.Close)
	f.opts.HTTPClient = f.server.Client()
	if encrypted {
		f.opts.Disposable = false
		f.opts.Passphrase = testKeyPassphrase
		f.opts.codec = fastKeyCodec
	}
	_, err := initializeWithCodec(context.Background(), InitOptions{Home: f.opts.Home, Network: f.opts.Network, Repository: f.server.URL + basePath + "/", Disposable: !encrypted, Encrypted: encrypted, Passphrase: testKeyPassphrase}, created, nil, fastKeyCodec)
	must(t, err)
	return f
}
func (f *publishFixture) statePath() string {
	return filepath.Join(f.opts.Home, f.opts.Network+".publication")
}
func (f *publishFixture) changeRoot() {
	f.opts.Manifest = bytes.Replace(publicationManifest, []byte("three.example.invalid"), []byte("replacement.example.invalid"), 1)
}
func (f *publishFixture) override(path string, data []byte, code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if data == nil {
		delete(f.overrides, path)
	} else {
		f.overrides[path] = data
	}
	if code == 0 {
		delete(f.codes, path)
	} else {
		f.codes[path] = code
	}
}

func TestPublishRoundTripRetryAndStatus(t *testing.T) {
	f := newPublishFixture(t)
	authorityPath := filepath.Join(f.opts.Home, f.opts.Network, "authority.json")
	original := file(t, authorityPath)
	first, err := Publish(context.Background(), f.opts)
	must(t, err)
	if first.Publication != "verified" || first.Release.Version != 1 || first.Release.VerifiedAt.IsZero() || len(first.Release.Roots) != 3 {
		t.Fatalf("bad report: %+v", first)
	}
	before := file(t, filepath.Join(f.opts.Directory, "timestamp.json"))
	journal := file(t, filepath.Join(f.statePath(), "1.release.json"))
	again, err := Publish(context.Background(), f.opts)
	must(t, err)
	if !bytes.Equal(before, file(t, filepath.Join(f.opts.Directory, "timestamp.json"))) || !bytes.Equal(journal, file(t, filepath.Join(f.statePath(), "1.release.json"))) || first.Fingerprint != again.Fingerprint {
		t.Fatal("retry changed numbered release")
	}
	local, err := Status(context.Background(), f.opts.Home, f.opts.Network)
	must(t, err)
	if local.Publication != "not_checked" || local.Release.VerifiedAt.IsZero() {
		t.Fatal("local inspection claimed live verification or lost receipt")
	}
	remote, err := VerifyPublication(context.Background(), f.opts.Home, f.opts.Network, f.opts.HTTPClient)
	must(t, err)
	if remote.Publication != "verified" {
		t.Fatal("remote verification not reported")
	}
	if !bytes.Equal(original, file(t, authorityPath)) {
		t.Fatal("publication changed immutable authority")
	}
	for _, name := range []string{"1.root.json", "1.targets.json", "1.snapshot.json", "timestamp.json"} {
		info, err := os.Stat(filepath.Join(f.opts.Directory, name))
		must(t, err)
		if info.Mode().Perm() != 0644 {
			t.Fatalf("%s is not readable public data", name)
		}
	}
	bundle, err := bootstrap.ParseBundle(file(t, filepath.Join(f.opts.Home, f.opts.Network, "bundle.json")))
	must(t, err)
	client, err := bootstrap.New(context.Background(), bootstrap.Config{Bundle: bundle, StateDir: filepath.Join(t.TempDir(), "client"), HTTPClient: f.opts.HTTPClient})
	must(t, err)
	view, err := client.Refresh(context.Background())
	must(t, err)
	if view.Versions.Timestamp != 1 || view.MetadataSHA256["timestamp"] != digest(before) {
		t.Fatal("real client did not accept exact release")
	}
	f.opts.Version = 2
	f.changeRoot()
	second, err := Publish(context.Background(), f.opts)
	must(t, err)
	if second.Release.Version != 2 || !bytes.Equal(journal, file(t, filepath.Join(f.statePath(), "1.release.json"))) {
		t.Fatal("new release changed history")
	}
	view, err = client.Refresh(context.Background())
	must(t, err)
	if view.Versions.Timestamp != 2 || view.Manifest.Roots[2].Origin != "https://replacement.example.invalid" {
		t.Fatal("returning client failed to adopt membership update")
	}
	f.override("/timestamp.json", before, 0)
	if _, err := client.Refresh(context.Background()); err == nil {
		t.Fatal("returning client accepted publication rollback")
	}
}

func TestPublishValidatesApprovalAndVersionBeforeWriting(t *testing.T) {
	cases := map[string]func(*PublishOptions){
		"empty": func(o *PublishOptions) { o.Manifest = nil },
		"wrong-network": func(o *PublishOptions) {
			o.Manifest = bytes.Replace(publicationManifest, []byte("rehearsal"), []byte("other"), 1)
		},
		"duplicate-origin": func(o *PublishOptions) {
			o.Manifest = bytes.Replace(publicationManifest, []byte("three.example.invalid"), []byte("one.example.invalid"), 1)
		},
		"http": func(o *PublishOptions) {
			o.Manifest = bytes.Replace(publicationManifest, []byte("https:"), []byte("http:"), 1)
		},
		"unsupported-schema": func(o *PublishOptions) {
			o.Manifest = bytes.Replace(publicationManifest, []byte(`"schema":1`), []byte(`"schema":2`), 1)
		},
		"duplicate-json": func(o *PublishOptions) {
			o.Manifest = bytes.Replace(publicationManifest, []byte(`"schema":1`), []byte(`"schema":1,"schema":1`), 1)
		},
		"too-large": func(o *PublishOptions) { o.Manifest = bytes.Repeat([]byte(" "), (64<<10)+1) },
		"not-three": func(o *PublishOptions) {
			m, err := bootstrap.ParseManifest(publicationManifest, o.Network)
			if err != nil {
				panic(err)
			}
			m.Roots = m.Roots[:2]
			o.Manifest = record(m)
		},
		"zero-version": func(o *PublishOptions) { o.Version = 0 },
		"production":   func(o *PublishOptions) { o.Disposable = false },
	}
	// Rejected options must leave this initialized home untouched, so each case
	// can use the same authority without repeating initialization and TLS setup.
	f := newPublishFixture(t)
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			opts := f.opts
			change(&opts)
			if _, err := Publish(context.Background(), opts); err == nil {
				t.Fatal("invalid approval accepted")
			}
			assertAbsent(t, f.opts.Directory)
			assertAbsent(t, f.statePath())
		})
	}
	_, err := Publish(context.Background(), f.opts)
	must(t, err)
	original := file(t, filepath.Join(f.opts.Directory, "timestamp.json"))
	f.changeRoot()
	if _, err := Publish(context.Background(), f.opts); err == nil {
		t.Fatal("same version accepted new membership")
	}
	f.opts.Version = 3
	if _, err := Publish(context.Background(), f.opts); err == nil {
		t.Fatal("skipped publication version")
	}
	f.opts.Version = 2
	_, err = Publish(context.Background(), f.opts)
	must(t, err)
	f.opts.Version = 1
	if _, err := Publish(context.Background(), f.opts); err == nil {
		t.Fatal("rolled back latest publication")
	}
	if bytes.Equal(original, file(t, filepath.Join(f.opts.Directory, "timestamp.json"))) {
		t.Fatal("timestamp rolled back")
	}
}

func TestPublishInterruptionsReusePreparedBytes(t *testing.T) {
	// Shared journal and repository write boundaries are exercised here, rather
	// than repeated for renewal and every rotation role that uses this publisher.
	phases := []string{"1.release.json:written", "1.release.json:linked", "release:durable", "public:1.root.json:written", "public:1.root.json:visible", "public:1.targets.json:durable", "public:1.snapshot.json:visible", "public:objects-ready", "public:timestamp.json:written", "public:timestamp.json:visible", "public:timestamp.json:durable", "publication:verified"}
	for _, phase := range phases {
		t.Run(phase, func(t *testing.T) {
			f := newPublishFixture(t)
			stop := errors.New("interrupted")
			_, err := publish(context.Background(), f.opts, testTime, func(at string) error {
				if at == phase {
					return stop
				}
				return nil
			})
			if !errors.Is(err, stop) {
				t.Fatalf("fault phase missed: %v", err)
			}
			journal, err := os.ReadFile(filepath.Join(f.statePath(), "1.release.json"))
			if errors.Is(err, os.ErrNotExist) {
				journal = file(t, filepath.Join(f.statePath(), "1.release.json.pending"))
			} else {
				must(t, err)
			}
			if !strings.Contains(phase, "timestamp.json:visible") && !strings.Contains(phase, "timestamp.json:durable") && phase != "publication:verified" {
				assertAbsent(t, filepath.Join(f.opts.Directory, "timestamp.json"))
			}
			_, err = publish(context.Background(), f.opts, testTime.Add(time.Minute), nil)
			must(t, err)
			if !bytes.Equal(journal, file(t, filepath.Join(f.statePath(), "1.release.json"))) {
				t.Fatal("retry re-signed or renumbered prepared release")
			}
			for _, dir := range []string{f.statePath(), f.opts.Directory, filepath.Join(f.opts.Directory, "targets")} {
				pending, err := filepath.Glob(filepath.Join(dir, "*.pending"))
				must(t, err)
				if len(pending) != 0 {
					t.Fatalf("retry left pending files: %v", pending)
				}
			}
		})
	}
}

func TestPublishRetrySweepsOnlyMatchingPendingTwins(t *testing.T) {
	f := newPublishFixture(t)
	_, err := Publish(context.Background(), f.opts)
	must(t, err)
	var first release
	must(t, decodeRecord(file(t, filepath.Join(f.statePath(), "1.release.json")), &first))
	f.opts.Version = 2
	f.changeRoot()
	_, err = Publish(context.Background(), f.opts)
	must(t, err)
	var second release
	must(t, decodeRecord(file(t, filepath.Join(f.statePath(), "2.release.json")), &second))

	finals := map[string][]byte{}
	preserved := map[string][]byte{}
	var twins []string
	for _, pair := range []struct {
		dir, old, current string
		mode              os.FileMode
	}{
		{f.statePath(), "1.release.json", "2.release.json", 0600},
		{f.opts.Directory, "1.snapshot.json", "2.snapshot.json", 0644},
		{filepath.Join(f.opts.Directory, "targets"), digest(first.Manifest) + ".bootstrap.json", digest(second.Manifest) + ".bootstrap.json", 0644},
	} {
		old := filepath.Join(pair.dir, pair.old)
		finals[old] = file(t, old)
		must(t, os.Link(old, old+".pending"))
		twins = append(twins, old+".pending")
		current := filepath.Join(pair.dir, pair.current)
		finals[current] = file(t, current)
		for name, data := range map[string][]byte{
			pair.current + ".pending":  []byte("different pending bytes"),
			"uncommitted.json.pending": []byte("no final object"),
		} {
			path := filepath.Join(pair.dir, name)
			preserved[path] = data
			must(t, os.WriteFile(path, data, pair.mode))
		}
	}
	// Identical bytes also qualify when the pending file is a separate inode.
	for _, path := range []string{filepath.Join(f.statePath(), "binding.json"), filepath.Join(f.opts.Directory, "1.root.json")} {
		finals[path] = file(t, path)
		must(t, os.WriteFile(path+".pending", finals[path], 0600))
		twins = append(twins, path+".pending")
	}
	_, err = Publish(context.Background(), f.opts)
	must(t, err)
	for _, path := range twins {
		assertAbsent(t, path)
	}
	for path, data := range preserved {
		if !bytes.Equal(data, file(t, path)) {
			t.Fatalf("retry changed unmatched pending file %s", path)
		}
	}
	for path, data := range finals {
		if !bytes.Equal(data, file(t, path)) {
			t.Fatalf("cleanup changed final object %s", path)
		}
	}
}

func TestInterruptedUpdateKeepsOldTimestampUntilObjectsReady(t *testing.T) {
	f := newPublishFixture(t)
	_, err := Publish(context.Background(), f.opts)
	must(t, err)
	old := file(t, filepath.Join(f.opts.Directory, "timestamp.json"))
	f.opts.Version = 2
	f.changeRoot()
	_, err = publish(context.Background(), f.opts, testTime, func(at string) error {
		if at == "public:objects-ready" {
			return errors.New("stop")
		}
		return nil
	})
	if err == nil {
		t.Fatal("expected interruption")
	}
	if !bytes.Equal(old, file(t, filepath.Join(f.opts.Directory, "timestamp.json"))) {
		t.Fatal("advertised update before completing immutable objects")
	}
	_, err = Publish(context.Background(), f.opts)
	must(t, err)
	if bytes.Equal(old, file(t, filepath.Join(f.opts.Directory, "timestamp.json"))) {
		t.Fatal("retry did not finish update")
	}
}

func TestVerificationDetectsOutageMissingAndChangedObjects(t *testing.T) {
	// All cases disturb only HTTP responses. Share the signed repository and
	// restore healthy verification after each fault, including failed assertions.
	f := newPublishFixture(t)
	_, err := Publish(context.Background(), f.opts)
	must(t, err)
	old := file(t, filepath.Join(f.opts.Directory, "timestamp.json"))
	f.opts.Version = 2
	f.changeRoot()
	_, err = Publish(context.Background(), f.opts)
	must(t, err)
	current := file(t, filepath.Join(f.opts.Directory, "timestamp.json"))
	manifest, err := approvedManifest(f.opts.Manifest, f.opts.Network)
	must(t, err)
	for _, kind := range []string{"outage", "missing-snapshot", "changed-target", "missing-initial-root", "redirect", "wrong-signature", "stale-version", "same-version-different-bytes"} {
		t.Run(kind, func(t *testing.T) {
			t.Cleanup(func() {
				f.mu.Lock()
				f.overrides = map[string][]byte{}
				f.codes = map[string]int{}
				f.mu.Unlock()
				_, err := VerifyPublication(context.Background(), f.opts.Home, f.opts.Network, f.opts.HTTPClient)
				must(t, err)
			})
			switch kind {
			case "outage":
				f.override("/timestamp.json", nil, 503)
			case "missing-snapshot":
				f.override("/2.snapshot.json", nil, 404)
			case "changed-target":
				f.override("/targets/"+digest(manifest)+".bootstrap.json", []byte("changed"), 0)
			case "missing-initial-root":
				f.override("/1.root.json", nil, 404)
			case "redirect":
				f.override("/timestamp.json", nil, 302)
			case "wrong-signature":
				f.override("/timestamp.json", bytes.Replace(current, []byte(`"sig":"`), []byte(`"sig":"00`), 1), 0)
			case "stale-version":
				f.override("/timestamp.json", old, 0)
			case "same-version-different-bytes":
				f.override("/timestamp.json", append(bytes.Clone(current), '\n'), 0)
			}
			report, err := VerifyPublication(context.Background(), f.opts.Home, f.opts.Network, f.opts.HTTPClient)
			if err == nil || report.Publication != "failed" || report.Problem == "" {
				t.Fatal("verification claimed success")
			}
			local, err := Status(context.Background(), f.opts.Home, f.opts.Network)
			if err == nil || local.Release.LastError == "" || local.Release.VerifiedAt.IsZero() {
				t.Fatal("failure was not persisted alongside historical successful check")
			}
		})
	}
}

func TestPublishRejectsImmutableConflictAndLostHistory(t *testing.T) {
	for _, kind := range []string{"immutable-conflict", "unknown-timestamp", "missing-release", "missing-binding", "lost-journal", "output-change", "private-overlap", "unknown-files"} {
		t.Run(kind, func(t *testing.T) {
			f := newPublishFixture(t)
			if kind == "private-overlap" {
				f.opts.Directory = f.opts.Home
				if _, err := Publish(context.Background(), f.opts); err == nil {
					t.Fatal("accepted private/public overlap")
				}
				return
			}
			if kind == "unknown-files" {
				must(t, os.Mkdir(f.opts.Directory, 0755))
				must(t, os.WriteFile(filepath.Join(f.opts.Directory, "keep"), []byte("keep"), 0644))
				if _, err := Publish(context.Background(), f.opts); err == nil {
					t.Fatal("adopted nonempty repository")
				}
				return
			}
			_, err := Publish(context.Background(), f.opts)
			must(t, err)
			original := file(t, filepath.Join(f.opts.Directory, "timestamp.json"))
			switch kind {
			case "immutable-conflict":
				must(t, os.WriteFile(filepath.Join(f.opts.Directory, "1.targets.json"), []byte("different"), 0644))
			case "unknown-timestamp":
				must(t, os.WriteFile(filepath.Join(f.opts.Directory, "timestamp.json"), []byte("different"), 0644))
			case "missing-release":
				must(t, os.Remove(filepath.Join(f.statePath(), "1.release.json")))
			case "missing-binding":
				must(t, os.Remove(filepath.Join(f.statePath(), "binding.json")))
			case "lost-journal":
				must(t, os.RemoveAll(f.statePath()))
			case "output-change":
				f.opts.Directory = filepath.Join(t.TempDir(), "different")
			}
			if _, err := Publish(context.Background(), f.opts); err == nil {
				t.Fatal("published through inconsistent history/destination")
			}
			if kind == "missing-release" {
				assertAbsent(t, filepath.Join(f.statePath(), "1.release.json"))
			}
			if kind == "missing-binding" {
				if _, err := VerifyPublication(context.Background(), f.opts.Home, f.opts.Network, f.opts.HTTPClient); err == nil {
					t.Fatal("missing binding mistaken for unpublished network")
				}
			}
			if kind != "unknown-timestamp" && kind != "output-change" && !bytes.Equal(original, file(t, filepath.Join(f.opts.Directory, "timestamp.json"))) {
				t.Fatal("failure replaced timestamp")
			}
		})
	}
}

func TestExpiredPreparedReleaseRequiresHigherRepair(t *testing.T) {
	f := newPublishFixture(t)
	_, err := publish(context.Background(), f.opts, testTime.Add(-timestampLifetime(timestampDays)-time.Hour), func(at string) error {
		if at == "release:durable" {
			return errors.New("stop")
		}
		return nil
	})
	if err == nil {
		t.Fatal("expected interruption")
	}
	original := file(t, filepath.Join(f.statePath(), "1.release.json"))
	if _, err := Publish(context.Background(), f.opts); err == nil {
		t.Fatal("published expired release")
	}
	assertAbsent(t, filepath.Join(f.opts.Directory, "timestamp.json"))
	f.opts.Version = 2
	_, err = Publish(context.Background(), f.opts)
	must(t, err)
	if !bytes.Equal(original, file(t, filepath.Join(f.statePath(), "1.release.json"))) {
		t.Fatal("repair rewrote expired release")
	}
}

func TestConcurrentPublishersAndCanceledLock(t *testing.T) {
	f := newPublishFixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Go(func() { _, err := Publish(context.Background(), f.opts); errs <- err })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		must(t, err)
	}
	assertAbsent(t, filepath.Join(f.statePath(), "2.release.json"))
	home, err := openHome(context.Background(), f.opts.Home, f.opts.Network, false)
	must(t, err)
	defer home.close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := Publish(ctx, f.opts); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock cancellation: %v", err)
	}
}

func TestPublicationProcessDeathRecovery(t *testing.T) {
	if os.Getenv("SOPH_PUBLISH_TEST_CHILD") == "1" {
		opts := PublishOptions{Home: os.Getenv("SOPH_PUBLISH_TEST_HOME"), Directory: os.Getenv("SOPH_PUBLISH_TEST_REPO"), Network: "rehearsal", Version: 1, Manifest: publicationManifest, Disposable: true}
		_, err := publish(context.Background(), opts, testTime, func(at string) error {
			if at == os.Getenv("SOPH_PUBLISH_TEST_PHASE") {
				if err := os.WriteFile(filepath.Join(opts.Home, "ready"), []byte("ready"), 0600); err != nil {
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
	for _, phase := range []string{"1.release.json:linked", "public:timestamp.json:visible"} {
		t.Run(phase, func(t *testing.T) {
			f := newPublishFixture(t)
			cmd := exec.Command(os.Args[0], "-test.run=^TestPublicationProcessDeathRecovery$")
			cmd.Env = append(os.Environ(), "SOPH_PUBLISH_TEST_CHILD=1", "SOPH_PUBLISH_TEST_HOME="+f.opts.Home, "SOPH_PUBLISH_TEST_REPO="+f.opts.Directory, "SOPH_PUBLISH_TEST_PHASE="+phase)
			var output bytes.Buffer
			cmd.Stdout = &output
			cmd.Stderr = &output
			must(t, cmd.Start())
			defer func() { _ = cmd.Process.Kill() }()
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(f.opts.Home, "ready")); err == nil {
					break
				}
				if time.Now().After(deadline) {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					t.Fatalf("child never reached boundary: %s", output.String())
				}
				time.Sleep(10 * time.Millisecond)
			}
			original := file(t, filepath.Join(f.statePath(), "1.release.json"))
			must(t, cmd.Process.Kill())
			_ = cmd.Wait()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := Publish(ctx, f.opts)
			must(t, err)
			if !bytes.Equal(original, file(t, filepath.Join(f.statePath(), "1.release.json"))) {
				t.Fatal("process recovery regenerated release")
			}
			assertAbsent(t, filepath.Join(f.statePath(), "1.release.json.pending"))
			assertAbsent(t, filepath.Join(f.opts.Directory, "1.snapshot.json.pending"))
		})
	}
}

func TestPublicFilesExcludePrivateKeys(t *testing.T) {
	f := newPublishFixture(t)
	report, err := Publish(context.Background(), f.opts)
	must(t, err)
	var a authority
	must(t, decodeRecord(file(t, filepath.Join(f.opts.Home, f.opts.Network, "authority.json")), &a))
	must(t, filepath.WalkDir(f.opts.Directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data := file(t, path)
		for _, key := range a.Keys {
			if bytes.Contains(data, []byte(key)) || bytes.Contains(record(report), []byte(key)) {
				return fmt.Errorf("private material in public output")
			}
		}
		return nil
	}))
}

func TestPublisherRefusesTLSBypassAndUnsafeDestination(t *testing.T) {
	for _, kind := range []string{"insecure-tls", "symlink-destination", "symlink-targets", "writable-destination", "home-under-repository"} {
		t.Run(kind, func(t *testing.T) {
			f := newPublishFixture(t)
			switch kind {
			case "insecure-tls":
				transport := f.opts.HTTPClient.Transport.(*http.Transport).Clone()
				transport.TLSClientConfig.InsecureSkipVerify = true
				f.opts.HTTPClient = &http.Client{Transport: transport}
			case "symlink-destination":
				must(t, os.Symlink(t.TempDir(), f.opts.Directory))
			case "writable-destination":
				must(t, os.Mkdir(f.opts.Directory, 0755))
				must(t, os.Chmod(f.opts.Directory, 0777))
			case "home-under-repository":
				f.opts.Directory = filepath.Dir(f.opts.Home)
			case "symlink-targets":
				_, err := publish(context.Background(), f.opts, testTime, func(at string) error {
					if at == "release:durable" {
						return errors.New("stop")
					}
					return nil
				})
				if err == nil {
					t.Fatal("expected interruption")
				}
				must(t, os.Symlink(t.TempDir(), filepath.Join(f.opts.Directory, "targets")))
			}
			if _, err := Publish(context.Background(), f.opts); err == nil {
				t.Fatal("unsafe publisher configuration accepted")
			}
			assertAbsent(t, filepath.Join(f.opts.Directory, "timestamp.json"))
		})
	}
}

func TestPublicationTornWritesAndConflictingPendingRelease(t *testing.T) {
	for _, kind := range []string{"torn-release", "corrupt-pending", "torn-public", "lost-tail-without-timestamp"} {
		t.Run(kind, func(t *testing.T) {
			f := newPublishFixture(t)
			phase := "1.release.json:written"
			if kind == "torn-public" {
				phase = "public:1.targets.json:written"
			}
			if kind == "lost-tail-without-timestamp" {
				_, err := Publish(context.Background(), f.opts)
				must(t, err)
				must(t, os.Remove(filepath.Join(f.statePath(), "1.release.json")))
				must(t, os.Remove(filepath.Join(f.opts.Directory, "timestamp.json")))
				if _, err := Publish(context.Background(), f.opts); err == nil {
					t.Fatal("reused a version visible in immutable metadata")
				}
				assertAbsent(t, filepath.Join(f.statePath(), "1.release.json"))
				return
			}
			_, err := publish(context.Background(), f.opts, testTime, func(at string) error {
				if at == phase {
					return errors.New("stop")
				}
				return nil
			})
			if err == nil {
				t.Fatal("expected interruption")
			}
			path := filepath.Join(f.statePath(), "1.release.json.pending")
			switch kind {
			case "torn-release":
				must(t, os.WriteFile(path, []byte(`{"schema":`), 0600))
			case "corrupt-pending":
				var r release
				must(t, decodeRecord(file(t, path), &r))
				r.Fingerprint = "wrong"
				must(t, os.WriteFile(path, record(r), 0600))
				if _, err := Publish(context.Background(), f.opts); err == nil {
					t.Fatal("replaced corrupt complete pending release")
				}
				return
			case "torn-public":
				must(t, os.WriteFile(filepath.Join(f.opts.Directory, "1.targets.json.pending"), []byte("torn"), 0644))
			}
			_, err = Publish(context.Background(), f.opts)
			must(t, err)
		})
	}
}

func TestPublishWithRestrictiveUmask(t *testing.T) {
	if os.Getenv("SOPH_PUBLISH_UMASK_TEST_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestPublishWithRestrictiveUmask$")
		cmd.Env = append(os.Environ(), "SOPH_PUBLISH_UMASK_TEST_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("umask subprocess: %v\n%s", err, output)
		}
		return
	}
	// Umask is process-wide: isolate this check from other tests and goroutines.
	previous := syscall.Umask(0077)
	defer syscall.Umask(previous)
	f, _, opts := renewalFixture(t, renewalInterval+time.Hour)
	mode := func(path string, want os.FileMode) {
		t.Helper()
		info, err := os.Stat(path)
		must(t, err)
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s: mode %04o, want %04o", path, got, want)
		}
	}
	targets := filepath.Join(f.opts.Directory, "targets")
	mode(f.opts.Directory, 0755)
	mode(targets, 0755)
	mode(filepath.Join(f.opts.Directory, "timestamp.json"), 0644)
	mode(filepath.Join(f.opts.Directory, ".omega-publish.lock"), 0600)
	mode(f.opts.Home, 0700)
	mode(filepath.Join(f.opts.Home, f.opts.Network, "authority.json"), 0600)
	mode(opts.Home, 0700)
	mode(filepath.Join(opts.Home, opts.Network+".renewal.json"), 0600)

	// An existing directory belongs to the operator; retries must not widen it.
	must(t, os.Chmod(f.opts.Directory, 0750))
	must(t, os.Chmod(targets, 0700))
	_, err := Renew(context.Background(), opts)
	must(t, err)
	mode(f.opts.Directory, 0750)
	mode(targets, 0700)

	// Renewal can recreate a missing targets directory while repairing objects.
	must(t, os.RemoveAll(targets))
	_, err = Renew(context.Background(), opts)
	must(t, err)
	mode(f.opts.Directory, 0750)
	mode(targets, 0755)
}
