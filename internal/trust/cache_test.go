package trust

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveAndLoadRoundtrip(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	list := &SignedList{
		Version: OmegaVersion,
		Expires: time.Now().Add(1 * time.Hour).Unix(),
		Nodes:   []string{"a:9090", "b:9090"},
	}
	list.Sign(priv)

	dir := t.TempDir()
	if err := SaveCache(dir, list); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := LoadCache(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded nil after save")
	}

	if loaded.Version != list.Version || loaded.Expires != list.Expires {
		t.Errorf("fields differ: %+v vs %+v", loaded, list)
	}
	if len(loaded.Nodes) != len(list.Nodes) {
		t.Errorf("nodes length differ: %d vs %d", len(loaded.Nodes), len(list.Nodes))
	}
	if len(loaded.Signature) != len(list.Signature) {
		t.Errorf("signature length differ")
	}
}

// TestLoadCacheMissing — absent cache file reports (nil, nil) so first-run
// callers can treat it as a normal condition.
func TestLoadCacheMissing(t *testing.T) {
	dir := t.TempDir()
	loaded, err := LoadCache(dir)
	if err != nil {
		t.Fatalf("load missing: %v", err)
	}
	if loaded != nil {
		t.Errorf("expected nil, got %+v", loaded)
	}
}

// TestSaveCacheAtomic — the on-disk file should never contain partial data.
// We can't cleanly simulate a crash, but we can at least check that no temp
// file is left behind after a normal save.
func TestSaveCacheAtomic(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	list := &SignedList{
		Version:   OmegaVersion,
		Expires:   time.Now().Add(time.Hour).Unix(),
		Nodes:     []string{"a:9090"},
		Signature: ed25519.Sign(priv, []byte("unused")),
	}

	dir := t.TempDir()
	if err := SaveCache(dir, list); err != nil {
		t.Fatalf("save: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected exactly 1 file in cache dir, got %v", names)
	}
	if got := entries[0].Name(); got != CacheFileName {
		t.Errorf("cache file name = %q, want %q", got, CacheFileName)
	}
}

func TestDefaultCacheDirEnvWins(t *testing.T) {
	t.Setenv("NODE_CACHE_DIR", "/tmp/explicit")
	if got := DefaultCacheDir(); got != "/tmp/explicit" {
		t.Errorf("NODE_CACHE_DIR override ignored: got %q", got)
	}
}

func TestDefaultCacheDirHomeFallback(t *testing.T) {
	t.Setenv("NODE_CACHE_DIR", "")
	// os.UserHomeDir checks HOME on linux; force a known value.
	t.Setenv("HOME", "/home/fake")
	got := DefaultCacheDir()
	want := filepath.Join("/home/fake", ".sopholeth", "cache")
	if got != want {
		t.Errorf("home fallback = %q, want %q", got, want)
	}
}
