package main

import (
	"bytes"
	"flag"
	"fmt"
)

// commandHelp preserves the command's flags for rendering after parsing has
// stopped, before any configuration, input, or network operations occur.
type commandHelp struct{ flags *flag.FlagSet }

func (*commandHelp) Error() string { return "help requested" }

func (a *app) printCommandHelp(fs *flag.FlagSet) error {
	commands := map[string]struct{ args, description string }{
		"omega init":              {"--home path --network id --repository https://host --disposable", "Create a complete authority atomically. Repeating the command recovers or verifies the same authority; existing keys are never replaced."},
		"omega provision-renewal": {"--home offline-path --network id --renewal-home online-path --disposable", "Provision only the online keys and hand off publication state to a separate home. Repeating the same command recovers the handoff."},
		"omega publish":           {"--home path --network id --disposable [--renew | --manifest path --repository-dir path --version n]", "Publish an approved manifest through the offline authority, or use --renew from the operational home to refresh due metadata using online keys. Verify the exact release over HTTPS."},
		"omega status":            {"--home path --network id [--verify]", "Inspect authority and release state. With --verify, fetch and verify the latest prepared release over HTTPS."},
		"omega rotate":            {"--home offline-path --network id --root-version n --disposable [--apply root-sha256]", "Prepare replacement snapshot/timestamp keys and a root-signed transition. Review the digest and key IDs, then apply that exact digest. Repeating the same version resumes the same transition."},
		"join":                    {"[endpoint] [--name name] [--public]", "Validate and save a node, making its network current. Without an endpoint, use signed public discovery."},
		"use":                     {"<name>", "Make a saved network current."},
		"networks":                {"", "List saved networks; * marks the current one."},
		"forget":                  {"<name>", "Remove a saved network."},
		"put":                     {"[key] [--ttl seconds] [--file path] [--require-confirmed]", "Write stdin or a file. Generate a key if omitted; print the key to stdout."},
		"get":                     {"<key> [--output path]", "Write the stored value's bytes to stdout or a file."},
		"exists":                  {"<key>", "Check presence and local TTL without the payload. Alias: head."},
		"list":                    {"[--prefix p] [--limit n] [--cursor c] [--all]", "List live keys on the contacted node."},
		"health":                  {"", "Show the node's health and identity as JSON."},
		"status":                  {"", "Show the node's status as JSON."},
		"topology":                {"", "Show the node's topology as JSON."},
		"metrics":                 {"", "Show the node's Prometheus metrics."},
		"version":                 {"", "Show the client version."},
		"serve":                   {"[--node endpoint] [--port 8181] [--bind address] [--q text] [--open]", "Serve soph.stream locally. The browser connects directly to the selected node; Ctrl-C stops the viewer server."},
	}
	info := commands[fs.Name()]
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Usage: soph [global flags] %s", fs.Name())
	if info.args != "" {
		fmt.Fprintf(&buf, " %s", info.args)
	}
	fmt.Fprintf(&buf, "\n\n%s\n\nCommand flags:\n", info.description)
	fs.SetOutput(&buf)
	defer fs.SetOutput(nil)
	fs.PrintDefaults()
	buf.WriteString("  -h, --help\n    \tshow this command's help\n\nGlobal flags go before the command; see 'soph help'.\n")
	_, err := a.stdout.Write(buf.Bytes())
	return err
}

func (a *app) cmdVersion(args []string) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usagef("version takes no arguments")
	}
	return a.printf("soph (Sopholeth client, development build)\n")
}

func (a *app) printf(format string, args ...any) error {
	_, err := fmt.Fprintf(a.stdout, format, args...)
	if err != nil {
		return fmt.Errorf("write stdout: %w", err)
	}
	return nil
}
