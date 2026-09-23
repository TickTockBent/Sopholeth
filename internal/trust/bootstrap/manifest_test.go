package bootstrap

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestManifestValidation(t *testing.T) {
	for _, raw := range []string{
		string(manifest) + ` {}`,
		strings.Replace(string(manifest), `"schema":1`, `"schema":1,"schema":2`, 1),
		strings.Replace(string(manifest), `"schema":1`, `"schema":1,"unknown":true`, 1),
		strings.Replace(string(manifest), `"schema":1`, `"schema":2`, 1),
		strings.Replace(string(manifest), `"disposable"`, `"different"`, 1),
		strings.Replace(string(manifest), `"default"`, `"other"`, 1),
		strings.Replace(string(manifest), `"id":"two"`, `"id":"one"`, 1),
		strings.Replace(string(manifest), `https://two.example.invalid`, `https://ONE.example.invalid:443/`, 1),
		strings.Replace(string(manifest), `https://one.example.invalid`, `http://one.example.invalid`, 1),
		strings.Replace(string(manifest), `https://one.example.invalid`, `https://user@one.example.invalid`, 1),
		`null`, `{}`, strings.Repeat(" ", manifestLimit+1),
	} {
		if _, err := ParseManifest([]byte(raw), "disposable"); err == nil {
			t.Errorf("accepted invalid manifest: %.120s", raw)
		}
	}
	got, err := ParseManifest(manifest, "disposable")
	check(t, err)
	if len(got.Roots) != 3 || got.Roots[0].ID != "one" {
		t.Fatalf("bad manifest: %+v", got)
	}
}

func TestHTTPSOriginValidation(t *testing.T) {
	for _, raw := range []string{
		"http://host", "https://user:secret@host", "https://host/a", "https://host/?x=1",
		"https://host?", "https://host#", "https://host/#x", "https://host/%2f", "https://host:",
		"https://host:0", "https://host:65536", "https://host:abc", "https://", "https://host.",
		"https://bad_name", "https://-bad", "https://host\\evil", "https://[fe80::1%25eth0]",
	} {
		if got, err := httpsOrigin(raw); err == nil {
			t.Errorf("accepted %q as %q", raw, got)
		}
	}
	for raw, want := range map[string]string{
		"https://EXAMPLE.invalid:443/": "https://example.invalid",
		"https://example.invalid:0443": "https://example.invalid",
		"https://127.0.0.1:8181":       "https://127.0.0.1:8181",
		"https://[::1]/":               "https://[::1]",
	} {
		got, err := httpsOrigin(raw)
		check(t, err)
		if got != want {
			t.Errorf("%s: got %s, want %s", raw, got, want)
		}
	}
}

func TestBundleValidationAndStableFingerprint(t *testing.T) {
	r := newRepository(t)
	bundle := r.bundle()
	var pretty bytes.Buffer
	check(t, json.Indent(&pretty, bundle.Root, "", "  "))
	bundle.Root = pretty.Bytes()
	data, err := json.Marshal(bundle)
	check(t, err)
	parsed, err := ParseBundle(data)
	check(t, err)
	if parsed.Fingerprint() != r.bundle().Fingerprint() {
		t.Fatal("bundle formatting changed fingerprint")
	}
	for _, root := range [][]byte{nil, []byte("{}"), []byte("null"), []byte(strings.Repeat("x", metadataLimit+1))} {
		bundle.Root = root
		if err := bundle.Validate(); err == nil {
			t.Fatal("accepted unconfigured or invalid trust anchor")
		}
	}
	bundle = r.bundle()
	bundle.Repository = "http://example.invalid"
	if err := bundle.Validate(); err == nil {
		t.Fatal("accepted HTTP repository")
	}
}

func TestRepositoryLocationValidation(t *testing.T) {
	for raw, want := range map[string]string{
		"https://EXAMPLE.invalid:443/":                          "https://example.invalid",
		"https://EXAMPLE.invalid:0443/omega/":                   "https://example.invalid/omega",
		"https://example.invalid/omega":                         "https://example.invalid/omega",
		"https://example.invalid/networks/public.v1/~metadata/": "https://example.invalid/networks/public.v1/~metadata",
		"https://[::1]:8181/omega/":                             "https://[::1]:8181/omega",
	} {
		got, err := ValidateLocation("test", raw)
		check(t, err)
		if got != want {
			t.Errorf("%q: got %q, want %q", raw, got, want)
		}
	}
	for _, raw := range []string{
		"http://host/omega", "https://user:secret@host/omega", "https://host/omega?", "https://host/omega#",
		"https://host/omega?key=value", "https://host/omega#fragment", "https://host//", "https://host/omega//",
		"https://host/omega/./v1", "https://host/omega/../other", "https://host/omega/.", "https://host/omega/..",
		"https://host/%6fmega", "https://host/omega/%2e%2e/other", "https://host/omega%2fother", "https://host/omega%252fother",
		"https://host/omega\\other", "https://host/omega;other", "https://host/omega space", "https://host/oméga",
		"https://host:0/omega", "https://host:/omega", "https://[fe80::1%25eth0]/omega", "//host/omega", "/omega/",
	} {
		if got, err := ValidateLocation("test", raw); err == nil {
			t.Errorf("accepted %q as %q", raw, got)
		}
	}
}
