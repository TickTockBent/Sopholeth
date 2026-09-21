# Sopholeth node configuration

The current implementation reads `REPRAM_*` environment variables.
`SOPH_*` variables are not implemented yet. See the
[rebrand checklist](rebrand.md) before changing deployment configuration.

## Network and listeners

| Variable | HTTP node default | Purpose |
| --- | --- | --- |
| `REPRAM_HTTP_PORT` | `8080` | HTTP API, gossip, and WebSocket port. |
| `REPRAM_NODE_ID` | Generated | Node identifier. Use distinct IDs within a deployment. |
| `REPRAM_ADDRESS` | `localhost` | Advertised hostname or address; does not control listener binding. |
| `REPRAM_NETWORK` | `public` | Signed public discovery or `private` manual discovery. |
| `REPRAM_PEERS` | Empty | Comma-separated bootstrap seeds, as `host:httpPort`. |
| `REPRAM_ENCLAVE` | `default` | Data replication boundary. |
| `REPRAM_INBOUND` | `false` | Accept WebSocket children when `true`; otherwise attach outbound when seeds are available. |
| `REPRAM_MAX_CHILDREN` | `100` | Substrate attachment limit; `0` disables attachments. |
| `REPRAM_GOSSIP_PORT` | `9090` | Legacy advertised metadata; no listener binds this port. |

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
| `REPRAM_REPLICATION` | `3` | Factor used to calculate the dynamic quorum threshold. |
| `REPRAM_MIN_TTL` | `300` | Local client-write TTL floor in seconds, normalized to at least 300. |
| `REPRAM_MAX_TTL` | `86400` | Local client-write TTL ceiling in seconds. |
| `REPRAM_WRITE_TIMEOUT` | `5` | Quorum wait in seconds; expiration normally produces `202`. |
| `REPRAM_MAX_STORAGE_MB` | `0` | Payload capacity in MiB; `0` is unlimited. Full stores reject writes with `507`. |

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
| `REPRAM_CLUSTER_SECRET` | Empty | Optional shared HMAC secret for peer traffic; does not authenticate clients. |
| `REPRAM_RATE_LIMIT` | `100` | HTTP requests per second per source IP. |
| `REPRAM_TRUST_PROXY` | `false` | Trust forwarded client-IP headers when `true`. |
| `REPRAM_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error`. |
| `REPRAM_CACHE_DIR` | See below | Verified public root-list cache directory. |
| `REPRAM_PPROF_ENABLED` | `false` | Enable a separate profiling listener. |
| `REPRAM_PPROF_ADDR` | `127.0.0.1:6060` | Profiling listener address. |

Trust proxy headers only when requests arrive through a trusted proxy that
controls those headers. Keep profiling reachable only through an operator
access path.

The root-list cache defaults to `$HOME/.repram/cache`, falling back to
`/var/cache/repram` when no home directory is available. Set a writable
`REPRAM_CACHE_DIR` in containers. It stores signed discovery metadata, not
application payloads.

## MCP defaults

With `--mcp`, the default network is private, HTTP port is `0` (OS-assigned),
payload capacity is 50 MiB, gossip metadata port is `0`, and log level is
`warn`. Explicit environment values override these defaults. Logs go to
stderr; stdout carries MCP messages.

MCP mode still starts an HTTP listener on all interfaces. A dedicated bind
setting and its defaults remain [prelaunch work](roadmap.md#before-public-alpha).
