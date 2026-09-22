package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"time"

	"sopholeth/internal/client"
	"sopholeth/sites"
)

func (a *app) serveEndpoint(override string) (string, error) {
	if override != "" {
		endpoint, err := client.NormalizeEndpoint(override)
		if err != nil {
			return "", usagef("%v", err)
		}
		return endpoint, nil
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return "", err
	}
	if a.networkFlag == "" && a.getenv("SOPH_NETWORK") == "" && cfg.Current == "" {
		return client.NormalizeEndpoint("localhost")
	}
	_, network, err := a.selectNetwork(cfg)
	if err != nil {
		return "", err
	}
	return network.Endpoint, nil
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
	endpoint, err := a.serveEndpoint(*node)
	if err != nil {
		return err
	}
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
	server := &http.Server{Handler: sites.StreamHandler(), ReadHeaderTimeout: 5 * time.Second}
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
