# Sopholeth roadmap

The next phase delivers three things: the first public network, a web
application that makes it useful to people, and a simulation that tests its
longer-term premise.

The [core principles](core-principles.md) constrain production behavior.
The [architecture](architecture.md) describes the current implementation.
This roadmap describes work still required; it is not a list of shipped
guarantees.

## Current foundation

The Go node implements in-memory TTL storage, an HTTP API, enclave gossip,
quorum reporting, signed discovery and caching, WebSocket substrate/transient
attachments, and an embedded MCP interface. A separate dashboard observes
topology.

Public launch remains pending. The compiled omega public key is a placeholder,
discovery names still reference the previous domain, and the restored Go
WebSocket tree needs sustained validation. The [rebrand checklist](rebrand.md)
tracks the naming transition.

## Before public alpha

### Establish precise behavior

- Count confirmations from distinct eligible replicas. Cover duplicate,
  unknown, cross-enclave, and late ACKs, relayed ACKs, and topology changes
  while a write is pending.
- Preserve local TTL semantics. Test delayed replication, clock disagreement,
  overwrite near expiration, and cleanup accounting.
- Validate incoming gossip independently of client HTTP validation: TTL
  bounds, message IDs and types, payload limits, addresses, and enclave scope.
- Handle signed-list expiration while a node is running. Root status and
  recovery seeds must not remain authorized solely by an expired list.
- Make listener binding explicit, especially for embedded MCP nodes.
- Test startup, cancellation, repeated shutdown, and WebSocket reconnection
  as complete lifecycles.

### Produce repeatable evidence

- Keep build and race tests green; add parser fuzzing and targeted
  integration coverage for the behavior above.
- Run an all-Go cluster with multiple substrates and transient clients.
  Exercise parent loss, root loss, churn, partitions, healing, duplicate
  delivery, and capacity exhaustion.
- Measure throughput, latency, process memory, and quorum outcomes from both
  the load driver and nodes. Do not infer request throughput from allocations.
- Run a ramp to failure and a sustained soak with recorded commits,
  configuration, workloads, fault timings, and recovery outcomes.
- Update the [burn-in harness](../test/burnin/README.md) so its scripts and
  dashboards can reproduce the run without personal paths or obsolete
  implementation dependencies.

Historical runs provide useful observations but do not validate the current
tree, a public deployment, or untested failure modes.

### Prepare operations

- Complete the source, configuration, tooling, artifact, and website rebrand.
- Generate the first real omega key offline, document custody and recovery,
  and bake its public key into the release.
- Deploy independent, reachable roots across failure domains.
- Publish signed discovery under the acquired domain, with monitoring for
  expiration, failed refreshes, unreachable roots, and degraded replication.
- Verify startup with valid DNS, valid cached fallback, invalid signatures,
  expired records, and no trusted discovery source.
- Publish an operator guide with resource sizing, supported versions,
  upgrade and rollback steps, and an incident contact.

**Exit:** a fresh release can join the public network through verified
discovery, exchange expiring data, and recover from the documented faults.
Known limits and reproducible validation results accompany the release.

## Demo web application

Build a small temporary conversation application: chat that behaves more
like speech than a permanent transcript.

The first experience should support invite-based rooms, live messages with
visible remaining lifetimes, optional client-side encryption, and rooms that
expire when participants stop refreshing them. Participants need no accounts.
The interface should explain that readers can still copy or record messages.

Keep the application protocol above Sopholeth:

- Give each message its own key.
- Use temporary room metadata to discover live messages.
- Put timestamps, signatures, and encryption envelopes inside client payloads.
- Handle concurrent metadata updates explicitly, using separate branch keys
  or a merge scheme that does not rely on atomic overwrite.
- Test slow readers, missing values, expiry, and interrupted connectivity.

**Exit:** two people can join a room, converse through the public network,
leave, and later find no live transcript served by the application. Publish
the client protocol so another application can reproduce the interaction.

Use the same milestone to dogfood MCP handoffs and presence signals. Keep
agent orchestration and application identity outside the node.

## Probe simulation

Build a separate simulation that consumes Sopholeth's primitive. Begin with
a small live-network scenario that writes and reads expiring signposts.
For larger and longer experiments, use a deterministic discrete-event model
checked against the current implementation's behavior.

Model distance and communication delays, local clocks, intermittent contact,
probe mortality and dormancy, descendant lineages, replication costs,
bandwidth and storage limits, and regional enclaves. The simulator may own
a global clock; simulated probes must act on local observations.

A first signpost protocol can carry observations, routes, hazards, signer
lineage, references to earlier observations, and refresh history. Visitors
validate what they understand, apply their own conflict policy, and choose
whether to renew or carry a signpost onward. Nodes still store opaque bytes.

Measure:

- Useful information survival versus stale-information retention.
- Refresh cost and TTL choices under delayed or lost communication.
- Information movement relative to the expansion frontier.
- Lineage divergence, conflicting observations, and loss of contact.
- Storage exhaustion and the cost of topology knowledge.

Visualize signpost lifetimes, refresh events, communication horizons, and
knowledge fading across disconnected regions.

**Exit:** repeatable runs and published assumptions explain what was learned.
Hypothetical protocol changes are explicitly versioned experiments, separate
from production behavior.

## Protocol independence and later research

Capture the current wire contract in a normative specification, with golden
fixtures and a black-box conformance runner. Cover HTTP and WebSocket gossip,
TTL, quorum outcomes, discovery, HMAC canonicalization, and error behavior.
A second implementation should target that contract instead of reconstructing
it from the Go source.

Use simulation results to evaluate longer TTL representations, bounded local
topology, store-and-carry transport, protocol evolution, and lineage trust.
The existing terrestrial discovery system is not a galactic trust model.
Production changes require evidence and compatibility decisions.

For each release, publish the behavior changes, supported interfaces,
validation artifacts, known limits, and operator migration steps. Preserve
historical evidence without presenting it as a current runbook.
