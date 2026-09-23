package bootstrap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
	"github.com/theupdateframework/go-tuf/v2/metadata/config"
	"github.com/theupdateframework/go-tuf/v2/metadata/trustedmetadata"
	"github.com/theupdateframework/go-tuf/v2/metadata/updater"
)

var ErrNoManifest = errors.New("bootstrap: no current authenticated manifest")

type Config struct {
	Bundle     Bundle
	StateDir   string
	HTTPClient *http.Client // Optional TLS/transport configuration, never a trust bypass.
}

type Client struct {
	bundle Bundle
	dir    string
	http   *http.Client
	now    func() time.Time
	hook   func(string) error // Tests only: inject interrupted durable writes.
}

type Versions struct {
	Root      int64 `json:"root"`
	Timestamp int64 `json:"timestamp"`
	Snapshot  int64 `json:"snapshot"`
	Targets   int64 `json:"targets"`
}

type View struct {
	Manifest Manifest  `json:"manifest"`
	Expires  time.Time `json:"expires"`
	Versions Versions  `json:"versions"`
	// Digests of the exact authenticated metadata, for publisher verification.
	MetadataSHA256 map[string]string `json:"metadata_sha256"`
}

// New initializes only an absent state directory. Existing, incomplete state
// is an error, never permission to start again from the bundled root.
// StateDir's parent must exist. State storage requires local filesystem locks
// and durable rename/fsync semantics (Linux and supported BSD/macOS systems).
func New(ctx context.Context, cfg Config) (*Client, error) {
	if err := cfg.Bundle.Validate(); err != nil {
		return nil, err
	}
	if cfg.StateDir == "" {
		return nil, errors.New("bootstrap: state directory is required")
	}
	dir, err := filepath.Abs(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(dir))
	if err != nil {
		return nil, err
	}
	dir = filepath.Join(parent, filepath.Base(dir))
	bundle := cfg.Bundle
	bundle.Root = bytes.Clone(bundle.Root)
	bundle.Repository, _ = httpsRepository(bundle.Repository)
	hc := &http.Client{}
	if cfg.HTTPClient != nil {
		*hc = *cfg.HTTPClient
	}
	transport, standard := http.DefaultTransport.(*http.Transport)
	if !standard {
		return nil, errors.New("bootstrap: default HTTP transport must be a standard TLS-verifying transport")
	}
	if hc.Transport != nil {
		var ok bool
		transport, ok = hc.Transport.(*http.Transport)
		if !ok {
			return nil, errors.New("bootstrap: HTTP client must use a standard TLS-verifying transport")
		}
	}
	transport = transport.Clone()
	if transport.DialTLS != nil || transport.DialTLSContext != nil ||
		(transport.TLSClientConfig != nil && (transport.TLSClientConfig.InsecureSkipVerify || transport.TLSClientConfig.ServerName != "")) {
		return nil, errors.New("bootstrap: TLS verification and repository hostname verification cannot be overridden")
	}
	hc.Transport = transport
	if hc.Timeout <= 0 || hc.Timeout > 10*time.Second {
		hc.Timeout = 10 * time.Second
	}
	hc.Jar = nil
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c := &Client{bundle: bundle, dir: dir, http: hc, now: time.Now}
	s, created, err := openStore(ctx, dir, true)
	if err != nil {
		return nil, err
	}
	defer s.close()
	if created {
		st := &state{Schema: 1, Network: bundle.Network, Repository: bundle.Repository,
			Fingerprint: bundle.Fingerprint(), Metadata: map[string][]byte{"root": bytes.Clone(bundle.Root)}}
		if err := s.save(st); err != nil {
			return nil, err
		}
	}
	if _, err := s.load(bundle); err != nil {
		return nil, err
	}
	return c, nil
}

