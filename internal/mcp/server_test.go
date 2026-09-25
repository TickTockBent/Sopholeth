package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"sopholeth/internal/cluster"
)

// newTestServer returns a Server wired to a single-node cluster, ready for
// in-process MCP calls. Tests construct the cluster directly (skipping the
// gossip listener) because the MCP handlers only touch ClusterNode.Put,
// GetWithMetadata, and Scan — no peers are involved.
func newTestServer(t *testing.T) (*cluster.ClusterNode, *Server, *io.PipeReader, *io.PipeWriter, *io.PipeReader, *io.PipeWriter) {
	t.Helper()

	cn := cluster.NewClusterNode(
		"mcp-test",
		"localhost",
		0, // gossip port — not started
		0,
		1, // replication factor; quorum of 1 lets writes complete locally
		0,
		5*time.Second,
		"",
		"default",
	)

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srv := NewServer(cn, inR, outW, 300, 86400)
	return cn, srv, inR, inW, outR, outW
}

// runOne starts srv, writes one request, reads one response, then closes input.
func runOne(t *testing.T, srv *Server, inW io.WriteCloser, outR io.Reader, req string) map[string]any {
	t.Helper()

	done := make(chan error, 1)
	go func() {
		done <- srv.Run(context.Background())
	}()

	if _, err := io.WriteString(inW, req+"\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}

	br := bufio.NewReader(outR)
	line, err := br.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	_ = inW.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("server did not exit after stdin close")
	}

	var resp map[string]any
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v", line, err)
	}
	return resp
}

