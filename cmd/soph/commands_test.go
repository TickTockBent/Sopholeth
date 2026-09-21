package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"sopholeth/internal/client"
	"sopholeth/internal/client/clienttest"
	"sopholeth/internal/trust"
)

func TestCommandHelpHasNoSideEffects(t *testing.T) {
	commands := map[string]string{
		"join": "-name", "put": "-ttl", "get": "-output", "list": "-cursor",
		"use": "<name>", "forget": "<name>", "networks": "current",
		"exists": "<key>", "head": "<key>", "health": "health",
		"status": "status", "topology": "topology", "metrics": "Prometheus", "version": "version",
	}
	for cmd, want := range commands {
		for _, args := range [][]string{{cmd, "--help"}, {cmd, "-h"}, {"help", cmd}} {
			t.Run(strings.Join(args, " "), func(t *testing.T) {
				ta := newTestApp(t)
				// Even a corrupt config cannot interfere with help.
				before := []byte("{bad config")
				if err := os.WriteFile(ta.configPath, before, 0o600); err != nil {
					t.Fatal(err)
				}
				ta.app.getenv = func(string) string { t.Fatal("help accessed environment"); return "" }
				ta.app.newHTTPClient = func() *http.Client { t.Fatal("help created a client"); return nil }
				ta.app.publicDiscovery = func(context.Context) (*trust.SignedList, error) {
					t.Fatal("help attempted discovery")
					return nil, nil
				}
				code, out, errOut := ta.run("", args...)
				if code != exitOK || errOut != "" || !strings.Contains(out, "Usage: soph") || !strings.Contains(out, want) {
					t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
				}
				after, err := os.ReadFile(ta.configPath)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("help changed config: %q, %v", after, err)
				}
			})
		}
	}
}

func TestHelpAfterPositionalsAndTerminator(t *testing.T) {
	ta := newTestApp(t)
	code, out, errOut := ta.run("", "put", "key", "--help")
	if code != exitOK || !strings.Contains(out, "-ttl") || errOut != "" {
		t.Fatalf("interspersed help: exit %d, %q, %q", code, out, errOut)
	}
	_, addr := startFakeNode(t)
	ta.mustRun(t, "", "join", addr)
	ta.mustRun(t, "value", "put", "--", "--help")
	out, _ = ta.mustRun(t, "", "get", "--", "--help")
	if out != "value" {
		t.Fatalf("literal --help key: %q", out)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

func TestHumanOutputFailureIsLocalError(t *testing.T) {
	_, addr := startFakeNode(t)
	for _, args := range [][]string{
		{"join", addr, "--name", "other"}, {"use", "lab"}, {"networks"}, {"forget", "lab"},
		{"exists", "k"}, {"list"}, {"get", "k"}, {"health"}, {"status"}, {"topology"}, {"metrics"},
		{"help"}, {"--help"}, {"put", "--help"}, {"version"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			ta := newTestApp(t)
			ta.mustRun(t, "", "join", addr, "--name", "lab")
			ta.mustRun(t, "v", "put", "k")
			ta.app.stdout = failingWriter{}
			code, _, errOut := ta.run("", args...)
			if code != exitError || !strings.Contains(errOut, "output unavailable") {
				t.Fatalf("exit %d, stderr %q", code, errOut)
			}
		})
	}
	// The empty-network branch must report output failure too.
	ta := newTestApp(t)
	ta.app.stdout = failingWriter{}
	code, _, _ := ta.run("", "networks")
	if code != exitError {
		t.Fatalf("empty networks: exit %d", code)
	}
}

func TestPutOutputFailurePreservesKnownOutcome(t *testing.T) {
	for _, pending := range []bool{false, true} {
		for _, jsonOut := range []bool{false, true} {
			t.Run(fmt.Sprintf("pending=%t/json=%t", pending, jsonOut), func(t *testing.T) {
				node, addr := startFakeNode(t)
				node.Pending = pending
				ta := newTestApp(t)
				ta.mustRun(t, "", "join", addr)
				ta.app.stdout = failingWriter{}
				args := []string{"put", "--require-confirmed"}
				if jsonOut {
					args = append([]string{"--json"}, args...)
				}
				code, _, errOut := ta.run("payload", args...)
				page, err := client.New("http://"+addr, &http.Client{}).ListKeys(context.Background(), "", 0, "")
				if err != nil || len(page.Keys) != 1 {
					t.Fatalf("stored keys = %v, %v", page, err)
				}
				key := page.Keys[0]
				if value, _ := node.Value(key); string(value) != "payload" {
					t.Fatalf("stored value = %q", value)
				}
				status := "confirmed"
				if pending {
					status = "pending"
				}
				if code != exitError || !strings.Contains(errOut, fmt.Sprintf("write of %q stored locally, quorum %s", key, status)) ||
					!strings.Contains(errOut, "output unavailable") {
					t.Fatalf("exit %d, stderr %q", code, errOut)
				}
				puts := 0
				for _, request := range node.Requests {
					if strings.HasPrefix(request, "PUT ") {
						puts++
					}
				}
				if puts != 1 {
					t.Fatalf("output failure caused %d writes", puts)
				}
			})
		}
	}
}

func TestJoinRejectsInvalidHealthWithoutChangingConfig(t *testing.T) {
	_, goodAddr := startFakeNode(t)
	for _, body := range []string{
		`{}`, `null`, `{"status":"unhealthy","node_id":"n","network":"private","enclave":"default"}`,
		`{"status":"healthy","network":"private","enclave":"default"}`,
		`{"status":"healthy","node_id":"n","enclave":"default"}`,
		`{"status":"healthy","node_id":"n","network":"private"}`,
		`{"status":"healthy","node_id":" ","network":"private","enclave":"default"}`,
		`{"status":"healthy","node_id":"n","network":"unknown","enclave":"default"}`,
	} {
		t.Run(body, func(t *testing.T) {
			bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, body)
			}))
			defer bad.Close()
			ta := newTestApp(t)
			code, _, errOut := ta.run("", "join", bad.URL, "--name", "lab")
			if code != exitError || !strings.Contains(errOut, "cannot join") {
				t.Fatalf("new join: exit %d, stderr %q", code, errOut)
			}
			if _, err := os.Stat(ta.configPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid health created config: %v", err)
			}
			ta.mustRun(t, "", "join", goodAddr, "--name", "lab")
			ta.mustRun(t, "", "join", goodAddr, "--name", "current")
			before, err := os.ReadFile(ta.configPath)
			if err != nil {
				t.Fatal(err)
			}
			code, _, errOut = ta.run("", "join", bad.URL, "--name", "lab")
			after, err := os.ReadFile(ta.configPath)
			if code != exitError || err != nil || !bytes.Equal(before, after) {
				t.Fatalf("replacement join changed config: exit %d, stderr %q, config %q, err %v", code, errOut, after, err)
			}
		})
	}
}

