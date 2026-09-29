# Omega trust design

The node and `soph` use HTTPS metadata verified by go-tuf v2. The encrypted
`soph omega` lifecycle, durable trust client, runtime refresh, Vercel publisher,
and first public testnet are implemented. This document records the design;
[omega operations](omega-operations.md) contains operator commands,
[discovery](discovery.md) describes consumers, and the
[trust-client reference](../internal/trust/bootstrap/README.md) specifies formats
and persistence behavior.

## Authority and custody

| TUF role | Responsibility | First-network custody |
| --- | --- | --- |
| Root | Authorize role keys and successor authorities | Three encrypted Ed25519 keys; 2-of-3 threshold |
| Targets | Approve the bootstrap manifest and its validity | Separate encrypted membership key; 1-of-1 |
| Snapshot | Bind metadata versions into a release | Separate online renewal key |
| Timestamp | Advertise the current snapshot | Separate online renewal key |

Root signing keys are unrelated to the three public root **nodes**. A connected
operator workstation may hold and unlock the root quorum. This supports recovery
from one lost key, not protection from whole-host compromise. A separate service
account holds only online renewal keys; node containers receive only public
trust material. Air gaps and independent signing devices are future options.

Production authority schema 2 contains public identity records with separate
age-encrypted key files. Schema 1 remains disposable-only. Ordinary status needs
no passphrase; `status --check-keys` verifies the active restored key generations.
See [custody and recovery](omega-production-custody.md) for accepted limits.

| Metadata | Lifetime | Renewal policy |
| --- | --- | --- |
| Root | 365 days | Operator rotation with at least 180 days remaining |
| Targets / membership | 90 days | Review and reapprove with at least 30 days remaining |
| Snapshot and timestamp | 7 days | Refresh daily; check/retry hourly |

Root and membership deadlines can shorten the online validity window. The
renewal service cannot approve different membership or extend offline approval
indefinitely. A failed daily publication normally leaves six days of validity;
a client without valid cached metadata still needs a reachable repository.
Key rotation is an operator action, not part of daily freshness renewal.

## Repository and manifest

The public bundle pins the network identity, HTTPS repository URL, and initial
signed root. The current authority is recorded in
[public-network-authority.md](public-network-authority.md), with metadata at
`https://sopholeth.io/omega/`. DNS locates that host; TXT records do not convey
trust. Metadata and node clients verify TLS and reject redirects.

Use consistent snapshots, numbered root/targets/snapshot files, a fixed
`timestamp.json`, and content-hashed `bootstrap.json` targets. The manifest
contains schema, network, enclave, and distinct root IDs/HTTPS origins. Validate
its complete signed chain, target hash, schema, network, and endpoints before
activating it. The manifest endorses bootstrap entry points only: ordinary nodes
may join and gossip without appearing in it, and writes remain anonymous.

The publisher retains every numbered root transition and published object in
its journal. Install immutable objects before updating the timestamp. Timestamps
and missing-object responses use `no-store`; existing immutable objects receive
one-year immutable caching. Never prune the chain to repair a failed update.
The metadata-only Vercel project is independent of docs deployments and rollbacks;
[Vercel publication](omega-vercel.md) documents its route, verification, and limits.

Repository identity is bound into the authority, bundle, and client state.
Changing it requires deliberate trust adoption; there is no in-place repository
migration. Normal signed key rotations retain the initial bundle/fingerprint.

## Durable client state and runtime authority

- Serialize access per network and refresh from the latest accepted root, not
  the original bundled root on every restart.
- Preserve authenticated partial progress even if a later download fails.
  Accepting timestamp N must prevent fallback to N-1 after a restart.
- Use durable checkpoints with private ownership, process locks, file/directory
  synchronization, and corruption checks. Missing or corrupt established state
  is an error, not an implicit trust reset.
- Activate only complete verified manifests. Their deadline is the earliest
  expiry across root, targets, snapshot, and timestamp metadata. An unchanged
  valid renewal can extend it; a failed refresh cannot.
- Withdraw incompatible retained authority after verified root/targets
  transitions. Check deadlines on every root-role and recovery-seed lookup,
  including while a refresh is stalled. Equality with the deadline is expired.
- Offline startup may reverify a still-valid durable view. Expired discovery
  withdraws the official root role and seeds; it does not evict ordinary peers
  or alter payload TTLs.

The node pins signed root origins/enclaves through peer updates, including
before first contact. A verified manifest can move those routes. Bootstrap
responses are checked against the signed root identity; arbitrary peer referrals
are not authenticated identities. The [discovery guide](discovery.md) covers
refresh scheduling, route pins, profile renewal, and transport limits.

## Signing, publication, and recovery

Initialization prepares and verifies a complete authority privately, then commits
it with one atomic, non-replacing directory rename. Retries recover the same
allocation or verify the committed authority. They never create replacement keys
for an existing network.

Publishing and rotation use single-writer locks, monotonic versions, immutable
prepared releases, and durable retry journals. `provision-renewal` transfers the
journal and online keys recoverably while leaving operator keys inaccessible to
the service. Publication verifies exact served bytes and signatures before
reporting success. Repair moves forward through retained history; rolling back
the timestamp or promoting arbitrary old metadata is not recovery.

A successor root must meet both the previous and new root thresholds. Online and
membership rotations replace their role assignments through that chain. Root
rotation also requires explicit renewal of the unchanged membership approval.
The scheduler can finish only a fully signed handoff; it cannot supply missing
operator approval. Retired private generations are never fallback signers.

Keep the public transition chain, current journals, and checked encrypted backups.
Loss of an online or membership key is recoverable through the root quorum;
loss of one root key through the other two. If the quorum or trustworthy version
history cannot be recovered, explicitly reset with a new independently adopted
bundle. Disconnected clients may keep accepting old signed metadata until its
lease expires, so rotation does not promise instantaneous revocation.

Native Windows durable discovery, explicit reset-bundle overrides, and remaining
operator report/ownership findings are tracked in the
[network plan](public-network-plan.md). The completed integration spike and
interim DNS audit remain in Git history; current public discovery does not use
that DNS protocol.
