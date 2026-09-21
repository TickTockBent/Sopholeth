package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sopholeth/internal/client/clienttest"
	"sopholeth/internal/trust"
)

// testApp wraps an app with captured streams and a temp config file.
type testApp struct {
	app        *app
	stdout     *bytes.Buffer
	stderr     *bytes.Buffer
	env        map[string]string
	configPath string
}

func newTestApp(t *testing.T) *testApp {
	t.Helper()
	dir := t.TempDir()
	ta := &testApp{
		stdout:     &bytes.Buffer{},
		stderr:     &bytes.Buffer{},
		env:        map[string]string{"SOPH_CONFIG_DIR": dir},
		configPath: filepath.Join(dir, configFileName),
	}
	ta.app = &app{
		stdout: ta.stdout,
		stderr: ta.stderr,
		getenv: func(k string) string { return ta.env[k] },
		publicDiscovery: func(context.Context) (*trust.SignedList, error) {
			return nil, errors.New("no discovery configured in test")
		},
		newHTTPClient: func() *http.Client { return &http.Client{} },
	}
	return ta
}

// run executes one soph invocation and returns its exit code plus what it
// wrote. Each call resets the per-invocation flag state.
func (ta *testApp) run(stdin string, args ...string) (int, string, string) {
	return ta.runCtx(context.Background(), stdin, args...)
}

func (ta *testApp) runCtx(ctx context.Context, stdin string, args ...string) (int, string, string) {
	ta.stdout.Reset()
	ta.stderr.Reset()
	ta.app.stdin = strings.NewReader(stdin)
	ta.app.configPath = ""
	ta.app.networkFlag = ""
	ta.app.jsonOut = false
	ta.app.timeout = 0
	code := ta.app.run(ctx, args)
	return code, ta.stdout.String(), ta.stderr.String()
}

func (ta *testApp) mustRun(t *testing.T, stdin string, args ...string) (string, string) {
	t.Helper()
	code, out, errOut := ta.run(stdin, args...)
	if code != exitOK {
		t.Fatalf("soph %v: exit %d\nstdout: %s\nstderr: %s", args, code, out, errOut)
	}
	return out, errOut
}

func startFakeNode(t *testing.T) (*clienttest.FakeNode, string) {
	t.Helper()
	node := clienttest.New()
	srv := httptest.NewServer(node.Handler())
	t.Cleanup(srv.Close)
	return node, strings.TrimPrefix(srv.URL, "http://")
}

func decodeJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("invalid JSON %q: %v", s, err)
	}
	return m
}

// ---------- usage ----------

func TestNoArgsAndHelp(t *testing.T) {
	ta := newTestApp(t)
	code, out, _ := ta.run("")
	if code != exitUsage || !strings.Contains(out, "Usage:") {
		t.Fatalf("no args: exit %d, out %q", code, out)
	}
	code, out, _ = ta.run("", "help")
	if code != exitOK || !strings.Contains(out, "Usage:") {
		t.Fatalf("help: exit %d", code)
	}
	code, _, errOut := ta.run("", "frobnicate")
	if code != exitUsage || !strings.Contains(errOut, "unknown command") {
		t.Fatalf("unknown: exit %d, stderr %q", code, errOut)
	}
}

func TestNoNetworkSelectedIsUsageError(t *testing.T) {
	ta := newTestApp(t)
	for _, args := range [][]string{{"get", "k"}, {"put", "k"}, {"exists", "k"}, {"list"}, {"health"}} {
		code, _, errOut := ta.run("x", args...)
		if code != exitUsage || !strings.Contains(errOut, "no network selected") {
			t.Fatalf("%v: exit %d, stderr %q", args, code, errOut)
		}
	}
}

func TestInterspersedFlags(t *testing.T) {
	_, addr := startFakeNode(t)
	ta := newTestApp(t)
	ta.mustRun(t, "", "join", addr)

	_, errOut := ta.mustRun(t, "v", "put", "greeting", "--ttl", "600")
	if !strings.Contains(errOut, "local ttl 600s") {
		t.Fatalf("flag after positional not honored: %q", errOut)
	}
	_, errOut = ta.mustRun(t, "v", "put", "--ttl", "900", "greeting")
	if !strings.Contains(errOut, "local ttl 900s") {
		t.Fatalf("flag before positional not honored: %q", errOut)
	}
	// "--" ends flag parsing so a key that looks like a flag can be used.
	ta.mustRun(t, "v", "put", "--", "--weird")
	out, _ := ta.mustRun(t, "", "get", "--", "--weird")
	if out != "v" {
		t.Fatalf("get of flag-like key = %q", out)
	}
}

