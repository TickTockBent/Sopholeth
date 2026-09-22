package omega

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"sopholeth/internal/trust/bootstrap"
)

type publicationBinding struct {
	Schema      int    `json:"schema"`
	Network     string `json:"network"`
	Fingerprint string `json:"fingerprint"`
	Directory   string `json:"directory"`
}

type publicObject struct {
	name string
	data []byte
}
type repositoryStore struct {
	*store
	path string
}

func canonicalPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("omega: repository directory is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}
func containsPath(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func openRepository(ctx context.Context, path, home string) (*repositoryStore, error) {
	canonical, err := canonicalPath(path)
	if err != nil {
		return nil, err
	}
	if containsPath(home, canonical) || containsPath(canonical, home) {
		return nil, errors.New("omega: repository and private custody home must be disjoint directory trees")
	}
	for p := filepath.Dir(canonical); ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		if err := safeAncestor(info); err != nil {
			return nil, err
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	if err := os.Mkdir(canonical, 0755); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(canonical)
	if err != nil {
		return nil, err
	}
	if err := publicDir(info); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, err
	}
	s := &store{root: root}
	fail := func(err error) (*repositoryStore, error) { s.close(); return nil, err }
	if info, err := root.Lstat(".omega-publish.lock"); err == nil {
		if err := safeFile(info); err != nil {
			return fail(err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	s.lock, err = root.OpenFile(".omega-publish.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fail(err)
	}
	if err := waitForLock(ctx, s.lock); err != nil {
		return fail(err)
	}
	parent, err := os.Open(filepath.Dir(canonical))
	if err != nil {
		return fail(err)
	}
	if err := errors.Join(parent.Sync(), parent.Close()); err != nil {
		return fail(err)
	}
	return &repositoryStore{store: s, path: canonical}, nil
}

func publicDir(info os.FileInfo) error {
	if !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return errors.New("omega: repository directories must be real directories without group/other write access")
	}
	return owned(info)
}
func (r *repositoryStore) read(name string) ([]byte, error) {
	info, err := r.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return nil, errors.New("omega: repository object must be a regular file without group/other write access")
	}
	if err := owned(info); err != nil {
		return nil, err
	}
	f, err := r.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, errors.New("omega: repository object exceeds limit")
	}
	return data, nil
}
func (r *repositoryStore) empty() (bool, error) {
	f, err := r.root.Open(".")
	if err != nil {
		return false, err
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Name() != ".omega-publish.lock" {
			return false, nil
		}
	}
	return true, nil
}
func (r *repositoryStore) targetsDir() error {
	if err := r.root.Mkdir("targets", 0755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := r.root.Lstat("targets")
	if err != nil {
		return err
	}
	if err := publicDir(info); err != nil {
		return err
	}
	return r.syncDir()
}
func (r *repositoryStore) syncObjectParent(name string) error {
	f, err := r.root.Open(filepath.Dir(name))
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

// Install immutable objects without clobbering existing bytes. Timestamp is
// the only replacing write, and its old bytes are checked under the destination
// lock immediately before the atomic rename.
func (r *repositoryStore) writeObject(obj publicObject, mutable bool) error {
	existing, err := r.read(obj.name)
	if err == nil && bytes.Equal(existing, obj.data) {
		f, err := r.root.Open(obj.name)
		if err != nil {
			return err
		}
		if err := errors.Join(f.Sync(), f.Close()); err != nil {
			return err
		}
		return r.syncObjectParent(obj.name)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && !mutable {
		return fmt.Errorf("omega: refusing to overwrite immutable object %s", obj.name)
	}
	pending := obj.name + ".pending"
	if _, err := r.read(pending); err == nil {
		if err := r.root.Remove(pending); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := r.root.OpenFile(pending, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(obj.data); err != nil {
		return err
	}
	// Ensure a restrictive shell umask does not make published data unreadable
	// by a separate HTTPS serving account; these bytes contain no private keys.
	if err := f.Chmod(0644); err != nil {
		return err
	}
	if err := r.phase("public:" + obj.name + ":written"); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if mutable {
		err = r.root.Rename(pending, obj.name)
	} else {
		err = r.root.Link(pending, obj.name)
	}
	if err != nil {
		return err
	}
	if err := r.phase("public:" + obj.name + ":visible"); err != nil {
		return err
	}
	if err := r.syncObjectParent(obj.name); err != nil {
		return err
	}
	if !mutable {
		if err := r.root.Remove(pending); err != nil {
			return err
		}
		if err := r.syncObjectParent(obj.name); err != nil {
			return err
		}
	}
	return r.phase("public:" + obj.name + ":durable")
}

func openPublication(home *store, bundle bootstrap.Bundle, repo *repositoryStore, create bool) (*store, publicationBinding, error) {
	name := bundle.Network + ".publication"
	if create {
		if err := home.root.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, publicationBinding{}, err
		}
		if err := home.syncDir(); err != nil {
			return nil, publicationBinding{}, err
		}
	}
	state, err := home.subdir(name)
	if err != nil {
		return nil, publicationBinding{}, err
	}
	fail := func(err error) (*store, publicationBinding, error) {
		state.close()
		return nil, publicationBinding{}, err
	}
	data, err := state.read("binding.json")
	if errors.Is(err, os.ErrNotExist) && create {
		entries, err := directoryNames(state)
		if err != nil {
			return fail(err)
		}
		for _, entry := range entries {
			if entry != "binding.json.pending" {
				return fail(errors.New("omega: publication binding missing from established journal; restore it"))
			}
		}
		empty, err := repo.empty()
		if err != nil {
			return fail(err)
		}
		if !empty {
			return fail(errors.New("omega: new publication journal requires an empty repository; restore existing publication history instead of resetting it"))
		}
		data = record(publicationBinding{Schema: 1, Network: bundle.Network, Fingerprint: bundle.Fingerprint(), Directory: repo.path})
		if pending, err := state.read("binding.json.pending"); err == nil && len(pending) > 0 {
			var old publicationBinding
			if decodeRecord(pending, &old) == nil && !bytes.Equal(pending, data) {
				return fail(errors.New("omega: pending publication binding has different configuration"))
			}
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fail(err)
		}
		if err := state.install("binding.json", data); err != nil {
			return fail(err)
		}
	} else if err != nil {
		return fail(fmt.Errorf("omega: publication binding is missing or unreadable; restore the journal: %v", err))
	}
	var binding publicationBinding
	if err := decodeRecord(data, &binding); err != nil || binding.Schema != 1 || binding.Network != bundle.Network || binding.Fingerprint != bundle.Fingerprint() {
		return fail(errors.New("omega: invalid publication journal binding"))
	}
	if repo != nil && binding.Directory != repo.path {
		return fail(errors.New("omega: publication is bound to a different repository directory"))
	}
	return state, binding, nil
}

func directoryNames(s *store) ([]string, error) {
	f, err := s.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Readdirnames(-1)
}
func loadReleases(s *store, bundle bootstrap.Bundle) ([]release, error) {
	names, err := directoryNames(s)
	if err != nil {
		return nil, err
	}
	var numbers []int
	for _, name := range names {
		if !strings.HasSuffix(name, ".release.json") {
			continue
		}
		number, err := strconv.Atoi(strings.TrimSuffix(name, ".release.json"))
		if err != nil || number < 1 || number > maxReleases || name != releaseName(int64(number)) {
			return nil, errors.New("omega: invalid journal version entry")
		}
		numbers = append(numbers, number)
	}
	sort.Ints(numbers)
	releases := make([]release, 0, len(numbers))
	previous := ""
	for i, n := range numbers {
		if n != i+1 {
			return nil, errors.New("omega: publication history is incomplete; restore it before publishing")
		}
		data, err := s.read(releaseName(int64(n)))
		if err != nil {
			return nil, err
		}
		var r release
		if err := decodeRecord(data, &r); err != nil || r.Version != int64(n) || r.Previous != previous {
			return nil, errors.New("omega: publication history integrity failure")
		}
		if _, _, err := r.validate(bundle); err != nil {
			return nil, err
		}
		releases = append(releases, r)
		previous = digest(data)
	}
	return releases, nil
}

// Mutable operator receipts use replace+fsync. A receipt never reserves a
// version or authorizes publication; immutable release records do that.
func (s *store) replaceRecord(name string, data []byte) error {
	if _, err := s.read(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	pending := name + ".pending"
	if _, err := s.read(pending); err == nil {
		if err := s.root.Remove(pending); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := s.root.OpenFile(pending, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := s.root.Rename(pending, name); err != nil {
		return err
	}
	return s.syncDir()
}
