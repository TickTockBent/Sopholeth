package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"errors"
	// Registers pprof handlers on http.DefaultServeMux unconditionally;
	// only exposed via a listener when NODE_PPROF_ENABLED=true.
	_ "net/http/pprof"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"net"

	"sopholeth/internal/cluster"
	"sopholeth/internal/discovery"
	"sopholeth/internal/endpoint"
	"sopholeth/internal/gossip"
	"sopholeth/internal/logging"
	mcprpc "sopholeth/internal/mcp"
	"sopholeth/internal/node"
	"sopholeth/internal/storage"
	"sopholeth/internal/transport/ws"
	"sopholeth/internal/tree"
)

const (
	defaultTTLSeconds = 30 * 60
	minimumTTLSeconds = cluster.MinTTLSeconds
)

// omegaLastRefreshGauge records the unix timestamp of the most recent
// successful signed-list refresh. The burn-in dashboard plots
// `time() - discovery_last_refresh_unix_seconds` to surface freshness.
// Stays at zero on private-network deployments (refresher never runs).
var omegaLastRefreshGauge = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "discovery_last_refresh_unix_seconds",
	Help: "Unix timestamp of the most recent successful omega root-list refresh (0 = never).",
})

func init() {
	prometheus.MustRegister(omegaLastRefreshGauge)
}

