# Sopholeth roadmap

The public test network and soph.stream viewer are running. The next work is
to validate failure behavior, fix the problems it exposes, and simplify setup
before rebuilding. Fade provides the first conversation demo; room-based chat
and a probe simulation follow.

The [core principles](core-principles.md) constrain behavior, and the
[architecture](architecture.md) describes the implementation. The
[public-network plan](public-network-plan.md) is the immediate work queue;
[open issues](https://github.com/TickTockBent/Sopholeth/issues) carry reproductions
and current status.

## Current foundation

The Go node provides anonymous HTTP put/get/list, in-memory TTL storage,
enclave gossip, observed replication acknowledgments, HTTPS/TUF discovery,
WebSocket attachments, and an embedded MCP interface. The `soph` CLI and
`soph serve` share the public trust bundle; the static viewer watches the
three roots by default. The separate dashboard still needs public discovery
integration.

The omega suite implements encrypted custody, publication, renewal, rotation,
and backup checking. Kraid runs the renewal service with hourly checks, daily
refresh, and seven-day freshness validity. See the
[authority record](public-network-authority.md) and
[live validation](public-network-validation.md) for deployment evidence.

## Public test network

Participation remains permissionless: omega endorses bootstrap entry points,
not members or write authors. The initial Linux deployment uses the `default`
enclave, HTTP gossip, and bounded streaming; WebSocket ingress and public
metrics remain excluded.

Healthy discovery, anonymous operations, replication, expiration, and an
unlisted node joining have passed. Root loss, slow peers, partitions, capacity,
and sustained operation still need testing. Native Windows public discovery,
MCP, dashboard deployment, and full transient participation retain their own
scope. Fix failures on exercised paths without making the entire audit queue
or additional identity infrastructure prerequisites for useful testing.

## Before public alpha

This is broader follow-up work after the initial test network is running.
Earlier launch labels refer to this broader scope. The network plan defines
what is required for first bring-up; this list is not a second prerequisite
checklist for that milestone.

- Work through replication, peer bookkeeping, capacity, deduplication, replay,
  and shutdown defects using small, targeted reproductions. Preserve the
  current local-lifetime and opaque-value contract.
- Harden public resource handling: storage overhead, key/value limits,
  listing and expiry work, rate limiting, HTTP deadlines and connections, and
  stream behavior (#217–#221). Deployment bounds used for testing do not close
  these issues or establish denial-of-service resistance.
- Complete native Windows public-client state storage and integration tests
  before claiming Windows `soph join`, profile renewal, and `soph serve` support.
- Validate WebSocket/transient behavior and safeguards before enabling it.
  Dashboard and MCP work retain their own scope.
- Expand fault, churn, partition, capacity, and sustained-load testing as
  problems are fixed. Reuse the [burn-in harness](../test/burnin/README.md) where
  useful; record workloads, versions, resource use, and observed limits.
- Complete release-pipeline review/test gates and artifact verification
  (#223/#224) before distributing a general release. The test deployment uses
  explicitly recorded builds rather than floating image tags.
- Publish accurate support limits, operator recovery/upgrade instructions,
  and the distribution terms already flagged for review in `LICENSE`.

Longer testing happens on the running network and disposable local clusters.
It does not promise independent trusted replicas, global ordering, durable
payload recovery, or authenticated writers. Application-level signatures and
identity remain client concerns.

## Demo web application

[Fade](fade.md) is the first slice: a public message board at
`sopholeth.com/fade/`, recreated from the original demo. It supports live
messages, visible lifetimes, optional callsign/location labels, node selection,
and key lookup on the current testnet APIs.

The later conversation application can add invite-based rooms, optional
client-side encryption, and rooms that expire when participants stop refreshing
them. Participants need no accounts. Keep explaining that readers can still
copy or record messages.

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

A playful first live-network scenario exists in [`cmd/probesim`](../cmd/probesim):
probes exchange beacons, findings, greetings, and replies through TTL'd keys
while radiation corrupts their codebases. Its most useful mutation is key
convention drift: the drifted probe stays healthy at the network layer but
disappears from everyone else's view, who notice only its missing beacon.
It is a traffic generator, not the discrete-event model above; light lag
appears only as flavor text. `make probesim-run` writes about one value per
second to the public testnet; `--dry-run` keeps it in memory.

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
