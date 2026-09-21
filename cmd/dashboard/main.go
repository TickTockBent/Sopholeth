package main

import (
	"context"
	"embed"
	"flag"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"sopholeth/internal/dashboard"
)

// Static frontend assets — index.html, app.js, vendored vis-network. The
// directory layout is fixed (web/) so the dashboard binary stays a single
// self-contained artifact. See cmd/dashboard/web/UPSTREAM.md for vendoring
// provenance.
//
//go:embed web/*
var webAssets embed.FS

func main() {
	var (
		publicAddr   = flag.String("listen", ":8080", "Address for the public listener (HTML + /api/snapshot).")
		internalAddr = flag.String("internal-addr", "127.0.0.1:9095", "Address for the internal metrics listener (/internal/metrics). Bind to a loopback unless you have a reason not to.")
		stateDir     = flag.String("state-dir", defaultStateDir(), "Directory for the omega cache and persisted snapshot.")
		pollInterval = flag.Duration("poll-interval", 60*time.Second, "How often to walk the topology.")
		pollWorkers  = flag.Int("poll-workers", 0, "Concurrent topology pollers per cycle. 0 selects the package default (8).")
		pollTimeout  = flag.Duration("poll-timeout", 0, "Per-request timeout for poller HTTP calls. 0 selects the package default (5s).")
		seedsFlag    = flag.String("seeds", "", "Comma-separated host:port list of root addresses. When set, bypasses omega DNS entirely — operator break-glass.")
		geoDBPath    = flag.String("geo-db", "", "Path to a GeoLite2-Country mmdb file. Empty disables country lookups (region falls back to '?').")
	)
	flag.Parse()

	logger := log.New(os.Stderr, "", log.LstdFlags|log.Lmicroseconds)

	seeds := splitSeeds(*seedsFlag)

	orch := dashboard.NewOrchestrator(dashboard.Config{
		StateDir:      *stateDir,
		PollInterval:  *pollInterval,
		PollWorkers:   *pollWorkers,
		PollTimeout:   *pollTimeout,
		GeoDBPath:     *geoDBPath,
		SeedAddresses: seeds,
		Logger:        logger,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Boot order: cache → DNS → seeds → exit. --seeds is the operator's
	// last-resort break-glass: it is used only when both the verified
	// cache and the omega DNS chain are unavailable. A Boot failure
	// means none of the three landed and the operator must intervene.
	if err := orch.Boot(ctx); err != nil {
		logger.Fatalf("dashboard: boot failed: %v (provide --seeds or fix DNS/cache and retry)", err)
	}

	// Embed sub-FS rooted at web/ so / serves index.html.
	assetsFS, err := fs.Sub(webAssets, "web")
	if err != nil {
		logger.Fatalf("dashboard: prepare asset FS: %v", err)
	}

	server := dashboard.NewServer(orch, *publicAddr, *internalAddr, assetsFS, logger)

	// Signals: SIGINT/SIGTERM = orderly shutdown, SIGHUP = trigger omega
	// refresh + immediate poll. SIGUSR1 is unused but reserved so future
	// admin signals don't clash with operator muscle memory.
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for sig := range sigCh {
			switch sig {
			case syscall.SIGINT, syscall.SIGTERM:
				logger.Printf("dashboard: received %s, shutting down", sig)
				cancel()
				return
			case syscall.SIGHUP:
				orch.SignalHUP()
			}
		}
	}()

	go orch.Run(ctx)
	if err := server.Run(ctx); err != nil {
		logger.Fatalf("dashboard: %v", err)
	}
}

func splitSeeds(s string) []string {
	if s == "" {
		return nil
	}
	out := make([]string, 0, 4)
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func defaultStateDir() string {
	// Use the conventional user state path when HOME exists, otherwise
	// /var/lib for system installs. DASHBOARD_STATE_DIR overrides either.
	if dir := os.Getenv("DASHBOARD_STATE_DIR"); dir != "" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home + "/.local/state/sopholeth/dashboard"
	}
	return "/var/lib/sopholeth/dashboard"
}
