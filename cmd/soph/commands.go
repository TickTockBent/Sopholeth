package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"sopholeth/internal/client"
	"sopholeth/internal/trust"
)

// ---------- network selection ----------

// selectNetwork resolves which saved network a data command should use.
// Precedence: --network flag, then SOPH_NETWORK, then the saved current
// selection. There is never a fallback from one network to another.
func (a *app) selectNetwork(cfg *Config) (string, *Network, error) {
	name := a.networkFlag
	source := "--network"
	if name == "" {
		name = a.getenv("SOPH_NETWORK")
		source = "SOPH_NETWORK"
	}
	if name == "" {
		name = cfg.Current
		source = "current selection"
	}
	if name == "" {
		return "", nil, usagef("no network selected; run 'soph join <node>' first")
	}
	net, ok := cfg.Networks[name]
	if !ok {
		if len(cfg.Networks) == 0 {
			return "", nil, usagef("network %q (from %s) is not saved; run 'soph join <node>' first", name, source)
		}
		return "", nil, usagef("network %q (from %s) is not saved; saved networks: %s", name, source, strings.Join(cfg.Names(), ", "))
	}
	if net.Mode == modePublic && net.Discovery == discoverySignedList && net.RootsExpire > 0 {
		if time.Now().Unix() >= net.RootsExpire {
			return "", nil, usagef("network %q: signed root list expired at %s; run 'soph join' again to re-discover",
				name, time.Unix(net.RootsExpire, 0).UTC().Format(time.RFC3339))
		}
	}
	return name, &net, nil
}

// connect loads config, selects a network, and returns a client for it.
func (a *app) connect() (string, *Network, *client.Client, error) {
	cfg, err := a.loadConfig()
	if err != nil {
		return "", nil, nil, err
	}
	name, net, err := a.selectNetwork(cfg)
	if err != nil {
		return "", nil, nil, err
	}
	return name, net, client.New(net.Endpoint, a.newHTTPClient()), nil
}

// ---------- join / use / networks / forget ----------

func (a *app) cmdJoin(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	name := fs.String("name", "", "local label for the saved network")
	public := fs.Bool("public", false, "treat the supplied node as a public-network entry point")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usagef("join takes at most one endpoint")
	}
	if *name != "" {
		if err := validateNetworkName(*name); err != nil {
			return usagef("%v", err)
		}
	}

	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}

	var (
		label string
		net   Network
	)
	switch {
	case len(pos) == 0:
		// Public network via signed discovery. The root list is the only
		// trust anchor; --public is implied and accepted if given.
		label = "public"
		if *name != "" {
			label = *name
		}
		net, err = a.joinPublicDiscovered(ctx)
		if err != nil {
			return err
		}
	default:
		endpoint, err := client.NormalizeEndpoint(pos[0])
		if err != nil {
			return usagef("%v", err)
		}
		label = defaultNetworkName(endpoint)
		if *name != "" {
			label = *name
		}
		health, err := a.probe(ctx, endpoint)
		if err != nil {
			return err
		}
		net = Network{
			Endpoint:    endpoint,
			Mode:        modePrivate,
			NodeID:      health.NodeID,
			NodeNetwork: health.Network,
			Enclave:     health.Enclave,
			JoinedAt:    time.Now().UTC(),
		}
		if *public {
			net.Mode = modePublic
			net.Discovery = discoveryOperatorSupplied
			fmt.Fprintf(a.stderr, "note: %s was supplied by you and is not verified against the signed public root list\n", endpoint)
			if health.Network != modePublic {
				fmt.Fprintf(a.stderr, "warning: node reports network %q, not public\n", health.Network)
			}
		} else if health.Network == modePublic {
			fmt.Fprintf(a.stderr, "note: node reports it is on the public network; saved as a private connection (use --public to record it as a public entry point)\n")
		}
	}

	if _, exists := cfg.Networks[label]; exists {
		fmt.Fprintf(a.stderr, "replacing saved network %q\n", label)
	}
	cfg.Networks[label] = net
	cfg.Current = label
	if err := saveConfig(a.configPath, cfg); err != nil {
		return err
	}

	if a.jsonOut {
		return a.writeJSON(map[string]any{
			"name":    label,
			"current": true,
			"network": net,
		})
	}
	return a.printf("joined %s (%s) as %q; now current\n  node %s, enclave %s\n",
		net.Endpoint, describeMode(net), label, net.NodeID, net.Enclave)
}

