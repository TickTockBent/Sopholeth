# First public test network: three working roots

Status: scope reset with the project owner, 2026-09-24. The immediate objective
is to get three public roots running so we can test Sopholeth on real hosts.
This replaces the earlier requirement to finish the entire public-alpha audit
queue before starting the network. [Issue #80](https://github.com/TickTockBent/Sopholeth/issues/80)
tracks this milestone; individual audit issues retain their findings.

## Contract and scope

The [core principles](core-principles.md) constrain every implementation choice:

- Any compatible node can bootstrap, join, and gossip without membership
  approval. Three roots are the initial entry points, not a membership limit.
- Writes have no authenticated author. There are no client accounts, write
  credentials, key owners, or required payload signatures.
- Omega authenticates the bootstrap directory and its updates. Being listed
  as a root grants a discovery role, not authority over values or other nodes.
  Removing a root from that directory does not ban it from ordinary gossip.
- Peer IDs are protocol handles. Acknowledgments report observed replication;
  they do not prove honest operators, independent physical copies, or consensus.
  Duplicate-response checks do not establish Sybil resistance.
- Values stay opaque, TTL starts at local acceptance, and restart loses local
  payloads. Connectivity recovery does not backfill missed writes.

There is no controlled-admission service, root-issued peer credential, trusted
voter class, or write-attribution system on this milestone's path. Future
transport protections must justify their cost while preserving open joining and
the write contract. The omega root-signing threshold is unrelated to data-write
acknowledgments.

Start with Linux standalone nodes, the `default` enclave, fixed reachable
endpoints, and the `soph` HTTP client. Public joining means both a fresh CLI
client and an additional standalone node can use the network. Native Windows
public discovery remains planned; it does not hold up this Linux test network.
MCP, dashboard deployment, full WebSocket/transient participation, and viewer
polish are outside the initial milestone.

## What is already ready

The node already provides anonymous put/get/list, local TTL expiration, and
gossip replication. The CLI already joins nodes and performs those operations;
the initial `soph serve` and soph.stream viewer also exist. Deploy and exercise
these capabilities. They are not new implementation milestones, and their
known bugs do not mean the whole subsystem needs rebuilding.

The `soph omega` suite implements encrypted initialization, backup checking,
publication, separate unattended renewal custody, status verification, and
online/membership/root-key rotation. Schema 1 remains disposable-only; schema 2
stores encrypted operator keys. The connected-host custody approach and reset
boundary are documented in [omega operations](omega-operations.md) and the
[custody proposal](omega-production-custody.md). No new custody design is needed.
Here, membership signing approves the bootstrap-root manifest, not ordinary
node admission.

The metadata-only Vercel project serves through `https://sopholeth.io/omega/`.
The [hosted rehearsal](omega-hosted-rehearsal.md) exercised publication, renewal,
rotation, and isolation from docs deployments. Kraid's service invocation
succeeded after the public-directory ownership repair, now fixed in #240.
The operator disabled and removed the disposable timer. A complete daily remote
renewal cycle has not yet been observed; observe it on the running test network.

Nodes and `soph join` now use the HTTPS trust client, embedded public bundle,
and initial TUF-root fingerprint gate. Local fixtures exercise saved-profile
renewal, HTTPS bootstrap/gossip, and runtime expiry. The embedded bundle remains
unconfigured: no intended test-network authority has been created and the three
roots are not deployed. Real-network validation belongs to bring-up.

## Delivery order

### 1. Remove the known startup/recovery loop

The [#150](https://github.com/TickTockBent/Sopholeth/issues/150) fix separates
explicit `SYNC_REQUEST` messages from one-way `SYNC` announcements. Two
under-peered nodes now finish their exchange, and later requests still discover
new peers. The regression exercises repeated recovery ticks and HTTP discovery.
This prerequisite is implemented. The discovery integration in step 2 is also
implemented; host configuration and bring-up are next. Deploy matching builds because older nodes lack the request type.

Do not make the entire replication/peer audit a dependency of first deployment.
Existing replication is sufficient to start with a small healthy-network test;
use the running network to work through the fault cases below. Any further
prerequisite needs a concrete explanation of what prevents bring-up.

### 2. Connect real discovery to nodes and the CLI

Implemented for Linux using the existing
[HTTPS trust client](../internal/trust/bootstrap/README.md) and omega output.
The [discovery contract](discovery.md) records the behavior below. Bundle
adoption is part of actual network setup; these are local integration results,
not a claim that public joining has been tested on deployed hosts.

- Load the public TUF bundle in the node and `soph`, and compare its fingerprint
  with the expected authority when building the test-network binaries. Keep
  ordinary unconfigured builds fail-closed. This completes the remaining
  bundle integration in #195 and replaces the legacy release-anchor check.
- Use signed HTTPS root origins without dropping their schemes, disabling
  certificate checks, or following redirects to an unapproved origin (#194).
  Checking an official bootstrap endpoint does not certify its peer referrals.
- Pin listed root IDs to their verified origin/enclave throughout peer-table
  updates. Only a later verified manifest can move those routes. Unsigned
  bootstrap/SYNC cannot move an established ordinary peer either, and PONG
  cannot change its enclave. This addresses the route-replacement part of
  #211; peer admission stays open and the broader liveness/resource cases
  remain follow-up work.
- Preserve rollback protection, cached discovery within its validity, runtime
  expiry, and retry backoff (#160, #193, #172). Expired metadata must not keep
  authorizing an official root role or bootstrap seed. Ordinary peer membership
  and payload TTLs do not acquire an omega authorization lease.
- Refresh public CLI profiles without manual rejoining (#198). Keep each
  profile bound to its selected network; discovery work must not repeat a PUT
  whose local acceptance may already be known. Exercise the same profile path
  from `soph serve` when using the viewer.

Local tests exercise a disposable authority, a fresh client, a saved profile
following updated roots, an ordinary unlisted node joining and replicating over
HTTPS, and expiry during blocked refresh. The viewer's stream selection is
withdrawn and rebuilt on refresh. Existing trust/rotation coverage is reused;
real `soph join`, external transport, and three-root operation still need the
running network in steps 3–4.

An explicit bundle override for deliberate resets (#231) is useful follow-up.
Initially, distributing a new binary with a deliberately adopted new bundle is
an acceptable reset procedure. Never silently replace a saved network's trust.

### 3. Put the three roots on the available hosts

The [bring-up runbook](public-network-bringup.md) uses Kraid (DigitalOcean),
Ridley (Hetzner), and Motherbrain (the operator workstation), with the approved
`kraid.sopholeth.io`, `ridley.sopholeth.io`, and `motherbrain.sopholeth.io`
origins. Each host has its own public tunnel and root. Cloudflare is a shared
ingress dependency; Motherbrain also depends on workstation uptime. Normal
login users run the commands, with explicit `sudo` for host administration.
The runbook/configuration do not activate the intended authority or roots.

The runbook must contain:

1. Each root's host, public hostname, reachable advertised address, fixed ports,
   TLS termination, and service paths. Configure stable, distinct `NODE_ID`
   values as a restart aid, not credentials. Validate advertised endpoints from
   another host; record the transport used for gossip as well as bootstrap.
2. The common `default` enclave, replication factor 3, observed-confirmation
   policy, storage setting, and process memory/CPU/connection budgets. Use small
   test payloads and a bounded driver workload. A process memory limit contains
   resource consumption; it does not repair storage accounting or prevent an
   attacker from interrupting this experimental service.
3. The exposed HTTP paths and proxy/firewall rules. Bootstrap and supported
   gossip remain open to joining nodes; no peer allowlist or shared cluster
   secret is required. Keep backend listeners behind the configured ingress so
   proxy limits cannot be bypassed. Exclude unsupported WebSocket, dashboard,
   and optional streaming surfaces, and verify the exclusions externally.
   Add the viewer/stream when its path is ready to test.
4. The reviewed, tested source commit and exact binaries deployed. Build from
   that commit and record binary checksums and the bundle fingerprint. Do not
   use a floating image tag or treat the current auto-publishing workflow as
   release approval. General release-pipeline work remains #223/#224.
5. Authority setup through the existing omega commands, a checked encrypted
   backup, and a saved public bundle/fingerprint. Recheck the
   [domain preflight](../sites/README.md#omega-metadata-hosting) before creating
   this network's authority at `/omega/`; do not reuse the disposable rehearsal
   identity. Node services get public omega material, never its signing keys.
6. Bootstrap/publication order, service install/start/status commands, and a
   stop/restart/update procedure. Kraid runs the separate renewal account: daily
   refresh, seven-day validity, hourly checks/retries. Re-enable the timer only
   for the intended network, with status/expiry checks and an operator contact.

Minimal ingress configuration needs body/header size limits, timeouts, and
connection limits appropriate to the test workload. Verify these on the actual
path. They reduce exposure while testing; they do not close #217–#221 or claim
robustness against hostile traffic. If a required path cannot be bounded by the
deployment, fix that specific problem before exposing it.

Use a disposable local integration run to settle commands and cold-start order.
Then activate the intended public test network through the runbook and test it
in place. There is no separate full production-scale rehearsal prerequisite.
Authority loss or compromise can require an announced reset with a new bundle;
this network has no continuity or durable-data guarantee.

### 4. Verify the healthy network, then test its failures

Record the commit, endpoints, relevant configuration, and results in #180,
using #183's current data contract. First verify existing functionality:

- A fresh `soph` client discovers the network and performs put/get/list without
  credentials. A write to each root can be read from the other two healthy
  roots before its local TTL expires; each node eventually expires its copy.
- A fourth, independently started compatible node bootstraps and participates
  without adding it to the signed root list or obtaining permission. Verify
  gossip in both directions, then remove it. Three roots do not cap membership.
- Verify signed discovery externally, cached restart within validity, and
  rejection of invalid/expired discovery in the disposable fixture. Check the
  public renewal service and retain its first actual daily refresh result.
- Check root resource use and logs under the small workload. Record failures
  and recoveries without logging value contents. Exercise `soph serve` and
  soph.stream once streaming is included; viewer polish does not gate the
  put/get/list network.

**Milestone complete:** three roots are reachable, verified public bootstrap
works, clients and an additional node can join permissionlessly, anonymous
put/get/list and healthy replication work, and the operator can manage the
services and renewal. Record the daily renewal observation when it occurs;
it is not a mandatory 24-hour wait before first bring-up.

Then stop/restart a root, introduce a slow peer, and exercise missing or repeated
acknowledgments. Expect the existing issues to explain some failures; record
them and fix the affected path without withholding the useful running network.
Do not equate every `201` with three copies: record the configured threshold and
current peer view. Rejoining does not imply payload backfill.

The first repair queue after bring-up is:

| Observed problem | Scope of the fix |
| --- | --- |
| Peer corruption/ghosts/races: #211, #213, #169 | Check message consistency and response correlation; validate referrals before changing existing records; remove stale duplicate paths. Record residual spoofing risk. No admission authority or trusted identity class is required. |
| ACK accounting/write outcomes: #164, #166, #170 | Unique write IDs, one response per peer per pending write, the recorded replication context/enclave, a defined threshold, and honest local-acceptance reporting. This does not prove independent or honest replicas. |
| Slow-peer delivery/forwarding: #212, #167 | Let healthy sends progress with bounded concurrency and lifetimes. Never retry the client PUT implicitly. |
| Service shutdown: #151 | Complete orderly stop/restart; verify subsequent writes after rejoining. |

Fix a problem earlier if it prevents basic joining or healthy replication on the
actual deployment. An endpoint answering a challenge is not proof of a trusted
operator, and a protocol peer ID does not identify a write's author.

Continue soak, capacity, churn, partition, and adversarial testing on this
running test network and disposable local clusters. Its purpose is to expose
remaining limitations. A discovered defect becomes a scoped fix, not evidence
that Sopholeth needs a different participation or data model.

## Follow-up work and issue scope

No audit finding is closed by this plan. Earlier `launch:blocker` labels and
public-alpha checklists describe a broader milestone; they are not a requirement
to finish every listed issue before this test network can start. The work above
in delivery steps 1–3 defines the immediate prerequisites; step 4 includes
testing and the subsequent repair queue. Preserve reproduction evidence and
update individual tickets as fixes or tested deployment mitigations land.

| Follow-up | Treatment for first bring-up |
| --- | --- |
| Resource/stream hardening: #217–#221, public diagnostics #222 | Use the limited workload and explicit ingress/process bounds above; keep optional surfaces excluded until tested. Track remaining denial-of-service and scaling limits openly. |
| Larger-mesh/capacity/replay behavior: #152, #162, #163, #165, #167, #168 | Test progressively after small-cluster bring-up; prioritize any #167 failure affecting the actual workload. No global ordering or authenticated writer is implied. |
| Mixed audit roll-ups #177/#178 | Take only findings needed by an exercised path into a current slice; do not make every residual a precondition for starting tests. |
| WebSocket participation: #140, #142, #144, #154, #173, #175, #147 | Keep the endpoint excluded for the initial HTTP/standalone-node path. Complete relevant safeguards and lifecycle tests before enabling it. |
| Release pipeline and distribution: #223/#224 | Test with recorded builds from the reviewed commit. Finish automated publication gates and artifact verification before distributing a general release. |
| Omega #195–#199 and CLI #198 | Operator functionality is largely implemented. Finish the consumer/runtime work above and record remote renewal; do not rebuild completed custody workflows. |
| Explicit reset bundles #231 | Follow up after the compiled-bundle path works; reset can initially require an explicitly adopted new build. |
| MCP, dashboard, viewer polish, attachment optimization, payload backfill | Remain outside the three-root milestone. |

### Windows public-client gate

Native Windows `soph join`, saved-profile renewal, and `soph serve` remain a
planned deliverable under #198/#195. The Linux test network may start first;
document Windows public discovery as unsupported until its backend is ready.
Do not introduce unlocked or memory-only trust as a shortcut.

The Windows work needs native locking, account/ACL and path validation, durable
state replacement, and native tests for join/restart, concurrent access,
interruption, rotation, rollback, and expiry. Keep common trust verification
shared. This is a prerequisite for claiming Windows public-client support, not
for running three Linux roots. Windows omega operator custody is separate.

## Plan history

The earlier plan accumulated the omega audit and broad public-alpha findings,
including peer admission and trusted-voter language in #225. That expansion
went beyond Sopholeth's permissionless contract. The 2026-09-24 reset removes
those architectural requirements and separates first bring-up from later
hardening. The original bugs remain recorded in their issues and Git history.
This document records intended work; it does not deploy nodes, create authority,
or claim that the remaining fixes are implemented.
