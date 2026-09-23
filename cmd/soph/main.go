// Command soph is the Sopholeth client.
//
// It is a direct HTTP client: it talks to one node's API and runs no node
// of its own. Join a network once, then put, get, exists, and list work
// against that node until you switch. See docs/cli.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"sopholeth/internal/client"
	"sopholeth/internal/trust"
)

// Exit codes. Scripts can branch on these; see docs/cli.md.
const (
	exitOK          = 0
	exitError       = 1 // node returned an error, or a local failure
	exitUsage       = 2 // bad arguments, no network selected, unknown network
	exitNotFound    = 3 // the contacted node has no live value for the key
	exitUnreachable = 4 // no response: refused, DNS failure, timeout, cancelled
	exitPending     = 5 // only with --require-confirmed: write stored, quorum pending
)

const defaultTimeout = 15 * time.Second

// app carries the process-level dependencies so run can be exercised in
// tests without touching the real environment.
type app struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	getenv func(string) string

	// publicDiscovery resolves the verified public root list. Tests inject
	// a fake; the real one uses the compiled omega key and DNS.
	publicDiscovery func(ctx context.Context) (*trust.SignedList, error)

	// newHTTPClient builds the transport. Tests may substitute an
	// httptest client.
	newHTTPClient func() *http.Client

	// Tests may substitute terminal input without exposing a password flag.
	omegaPassphrase func(context.Context, bool) ([]byte, error)

	// Populated from global flags.
	configPath  string
	networkFlag string
	jsonOut     bool
	timeout     time.Duration
	timeoutSet  bool
}

func main() {
	os.Exit(runMain())
}

func runMain() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a := &app{
		stdin:           os.Stdin,
		stdout:          os.Stdout,
		stderr:          os.Stderr,
		getenv:          os.Getenv,
		publicDiscovery: realPublicDiscovery,
		newHTTPClient:   func() *http.Client { return &http.Client{} },
	}
	return a.run(ctx, os.Args[1:])
}

// usageError marks a problem with how soph was invoked, as opposed to a
// problem talking to the node.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// pendingError is returned by put when --require-confirmed is set and the
// node answered 202.
type pendingError struct{ key string }

func (e *pendingError) Error() string {
	return fmt.Sprintf("write of %q stored locally, quorum pending", e.key)
}