// ---------- join / networks ----------

func TestJoinSavesAndSelects(t *testing.T) {
	node, addr := startFakeNode(t)
	node.NodeID = "node-xyz"
	node.Enclave = "lab-enclave"
	ta := newTestApp(t)

	out, _ := ta.mustRun(t, "", "join", addr)
	if !strings.Contains(out, "joined http://"+addr) || !strings.Contains(out, "node-xyz") {
		t.Fatalf("join output = %q", out)
	}
	cfg, err := loadConfig(ta.configPath)
	if err != nil {
		t.Fatal(err)
	}
	host, _, _ := net.SplitHostPort(addr)
	if cfg.Current != host {
		t.Fatalf("current = %q, want %q", cfg.Current, host)
	}
	saved := cfg.Networks[host]
	if saved.Endpoint != "http://"+addr || saved.Mode != modePrivate || saved.Enclave != "lab-enclave" || saved.NodeID != "node-xyz" {
		t.Fatalf("saved = %+v", saved)
	}
	info, err := os.Stat(ta.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config perm = %o, want 600", perm)
	}
}

func TestJoinUnreachableDoesNotSave(t *testing.T) {
	ta := newTestApp(t)
	code, _, errOut := ta.run("", "join", "127.0.0.1:1")
	if code != exitUnreachable || !strings.Contains(errOut, "cannot join") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if _, err := os.Stat(ta.configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config was written after failed join: %v", err)
	}
}

func TestJoinRejectsBadInput(t *testing.T) {
	ta := newTestApp(t)
	for _, args := range [][]string{
		{"join", "ftp://x"},
		{"join", "a", "b"},
		{"join", "127.0.0.1:1", "--name", "bad name!"},
	} {
		code, _, _ := ta.run("", args...)
		if code != exitUsage {
			t.Fatalf("%v: exit %d, want usage", args, code)
		}
	}
}

func TestTwoNetworksDoNotMix(t *testing.T) {
	nodeA, addrA := startFakeNode(t)
	nodeB, addrB := startFakeNode(t)
	ta := newTestApp(t)

	ta.mustRun(t, "", "join", addrA, "--name", "alpha")
	ta.mustRun(t, "", "join", addrB, "--name", "beta")

	// beta is current after the second join.
	ta.mustRun(t, "in-beta", "put", "k")
	if _, ok := nodeB.Value("k"); !ok {
		t.Fatal("write did not land on beta")
	}
	if _, ok := nodeA.Value("k"); ok {
		t.Fatal("write leaked to alpha")
	}

	// --network overrides for one command without changing the selection.
	ta.mustRun(t, "in-alpha", "--network", "alpha", "put", "k")
	if v, _ := nodeA.Value("k"); string(v) != "in-alpha" {
		t.Fatalf("alpha value = %q", v)
	}
	out, _ := ta.mustRun(t, "", "get", "k")
	if out != "in-beta" {
		t.Fatalf("current should still be beta, got %q", out)
	}

	// SOPH_NETWORK sits between the flag and the saved selection.
	ta.env["SOPH_NETWORK"] = "alpha"
	out, _ = ta.mustRun(t, "", "get", "k")
	if out != "in-alpha" {
		t.Fatalf("SOPH_NETWORK not honored: %q", out)
	}
	out, _ = ta.mustRun(t, "", "--network", "beta", "get", "k")
	if out != "in-beta" {
		t.Fatalf("--network should beat SOPH_NETWORK: %q", out)
	}
	ta.env["SOPH_NETWORK"] = "ghost"
	code, _, errOut := ta.run("", "get", "k")
	if code != exitUsage || !strings.Contains(errOut, `"ghost" (from SOPH_NETWORK)`) {
		t.Fatalf("unknown env network: exit %d, %q", code, errOut)
	}
	delete(ta.env, "SOPH_NETWORK")

	// use switches; networks lists with the marker.
	ta.mustRun(t, "", "use", "alpha")
	out, _ = ta.mustRun(t, "", "networks")
	if !strings.Contains(out, "* alpha") || !strings.Contains(out, "  beta") {
		t.Fatalf("networks = %q", out)
	}
	out, _ = ta.mustRun(t, "", "--json", "networks")
	listing := decodeJSON(t, out)
	if listing["current"] != "alpha" {
		t.Fatalf("json current = %v", listing["current"])
	}

	// forget the current one: nothing is selected afterwards, no fallback.
	ta.mustRun(t, "", "forget", "alpha")
	code, _, errOut = ta.run("", "get", "k")
	if code != exitUsage || !strings.Contains(errOut, "no network selected") {
		t.Fatalf("after forget: exit %d, %q", code, errOut)
	}
	code, _, _ = ta.run("", "use", "alpha")
	if code != exitUsage {
		t.Fatalf("use of forgotten network: exit %d", code)
	}
	ta.mustRun(t, "", "use", "beta")
	out, _ = ta.mustRun(t, "", "get", "k")
	if out != "in-beta" {
		t.Fatalf("after use beta: %q", out)
	}
}