func main() {
	// Detect --mcp before logging.Init so embedded-defaults override env-derived ones.
	mcpMode := false
	for _, arg := range os.Args[1:] {
		if arg == "--mcp" {
			mcpMode = true
			break
		}
	}

	if mcpMode {
		// In MCP mode the stdout channel carries JSON-RPC frames; logs must
		// not be interleaved with them. The default logger already writes to
		// stderr, but pin it explicitly so any future change can't regress.
		log.SetOutput(os.Stderr)
		// Quieter default — the host agent is the only audience for stderr.
		if os.Getenv("NODE_LOG_LEVEL") == "" {
			os.Setenv("NODE_LOG_LEVEL", "warn")
		}
	}

	logging.Init()

	// Generate a unique node ID
	nodeID := os.Getenv("NODE_ID")
	if nodeID == "" {
		nodeID = fmt.Sprintf("node-%d", time.Now().UnixNano())
	}

	address := os.Getenv("NODE_ADDRESS")
	if address == "" {
		address = "localhost"
	}

	// Defaults differ for embedded MCP: HTTP and gossip listeners pick
	// random free ports, storage is capped at 50MB so an agent fleet
	// doesn't blow out the host's memory, and the network is private —
	// agents that want to join the public mesh set NODE_NETWORK=public
	// explicitly and accept the omega bootstrap dependency.
	defaultHTTPPort := 8080
	defaultGossipPort := 9090
	defaultStorageMB := 0 // unlimited
	defaultNetwork := "public"
	if mcpMode {
		defaultHTTPPort = 0
		defaultGossipPort = 0
		defaultStorageMB = 50
		defaultNetwork = "private"
	}

	// Configuration: one name per setting, no aliases
	httpPort := envInt("NODE_HTTP_PORT", defaultHTTPPort)
	gossipPort := envInt("NODE_GOSSIP_PORT", defaultGossipPort)
	replicationFactor := envInt("NODE_REPLICATION", 3)
	minTTL := envInt("NODE_MIN_TTL", 300)
	maxTTL := envInt("NODE_MAX_TTL", cluster.DefaultMaxTTLSeconds)
	// Five minutes is the protocol floor: callers may request less, but the
	// accepted value is normalized upward so it has time to propagate. An
	// operator may configure a stricter floor, never a looser one.
	if minTTL < minimumTTLSeconds {
		minTTL = minimumTTLSeconds
	}
	rateLimit := envInt("NODE_RATE_LIMIT", 100)
	maxStorageMB := envInt("NODE_MAX_STORAGE_MB", defaultStorageMB)
	writeTimeout := envInt("NODE_WRITE_TIMEOUT", 5) // seconds
	clusterSecret := os.Getenv("NODE_CLUSTER_SECRET")
	trustProxy := strings.EqualFold(os.Getenv("NODE_TRUST_PROXY"), "true")
	enclave := os.Getenv("NODE_ENCLAVE") // default: "default"
	network := os.Getenv("NODE_NETWORK")
	if network == "" {
		network = defaultNetwork
	}
	pprofEnabled := strings.EqualFold(os.Getenv("NODE_PPROF_ENABLED"), "true")
	pprofAddr := os.Getenv("NODE_PPROF_ADDR")
	if pprofAddr == "" {
		pprofAddr = "127.0.0.1:6060"
	}

	// Resolve bootstrap peers.
	// NODE_PEERS are HTTP addresses (host:httpPort) since the bootstrap
	// handshake is an HTTP POST to /v1/bootstrap. Example: "node2:8080,node3:8080"
	var bootstrapNodes []string
	if peers := os.Getenv("NODE_PEERS"); peers != "" {
		bootstrapNodes = strings.Split(peers, ",")
		for i, n := range bootstrapNodes {
			bootstrapNodes[i] = strings.TrimSpace(n)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var publicDiscovery *discovery.Session
	httpOrigin := os.Getenv("NODE_HTTP_ORIGIN")
	if httpOrigin != "" {
		var err error
		httpOrigin, err = endpoint.Normalize(httpOrigin)
		if err != nil {
			log.Fatalf("NODE_HTTP_ORIGIN: %v", err)
		}
	}
	if network == "public" && len(bootstrapNodes) == 0 {
		var err error
		publicDiscovery, err = resolveOmegaBootstrap(ctx)
		if err != nil {
			log.Fatalf("Omega bootstrap failed: %v", err)
		}
		bootstrapNodes = publicDiscovery.Seeds()
	} else if network == "public" {
		logging.Warn("NODE_PEERS supplies unverified bootstrap seeds; this node cannot claim an official root role")
	}
	clusterNode := cluster.NewClusterNode(nodeID, address, gossipPort, httpPort, replicationFactor, int64(maxStorageMB)*1024*1024, time.Duration(writeTimeout)*time.Second, clusterSecret, enclave)
	limits := cluster.DefaultWriteLimits()
	limits.MaxValueBytes = envInt("NODE_MAX_VALUE_BYTES", limits.MaxValueBytes)
	limits.MaxKeyBytes = envInt("NODE_MAX_KEY_BYTES", limits.MaxKeyBytes)
	limits.MinTTLSeconds, limits.MaxTTLSeconds = minTTL, maxTTL
	if err := clusterNode.SetWriteLimits(limits); err != nil {
		log.Fatalf("Invalid write limits: %v", err)
	}
	clusterNode.SetHTTPOrigin(httpOrigin)
	if enclave == "" {
		enclave = "default"
	}
	if publicDiscovery != nil {
		if err := configurePublicDiscovery(clusterNode, publicDiscovery, nodeID, httpOrigin, enclave); err != nil {
			log.Fatalf("Public root configuration: %v", err)
		}
		logging.Info("Omega bootstrap: official root=%t, advertised origin=%s", clusterNode.IsRoot(), httpOrigin)
		go publicDiscovery.Run(ctx, func(err error) {
			if err != nil {
				logging.Warn("Omega refresh: %v", err)
			} else {
				omegaLastRefreshGauge.SetToCurrentTime()
			}
		})
	} else if len(bootstrapNodes) > 0 {
		seeds := append([]string(nil), bootstrapNodes...)
		clusterNode.SetSeedProvider(func() []string { return seeds })
	}
	if err := clusterNode.Start(ctx, bootstrapNodes); err != nil {
		log.Fatalf("Failed to start cluster node: %v", err)
	}

	// Tree manager owns substrate-transient attachment state. Substrate
	// nodes (NODE_INBOUND=true) accept inbound WS attachments and act as
	// AckRouter + ChildBroadcaster for the cluster node. Transients
	// (default, NODE_INBOUND=false) attach outbound after bootstrap.
	inbound := tree.InboundFalse
	if strings.EqualFold(os.Getenv("NODE_INBOUND"), "true") {
		inbound = tree.InboundTrue
	}
	maxChildren := envInt("NODE_MAX_CHILDREN", tree.DefaultMaxChildren)
	treeMgr := tree.NewManager(
		&gossip.Node{
			ID: gossip.NodeID(nodeID), Address: address,
			Port: gossipPort, HTTPPort: httpPort, HTTPOrigin: httpOrigin, Enclave: enclave,
		},
		clusterPeerer{cn: clusterNode},
		tree.Options{
			Inbound:       inbound,
			MaxChildren:   maxChildren,
			ClusterSecret: clusterSecret,
		},
	)
	clusterNode.SetAckRouter(treeMgr)
	clusterNode.SetChildBroadcaster(treeMgr)

	// Wire the parent-side gossip dispatch on the tree manager. Attach()
	// and every successful reattach install this handler on the new
	// connection BEFORE the function returns — closes the post-welcome
	// dispatch race the SetReattachCallback hook used to leave open.
	treeMgr.SetParentDispatch(func(m *gossip.Message) {
		if err := clusterNode.HandleGossipMessage(m); err != nil {
			logging.Debug("Parent WS dispatch: %v", err)
		}
	})

	// Transient bootstrap: if this node accepts no inbound, kick off a
	// best-effort outbound WS attach to one of the seed substrates after
	// HTTP bootstrap is done. Failure falls back to HTTP-only operation
	// (writes still propagate via HTTP gossip; reads of other agents'
	// writes don't reach this node until reattach succeeds).
	if network != "public" && inbound == tree.InboundFalse && len(bootstrapNodes) > 0 {
		go func(seeds []string) {
			// Give the gossip bootstrap a moment to settle so the peer
			// list reflects the actual cluster before we pick an attach
			// target. 500ms is enough for the bootstrap response round-trip.
			time.Sleep(500 * time.Millisecond)
			for _, seed := range seeds {
				idx := strings.LastIndex(seed, ":")
				if idx <= 0 {
					continue
				}
				host := seed[:idx]
				port, err := strconv.Atoi(seed[idx+1:])
				if err != nil || port <= 0 {
					continue
				}
				if host == address && port == httpPort {
					continue
				}
				conn, err := ws.ConnectToSubstrate(ctx, host, port, clusterSecret, 10*time.Second)
				if err != nil {
					logging.Warn("WS attach to %s failed: %v — trying next seed", seed, err)
					continue
				}
				if _, err := treeMgr.Attach(ctx, conn); err != nil {
					logging.Warn("WS attach handshake to %s failed: %v", seed, err)
					conn.Close(1000, "")
					continue
				}
				// parent dispatch was installed by treeMgr.Attach via
				// SetParentDispatch; nothing extra to wire here.
				conn.StartHeartbeat()
				logging.Info("Transient mode: attached to substrate at %s", seed)
				return
			}
			logging.Warn("Transient mode: no seed accepted WS attach (degraded — HTTP gossip only)")
		}(bootstrapNodes)
	}
	// Seed provider for tree-side reattach mirrors the cluster's recovery seeds.
	if network != "public" {
		treeMgr.SetSeedProvider(func() []string { return bootstrapNodes })
	}
	// Public transient/WSS support is deferred. Never reinterpret an HTTPS
	// discovery origin as a plaintext WebSocket address.

	server := &HTTPServer{
		clusterNode:    clusterNode,
		treeManager:    treeMgr,
		nodeID:         nodeID,
		network:        network,
		minTTL:         minTTL,
		maxTTL:         maxTTL,
		startTime:      time.Now(),
		streamDisabled: strings.EqualFold(os.Getenv("NODE_STREAM"), "off"),
		streamDone:     make(chan struct{}),
	}

	// Initialize security middleware
	securityMW := node.NewSecurityMiddleware(
		rateLimit,
		rateLimit*2,  // burst = 2x rate
		10*1024*1024, // 10MB max request size
		trustProxy,
	)
	server.securityMW = securityMW

	peerCount := len(bootstrapNodes)
	logging.Info("Node online. Peers: %d. Network: %s", peerCount, network)
	logging.Info("  Node ID: %s", nodeID)
	logging.Info("  HTTP: :%d  Gossip: :%d  Enclave: %s", httpPort, gossipPort, clusterNode.Enclave())
	logging.Info("  Replication: %d  TTL range: %d-%ds  Write timeout: %ds", replicationFactor, minTTL, maxTTL, writeTimeout)
	if clusterSecret != "" {
		logging.Info("  Gossip authentication: HMAC-SHA256 (cluster secret configured)")
	} else {
		logging.Info("  Gossip authentication: none (open mode)")
	}
	if pprofEnabled {
		logging.Info("  pprof: enabled on %s (do not expose in untrusted environments)", pprofAddr)
	}

	// Create HTTP server for graceful shutdown support. When httpPort is 0
	// (default in MCP mode) we bind first and read the chosen port back so
	// the log line and any potential gossip-self-discovery code see the
	// real listener.
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", httpPort))
	if err != nil {
		log.Fatalf("HTTP listen failed: %v", err)
	}
	if tcpAddr, ok := listener.Addr().(*net.TCPAddr); ok {
		httpPort = tcpAddr.Port
		logging.Info("  HTTP listener bound to :%d", httpPort)
	}
	// Outer mux routes /v1/ws directly (bypassing data-plane middleware)
	// and delegates everything else to the gorilla router. http.NewServeMux
	// longest-prefix match means /v1/ws is consumed here, "/" catches the rest.
	outerMux := http.NewServeMux()
	outerMux.HandleFunc("/v1/ws", server.wsHandler)
	outerMux.Handle("/", server.Router())
	httpServer := &http.Server{Handler: outerMux}

	// Optional pprof server on a separate listener (diagnostic plane).
	// Uses http.DefaultServeMux which has pprof handlers auto-registered
	// via the net/http/pprof blank import.
	var pprofServer *http.Server
	if pprofEnabled {
		pprofServer = &http.Server{
			Addr:    pprofAddr,
			Handler: http.DefaultServeMux,
		}
		go func() {
			logging.Info("pprof listening on %s", pprofAddr)
			if err := pprofServer.ListenAndServe(); err != http.ErrServerClosed {
				logging.Warn("pprof server error: %v", err)
			}
		}()
	}

	shutdown := func() {
		logging.Info("Shutting down — draining in-flight requests...")
		close(server.streamDone)

		// Shut down pprof first with a short deadline. pprof has no
		// data-plane responsibilities; a 2s cap prevents a long-running
		// CPU profile (/debug/pprof/profile, 30s default) from starving
		// the main server's drain window.
		if pprofServer != nil {
			pprofCtx, pprofCancel := context.WithTimeout(context.Background(), 2*time.Second)
			if err := pprofServer.Shutdown(pprofCtx); err != nil {
				logging.Warn("pprof server shutdown error: %v", err)
			}
			pprofCancel()
		}

		// Give in-flight data-plane requests up to 10 seconds to complete
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()

		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			logging.Warn("HTTP server shutdown error: %v", err)
		}

		securityMW.Close()
		treeMgr.Stop()
		clusterNode.Stop()
		cancel()
	}

	if mcpMode {
		// MCP drives lifecycle: when the host closes stdin, we shut down.
		// SIGINT/SIGTERM are still honoured as a belt-and-braces fallback.
		go func() {
			if err := httpServer.Serve(listener); err != http.ErrServerClosed {
				logging.Warn("HTTP server error: %v", err)
			}
		}()

		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
		mcpCtx, mcpCancel := context.WithCancel(context.Background())
		defer mcpCancel()
		go func() {
			<-sigChan
			mcpCancel()
		}()

		srv := mcprpc.NewServer(clusterNode, os.Stdin, os.Stdout, minTTL, maxTTL)
		if err := srv.Run(mcpCtx); err != nil && !errors.Is(err, context.Canceled) {
			logging.Warn("MCP server exited: %v", err)
		}
		shutdown()
		logging.Info("Shutdown complete.")
		return
	}

	// Graceful shutdown: drain in-flight requests before exiting
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		shutdown()
	}()

	if err := httpServer.Serve(listener); err != http.ErrServerClosed {
		log.Fatalf("HTTP server error: %v", err)
	}
	logging.Info("Shutdown complete.")
}