// Current revalidates the accepted manifest and its expiry on every call,
// including offline restarts. It never fetches or switches networks. Consumers
// must also revoke any retained root-status flag when View.Expires is reached.
func (c *Client) Current(ctx context.Context) (View, error) {
	s, _, err := openStore(ctx, c.dir, false)
	if err != nil {
		return View{}, err
	}
	defer s.close()
	st, err := s.load(c.bundle)
	if err != nil {
		return View{}, err
	}
	if st.Accepted == nil {
		return View{}, ErrNoManifest
	}
	// A checkpoint that changes root or membership metadata revokes the old
	// accepted view before another download, even if the refresh later fails.
	for _, role := range []string{"root", "targets"} {
		if !bytes.Equal(st.Accepted.Metadata[role], st.Metadata[role]) {
			return View{}, fmt.Errorf("%w: accepted authority differs from current state", ErrState)
		}
	}
	return verifyAccepted(st.Accepted, c.bundle.Network, c.now())
}

// Refresh uses go-tuf's full updater. Before each subsequent network request,
// verified cache progress is checkpointed as one durable state transaction.
// Errors never return a fresh view; callers can explicitly query Current for
// a previously accepted, still-valid view. Root/targets changes revoke it.
func (c *Client) Refresh(ctx context.Context) (view View, resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	s, _, err := openStore(ctx, c.dir, false)
	if err != nil {
		return View{}, err
	}
	defer s.close()
	s.hook = c.hook
	st, err := s.load(c.bundle)
	if err != nil {
		return View{}, err
	}
	// Scratch is private and never authoritative. A killed process can leave
	// it behind; only these fixed, owned scratch entries are discarded.
	if err := s.root.RemoveAll("work"); err != nil {
		return View{}, err
	}
	if err := s.root.Mkdir("work", 0700); err != nil {
		return View{}, err
	}
	defer s.root.RemoveAll("work")
	work := filepath.Join(c.dir, "work")
	metaDir := filepath.Join(work, "metadata")
	if err := os.Mkdir(metaDir, 0700); err != nil {
		return View{}, err
	}
	for name, data := range st.Metadata {
		if err := os.WriteFile(filepath.Join(metaDir, name+".json"), data, 0600); err != nil {
			return View{}, err
		}
	}
	checkpointFailed := false
	checkpoint := func() error {
		if checkpointFailed {
			return errors.New("bootstrap: prior state checkpoint failed")
		}
		err := checkpointMetadata(s, st, metaDir)
		checkpointFailed = err != nil
		return err
	}
	// Also checkpoint the last successful role after network/validation errors.
	// Never retry a failed persistence operation inside the same transaction.
	defer func() {
		if !checkpointFailed {
			if err := checkpoint(); err != nil {
				view = View{}
				resultErr = errors.Join(resultErr, err)
			}
		}
	}()
	cfg, err := config.New(c.bundle.Repository, st.Metadata["root"])
	if err != nil {
		return View{}, err
	}
	cfg.LocalMetadataDir = metaDir
	cfg.LocalTargetsDir = filepath.Join(work, "targets")
	cfg.RootMaxLength = metadataLimit
	cfg.SnapshotMaxLength = metadataLimit
	cfg.TargetsMaxLength = metadataLimit
	cfg.Fetcher = &boundedFetcher{ctx: ctx, client: c.http, repository: c.bundle.Repository, checkpoint: checkpoint}
	u, err := updater.New(cfg)
	if err != nil {
		return View{}, err
	}
	if err := u.Refresh(); err != nil {
		return View{}, err
	}
	if err := checkpoint(); err != nil {
		return View{}, err
	}
	if u.GetTopLevelTargets()["bootstrap.json"] == nil {
		return View{}, errors.New("bootstrap: bootstrap.json must be a top-level target")
	}
	target, err := u.GetTargetInfo("bootstrap.json")
	if err != nil {
		return View{}, err
	}
	if target.Length < 1 || target.Length > manifestLimit {
		return View{}, errors.New("bootstrap: signed manifest length exceeds limit")
	}
	_, data, err := u.DownloadTarget(target, "", "")
	if err != nil {
		return View{}, err
	}
	record := &accepted{Metadata: maps.Clone(st.Metadata), Manifest: data}
	view, err = verifyAccepted(record, c.bundle.Network, c.now())
	if err != nil {
		return View{}, err
	}
	st.Accepted = record
	if err := s.save(st); err != nil {
		checkpointFailed = true
		return View{}, err
	}
	return view, nil
}