// probe performs the health check that validates an endpoint before it is
// saved.
func (a *app) probe(ctx context.Context, endpoint string) (*client.Health, error) {
	rctx, cancel := a.requestContext(ctx)
	defer cancel()
	health, err := client.New(endpoint, a.newHTTPClient()).Health(rctx)
	if err != nil {
		return nil, fmt.Errorf("cannot join through %s: %w", endpoint, err)
	}
	if health.Status != "healthy" {
		return nil, fmt.Errorf("cannot join through %s: health status %q, expected healthy", endpoint, health.Status)
	}
	if strings.TrimSpace(health.NodeID) == "" || strings.TrimSpace(health.Enclave) == "" ||
		(health.Network != modePrivate && health.Network != modePublic) {
		return nil, fmt.Errorf("cannot join through %s: invalid health identity (node_id and enclave must be non-empty, network must be private or public)", endpoint)
	}
	return health, nil
}

// joinPublicDiscovered resolves the signed root list and picks the first
// root that answers a health check.
func (a *app) joinPublicDiscovered(ctx context.Context) (Network, error) {
	dctx, cancel := a.requestContext(ctx)
	list, err := a.publicDiscovery(dctx)
	cancel()
	if err != nil {
		// A resolver that cannot be reached is a connectivity problem
		// (exit 4). A record that fails to parse or verify is not.
		var netErr net.Error
		if errors.As(err, &netErr) || errors.Is(err, context.DeadlineExceeded) {
			err = &client.UnreachableError{Err: err}
		}
		return Network{}, fmt.Errorf("public discovery unavailable: %w", err)
	}
	if len(list.Nodes) == 0 {
		return Network{}, errors.New("public discovery unavailable: signed root list contains no nodes")
	}
	var lastErr error
	for _, root := range list.Nodes {
		endpoint, err := client.NormalizeEndpoint(root)
		if err != nil {
			lastErr = err
			continue
		}
		health, err := a.probe(ctx, endpoint)
		if err != nil {
			lastErr = err
			fmt.Fprintf(a.stderr, "root %s failed health check: %v\n", root, err)
			continue
		}
		return Network{
			Endpoint:    endpoint,
			Mode:        modePublic,
			Discovery:   discoverySignedList,
			Roots:       append([]string(nil), list.Nodes...),
			RootsExpire: list.Expires,
			NodeID:      health.NodeID,
			NodeNetwork: health.Network,
			Enclave:     health.Enclave,
			JoinedAt:    time.Now().UTC(),
		}, nil
	}
	return Network{}, fmt.Errorf("no public root passed health checks: %w", lastErr)
}

// newPublicDiscovery builds a discovery hook that resolves and verifies the
// signed root list against pubkey using the given DNS configuration. A nil
// pubkey means the compiled omega key. Tests inject a test anchor and an
// in-memory resolver.
func newPublicDiscovery(pubkey ed25519.PublicKey, dns trust.DNSConfig) func(ctx context.Context) (*trust.SignedList, error) {
	return func(ctx context.Context) (*trust.SignedList, error) {
		key := pubkey
		if key == nil {
			var err error
			key, err = trust.DecodedOmegaPubkey()
			if err != nil {
				return nil, fmt.Errorf("compiled omega public key is invalid: %w", err)
			}
		}
		return trust.FetchSigned(ctx, dns, key, time.Now())
	}
}

// realPublicDiscovery is the production hook: compiled omega key, default
// DNS names.
var realPublicDiscovery = newPublicDiscovery(nil, trust.DNSConfig{})

func describeMode(n Network) string {
	if n.Mode != modePublic {
		return modePrivate
	}
	if n.Discovery != "" {
		return "public, " + n.Discovery
	}
	return modePublic
}

