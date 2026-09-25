# Sopholeth node configuration

The node reads `NODE_*` environment variables, with `NODE_ID` for its ID.
The dashboard uses `DASHBOARD_STATE_DIR` for its own state. The
[migration guide](rebrand.md) covers the direct switch from the old names.

## Network and listeners

| Variable | HTTP node default | Purpose |
| --- | --- | --- |
| `NODE_HTTP_PORT` | `8080` | HTTP API, gossip, and WebSocket port. |
| `NODE_ID` | Generated | Node identifier. Use distinct IDs within a deployment. |
| `NODE_HTTP_ORIGIN` | Empty | External HTTP(S) origin for API/gossip behind a proxy; public roots must match their signed HTTPS origin. |
| `NODE_ADDRESS` | `localhost` | Advertised hostname or address; does not control listener binding. |
| `NODE_NETWORK` | `public` | Signed public discovery or `private` manual discovery. |
| `NODE_PEERS` | Empty | Comma-separated bootstrap seeds, as bare `host:httpPort` or explicit HTTP(S) origins. |
| `NODE_ENCLAVE` | `default` | Data replication boundary. |
| `NODE_INBOUND` | `false` | Accept WebSocket children when `true`; otherwise attach outbound on private networks when seeds are available. Public outbound attachment is deferred. |
| `NODE_MAX_CHILDREN` | `100` | Substrate attachment limit; `0` disables attachments. |
| `NODE_GOSSIP_PORT` | `9090` | Legacy advertised metadata; no listener binds this port. |

The HTTP listener binds all interfaces. Private mode disables public HTTPS
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
| `NODE_MIN_TTL` | `300` | Local TTL floor for client and peer writes, in seconds; normalized to at least 300. |
| `NODE_MAX_TTL` | `86400` | Local TTL ceiling for client and peer writes, in seconds. |
| `NODE_MAX_VALUE_BYTES` | `102400` | Maximum decoded value size (100 KiB); larger writes return `413`. |
| `NODE_MAX_KEY_BYTES` | `1024` | Maximum decoded key size in bytes (1 KiB); longer write keys return `413`. |
| `NODE_WRITE_TIMEOUT` | `5` | Quorum wait in seconds; expiration normally produces `202`. |
| `NODE_MAX_STORAGE_MB` | `0` | Payload capacity in MiB; `0` is unlimited. Full stores reject writes with `507`. |

Size limits must be positive. Maximum TTL must be at least the effective
minimum and at most 2147483647 seconds (the gossip wire format's ceiling);
invalid bounds fail startup. The handlers also have a 10-second write context,
so increasing the quorum timeout alone does not extend that ceiling.

These size caps are node settings, not protocol constants. Keep a network's
limits aligned so peers can accept each other's writes. When raising them,
also raise the ingress body limits: gossip carries base64 values and needs
about 1.4 times the value limit plus room for keys and JSON metadata.

Capacity counts payload bytes, not total process memory. Keys, entry metadata,
connections, and runtime overhead require additional memory. Plan capacity
from measured workloads. All stored writes share key/value caps and TTL
clamping, including HTTP and WebSocket gossip. Broader peer-message validation
and resource accounting remain tracked in #177/#178 and #217.

## Operations

| Variable | Default | Purpose |
| --- | --- | --- |
| `NODE_CLUSTER_SECRET` | Empty | Optional shared HMAC secret for peer traffic; does not authenticate clients. |
| `NODE_RATE_LIMIT` | `100` | HTTP requests per second per source IP. |
| `NODE_TRUST_PROXY` | `false` | Trust forwarded client-IP headers when `true`. |
| `NODE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error`. |
| `NODE_STREAM` | Enabled | Set to `off` to disable `/v1/stream`. See [stream limits](api.md#live-stream). |
| `NODE_STATE_DIR` | `$HOME/.sopholeth/state` | Persistent service-owned state; HTTPS trust lives in its `discovery/` subdirectory. |
| `NODE_PPROF_ENABLED` | `false` | Enable a separate profiling listener. |
| `NODE_PPROF_ADDR` | `127.0.0.1:6060` | Profiling listener address. |

Trust proxy headers only when requests arrive through a trusted proxy that
controls those headers. Keep profiling reachable only through an operator
access path.

Keep `NODE_STATE_DIR` persistent and owned by the service account. It stores
public metadata and rollback history, never signing keys or application
payloads. The trust directory is private; missing/corrupt existing state is not
silently reset. Set the variable explicitly for service accounts without a
home. The node no longer reads the legacy `NODE_CACHE_DIR` DNS cache.

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