func checkpointMetadata(s *store, st *state, dir string) error {
	files := make(map[string][]byte)
	for _, role := range metadata.TOP_LEVEL_ROLE_NAMES {
		f, err := os.Open(filepath.Join(dir, role+".json"))
		if errors.Is(err, os.ErrNotExist) && role != metadata.ROOT {
			continue
		}
		if err != nil {
			return err
		}
		data, err := readLimited(f, metadataLimit)
		closeErr := f.Close()
		if err = errors.Join(err, closeErr); err != nil {
			return err
		}
		files[role] = data
	}
	if maps.EqualFunc(files, st.Metadata, bytes.Equal) {
		return nil
	}
	if err := checkMetadata(files); err != nil {
		return err
	}
	next := *st
	next.Metadata = files
	if !bytes.Equal(files["root"], st.Metadata["root"]) || !bytes.Equal(files["targets"], st.Metadata["targets"]) {
		next.Accepted = nil
	}
	if err := s.save(&next); err != nil {
		return err
	}
	*st = next
	return nil
}

func verifyAccepted(a *accepted, network string, now time.Time) (View, error) {
	tm, err := trustedmetadata.New(a.Metadata["root"])
	if err != nil {
		return View{}, err
	}
	tm.RefTime = now
	if _, err := tm.UpdateTimestamp(a.Metadata["timestamp"]); err != nil {
		return View{}, err
	}
	if _, err := tm.UpdateSnapshot(a.Metadata["snapshot"], false); err != nil {
		return View{}, err
	}
	targets, err := tm.UpdateDelegatedTargets(a.Metadata["targets"], metadata.TARGETS, metadata.ROOT)
	if err != nil {
		return View{}, err
	}
	target := targets.Signed.Targets["bootstrap.json"]
	if target == nil {
		return View{}, errors.New("bootstrap: missing approved bootstrap.json")
	}
	if err := target.VerifyLengthHashes(a.Manifest); err != nil {
		return View{}, err
	}
	m, err := ParseManifest(a.Manifest, network)
	if err != nil {
		return View{}, err
	}
	deadline := tm.Root.Signed.Expires
	for _, expires := range []time.Time{tm.Timestamp.Signed.Expires, tm.Snapshot.Signed.Expires, targets.Signed.Expires} {
		if expires.Before(deadline) {
			deadline = expires
		}
	}
	// go-tuf v2.4.2 considers metadata expired strictly after its deadline.
	// Sopholeth's runtime lease ends at the deadline, including equality.
	if !now.Before(deadline) {
		return View{}, &metadata.ErrExpiredMetadata{Msg: "bootstrap authority lease has expired"}
	}
	hashes := make(map[string]string, len(a.Metadata))
	for role, data := range a.Metadata {
		hashes[role] = fmt.Sprintf("%x", sha256.Sum256(data))
	}
	return View{Manifest: m, Expires: deadline, MetadataSHA256: hashes, Versions: Versions{Root: tm.Root.Signed.Version,
		Timestamp: tm.Timestamp.Signed.Version, Snapshot: tm.Snapshot.Signed.Version, Targets: targets.Signed.Version}}, nil
}

type boundedFetcher struct {
	ctx        context.Context
	client     *http.Client
	repository string
	checkpoint func() error
}

func (f *boundedFetcher) DownloadFile(rawURL string, maxLength int64, _ time.Duration) ([]byte, error) {
	if err := f.checkpoint(); err != nil {
		return nil, err
	}
	location, err := httpsRepository(rawURL)
	if err != nil || location != rawURL || !strings.HasPrefix(location, f.repository+"/") {
		return nil, errors.New("bootstrap: download escaped the configured HTTPS repository")
	}
	if maxLength < 1 || maxLength > metadataLimit {
		return nil, errors.New("bootstrap: download size exceeds configured limit")
	}
	req, err := http.NewRequestWithContext(f.ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	res, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, &metadata.ErrDownloadHTTP{StatusCode: res.StatusCode, URL: rawURL}
	}
	if res.ContentLength > maxLength {
		return nil, &metadata.ErrDownloadLengthMismatch{Msg: "response exceeds size limit"}
	}
	data, err := readLimited(res.Body, maxLength)
	if err != nil {
		return nil, err
	}
	// Preserve exact signed bytes; the updater owns metadata parsing.
	return data, nil
}