func (a *app) cmdUse(args []string) error {
	fs := flag.NewFlagSet("use", flag.ContinueOnError)
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: soph use <name>")
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	name := pos[0]
	net, ok := cfg.Networks[name]
	if !ok {
		return usagef("network %q is not saved; saved networks: %s", name, strings.Join(cfg.Names(), ", "))
	}
	cfg.Current = name
	if err := saveConfig(a.configPath, cfg); err != nil {
		return err
	}
	if a.jsonOut {
		return a.writeJSON(map[string]any{"name": name, "current": true, "network": net})
	}
	return a.printf("using %q (%s)\n", name, net.Endpoint)
}

func (a *app) cmdNetworks(args []string) error {
	fs := flag.NewFlagSet("networks", flag.ContinueOnError)
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usagef("networks takes no arguments")
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	if a.jsonOut {
		type entry struct {
			Name    string `json:"name"`
			Current bool   `json:"current"`
			Network
		}
		out := make([]entry, 0, len(cfg.Networks))
		for _, name := range cfg.Names() {
			out = append(out, entry{Name: name, Current: name == cfg.Current, Network: cfg.Networks[name]})
		}
		return a.writeJSON(map[string]any{"current": cfg.Current, "networks": out})
	}
	if len(cfg.Networks) == 0 {
		return a.printf("no saved networks; run 'soph join <node>'\n")
	}
	for _, name := range cfg.Names() {
		n := cfg.Networks[name]
		marker := " "
		if name == cfg.Current {
			marker = "*"
		}
		if err := a.printf("%s %-20s %-28s %-22s enclave=%s\n", marker, name, n.Endpoint, describeMode(n), n.Enclave); err != nil {
			return err
		}
	}
	return nil
}

func (a *app) cmdForget(args []string) error {
	fs := flag.NewFlagSet("forget", flag.ContinueOnError)
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: soph forget <name>")
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	name := pos[0]
	if _, ok := cfg.Networks[name]; !ok {
		return usagef("network %q is not saved", name)
	}
	delete(cfg.Networks, name)
	if cfg.Current == name {
		cfg.Current = ""
	}
	if err := saveConfig(a.configPath, cfg); err != nil {
		return err
	}
	if a.jsonOut {
		return a.writeJSON(map[string]any{"forgot": name, "current": cfg.Current})
	}
	if err := a.printf("forgot %q\n", name); err != nil {
		return err
	}
	if cfg.Current == "" {
		return a.printf("no network is current; run 'soph use <name>' or 'soph join <node>'\n")
	}
	return nil
}

// ---------- put / get / exists / list ----------

func (a *app) cmdPut(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("put", flag.ContinueOnError)
	ttl := fs.Int("ttl", 0, "TTL in seconds (node default when omitted)")
	file := fs.String("file", "", "read the value from this file instead of stdin")
	requireConfirmed := fs.Bool("require-confirmed", false, "exit 5 if the node did not confirm quorum")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usagef("usage: soph put [key] [--ttl seconds] [--file path]")
	}
	if *ttl < 0 {
		return usagef("--ttl must be a positive number of seconds")
	}
	key := ""
	generated := false
	if len(pos) == 1 {
		key = pos[0]
		if key == "" {
			return usagef("key must not be empty")
		}
	} else {
		key = newKey()
		generated = true
	}

	name, net, c, err := a.connect()
	if err != nil {
		return err
	}
	data, err := a.readInput(ctx, *file)
	if err != nil {
		return err
	}
	rctx, cancel := a.requestContext(ctx)
	res, err := c.Put(rctx, key, data, *ttl)
	cancel()
	if err != nil {
		return err
	}

	// Best-effort observation of the current key, separate from the write
	// result: another writer may have replaced it before this HEAD request.
	var observed *client.Metadata
	mctx, mcancel := a.requestContext(ctx)
	if m, err := c.Exists(mctx, key); err == nil {
		observed = m
	}
	mcancel()

	var outputErr error
	if a.jsonOut {
		out := map[string]any{
			"key":           key,
			"generated_key": generated,
			"bytes":         len(data),
			"quorum_status": string(res.Status),
			"network":       name,
			"endpoint":      net.Endpoint,
		}
		if *ttl > 0 {
			out["ttl_requested"] = *ttl
		}
		if observed != nil {
			out["observed_current_key"] = metadataJSON(*observed, nil)
		}
		outputErr = a.writeJSON(out)
	} else {
		// The key is the one thing scripts need from stdout.
		outputErr = a.printf("%s\n", key)
		var b strings.Builder
		fmt.Fprintf(&b, "stored %d bytes under %q on %s: quorum %s", len(data), key, net.Endpoint, res.Status)
		if *ttl > 0 {
			fmt.Fprintf(&b, ", requested ttl %ds", *ttl)
		}
		if res.Status == client.WritePending {
			b.WriteString("; stored locally, replication continues in the background")
		}
		fmt.Fprintln(a.stderr, b.String())
		if observed != nil {
			fmt.Fprintf(a.stderr, "observed current key %q: local ttl %ds (may reflect a subsequent write)\n",
				key, int(observed.OriginalTTL.Seconds()))
		}
	}
	if outputErr != nil {
		return fmt.Errorf("write of %q stored locally, quorum %s; output failed: %w", key, res.Status, outputErr)
	}
	if *requireConfirmed && res.Status != client.WriteConfirmed {
		return &pendingError{key: key}
	}
	return nil
}