func TestPublicJoinSkipsInvalidHealth(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) }))
	defer bad.Close()
	_, goodAddr := startFakeNode(t)
	ta := newTestApp(t)
	ta.app.publicDiscovery = func(context.Context) (*trust.SignedList, error) {
		return &trust.SignedList{Nodes: []string{bad.URL, goodAddr}, Expires: time.Now().Add(time.Hour).Unix()}, nil
	}
	ta.mustRun(t, "", "join")
	cfg, err := loadConfig(ta.configPath)
	if err != nil || cfg.Networks["public"].Endpoint != "http://"+goodAddr {
		t.Fatalf("selected invalid root: %+v, %v", cfg, err)
	}
}

type notifiedReader struct {
	io.Reader
	started chan struct{}
}

func (r notifiedReader) Read(p []byte) (int, error) {
	select {
	case r.started <- struct{}{}:
	default:
	}
	return r.Reader.Read(p)
}

func TestPutCanCancelBlockedStdin(t *testing.T) {
	node, addr := startFakeNode(t)
	ta := newTestApp(t)
	ta.mustRun(t, "", "join", addr)
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	started := make(chan struct{}, 1)
	ta.app.stdin = notifiedReader{reader, started}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- ta.app.run(ctx, []string{"put", "k"}) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("put did not begin reading stdin")
	}
	cancel()
	select {
	case code := <-done:
		if code != exitUnreachable || !strings.Contains(ta.stderr.String(), "context canceled") {
			t.Fatalf("exit %d, stderr %q", code, ta.stderr.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("put ignored cancellation while stdin was still open")
	}
	if _, ok := node.Value("k"); ok {
		t.Fatal("cancelled input was sent to node")
	}
}

func TestPutValidatesNetworkBeforeReading(t *testing.T) {
	ta := newTestApp(t)
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	ta.app.stdin = reader
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if code := ta.app.run(ctx, []string{"put", "k"}); code != exitUsage {
		t.Fatalf("exit %d, stderr %q", code, ta.stderr.String())
	}
}

func TestPutConcurrentOverwriteIsOnlyObservedMetadata(t *testing.T) {
	node := clienttest.New()
	handler := node.Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			// Another writer replaces the value after the CLI's PUT and
			// before its follow-up HEAD, with a different TTL.
			overwrite := httptest.NewRequest(http.MethodPut, "/v1/data/shared", strings.NewReader("other writer"))
			overwrite.Header.Set("X-TTL", "3600")
			handler.ServeHTTP(httptest.NewRecorder(), overwrite)
		}
		handler.ServeHTTP(w, r)
	}))
	defer srv.Close()
	ta := newTestApp(t)
	ta.mustRun(t, "", "join", srv.URL)
	_, errOut := ta.mustRun(t, "first", "put", "shared", "--ttl", "300")
	if !strings.Contains(errOut, "requested ttl 300s") ||
		!strings.Contains(errOut, `observed current key "shared": local ttl 3600s (may reflect a subsequent write)`) ||
		strings.Contains(errOut, "clamped") {
		t.Fatalf("misleading TTL report: %q", errOut)
	}
	out, _ := ta.mustRun(t, "first", "--json", "put", "shared", "--ttl", "300")
	res := decodeJSON(t, out)
	observed, _ := res["observed_current_key"].(map[string]any)
	if observed["ttl_seconds"] != float64(3600) || res["ttl_requested"] != float64(300) ||
		res["bytes"] != float64(5) || res["quorum_status"] != "confirmed" {
		t.Fatalf("write result and observed metadata: %v", res)
	}
	if _, ok := res["ttl_local"]; ok {
		t.Fatal("attributed another writer's TTL to this write")
	}
	if _, ok := res["created_at"]; ok {
		t.Fatal("attributed another writer's timestamp to this write")
	}
	if value, _ := node.Value("shared"); string(value) != "other writer" {
		t.Fatalf("overwrite did not occur: %q", value)
	}
}
