# Sopholeth architecture

Sopholeth makes forgetting part of the storage lifecycle. A client leaves
named bytes, reachable peers carry them, and each node serves them for a
bounded local lifetime. Useful state survives through new writes from
participants who still need it.

The reference node is written in Go. The same executable runs an HTTP service
or an MCP stdio server with an embedded node. The service builds as `server`;
the [`soph` client](cli.md) is a separate executable.

## Components

| Component | Responsibility |
| --- | --- |
| [Memory store](../internal/storage/memory.go) | Local bytes, TTL checks, cleanup, and payload capacity. |
| [Node entry point](../cmd/server/main.go) | Configuration, HTTP handlers, startup, and shutdown. |
| [Cluster](../internal/cluster/node.go) | Local writes, replication, and acknowledgement tracking. |
| [Gossip](../internal/gossip/protocol.go) | Peer knowledge, health checks, forwarding, and message deduplication. |
| [Tree manager](../internal/tree/manager.go) | Substrate and transient WebSocket attachment. |
| [Discovery](../internal/discovery/session.go) | HTTPS/TUF root discovery, runtime expiry, and refresh. |
| [Trust client](../internal/trust/bootstrap/README.md) | Signed metadata validation and durable rollback protection. |
| [MCP adapter](../internal/mcp/server.go) | Agent tools over stdio, sharing the local node. |
| [Dashboard](../cmd/dashboard/main.go) | Topology observation through a separate operator application. |

## Write lifecycle

1. A client writes a key, byte value, and TTL.
2. The receiving node stores the value in memory and begins its local TTL.
3. When replication peers are known, it queues the write for enclave gossip,
   whatever the quorum size. It does not wait for the sends.
4. Each receiving replica checks the write against its key and value limits
   and drops duplicates by message ID. It then stores the value with a new
   local TTL, queues any epidemic forwards and child fan-out, and answers its
   sender. It queues an ACK to the origin only if its store accepted the
   value, because an ACK means it holds a copy. A replica whose store refuses
   the value (for example, store full) still forwards it, so peers past it
   with room still receive it; it sends no ACK, answers its sender with the
   store error, and counts the miss in `gossip_relay_store_failed_total`. A
   write over the limits is rejected before any of this and is not forwarded.
5. The origin reports whether it observed the quorum threshold before its
   write timeout.

