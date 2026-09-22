# First public network: omega, then three roots

Status: agreed delivery path, 2026-09-22. Placeholder rejection and the public
release fingerprint gate, durable trust client, and atomic disposable
`soph omega init`/local `status` are implemented. Publishing, renewal, rotation,
consumer integration, and deployment remain pending. The [omega audit](omega-signing-audit.md) records the initial defects;
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
views. Atomic disposable authority initialization and local inspection now
run through `soph omega`. The remaining operator commands, compiled bundle/release
gate, and discovery consumer/transport integration remain pending.

The proposed command surface is:

| Command | Required outcome |
| --- | --- |
| `soph omega init` | Safely create authority and recovery material, identify its fingerprint, and produce the public trust bundle needed by a release. Existing or interrupted initialization must not silently replace keys. |
| `soph omega publish` | Validate an approved root manifest, sign through the chosen key store, publish it, and verify the metadata clients actually receive. Return failure when publication or verification fails. |
| `soph omega status` | Report accepted versions, authority fingerprints, roots, expiration, renewal/publication health, and actionable failures, with script-friendly output. |
| `soph omega rotate` | Prepare and carry out an authenticated successor-key transition with defined overlap, retained transition metadata, adoption checks, and retirement criteria. |

`init` and local `status` are implemented for disposable Linux authorities;
see [atomic initialization and recovery](omega-operations.md#all-or-nothing-commit-and-retry).
Publishing, renewal, rotation, production custody, and remote status remain
pending. Fold the existing standalone
`omega` tool into `soph` and retire that binary, updating builds, releases, and
documentation. Node hosts receive public trust material, not the ultimate
private authority key. Routine freshness renewal must run unattended without
requiring repeated use of that ultimate key.

Implement the related findings in the selected design:

- Persist authenticated ordering and reject rollback
  ([#160](https://github.com/TickTockBent/Sopholeth/issues/160)). Identical
  metadata refreshes remain idempotent.
- Expire runtime root authority and recovery seeds independently of refresh
  success ([#193](https://github.com/TickTockBent/Sopholeth/issues/193)); fix
  retry timing ([#172](https://github.com/TickTockBent/Sopholeth/issues/172)).
- Authenticate bootstrap connections and carry endpoint transport information
  consistently ([#194](https://github.com/TickTockBent/Sopholeth/issues/194)).
- Make initialization exclusive and recoverable
  ([#196](https://github.com/TickTockBent/Sopholeth/issues/196)); validate keys,
  inputs, and exact signed output
  ([#197](https://github.com/TickTockBent/Sopholeth/issues/197)).
- Automate renewal, verify publication, and expose expiration/failure signals
  ([#199](https://github.com/TickTockBent/Sopholeth/issues/199)).

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
Then address the root-facing correctness, resource, and lifecycle gates
identified in #80: distinct eligible confirmations, unique message IDs,
validated ingress, bounded forwarding and deduplication, capacity handling,
topology recovery, race-free peer state, and completed shutdown.

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
records this future work; no Vercel routing/cache change is part of the plan update.

The runbook must record the following, in execution order:

1. Inventory all three hosts: stable node identity, public hostname/address,
   failure domain, fixed listening ports, TLS termination, DNS control,
   service/container runtime, and resource budgets. Record the expected
   release and operator access separately from credentials.
2. Specify the signed endpoint representation and root self-recognition,
   `default` enclave, replication and quorum policy, cache/state directories,
   restart policy, and permitted ingress. Record the forwarding-header policy
   if a proxy is used; a supplied header is not a trusted identity by itself.
3. Build the intended release with the disposable public trust bundle. Start
   the publication and node services in a documented order that handles cold
   bootstrap and roots becoming reachable at different times.
4. Publish the three-root manifest through `soph omega`, verify it from an
   external client, and establish root membership and peer connectivity on
   every node. A health response alone is insufficient.
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
recovery steps are concrete and recorded. Private key material is excluded
from the repository, node deployment, and validation artifacts.

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