func TestInitialize(t *testing.T) {
	_, srv, _, inW, outR, outW := newTestServer(t)
	defer outW.Close()

	resp := runOne(t, srv, inW, outR,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`)

	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %+v", resp)
	}
	if got := result["protocolVersion"]; got != "2024-11-05" {
		t.Errorf("protocolVersion = %v, want 2024-11-05", got)
	}
	info, ok := result["serverInfo"].(map[string]any)
	if !ok || info["name"] != "sopholeth" {
		t.Errorf("unexpected serverInfo: %+v", result["serverInfo"])
	}
}

func TestToolsList(t *testing.T) {
	_, srv, _, inW, outR, outW := newTestServer(t)
	defer outW.Close()

	resp := runOne(t, srv, inW, outR,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)

	result := resp["result"].(map[string]any)
	tools := result["tools"].([]any)
	if len(tools) != 4 {
		t.Fatalf("expected 4 tools, got %d", len(tools))
	}

	names := map[string]bool{}
	for _, raw := range tools {
		t := raw.(map[string]any)
		names[t["name"].(string)] = true
	}
	for _, want := range []string{"store", "retrieve", "exists", "list_keys"} {
		if !names[want] {
			t.Errorf("missing tool: %s", want)
		}
	}
}

// runStream lets tests send multiple requests and collect responses on the
// same Server.Run invocation.
type stream struct {
	t     *testing.T
	srv   *Server
	inW   io.WriteCloser
	br    *bufio.Reader
	done  chan error
	idSeq int
	mu    sync.Mutex
	cn    *cluster.ClusterNode
	outW  io.Closer
}

func openStream(t *testing.T) *stream {
	t.Helper()
	cn, srv, _, inW, outR, outW := newTestServer(t)
	s := &stream{
		t:    t,
		srv:  srv,
		inW:  inW,
		br:   bufio.NewReader(outR),
		done: make(chan error, 1),
		cn:   cn,
		outW: outW,
	}
	go func() { s.done <- srv.Run(context.Background()) }()
	return s
}

func (s *stream) call(method, params string) map[string]any {
	s.t.Helper()
	s.mu.Lock()
	s.idSeq++
	id := s.idSeq
	s.mu.Unlock()
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != "" {
		req["params"] = json.RawMessage(params)
	}
	enc, _ := json.Marshal(req)
	if _, err := s.inW.Write(append(enc, '\n')); err != nil {
		s.t.Fatalf("write: %v", err)
	}
	line, err := s.br.ReadBytes('\n')
	if err != nil {
		s.t.Fatalf("read: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal(line, &resp); err != nil {
		s.t.Fatalf("unmarshal %q: %v", line, err)
	}
	return resp
}

func (s *stream) close() {
	_ = s.inW.Close()
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
		s.t.Fatalf("server did not exit")
	}
	_ = s.outW.Close()
	_ = s.cn.Stop()
}

// toolContent extracts the JSON-marshalled text payload from a tool/call
// response and re-parses it.
func toolContent(t *testing.T, resp map[string]any) map[string]any {
	t.Helper()
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %+v", resp)
	}
	content := result["content"].([]any)
	first := content[0].(map[string]any)
	text := first["text"].(string)
	if text == "null" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("inner unmarshal %q: %v", text, err)
	}
	return out
}

func TestStoreRetrieveRoundtrip(t *testing.T) {
	s := openStream(t)
	defer s.close()

	resp := s.call("tools/call", `{"name":"store","arguments":{"data":"hello world","ttl_seconds":600,"key":"greeting"}}`)
	stored := toolContent(t, resp)
	if stored["key"] != "greeting" {
		t.Errorf("key = %v, want greeting", stored["key"])
	}
	if stored["ttl_seconds"].(float64) != 600 {
		t.Errorf("ttl_seconds = %v, want 600", stored["ttl_seconds"])
	}
	// Single-node test cluster reaches quorum (1) locally, so the write
	// is confirmed synchronously. "pending" would indicate a regression
	// in the local-quorum fast path.
	if stored["quorum_status"] != "confirmed" {
		t.Errorf("quorum_status = %v, want confirmed", stored["quorum_status"])
	}

	resp = s.call("tools/call", `{"name":"retrieve","arguments":{"key":"greeting"}}`)
	got := toolContent(t, resp)
	if got["data"] != "hello world" {
		t.Errorf("data = %v, want hello world", got["data"])
	}
}

func TestRetrieveMissing(t *testing.T) {
	s := openStream(t)
	defer s.close()

	resp := s.call("tools/call", `{"name":"retrieve","arguments":{"key":"nope"}}`)
	result := resp["result"].(map[string]any)
	content := result["content"].([]any)
	first := content[0].(map[string]any)
	if first["text"] != "null" {
		t.Errorf("expected text 'null', got %v", first["text"])
	}
}

func TestExists(t *testing.T) {
	s := openStream(t)
	defer s.close()

	// Missing key → exists:false.
	resp := s.call("tools/call", `{"name":"exists","arguments":{"key":"missing"}}`)
	got := toolContent(t, resp)
	if got["exists"] != false {
		t.Errorf("exists for missing = %v, want false", got["exists"])
	}

	// Store then exists:true.
	_ = s.call("tools/call", `{"name":"store","arguments":{"data":"v","ttl_seconds":600,"key":"alive"}}`)
	resp = s.call("tools/call", `{"name":"exists","arguments":{"key":"alive"}}`)
	got = toolContent(t, resp)
	if got["exists"] != true {
		t.Errorf("exists for stored = %v, want true", got["exists"])
	}
	if got["remaining_ttl_seconds"] == nil {
		t.Errorf("missing remaining_ttl_seconds")
	}
}

func TestListKeys(t *testing.T) {
	s := openStream(t)
	defer s.close()

	_ = s.call("tools/call", `{"name":"store","arguments":{"data":"a","ttl_seconds":600,"key":"proj-x/a"}}`)
	_ = s.call("tools/call", `{"name":"store","arguments":{"data":"b","ttl_seconds":600,"key":"proj-x/b"}}`)
	_ = s.call("tools/call", `{"name":"store","arguments":{"data":"c","ttl_seconds":600,"key":"other"}}`)

	resp := s.call("tools/call", `{"name":"list_keys","arguments":{"prefix":"proj-x/"}}`)
	got := toolContent(t, resp)
	keys := got["keys"].([]any)
	if len(keys) != 2 {
		t.Fatalf("got %d keys, want 2: %+v", len(keys), keys)
	}
	if keys[0] != "proj-x/a" || keys[1] != "proj-x/b" {
		t.Errorf("unexpected keys: %+v", keys)
	}

	// No prefix → all 3.
	resp = s.call("tools/call", `{"name":"list_keys","arguments":{}}`)
	got = toolContent(t, resp)
	keys = got["keys"].([]any)
	if len(keys) != 3 {
		t.Errorf("got %d keys, want 3: %+v", len(keys), keys)
	}
}

func TestStoreGeneratesKey(t *testing.T) {
	s := openStream(t)
	defer s.close()

	resp := s.call("tools/call", `{"name":"store","arguments":{"data":"anon","ttl_seconds":600}}`)
	got := toolContent(t, resp)
	key, ok := got["key"].(string)
	if !ok || len(key) < 30 {
		t.Errorf("expected uuid-shaped key, got %q", key)
	}
}

func TestStoreOmittedTTLDefaultsToThirtyMinutes(t *testing.T) {
	s := openStream(t)
	defer s.close()

	resp := s.call("tools/call", `{"name":"store","arguments":{"data":"default","key":"default-ttl"}}`)
	got := toolContent(t, resp)
	if got["ttl_seconds"].(float64) != 1800 {
		t.Errorf("ttl_seconds = %v, want 1800", got["ttl_seconds"])
	}
}

func TestStoreClampsTTL(t *testing.T) {
	s := openStream(t)
	defer s.close()

	// The MCP and cluster policies match the node's five-minute floor.
	resp := s.call("tools/call", `{"name":"store","arguments":{"data":"x","ttl_seconds":1,"key":"clamped"}}`)
	got := toolContent(t, resp)
	if got["ttl_seconds"].(float64) != 300 {
		t.Errorf("ttl_seconds = %v, want 300 (clamped)", got["ttl_seconds"])
	}
	_, _, ttl, _ := s.cn.GetWithMetadata("clamped")
	if ttl != 300*time.Second {
		t.Fatalf("stored TTL = %s, want 5m", ttl)
	}
}

func TestUnknownTool(t *testing.T) {
	s := openStream(t)
	defer s.close()

	resp := s.call("tools/call", `{"name":"not_a_tool","arguments":{}}`)
	if resp["error"] == nil {
		t.Fatalf("expected JSON-RPC error, got %+v", resp)
	}
}

func TestNotificationProducesNoResponse(t *testing.T) {
	_, srv, _, inW, outR, outW := newTestServer(t)
	defer outW.Close()

	done := make(chan error, 1)
	go func() { done <- srv.Run(context.Background()) }()

	// Notification (no id field), then a real call so we can prove the
	// reader only saw one response on the wire.
	io.WriteString(inW, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")
	io.WriteString(inW, `{"jsonrpc":"2.0","id":7,"method":"tools/list"}`+"\n")

	br := bufio.NewReader(outR)
	line, err := br.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// The notification must not produce output; first frame is the tools/list reply.
	if !strings.Contains(string(line), `"id":7`) {
		t.Errorf("expected id=7 frame first, got %s", line)
	}

	_ = inW.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("server did not exit")
	}
}

func TestParseErrorEmitsResponse(t *testing.T) {
	_, srv, _, inW, outR, outW := newTestServer(t)
	defer outW.Close()

	done := make(chan error, 1)
	go func() { done <- srv.Run(context.Background()) }()

	io.WriteString(inW, "{not valid json\n")
	br := bufio.NewReader(outR)
	line, err := br.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Contains(line, []byte(`-32700`)) {
		t.Errorf("expected parse-error code, got %s", line)
	}
	_ = inW.Close()
	<-done
}