// ---------- put / get / exists ----------

func TestPutGetRoundTripsBytes(t *testing.T) {
	_, addr := startFakeNode(t)
	ta := newTestApp(t)
	ta.mustRun(t, "", "join", addr)

	cases := map[string]string{
		"text":   "hello world\n",
		"binary": "\x00\xff\r\n\x80\x00",
		"empty":  "",
	}
	for key, payload := range cases {
		out, errOut := ta.mustRun(t, payload, "put", key, "--ttl", "300")
		if strings.TrimSpace(out) != key {
			t.Fatalf("put %q stdout = %q, want the key", key, out)
		}
		if !strings.Contains(errOut, "quorum confirmed") {
			t.Fatalf("put %q stderr = %q", key, errOut)
		}
		got, _ := ta.mustRun(t, "", "get", key)
		if got != payload {
			t.Fatalf("get %q = %q, want %q", key, got, payload)
		}
	}

	// --file and --output round trip.
	src := filepath.Join(t.TempDir(), "in.bin")
	dst := filepath.Join(t.TempDir(), "out.bin")
	blob := make([]byte, 4096)
	rand.Read(blob)
	os.WriteFile(src, blob, 0o600)
	ta.mustRun(t, "", "put", "blob", "--file", src)
	out, _ := ta.mustRun(t, "", "get", "blob", "--output", dst)
	if out != "" {
		t.Fatalf("stdout should be empty with --output, got %d bytes", len(out))
	}
	back, _ := os.ReadFile(dst)
	if !bytes.Equal(back, blob) {
		t.Fatal("--file/--output round trip corrupted bytes")
	}
}

func TestPutGeneratesKeyAndReportsJSON(t *testing.T) {
	node, addr := startFakeNode(t)
	ta := newTestApp(t)
	ta.mustRun(t, "", "join", addr)

	out, _ := ta.mustRun(t, "auto", "--json", "put")
	res := decodeJSON(t, out)
	key, _ := res["key"].(string)
	if len(key) != 36 || res["generated_key"] != true {
		t.Fatalf("generated key result = %v", res)
	}
	if res["quorum_status"] != "confirmed" || res["ttl_local"] != float64(1800) {
		t.Fatalf("result = %v", res)
	}
	if _, ok := res["ttl_requested"]; ok {
		t.Fatal("ttl_requested should be absent when no --ttl given")
	}
	if v, _ := node.Value(key); string(v) != "auto" {
		t.Fatalf("value under generated key = %q", v)
	}
}

func TestPutReportsClampedTTL(t *testing.T) {
	_, addr := startFakeNode(t)
	ta := newTestApp(t)
	ta.mustRun(t, "", "join", addr)

	_, errOut := ta.mustRun(t, "v", "put", "k", "--ttl", "5")
	if !strings.Contains(errOut, "local ttl 300s (requested 5s, clamped by node)") {
		t.Fatalf("clamp not reported: %q", errOut)
	}
	out, _ := ta.mustRun(t, "v", "--json", "put", "k", "--ttl", "5")
	res := decodeJSON(t, out)
	if res["ttl_requested"] != float64(5) || res["ttl_local"] != float64(300) {
		t.Fatalf("json ttl fields = %v", res)
	}
}

