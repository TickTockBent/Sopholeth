# Sopholeth core principles

Sopholeth provides temporary shared state through opaque bytes, local
lifetimes, and peer replication. These principles constrain the core;
applications build their own meaning above it.

## Opaque key-value storage

Nodes store keys, byte values, and the transport and lifecycle metadata needed
to handle them. They do not interpret values, assign schemas or ownership, or
index application content. Stored values must not be written to logs or
metrics.

Opaque does not mean encrypted. A node holding plaintext can read it.
Operational metadata, including keys and peer addresses, can be visible in
API responses and diagnostic logs.

## Permissionless client access

There are no client accounts, API credentials, or key owners. Anyone who can
reach a node can read, list, and overwrite its data through the client API.
Knowing a key is an addressing mechanism, not access control: keys are
discoverable through listing.

Nodes may track network sources temporarily for rate limiting. Optional
shared-secret authentication between peers does not authenticate clients or
make enclaves private.

## Mandatory local lifetimes

Every stored value has a TTL. An overwrite is a new write with a fresh local
lifetime. The client API provides no DELETE operation and no separate TTL
extension operation.

Expired values disappear from reads and listings at local expiration.
Background cleanup reclaims expired entries from the store. This is a data
lifecycle rule, not a secure memory-erasure guarantee.

Replicas start their lifetimes when they accept a value, so expiration is not
a shared global deadline. A stopped process loses its in-memory payloads.
The node has no durable payload archive or recovery facility. Observers can
retain or republish what they read.

The [API reference](api.md) defines the current TTL defaults and bounds.

## Peer replication within enclaves

Every reachable member of an enclave is a replication target. The core does
not shard payloads or elect a primary data owner. Finite TTL, partitions,
resource limits, and node failures can prevent delivery.

All nodes run the same software. Public roots provide bootstrap discovery;
substrates and transients provide different connection capabilities. These
roles do not grant authority over client values.

Accepted writes are local first. Quorum reporting distinguishes observed
confirmation from local acceptance without confirmation. It does not supply
consensus, exclusive ownership, or a globally ordered register.

## Bounded history, explicit limits

Nodes do not retain payload history to reconcile every missed write.
Partitions can leave different live values at different nodes, and healing
connectivity does not backfill missed payloads. Later writes and expiration
can remove disagreement without preserving it as permanent history.

Applications must tolerate absence and stale observations. Encryption,
signatures, identity, provenance, and conflict resolution belong in the
client protocol. Sopholeth reduces retained state; it cannot guarantee
confidentiality or force other participants to forget.
