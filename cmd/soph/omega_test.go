//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOmegaCLI(t *testing.T) {
	ta := newTestApp(t)
	home := filepath.Join(t.TempDir(), "custody")
	args := []string{"--json", "omega", "init", "--home", home, "--network", "rehearsal", "--repository", "https://metadata.example.invalid", "--disposable"}
	out, _ := ta.mustRun(t, "", args...)
	result := decodeJSON(t, out)
	if result["state"] != "initialized" || result["publication"] != "not_checked" {
		t.Fatal("unexpected init output")
	}
	out, _ = ta.mustRun(t, "", "--json", "omega", "status", "--home", home, "--network", "rehearsal")
	if decodeJSON(t, out)["fingerprint"] != result["fingerprint"] {
		t.Fatal("status changed authority identity")
	}
	out, _ = ta.mustRun(t, "", args...)
	if decodeJSON(t, out)["fingerprint"] != result["fingerprint"] {
		t.Fatal("repeat init changed authority identity")
	}
	if _, err := os.Stat(ta.configPath); !os.IsNotExist(err) {
		t.Fatal("omega accessed client profile")
	}
	if err := os.Remove(filepath.Join(home, "rehearsal", "bundle.json")); err != nil {
		t.Fatal(err)
	}
	code, out, _ := ta.run("", "--json", "omega", "status", "--home", home, "--network", "rehearsal")
	if code != exitError || decodeJSON(t, out)["state"] != "invalid" {
		t.Fatal("status did not report corrupted authority")
	}
}

func TestOmegaHelpAndUsage(t *testing.T) {
	ta := newTestApp(t)
	for _, args := range [][]string{{"help", "omega"}, {"omega", "--help"}, {"omega", "init", "--help"}, {"omega", "status", "--help"}} {
		out, _ := ta.mustRun(t, "", args...)
		if !strings.Contains(out, "Usage:") {
			t.Fatalf("missing help for %v", args)
		}
	}
	for _, args := range [][]string{{"omega", "publish"}, {"omega", "init"}, {"omega", "status"}, {"omega", "init", "--home", "/unused", "--network", "test", "--repository", "https://example.invalid"}, {"--network", "client-profile", "omega", "status", "--home", "/unused", "--network", "test"}} {
		if code, _, _ := ta.run("", args...); code != exitUsage {
			t.Fatalf("usage: %v got %d", args, code)
		}
	}
}

func TestOmegaOutputFailureDoesNotUndoOrReplaceAuthority(t *testing.T) {
	ta := newTestApp(t)
	home := filepath.Join(t.TempDir(), "custody")
	args := []string{"--json", "omega", "init", "--home", home, "--network", "rehearsal", "--repository", "https://metadata.example.invalid", "--disposable"}
	ta.app.stdout = failingWriter{}
	if code, _, _ := ta.run("", args...); code != exitError {
		t.Fatal("stdout failure was not reported")
	}
	path := filepath.Join(home, "rehearsal", "authority.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("authority did not commit before output failure")
	}
	ta.app.stdout = ta.stdout
	out, _ := ta.mustRun(t, "", args...)
	if decodeJSON(t, out)["state"] != "initialized" {
		t.Fatal("retry did not recognize completed authority")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("output failure caused new authority creation")
	}
}
