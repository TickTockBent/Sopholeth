# Sopholeth API

This describes the Go implementation. The [naming guide](rebrand.md) lists
current configuration, tool, and metric names for the direct cutover.

The HTTP client API has no authentication or key ownership. All reads,
existence checks, and listings inspect the contacted node's local store.
A missing value may exist elsewhere.

## Store

```bash
curl -i -X PUT -H "X-TTL: 300" --data-binary "hello" \
  http://localhost:8080/v1/data/example:message
```

Keys occupy one URL path segment. Use `:` or `-` for namespaces, avoid
`/`, and URL-encode keys in clients. Any writer can silently replace an
existing value.

The default node policy allows values up to **100 KiB (102400 bytes)** and
decoded keys up to **1 KiB (1024 bytes)**, measured in bytes rather than
characters. Empty values remain valid. Operators can change these limits with
`NODE_MAX_VALUE_BYTES` and `NODE_MAX_KEY_BYTES`; they apply to both client and
peer writes. Oversized writes are rejected before storage or replication.

| Response | Meaning |
| --- | --- |
| `201 Created` | Stored locally; the node observed its dynamic quorum. |
| `202 Accepted` | Stored locally; quorum was not confirmed within the write timeout. |
| `413 Request Entity Too Large` | The value, key, or ingress request body exceeds its configured limit. |
| `507 Insufficient Storage` | The local payload capacity limit prevented the write. |

Quorum is an acknowledgement threshold, not proof of global agreement.
Neither success response guarantees eventual delivery to every peer. A failed
or interrupted request can also have stored data before its response was
lost; retries are new writes and restart TTL.

### TTL

TTL is an integer number of seconds. A nonempty `?ttl=` query parameter takes
precedence over the `X-TTL` header.

- Omitted TTL defaults to `1800` seconds.
- Positive values below the local minimum are raised to that minimum.
- Values above the local maximum are lowered to that maximum.
- The default minimum is `300` seconds and the default maximum is `86400`.
  Operators can set local bounds; the minimum cannot be configured below 300.
- In the current HTTP handler, malformed, zero, or negative input falls back
  to the default before clamping. An invalid nonempty query value does not
  fall through to the header.

Peer PUTs are also clamped to the receiving node's minimum and maximum,
including zero and negative TTLs. Gossip forwarding carries the clamped TTL.

Each replica starts its own TTL when storing the value. Overwrites reset it.
Expired values are excluded on access; a background sweep every 30 seconds
removes expired entries from the store.

## Retrieve or check existence

```bash
curl -i http://localhost:8080/v1/data/example:message
curl -I http://localhost:8080/v1/data/example:message
```

GET returns `200` with the raw byte value or `404` for an absent or expired
key. HEAD returns the corresponding status and headers without the body.

Successful reads include:

| Header | Meaning |
| --- | --- |
| `X-Created-At` | Local storage time in RFC 3339 format. |
| `X-Original-TTL` | Local TTL in seconds. |
| `X-Remaining-TTL` | Remaining local lifetime, truncated to whole seconds. |

## List keys

```bash
curl "http://localhost:8080/v1/keys?prefix=example:&limit=10"
curl "http://localhost:8080/v1/keys?prefix=example:&limit=10&cursor=example:message"
```

Responses contain a `keys` array and, when another page is available, a
`next_cursor`. Keys are sorted lexicographically. Pass the returned cursor
unchanged, URL-encoded, to continue after that key. Without a positive
`limit`, the response is unbounded.

Pagination is not a snapshot: concurrent writes and expiration can change the
set between requests. An empty result may currently serialize as `null`
rather than `[]`; clients should handle both.

## Live stream

```bash
curl -N http://localhost:8080/v1/stream
```

The response is `text/event-stream`, with one JSON object per named event.
The initial snapshot and subscription are captured together: subsequent
accepted mutations follow the snapshot in storage order. This is local
observation, independent of quorum confirmation.

