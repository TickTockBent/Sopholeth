# Open-issue inventory — 2026-09-29

Reviewed all **69 issues** open at the start against main `03ff9b2`,
merged PRs, current issue comments, and the live activation evidence in
[PR #250](https://github.com/TickTockBent/Sopholeth/pull/250). **Nine are complete; 60 retain work.**
An open PR is not a completed fix: #248 remains under review for #162/#220.

The first three-root milestone is complete. Remaining severity labels describe
defect impact, not new prerequisites for that milestone. Historical
`launch:blocker` labels came from the broader alpha audit; use the
[current plan](public-network-plan.md) for sequencing. MCP remains deferred;
WS, stream, and dashboard findings apply before those surfaces are enabled.

The review uses source inspection and existing tests, not a fresh reproduction
of every reported failure. Targeted race checks for rollback, runtime expiry,
retry scheduling, HTTPS/bootstrap, saved-profile refresh, and peer routing
passed. Six selected omega initialization, encrypted-backup, signing-validation,
rotation, and renewal-boundary tests passed. No production code changed.

## Completed issues

| Issue | Resolution | Evidence |
| --- | --- | --- |
| [#80](https://github.com/TickTockBent/Sopholeth/issues/80) | Three roots are deployed; verified discovery, anonymous data operations, replication, expiry, and an unlisted guest passed. Record in #250; longer fault testing stays in #180. | [docs/public-network-validation.md](../docs/public-network-validation.md) |
| [#160](https://github.com/TickTockBent/Sopholeth/issues/160) | Durable TUF rollback protection replaced public DNS discovery in #205/#243; old DNS is disabled for public use. | [internal/trust/bootstrap/client_test.go](../internal/trust/bootstrap/client_test.go) |
| [#169](https://github.com/TickTockBent/Sopholeth/issues/169) | #243 removed PONG peer mutation; signed updates replace peer objects instead of modifying snapshots. | [internal/gossip/bindings.go](../internal/gossip/bindings.go) |
| [#172](https://github.com/TickTockBent/Sopholeth/issues/172) | Public Session retries only after backoff, without the extra normal interval; covered in #243. | [internal/discovery/session_test.go](../internal/discovery/session_test.go) |
| [#193](https://github.com/TickTockBent/Sopholeth/issues/193) | Public root and recovery-seed lookups expire independently of blocked refresh; #243 covers expiry and restoration. | [internal/discovery/session.go](../internal/discovery/session.go) |
| [#195](https://github.com/TickTockBent/Sopholeth/issues/195) | soph omega implements encrypted custody, online/membership/root rotation, recovery, and runbooks; old binary retired. | [docs/omega-operations.md](../docs/omega-operations.md) |
| [#196](https://github.com/TickTockBent/Sopholeth/issues/196) | Locked staging, exclusive publication, no-replace directory promotion, and recovery replace unsafe keypair creation. | [internal/omega/authority_test.go](../internal/omega/authority_test.go) |
| [#197](https://github.com/TickTockBent/Sopholeth/issues/197) | Replacement signer validates manifests/keys and verifies exact releases through the trust client. | [internal/omega/publish_test.go](../internal/omega/publish_test.go) |
| [#199](https://github.com/TickTockBent/Sopholeth/issues/199) | Journaled publication, separate unattended renewal custody, HTTPS verification, Vercel hosting, and daily renewal are implemented and exercised. | [docs/public-network-authority.md](../docs/public-network-authority.md) |

## Remaining issues

Partial means some subfindings are resolved; the row names the residual.

| Issue | Status | Remaining work / disposition | Source checked |
| --- | --- | --- | --- |
| [#67](https://github.com/TickTockBent/Sopholeth/issues/67) | Open | Attachment scoring and periodic reassessment remain absent; failure recovery is a different feature. | [internal/tree/manager.go](../internal/tree/manager.go) |
| [#68](https://github.com/TickTockBent/Sopholeth/issues/68) | Open | Overlap migration and hysteresis remain unimplemented; depends on #67. | [internal/tree/manager.go](../internal/tree/manager.go) |
| [#126](https://github.com/TickTockBent/Sopholeth/issues/126) | Open | Embedded MCP still binds :port rather than an explicitly scoped listener. | [cmd/server/main.go](../cmd/server/main.go) |
| [#128](https://github.com/TickTockBent/Sopholeth/issues/128) | Open | MCP ReadBytes remains unbounded; reader buffer size is not a frame limit. | [internal/mcp/server.go](../internal/mcp/server.go) |
| [#129](https://github.com/TickTockBent/Sopholeth/issues/129) | Open | Unknown tool still returns method-not-found rather than invalid-params. | [internal/mcp/server.go](../internal/mcp/server.go) |
| [#131](https://github.com/TickTockBent/Sopholeth/issues/131) | Open | MCP pending-quorum coverage remains absent; single-node confirmation is covered. | [internal/mcp/server_test.go](../internal/mcp/server_test.go) |
| [#132](https://github.com/TickTockBent/Sopholeth/issues/132) | Open | Initialize still echoes a requested protocol version without supported-version negotiation. | [internal/mcp/server.go](../internal/mcp/server.go) |
| [#140](https://github.com/TickTockBent/Sopholeth/issues/140) | Open | Accepted WS children still lack heartbeat startup; public WS remains excluded. | [cmd/server/main.go](../cmd/server/main.go) |
| [#142](https://github.com/TickTockBent/Sopholeth/issues/142) | Open | Shutdown still does not join connection readers/watchdogs or close all child sockets. | [internal/tree/manager.go](../internal/tree/manager.go) |
| [#143](https://github.com/TickTockBent/Sopholeth/issues/143) | Open | Topology still omits parent_id while unattached; explicit attached state remains optional polish. | [cmd/server/main.go](../cmd/server/main.go) |
| [#144](https://github.com/TickTockBent/Sopholeth/issues/144) | Open | Malformed hello closes the socket without releasing the no-hello watchdog immediately. | [cmd/server/main.go](../cmd/server/main.go) |
| [#147](https://github.com/TickTockBent/Sopholeth/issues/147) | Open | Multi-substrate/transient and failure matrix remains; HTTP guest validation does not establish WS behavior. | [cmd/server/ws_integration_test.go](../cmd/server/ws_integration_test.go) |
| [#151](https://github.com/TickTockBent/Sopholeth/issues/151) | Open | Standalone Serve returns before the signal goroutine completes shutdown. | [cmd/server/main.go](../cmd/server/main.go) |
| [#152](https://github.com/TickTockBent/Sopholeth/issues/152) | Open | Local TTL clamp does not define stale-message/replay policy beyond the seen window. | [internal/cluster/node.go](../internal/cluster/node.go) |
| [#153](https://github.com/TickTockBent/Sopholeth/issues/153) | Open | Transient-origin PUT/upstream relay and reverse ACK integration remain incomplete. | [internal/cluster/node.go](../internal/cluster/node.go) |
| [#154](https://github.com/TickTockBent/Sopholeth/issues/154) | Open | No SetReadLimit on WS connections; decoded write caps do not bound frame allocation. | [internal/transport/ws/connection.go](../internal/transport/ws/connection.go) |
| [#155](https://github.com/TickTockBent/Sopholeth/issues/155) | Open | Dashboard poller still follows peer destinations and default redirects without destination policy. | [internal/dashboard/poller.go](../internal/dashboard/poller.go) |
| [#156](https://github.com/TickTockBent/Sopholeth/issues/156) | Open | Unreachable dashboard nodes can retain host:port IDs despite the projection's stripping contract. | [internal/dashboard/builder.go](../internal/dashboard/builder.go) |
| [#157](https://github.com/TickTockBent/Sopholeth/issues/157) | Open | Failed dashboard cycles still mutate a published snapshot pointer in place. | [internal/dashboard/orchestrator.go](../internal/dashboard/orchestrator.go) |
| [#158](https://github.com/TickTockBent/Sopholeth/issues/158) | Open | Ephemeral HTTP port is still read after constructing its consumers. | [cmd/server/main.go](../cmd/server/main.go) |
| [#159](https://github.com/TickTockBent/Sopholeth/issues/159) | Open | Proxy-header trust remains a boolean; tunnel/NGINX header replacement mitigates this deployment only. | [internal/node/middleware.go](../internal/node/middleware.go) |
| [#162](https://github.com/TickTockBent/Sopholeth/issues/162) | Pending PR | Expired-unswept bytes still count against capacity on main. #248 proposes the fix but is unmerged. | [internal/storage/memory.go](../internal/storage/memory.go) |
| [#163](https://github.com/TickTockBent/Sopholeth/issues/163) | Open | Peer PUT is still marked seen before storage and stops propagation when storage fails. | [internal/cluster/node.go](../internal/cluster/node.go) |
| [#164](https://github.com/TickTockBent/Sopholeth/issues/164) | Open | ACK counter still lacks sender dedup and fixed per-write eligibility/threshold. | [internal/cluster/node.go](../internal/cluster/node.go) |
| [#165](https://github.com/TickTockBent/Sopholeth/issues/165) | Open | Topology repair still stops once peer count reaches RF minus one; SYNC storm fix does not fix asymmetric eviction. | [internal/gossip/protocol.go](../internal/gossip/protocol.go) |
| [#166](https://github.com/TickTockBent/Sopholeth/issues/166) | Open | PUT message IDs still combine key and UnixNano; cross-process collision policy remains. | [internal/cluster/node.go](../internal/cluster/node.go) |
| [#167](https://github.com/TickTockBent/Sopholeth/issues/167) | Open | Receive path still sends ACK and forwards synchronously before replying. | [internal/cluster/node.go](../internal/cluster/node.go) |
| [#168](https://github.com/TickTockBent/Sopholeth/issues/168) | Open | Midpoint dedup eviction can still discard a burst's whole recent set. | [internal/gossip/protocol.go](../internal/gossip/protocol.go) |
| [#170](https://github.com/TickTockBent/Sopholeth/issues/170) | Open | Fixed ten-second handler context still conflicts with longer write waits and accepted outcomes. | [cmd/server/main.go](../cmd/server/main.go) |
| [#171](https://github.com/TickTockBent/Sopholeth/issues/171) | Open | Single-segment HTTP keys still differ from MCP/gossip capabilities; key length caps do not settle encoding. | [cmd/server/main.go](../cmd/server/main.go) |
| [#173](https://github.com/TickTockBent/Sopholeth/issues/173) | Open | Accepted child PUT dispatch still lacks inbound enclave scoping; WS remains excluded. | [cmd/server/main.go](../cmd/server/main.go) |
| [#175](https://github.com/TickTockBent/Sopholeth/issues/175) | Open | WS hello/close handler transitions, retry, and echo behavior remain incomplete. | [internal/tree/manager.go](../internal/tree/manager.go) |
| [#176](https://github.com/TickTockBent/Sopholeth/issues/176) | Open | Dashboard workers can still block filling the bounded BFS queue. | [internal/dashboard/poller.go](../internal/dashboard/poller.go) |
| [#177](https://github.com/TickTockBent/Sopholeth/issues/177) | Partial | #247 fixes ingress TTL/key/value bounds and chunked 413 reporting. Lifecycle/semantic checks remain; preserve new seeds-as-peers logging nit. | [cmd/server/main.go](../cmd/server/main.go) |
| [#178](https://github.com/TickTockBent/Sopholeth/issues/178) | Partial | Retired omega subfindings are superseded. WS hello validation, socket/handler lifetime, envelope types, and closed-send reporting remain. | [internal/transport/ws/connection.go](../internal/transport/ws/connection.go) |
| [#179](https://github.com/TickTockBent/Sopholeth/issues/179) | Partial | Unused packages:write permission is gone. Dashboard route documentation/release history and dependency housekeeping remain. | [.github/workflows/docker-build.yml](../.github/workflows/docker-build.yml) |
| [#180](https://github.com/TickTockBent/Sopholeth/issues/180) | Partial | Healthy public baseline passed; sustained load, restart/fault regimes, and timer-driven due renewal observation remain. | [docs/public-network-validation.md](../docs/public-network-validation.md) |
| [#181](https://github.com/TickTockBent/Sopholeth/issues/181) | Open | Windowed gap fill does not exist; replay/lifetime contract must precede it. | [internal/storage/memory.go](../internal/storage/memory.go) |
| [#182](https://github.com/TickTockBent/Sopholeth/issues/182) | Open | No join snapshot, payload backfill, or embedded-client fallback; CLI direct connections do not implement these. | [internal/cluster/node.go](../internal/cluster/node.go) |
| [#183](https://github.com/TickTockBent/Sopholeth/issues/183) | Partial | Boundary/TTL regressions and live expiry add evidence; payload-log guards and replay/key-contract cases remain. | [internal/cluster/write_limits_test.go](../internal/cluster/write_limits_test.go) |
| [#194](https://github.com/TickTockBent/Sopholeth/issues/194) | Partial | Node/CLI HTTPS bootstrap is complete. Retain only dashboard TUF/origin migration and future authenticated WSS paths. | [internal/transport/ws/client.go](../internal/transport/ws/client.go) |
| [#198](https://github.com/TickTockBent/Sopholeth/issues/198) | Partial | Linux join/profile/viewer refresh is complete in #243. Native Windows durable public discovery remains explicitly planned. | [internal/trust/bootstrap/lock_other.go](../internal/trust/bootstrap/lock_other.go) |
| [#211](https://github.com/TickTockBent/Sopholeth/issues/211) | Partial | Signed roots and existing ordinary routes are pinned; PONG cannot flip enclave. Retain bounded referrals and liveness correlation, preserving open admission. | [internal/gossip/protocol.go](../internal/gossip/protocol.go) |
| [#212](https://github.com/TickTockBent/Sopholeth/issues/212) | Open | Originating broadcasts still send serially inline; slow peers can delay healthy delivery. | [internal/gossip/protocol.go](../internal/gossip/protocol.go) |
| [#213](https://github.com/TickTockBent/Sopholeth/issues/213) | Partial | Fixed root IDs/origins are documented and deployed. PING/PONG correlation and stale same-address IDs remain. | [internal/gossip/protocol.go](../internal/gossip/protocol.go) |
| [#214](https://github.com/TickTockBent/Sopholeth/issues/214) | Partial | 100 KiB defaults and deployed ingress prevent the current failure. Raising caps can still exceed the independent ten-MiB encoded-body limit. | [cmd/server/main.go](../cmd/server/main.go) |
| [#215](https://github.com/TickTockBent/Sopholeth/issues/215) | Partial | #243 disables automatic public WS attachment. Quorum-of-one still returns before broadcasting. | [internal/cluster/node.go](../internal/cluster/node.go) |
| [#217](https://github.com/TickTockBent/Sopholeth/issues/217) | Partial | 1 KiB keys, 100 KiB values, and explicit root budgets are deployed. Storage still counts only value bytes, leaving empty-entry overhead unaccounted. | [internal/storage/memory.go](../internal/storage/memory.go) |
| [#218](https://github.com/TickTockBent/Sopholeth/issues/218) | Partial | Stream is disabled on roots and 1 KiB keys prevent the old long-key trigger. Snapshot growth and global slot starvation remain when enabled. | [internal/storage/stream.go](../internal/storage/stream.go) |
| [#219](https://github.com/TickTockBent/Sopholeth/issues/219) | Partial | NGINX bounds deployed ingress; standalone node http.Server still has no header/read/idle or connection limits. | [cmd/server/main.go](../cmd/server/main.go) |
| [#220](https://github.com/TickTockBent/Sopholeth/issues/220) | Pending PR | Full scan/sort and unbounded default listing remain on main; ordered paging and incremental sweep are proposed in unmerged #248. | [internal/storage/memory.go](../internal/storage/memory.go) |
| [#221](https://github.com/TickTockBent/Sopholeth/issues/221) | Open | Exact-IP buckets, unbounded bucket-map growth, and the map-wide hot-path lock remain. | [internal/node/middleware.go](../internal/node/middleware.go) |
| [#222](https://github.com/TickTockBent/Sopholeth/issues/222) | Partial | Deployment policy exposes health/status/topology and excludes metrics. Node-native diagnostic-listener/configuration policy remains. | [docs/examples/public-network/nginx.conf](../docs/examples/public-network/nginx.conf) |
| [#223](https://github.com/TickTockBent/Sopholeth/issues/223) | Partial | Path filters and bundle fingerprint gate exist; main remains unprotected, tags unrestricted by rulesets, and publishing is independent of tests. | [.github/workflows/docker-build.yml](../.github/workflows/docker-build.yml) |
| [#224](https://github.com/TickTockBent/Sopholeth/issues/224) | Partial | Deployment records exact local images and a pinned ingress digest; base/action pinning and distributed artifact signing/provenance remain. | [Dockerfile](../Dockerfile) |
| [#231](https://github.com/TickTockBent/Sopholeth/issues/231) | Open | Public bundle remains embedded; explicit file/URL override and isolated saved trust identity are absent. | [internal/discovery/bundle.go](../internal/discovery/bundle.go) |
| [#234](https://github.com/TickTockBent/Sopholeth/issues/234) | Open | Publish/rotate reports still begin with initial-generation key-file status; active-generation inspection is status-only. | [internal/omega/authority.go](../internal/omega/authority.go) |
| [#235](https://github.com/TickTockBent/Sopholeth/issues/235) | Open | Key-check failure action still directs operators to passphrase/key files even when the renewal home is missing. | [internal/omega/encrypted_authority.go](../internal/omega/encrypted_authority.go) |
| [#236](https://github.com/TickTockBent/Sopholeth/issues/236) | Open | Privileged inheritance still chowns reopened files without link-count/exclusive-creation protection; #240 fixes a different ownership problem. | [internal/omega/platform_linux.go](../internal/omega/platform_linux.go) |
| [#246](https://github.com/TickTockBent/Sopholeth/issues/246) | Open | Gossip values are still JSON/base64; iteration-two raw-value transport remains a proposal. Key encoding must follow #171. | [internal/gossip/http_transport.go](../internal/gossip/http_transport.go) |

## Scope corrections

- #177 retains the operator's new startup-log finding: seeds are reported as
  peers. It is polish. The completed TTL/key/value ingress limits are removed
  from its active work; lifecycle and semantic-validation residuals stay open.
- #194 now owns the remaining dashboard/public-WSS consumer migration.
  Node/CLI HTTPS bootstrap is complete. #198 now owns native Windows public
  discovery; Linux saved-profile/viewer refresh is complete.
- #211 no longer proposes root-issued membership, a closed public mesh, or
  trusted peer identities. Route protection is implemented; bounded referrals
  and liveness correlation remain. Joining and writing stay permissionless.
- #214 remains a raised-cap configuration problem. #217 still lacks entry/key
  capacity accounting. #218 remains an enabled-stream growth/subscriber problem.
  Deployment caps and excluded routes are recorded as mitigations.
- #180 retains fault/recovery/load evidence and observation of the first
  timer-driven due renewal. The healthy-network check is complete, not a reason
  to claim the larger matrix passed. Startup simplification and a deliberate
  teardown/rebuild follow the running-network tests.

This is a dated inventory. Issue bodies carry the current scope; historical
findings and operator comments are retained rather than erased.
