# First public network: omega, then three roots

Status: agreed delivery path, 2026-09-23. Placeholder rejection and the public
release fingerprint gate, durable trust client, and atomic disposable
`soph omega init`, local-directory `publish`, unattended online renewal, and
HTTPS verification in `status`, and online/membership/root-key `rotate` are implemented for
disposable authorities, including root-expiry recovery and replacement with one
rotated root signer unavailable. Encrypted custody now covers initialization, publication, all rotations, renewal
provisioning, public status, and active-key backup verification. Production mode
uses schema 2 by default, with throwaway backup/reset rehearsals. No live authority
has been created. Hosted metadata, compiled trust-bundle/release integration,
discovery consumers, and deployment remain pending. The peer-plane
audit adds three launch blockers: unauthenticated peer mutations (#211),
serial write broadcast (#212), and identity-blind liveness (#213). These must
be resolved before the remote three-root rehearsal and public exposure.
The public-ingress and release audit adds six more blockers: uncounted key
and entry overhead (#217), stream collapse (#218), missing server timeouts
(#219), full-keyspace listing (#220), the IPv6-bypassable rate limiter
(#221), and ungated publishing (#223). Fix #223 before building any release
candidate.
The [omega audit](omega-signing-audit.md) records the initial defects;
[issue #80](https://github.com/TickTockBent/Sopholeth/issues/80) tracks launch
readiness and the live issue queue.

The first deliverable is a working `soph omega` suite. The second is a
documented, rehearsed procedure to stand up three public root nodes. This
sequence takes priority over completing the broader viewer and MCP backlog.

## Initial scope

- Three independently reachable, fixed-port standalone roots in the shared
  `default` enclave. Enclaves scope replication; they do not provide privacy.
- The `soph` HTTP client for joining, writing, reading, and listing.
  `soph serve` and soph.stream observe the same nodes in the existing aesthetic.
- Native Windows support for public CLI discovery, including `soph join` and
  renewal of saved public profiles. The initial root-node rehearsal uses Linux.
- Verified discovery, automated renewal, graceful key rotation, and explicit
  recovery procedures, all operated through the existing CLI.
- Remote endpoints supplied by the project owner for deployment rehearsal.
  Addresses, access, DNS ownership, and hosting configuration will be recorded
  when preparing that rehearsal.

MCP is deferred, including embedded-node listeners, ephemeral ports, protocol
updates, and MCP-specific tests. Dashboard deployment, NAT-bound transient
writer completion, attachment optimization, and payload backfill are follow-up
work. Viewer polish and the editor-proxy connection problem do not gate omega
development or root bring-up.

Any interface exposed by a root still needs its applicable safeguards. In
particular, deferring transient features does not make an exposed WebSocket
endpoint safe. The deployment must either fix its relevant defects or exclude
that endpoint through a verified exposure policy. Three roots describes the
bootstrap root set, not a maximum of three members in the public mesh.

## 1. Build the omega suite

Placeholder rejection ([#192](https://github.com/TickTockBent/Sopholeth/issues/192))
now disables public discovery in ordinary builds and rejects the old all-zero
anchor in DNS and cache verification. Version-tagged node images require the
expected authority fingerprint. Next is the production trust design
([#195](https://github.com/TickTockBent/Sopholeth/issues/195)). Use disposable
authorities throughout development and rehearsal.

Choose the authority, membership-signing, and freshness-renewal roles before
implementing changes to the wire format. Evaluate TUF with a small integration
spike covering a reviewed library release, Go toolchain compatibility,
persisted client state, and operator complexity. The completed
[spike and design recommendation](omega-trust-design.md) establish go-tuf
v2.4.2 as the proposed integration baseline, with separate offline approval
and online renewal roles. The application now uses Go 1.27.1 and includes the
[durable TUF client](../internal/trust/bootstrap/README.md), with bundle/manifest
validation, bounded HTTPS, locked state checkpoints, and expiring accepted
views. Atomic encrypted authority initialization, journaled local-directory
publication, unattended renewal with separate online custody, all three rotation
roles, and HTTPS verification run through `soph omega`. Hosted publication and its
disposable rehearsal are implemented. Remote scheduling, the compiled bundle/release
gate, and discovery consumer/transport integration remain.

The command surface is:

| Command | Required outcome |
| --- | --- |
| `soph omega init` | Safely create authority and recovery material, identify its fingerprint, and produce the public trust bundle needed by a release. Existing or interrupted initialization must not silently replace keys. |
| `soph omega provision-renewal` | Recoverably hand off publication history to a separate operational home containing only snapshot/timestamp keys. |
| `soph omega publish` | Validate an approved root manifest, sign through the chosen key store, publish it, and verify the metadata clients actually receive. Return failure when publication or verification fails. |
| `soph omega status` | Report accepted versions, authority fingerprints, roots, expiration, renewal/publication health, and actionable failures, with script-friendly output. |
| `soph omega rotate` | Prepare and carry out an authenticated successor-key transition with defined overlap, retained transition metadata, adoption checks, and retirement criteria. |

`init`, `provision-renewal`, `publish` (including `--renew`), online/membership/root-key `rotate`,
and `status --verify` are implemented for encrypted and disposable Linux authorities; see
[omega operations](omega-operations.md). Publication uses either a local HTTPS-served
directory or the Vercel adapter, retaining immutable history and verifying the
exact served release through the real client. Renewal preserves the exact approved membership,
uses only online keys, and caps freshness at the offline approval deadlines.
Refresh daily with seven-day snapshot/timestamp validity; check and retry hourly.
The operator guide includes scheduler examples, monitoring fields, and the
prepare/review/apply procedures for all three rotation roles, including offline
custody, root-expiry recovery, and recoverable signed handoff. Root application
requires explicit renewal of the unchanged membership approval. Encrypted custody
now supports the complete operator lifecycle. The
[custody proposal](omega-production-custody.md) targets one operator's connected
workstation, encrypted key files, a tested backup, and a new authority schema.
Its launch criteria are authenticated discovery and a usable operator recovery
path. Air gaps and independent signing machines are not requirements. Authority
compromise or unrecoverable state may lead to an explicit experimental-network
reset, with a new trust bundle that clients must deliberately adopt.
Both custody slices are implemented. Production initialization uses encrypted
schema 2 by default; new passphrases require at least 12 characters, while older
files remain unlockable for recovery. Publication and all rotations select the
active encrypted operator keys; unattended renewal uses separate service-owned
snapshot/timestamp files. Public inspection needs no password. The existing
alternating-rotation test covers encrypted interruption recovery, backup restore
with working copies unavailable, and an explicit new-identity reset. The standalone
`omega` binary is retired from normal builds; the old DNS signer remains only as
a burn-in helper until consumers migrate.

**HTTPS base paths and serving configuration — implemented:** bundles,
initialization, fetching, and publication verification support `/omega/` with
strict directory confinement. The `sopholeth.io` configuration serves existing
numbered metadata and hash-addressed targets with immutable caching; timestamp
and missing-object responses are uncached. Missing metadata gets a JSON 404 and
internal/pending names cannot be served. The existing encrypted lifecycle runs
against a path-prefixed HTTPS fixture; a Vercel local-router smoke test checks
headers, misses, and docs routing.

**Vercel adapter and hosted rehearsal — complete:** `publish`, renewal,
and rotation application accept a metadata-project config and upload only public
objects reconstructed from the retained journal. Staging cannot assign production
domains. Exact bytes/cache checks precede promotion, promotion state is journaled
for retry, and success requires canonical HTTPS/TUF verification. Hourly checks
do not deploy unchanged metadata; daily refresh and seven-day expiry remain.

The separate `sopholeth-omega` project and project-level regex rewrite are configured.
The apex serves directly and www redirects to it. The
[2026-09-23 hosted rehearsal](omega-hosted-rehearsal.md) verified signed publication,
CLI renewal without offline custody, online-key rotation, fresh/returning clients,
and survival of docs deployment/rollback under a unique rehearsal prefix.

**Remote operator check:** the operator configured Kraid and completed a successful
systemd service invocation after repairing the public-spool ownership bug found
in the [manual encrypted rehearsal](omega-kraid-rehearsal.md). The timer is now
disabled and removed; a full daily scheduled cycle remains unobserved. The code
fix and a real UID-transition regression cover the setup failure. Reserve
`/omega/` itself for the final authority to avoid
immutable-cache collisions. No production authority has been created. Deployment
credentials remain separate from authority keys. Recheck the
[domain preflight](../sites/README.md#omega-metadata-hosting)
before public `soph omega init`; keep the client's redirect rejection.
Consumer integration must still follow the peer-identity decision below;
compiled trust bundles and #223 remain release gates. Node hosts receive public
trust material only.

Reserve `authority.json` schema 1 permanently for disposable authorities.
Schema 2 now separates public identity from encrypted key storage. Production
custody must never store authority material in schema 1, extend that schema to
enable production use, or relabel existing disposable keys as production keys.

Implement the related findings in the selected design:

- Persist authenticated ordering and reject rollback
  ([#160](https://github.com/TickTockBent/Sopholeth/issues/160)). Identical
  metadata refreshes remain idempotent.
- Expire runtime root authority and recovery seeds independently of refresh
  success ([#193](https://github.com/TickTockBent/Sopholeth/issues/193)); fix
  retry timing ([#172](https://github.com/TickTockBent/Sopholeth/issues/172)).
- Authenticate bootstrap connections and carry endpoint transport information
  consistently ([#194](https://github.com/TickTockBent/Sopholeth/issues/194)).
  Before integrating that transport into discovery consumers, settle the
  peer identity and admission model with #211/#213 in the next section.
  Signed discovery authenticates metadata; it does not prove who sent a
  peer message. Per-node credentials must remain separate from omega's
  authority and publication keys. This design dependency does not displace
  the remaining omega operator work.
- Make initialization exclusive and recoverable
  ([#196](https://github.com/TickTockBent/Sopholeth/issues/196)); validate keys,
  inputs, and exact signed output
  ([#197](https://github.com/TickTockBent/Sopholeth/issues/197)).
- Unattended disposable renewal, verified publication, and expiration/failure
  signals are implemented; deployment-specific scheduling and alert wiring
  remain part of rehearsal ([#199](https://github.com/TickTockBent/Sopholeth/issues/199)).

**Exit:** disposable-key integration tests demonstrate initialization,
publication, verified discovery, renewal, rotation without rebuilding every
client, rollback rejection, expiration, interrupted publication, and recovery
of returning clients. The operator guide states custody, retention, overlap,
and loss/compromise procedures, including the limits of recovery when the
ultimate authority is lost or compromised. The old manual DNS signing loop
does not serve as the production workflow.

## 2. Prepare the roots and public client

Fix the under-peered SYNC storm
([#150](https://github.com/TickTockBent/Sopholeth/issues/150)) before cluster
fault/load runs. It is triggered by normal recovery, even in a small network.
Implement the root-facing gates in this dependency order:

| Order | Work | Required outcome |
| --- | --- | --- |
| 1 | SYNC storm: [#150](https://github.com/TickTockBent/Sopholeth/issues/150) | Under-peered recovery terminates with bounded work, making fault/load runs possible. |
| 2 | Peer identity, admission, and liveness: [#211](https://github.com/TickTockBent/Sopholeth/issues/211), [#213](https://github.com/TickTockBent/Sopholeth/issues/213), with authenticated transport [#194](https://github.com/TickTockBent/Sopholeth/issues/194) and eligible confirmations [#164](https://github.com/TickTockBent/Sopholeth/issues/164) | Only verified identities can change their peer records or supply eligible confirmations; liveness proves the expected responder. |
| 3 | Bounded outbound delivery: [#212](https://github.com/TickTockBent/Sopholeth/issues/212), coordinated with inbound forwarding [#167](https://github.com/TickTockBent/Sopholeth/issues/167) and write outcomes [#170](https://github.com/TickTockBent/Sopholeth/issues/170) | Slow peers cannot hold up a healthy quorum or starve healthy delivery; all work has explicit limits and lifetimes. |
| 4 | Bounded public ingress: [#217](https://github.com/TickTockBent/Sopholeth/issues/217), [#219](https://github.com/TickTockBent/Sopholeth/issues/219), [#220](https://github.com/TickTockBent/Sopholeth/issues/220), [#221](https://github.com/TickTockBent/Sopholeth/issues/221), and the stream [#218](https://github.com/TickTockBent/Sopholeth/issues/218) | No single client can exhaust a root's memory, connections, or CPU, or disable the stream for other viewers. |
| 5 | Remaining exposed-root gates in [#80](https://github.com/TickTockBent/Sopholeth/issues/80) | Complete unique message IDs, ingress validation, deduplication, capacity handling, topology recovery, race-free peer state, and shutdown, including applicable WebSocket safeguards. |

### Authenticated peer identity and admission

#211 is critical; #213 is high severity. Both block launch independently of
the initial root count. Define how a node proves its identity, which verified
identities may join and vote, and how credentials, addresses, and enclave
membership change. Identity authentication alone does not establish quorum
eligibility or prevent one participant from presenting many identities.
Carry that policy into #164's distinct eligible voters and stable per-write
quorum target, rather than deriving trust from arbitrary peer-table entries.

The implementation and its regression tests must establish that:

- Bootstrap, SYNC, and PONG cannot overwrite another node's address or enclave
  from an unverified claim. Matching `From` and `NodeInfo.ID`, or an endpoint
  echoing a claimed ID, is not proof. Treat third-party referrals as discovery
  candidates until verified. Bound verification and peer-endpoint work, with
  a destination policy that prevents injected addresses from directing roots
  at arbitrary third parties or internal services. The client rate limiter's
  peer exemption is not a substitute for dedicated peer-plane limits.
- Reject a nonempty `To` that names another node. Accept liveness only from
  the expected authenticated identity, correlated to a fresh outstanding
  probe; HTTP 200 or an unrelated PONG is insufficient.
- Persist root identities and credentials across restarts, and validate their
  explicitly configured, reachable advertised endpoints. An address collision
  requires verified ownership and a defined replacement procedure before
  retiring the stale identity. A new claimant must not be able to evict an
  existing peer merely by naming its address.
- Cover forged PONGs, bootstrap ID collisions, SYNC injection, request floods,
  stale or mismatched probes, wrong destinations, repeated restarts at the
  same address, and two IDs claiming one address. Assert that neither false
  membership/quorum nor permanent duplicate send paths survive.

A shared `NODE_CLUSTER_SECRET` is an interim option only for a controlled lab
with restricted peer ingress and trusted transport. It does not establish
individual identity, resolve #212/#213, or satisfy the public-mesh gate. A
closed lab must not silently redefine the agreed public network's admission
scope. Keep omega authority keys off node hosts; provision node credentials
separately.

### Bounded replication and truthful write outcomes

#212 is a high-severity launch blocker even among trusted peers. Coordinate
its send path with #167's receive/forward path without treating either issue
as a duplicate. Specify total concurrency, per-peer in-flight limits, bounded
queues and overload behavior, fair scheduling, per-send deadlines, and
cancellation. An unbounded goroutine per message is not an acceptable fix.

The write budget must cover dispatch and quorum waiting together. Observe
eligible quorum completion without first waiting for every peer send, and
preserve an honest confirmed/pending outcome after local acceptance (#170).
Define a separate bounded lifetime for replication still owed when the client
request finishes, including shutdown behavior; response cancellation must
not discard healthy delivery. Never retry the client's PUT implicitly.

Test one and two peers that accept connections but never answer, a slow peer,
shuffled peer order, queue saturation, cancellation, and shutdown. A healthy
quorum must complete within the declared request budget regardless of peer
order; when quorum is unavailable, report the correct pending outcome.
Verify healthy-peer delivery and bounded work under sustained load, including
inbound forwarding above the fanout threshold. Record these results in #180.

### Bounded public ingress

The client API is open to anyone, so no single client may be able to exhaust
a root. These gates hold regardless of peer identity work and can proceed in
parallel with it.

- Capacity counts key bytes and a fixed per-entry overhead, not payload bytes
  alone. Enforce a maximum key length with the launch value cap (planned at
  128 KB) on client ingress and on gossip and WebSocket ingress (#177). Size
  the gossip body limit from the value cap instead of the global request
  limit. Public roots run with an explicit storage cap (#217).
- The node server sets header-read, body-read, response-write, and idle
  deadlines, limits header size, and bounds connections per client and in
  total. Preserve `/v1/stream`'s existing per-write deadlines so slow readers
  are bounded without imposing a fixed lifetime on healthy streams (#219).
- Listing costs about the page size, with a default and maximum `limit`.
  Request paths do not hold the store lock for O(n) work. Make background
  expiry sweeping incremental, with bounded work per lock hold, so cleanup
  cannot stall reads and writes as the keyspace grows (#220).
- Rate limiting aggregates IPv6 clients by prefix, keeps its bucket table
  bounded, adds a global ceiling, and does not serialize every request on
  one lock (#221).
- One oversized key or event cannot drop other stream subscribers. The
  snapshot degrades as the store grows instead of failing, and one client
  cannot hold every subscription slot (#218). soph.stream depends on this.

Test key-only and maximum-length entries against a small cap, slow headers,
bodies, and response readers, idle connection floods, listing at 10^5 to 10^6
keys alongside writes, address rotation within one IPv6 /64, a long key during
active streams, and a store past the snapshot limits. Measure read and write
latency while background expiry sweeping runs; verify healthy streams remain
usable beyond the ordinary response deadline.

Decide which operator data public roots expose. `/v1/topology` lists mesh
members' addresses, and `/v1/status` and `/v1/metrics` expose runtime state
([#222](https://github.com/TickTockBent/Sopholeth/issues/222)). Record the
decision in the exposure policy.

### Preserve the data contract and public client

Settle the remaining replay/freshness policy before encoding assumptions in
tests. Preserve the [current contract](core-principles.md): TTL begins on
local acceptance, a new PUT starts a new lifetime, keys are listable, and
healing connectivity does not backfill missed payloads. Do not introduce
global ordering or a global expiration clock as an incidental fix.

Complete verified public-profile and long-running viewer renewal
([#198](https://github.com/TickTockBent/Sopholeth/issues/198)). Metadata work
must preserve a known write outcome, never retry a PUT implicitly, and never
switch to a different saved network.

### Windows public-client gate

Windows is a required public-client platform. Complete its trust-state backend
and native integration tests before switching public `soph join`, saved-profile
renewal, or `soph serve` to the TUF client. Track this with the public-profile
work in [#198](https://github.com/TickTockBent/Sopholeth/issues/198) and the
shared trust lifecycle in [#195](https://github.com/TickTockBent/Sopholeth/issues/195).
Linux operator/root-node development can proceed while this work is prepared;
Windows root hosting is outside the initial three-root rehearsal.

The current package explicitly rejects Windows state access. Adding a lock
implementation alone is insufficient: initialization, ownership/permissions,
ancestor-path checks, and durable replacement currently assume Unix behavior.
Keep verification, state format, rollback protection, and expiration shared,
and provide platform-specific storage operations with these required outcomes:

- Native interprocess locking with cancelable waits and recovery after process
  termination, preserving serialization between separate `soph` processes.
- Windows account/ACL validation for state and scratch directories, including
  inherited permissions and protection against replacing ancestor paths.
  Cover Windows path aliases, junctions/reparse points, and normal user-profile
  locations; do not translate Unix UID or mode-bit checks mechanically.
- A documented replacement/flush protocol that preserves committed ordering
  and verified partial progress across interruption and restart. Keep explicit
  errors for corrupt state and unsupported filesystems; never fall back to
  unlocked or memory-only public trust.
- Native Windows CI exercising first public join, cached restart, concurrent
  clients, canceled lock waits, process termination, interrupted writes,
  rotation, rollback rejection, and expiration during an outage. Use disposable
  authorities and local HTTPS. Cross-compilation or WSL-only tests do not meet
  this gate; retain the Linux regression suite as well.

**Exit:** a normal Windows user can join the public network, persist and renew
its profile, restart safely, and use `soph serve` with the same trust guarantees
as the Linux client. Record supported Windows versions/filesystems and any
remaining limitations before public release. Windows support for offline
operator key custody can be scoped separately from this public-client gate.

Implementation references: Microsoft's
[file locking](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-lockfileex),
[file access-control model](https://learn.microsoft.com/en-us/windows/win32/fileio/file-security-and-access-rights),
and [buffer flushing](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-flushfilebuffers).

### Release integrity

Releases compile in the omega trust anchor, so the build pipeline is part of
the trust chain. Before building any release candidate
([#223](https://github.com/TickTockBent/Sopholeth/issues/223)):

- Protect `main` (pull requests, a required CI gate, no force-push) and restrict
  `v*` tag creation. The gate must report on every PR, including docs/site-only
  changes. Keep Go tests conditional on relevant changes inside the workflow,
  rather than path-filtering the entire required workflow. The gate must fail
  if tests that should run fail, are canceled, or never report a result.
- Publish only from a tagged commit reachable from `main`, after the release
  pipeline runs and passes tests against that exact commit, even when the PR
  gate skipped tests for a docs/site-only change. Main pushes do not publish
  `:latest`.
- Keep the expected release fingerprint in a protected environment, so
  changing a repository variable cannot redirect the gate.

Artifact verification is conditional on the supported install path
([#224](https://github.com/TickTockBent/Sopholeth/issues/224)): SHA-pinned
actions, digest-pinned base images, and signed images or checksums with
published verification steps.

### Remaining launch gates

Severity and launch scope are separate. A deferred MCP defect can remain high
severity without blocking fixed-port roots. Mixed audit roll-ups must identify
their unresolved launch requirements separately from cosmetic residuals.

**Exit:** the release candidate supports the intended root/client path and
has a documented exposure policy. Relevant regressions pass. Deferred
interfaces are explicitly outside the support matrix; defects in interfaces
that remain exposed are resolved or mitigated by tested deployment controls.

## 3. Write and rehearse the three-root runbook

Replace the interim [omega operations reference](omega-operations.md) with
an executable production procedure once the suite and endpoint model exist.
Do not invent command flags or deploy the old signing format to fill gaps.

Prepare the planned metadata home at `https://sopholeth.io/omega/` with client
base-path support and publication independent of docs deployments. Configure
short/no caching for fixed-name `timestamp.json`, immutable caching for
numbered metadata, and uncached 404s for missing future versions. Verify actual
HTTP status and cache headers alongside the signed publication. The
[hosting design](omega-trust-design.md#repository-and-bootstrap-manifest)
records the implemented URL/routing rules and remaining deployment integration.
Complete the domain preflight before allocating the public authority: hostname
and base path become bound state, and no in-place URL migration is implemented.

The runbook must record the following, in execution order:

1. Inventory all three hosts: stable node identity, public hostname/address,
   failure domain, fixed listening ports, TLS termination, DNS control,
   service/container runtime, and resource budgets. Record the expected
   release and operator access separately from credentials. Specify persistent
   per-node credential storage and recovery, advertised-endpoint validation,
   and the authenticated procedure for replacing an identity at an address.
2. Specify the signed endpoint representation and root self-recognition,
   `default` enclave, replication and quorum policy, cache/state directories,
   restart policy, and permitted ingress. Record the forwarding-header policy
   if a proxy is used; a supplied header is not a trusted identity by itself.
3. Build the intended release with the disposable public trust bundle. Start
   the publication and node services in a documented order that handles cold
   bootstrap and roots becoming reachable at different times.
4. Publish the three-root manifest through `soph omega`, verify it from an
   external client, and establish root membership and peer connectivity on
   every node. Verify authenticated peer identities and distinct eligible
   quorum voters; a health response alone is insufficient.
5. Enable scheduled renewal, expiration alerts, process/resource monitoring,
   and replication/discovery health checks. Identify who owns each alert and
   which credentials the renewal service needs.
6. Exercise rotation, publisher/signer outage, stale clients, node restart,
   upgrade/rollback, and key recovery. Preserve the transition metadata needed
   by returning clients. Document emergency actions separately from routine
   renewal and planned rotation.

Use the available remote endpoints with disposable keys to rehearse this
exact procedure. The existing lab dnsmasq scripts are not a substitute.

**Exit:** an operator can reproduce the three-root setup from the runbook;
addresses, commands, configuration, release, expected observations, and
recovery steps are concrete and recorded. Private keys are excluded from the
repository and validation artifacts. Node hosts receive only their own node
credentials and public omega trust material.

## 4. Validate, then activate

[Issue #180](https://github.com/TickTockBent/Sopholeth/issues/180) owns the
deployment evidence, with current-contract guards in
[#183](https://github.com/TickTockBent/Sopholeth/issues/183). Record pass/fail
thresholds before running the workload. Required evidence includes:

- A fresh client joins through verified discovery and uses put/get/list;
  writes propagate to all three healthy roots in the tested workload, with
  truthful confirmed/pending outcomes under the chosen quorum policy.
- One-root failure and return, cold starts, partitions/healing, invalid and
  expired discovery, valid cache fallback, and an unavailable publisher.
- Peer takeover/injection and enclave-spoofing attempts cannot alter trusted
  membership or produce false confirmed writes (#211/#164). Admission and
  verification floods remain bounded under the actual ingress policy.
- Repeated root restarts retain their identities. A rehearsed identity
  replacement, conflicting address claims, and wrong advertised endpoints
  neither leave ghosts nor evict a valid peer based on a claim (#213).
- Unanswering/slow peers and shuffled send order cannot delay a healthy
  quorum until every send finishes (#212/#167). Verify healthy-root delivery,
  honest pending outcomes without quorum, and bounded queues/concurrency
  both below and above the fanout threshold. Use a disposable larger topology
  for the latter; three roots alone do not exercise epidemic forwarding.
- A single client cannot exhaust a root through key-only or long-key writes,
  slow headers/bodies or response readers, idle connections, repeated listing,
  or IPv6 address rotation, and cannot disable the stream for other viewers.
  Read and write latency stays within the declared budget while background
  expiry sweeping runs at the target key counts (#217–#221).
- Renewal and planned rotation while clients run, including a client that
  returns after an extended absence. Observe actual local TTLs and avoid
  claiming that connectivity recovery restores missed data.
- `soph serve` and the hosted viewer over the actual HTTPS path: stream
  flushing, idle connections, bounded slow viewers, reconnect to a fresh
  snapshot, and preserved network selection.
- Build/race checks, relevant parser and adversarial tests, capacity pressure,
  ramp-to-failure, and a sustained soak targeting at least 24 hours. Record
  the commit, configuration, workloads, fault timings, driver latency and
  throughput, write outcomes, process memory, and recovery results.

The larger multi-substrate/transient matrix in
[#147](https://github.com/TickTockBent/Sopholeth/issues/147) is required before
supporting that later participation mode. MCP tests are deferred. Tests of
exposed root endpoints remain required regardless of the supported clients.

After rehearsal and the applicable launch gates pass, establish production
custody/recovery, create the real authority through the suite, verify the
release fingerprint, and deploy the three roots using the rehearsed runbook.
Verify publication and joining externally, enable renewal/alerts, and publish
supported behavior, known limits, operator contacts, and validation results.
Complete the artifact/site and distribution-term work already recorded in
the [roadmap](roadmap.md). Activation is a distinct operator step; this plan
does not generate keys or deploy services.

## Triage record

On 2026-09-22, all 61 open issues were reviewed against main `4b89751`.
Eight were closed with evidence: #127 and #146 (no separate gossip listener),
#135 (porting umbrella superseded by specific behavior/validation tickets),
#141 (remaining liveness work consolidated into #140), #145 (quorum locking
consolidated into #164), #161 (HTTP-port help corrected), #174 (obsolete
key-secrecy premise), and #186 (site 404 routing implemented).

The remaining 53 were labeled: 2 critical, 27 high, 18 medium, and 6 low.
Launch labels distinguish required work, conditional exposed surfaces, and
deferred work. These counts are a dated snapshot, not a release checklist to
clear indiscriminately. The [live tracker and closure evidence](https://github.com/TickTockBent/Sopholeth/issues/80)
are authoritative as implementation proceeds.

The subsequent peer-plane audit added #211 (critical), #212 (high), and #213
(high), all launch blockers. Source review against main `821c25e` confirmed
that their affected paths were unchanged from the audit's `c4dadbe` baseline.
This plan incorporates their dependencies and required evidence; the review
did not rerun the audit's HTTP reproductions or resolve the findings.

A public-ingress and release audit against `c4dadbe` added #217–#221 and #223
as launch blockers, #222 and #224 as conditional, and deferred #214. It
reproduced #217, #218, and #220 with probe tests; #219, #221, and #223 come
from source and repository-settings review. Omega PR #216 did not touch the
affected paths.
