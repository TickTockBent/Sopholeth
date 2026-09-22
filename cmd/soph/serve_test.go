package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type linkWriter struct{ links chan string }

func (w linkWriter) Write(p []byte) (int, error) { w.links <- string(p); return len(p), nil }

func TestServeEmbeddedViewerAndShutdown(t *testing.T) {
	_, addr := startFakeNode(t)
	for _, jsonOut := range []bool{false, true} {
		ta := newTestApp(t)
		ta.mustRun(t, "", "join", addr, "--name", "lab")
		links := make(chan string, 1)
		ta.app.stdout = linkWriter{links}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan int, 1)
		args := []string{"--network", "lab", "serve", "--port", "0", "--q", "hello & goodbye"}
		if jsonOut {
			args = append([]string{"--json"}, args...)
		}
		go func() { done <- ta.app.run(ctx, args) }()
		var output string
		select {
		case output = <-links:
		case <-time.After(3 * time.Second):
			t.Fatal("serve did not print its link")
		}
		link := strings.TrimSpace(output)
		if jsonOut {
			var result map[string]string
			if err := json.Unmarshal([]byte(output), &result); err != nil {
				t.Fatal(err)
			}
			link = result["url"]
			if result["node"] != "fake-1" || result["enclave"] != "default" {
				t.Fatal(result)
			}
		}
		u, err := url.Parse(link)
		if err != nil {
			t.Fatal(err)
		}
		if u.Hostname() != "127.0.0.1" || u.Query().Get("node") != "http://"+addr || u.Query().Get("q") != "hello & goodbye" {
			t.Fatal(u)
		}
		c := &http.Client{Timeout: 2 * time.Second}
		for _, path := range []string{"/", "/styles.css", "/viewer.js", "/v1/stream", "/stream.go"} {
			response, err := c.Get(u.Scheme + "://" + u.Host + path)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			want := 200
			if path == "/v1/stream" || path == "/stream.go" {
				want = 404
			}
			if response.StatusCode != want || len(body) == 0 {
				t.Fatalf("%s: %d, %s", path, response.StatusCode, body)
			}
		}
		cancel()
		select {
		case code := <-done:
			if code != exitOK {
				t.Fatalf("shutdown exit %d: %s", code, ta.stderr.String())
			}
		case <-time.After(4 * time.Second):
			t.Fatal("serve did not shut down")
		}
		cfg, _ := loadConfig(ta.configPath)
		if cfg.Current != "lab" {
			t.Fatal("serve changed the current network")
		}
	}
}

func TestServeEndpointSelection(t *testing.T) {
	ta := newTestApp(t)
	endpoint, err := ta.app.serveEndpoint("")
	if err != nil || endpoint != "http://localhost:8080" {
		t.Fatal(endpoint, err)
	}
	_, a := startFakeNode(t)
	_, b := startFakeNode(t)
	ta.mustRun(t, "", "join", a, "--name", "a")
	ta.mustRun(t, "", "join", b, "--name", "b")
	endpoint, err = ta.app.serveEndpoint("")
	if err != nil || endpoint != "http://"+b {
		t.Fatal(endpoint, err)
	}
	ta.env["SOPH_NETWORK"] = "a"
	endpoint, err = ta.app.serveEndpoint("")
	if err != nil || endpoint != "http://"+a {
		t.Fatal(endpoint, err)
	}
	ta.app.networkFlag = "b"
	endpoint, err = ta.app.serveEndpoint("")
	if err != nil || endpoint != "http://"+b {
		t.Fatal(endpoint, err)
	}
	ta.app.networkFlag = "missing"
	if _, err := ta.app.serveEndpoint(""); err == nil {
		t.Fatal("unknown selection silently fell back")
	}
	endpoint, err = ta.app.serveEndpoint(a)
	if err != nil || endpoint != "http://"+a {
		t.Fatal(endpoint, err)
	}
	for _, args := range [][]string{{"serve", "extra"}, {"serve", "--port", "-1"}, {"serve", "--node", "ftp://invalid"}, {"serve", "--bind", ""}} {
		code, _, errOut := ta.run("", args...)
		if code != exitUsage {
			t.Fatalf("%v: %d %s", args, code, errOut)
		}
	}
}
