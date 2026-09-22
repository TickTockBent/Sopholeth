package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"sopholeth/internal/omega"
)

func (a *app) cmdOmega(ctx context.Context, args []string) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help")) {
		return a.printf("Usage: soph [global flags] omega <init|provision-renewal|publish|rotate|status> [flags]\n\n  init     Atomically initialize a disposable authority, or recover the same transaction.\n  provision-renewal  Provision online keys and hand off the publication journal.\n  publish  Publish an approved release, or --renew using online keys, and verify HTTPS.\n  rotate   Prepare an online or membership-key transition; --apply its reviewed root digest.\n  status   Inspect local authority and publication state; --verify checks HTTPS.\n\nUse 'soph omega <command> --help' for flags.\n")
	}
	cmd := args[0]
	if cmd != "init" && cmd != "status" && cmd != "publish" && cmd != "provision-renewal" && cmd != "rotate" {
		return usagef("unknown omega command %q; run 'soph omega --help'", cmd)
	}
	fs := flag.NewFlagSet("omega "+cmd, flag.ContinueOnError)
	home := fs.String("home", "", "offline authority home, or operational home for --renew/status (required)")
	network := fs.String("network", "", "authority network identity (required; independent of client profiles)")
	var repository string
	var disposable bool
	var manifestPath, directory string
	var version int64
	var verify, renew bool
	var renewalHome string
	var rootVersion int64
	var apply string
	var rotationRole string
	if cmd == "init" || cmd == "publish" || cmd == "provision-renewal" || cmd == "rotate" {
		fs.BoolVar(&disposable, "disposable", false, "use development/rehearsal keys; production custody is not implemented")
	}
	if cmd == "init" {
		fs.StringVar(&repository, "repository", "", "HTTPS metadata repository origin (required; base paths are planned)")
	} else if cmd == "provision-renewal" {
		fs.StringVar(&renewalHome, "renewal-home", "", "separate private operational home for online keys and publication state (required)")
	} else if cmd == "rotate" {
		fs.StringVar(&rotationRole, "role", "online", "keys to replace: online (snapshot and timestamp) or targets (offline membership key)")
		fs.Int64Var(&rootVersion, "root-version", 0, "successor root number, or the same number for retry (required; starts at 2)")
		fs.StringVar(&apply, "apply", "", "apply a previously prepared transition by its exact root_sha256; omit to prepare/review")
	} else if cmd == "publish" {
		fs.BoolVar(&renew, "renew", false, "renew due freshness or recover/verify the current release using only online custody")
		fs.StringVar(&manifestPath, "manifest", "", "approved three-root manifest JSON file (required)")
		fs.StringVar(&directory, "repository-dir", "", "dedicated public directory served at the authority's HTTPS origin (required)")
		fs.Int64Var(&version, "version", 0, "release number: latest for retry, next for new approval (required; starts at 1)")
	} else {
		fs.BoolVar(&verify, "verify", false, "verify the latest prepared release through HTTPS, recording the result")
	}
	pos, err := parseInterspersed(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 0 || *home == "" || *network == "" {
		return usagef("omega %s requires --home and --network, with no positional arguments", cmd)
	}
	if a.networkFlag != "" {
		return usagef("omega uses its own --network after the subcommand, not the global client-profile flag")
	}
	opCtx, cancel := a.requestContext(ctx)
	defer cancel()
	var report omega.Report
	if cmd == "init" {
		if repository == "" || !disposable {
			return usagef("omega init requires --repository and --disposable")
		}
		report, err = omega.Init(opCtx, omega.InitOptions{Home: *home, Network: *network, Repository: repository, Disposable: disposable})
	} else if cmd == "provision-renewal" {
		if !disposable || renewalHome == "" {
			return usagef("omega provision-renewal requires --renewal-home and --disposable")
		}
		report, err = omega.ProvisionRenewal(opCtx, omega.ProvisionRenewalOptions{Home: *home, Network: *network, RenewalHome: renewalHome, Disposable: disposable})
	} else if cmd == "rotate" {
		if !disposable || rootVersion < 2 {
			return usagef("omega rotate requires --root-version (at least 2) and --disposable")
		}
		if rotationRole != "online" && rotationRole != "targets" {
			return usagef("omega rotate --role must be online or targets")
		}
		report, err = omega.Rotate(opCtx, omega.RotateOptions{Home: *home, Network: *network, RootVersion: rootVersion, Role: rotationRole, Apply: apply, Disposable: disposable, HTTPClient: a.newHTTPClient()})
	} else if cmd == "publish" && renew {
		if !disposable || manifestPath != "" || directory != "" || version != 0 {
			return usagef("omega publish --renew requires --disposable and accepts no manifest, repository-dir, or version; the operational journal supplies them")
		}
		report, err = omega.Renew(opCtx, omega.RenewOptions{Home: *home, Network: *network, Disposable: disposable, HTTPClient: a.newHTTPClient()})
	} else if cmd == "publish" {
		if !disposable || manifestPath == "" || directory == "" || version < 1 {
			return usagef("omega publish requires --manifest, --repository-dir, --version, and --disposable")
		}
		file, readErr := os.Open(manifestPath)
		if readErr != nil {
			return readErr
		}
		manifest, readErr := io.ReadAll(io.LimitReader(file, (64<<10)+1))
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		report, err = omega.Publish(opCtx, omega.PublishOptions{Home: *home, Network: *network, Directory: directory, Manifest: manifest, Version: version, Disposable: disposable, HTTPClient: a.newHTTPClient()})
	} else if verify {
		report, err = omega.VerifyPublication(opCtx, *home, *network, a.newHTTPClient())
	} else {
		report, err = omega.Status(opCtx, *home, *network)
	}
	if report.Schema != 0 {
		if outputErr := a.printOmegaReport(report); outputErr != nil {
			return outputErr
		}
	}
	return err
}

