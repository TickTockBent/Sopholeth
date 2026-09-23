//go:build linux && omega_hosted_rehearsal

package omega

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sopholeth/internal/trust/bootstrap"
)

// Explicitly invoked operator rehearsal, excluded from the ordinary suite.
// It deploys disposable metadata to the configured project. State is retained
// at the supplied home so a failed operation can be inspected and retried.
func TestHostedVercelRehearsal(t *testing.T) {
	base, repository, stage := os.Getenv("OMEGA_HOSTED_HOME"), os.Getenv("OMEGA_HOSTED_REPOSITORY"), os.Getenv("OMEGA_HOSTED_STAGE")
	if !filepath.IsAbs(base) || !strings.HasPrefix(repository, "https://sopholeth.io/omega/rehearsal-") {
		t.Fatal("set an absolute OMEGA_HOSTED_HOME and a unique /omega/rehearsal-... repository")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	configPath, err := filepath.Abs("../../docs/examples/omega/vercel.json")
	must(t, err)
	config, err := ParseVercelConfig(file(t, configPath))
	must(t, err)
	binary, err := filepath.Abs("../../bin/soph")
	must(t, err)
	home, online := filepath.Join(base, "offline"), filepath.Join(base, "online")
	save := func(name string, value any) {
		t.Helper()
		must(t, os.WriteFile(filepath.Join(base, name+".json"), record(value), 0600))
	}
	cli := func(name string, args ...string) Report {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, append([]string{"--json", "--timeout", "5m", "omega"}, args...)...)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: %v; public report: %s", name, err, out)
		}
		var report Report
		must(t, json.Unmarshal(out, &report))
		save(name, report)
		t.Logf("%s: state=%s publication=%s", name, report.State, report.Publication)
		return report
	}
	if stage == "publish" {
		created := time.Now().UTC().Truncate(time.Second).Add(-25 * time.Hour)
		r, err := initialize(ctx, InitOptions{Home: home, Network: "rehearsal", Repository: repository, Disposable: true}, created, nil)
		must(t, err)
		save("initialization", r)
		must(t, os.WriteFile(filepath.Join(base, "bundle.json"), file(t, filepath.Join(home, "rehearsal", "bundle.json")), 0600))
		r, err = publish(ctx, PublishOptions{Home: home, Network: "rehearsal", Directory: filepath.Join(base, "repository"), Manifest: publicationManifest, Version: 1, Disposable: true,
			Vercel: &VercelOptions{Config: config, Token: os.Getenv("VERCEL_TOKEN")}}, created, nil)
		save("publication", r)
		must(t, err)
		if r.Publication != "verified" {
			t.Fatalf("unverified publication: %+v", r)
		}
		cli("provision", "provision-renewal", "--home", home, "--network", "rehearsal", "--renewal-home", online, "--disposable")
	} else if stage == "renew" {
		// The real command must succeed with offline custody unavailable.
		held := home + ".unavailable"
		must(t, os.Rename(home, held))
		defer func() { must(t, os.Rename(held, home)) }()
		r := cli("renewal", "publish", "--home", online, "--network", "rehearsal", "--renew", "--disposable", "--vercel-config", configPath)
		if r.Publication != "verified" || r.Release == nil || r.Release.Version != 2 || r.Release.Versions.Targets != 1 {
			t.Fatalf("unexpected renewal: %+v", r)
		}
		receiptPath := filepath.Join(online, "rehearsal.publication", vercelDeploymentName)
		before := string(file(t, receiptPath))
		cli("hourly-retry", "publish", "--home", online, "--network", "rehearsal", "--renew", "--disposable", "--vercel-config", configPath)
		if string(file(t, receiptPath)) != before {
			t.Fatal("hourly retry replaced deployment receipt")
		}
	} else if stage == "rotate" {
		r := cli("rotation-prepared", "rotate", "--home", home, "--network", "rehearsal", "--root-version", "2", "--disposable")
		if r.Rotation == nil || r.Rotation.Role != "online" || r.Rotation.RootVersion != 2 || r.Rotation.RootSHA256 == "" {
			t.Fatalf("unexpected rotation preparation: %+v", r)
		}
		for _, role := range []string{"snapshot", "timestamp"} {
			if len(r.Rotation.Keys[role]) != 1 || len(r.Rotation.Replaces[role]) != 1 || r.Rotation.Keys[role][0] == r.Rotation.Replaces[role][0] {
				t.Fatalf("%s key was not replaced", role)
			}
		}
		r = cli("rotation-applied", "rotate", "--home", home, "--network", "rehearsal", "--root-version", "2", "--apply", r.Rotation.RootSHA256, "--disposable", "--vercel-config", configPath)
		if r.Publication != "verified" || r.RootVersion != 2 {
			t.Fatalf("unverified rotation: %+v", r)
		}
	} else if stage != "verify" {
		t.Fatal("OMEGA_HOSTED_STAGE must be publish, renew, rotate, or verify")
	}
	r := cli("status-"+stage, "status", "--home", online, "--network", "rehearsal", "--verify")
	if r.Publication != "verified" {
		t.Fatalf("unverified status: %+v", r)
	}
	// A new client starts from root 1; the returning client keeps its prior
	// checkpoint across stages and must accept renewal and root transitions.
	bundle, err := bootstrap.ParseBundle(file(t, filepath.Join(base, "bundle.json")))
	must(t, err)
	for _, name := range []string{"returning", "fresh-" + stage} {
		client, err := bootstrap.New(ctx, bootstrap.Config{Bundle: bundle, StateDir: filepath.Join(base, name)})
		must(t, err)
		view, err := client.Refresh(ctx)
		must(t, err)
		if view.Versions != r.Release.Versions {
			t.Fatalf("client %s has versions %+v, expected %+v", name, view.Versions, r.Release.Versions)
		}
		save(name+"-"+stage, view)
		t.Logf("%s accepted %+v", name, view.Versions)
	}
}
