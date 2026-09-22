package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
		for _, path := range []string{"/", "/styles.css", "/viewer.js", "/config.json", "/v1/status", "/stream.go"} {
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
			if path == "/v1/status" || path == "/stream.go" {
				want = 404
			}
			if response.StatusCode != want || len(body) == 0 {
				t.Fatalf("%s: %d, %s", path, response.StatusCode, body)
			}
			if path == "/config.json" {
				var config map[string]string
				if err := json.Unmarshal(body, &config); err != nil || config["node"] != "http://"+addr || config["q"] != "hello & goodbye" {
					t.Fatalf("startup selection without query parameters: %s (%v)", body, err)
				}
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

func TestServeViewerProxyReads(t *testing.T) {
	requests := make(chan *http.Request, 8)
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(context.Background())
		if r.URL.Path == "/v1/data/missing" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Remaining-TTL", "299")
		w.Header().Set("Set-Cookie", "node-cookie=unrelated")
		w.Write([]byte{0, 255, 128, 65})
	}))
	defer node.Close()
	target, _ := url.Parse(node.URL)
	viewer := httptest.NewTLSServer(http.StripPrefix("/proxy/8181", serveViewerHandler(target, "")))
	defer viewer.Close()
	c := viewer.Client()
	c.Timeout = 2 * time.Second
	req, _ := http.NewRequest(http.MethodGet, viewer.URL+"/proxy/8181/v1/data/hello%20%3F%23?node=http://untrusted.invalid", nil)
	req.Header.Set("Cookie", "editor-session=private")
	req.Header.Set("Authorization", "Bearer editor-credential")
	req.Header.Set("Accept", "application/octet-stream")
	response, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(body) != string([]byte{0, 255, 128, 65}) || response.StatusCode != http.StatusOK {
		t.Fatalf("proxied value: %v, %v", body, err)
	}
	if response.Header.Get("X-Remaining-TTL") != "299" || response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Set-Cookie") != "" {
		t.Fatal(response.Header)
	}
	upstream := <-requests
	if upstream.URL.Path != "/v1/data/hello ?#" || upstream.URL.RawQuery != "" || upstream.Host != target.Host {
		t.Fatal(upstream.URL, upstream.Host)
	}
	if upstream.Header.Get("Cookie") != "" || upstream.Header.Get("Authorization") != "" || upstream.Header.Get("Accept") != "application/octet-stream" {
		t.Fatal(upstream.Header)
	}
	response, err = c.Get(viewer.URL + "/proxy/8181/v1/data/missing")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatal(response.StatusCode)
	}
	<-requests
	for _, test := range []struct {
		method, path string
		status       int
	}{
		{"PUT", "/v1/data/key", http.StatusMethodNotAllowed},
		{"POST", "/v1/stream", http.StatusMethodNotAllowed},
		{"GET", "/v1/status", http.StatusNotFound},
		{"GET", "/health", http.StatusNotFound},
	} {
		req, _ := http.NewRequest(test.method, viewer.URL+"/proxy/8181"+test.path, nil)
		response, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != test.status {
			t.Fatalf("%s %s: %d", test.method, test.path, response.StatusCode)
		}
	}
	select {
	case request := <-requests:
		t.Fatalf("unexpected upstream request: %s %s", request.Method, request.URL)
	default:
	}
}

func TestServeProxiedStreamShutdown(t *testing.T) {
	closed := make(chan struct{})
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"status":"healthy","node_id":"fixture","network":"private","enclave":"test"}`)
		case "/v1/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: snapshot\ndata: {}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			close(closed)
		default:
			http.NotFound(w, r)
		}
	}))
	defer node.Close()
	ta := newTestApp(t)
	links := make(chan string, 1)
	ta.app.stdout = linkWriter{links}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- ta.app.run(ctx, []string{"serve", "--node", node.URL, "--port", "0"}) }()
	var link string
	select {
	case link = <-links:
	case code := <-done:
		t.Fatalf("serve exited %d: %s", code, ta.stderr.String())
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not start")
	}
	u, _ := url.Parse(strings.TrimSpace(link))
	c := &http.Client{Timeout: 5 * time.Second}
	response, err := c.Get(u.Scheme + "://" + u.Host + "/v1/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil || line != "event: snapshot\n" || response.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("unflushed stream: %q, %v", line, err)
	}
	cancel() // An active SSE viewer must not keep the CLI alive on Ctrl-C.
	select {
	case code := <-done:
		if code != exitOK {
			t.Fatalf("shutdown exit %d: %s", code, ta.stderr.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("active stream blocked shutdown")
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream stream was not closed")
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