// envInt reads an environment variable as int with a default fallback.
func envInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return defaultVal
}

// CORS middleware
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-TTL")
		w.Header().Set("Access-Control-Expose-Headers", "X-Created-At, X-Original-TTL, X-Remaining-TTL")
		w.Header().Set("Access-Control-Max-Age", "3600")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

type HTTPServer struct {
	clusterNode    *cluster.ClusterNode
	nodeID         string
	network        string
	minTTL         int
	maxTTL         int
	startTime      time.Time
	securityMW     *node.SecurityMiddleware
	streamDisabled bool
	streamDone     chan struct{}
	streamActive   atomic.Int32
	// treeManager owns substrate-transient attachment state. Always non-nil
	// — the constructor wires one up regardless of inbound capability so
	// transients can also call Attach when they have a substrate peer.
	treeManager *tree.Manager
}

func (s *HTTPServer) Router() *mux.Router {
	r := mux.NewRouter()

	// Disable gorilla's default path cleaning. Without this, a request to
	// `/v1/data/%2F` decodes to `/v1/data//`, gorilla cleans it to
	// `/v1/data/`, and returns 301 — pre-empting the NotFoundHandler. PUT
	// clients then see a redirect that converts to GET (RFC 9110), so the
	// PUT never gets a coherent 400. With SkipClean, the original path
	// reaches the router, fails the `{key}` route, and lands in the
	// NotFoundHandler that returns 400. None of the node's routes need
	// path cleaning (no `..`, no double-slash semantics) (#91).
	r.SkipClean(true)

	// Apply middleware
	r.Use(corsMiddleware)
	r.Use(s.securityMW.Middleware)
	r.Use(node.MaxRequestSizeMiddleware(s.securityMW.MaxRequestSize()))
	r.Use(func(next http.Handler) http.Handler {
		timed := node.TimeoutMiddleware(30 * time.Second)(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/stream" {
				next.ServeHTTP(w, r)
				return
			}
			timed.ServeHTTP(w, r)
		})
	})

	// v1 API endpoints
	r.HandleFunc("/v1/data/{key}", s.putHandler).Methods("PUT", "OPTIONS")
	r.HandleFunc("/v1/data/{key}", s.getHandler).Methods("GET", "HEAD", "OPTIONS")
	r.HandleFunc("/v1/keys", s.keysHandler).Methods("GET", "OPTIONS")
	r.HandleFunc("/v1/health", s.healthHandler).Methods("GET", "OPTIONS")
	r.HandleFunc("/v1/status", s.statusHandler).Methods("GET", "OPTIONS")
	r.HandleFunc("/v1/metrics", promhttp.Handler().ServeHTTP).Methods("GET", "OPTIONS")
	r.HandleFunc("/v1/topology", s.topologyHandler).Methods("GET", "OPTIONS")
	r.HandleFunc("/v1/stream", s.streamHandler).Methods("GET", "OPTIONS")

	// Internal gossip endpoints
	r.HandleFunc("/v1/gossip/message", s.gossipHandler).Methods("POST", "OPTIONS")
	r.HandleFunc("/v1/bootstrap", s.bootstrapHandler).Methods("POST", "OPTIONS")
	// /v1/ws is intentionally NOT registered here. The WebSocket upgrade
	// needs Hijack() and a long-lived connection, both of which fight the
	// http.TimeoutHandler + MaxRequestSize wrappers above. It's wired
	// directly on the outer mux below.

	r.NotFoundHandler = http.HandlerFunc(s.notFoundHandler)

	return r
}