func TestPutPendingAndRequireConfirmed(t *testing.T) {
	node, addr := startFakeNode(t)
	node.Pending = true
	ta := newTestApp(t)
	ta.mustRun(t, "", "join", addr)

	out, errOut := ta.mustRun(t, "v", "put", "k")
	if strings.TrimSpace(out) != "k" || !strings.Contains(errOut, "quorum pending") {
		t.Fatalf("pending put: out %q, err %q", out, errOut)
	}
	out, _ = ta.mustRun(t, "v", "--json", "put", "k")
	if decodeJSON(t, out)["quorum_status"] != "pending" {
		t.Fatalf("json = %s", out)
	}
	code, out, errOut := ta.run("v", "put", "k", "--require-confirmed")
	if code != exitPending {
		t.Fatalf("--require-confirmed: exit %d, %q %q", code, out, errOut)
	}
	if strings.TrimSpace(out) != "k" {
		t.Fatalf("key should still be printed on pending: %q", out)
	}
	if v, _ := node.Value("k"); string(v) != "v" {
		t.Fatal("pending write should still have been stored")
	}
}

func TestPutStoreFullIsServerError(t *testing.T) {
	node, addr := startFakeNode(t)
	node.Full = true
	ta := newTestApp(t)
	ta.mustRun(t, "", "join", addr)
	code, _, errOut := ta.run("v", "put", "k")
	if code != exitError || !strings.Contains(errOut, "capacity") {
		t.Fatalf("exit %d, %q", code, errOut)
	}
}

func TestPutRejectsBadArgs(t *testing.T) {
	_, addr := startFakeNode(t)
	ta := newTestApp(t)
	ta.mustRun(t, "", "join", addr)
	for _, args := range [][]string{
		{"put", "k", "--ttl", "-1"},
		{"put", "a", "b"},
		{"put", "k", "--file", filepath.Join(t.TempDir(), "missing")},
		{"put", "a/b"},
	} {
		code, _, _ := ta.run("v", args...)
		want := exitUsage
		if args[len(args)-2] == "--file" {
			want = exitError
		}
		if code != want {
			t.Fatalf("%v: exit %d, want %d", args, code, want)
		}
	}
}

func TestGetAndExistsMissing(t *testing.T) {
	_, addr := startFakeNode(t)
	ta := newTestApp(t)
	ta.mustRun(t, "", "join", addr)

	code, out, errOut := ta.run("", "get", "nope")
	if code != exitNotFound || out != "" || !strings.Contains(errOut, "not found on contacted node") {
		t.Fatalf("get missing: exit %d, out %q, err %q", code, out, errOut)
	}
	code, _, _ = ta.run("", "exists", "nope")
	if code != exitNotFound {
		t.Fatalf("exists missing: exit %d", code)
	}
	code, out, _ = ta.run("", "--json", "exists", "nope")
	if code != exitNotFound || decodeJSON(t, out)["exists"] != false {
		t.Fatalf("json exists missing: exit %d, %q", code, out)
	}

	ta.mustRun(t, "payload", "put", "k", "--ttl", "600")
	out, _ = ta.mustRun(t, "", "exists", "k")
	if !strings.HasPrefix(out, "k\tremaining 590s of 600s") {
		t.Fatalf("exists = %q", out)
	}
	out, _ = ta.mustRun(t, "", "--json", "exists", "k")
	res := decodeJSON(t, out)
	if res["exists"] != true || res["ttl_seconds"] != float64(600) || res["remaining_seconds"] != float64(590) {
		t.Fatalf("json exists = %v", res)
	}
	out, _ = ta.mustRun(t, "", "--json", "get", "k")
	res = decodeJSON(t, out)
	decoded, _ := base64.StdEncoding.DecodeString(res["value_base64"].(string))
	if string(decoded) != "payload" || res["encoding"] != "base64" {
		t.Fatalf("json get = %v", res)
	}
}

