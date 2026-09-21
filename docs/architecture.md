# Sopholeth architecture

Sopholeth makes forgetting part of the storage lifecycle. A client leaves
named bytes, reachable peers carry them, and each node serves them for a
bounded local lifetime. Useful state survives through new writes from
participants who still need it.

The reference node is written in Go. The same executable runs an HTTP service
or an MCP stdio server with an embedded node. The service builds as `server`; the future `soph` client is separate.
Component names and interfaces are described in the [migration guide](rebrand.md).

## Components

| Component | Responsibility |
| --- | --- |
| [Memory store](../internal/storage/memory.go) | Local bytes, TTL checks, cleanup, and payload capacity. |
| [Node entry point](../cmd/server/main.go) | Configuration, HTTP handlers, startup, and shutdown. |
| [Cluster](../internal/cluster/node.go) | Local writes, replication, and acknowledgement tracking. |
| [Gossip](../internal/gossip/protocol.go) | Peer knowledge, health checks, forwarding, and message deduplication. |
| [Tree manager](../internal/tree/manager.go) | Substrate and transient WebSocket attachment. |
| [Trust](../internal/trust/signedlist.go) | Signed public root-list validation. |
| [MCP adapter](../internal/mcp/server.go) | Agent tools over stdio, sharing the local node. |
| [Dashboard](../cmd/dashboard/main.go) | Topology observation through a separate operator application. |

## Write lifecycle

1. A client writes a key, byte value, and TTL.
2. The receiving node stores the value in memory and begins its local TTL.
3. When replication peers are known, it sends the write through enclave gossip.
4. Each accepting replica stores the value with a new local TTL.
5. The origin reports whether it observed the quorum threshold.
6. Reads and listings exclude a value at local expiration. A periodic sweep
   removes the expired entry from the store.

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

The current quorum calculation is:

```text
effective = min(known enclave nodes including self, replication factor)
quorum = max(1, floor(effective / 2) + 1)
```

The local write supplies the first confirmation. A single-node enclave can
therefore confirm a write locally. The replication factor controls the
confirmation threshold, not a fixed placement set.

HTTP returns `201` when the node observes quorum and `202` when its quorum
wait expires after local storage. MCP reports `confirmed` or `pending`.
There is no durable retry queue behind `202`.

The current acknowledgement handler counts confirmations without keeping a
set of distinct senders, and can recalculate the threshold as topology
changes. Auditing sender eligibility, duplicate ACKs, and an in-flight
write's quorum target is a [public-alpha gate](roadmap.md#before-public-alpha).
Treat current confirmation as the implementation's observation, not a
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

Private deployments supply manual HTTP seed addresses. Public discovery uses
an Ed25519 trust anchor compiled into the binary to verify a DNS-delivered
root list. It authenticates that list, not every peer or client. See
[signed discovery](discovery.md) for the wire format and current limits.

WebSocket attachments carry the same gossip messages as HTTP. They let a
transient use an outbound connection to participate without relying on direct
inbound reachability. The restored Go tree needs a sustained multi-substrate
validation run before launch.

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
