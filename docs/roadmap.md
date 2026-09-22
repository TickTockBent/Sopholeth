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

Public launch remains pending. The compiled omega public key is unset and
public discovery fails closed. Discovery publication is pending, and the restored Go
WebSocket tree needs sustained validation. The [rebrand checklist](rebrand.md)
tracks the naming transition.

## First public network

Build the working `soph omega` suite first, then document and rehearse a public
network with three fixed-port roots in the shared `default` enclave. The
[public-network plan](public-network-plan.md) defines the implementation
order, required evidence, and operator cutover. It takes priority over the
remaining viewer and MCP backlog.

Before remote rehearsal and public exposure, resolve unauthenticated peer
mutations ([#211](https://github.com/TickTockBent/Sopholeth/issues/211)),
identity-blind liveness ([#213](https://github.com/TickTockBent/Sopholeth/issues/213)),
and serial broadcast stalls ([#212](https://github.com/TickTockBent/Sopholeth/issues/212)).
Settle peer identity and admission before discovery transport integration;
the launch plan sequences implementation after the remaining omega work and
the SYNC-storm prerequisite. A shared-secret lab does not satisfy these
public launch gates.

The local viewer and initial `soph serve` are implemented. Use them and the
`soph` HTTP client to validate the remote roots. The
[viewer plan](soph-stream-plan.md) retains the remaining feature work; finishing
its local-cluster polish is not a prerequisite for omega development.
Keep the existing aesthetic and named-network behavior.

MCP is explicitly deferred. Dashboard deployment and full transient-writer
participation are later surfaces; any endpoint exposed by a root must still
meet its applicable safety and lifecycle requirements.

## Before public alpha

### Establish precise behavior

- Authenticate peer identity and changes to addresses/enclaves, define
  admission and safe identity replacement, and require fresh authenticated
  liveness responses. Persist root identities across restarts.
- Count confirmations from distinct eligible replicas. Cover duplicate,
  unknown, cross-enclave, and late ACKs, relayed ACKs, and topology changes
  while a write is pending.
- Bound send/forward concurrency, queues, and lifetimes. Slow peers must not
  block a healthy quorum or healthy-peer delivery, and write deadlines must
  cover dispatch as well as quorum waiting. Preserve truthful pending outcomes.
- Preserve local TTL semantics. Test delayed replication, clock disagreement,
  overwrite near expiration, and cleanup accounting.
- Validate incoming gossip independently of client HTTP validation: TTL
  bounds, message IDs and types, payload limits, addresses, and enclave scope.
- Handle signed-list expiration while a node is running. Root status and
  recovery seeds must not remain authorized solely by an expired list.
- Bound public ingress so one client cannot exhaust a root: count keys and
  per-entry overhead against capacity, limit key and value size on every
  ingress path, set server deadlines and connection limits, make listing cost
  proportional to the page, aggregate IPv6 clients for rate limiting, and keep
  the stream available as data grows (#217–#221).
- Make standalone listener binding and public exposure explicit. Embedded
  MCP listener and ephemeral-port work is deferred.
- Test startup, cancellation, repeated shutdown, and exposed connection
  lifecycles. Complete broader transient reconnection tests before supporting
  that participation mode.

### Produce repeatable evidence

- Keep build and race tests green; add parser fuzzing and targeted
  integration coverage for the behavior above.
- Validate the initial three-root standalone deployment and HTTP clients:
  root loss, churn, partitions, healing, duplicate delivery, and capacity
  exhaustion. The broader substrate/transient matrix is required before
  supporting that later mode; MCP-specific tests are deferred.
- Measure throughput, latency, process memory, and quorum outcomes from both
  the load driver and nodes. Do not infer request throughput from allocations.
- Run a ramp to failure and a sustained soak with recorded commits,
  configuration, workloads, fault timings, and recovery outcomes.
- Use and adapt the [burn-in harness](../test/burnin/README.md) for the actual
  three-root configuration, preserving driver failures and accepted TTLs.

Historical runs provide useful observations but do not validate the current
tree, a public deployment, or untested failure modes.

### Prepare operations

- Gate publishing on review and tests: protect `main` and release tags, and
  publish only from tested release tags (#223). Pin and sign release
  artifacts as the supported install path requires (#224).
- Publish the renamed artifacts and site after validating this code rebrand.
- Reconcile the legacy proprietary appendix in `LICENSE` with the intended
  distribution terms before public release.
- Complete the unified `soph omega` workflow, rotation and recovery rehearsal,
  and production operator guide before creating the real authority. Install
  only its public trust material in the release and on nodes.
- Deploy three independent, reachable roots across failure domains using the
  rehearsed runbook and shared `default` enclave.
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

MCP handoffs and presence signals can be evaluated later; they do not gate
the public network or demo. Keep agent orchestration and application identity
outside the node.

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
