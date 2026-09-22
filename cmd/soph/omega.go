package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"sopholeth/internal/omega"
)

func (a *app) cmdOmega(ctx context.Context, args []string) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help")) {
		return a.printf("Usage: soph [global flags] omega <init|status> [flags]\n\n  init    Atomically initialize a disposable authority, or recover the same transaction.\n  status  Verify and inspect a committed local authority.\n\nUse 'soph omega <command> --help' for flags.\n")
	}
	cmd := args[0]
	if cmd != "init" && cmd != "status" {
		return usagef("unknown omega command %q; run 'soph omega --help'", cmd)
	}
	fs := flag.NewFlagSet("omega "+cmd, flag.ContinueOnError)
	home := fs.String("home", "", "private custody home, shared by all operator commands (required)")
	network := fs.String("network", "", "authority network identity (required; independent of client profiles)")
	var repository string
	var disposable bool
	if cmd == "init" {
		fs.StringVar(&repository, "repository", "", "HTTPS metadata repository origin (required; base paths are planned)")
		fs.BoolVar(&disposable, "disposable", false, "initialize development/rehearsal keys; production custody is not implemented")
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
	fmt.Fprintf(&out, "Publication: %s\nNext: %s\n", r.Publication, r.Action)
	return a.printf("%s", out.String())
}
