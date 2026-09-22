package omega

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sopholeth/internal/trust/bootstrap"
)

type publicationVerifier struct {
	client *bootstrap.Client
	http   *http.Client
}

func newPublicationVerifier(ctx context.Context, state *store, bundle bootstrap.Bundle, httpClient *http.Client) (*publicationVerifier, func(), error) {
	// The network lock excludes other verifiers. Scratch left by a killed
	// process carries no ordering authority: the immutable journal does.
	names, err := directoryNames(state)
	if err != nil {
		return nil, func() {}, err
	}
	for _, name := range names {
		if !strings.HasPrefix(name, ".verify-") {
			continue
		}
		info, err := state.root.Lstat(name)
		if err != nil {
			return nil, func() {}, err
		}
		if err := safeDir(info); err != nil {
			return nil, func() {}, err
		}
		if err := state.root.RemoveAll(name); err != nil {
			return nil, func() {}, err
		}
	}
	dir, err := os.MkdirTemp(state.root.Name(), ".verify-")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	hc := &http.Client{}
	if httpClient != nil {
		*hc = *httpClient
	}
	hc.Jar = nil
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if hc.Timeout <= 0 || hc.Timeout > 10*time.Second {
		hc.Timeout = 10 * time.Second
	}
	c, err := bootstrap.New(ctx, bootstrap.Config{Bundle: bundle, StateDir: filepath.Join(dir, "client"), HTTPClient: hc})
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return &publicationVerifier{client: c, http: hc}, cleanup, nil
}
func (v *publicationVerifier) verify(ctx context.Context, bundle bootstrap.Bundle, r release) error {
	view, err := v.client.Refresh(ctx)
	if err != nil {
		return fmt.Errorf("omega: served publication verification: %w", err)
	}
	if view.Versions != r.versions() || !bytes.Equal(record(view.Manifest), r.Manifest) {
		return errors.New("omega: served publication differs from the prepared version or root manifest")
	}
	for role, data := range map[string][]byte{"root": bundle.Root, "targets": r.Targets, "snapshot": r.Snapshot, "timestamp": r.Timestamp} {
		if view.MetadataSHA256[role] != digest(data) {
			return fmt.Errorf("omega: served %s bytes differ from the prepared release", role)
		}
	}
	// A fresh client starts from its bundle and does not download 1.root.json.
	// Check its served bytes too, for repository completeness and future recovery.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, bundle.Repository+"/1.root.json", nil)
	if err != nil {
		return err
	}
	response, err := v.http.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("omega: initial root fetch returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, int64(len(bundle.Root))+1))
	if err != nil {
		return err
	}
	if !bytes.Equal(data, bundle.Root) {
		return errors.New("omega: served initial root differs from the authority")
	}
	// Do not report success if the lease expires while checking the final object.
	if !time.Now().Before(view.Expires) {
		return errors.New("omega: publication expired during verification")
	}
	return nil
}
