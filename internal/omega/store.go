package omega

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type store struct {
	root *os.Root
	lock *os.File
	hook func(string) error
}

// One lock and one immutable authority slot per network within the custody
// home. Staging is never a usable authority and is not returned to callers.
func openHome(ctx context.Context, home, network string, create bool) (*store, error) {
	if err := supportedPlatform(); err != nil {
		return nil, err
	}
	if home == "" {
		return nil, errors.New("omega: custody home is required")
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return nil, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return nil, err
	}
	abs = filepath.Join(parent, filepath.Base(abs))
	for path := parent; ; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if err := safeAncestor(info); err != nil {
			return nil, fmt.Errorf("omega: unsafe custody ancestor %s: %w", path, err)
		}
		if filepath.Dir(path) == path {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if create {
		if err := os.Mkdir(abs, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	if err := safeDir(info); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	s := &store{root: root}
	fail := func(err error) (*store, error) { s.close(); return nil, err }
	name := network + ".lock"
	if info, err := root.Lstat(name); err == nil {
		if err := safeFile(info); err != nil {
			return fail(err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	s.lock, err = root.OpenFile(name, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fail(err)
	}
	if err := waitForLock(ctx, s.lock); err != nil {
		return fail(err)
	}
	if create {
		f, err := os.Open(parent)
		if err != nil {
			return fail(err)
		}
		err = errors.Join(f.Sync(), f.Close())
		if err != nil {
			return fail(err)
		}
		if err := s.syncDir(); err != nil {
			return fail(err)
		}
	}
	return s, nil
}

func waitForLock(ctx context.Context, file *os.File) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		locked, err := tryLock(file)
		if err != nil {
			return err
		}
		if locked {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (s *store) close() {
	if s.lock != nil {
		_ = s.lock.Close()
	}
	if s.root != nil {
		_ = s.root.Close()
	}
}

func safeDir(info os.FileInfo) error {
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("omega: custody directories must have mode 0700 and cannot be symlinks")
	}
	return owned(info)
}
func safeFile(info os.FileInfo) error {
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return errors.New("omega: authority files must be regular files with mode 0600, not symlinks")
	}
	return owned(info)
}
func (s *store) read(name string) ([]byte, error) {
	info, err := s.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if err := safeFile(info); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	f, err := s.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("omega: %s exceeds size limit", name)
	}
	return data, nil
}
func (s *store) subdir(name string) (*store, error) {
	info, err := s.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if err := safeDir(info); err != nil {
		return nil, err
	}
	root, err := s.root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	return &store{root: root, hook: s.hook}, nil
}
func (s *store) phase(name string) error {
	if s.hook != nil {
		return s.hook(name)
	}
	return nil
}
func (s *store) syncDir() error {
	f, err := s.root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

// install is only used inside private staging. Committed files are immutable;
// incomplete temporary output may be reconstructed from the durable authority
// record. The authority record itself is committed before any signing occurs.
func (s *store) install(name string, data []byte) error {
	if existing, err := s.read(name); err == nil {
		if !bytes.Equal(existing, data) {
			return fmt.Errorf("omega: refusing to replace %s", name)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	pending := name + ".pending"
	existing, readErr := s.read(pending)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	reuse := readErr == nil && bytes.Equal(existing, data)
	if readErr == nil && !reuse {
		if err := s.root.Remove(pending); err != nil {
			return err
		}
	}
	flags := os.O_CREATE | os.O_EXCL | os.O_WRONLY
	if reuse {
		flags = os.O_WRONLY
	}
	f, err := s.root.OpenFile(pending, flags, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if !reuse {
		if _, err := f.Write(data); err != nil {
			return err
		}
	}
	if err := s.phase(name + ":written"); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := s.phase(name + ":synced"); err != nil {
		return err
	}
	if err := s.root.Link(pending, name); err != nil {
		return err
	}
	if err := s.phase(name + ":linked"); err != nil {
		return err
	}
	if err := s.syncDir(); err != nil {
		return err
	}
	if err := s.phase(name + ":durable"); err != nil {
		return err
	}
	if err := s.root.Remove(pending); err != nil {
		return err
	}
	return s.syncDir()
}

// cleanPending runs only after every committed file verifies. Pending outputs
// are never authoritative; their bytes may be torn by a process/power failure.
func (s *store) cleanPending() error {
	for _, name := range authorityFiles {
		if _, err := s.read(name + ".pending"); err == nil {
			if err := s.root.Remove(name + ".pending"); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return s.syncDir()
}