func TestGetEmptyValueIsNotMissing(t *testing.T) {
	_, addr := startFakeNode(t)
	ta := newTestApp(t)
	ta.mustRun(t, "", "join", addr)
	ta.mustRun(t, "", "put", "empty")
	code, out, _ := ta.run("", "get", "empty")
	if code != exitOK || out != "" {
		t.Fatalf("empty get: exit %d, out %q", code, out)
	}
	code, _, _ = ta.run("", "exists", "empty")
	if code != exitOK {
		t.Fatalf("empty exists: exit %d", code)
	}
}

// ---------- list ----------

func TestListPagination(t *testing.T) {
	node, addr := startFakeNode(t)
	ta := newTestApp(t)
	ta.mustRun(t, "", "join", addr)
	for _, k := range []string{"demo:1", "demo:2", "demo:3", "other"} {
		ta.mustRun(t, "v", "put", k)
	}

	out, errOut := ta.mustRun(t, "", "list", "--prefix", "demo:", "--limit", "2")
	if out != "demo:1\ndemo:2\n" || !strings.Contains(errOut, `--cursor "demo:2"`) {
		t.Fatalf("page 1: out %q err %q", out, errOut)
	}
	out, errOut = ta.mustRun(t, "", "list", "--prefix", "demo:", "--limit", "2", "--cursor", "demo:2")
	if out != "demo:3\n" || errOut != "" {
		t.Fatalf("page 2: out %q err %q", out, errOut)
	}
	out, _ = ta.mustRun(t, "", "--json", "list", "--prefix", "demo:", "--limit", "2")
	res := decodeJSON(t, out)
	if res["next_cursor"] != "demo:2" {
		t.Fatalf("json page = %v", res)
	}
	out, _ = ta.mustRun(t, "", "list", "--prefix", "demo:", "--limit", "2", "--all")
	if out != "demo:1\ndemo:2\ndemo:3\n" {
		t.Fatalf("--all = %q", out)
	}
	out, _ = ta.mustRun(t, "", "list")
	if out != "demo:1\ndemo:2\ndemo:3\nother\n" {
		t.Fatalf("unbounded = %q", out)
	}

	node.NullKeys = true
	out, _ = ta.mustRun(t, "", "--json", "list")
	if !strings.Contains(out, `"keys":[]`) {
		t.Fatalf("null keys should serialize as []: %s", out)
	}
	code, _, _ := ta.run("", "list", "--all", "--cursor", "x")
	if code != exitUsage {
		t.Fatalf("--all with --cursor: exit %d", code)
	}
}

// ---------- diagnostics ----------

func TestDiagnostics(t *testing.T) {
	_, addr := startFakeNode(t)
	ta := newTestApp(t)
	ta.mustRun(t, "", "join", addr)

	out, _ := ta.mustRun(t, "", "health")
	if !strings.Contains(out, `"node_id": "fake-1"`) {
		t.Fatalf("health = %q", out)
	}
	out, _ = ta.mustRun(t, "", "--json", "status")
	if decodeJSON(t, out)["uptime"] != "1s" {
		t.Fatalf("status = %q", out)
	}
	out, _ = ta.mustRun(t, "", "topology")
	if !strings.Contains(out, `"peers": []`) {
		t.Fatalf("topology = %q", out)
	}
	out, _ = ta.mustRun(t, "", "metrics")
	if !strings.HasPrefix(out, "# HELP http_requests_total") {
		t.Fatalf("metrics = %q", out)
	}
}

// ---------- reachability, timeout, cancellation ----------

func TestSavedNetworkUnreachable(t *testing.T) {
	_, addr := startFakeNode(t)
	ta := newTestApp(t)
	ta.mustRun(t, "", "join", addr)
	cfg, _ := loadConfig(ta.configPath)
	dead := cfg.Networks[cfg.Current]
	dead.Endpoint = "http://127.0.0.1:1"
	cfg.Networks["dead"] = dead
	saveConfig(ta.configPath, cfg)

	code, _, errOut := ta.run("v", "--network", "dead", "put", "k")
	if code != exitUnreachable || !strings.Contains(errOut, "unreachable") {
		t.Fatalf("exit %d, %q", code, errOut)
	}
}

