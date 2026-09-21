package trust

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// CacheFileName is the on-disk name for the cached signed list. Callers
// receive a directory, not a file path; cache.Load/Save handle the filename
// so the name can evolve without touching call sites.
const CacheFileName = "root-list.json"

// cachedList is the on-disk representation. We persist the *parsed* list
// (not the raw TXT string) so the reader doesn't have to re-parse on every
// startup. The signature is preserved so a load can be re-verified.
type cachedList struct {
	Version   string   `json:"version"`
	Expires   int64    `json:"expires"`
	Nodes     []string `json:"nodes"`
	Signature []byte   `json:"signature"`
}

// LoadCache reads and returns the signed list stored at
// filepath.Join(dir, CacheFileName). Returns (nil, nil) when the cache file
// is absent — that is the normal first-run case. Any other error is returned.
//
// Callers must still Verify the returned list: the on-disk copy is
// public data, so an attacker with write access to the cache file could
// replace it with another signed list from the same omega version. Verify
// catches an expired or wrong-version swap, and the baked-in pubkey catches
// a forged one.
func LoadCache(dir string) (*SignedList, error) {
	path := filepath.Join(dir, CacheFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read cache %s: %w", path, err)
	}

	var c cachedList
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse cache %s: %w", path, err)
	}
	return &SignedList{
		Version:   c.Version,
		Expires:   c.Expires,
		Nodes:     c.Nodes,
		Signature: c.Signature,
	}, nil
}

// SaveCache writes list to filepath.Join(dir, CacheFileName) atomically by
// writing to a temp file in the same directory and renaming into place. The
// directory is created (with 0700) if it doesn't exist.
func SaveCache(dir string, list *SignedList) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir cache dir %s: %w", dir, err)
	}

	payload, err := json.Marshal(cachedList{
		Version:   list.Version,
		Expires:   list.Expires,
		Nodes:     list.Nodes,
		Signature: list.Signature,
	})
	if err != nil {
		return fmt.Errorf("marshal cache: %w", err)
	}

	tmp, err := os.CreateTemp(dir, CacheFileName+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp cache file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp cache file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp cache file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp cache file: %w", err)
	}

	finalPath := filepath.Join(dir, CacheFileName)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("rename cache file: %w", err)
	}
	cleanup = false
	return nil
}

// DefaultCacheDir resolves NODE_CACHE_DIR > $HOME/.sopholeth/cache >
// /var/cache/sopholeth, as documented in docs/configuration.md.
//
// Callers should prefer ResolveCacheDir, which additionally returns whether
// the fallback was used. The /var/cache/sopholeth path typically requires
// root write access; non-root container deployments will produce refresh
// log spam if they land on it. Operators in that situation should set
// NODE_CACHE_DIR explicitly.
func DefaultCacheDir() string {
	dir, _ := ResolveCacheDir()
	return dir
}

// ResolveCacheDir returns the cache directory and whether the last-resort
// /var/cache/sopholeth fallback was selected. Callers can use the second
// return value to log a one-time startup warning.
func ResolveCacheDir() (dir string, usedLastResort bool) {
	if dir := os.Getenv("NODE_CACHE_DIR"); dir != "" {
		return dir, false
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".sopholeth", "cache"), false
	}
	return "/var/cache/sopholeth", true
}