func (a *app) cmdGet(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	output := fs.String("output", "", "write the value to this file instead of stdout")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: soph get <key> [--output path]")
	}
	key := pos[0]

	_, net, c, err := a.connect()
	if err != nil {
		return err
	}
	rctx, cancel := a.requestContext(ctx)
	defer cancel()
	val, err := c.Get(rctx, key)
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			return fmt.Errorf("%w (%s)", err, net.Endpoint)
		}
		return err
	}

	if *output != "" {
		if err := os.WriteFile(*output, val.Data, 0o600); err != nil {
			return fmt.Errorf("write --output: %w", err)
		}
		if a.jsonOut {
			return a.writeJSON(metadataJSON(val.Metadata, map[string]any{"bytes": len(val.Data), "output": *output}))
		}
		return nil
	}
	if a.jsonOut {
		return a.writeJSON(metadataJSON(val.Metadata, map[string]any{
			"bytes":        len(val.Data),
			"encoding":     "base64",
			"value_base64": base64.StdEncoding.EncodeToString(val.Data),
		}))
	}
	// Raw bytes, nothing added: an empty value writes nothing.
	_, err = a.stdout.Write(val.Data)
	return err
}

func (a *app) cmdExists(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("exists", flag.ContinueOnError)
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: soph exists <key>")
	}
	key := pos[0]

	_, net, c, err := a.connect()
	if err != nil {
		return err
	}
	rctx, cancel := a.requestContext(ctx)
	defer cancel()
	meta, err := c.Exists(rctx, key)
	if errors.Is(err, client.ErrNotFound) {
		if a.jsonOut {
			if werr := a.writeJSON(map[string]any{"key": key, "exists": false, "endpoint": net.Endpoint}); werr != nil {
				return werr
			}
		}
		return fmt.Errorf("%w (%s)", err, net.Endpoint)
	}
	if err != nil {
		return err
	}
	if a.jsonOut {
		return a.writeJSON(metadataJSON(*meta, map[string]any{"exists": true, "endpoint": net.Endpoint}))
	}
	return a.printf("%s\tremaining %ds of %ds\tcreated %s\n",
		key, int(meta.RemainingTTL.Seconds()), int(meta.OriginalTTL.Seconds()), meta.CreatedAt.UTC().Format(time.RFC3339))
}

