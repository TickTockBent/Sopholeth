//go:build linux

package main

import (
	"net/http"
	"net/http/httptest"
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
	if result["state"] != "initialized" || result["publication"] != "not_checked" || result["root_expires"] == nil {
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

func TestOmegaInitConfigurationConflictReport(t *testing.T) {
	ta := newTestApp(t)
	home := filepath.Join(t.TempDir(), "custody")
	args := []string{"--json", "omega", "init", "--home", home, "--network", "rehearsal", "--disposable", "--repository", "https://metadata.example.invalid"}
	out, _ := ta.mustRun(t, "", args...)
	original := decodeJSON(t, out)
	args[len(args)-1] = "https://other.example.invalid"
	code, out, errOut := ta.run("", args...)
	result := decodeJSON(t, out)
	if code != exitError || result["problem"] == nil || !strings.Contains(errOut, result["problem"].(string)) {
		t.Fatalf("conflict must report the failure in JSON and stderr: exit=%d out=%s stderr=%s", code, out, errOut)
	}
	if result["fingerprint"] != original["fingerprint"] || result["repository"] != original["repository"] {
		t.Fatal("conflict report changed the existing authority's identity")
	}
	if action, _ := result["action"].(string); !strings.Contains(action, "--repository") || !strings.Contains(action, "do not replace") {
		t.Fatalf("conflict action does not explain how to proceed: %s", out)
	}
}

func TestOmegaStatusUninitializedAndInvalidReports(t *testing.T) {
	for _, kind := range []string{"missing-home", "missing-parent", "missing-network", "pending", "damaged", "unsafe-home"} {
		t.Run(kind, func(t *testing.T) {
			ta := newTestApp(t)
			home := filepath.Join(t.TempDir(), "custody")
			state, action := "absent", "Initialize this network with soph omega init."
			if kind == "missing-parent" {
				home = filepath.Join(home, "nested")
			} else if kind == "damaged" {
				ta.mustRun(t, "", "omega", "init", "--home", home, "--network", "rehearsal", "--repository", "https://metadata.example.invalid", "--disposable")
				if err := os.Remove(filepath.Join(home, "rehearsal", "bundle.json")); err != nil {
					t.Fatal(err)
				}
				state, action = "invalid", "restore verified recovery material"
			} else if kind != "missing-home" {
				if err := os.Mkdir(home, 0700); err != nil {
					t.Fatal(err)
				}
				if kind == "pending" {
					if err := os.Mkdir(filepath.Join(home, ".rehearsal.pending"), 0700); err != nil {
						t.Fatal(err)
					}
					state, action = "pending", "Rerun the same soph omega init command"
				} else if kind == "unsafe-home" {
					if err := os.Chmod(home, 0755); err != nil {
						t.Fatal(err)
					}
					state, action = "invalid", "restore verified recovery material"
				}
			}
			args := []string{"omega", "status", "--home", home, "--network", "rehearsal"}
			code, out, _ := ta.run("", append([]string{"--json"}, args...)...)
			result := decodeJSON(t, out)
			if code != exitError || result["state"] != state || result["problem"] == nil {
				t.Fatalf("unexpected status: exit=%d out=%s", code, out)
			}
			if next, _ := result["action"].(string); !strings.Contains(next, action) {
				t.Fatalf("wrong operator action: %s", out)
			}
			if _, exists := result["root_expires"]; exists {
				t.Fatalf("unknown root expiration must be omitted: %s", out)
			}
			code, out, _ = ta.run("", args...)
			if code != exitError || !strings.Contains(out, action) {
				t.Fatalf("text status has the wrong operator action: %s", out)
			}
			if kind == "missing-home" || kind == "missing-parent" {
				if _, err := os.Lstat(home); !os.IsNotExist(err) {
					t.Fatal("status created the missing custody home")
				}
			}
		})
	}
}

func TestOmegaHelpAndUsage(t *testing.T) {
	ta := newTestApp(t)
	for _, args := range [][]string{{"help", "omega"}, {"omega", "--help"}, {"omega", "init", "--help"}, {"omega", "provision-renewal", "--help"}, {"omega", "publish", "--help"}, {"omega", "status", "--help"}} {
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

func TestOmegaPublishCLIAndVerifiedStatus(t *testing.T) {
	ta := newTestApp(t)
	base := t.TempDir()
	home := filepath.Join(base, "custody")
	repository := filepath.Join(base, "repository")
	server := httptest.NewTLSServer(http.FileServer(http.Dir(repository)))
	defer server.Close()
	ta.app.newHTTPClient = server.Client
	ta.mustRun(t, "", "omega", "init", "--home", home, "--network", "rehearsal", "--repository", server.URL, "--disposable")
	manifest := filepath.Join(base, "manifest.json")
	raw := `{"schema":1,"network":"rehearsal","enclave":"default","roots":[{"id":"a","origin":"https://a.example.invalid"},{"id":"b","origin":"https://b.example.invalid"},{"id":"c","origin":"https://c.example.invalid"}]}`
	if err := os.WriteFile(manifest, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--json", "omega", "publish", "--home", home, "--network", "rehearsal", "--manifest", manifest, "--repository-dir", repository, "--version", "1", "--disposable"}
	out, _ := ta.mustRun(t, "", args...)
	result := decodeJSON(t, out)
	if result["publication"] != "verified" || result["release"].(map[string]any)["version"] != float64(1) {
		t.Fatalf("unexpected publication: %s", out)
	}
	out, _ = ta.mustRun(t, "", "omega", "status", "--home", home, "--network", "rehearsal", "--verify")
	if !strings.Contains(out, "Publication: verified") || !strings.Contains(out, "Release: 1") || !strings.Contains(out, "Node root: a") || !strings.Contains(out, "timestamp expires:") {
		t.Fatalf("missing publication information: %s", out)
	}
	if err := os.Remove(filepath.Join(repository, "1.snapshot.json")); err != nil {
		t.Fatal(err)
	}
	code, out, _ := ta.run("", "--json", "omega", "status", "--home", home, "--network", "rehearsal", "--verify")
	if code != exitError || decodeJSON(t, out)["publication"] != "failed" {
		t.Fatalf("false verification success: %d %s", code, out)
	}
	out, _ = ta.mustRun(t, "", args...)
	if decodeJSON(t, out)["publication"] != "verified" {
		t.Fatal("retry did not repair missing immutable object")
	}
	if _, err := os.Stat(ta.configPath); !os.IsNotExist(err) {
		t.Fatal("publisher accessed client profiles")
	}
}

func TestOmegaRenewalCLI(t *testing.T) {
	ta := newTestApp(t)
	base := t.TempDir()
	home, online, repository := filepath.Join(base, "offline"), filepath.Join(base, "online"), filepath.Join(base, "repository")
	server := httptest.NewTLSServer(http.FileServer(http.Dir(repository)))
	defer server.Close()
	ta.app.newHTTPClient = server.Client
	ta.mustRun(t, "", "omega", "init", "--home", home, "--network", "rehearsal", "--repository", server.URL, "--disposable")
	// Provisioning before the first approval is also supported.
	ta.mustRun(t, "", "omega", "provision-renewal", "--home", home, "--network", "rehearsal", "--renewal-home", online, "--disposable")
	manifest := filepath.Join(base, "manifest.json")
	if err := os.WriteFile(manifest, []byte(`{"schema":1,"network":"rehearsal","enclave":"default","roots":[{"id":"a","origin":"https://a.example.invalid"},{"id":"b","origin":"https://b.example.invalid"},{"id":"c","origin":"https://c.example.invalid"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	ta.mustRun(t, "", "omega", "publish", "--home", home, "--network", "rehearsal", "--manifest", manifest, "--repository-dir", repository, "--version", "1", "--disposable")
	if err := os.Rename(home, home+"-unmounted"); err != nil {
		t.Fatal(err)
	}
	args := []string{"--json", "omega", "publish", "--home", online, "--network", "rehearsal", "--renew", "--disposable"}
	out, _ := ta.mustRun(t, "", args...)
	r := decodeJSON(t, out)
	if r["state"] != "renewal_ready" || r["publication"] != "verified" || r["operational_home"] != online {
		t.Fatalf("unexpected renewal output: %s", out)
	}
	publication := r["release"].(map[string]any)
	if publication["version"] != float64(1) || publication["renew_after"] == nil || publication["renewal_due"] != false {
		t.Fatalf("renewed before due or omitted schedule: %s", out)
	}
	out, _ = ta.mustRun(t, "", "omega", "status", "--home", online, "--network", "rehearsal", "--verify")
	if !strings.Contains(out, "Renew after:") || !strings.Contains(out, "Publication: verified") {
		t.Fatalf("missing renewal status: %s", out)
	}
	for _, extra := range [][]string{{"--manifest", manifest}, {"--repository-dir", repository}, {"--version", "2"}} {
		if code, _, _ := ta.run("", append(append([]string{}, args...), extra...)...); code != exitUsage {
			t.Fatal("renewal accepted manual approval flags")
		}
	}
	if _, err := os.Stat(ta.configPath); !os.IsNotExist(err) {
		t.Fatal("renewal accessed client profiles")
	}
}

func TestOmegaRenewalUnavailableReports(t *testing.T) {
	for _, kind := range []string{"missing-home", "missing-parent", "unprovisioned", "missing-custody", "corrupt-custody", "unsafe-home"} {
		t.Run(kind, func(t *testing.T) {
			ta := newTestApp(t)
			home := filepath.Join(t.TempDir(), "online")
			state, action := "absent", "provision-renewal"
			if kind == "missing-parent" {
				home = filepath.Join(home, "nested")
			} else if kind != "missing-home" {
				if err := os.Mkdir(home, 0700); err != nil {
					t.Fatal(err)
				}
				state = "unprovisioned"
				switch kind {
				case "missing-custody":
					if err := os.Mkdir(filepath.Join(home, "rehearsal.publication"), 0700); err != nil {
						t.Fatal(err)
					}
					state, action = "invalid", "restore"
				case "corrupt-custody":
					if err := os.WriteFile(filepath.Join(home, "rehearsal.renewal.json"), []byte(`{"schema":`), 0600); err != nil {
						t.Fatal(err)
					}
					state, action = "invalid", "restore"
				case "unsafe-home":
					if err := os.Chmod(home, 0755); err != nil {
						t.Fatal(err)
					}
					state, action = "invalid", "permissions"
				}
			}
			args := []string{"omega", "publish", "--renew", "--home", home, "--network", "rehearsal", "--disposable"}
			code, out, errOut := ta.run("", append([]string{"--json"}, args...)...)
			if code != exitError || out == "" {
				t.Fatalf("missing error report: exit=%d stdout=%q stderr=%s", code, out, errOut)
			}
			r := decodeJSON(t, out)
			problem, _ := r["problem"].(string)
			next, _ := r["action"].(string)
			if r["state"] != state || r["publication"] != "failed" || r["network"] != "rehearsal" || r["operational_home"] != home || problem == "" || !strings.Contains(errOut, problem) || !strings.Contains(next, action) {
				t.Fatalf("unexpected renewal failure: %s stderr=%s", out, errOut)
			}
			code, out, _ = ta.run("", args...)
			if code != exitError || !strings.Contains(out, next) {
				t.Fatalf("text report omitted action: %s", out)
			}
			if kind == "missing-home" || kind == "missing-parent" {
				if _, err := os.Lstat(home); !os.IsNotExist(err) {
					t.Fatal("renewal created the missing home")
				}
			}
		})
	}
}
