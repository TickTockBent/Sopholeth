package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"sopholeth/internal/client"
	"sopholeth/sites"
)

func (a *app) serveEndpoint(override string) (string, error) {
	n, err := a.serveSelection(context.Background(), override)
	return n.Endpoint, err
}

func (a *app) serveSelection(ctx context.Context, override string) (Network, error) {
	if override != "" {
		endpoint, err := client.NormalizeEndpoint(override)
		if err != nil {
			return Network{}, usagef("%v", err)
		}
		return Network{Endpoint: endpoint}, nil
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return Network{}, err
	}
	if a.networkFlag == "" && a.getenv("SOPH_NETWORK") == "" && cfg.Current == "" {
		endpoint, err := client.NormalizeEndpoint("localhost")
		return Network{Endpoint: endpoint}, err
	}
	_, n, _, err := a.connect(ctx)
	if err != nil {
		return Network{}, err
	}
	return *n, nil
}

func (a *app) cmdServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	node := fs.String("node", "", "node endpoint override (default selected network, or localhost:8080)")
	port := fs.Int("port", 8181, "viewer HTTP port (0 chooses a free port)")
	bind := fs.String("bind", "127.0.0.1", "viewer listen address")
	query := fs.String("q", "", "initial search in keys and payload previews")
	open := fs.Bool("open", false, "open the viewer in your browser")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usagef("serve takes no positional arguments")
	}
	if *port < 0 || *port > 65535 {
		return usagef("--port must be between 0 and 65535")
	}
	if *bind == "" {
		return usagef("--bind must not be empty")
	}
	selected, err := a.serveSelection(ctx, *node)
	if err != nil {
		return err
	}
	endpoint := selected.Endpoint
	health, err := a.probe(ctx, endpoint)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(*bind, strconv.Itoa(*port)))
	if err != nil {
		return fmt.Errorf("serve listen: %w", err)
	}
	defer listener.Close()
	host, portString, _ := net.SplitHostPort(listener.Addr().String())
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		host = "localhost"
	}
	link := url.URL{Scheme: "http", Host: net.JoinHostPort(host, portString), Path: "/"}
	params := url.Values{"node": {endpoint}}
	if *query != "" {
		params.Set("q", *query)
	}
	link.RawQuery = params.Encode()
	if a.jsonOut {
		err = a.writeJSON(map[string]any{"url": link.String(), "endpoint": endpoint, "node": health.NodeID, "enclave": health.Enclave})
	} else {
		err = a.printf("%s\n", link.String())
		fmt.Fprintf(a.stderr, "watching %s, enclave %s (%s)\n", health.NodeID, health.Enclave, endpoint)
	}
	if err != nil {
		return err
	}
	if *open {
		if err := openBrowser(link.String()); err != nil {
			fmt.Fprintf(a.stderr, "could not open browser: %v; use the link above\n", err)
		}
	}
	ctx, stopViewer := context.WithCancel(ctx)
	defer stopViewer()
	lease := &viewerLease{}
	lease.set(ctx, selected)
	defer lease.clear()
	if selected.Discovery == discoveryHTTPS {
		done := make(chan struct{})
		go func() { defer close(done); a.refreshViewer(ctx, lease, selected) }()
		defer func() { stopViewer(); <-done }()
	}
	hc := a.newHTTPClient()
	if selected.Discovery == discoveryHTTPS {
		hc, err = a.verifiedHTTPClient()
		if err != nil {
			return err
		}
	}
	server := &http.Server{
		Handler:           serveViewerHandlerWithLease(lease.current, *query, hc.Transport),
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			server.Close()
			return err
		}
		<-done
		return nil
	}
}

// serveViewerHandler exposes only the reads used by the viewer, to the node
// selected at startup. A browser cannot change the upstream through a request.
func serveViewerHandler(target *url.URL, query string) http.Handler {
	return serveViewerHandlerWithLease(func() (*url.URL, context.Context, error) { return target, context.Background(), nil }, query, nil)
}

func serveViewerHandlerWithLease(current func() (*url.URL, context.Context, error), query string, transport http.RoundTripper) http.Handler {
	assets := sites.StreamHandler()
	proxy := &httputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: -1,
		ModifyResponse: func(r *http.Response) error {
			if r.StatusCode >= 300 && r.StatusCode < 400 {
				return fmt.Errorf("node redirect refused")
			}
			r.Header.Set("Cache-Control", "no-store")
			r.Header.Set("X-Accel-Buffering", "no")
			r.Header.Del("Set-Cookie")
			return nil
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/config.json":
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			target, _, err := current()
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			if r.Method == http.MethodGet {
				json.NewEncoder(w).Encode(map[string]string{"node": target.String(), "q": query})
			}
		case r.URL.Path == "/v1/stream" || strings.HasPrefix(r.URL.Path, "/v1/data/"):
			// The viewer's write panel PUTs values; the stream stays read-only.
			write := r.Method == http.MethodPut && r.URL.Path != "/v1/stream"
			if r.Method != http.MethodGet && !write {
				allow := "GET"
				if r.URL.Path != "/v1/stream" {
					allow = "GET, PUT"
				}
				w.Header().Set("Allow", allow)
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			target, leaseCtx, err := current()
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			requestCtx, cancel := context.WithCancel(r.Context())
			stop := context.AfterFunc(leaseCtx, cancel)
			defer stop()
			defer cancel()
			selectedProxy := *proxy
			selectedProxy.Rewrite = func(req *httputil.ProxyRequest) {
				req.SetURL(target)
				req.Out.URL.RawQuery = ""
				req.Out.Header = make(http.Header)
				req.Out.Header.Set("Accept", req.In.Header.Get("Accept"))
				req.Out.Header.Set("User-Agent", client.UserAgent)
				if req.In.Method == http.MethodPut {
					for _, name := range []string{"Content-Type", "X-TTL"} {
						if value := req.In.Header.Get(name); value != "" {
							req.Out.Header.Set(name, value)
						}
					}
				}
			}
			selectedProxy.ServeHTTP(w, r.WithContext(requestCtx))
		default:
			assets.ServeHTTP(w, r)
		}
	})
}

func openBrowser(link string) error {
	name, args := "xdg-open", []string{link}
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", link}
	}
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