func (a *app) cmdList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	prefix := fs.String("prefix", "", "only keys with this prefix")
	limit := fs.Int("limit", 0, "page size (unbounded when 0)")
	cursor := fs.String("cursor", "", "continue after this key")
	all := fs.Bool("all", false, "walk every page")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usagef("usage: soph list [--prefix p] [--limit n] [--cursor c] [--all]")
	}
	if *limit < 0 {
		return usagef("--limit must not be negative")
	}
	if *all && *cursor != "" {
		return usagef("--all and --cursor are mutually exclusive")
	}

	_, net, c, err := a.connect()
	if err != nil {
		return err
	}

	var page *client.KeyPage
	if *all {
		// Each page gets its own timeout; the walk as a whole is bounded
		// only by the caller's context.
		pageSize := *limit
		if pageSize == 0 {
			pageSize = 1000
		}
		keys := []string{}
		next := ""
		for {
			rctx, cancel := a.requestContext(ctx)
			p, err := c.ListKeys(rctx, *prefix, pageSize, next)
			cancel()
			if err != nil {
				return err
			}
			keys = append(keys, p.Keys...)
			if p.NextCursor == "" || p.NextCursor == next {
				break
			}
			next = p.NextCursor
		}
		page = &client.KeyPage{Keys: keys}
	} else {
		rctx, cancel := a.requestContext(ctx)
		page, err = c.ListKeys(rctx, *prefix, *limit, *cursor)
		cancel()
		if err != nil {
			return err
		}
	}

	if a.jsonOut {
		out := map[string]any{"keys": page.Keys, "endpoint": net.Endpoint}
		if page.NextCursor != "" {
			out["next_cursor"] = page.NextCursor
		}
		return a.writeJSON(out)
	}
	for _, k := range page.Keys {
		if err := a.printf("%s\n", k); err != nil {
			return err
		}
	}
	if page.NextCursor != "" {
		fmt.Fprintf(a.stderr, "more keys available; continue with --cursor %q\n", page.NextCursor)
	}
	return nil
}

// ---------- diagnostics ----------

func (a *app) cmdDiagnostic(ctx context.Context, which string, args []string) error {
	fs := flag.NewFlagSet(which, flag.ContinueOnError)
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usagef("%s takes no arguments", which)
	}
	_, _, c, err := a.connect()
	if err != nil {
		return err
	}
	rctx, cancel := a.requestContext(ctx)
	defer cancel()

	var raw []byte
	switch which {
	case "health":
		h, err := c.Health(rctx)
		if err != nil {
			return err
		}
		raw, _ = json.Marshal(h)
	case "status":
		raw, err = c.Status(rctx)
	case "topology":
		raw, err = c.Topology(rctx)
	case "metrics":
		raw, err = c.Metrics(rctx)
		if err != nil {
			return err
		}
		_, err = a.stdout.Write(raw)
		return err
	}
	if err != nil {
		return err
	}
	if a.jsonOut {
		// Already JSON; pass through compact.
		_, err = a.stdout.Write(append(bytes.TrimSpace(raw), '\n'))
		return err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, bytes.TrimSpace(raw), "", "  "); err != nil {
		_, err = a.stdout.Write(raw)
		return err
	}
	buf.WriteByte('\n')
	_, err = a.stdout.Write(buf.Bytes())
	return err
}

// ---------- helpers ----------

// readInput lets process cancellation win even while a pipe is waiting for
// EOF or a named file is blocked on open/read. io.Reader has no cancellation
// contract; an uninterruptible read can outlive this call until the CLI exits.
// The buffered result lets a completed read finish without a waiting caller.
func (a *app) readInput(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		var data []byte
		var err error
		if path != "" {
			data, err = os.ReadFile(path)
			if err != nil {
				err = fmt.Errorf("read --file: %w", err)
			}
		} else {
			data, err = io.ReadAll(a.stdin)
			if err != nil {
				err = fmt.Errorf("read stdin: %w", err)
			}
		}
		done <- result{data, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-done:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return res.data, res.err
	}
}

func metadataJSON(m client.Metadata, extra map[string]any) map[string]any {
	out := map[string]any{
		"key":               m.Key,
		"created_at":        m.CreatedAt.UTC().Format(time.RFC3339),
		"ttl_seconds":       int(m.OriginalTTL.Seconds()),
		"remaining_seconds": int(m.RemainingTTL.Seconds()),
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func (a *app) writeJSON(v any) error {
	enc := json.NewEncoder(a.stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// newKey returns a UUID-shaped random hex string, matching the shape the
// MCP server generates. Keys are opaque to the network.
func newKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("k-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	)
}
