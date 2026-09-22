# Sopholeth usage patterns

Sopholeth provides PUT with TTL, local reads, silent overwrite, and automatic
expiration. Applications supply meaning, encryption, and any conflict rules.
These patterns assume missing or delayed data is an acceptable outcome.

## Handoffs and temporary work

**Dead drop.** One participant writes a payload and shares its key through an
agreed channel. Another retrieves it before expiration. For rendezvous,
participants can derive a key from shared context. Use encryption for
confidential values. Reading does not consume the entry, and delivery is not
guaranteed.

**Scratchpad.** A worker stores intermediate state and overwrites it as work
progresses. Each write starts a fresh TTL. This suits disposable working
state, not the only copy of a result that must survive.

**Encrypted relay.** Clients exchange ciphertext through the network and
share encryption material separately. See the
[encryption example](encryption-example.md). Nodes can still observe keys,
value sizes, and traffic; readers can retain ciphertext after expiration.

## Presence and state

**Heartbeat.** A participant refreshes a key before its TTL expires.
Observers poll GET or HEAD for a recent signal. Absence means no live signal
at the contacted node; it can reflect a stopped writer, delay, partition,
overwrite, or node restart. It is not proof of failure. Account for the
five-minute minimum accepted TTL when choosing refresh intervals.

**Advisory claim.** A participant writes that it intends to work on a task.
Other participants may use that as a coordination hint. There is no atomic
create-if-absent, compare-and-swap, or exclusive lock: two workers can both
observe absence and proceed.

**Job status.** A worker overwrites a key with `queued`, `in_progress`, or
`complete`. Readers get a temporary local observation, not a guaranteed
transition history. A missing completion record means the result is unknown.
Concurrent writers can disagree; use per-writer keys when branches matter.

## Application building blocks

| Pattern | Useful behavior | Application responsibility |
| --- | --- | --- |
| Health hint | Poll a refreshed status value before sending work. | Treat missing or stale data according to local fallback policy. |
| Temporary broadcast | Publish the latest value under a known key. | Observe the local stream or poll; tolerate missed intermediate values and define behavior after expiry. |
| Session state | Refresh disposable state during activity. | Encrypt sensitive data and authenticate sessions outside the node. |
| Deduplication hint | Remember recently observed event IDs for a TTL window. | Tolerate concurrent processing; this does not provide exactly-once execution. |
| Temporary conversation | Give each message a key and discover live messages through room metadata. | Handle encryption, polling, ordering, and metadata races in the client. |

The [SSE stream](api.md#live-stream) observes local writes and expiration,
starting with a snapshot on each connection. Reconnects do not replay missed
events. The WebSocket transport remains a node-gossip interface.

## Key naming

Keys are single URL path segments. Use `:` or `-` for structure, avoid
`/`, and URL-encode keys when constructing requests. Prefixes are conventions;
nodes do not understand or reserve them.

| Example | Purpose |
| --- | --- |
| `myapp:handoff:<random-id>` | One handoff payload. |
| `myapp:scratch:<worker-id>` | A worker's temporary state. |
| `myapp:claim:<task-id>:<worker-id>` | An advisory claim from one worker. |
| `myapp:heartbeat:<worker-id>` | A refreshed presence signal. |
| `myapp:state:<job-id>:<writer-id>` | One writer's job observation. |

The current MCP store tool generates a random UUID-shaped key by default.
Random keys make accidental collisions unlikely; they do not confer ownership.
Human-readable prefixes help discovery but may expose application context.
Anyone can list or overwrite them.

For deterministic rendezvous, agree on an unambiguous serialization of all
context fields, such as a JSON array. If the context should not be exposed in
the key name, use HMAC with a separately shared random secret, as in the
encryption example. A derived key remains listable and is not an access token.

Prefix discovery is local and best effort:

```bash
curl "http://localhost:8080/v1/keys?prefix=myapp:heartbeat:&limit=100"
```

Follow `next_cursor` for additional pages. A listing may change while you
page through it, and listed keys may expire before retrieval.