// notFoundHandler turns the implicit 404 from gorilla/mux on slash-containing
// keys into an explicit 400 with a useful message. Keys are opaque, single-
// segment strings (see docs/patterns.md); a request to `/v1/data/foo%2Fbar`
// gets the path decoded by gorilla/mux to `/v1/data/foo/bar` before route
// matching, which fails the `{key}` pattern. Without this handler clients
// see a generic 404 and can't tell whether the key expired or was malformed
// at the wire (#91).
func (s *HTTPServer) notFoundHandler(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/v1/data/") {
		http.Error(w, "key must not contain '/'", http.StatusBadRequest)
		return
	}
	http.NotFound(w, r)
}

func (s *HTTPServer) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "healthy",
		"node_id": s.nodeID,
		"network": s.network,
		"enclave": s.clusterNode.Enclave(),
	})
}

func (s *HTTPServer) statusHandler(w http.ResponseWriter, r *http.Request) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":     "healthy",
		"node_id":    s.nodeID,
		"network":    s.network,
		"enclave":    s.clusterNode.Enclave(),
		"uptime":     time.Since(s.startTime).String(),
		"goroutines": runtime.NumGoroutine(),
		"memory": map[string]interface{}{
			"alloc":       m.Alloc,
			"total_alloc": m.TotalAlloc,
			"sys":         m.Sys,
			"num_gc":      m.NumGC,
		},
	})
}