func (a *app) run(ctx context.Context, args []string) int {
	global := flag.NewFlagSet("soph", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	global.StringVar(&a.networkFlag, "network", "", "saved network to use for this command")
	global.BoolVar(&a.jsonOut, "json", false, "machine-readable JSON output")
	global.DurationVar(&a.timeout, "timeout", defaultTimeout, "per-request timeout")
	global.StringVar(&a.configPath, "config", "", "config file path (default $SOPH_CONFIG_DIR/soph.json)")
	showHelp := global.Bool("help", false, "show help")
	global.BoolVar(showHelp, "h", false, "show help")

	// Global flags may precede the subcommand. Anything after the
	// subcommand belongs to it.
	if err := global.Parse(args); err != nil {
		return a.fail(usagef("%v", err))
	}
	a.timeoutSet = false
	global.Visit(func(f *flag.Flag) {
		if f.Name == "timeout" {
			a.timeoutSet = true
		}
	})
	rest := global.Args()
	if *showHelp || len(rest) == 0 {
		if err := a.printUsage(a.stdout); err != nil {
			return a.fail(err)
		}
		if len(rest) == 0 && !*showHelp {
			return exitUsage
		}
		return exitOK
	}
	if a.timeout <= 0 {
		return a.fail(usagef("--timeout must be positive"))
	}
	cmd, cmdArgs := rest[0], rest[1:]
	if cmd == "help" {
		if len(cmdArgs) > 1 {
			return a.fail(usagef("usage: soph help [command]"))
		}
		if len(cmdArgs) == 1 {
			cmd, cmdArgs = cmdArgs[0], []string{"--help"}
		}
	}
	var err error
	switch cmd {
	case "help", "-h", "--help":
		err = a.printUsage(a.stdout)
	case "join":
		err = a.cmdJoin(ctx, cmdArgs)
	case "use":
		err = a.cmdUse(cmdArgs)
	case "networks":
		err = a.cmdNetworks(cmdArgs)
	case "forget":
		err = a.cmdForget(cmdArgs)
	case "put":
		err = a.cmdPut(ctx, cmdArgs)
	case "get":
		err = a.cmdGet(ctx, cmdArgs)
	case "exists", "head":
		err = a.cmdExists(ctx, cmdArgs)
	case "list":
		err = a.cmdList(ctx, cmdArgs)
	case "serve":
		err = a.cmdServe(ctx, cmdArgs)
	case "omega":
		err = a.cmdOmega(ctx, cmdArgs)
	case "health", "status", "topology", "metrics":
		err = a.cmdDiagnostic(ctx, cmd, cmdArgs)
	case "version":
		err = a.cmdVersion(cmdArgs)
	default:
		err = usagef("unknown command %q; run 'soph help'", cmd)
	}
	var help *commandHelp
	if errors.As(err, &help) {
		err = a.printCommandHelp(help.flags)
	}
	if err != nil {
		return a.fail(err)
	}
	return exitOK
}

// fail prints err to stderr and maps it to an exit code.
func (a *app) fail(err error) int {
	fmt.Fprintf(a.stderr, "soph: %v\n", err)
	return exitCodeFor(err)
}

func exitCodeFor(err error) int {
	var usage *usageError
	var unreachable *client.UnreachableError
	var pending *pendingError
	switch {
	case errors.As(err, &usage):
		return exitUsage
	case errors.Is(err, client.ErrNotFound):
		return exitNotFound
	case errors.As(err, &unreachable), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return exitUnreachable
	case errors.As(err, &pending):
		return exitPending
	case errors.Is(err, client.ErrEmptyKey), errors.Is(err, client.ErrBadKey):
		return exitUsage
	default:
		return exitError
	}
}

// parseInterspersed lets flags appear before or after positionals, so both
// `soph put --ttl 300 greeting` and `soph put greeting --ttl 300` work. A
// literal "--" ends flag parsing; everything after it is positional.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var positional, afterTerminator []string
	for i, arg := range args {
		if arg == "--" {
			afterTerminator = args[i+1:]
			args = args[:i]
			break
		}
	}
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, &commandHelp{flags: fs}
			}
			return nil, usagef("%v", err)
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
	return append(positional, afterTerminator...), nil
}

// requestContext bounds one node request.
func (a *app) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, a.timeout)
}

func (a *app) printUsage(w io.Writer) error {
	_, err := fmt.Fprint(w, strings.TrimLeft(`
soph - Sopholeth client

Usage:
  soph [global flags] <command> [command flags] [args]

Join a network once, then every command uses it:
  soph join 192.168.0.1              join a private network through that node (port 8080)
  soph join node.example:9000 --name lab
  soph join                          join the public network via signed discovery
  soph join host:port --public       public network through an operator-named node

Networks:
  networks                           list saved networks; * marks the current one
  use <name>                         make a saved network current
  forget <name>                      remove a saved network

Data (against the current network's node):
  put [key] [--ttl seconds] [--file path] [--require-confirmed]
                                     write stdin (or --file) under key; generates a key if omitted
  get <key> [--output path]          write the value's bytes to stdout (or --output)
  exists <key>                       check presence and local TTL without the payload
  list [--prefix p] [--limit n] [--cursor c] [--all]
                                     list live keys on the contacted node

Diagnostics:
  health | status | topology | metrics

Viewer:
  serve [--node endpoint] [--port 8181] [--q text] [--open]
                                     serve soph.stream for the selected network

Authority operations:
  omega init | publish | status      initialize, publish, or inspect a disposable TUF authority

Global flags:
  --network <name>   use a saved network for this command (also SOPH_NETWORK)
  --json             machine-readable output
  --timeout <dur>    timeout (default 15s; encrypted omega init/check-keys 2m)
  --config <path>    config file (default $SOPH_CONFIG_DIR/soph.json)

Use 'soph <command> --help' for command-specific flags and arguments.

Exit codes: 0 ok, 1 node or local error, 2 usage, 3 not found on contacted node,
4 node unreachable or timed out, 5 write pending (only with --require-confirmed).

Reads and listings reflect the contacted node only. A missing key there
does not mean it is absent from the network. TTL is mandatory and local to
each replica; the node may clamp the requested value to its bounds.
`, "\n"))
	return err
}