The origin gives each write a message ID of 128 random bits, independent of
the key, the node and the clock, so two writes to one key in the same clock
tick, on one node or on two, never share an ID. Forwards, ACKs and the
WebSocket envelope carry the origin's ID unchanged; the origin matches an ACK
to a write by that ID, then counts it once per sender (see
[Replication and quorum](#replication-and-quorum)).
6. Reads and listings exclude a value at local expiration. A periodic sweep
   removes the expired entry from the store.

Gossip sends (writes, forwards and ACKs) go through a bounded dispatcher in
[internal/gossip/dispatcher.go](../internal/gossip/dispatcher.go). Each peer
has a FIFO queue drained by a few workers (4 by default), which is also its
limit on sends in flight, and a global cap bounds sends in flight across
peers. Sends to one peer may complete out of order. Each send has its own deadline. Sends are
not tied to the client's request, so a slow or unreachable peer delays only
its own queue, and a `202` does not cancel delivery to other peers. When a
peer's queue is full, new messages for it are dropped and counted in
`gossip_send_queue_dropped_total`; there is no retry. A message that has
waited 15 seconds, in its queue and for a global send slot, is dropped unsent
and counted in `gossip_send_expired_total` (see
[Duplicates and late copies](#duplicates-and-late-copies)). Evicting a peer or
stopping the node cancels its queued and in-flight sends.

There is no client DELETE operation, durable payload archive, or payload
backfill when a node rejoins. Restarting a node loses its stored values.
Discovery caches and dashboard snapshots are operational metadata, separate
from the payload store.

## Replication and quorum

An enclave defines the set of peers that should receive the same values.
Every reachable member is a replication target; the design does not shard
data within an enclave. Other enclaves can share topology without exchanging
application values. An enclave name is not a credential.

For up to ten replication peers, gossip uses full broadcast. Above that
threshold, it uses approximately square-root fanout and epidemic forwarding.
A bounded message-ID cache suppresses repeated processing. This design aims
to spread writes across the enclave, but partitions, finite TTL, resource
limits, and process failures can prevent delivery.

### Duplicates and late copies

A write reaches a node once per path, so most nodes receive several copies.
The first copy is stored, ACKed and forwarded; later copies must not be, or a
copy that arrives late restarts the node's local lifetime and starts a
second propagation wave. Three local rules bound this (#152). None of them
compares clocks between nodes or reads the message's `timestamp`.

- **Send age.** A sender drops a gossip send that has waited more than 15
  seconds to start, measured on its own clock. With the 5-second send
  deadline, a copy reaches a peer within 20 seconds of the sender accepting
  it, or not at all.
- **Hop limit.** A PUT carries `hops`, the number of times it has been
  forwarded. The origin sends 0 and each forward adds one. A node stores and
  ACKs a copy whatever its count, but forwards only while `hops` is below 8.
  With the send age, every copy of a write is gone from the mesh within
  9 × 20 = 180 seconds of the write.
- **Duplicate window.** A node remembers each message ID for 180 seconds,
  the longest a copy can stay in the mesh, so any later copy is recognized
  and dropped: it is not stored again, ACKed or forwarded. The cache holds
  at most 100,000 IDs, so its memory is bounded. When it is full it drops
  the oldest ID first (#168) and counts the drop in
  `gossip_dedup_capacity_evictions_total`; the effective window is then
  shorter than 180 seconds. A copy that arrives after its ID was dropped is
  stored again with a fresh local lifetime and may be forwarded, but only
  until its hop count runs out, so the second wave stays bounded.

In simulations with asynchronous delivery, a wave can run past 8 hops in a
large enclave, because a node is often first reached along a long fast path.
The limit still made no measurable difference to coverage: a copy stopped at
the limit is stored, and its would-be targets are reached by other paths.

An overwrite is a new write with a new message ID, so it is never taken for
a late copy and propagates normally. The rules apply to copies inside the
mesh. Anyone can still write the same key and value again through the client
API or post a gossip message with `hops` 0; that is a new write, and its
lifetime starts on each replica when the replica accepts it. Two writes to
one key have no order: an older write's copy can still reach a node after a
newer write.

Nodes without these rules ignore `hops` and forward every copy as hop 0, so
the limit holds only along paths of nodes that have it. Below the fanout
threshold, nodes do not forward at all.

The current quorum calculation is:

```text
effective = min(known enclave nodes including self, replication factor)
quorum = max(1, floor(effective / 2) + 1)
```

The origin computes the quorum once, from its peer table when the write
starts; peers joining or leaving mid-write do not move it. The local write
supplies the first confirmation, under the origin's own node ID. A
single-node enclave can therefore confirm a write locally. The replication
factor controls the confirmation threshold, not a fixed placement set.

Each further confirmation is an ACK from a distinct sender ID, counted once
per write. The origin counts an ACK only if its sender is a replication peer
in the origin's own peer table: a peer in the origin's enclave now, or when
the write started (so a peer evicted mid-write still counts). Above the
fanout threshold that includes relays the origin never sent to, since they
ACK the origin directly. A repeated ACK from one sender, an ACK from an ID
the origin has no peer record for, one from another enclave, and one
claiming the origin's own ID are not counted. ACKs arriving after the write
completed or timed out are ignored.

HTTP returns `201` when the node observed enough distinct peers report a
stored copy to meet that quorum, and `202` when its quorum wait expires after
local storage. MCP reports `confirmed` or `pending`. There is no durable
retry queue behind `202`.

This is replication bookkeeping from the origin's local peer table, not
authentication. Without `NODE_CLUSTER_SECRET`, a sender ID is an unchecked
claim, and a host that joins under several IDs can supply several ACKs. A
`201` is the origin's observation, not proof of independent copies and not a
consensus certificate.

## Same-key writes and time

The last PUT delivered to a node wins at that node. Writes do not compare
timestamps, merge values, reserve keys, or elect an authoritative writer.
Different delivery orders can leave different live values at different nodes,
even after connectivity recovers. A later write reaching those nodes or
expiration can remove that disagreement.

TTL begins on local acceptance, including acceptance of a replica. The wire
timestamp does not enforce a global expiration deadline. Delayed replication
can leave a value live after another node has expired its copy. A rewrite
starts a new lifetime.

Applications needing ordering, signatures, conflict resolution, or
observation age must encode those in their values and key conventions.
Client-side merge logic cannot recover competing values that were already
overwritten; use separate keys for branches that must remain observable.

## Discovery and reachability

All nodes run the same software, with separate discovery and connection roles:

| Role | Capability |
| --- | --- |
| Public root | Appears in the verified signed list and answers public bootstrap requests. |
| Substrate | Accepts inbound WebSocket attachments. |
| Transient | Attaches outbound to a substrate when one is available. |

These roles can overlap. They route and discover peers; they do not confer
ownership of client data. Roots do not approve client writes.

An under-peered node sends `SYNC_REQUEST` to ask known peers for topology.
Each response is a one-way `SYNC` announcement, including the responder's own
entry. Bootstrap notifications and WebSocket welcome entries also use `SYNC`;
announcements never request another peer list. This prevents an endless exchange
when two nodes remain below their desired peer count. Upgrade participating
nodes together: older builds do not understand `SYNC_REQUEST`, and their legacy
`SYNC` messages are accepted as announcements without a peer-list response.

Private deployments supply manual HTTP seed addresses. Public discovery uses
a compiled public TUF bundle to verify an HTTPS bootstrap directory and
connect to its authenticated HTTPS origins. Runtime expiry withdraws official
root roles and recovery seeds while ordinary peer traffic continues. Advertised
`http_origin` preserves TLS routing through peer exchange. Omega does not
authorize ordinary peers or writes. See
[signed discovery](discovery.md) for the wire format and current limits.

WebSocket attachments carry the same gossip messages as HTTP. They let a
transient use an outbound connection to participate without relying on direct
inbound reachability. Public WebSocket participation remains disabled pending
multi-substrate validation and safeguards; the testnet uses HTTP gossip.

## Access and retention

Anyone who can reach the client API can read, list, or overwrite values.
Nodes can read plaintext and observe traffic metadata. Optional peer HMAC
does not add client access control. Keys may appear in diagnostics; payload
values must not be logged.

Encrypt confidential values before storing them. Authenticated encryption or
signatures can help clients reject altered content, but do not prevent
deletion by overwrite, withholding, or replay. Expiration is enforced by
cooperating nodes; it cannot erase copies retained by an observer or promise
secure erasure of process memory.

## Why this primitive extends beyond agents

Handoffs, temporary conversations, and presence signals all need information
that is useful while current. The longer-term research case is coordination
among autonomous, self-replicating probes with intermittent contact, limited
resources, and no shared present.

A probe can leave a signpost that later visitors inspect, update, and renew.
Information survives because successive participants choose to carry it,
rather than because a permanent archive preserves every observation.

The current implementation is terrestrial. Deep-time TTL representation,
bounded regional topology, store-and-carry transport, and evolving trust
require experiments and possibly new protocol versions. The
[simulation milestone](roadmap.md#probe-simulation) studies those questions
without treating them as current network capabilities.