func (a *app) printOmegaReport(r omega.Report) error {
	if a.jsonOut {
		return json.NewEncoder(a.stdout).Encode(r)
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Authority: %s\n", r.State)
	if r.Network != "" {
		fmt.Fprintf(&out, "Network: %s\n", r.Network)
	}
	if r.Repository != "" {
		fmt.Fprintf(&out, "Repository: %s\n", r.Repository)
	}
	if r.Fingerprint != "" {
		fmt.Fprintf(&out, "Initial root SHA-256: %s\nRoot version: %d\nRoot expires: %s\n", r.Fingerprint, r.RootVersion, r.RootExpires.Format("2006-01-02T15:04:05Z07:00"))
		for _, role := range []string{"root", "targets", "snapshot", "timestamp"} {
			keys := r.Roles[role]
			fmt.Fprintf(&out, "%s: %d-of-%d; key IDs %s\n", role, keys.Threshold, len(keys.KeyIDs), strings.Join(keys.KeyIDs, ", "))
		}
	}
	if r.OperationalHome != "" {
		fmt.Fprintf(&out, "Operational home: %s\n", r.OperationalHome)
	}
	if rotation := r.Rotation; rotation != nil {
		fmt.Fprintf(&out, "Rotation: %s (root %d, role %s)\nSuccessor root SHA-256: %s\n", rotation.State, rotation.RootVersion, rotation.Role, rotation.RootSHA256)
		for _, role := range []string{"targets", "snapshot", "timestamp"} {
			if len(rotation.Keys[role]) == 0 {
				continue
			}
			fmt.Fprintf(&out, "%s key IDs: %s -> %s\n", role, strings.Join(rotation.Replaces[role], ", "), strings.Join(rotation.Keys[role], ", "))
		}
	}
	fmt.Fprintf(&out, "Publication: %s\n", r.Publication)
	if release := r.Release; release != nil {
		fmt.Fprintf(&out, "Release: %d (root %d, targets %d, snapshot %d, timestamp %d)\n", release.Version, release.Versions.Root, release.Versions.Targets, release.Versions.Snapshot, release.Versions.Timestamp)
		for _, root := range release.Roots {
			fmt.Fprintf(&out, "Node root: %s %s\n", root.ID, root.Origin)
		}
		for _, role := range []string{"targets", "snapshot", "timestamp"} {
			fmt.Fprintf(&out, "%s expires: %s\n", role, release.Expires[role].Format("2006-01-02T15:04:05Z07:00"))
		}
		fmt.Fprintf(&out, "Renew after: %s (due: %t)\n", release.RenewAfter.Format("2006-01-02T15:04:05Z07:00"), release.RenewalDue)
		for _, warning := range release.Warnings {
			fmt.Fprintf(&out, "Warning: %s\n", warning)
		}
		if !release.VerifiedAt.IsZero() {
			fmt.Fprintf(&out, "Last verified: %s\n", release.VerifiedAt.Format("2006-01-02T15:04:05Z07:00"))
		}
		if release.LastError != "" {
			fmt.Fprintf(&out, "Last publication error: %s\n", release.LastError)
		}
	}
	fmt.Fprintf(&out, "Next: %s\n", r.Action)
	return a.printf("%s", out.String())
}
