package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

var ErrState = errors.New("bootstrap: invalid or incomplete trust state; explicit recovery is required")

type accepted struct {
	Metadata map[string][]byte `json:"metadata"`
	Manifest []byte            `json:"manifest"`
}

type state struct {
	Schema      int               `json:"schema"`
	Network     string            `json:"network"`
	Repository  string            `json:"repository"`
	Fingerprint string            `json:"fingerprint"`
	Metadata    map[string][]byte `json:"metadata"`
	Accepted    *accepted         `json:"accepted,omitempty"`
}

// The checksum detects accidental damage, not malicious edits by the owner.
// TUF signatures are reverified before cached authority is returned.
type envelope struct {
	Payload []byte `json:"payload"`
	SHA256  string `json:"sha256"`
}

type store struct {
	root *os.Root
	lock *os.File
	hook func(string) error // Fault injection at durable-write boundaries, tests only.
}

func openStore(ctx context.Context, dir string, create bool) (*store, bool, error) {
	created := false
	if create {
		// The caller supplies an existing parent; do not silently construct a
		// hierarchy whose permissions/custody have never been checked.
		err := os.Mkdir(dir, 0700)
		created = err == nil
		if err != nil && !errors.Is(err, os.ErrExist) {
			return nil, false, err
		}
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrState, err)
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, false, fmt.Errorf("%w: state directory must be a private directory (0700), not a symlink", ErrState)
	}
	if err := checkOwner(info); err != nil {
		return nil, false, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, false, err
	}
	s := &store{root: root}
	if info, err := root.Lstat("lock"); err == nil {
		if err := privateFile(info); err != nil {
			s.close()
			return nil, false, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		s.close()
		return nil, false, err
	}
	s.lock, err = root.OpenFile("lock", os.O_CREATE|os.O_RDWR, 0600)
	if err == nil {
		err = lockContext(ctx, s.lock)
	}
	if err != nil {
		s.close()
		return nil, false, err
	}
	if created {
		// Persist the directory entry as well as subsequent contents.
		parent, err := os.Open(filepath.Dir(dir))
		if err != nil {
			s.close()
			return nil, false, err
		}
		err = parent.Sync()
		closeErr := parent.Close()
		if err = errors.Join(err, closeErr); err != nil {
			s.close()
			return nil, false, err
		}
	}
	return s, created, nil
}

func (s *store) close() {
	if s.lock != nil {
		_ = s.lock.Close() // Closing releases the OS lock, including after process exit.
	}
	_ = s.root.Close()
}

func privateFile(info os.FileInfo) error {
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("%w: state files must be private regular files", ErrState)
	}
	return checkOwner(info)
}

func readLimited(file io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = errors.New("bootstrap: file exceeds size limit")
	}
	return data, err
}

func (s *store) load(bundle Bundle) (*state, error) {
	info, err := s.root.Lstat("state.json")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrState, err)
	}
	if err := privateFile(info); err != nil {
		return nil, err
	}
	f, err := s.root.Open("state.json")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := readLimited(f, stateLimit)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrState, err)
	}
	var outer envelope
	if err := strictJSON(data, &outer); err != nil || fmt.Sprintf("%x", sha256.Sum256(outer.Payload)) != outer.SHA256 {
		return nil, fmt.Errorf("%w: state checksum or encoding", ErrState)
	}
	var st state
	if err := strictJSON(outer.Payload, &st); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrState, err)
	}
	if st.Schema != 1 || st.Network != bundle.Network || st.Repository != bundle.Repository || st.Fingerprint != bundle.Fingerprint() {
		return nil, fmt.Errorf("%w: state belongs to a different bundle or network", ErrState)
	}
	if err := checkMetadata(st.Metadata); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrState, err)
	}
	if st.Accepted != nil {
		if err := checkMetadata(st.Accepted.Metadata); err != nil || len(st.Accepted.Manifest) > manifestLimit {
			return nil, fmt.Errorf("%w: invalid accepted manifest state", ErrState)
		}
	}
	return &st, nil
}

func checkMetadata(files map[string][]byte) error {
	for name, data := range files {
		if name != "root" && name != "timestamp" && name != "snapshot" && name != "targets" {
			return errors.New("unsupported cached metadata role")
		}
		if len(data) == 0 || len(data) > metadataLimit || !json.Valid(data) {
			return errors.New("invalid cached metadata")
		}
	}
	return validateRoot(files["root"])
}

func (s *store) save(st *state) error {
	payload, err := json.Marshal(st)
	if err != nil {
		return err
	}
	data, err := json.Marshal(envelope{Payload: payload, SHA256: fmt.Sprintf("%x", sha256.Sum256(payload))})
	if err != nil {
		return err
	}
	if len(data) > stateLimit {
		return errors.New("bootstrap: state exceeds size limit")
	}
	// A pending file is never trusted after interruption. The established
	// state remains the only source of truth until the atomic rename.
	if err := s.root.Remove("state.pending"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := s.root.OpenFile("state.pending", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = s.phase("written"); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = s.phase("synced"); err != nil {
		return err
	}
	if err = s.root.Rename("state.pending", "state.json"); err != nil {
		return err
	}
	if err = s.phase("renamed"); err != nil {
		return err
	}
	dir, err := s.root.Open(".")
	if err != nil {
		return err
	}
	err = dir.Sync()
	closeErr := dir.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return err
	}
	return s.phase("committed")
}

func (s *store) phase(name string) error {
	if s.hook != nil {
		return s.hook(name)
	}
	return nil
}

func lockContext(ctx context.Context, f *os.File) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		locked, err := tryLock(f)
		if err != nil || locked {
			return err
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