func (s *HTTPServer) topologyHandler(w http.ResponseWriter, r *http.Request) {
	peers := s.clusterNode.Topology()

	type peerInfo struct {
		ID         string `json:"id"`
		Address    string `json:"address"`
		HTTPPort   int    `json:"http_port"`
		HTTPOrigin string `json:"http_origin,omitempty"`
		Enclave    string `json:"enclave"`
	}

	peerList := make([]peerInfo, 0, len(peers))
	for _, p := range peers {
		peerList = append(peerList, peerInfo{
			ID:         string(p.ID),
			Address:    p.Address,
			HTTPPort:   p.HTTPPort,
			HTTPOrigin: p.HTTPOrigin,
			Enclave:    p.Enclave,
		})
	}

	// Attached children (transients) — visible only on substrate nodes
	// that have accepted WS attachments.
	type childInfo struct {
		ID      string `json:"id"`
		Enclave string `json:"enclave"`
	}
	var children []childInfo
	if s.treeManager != nil {
		for id, conn := range s.treeManager.Children() {
			children = append(children, childInfo{ID: id, Enclave: conn.RemoteEnclave()})
		}
	}

	resp := map[string]interface{}{
		"node_id": s.nodeID,
		"enclave": s.clusterNode.Enclave(),
		"peers":   peerList,
	}
	if s.treeManager != nil {
		resp["role"] = string(s.treeManager.Role())
		resp["children"] = children
		if parent := s.treeManager.Parent(); parent != nil {
			resp["parent_id"] = parent.RemoteNodeID()
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *HTTPServer) putHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	key := vars["key"]
	if err := s.clusterNode.ValidateKey(key); err != nil {
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	}

	// Bound reads as well as storage, including uploads without Content-Length.
	maxValueBytes := s.clusterNode.WriteLimits().MaxValueBytes
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(maxValueBytes)))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, fmt.Sprintf("%s: limit is %d bytes", cluster.ErrValueTooLarge, maxValueBytes), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	// TTL from header or query param
	ttl := defaultTTLSeconds
	if ttlStr := r.URL.Query().Get("ttl"); ttlStr != "" {
		if parsed, err := strconv.Atoi(ttlStr); err == nil && parsed > 0 {
			ttl = parsed
		}
	} else if ttlHeader := r.Header.Get("X-TTL"); ttlHeader != "" {
		if parsed, err := strconv.Atoi(ttlHeader); err == nil && parsed > 0 {
			ttl = parsed
		}
	}

	// Enforce TTL bounds
	if ttl < s.minTTL {
		ttl = s.minTTL
	}
	if ttl > s.maxTTL {
		ttl = s.maxTTL
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if err := s.clusterNode.Put(ctx, key, body, time.Duration(ttl)*time.Second); err != nil {
		if errors.Is(err, cluster.ErrValueTooLarge) || errors.Is(err, cluster.ErrKeyTooLong) {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		if errors.Is(err, storage.ErrStoreFull) {
			http.Error(w, "Node storage capacity exceeded", http.StatusInsufficientStorage)
			return
		}
		if errors.Is(err, cluster.ErrQuorumTimeout) {
			// Data is stored locally and will propagate via gossip.
			// 202 Accepted signals "written, replication in progress."
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprintf(w, "Accepted (quorum pending)")
			return
		}
		http.Error(w, fmt.Sprintf("Write failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, "OK")
}

func (s *HTTPServer) getHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	key := vars["key"]

	data, createdAt, originalTTL, exists := s.clusterNode.GetWithMetadata(key)
	if !exists {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	elapsed := time.Since(createdAt)
	remainingTTL := originalTTL - elapsed
	if remainingTTL < 0 {
		remainingTTL = 0
	}

	w.Header().Set("X-Created-At", createdAt.Format(time.RFC3339))
	w.Header().Set("X-Original-TTL", strconv.Itoa(int(originalTTL.Seconds())))
	w.Header().Set("X-Remaining-TTL", strconv.Itoa(int(remainingTTL.Seconds())))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

func (s *HTTPServer) keysHandler(w http.ResponseWriter, r *http.Request) {
	keys := s.clusterNode.Scan()

	// Optional prefix filter
	if prefix := r.URL.Query().Get("prefix"); prefix != "" {
		var filtered []string
		for _, k := range keys {
			if strings.HasPrefix(k, prefix) {
				filtered = append(filtered, k)
			}
		}
		keys = filtered
	}

	// Sort for stable cursor-based pagination
	sort.Strings(keys)

	// Cursor: skip keys <= cursor value (cursor is the last key from previous page)
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		idx := sort.SearchStrings(keys, cursor)
		// Skip past the cursor key itself
		if idx < len(keys) && keys[idx] == cursor {
			idx++
		}
		keys = keys[idx:]
	}

	// Limit: cap the number of returned keys
	limit := 0
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	var nextCursor string
	if limit > 0 && len(keys) > limit {
		nextCursor = keys[limit-1]
		keys = keys[:limit]
	}

	resp := map[string]interface{}{
		"keys": keys,
	}
	if nextCursor != "" {
		resp["next_cursor"] = nextCursor
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *HTTPServer) verifyGossipSignature(w http.ResponseWriter, r *http.Request, body []byte) bool {
	secret := s.clusterNode.ClusterSecret()
	if secret == "" {
		return true // open mode
	}
	sig := r.Header.Get(gossip.SignatureHeader)
	if sig == "" {
		http.Error(w, "Missing signature", http.StatusForbidden)
		return false
	}
	if !gossip.VerifyBody(secret, body, sig) {
		http.Error(w, "Invalid signature", http.StatusForbidden)
		return false
	}
	return true
}

func (s *HTTPServer) gossipHandler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	if !s.verifyGossipSignature(w, r, body) {
		return
	}

	var simpleMsg gossip.SimpleMessage
	if err := json.Unmarshal(body, &simpleMsg); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	gossipMsg := &gossip.Message{
		Type:      gossip.MessageType(simpleMsg.Type),
		From:      gossip.NodeID(simpleMsg.From),
		To:        gossip.NodeID(simpleMsg.To),
		Key:       simpleMsg.Key,
		Data:      simpleMsg.Data,
		TTL:       int(simpleMsg.TTL),
		Timestamp: time.Unix(simpleMsg.Timestamp, 0),
		MessageID: simpleMsg.MessageID,
	}

	if simpleMsg.NodeInfo != nil {
		enclave := simpleMsg.NodeInfo.Enclave
		if enclave == "" {
			enclave = "default"
		}
		gossipMsg.NodeInfo = &gossip.Node{
			ID:         gossip.NodeID(simpleMsg.NodeInfo.ID),
			Address:    simpleMsg.NodeInfo.Address,
			Port:       simpleMsg.NodeInfo.Port,
			HTTPPort:   simpleMsg.NodeInfo.HTTPPort,
			HTTPOrigin: simpleMsg.NodeInfo.HTTPOrigin,
			Enclave:    enclave,
		}
	}

	if err := s.clusterNode.HandleGossipMessage(gossipMsg); err != nil {
		if errors.Is(err, cluster.ErrValueTooLarge) || errors.Is(err, cluster.ErrKeyTooLong) {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, fmt.Sprintf("Gossip error: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func (s *HTTPServer) bootstrapHandler(w http.ResponseWriter, r *http.Request) {
	// Only roots answer bootstrap requests. On the public network a node is
	// a root iff its advertised address is in the current signed omega list
	// (see resolveOmegaBootstrap). On private networks no one is a root
	// under this gate — peer discovery is driven by NODE_PEERS directly.
	if s.network == "public" && !s.clusterNode.IsRoot() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{"error": "not a bootstrap root"})
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	if !s.verifyGossipSignature(w, r, body) {
		return
	}

	var req gossip.BootstrapRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	resp := s.clusterNode.HandleBootstrap(&req)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// clusterPeerer adapts *cluster.ClusterNode to the tree.Peerer interface.
// ClusterNode.Topology already returns the full peer list with enclave
// metadata; this just renames the method to match what tree wants.
type clusterPeerer struct{ cn *cluster.ClusterNode }

func (c clusterPeerer) GetPeers() []*gossip.Node { return c.cn.Topology() }

// wsHandler accepts an incoming substrate-transient WebSocket attachment.
// The first non-control frame must be a hello; subsequent gossip-typed
// frames are dispatched to clusterNode.HandleGossipMessage, with PUTs
// recording an ACK route so the substrate can forward the enclave-peer
// ACKs back through the WS pipe to the originating child.
//
// All routing decisions are driven by treeManager.HandleHello — if the
// substrate is at capacity or attachments are disabled, the manager sends
// a goodbye-with-alternatives and closes the connection itself.
func (s *HTTPServer) wsHandler(w http.ResponseWriter, r *http.Request) {
	if s.treeManager == nil || !s.treeManager.IsInboundCapable() {
		// Transient nodes don't accept inbound; refuse with 404 to
		// avoid leaking the role to scanners.
		http.NotFound(w, r)
		return
	}
	wsHandler := ws.Handler(s.clusterNode.ClusterSecret(), nil, func(conn *ws.Connection) {
		s.bindWSConnection(conn)
	})
	wsHandler.ServeHTTP(w, r)
}

// bindWSConnection sets up the gossip dispatch + ACK-route recording on a
// freshly accepted child connection. Called from ws.Handler's onAccept.
func (s *HTTPServer) bindWSConnection(conn *ws.Connection) {
	// One-shot hello handler: install the gossip dispatch only after a
	// valid hello arrives. Until then ignore everything (matches the TS
	// reference's gate at handleUpgrade attachment handler).
	helloDone := make(chan struct{})
	var helloOnce sync.Once

	removeHello := conn.AddAttachmentHandler(func(msg *ws.AttachmentMessage) {
		if msg.Type != ws.AttachmentTypeHello {
			return
		}
		var h ws.HelloPayload
		if err := json.Unmarshal(msg.Payload, &h); err != nil {
			logging.Warn("WS attach: hello decode failed: %v", err)
			conn.Close(1003, "bad hello")
			return
		}
		helloOnce.Do(func() { close(helloDone) })
		if !s.treeManager.HandleHello(conn, &h) {
			// HandleHello already sent goodbye-with-alternatives and is
			// scheduling close. Bail.
			return
		}
		// Dispatch any subsequent gossip frame into the cluster handler.
		// Recording the ACK route happens for PUTs so the substrate can
		// reverse-route ACKs back through this pipe.
		conn.OnMessage(func(gmsg *gossip.Message) {
			if gmsg.Type == gossip.MessageTypePut {
				s.treeManager.RecordAckRoute(gmsg.MessageID, conn, s.clusterNode.WriteTimeout())
			}
			if err := s.clusterNode.HandleGossipMessage(gmsg); err != nil {
				logging.Debug("WS gossip handler: %v", err)
			}
		})
	})

	// If hello never arrives within 30s, close. Mirrors the TS reference's
	// silent-attachment ceiling.
	go func() {
		select {
		case <-helloDone:
			removeHello()
		case <-time.After(30 * time.Second):
			if !conn.IsClosed() {
				logging.Warn("WS attach: no hello within 30s, closing")
				conn.Close(1002, "no hello")
			}
			removeHello()
		}
	}()
}
