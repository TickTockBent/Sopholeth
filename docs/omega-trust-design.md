# Omega trust lifecycle: integration spike and proposed design

Status: trust client, atomic disposable authority initialization, local-directory
publication, unattended online renewal, online-key rotation, and served-release
verification implemented. Membership/root-key rotation, production custody/hosting,
and consumer integration remain pending.
The completed [spike](../test/omega-tuf/README.md) established the design for
[#195](https://github.com/TickTockBent/Sopholeth/issues/195) and the
[public-network plan](public-network-plan.md). Its scenarios now exercise the
[durable client](../internal/trust/bootstrap/README.md) in the main Go suite.
`soph omega init`, `provision-renewal`, local-directory `publish` with online
renewal, online-key `rotate`, and `status --verify` implement the current operator slices.
This does not switch node discovery or establish a production authority.
The [audit](omega-signing-audit.md) remains the record of the interim protocol.

## Decision

Use TUF for authenticated distribution of a small bootstrap manifest over
HTTPS. Proceed with `github.com/theupdateframework/go-tuf/v2` **v2.4.2** as
the integration baseline. Keep Sopholeth's manifest validation, runtime
authorization, durable state, and operator workflows outside the library.
Do not develop another custom rotation protocol or introduce another CLI.

The spike selected a reviewed stable release, rather than upstream master.
That release requires Go 1.25.0; the previous Go 1.22.2 toolchain could not
compile it. The application, Docker builders, development requirements, and
CI now use Go 1.27.1, and the root module includes go-tuf v2.4.2. The separate
spike module/workflow has been retired. Existing discovery consumers have not
yet switched to the new package.
[Release](https://github.com/theupdateframework/go-tuf/releases/tag/v2.4.2),
[release go.mod](https://github.com/theupdateframework/go-tuf/blob/v2.4.2/go.mod),
[Go releases](https://go.dev/dl/).

Version 2.4.2 includes the fix for counting one public key under multiple IDs
toward a signature threshold. Choosing an older version just to retain
Go 1.22 would need a separate security review. This spike reviews the release
notes, published upstream advisories, and integration paths; it is not an
independent cryptographic audit or a blanket assurance about dependencies.
[Threshold advisory](https://github.com/theupdateframework/go-tuf/security/advisories/GHSA-3633-5h82-39pq).

The original spike module had two direct requirements (go-tuf and its Sigstore
signer API) and ten indirect requirements. That includes crypto, protobuf,
and container-related utilities pulled in by the signing library. The actual
versions used by the integrated client and tests are recorded in
[go.mod](../go.mod) and [go.sum](../go.sum).
This is a real dependency/toolchain cost,
but preferable to maintaining the update security protocol ourselves.

## What the experiment establishes

The tests use newly generated Ed25519 keys, an HTTPS repository on loopback,
real signed metadata/target downloads, temporary client directories, and a
controlled clock. They demonstrate:

- First discovery and identical refresh after restarting an updater.
- Online renewal after removing both offline signing roles from the fixture.
- Timestamp rollback rejection after restart, including persisted partial
  progress when a new timestamp references a missing snapshot.
- Rejection of older snapshot/membership versions even inside freshly signed
  online metadata, when the client has already accepted newer versions.
- A root transition accepted with two old and two new root signatures, and
  rejected with either quorum missing or below threshold.
- A returning client traversing two transitions, including expired initial
  and intermediate roots, when the final authority and metadata are current.
- Restart from the latest accepted root, rejection of a retired online key,
  and failure to recover an old client when a required transition is missing.
- Rejection of changed target bytes and expiration of each metadata role.
- A retained updater still returning target information after the deadline
  represented by its metadata: runtime revocation is our responsibility.

The original small restart wrapper rejected a missing/corrupt established
root but lacked durable storage and locking. It is now replaced by the
[client implementation](../internal/trust/bootstrap/README.md), with durable
checkpoints, state validation, and process locks. Its regression tests also
cover interrupted writes, bounded HTTPS, concurrent access, and runtime leases.

## Authority and custody

| TUF role | Sopholeth responsibility | Proposed custody |
| --- | --- | --- |
| Root | Authorize role keys, thresholds, and successor authorities | Offline 2-of-3 Ed25519 keys with independent recovery copies |
| Targets | Approve the bootstrap manifest and its validity period | Separate protected offline membership key, 1-of-1 initially |
| Snapshot | Bind approved metadata versions into a release | Online renewal service, separate key |
| Timestamp | Advertise a short-lived current snapshot | Online renewal service, separate key |

The three root signing keys are unrelated to the three public **node** roots.
Node hosts receive public trust material only. Hosting credentials are also
separate from signing keys. Keeping all three authority keys on one ordinary
host would defeat the intended custody separation, even if a threshold were
configured. Rehearsal must verify custody and recovery before activation.

Compromise of the online service must not permit new membership approval or
replacement of the root authority. It can still deny updates or replay an
older, still-valid approved manifest to a client without newer state. Expiry
limits that exposure; signing separation does not eliminate it. Membership
key compromise permits malicious approved endpoints until corrected through
the authority; it deserves protected storage and a rehearsed revocation path.

Start rehearsal with the following policy, and adjust from measured outage
tolerance before activation:

| Metadata | Lifetime | Renewal trigger |
| --- | --- | --- |
| Root | 365 days | Planned ceremony with at least 180 days remaining |
| Targets / membership | 90 days | Review and reapprove with at least 30 days remaining |
| Snapshot | 7 days | Renew with timestamp every 6 hours |
| Timestamp | 24 hours | Renew every 6 hours; alert on a missed cycle |

These are proposed operating values, not library defaults. Online renewal
cannot extend root or membership approval forever. It must refuse to present
expired approval as healthy and warn well before either offline ceremony is
due. Clock synchronization and clock-error reporting are deployment inputs.

## Repository and bootstrap manifest

Initially ship an explicit HTTPS repository URL and the initial signed root
bundle with a public release. DNS locates that HTTPS hostname; DNS TXT does
not carry trust metadata or choose an arbitrary download URL. If alternate
locators are added later, they need an explicit policy for allowed origins,
redirects, and local/private address access.

The planned public home is `https://sopholeth.io/omega/`. Before adopting it,
extend the client's origin-only repository validation to support a base path
and confine downloads to that origin and path. Keep node endpoint validation
as HTTPS origins. Publication should remain independent of docs deployments
so routine renewal and website rollback cannot inadvertently revert metadata.

When configuring that endpoint, preserve real 404 responses for missing files
under the site's existing filesystem/404 routing. Serve fixed-name
`timestamp.json` with `no-cache` or a very short `max-age`; serve numbered root,
snapshot, and targets metadata with long-lived immutable caching. Never replace
the contents of an already-published numbered file. Missing future versions
must not acquire the immutable cache policy: a cached 404 must not delay a
later root transition. Verify the response headers at the public endpoint as
part of publication rehearsal. These routing/cache changes are planned;
the site's Vercel configuration has not been changed for omega hosting yet.

Use consistent snapshots, a fixed target name `bootstrap.json`, numbered
root/targets/snapshot files, and content-hashed target objects. Upload immutable
objects before changing `timestamp.json`. Retain every numbered root
transition for the first network, including transitions whose expiry has
passed. This small repository does not need aggressive garbage collection.
Retain other published objects through their relevant validity windows and
the documented download/recovery margin; determine that margin in rehearsal.
The [TUF specification](https://theupdateframework.github.io/specification/latest/)
defines the update protocol; the retention and publication policy here is
our operational choice.

The proposed manifest describes a schema version, network identity, enclave,
and bootstrap roots with stable node identifiers and explicit HTTPS origins.
The first public manifest has three distinct roots in `default`. Validate
identities, duplicate origins, URL form, and supported schema before signing
and after download. Do not encode secrets or replication topology in it.
Finalize the exact wire schema with the endpoint work in
[#194](https://github.com/TickTockBent/Sopholeth/issues/194).

HTTPS metadata delivery alone does not repair bootstrap transport. Nodes,
CLI, and viewer must follow signed endpoint schemes consistently, verify TLS
certificates and hostname identity, and define how configured node identity
is checked. An authenticated bootstrap response must also have a defined
policy for advertised gossip/WebSocket endpoints. HTTP downgrades or blind
trust in forwarding headers must not reintroduce the audited gap.

Use an explicitly configured HTTP client with timeouts, bounded downloads,
and restricted redirects; the library's default fetcher uses
`http.DefaultClient`. The spike's client trusts the test server's certificate
without disabling TLS verification. Public-Internet endpoint validation,
redirect policy, and node transport are not implemented by this test.

## Client state and runtime authority

Keep state separate for each network and serialize access across processes
and refresh goroutines. A new updater is needed for each refresh. On restart,
supply the latest accepted local `root.json` to `config.New`; only a genuinely
new state store starts from the release bundle. `updater.New` persists its
supplied root immediately. Passing the bundled root on every restart would
overwrite the newer root. These are integration requirements observed in the
pinned [updater source](https://github.com/theupdateframework/go-tuf/blob/v2.4.2/metadata/updater/updater.go).

Preserve successfully authenticated metadata even when a later step fails.
A client can have accepted timestamp N while snapshot N is unavailable; it
must not discard N and accept N-1 on restart. The production state layer must
record an initialized marker, accepted versions, and complete metadata writes,
and treat missing/corrupt established state as an actionable error. Do not
silently reset it from an older bundle. Recovery/reset must be explicit and
must explain that discarding state discards rollback history.

The library renames temporary metadata files but does not fsync the file or
parent directory; target writes are ordinary writes. Provide crash-durable
storage around the complete update sequence, preserving partial verified
progress, before claiming restart protection through power loss. Evaluate
that storage boundary during implementation rather than assuming the cache
is a transaction. Protect directory ownership/permissions and reject unsafe
existing locations; library directory creation does not tighten existing
permissions. Local state modification by an attacker is outside the network
update protection demonstrated here.

Only activate a manifest after its complete signature/version chain, target
hash, and Sopholeth schema/endpoint checks pass. Retain the accepted manifest
separately from an in-progress refresh, with its authenticated expiration
deadline: the earliest expiry among its root, targets, snapshot, and
timestamp metadata. A successful unchanged renewal can extend that deadline.
A failed refresh cannot extend it. Any newly authenticated revocation or
authority change must also invalidate incompatible retained authorization;
do not blindly retain a lease under keys the client has already retired.

Check that deadline whenever returning trusted roots or recovery seeds, and
schedule revocation of root bootstrap status even while refreshes are failing.
Treat equality with the deadline as expired. Existing peer sessions and
payload operations require their own explicitly documented behavior; losing
bootstrap authority must not accidentally rewrite payload TTL semantics.
Startup while offline must reverify a complete accepted state and its lease;
do not turn on the library's `UnsafeLocalMode` as an unexplained fallback.

## One operator workflow

`soph omega init`, `provision-renewal`, local-directory `publish` (including
`--renew`), online-key `rotate`, and `status --verify` are
implemented for disposable authorities on Linux; see the
[operator guide](omega-operations.md). Initialization prepares
and verifies the entire authority privately, then commits it with a single
atomic, non-replacing directory rename. Repeated invocations recover the same
transaction or verify the same committed authority. Production custody remains
pending. Publication uses a separate immutable release journal, explicit
consecutive release versions, independent approval/renewal versions,
timestamp-last writes, and exact served-byte
verification through a fresh durable client. See the operator guide for its
retry and expiration contract. The complete command surface below includes
subsequent planned work:

- `soph omega init`: create an exclusive staged authority directory, protect
  key files, verify generated key consistency, produce public bundle and
  fingerprints, and record the recoverable initialization state. An existing
  or interrupted initialization cannot silently replace keys.
- `soph omega provision-renewal`: bind a separate operational home and transfer
  the journal recoverably, enabling it only after the history and two online
  keys are durable. Keep root and membership keys in the offline home.
- `soph omega publish`: validate membership approval, prepare immutable
  objects, sign permitted roles, publish timestamp last, then independently
  fetch and verify the served result and expected versions. Nonzero exit on
  any publication or verification failure. An unattended renewal mode uses
  only approved membership metadata and online keys; it never requests the
  ultimate authority key just to extend routine freshness.
- `soph omega status`: display network, accepted versions, role fingerprints,
  bootstrap roots, all expiration deadlines, last verified publication, and
  required action. Provide machine-readable output and failure exit codes.
- `soph omega rotate`: prepare replacement snapshot/timestamp keys and a
  successor root signed by the unchanged 2-of-3 root quorum. Require its reviewed
  digest for application, retain numbered roots, and recover publication through
  the online journal. Membership/root-key rotation and automated deployment
  adoption tracking remain later slices; do not overwrite keys in place.

Signing and publication need a single-writer lock, durable monotonic counters,
immutable prepared releases, and an idempotent retry journal. If timestamp N
escapes before its objects, complete N or publish a higher numbered repair;
rolling the timestamp back is not a valid recovery procedure.

For an authority rotation, root N+1 must meet both root N's threshold and its
own threshold. For an online or membership role rotation, the root authority
signs the changed role assignment. Publish the numbered root chain and
successor role metadata in the planned order; retain the chain permanently.
There is no need for the old online key to remain valid in the new root just
to accommodate returning clients. Clients that have not seen the transition
can remain on old signed state until its lease expires, so emergency rotation
is not instantaneous revocation across disconnected clients.

Retire replaced operational keys after publication verification and observed
adoption by all three roots and representative clients, preserving the
transition and the documented offline recovery material. Loss of an online
or membership key is recoverable through the root quorum. Loss of one of
three root keys is recoverable with the other two. Losing the quorum requires
its protected backups; losing every recovery path requires a newly distributed
trust anchor. Compromise of the root quorum cannot be repaired by trusting an
unsigned replacement from the same compromised delivery channel.

## Implementation sequence and remaining gates

1. Preserve the placeholder rejection/release gate from
   [#192](https://github.com/TickTockBent/Sopholeth/issues/192) while upgrading
   the application toolchain and integrating the public root bundle representation.
2. Build the durable trust client and manifest/endpoint validation. Carry the
   spike's cases into production regression tests and add crash interruption,
   corruption, concurrent access, bounded fetches, and runtime revocation.
3. Implement `soph omega` initialization, publishing/renewal, status, and
   rotation with disposable custody material. Retire the standalone `omega`
   command and update all builds and consumers together.
4. Integrate runtime expiration, retry timing, and public profile/viewer
   renewal; rehearse real publication, key recovery, and bootstrap transport.
5. Continue the root correctness work and remote three-root rehearsal in the
   public-network plan. Production custody, bundle fingerprints, addresses,
   DNS, and activation come from that runbook, not this spike.

The toolchain upgrade, explicit bundle/manifest types, and durable client from
steps 1–2 are implemented. Step 3 now includes atomic disposable `init`,
journaled local-directory `publish`, restricted online custody provisioning,
unattended renewal, and local/HTTPS-verified `status`. Rotation, production
custody/hosting, and standalone-tool
retirement remain pending. The compiled public bundle/release-gate migration,
node/CLI/dashboard adoption, runtime callbacks, and transport checks remain
pending. The [client reference](../internal/trust/bootstrap/README.md)
records its supported storage platforms and exact validation boundaries.

Native Windows public-client support is required before the TUF consumer
cutover. The [Windows gate](public-network-plan.md#windows-public-client-gate)
covers the complete storage backend, including ACL/account policy, ancestor
paths, locking, durable replacement, and native Windows regression tests.
The current unsupported-platform error is an interim implementation limit;
Linux root-node deployment does not define the public CLI's platform scope.

The spike does not close #195 or the related audit issues. It establishes a
feasible library and trust lifecycle and identifies the integration work
needed before the first public network can use them.