func hangingNode(t *testing.T) string {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			w.Write([]byte(`{"status":"healthy","node_id":"slow","network":"private","enclave":"default"}`))
			return
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})
	return strings.TrimPrefix(srv.URL, "http://")
}

func TestTimeoutIsUnreachable(t *testing.T) {
	addr := hangingNode(t)
	ta := newTestApp(t)
	ta.mustRun(t, "", "join", addr)
	start := time.Now()
	code, _, errOut := ta.run("", "--timeout", "100ms", "get", "k")
	if code != exitUnreachable || !strings.Contains(errOut, "deadline exceeded") {
		t.Fatalf("exit %d, %q", code, errOut)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("timeout took %v", time.Since(start))
	}
	code, _, _ = ta.run("", "--timeout", "0", "get", "k")
	if code != exitUsage {
		t.Fatalf("zero timeout: exit %d", code)
	}
}

func TestCancellationIsUnreachable(t *testing.T) {
	addr := hangingNode(t)
	ta := newTestApp(t)
	ta.mustRun(t, "", "join", addr)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	code, _, _ := ta.runCtx(ctx, "v", "put", "k")
	if code != exitUnreachable {
		t.Fatalf("cancelled put: exit %d", code)
	}
}

// ---------- public discovery ----------

// mapResolver is an in-memory TXT resolver; missing names return a DNS
// not-found error, which satisfies net.Error.
type mapResolver map[string][]string

