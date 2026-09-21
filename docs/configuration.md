# Sopholeth node configuration

The node reads `NODE_*` environment variables, with `NODE_ID` for its ID.
The dashboard uses `DASHBOARD_STATE_DIR` for its own state. The
[migration guide](rebrand.md) covers the direct switch from the old names.

## Network and listeners

| Variable | HTTP node default | Purpose |
| --- | --- | --- |
| `NODE_HTTP_PORT` | `8080` | HTTP API, gossip, and WebSocket port. |
| `NODE_ID` | Generated | Node identifier. Use distinct IDs within a deployment. |
| `NODE_ADDRESS` | `localhost` | Advertised hostname or address; does not control listener binding. |
| `NODE_NETWORK` | `public` | Signed public discovery or `private` manual discovery. |
| `NODE_PEERS` | Empty | Comma-separated bootstrap seeds, as `host:httpPort`. |
| `NODE_ENCLAVE` | `default` | Data replication boundary. |
| `NODE_INBOUND` | `false` | Accept WebSocket children when `true`; otherwise attach outbound when seeds are available. |
| `NODE_MAX_CHILDREN` | `100` | Substrate attachment limit; `0` disables attachments. |
| `NODE_GOSSIP_PORT` | `9090` | Legacy advertised metadata; no listener binds this port. |

The HTTP listener binds all interfaces. Private mode disables public DNS
discovery; it does not add authentication or bind to loopback. An enclave is
a replication boundary, not an access-control boundary.

Setting manual peers bypasses signed discovery even in public mode. Such a
node does not become a verified root and returns `403` from public
`/v1/bootstrap`. Public roots must use verified discovery with no manual
peer override.

## Storage and replication

| Variable | Default | Purpose |
| --- | --- | --- |
| `NODE_REPLICATION` | `3` | Factor used to calculate the dynamic quorum threshold. |
| `NODE_MIN_TTL` | `300` | Local client-write TTL floor in seconds, normalized to at least 300. |
| `NODE_MAX_TTL` | `86400` | Local client-write TTL ceiling in seconds. |
| `NODE_WRITE_TIMEOUT` | `5` | Quorum wait in seconds; expiration normally produces `202`. |
| `NODE_MAX_STORAGE_MB` | `0` | Payload capacity in MiB; `0` is unlimited. Full stores reject writes with `507`. |

Keep maximum TTL at least as large as the effective minimum. The handlers
also have a 10-second write context, so increasing the quorum timeout alone
does not extend that ceiling.

Capacity counts payload bytes, not total process memory. Keys, entry metadata,
connections, and runtime overhead require additional memory. Plan capacity
from measured workloads. Replicated writes need a separate validation audit
before public launch; client-side clamping is not a complete wire policy.

## Operations

| Variable | Default | Purpose |
| --- | --- | --- |
| `NODE_CLUSTER_SECRET` | Empty | Optional shared HMAC secret for peer traffic; does not authenticate clients. |
| `NODE_RATE_LIMIT` | `100` | HTTP requests per second per source IP. |
| `NODE_TRUST_PROXY` | `false` | Trust forwarded client-IP headers when `true`. |
| `NODE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error`. |
| `NODE_CACHE_DIR` | See below | Verified public root-list cache directory. |
| `NODE_PPROF_ENABLED` | `false` | Enable a separate profiling listener. |
| `NODE_PPROF_ADDR` | `127.0.0.1:6060` | Profiling listener address. |

Trust proxy headers only when requests arrive through a trusted proxy that
controls those headers. Keep profiling reachable only through an operator
access path.

The root-list cache defaults to `$HOME/.sopholeth/cache`, falling back to
`/var/cache/sopholeth` when no home directory is available. Set a writable
`NODE_CACHE_DIR` in containers. It stores signed discovery metadata, not
application payloads.

## MCP defaults

With `--mcp`, the default network is private, HTTP port is `0` (OS-assigned),
payload capacity is 50 MiB, gossip metadata port is `0`, and log level is
`warn`. Explicit environment values override these defaults. Logs go to
stderr; stdout carries MCP messages.

MCP mode still starts an HTTP listener on all interfaces. A dedicated bind
setting and its defaults remain [prelaunch work](roadmap.md#before-public-alpha).

## Dashboard state

`DASHBOARD_STATE_DIR` overrides the dashboard state directory. Its default is
`$HOME/.local/state/sopholeth/dashboard`, falling back to
`/var/lib/sopholeth/dashboard`. This holds discovery metadata and topology
snapshots, not application payloads.