| Event | JSON fields | Meaning |
| --- | --- | --- |
| `snapshot` | `now`, `node`, `enclave`, `entries` | Every live local entry at subscription time. Replaces the previous view. |
| `put` | `now`, `node`, `entry` | An accepted write or overwrite, including replicated writes. |
| `expire` | `node`, `key`, `revision` | Cleanup removed this revision. Advisory; clients can expire their view by local timestamps. |
| `clock` | `now` | Node time, sent every 15 seconds to keep the connection active. |

An entry contains `key`, `payload` (base64, empty string for an empty value),
`truncated`, `size` (full payload bytes), `ttl_seconds`, `written_at`,
`expires_at`, and `revision`. Timestamps use RFC 3339 with subsecond precision.
The payload preview contains at most 4096 bytes. `revision` is a decimal
string identifying a write within this node process; it is not a network-wide
version and resets after restart. Apply an expiration only to its matching
revision. Reconnection always starts with a new snapshot; there is no event
replay or retained history.

Fetch the current full value through `GET /v1/data/<key>`. It may differ from
the preview because the key can be overwritten or expire between requests.
The read's TTL headers are exposed to browsers through CORS.

The initial limits are eight active stream connections per node, 64 queued
events per subscriber, 4096 snapshot entries, and a conservative 4 MiB JSON
snapshot budget. An individual event has a conservative 16 KiB budget,
including the escaped key. Oversized snapshots return `503`, without a
partial result. A full queue or oversized live event drops the subscriber;
writes continue. Each network write has a five-second deadline. These bounds
add memory overhead beyond the payload storage capacity setting.

`NODE_STREAM=off` makes the endpoint return `404`. Connection/snapshot limits
return `503` with `Retry-After: 5`. Normal request admission/rate limits and
CORS apply; the stream bypasses the ordinary 30-second response timeout.
Responses set `Cache-Control: no-store` and `X-Accel-Buffering: no`.

## Node endpoints

| Method and path | Purpose |
| --- | --- |
| `GET /v1/health` | Process health, node ID, and configured network. |
| `GET /v1/status` | Node status, storage and runtime statistics. |
| `GET /v1/topology` | Known peers, enclave membership, and tree attachment state. |
| `GET /v1/stream` | SSE snapshot and live changes in the local store; see [stream format](#live-stream). |
| `GET /v1/metrics` | Prometheus metrics grouped under `gossip_*`, `http_*`, and `discovery_*`. |
| `POST /v1/bootstrap` | Peer bootstrap; gated by root status in public mode. |
| `POST /v1/gossip/message` | Peer gossip transport. |
| `GET /v1/ws` | WebSocket upgrade for substrate attachments. |

Gossip and WebSocket traffic use the HTTP port. There is no separate gossip
listener. Health alone does not establish replication, quorum, or public
discovery readiness.

When `NODE_CLUSTER_SECRET` is set, HTTP gossip and bootstrap requests carry
their body HMAC in `X-Gossip-Signature`. WebSocket message signatures retain
their existing wire fields.

The HTTP API accepts browser requests from any origin. A reverse proxy can
control transport exposure, but CORS is not client authentication. See
[configuration](configuration.md) for resource limits and proxy settings.

## MCP tools

Run the locally built `server --mcp` through an MCP client. The server hosts an
embedded node and exposes these tool IDs:

| Tool | Arguments | Result |
| --- | --- | --- |
| `store` | Required string `data`; optional `key`, integer `ttl_seconds` | Key, accepted TTL, expiry estimate, and `quorum_status` (`confirmed` or `pending`). |
| `retrieve` | `key` | Value and local TTL metadata, or a missing result. |
| `exists` | `key` | Existence and local TTL metadata. |
| `list_keys` | Optional `prefix` | Matching local keys. |

Store generates a random UUID-shaped key when one is not supplied. MCP
accepts string values; encode binary payloads, such as ciphertext, before
passing them through this interface. MCP listing does not currently expose
HTTP cursor pagination.

MCP TTL defaults to 1800 seconds and numeric values are clamped to local
bounds. Unlike HTTP, an explicit nonpositive integer is clamped to the
minimum, and a nonnumeric argument returns a tool error.

The store response's expiry is an estimate produced after the write completes.
Use retrieve or exists for metadata from the actual stored entry.