func (m mapResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	if recs, ok := m[name]; ok {
		return recs, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func signedListFor(t *testing.T, priv ed25519.PrivateKey, nodes []string, expires time.Time) string {
	t.Helper()
	list := &trust.SignedList{Version: trust.OmegaVersion, Expires: expires.Unix(), Nodes: nodes}
	list.Sign(priv)
	return list.Encode()
}

func TestPublicJoinWithTestAnchor(t *testing.T) {
	node, addr := startFakeNode(t)
	node.Network = "public"
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	dns := mapResolver{
		"_bootstrap.test": {"omega=_omega.test"},
		"_omega.test":     {signedListFor(t, priv, []string{"127.0.0.1:1", addr}, time.Now().Add(time.Hour))},
	}
	ta := newTestApp(t)
	ta.app.publicDiscovery = newPublicDiscovery(pub, trust.DNSConfig{Resolver: dns, BootstrapName: "_bootstrap.test"})

	out, errOut := ta.mustRun(t, "", "join")
	if !strings.Contains(out, "public, signed-list") || !strings.Contains(out, "http://"+addr) {
		t.Fatalf("public join out = %q", out)
	}
	if !strings.Contains(errOut, "root 127.0.0.1:1 did not answer") {
		t.Fatalf("dead root should be reported: %q", errOut)
	}
	cfg, _ := loadConfig(ta.configPath)
	saved := cfg.Networks["public"]
	if cfg.Current != "public" || saved.Mode != modePublic || saved.Discovery != discoverySignedList || len(saved.Roots) != 2 {
		t.Fatalf("saved = %+v", saved)
	}
	ta.mustRun(t, "v", "put", "k")
	if v, _ := node.Value("k"); string(v) != "v" {
		t.Fatal("put through public profile did not reach the root")
	}

	// Wrong trust anchor: verification fails, and that is not a
	// reachability problem.
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	ta.app.publicDiscovery = newPublicDiscovery(otherPub, trust.DNSConfig{Resolver: dns, BootstrapName: "_bootstrap.test"})
	code, _, errOut := ta.run("", "join", "--name", "bad")
	if code != exitError || !strings.Contains(errOut, "public discovery unavailable") {
		t.Fatalf("bad anchor: exit %d, %q", code, errOut)
	}
	if _, ok := loadOrEmpty(ta.configPath).Networks["bad"]; ok {
		t.Fatal("failed public join must not save a profile")
	}

	// No records at all: DNS failure is a reachability problem.
	ta.app.publicDiscovery = newPublicDiscovery(pub, trust.DNSConfig{Resolver: mapResolver{}, BootstrapName: "_bootstrap.test"})
	code, _, errOut = ta.run("", "join")
	if code != exitUnreachable || !strings.Contains(errOut, "public discovery unavailable") {
		t.Fatalf("no dns: exit %d, %q", code, errOut)
	}
}

func loadOrEmpty(path string) *Config {
	cfg, err := loadConfig(path)
	if err != nil {
		return newConfig()
	}
	return cfg
}

func TestPublicProfileExpiresWithSignedList(t *testing.T) {
	_, addr := startFakeNode(t)
	ta := newTestApp(t)
	cfg := newConfig()
	cfg.Current = "public"
	cfg.Networks["public"] = Network{
		Endpoint: "http://" + addr, Mode: modePublic, Discovery: discoverySignedList,
		Roots: []string{addr}, RootsExpire: time.Now().Add(-time.Minute).Unix(),
	}
	if err := saveConfig(ta.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := ta.run("", "get", "k")
	if code != exitUsage || !strings.Contains(errOut, "signed root list expired") {
		t.Fatalf("exit %d, %q", code, errOut)
	}
}

func TestPublicOperatorSuppliedEntryPoint(t *testing.T) {
	node, addr := startFakeNode(t)
	ta := newTestApp(t)

	// Node says private, user says public: saved as public/operator-supplied
	// with both a note and a warning, and never marked as discovered.
	_, errOut := ta.mustRun(t, "", "join", addr, "--public", "--name", "pub")
	if !strings.Contains(errOut, "not verified against the signed public root list") {
		t.Fatalf("missing trust note: %q", errOut)
	}
	if !strings.Contains(errOut, `node reports network "private", not public`) {
		t.Fatalf("missing mismatch warning: %q", errOut)
	}
	cfg, _ := loadConfig(ta.configPath)
	saved := cfg.Networks["pub"]
	if saved.Mode != modePublic || saved.Discovery != discoveryOperatorSupplied || len(saved.Roots) != 0 {
		t.Fatalf("saved = %+v", saved)
	}

	// Node says public, user did not pass --public: saved private with a hint.
	node.Network = "public"
	_, errOut = ta.mustRun(t, "", "join", addr, "--name", "priv")
	if !strings.Contains(errOut, "use --public") {
		t.Fatalf("missing hint: %q", errOut)
	}
	cfg, _ = loadConfig(ta.configPath)
	if cfg.Networks["priv"].Mode != modePrivate {
		t.Fatalf("saved mode = %q", cfg.Networks["priv"].Mode)
	}
}

// ---------- config helpers ----------

func TestConfigHelpers(t *testing.T) {
	names := map[string]string{
		"http://192.168.0.1:8080":  "192.168.0.1",
		"http://node.example:9000": "node.example",
		"https://[::1]:8080":       "1",
		"http://a_b-c.d:1":         "a_b-c.d",
	}
	for in, want := range names {
		if got := defaultNetworkName(in); got != want {
			t.Errorf("defaultNetworkName(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", "has space", "slash/name", strings.Repeat("x", 65)} {
		if err := validateNetworkName(bad); err == nil {
			t.Errorf("validateNetworkName(%q) accepted", bad)
		}
	}
	for _, good := range []string{"lab", "site-2", "a.b_c"} {
		if err := validateNetworkName(good); err != nil {
			t.Errorf("validateNetworkName(%q) rejected: %v", good, err)
		}
	}

	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	env["HOME"] = "/home/u"
	if d, _ := resolveConfigDir(getenv); d != "/home/u/.config/sopholeth" {
		t.Errorf("home dir = %q", d)
	}
	env["XDG_CONFIG_HOME"] = "/xdg"
	if d, _ := resolveConfigDir(getenv); d != "/xdg/sopholeth" {
		t.Errorf("xdg dir = %q", d)
	}
	env["SOPH_CONFIG_DIR"] = "/explicit"
	if d, _ := resolveConfigDir(getenv); d != "/explicit" {
		t.Errorf("explicit dir = %q", d)
	}

	// A corrupt config file is reported, not silently replaced.
	path := filepath.Join(t.TempDir(), configFileName)
	os.WriteFile(path, []byte("{not json"), 0o600)
	if _, err := loadConfig(path); err == nil {
		t.Error("corrupt config loaded without error")
	}
}
