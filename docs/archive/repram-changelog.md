# Pre-rebrand development history

This is the changelog as it stood before the Sopholeth documentation rebrand.
Original project names, issue links, version labels, and the cumulative
`Unreleased` section are preserved for provenance. That section includes
features later replaced or removed and does not establish a released version.

Historical statements about security, exact test counts, key listing, npm
packages, and TypeScript support are not current guidance. See the
[current changelog](../../CHANGELOG.md), [API](../api.md), and
[architecture](../architecture.md).

---

# Changelog

All notable changes to REPRAM are documented here.

## [Unreleased]

### Restored — Substrate/transient WS tree in Go ([#135](https://github.com/TickTockBent/repram/issues/135), recovers the gap from [#133](https://github.com/TickTockBent/repram/issues/133))
The substrate/transient tree topology that #125 inadvertently removed when it deleted the TypeScript node is now ported into the Go binary. `repram --mcp` (and any node with `REPRAM_INBOUND=false`) is once again a real cluster participant: it attaches to a substrate via persistent outbound WebSocket, sees other agents' writes in its local store, and survives substrate failure via cached alternatives.

- New `internal/transport/ws` package — WS `Connection` with heartbeat, optional HMAC, multi-subscriber lifecycle handlers; outbound `ConnectToSubstrate` dialer; inbound `Handler` upgrader. Wire format on WS payloads is identical to HTTP gossip — same JSON shape, same handlers process both transports.
- New `internal/tree` package — `Manager` owns substrate (`HandleHello` + child registration + welcome-with-topology) and transient (`Attach` + cached-topology / seed-list reattach loop) lifecycle. Self-skip is enforced literally on address+http\_port to prevent the [#120](https://github.com/TickTockBent/repram/issues/120) regression. Stop-aware context threading and a goroutine WaitGroup ensure clean shutdown.
- `cluster.ClusterNode` gets `AckRouter` and `ChildBroadcaster` interfaces (satisfied by the tree manager). `handlePutMessage` routes the substrate's own ACK back through the WS pipe to the originating transient and broadcasts replicas out to enclave-matched children; `handleAckMessage` forwards enclave-peer ACKs upstream when they're for a relayed write. Quorum-complete signaling is guarded by `sync.Once` so concurrent ACK paths can't panic on close-of-closed-channel.
- `cmd/repram/main.go` mounts `/v1/ws` on an outer `http.ServeMux` that bypasses the data-plane TimeoutHandler + MaxRequestSize wrappers. Substrates accept attachments; transients 404 the endpoint to avoid leaking role to scanners. `--mcp` mode auto-attaches outbound after HTTP bootstrap and falls back to HTTP-only on attach failure. Parent-side gossip dispatch is installed inside `Attach` before the function returns so a fast substrate's first PUT after welcome cannot be silently dropped. `/v1/topology` now exposes `role`, attached `children`, and `parent_id`.
- Enclave isolation hardening: `HandleHello` normalizes empty hello-enclave to `"default"` so the `BroadcastToChildren` filter cannot be bypassed by an underspecified hello.
- 50+ new tests: 25 WS transport, 22 tree manager (including the [#120](https://github.com/TickTockBent/repram/issues/120) self-skip timing assertion and an empty-enclave isolation regression), 4 HTTP-server-level WS integration tests. Full repo passes `go test -race ./...` with no regressions in existing 118+ tests.

Phase 7 of #135 — a 24h+ burn-in 2.2 on a real multi-substrate + multi-transient cluster — is tracked as a follow-up; it does not gate this PR.

### Changed — Go-native MCP server ([#123](https://github.com/TickTockBent/repram/issues/123))
The Go binary now serves MCP directly via `repram --mcp`: an embedded node, in-process tool handlers, and JSON-RPC 2.0 on stdin/stdout. The TypeScript node (`repram-mcp/`) has been removed.

- New `internal/mcp` package implements MCP stdio transport, `initialize`, `tools/list`, `tools/call`, and the four REPRAM tools (`repram_store`, `repram_retrieve`, `repram_exists`, `repram_list_keys`).
- MCP mode defaults: private network, `REPRAM_HTTP_PORT=0` (OS-assigned), 50MB storage cap, `warn` log level, logs routed to stderr only so stdout stays clean for JSON-RPC frames.
- The agent footprint drops from ~191MB RSS (Node.js + V8) to roughly 37MB per process, removing the V8 tax that made parallel-agent fleets untenable.
- Removed: `repram-mcp/` directory, the npm publishing workflow, `test/live-wire-compat/` (Go ↔ TS interop tests with one implementation only), and the burn-in TS Dockerfile.
- MCP client config simplifies to `{ "command": "repram", "args": ["--mcp"] }` — no Node.js, no npm install, single static binary.

### Added — Unified TypeScript Node ([#45](https://github.com/TickTockBent/repram/issues/45))
Complete TypeScript reimplementation of the REPRAM node, merged into `repram-mcp` so a single `npx repram-mcp` provides both a full node and MCP agent tools.
- **Storage layer** — MemoryStore with TTL expiration, background cleanup, capacity limits ([#46](https://github.com/TickTockBent/repram/issues/46))
- **Auth + Logging** — HMAC-SHA256 signing/verification, leveled logger ([#47](https://github.com/TickTockBent/repram/issues/47))
- **HTTP transport** — gossip wire format with base64-encoded data, JSON serialization ([#48](https://github.com/TickTockBent/repram/issues/48))
- **Bootstrap** — DNS SRV/A resolution and cluster join handshake ([#49](https://github.com/TickTockBent/repram/issues/49))
- **Gossip protocol** — peer management, √N probabilistic fanout, bounded dedup cache (100k max), health checks, topology sync ([#50](https://github.com/TickTockBent/repram/issues/50))
- **Cluster node** — quorum writes with MessageID tracking, dynamic quorum, ACK routing ([#51](https://github.com/TickTockBent/repram/issues/51))
- **Wire compatibility tests** — 34 tests validating Go ↔ TS JSON parity and HMAC cross-verification using Go-generated test vectors ([#52](https://github.com/TickTockBent/repram/issues/52))
- **Middleware** — rate limiting (token bucket per IP), scanner detection, security headers, CORS ([#53](https://github.com/TickTockBent/repram/issues/53))
- **HTTP server** — full v1 API (PUT/GET/HEAD /v1/data/{key}, /v1/keys with pagination, /v1/health, /v1/status, /v1/topology, /v1/gossip/message, /v1/bootstrap) ([#54](https://github.com/TickTockBent/repram/issues/54))
- **MCP in-process integration** — InProcessClient bypasses HTTP for embedded mode ([#55](https://github.com/TickTockBent/repram/issues/55))
- **Standalone mode** — `REPRAM_MODE=standalone` or `--standalone` runs HTTP server without MCP transport ([#56](https://github.com/TickTockBent/repram/issues/56))
- **Embedded defaults** — port 0 (auto-select), 50MB storage cap, warn log level in MCP mode
- **Lazy MCP imports** — `@modelcontextprotocol/sdk` only loaded when needed (not in standalone mode)
- **248 TypeScript tests** across 13 test files (storage, auth, transport, bootstrap, gossip, cluster, wire compat, middleware, server, in-process client, tools, HTTP client)
- Version bumped to 2.0.0

### Added
- **Peer failure detection** — evicts peers after 3 consecutive failed health checks (~90s); peers rejoin automatically via bootstrap ([#25](https://github.com/TickTockBent/repram/issues/25))
- **Peer eviction metrics** — four Prometheus metrics for cluster health: `repram_peers_active` (gauge), `repram_peer_evictions_total`, `repram_peer_joins_total`, `repram_ping_failures_total` (counters) ([#28](https://github.com/TickTockBent/repram/issues/28))
- **Probabilistic gossip fanout** — enclaves with >10 peers switch from full broadcast O(N) to √N random fanout per hop with epidemic forwarding and message deduplication ([#31](https://github.com/TickTockBent/repram/issues/31))
- **Topology sync peer propagation** — SYNC responses now include the responder's full peer list, enabling transitive peer discovery after partitions and node churn ([#36](https://github.com/TickTockBent/repram/issues/36))
- **Bounded dedup cache** — `seenMessages` capped at 100k entries; evicts expired then oldest half on overflow ([#37](https://github.com/TickTockBent/repram/issues/37))
- **Enclave-scoped replication** — `REPRAM_ENCLAVE` env var defines replication boundaries; nodes in the same enclave replicate data, all nodes share topology; dynamic quorum based on enclave size
- `/v1/topology` endpoint — returns full peer list with enclave membership
- `REPRAM_TRUST_PROXY` env var — proxy headers (`X-Forwarded-For`, `X-Real-IP`) now ignored by default; set to `true` when behind a reverse proxy ([#30](https://github.com/TickTockBent/repram/issues/30))
- `docs/patterns.md` — full usage pattern catalog (agent patterns + general-purpose primitives + key naming conventions)
- `docs/encryption-example.md` — client-side AES-256-GCM example with opaque key derivation ([#18](https://github.com/TickTockBent/repram/issues/18))
- Heartbeat/presence and state machine agent patterns across all documentation
- "Beyond Agents" general-purpose patterns: circuit breaker, ephemeral broadcast, secure relay, session continuity, distributed deduplication, ephemeral pub/sub
- Key naming conventions: namespace prefixes, key generation strategies, prefix listing, collision avoidance ([#17](https://github.com/TickTockBent/repram/issues/17))
- `repram_exists` MCP tool — lightweight HEAD-based existence check with remaining TTL, avoids transferring payload ([#14](https://github.com/TickTockBent/repram/issues/14))
- `REPRAM_WRITE_TIMEOUT` env var — configurable quorum timeout, default 5s (was hard-coded 2s) ([#21](https://github.com/TickTockBent/repram/issues/21))
- `REPRAM_CLUSTER_SECRET` env var — HMAC-SHA256 authentication for gossip and bootstrap messages ([#22](https://github.com/TickTockBent/repram/issues/22))
- `REPRAM_MAX_STORAGE_MB` env var — configurable capacity limit, returns HTTP 507 when full ([#20](https://github.com/TickTockBent/repram/issues/20))
- `REPRAM_LOG_LEVEL` env var — leveled logging (debug/info/warn/error), replaces raw fmt.Printf ([#12](https://github.com/TickTockBent/repram/issues/12))
- **Integration tests** — 7 in-process tests with real HTTP transport: bootstrap discovery, write replication, quorum confirmation, enclave isolation, quorum timeout, 3-node topology, 3-node replication ([#26](https://github.com/TickTockBent/repram/issues/26))
- **Cursor-based pagination for `/v1/keys`** — `?limit=N&cursor=X` parameters for paging through large key sets; keys returned in stable lexicographic order; `next_cursor` in response when more pages available ([#43](https://github.com/TickTockBent/repram/issues/43))
- **Handler-level HTTP tests** — 20 tests for PUT/GET/HEAD/keys/health/status covering TTL parsing, clamping, metadata headers, prefix filtering, overwrite behavior, pagination ([#39](https://github.com/TickTockBent/repram/issues/39), [#43](https://github.com/TickTockBent/repram/issues/43))
- **MCP server tests** — 35 vitest tests covering all 4 tool handlers (store, retrieve, exists, list_keys) and HTTP client (URL construction, header parsing, error handling, null safety) ([#40](https://github.com/TickTockBent/repram/issues/40))
- Test suite: 83 Go tests + 248 TypeScript tests (331 total) covering storage, middleware, gossip auth, peer failure detection, eviction metrics, proxy trust, gossip fanout, topology sync, handler edge cases, pagination, MCP tool handlers, wire compatibility, cluster quorum, and distributed integration ([#11](https://github.com/TickTockBent/repram/issues/11), [#26](https://github.com/TickTockBent/repram/issues/26), [#28](https://github.com/TickTockBent/repram/issues/28), [#31](https://github.com/TickTockBent/repram/issues/31), [#36](https://github.com/TickTockBent/repram/issues/36), [#39](https://github.com/TickTockBent/repram/issues/39), [#40](https://github.com/TickTockBent/repram/issues/40), [#43](https://github.com/TickTockBent/repram/issues/43), [#45](https://github.com/TickTockBent/repram/issues/45))
- CI test workflow — runs `make build` + `go test -race` on push to main and PRs
- CI npm publish workflow — publishes `repram-mcp` to npm on `mcp-v*` tags ([#13](https://github.com/TickTockBent/repram/issues/13))
- `workflow_dispatch` trigger on Docker build workflow
- "Design Decisions: No DELETE" section in whitepaper — documents rationale for TTL-only lifecycle ([#19](https://github.com/TickTockBent/repram/issues/19))

### Changed
- Docker image published as `ticktockbent/repram-node` (was `repram/node`) ([#23](https://github.com/TickTockBent/repram/issues/23))
- `repram-mcp` published to npm — `npx repram-mcp` now works; current version 2.0.0 (embedded node + MCP server)
- Quorum timeout returns 202 Accepted (stored locally, replication pending) instead of 500 ([#21](https://github.com/TickTockBent/repram/issues/21))
- HEAD requests supported on `/v1/data/{key}` for lightweight existence checks
- Rate limiter no longer trusts `X-Forwarded-For` / `X-Real-IP` by default — requires `REPRAM_TRUST_PROXY=true` ([#30](https://github.com/TickTockBent/repram/issues/30))
- Message IDs use atomic counter suffix to prevent theoretical same-nanosecond collisions ([#29](https://github.com/TickTockBent/repram/issues/29))
- Docker Compose uses `condition: service_healthy` for proper startup ordering; ports remapped to 8091-8093
- README links to `docs/patterns.md` instead of inlining the full pattern catalog
- Website uses a single-line teaser for general-purpose patterns instead of a second grid
- CONTRIBUTING.md placeholder URLs replaced with actual GitHub links ([#24](https://github.com/TickTockBent/repram/issues/24))
- Updated `google.golang.org/protobuf` to v1.36.6 (CVE fix, Dependabot alert #11)
- Documented `/v1/keys` cleanup granularity — listings may include keys up to 30s past TTL; direct GET always enforces TTL precisely ([#27](https://github.com/TickTockBent/repram/issues/27))

### Fixed
- **Graceful shutdown** — signal handler now calls `server.Shutdown()` with 10s drain timeout instead of `os.Exit(0)`; in-flight requests complete before process exits ([#33](https://github.com/TickTockBent/repram/issues/33))
- **Quorum tracking for concurrent writes** — `pendingWrites` map keyed on message ID instead of data key; concurrent writes to the same key now track quorum independently ([#34](https://github.com/TickTockBent/repram/issues/34))
- **Request body size enforcement** — `MaxRequestSizeMiddleware` (with `http.MaxBytesReader`) wired into router; previously only `ContentLength` header was checked, which clients could omit ([#35](https://github.com/TickTockBent/repram/issues/35))
- **CORS policy documented** — README now explicitly states that any origin is accepted by design, consistent with permissionless access model ([#38](https://github.com/TickTockBent/repram/issues/38))
- **MemoryStore.Get() data race** — removed `delete()` under read lock; expired entries now returned as not-found, cleaned up by background worker ([#8](https://github.com/TickTockBent/repram/issues/8))
- **MemoryStore returns mutable references** — Get/GetWithMetadata return byte slice copies; Put copies input ([#10](https://github.com/TickTockBent/repram/issues/10))
- **Suspicious request filter false-positives** — removed `python-requests` from blocked UAs; removed URL pattern matching that blocked legitimate keys containing words like `select`, `delete`, `drop` ([#9](https://github.com/TickTockBent/repram/issues/9))
- **Unbounded memory growth** — MemoryStore now enforces configurable capacity limit with proper size tracking through overwrites and expiration ([#20](https://github.com/TickTockBent/repram/issues/20))
- **Dead peers degrade write latency** — peers now evicted after 3 consecutive failed pings; gossip broadcasts no longer accumulate timeouts against unreachable nodes ([#25](https://github.com/TickTockBent/repram/issues/25))
- **Docker Compose bootstrap race** — nodes now wait for dependencies to pass healthchecks before starting

### Removed
- GitHub Pages deployment workflow (site is hosted on Vercel)
- Docker Scout security scan step (requires subscription)

## [2.0.0] — 2025-12-19

v2 is a ground-up rearchitecture. The multi-binary, SDK-dependent, Kubernetes-oriented codebase was replaced with a single Go binary, gossip replication over HTTP, and an MCP server as the primary agent interface. ~21,000 lines of code were removed; ~1,400 were added.

### Added
- **MCP server** (`repram-mcp/`) — TypeScript MCP server exposing `repram_store`, `repram_retrieve`, `repram_list_keys` tools for AI agent integration via Claude Code, Cursor, etc.
- **Unified binary** (`cmd/repram/`) — single Go entry point replacing `cmd/node/`, `cmd/cluster-node/`, `cmd/example/`
- **Versioned API** — all routes under `/v1/` (`/v1/data/{key}`, `/v1/keys`, `/v1/health`, `/v1/status`, `/v1/metrics`, `/v1/gossip/message`, `/v1/bootstrap`)
- `REPRAM_ADDRESS` env var (replaces `NODE_ADDRESS`)
- `REPRAM_PEERS` port convention documented — peers are HTTP addresses (`host:httpPort`)
- DNS-based bootstrap for public network discovery (`REPRAM_NETWORK=public`)
- Private network mode (`REPRAM_NETWORK=private`) for isolated clusters
- Docker Compose 3-node cluster configuration for development

### Changed
- **Messaging overhaul** — "privacy through transience" replaces "zero-trust" across all documentation; dead drop is the primary metaphor; "zero-knowledge nodes" is the consistent term
- **Security framing** — reframed as a natural consequence of ephemerality, not a bolted-on posture. "Nodes hold nothing of lasting value" and "hostile infrastructure is irrelevant" replace "nodes are untrusted" and "hostile network assumption"
- **Resilience framing** — "resilience through ephemerality" articulated: nodes don't need tight coupling because the data lifecycle is self-limiting. No catch-up problem, no split-brain
- README completely rewritten for agent-focused messaging and quick start
- Website (`web/index.html`, `web/script.js`) rewritten with agent-focused content; hackerpunk CSS preserved
- `docs/whitepaper.md` rewritten with dead drop framing, "What REPRAM Is Not" section, deployment model
- `docs/core-principles.md` updated: removed SDK encryption references, removed unimplemented compliance section, added "Resilience Through Ephemerality" (4.3), reframed security principles (5.1)
- `docs/project-overview.md` rewritten with agent coordination angle, MCP server as primary interface
- Gossip internal paths updated to match versioned routes (`/v1/bootstrap`, `/v1/gossip/message`)

### Removed
- `cmd/node/`, `cmd/cluster-node/`, `cmd/example/` — replaced by unified `cmd/repram/`
- `cmd/discovery-demo/` — empty directory
- `demos/fade/` — FADE ephemeral message board demo
- `demos/synth-tree/` — Synth-Tree discovery protocol demo
- `web/synth-tree/` — embedded Synth-Tree visualization
- `deployment/` — Discord bridge, GCP configs, Flux/Kubernetes deployment files
- `docs/future/` — speculative pre-v2 discovery protocol designs
- `DEPLOYMENT_STATUS.md`, `STRATEGIC_SHIFT_SUMMARY.md`
- Legacy unversioned routes (`/gossip/message`, `/bootstrap`)
- All "Proprietary SDK" / "Commercial License" / dual licensing language
- `web/node_modules/` removed from git tracking (3.6MB of vendored deps)
- Dead synth-tree route from `web/vercel.json`

### Fixed
- Gossip bootstrap and message propagation used legacy unversioned paths after route migration — nodes couldn't communicate
- `zod` added as direct dependency in `repram-mcp` (was only a transitive dep of `@modelcontextprotocol/sdk`)
