# Sopholeth omega operations

`soph omega init`, `provision-renewal`, `publish`, `rotate`, and `status` support
encrypted TUF authorities, unattended renewal, online/membership/root-key
rotation, root-expiry recovery, and verified publication to a local HTTPS-served
repository on Linux. Production initialization uses encrypted custody by default.
No live authority is created by this implementation. Automated hosted publication,
the compiled trust bundle/release gate, and discovery consumers remain in the
[public-network plan](public-network-plan.md).

The launch profile uses one secured connected operator workstation and a tested
backup. The operator may use `sudo`; the renewal service runs as a separate,
unprivileged account with access only to its own online keys. See the
[custody decisions](omega-production-custody.md) for the accepted limits.
For throwaway rehearsals, add `--encrypted --disposable` to `init` and
`--disposable` to provision/publish/rotate. The flag must match the recorded mode.
Plain `--disposable` retains the legacy plaintext backend for tests only;
existing authorities cannot be relabeled as production.

## Initialize an authority

Build the existing CLI, then choose **one private custody home** for all
operator work. Its parent must exist and be protected from replacement by
other users. The home is created with mode `0700` if absent.

```bash
make build-soph
sudo ./bin/soph omega init \
  --home /path/to/private/omega-home \
  --network sopholeth \
  --repository https://metadata.example.invalid/omega/
sudo ./bin/soph --json omega status \
  --home /path/to/private/omega-home --network sopholeth
```

`--network` here identifies the authority, not a saved client profile. It goes
after `omega init` or `omega status`. IDs contain 1–64 ASCII letters, digits,
underscores or hyphens, beginning with a letter or digit, and are case-sensitive.
The repository accepts HTTPS origins or base paths, including
`https://sopholeth.io/omega/`. The stored URL has no trailing slash; equivalent
host/default-port spellings normalize before keys are allocated. Path segments
use literal ASCII letters, digits, or `-._~`. Traversal, doubled slashes, percent
escapes, credentials, queries, fragments, and backslashes are rejected. The
repository URL is part of authority/key bindings: do not edit an existing
origin-only authority to move it under a path. Hosting/deployment automation is
still separate work.
`init` and local `status` do not contact the repository. Operator commands
do not read or change client profiles.

Initialization creates six distinct Ed25519 keys: a 2-of-3 root quorum and
separate targets, snapshot, and timestamp keys. It signs and verifies a
version-1 root with consistent snapshots and a 365-day expiration, then
produces a public bootstrap bundle accepted by the durable trust client.
The reported SHA-256 identifies the normalized signed initial root; it is
**not** the legacy single-key fingerprint used by the current release gate.

### All-or-nothing commit and retry

Each network has one immutable authority slot under the custody home. A
process lock serializes creation, recovery, and inspection for that slot.
Everything is prepared in a private `.<network>.pending` directory. All six
keys and the fixed intent are committed together in `authority.json` (or the
encrypted staging allocation described below) before
any metadata is signed. After that boundary, retries use exactly those keys,
repository, and expiration; there is no replace/reset/force option.

The complete set is checked against the private keys, the exact serialized
root/bundle is verified, and files and directories are synchronized before
an atomic, non-replacing rename makes `<home>/<network>` visible. A caller
can observe an absent authority or the complete verified authority. Staging
is never reported as a usable authority, exported, or published.

Rerun the **same init command** after interruption. It automatically completes
staging or verifies the existing committed authority, returning the same
fingerprint. Different configuration for an existing or pending network is
rejected. A process can die after commit but before reporting success; retry
recognizes that completed transaction. A parent-directory sync failure is
reported as an unconfirmed durability result, not permission to create new keys.

A torn first private-record write, before any signing, can be discarded and
retried. A complete staged record is preserved. Subsequent torn public-output
writes are reconstructed from the durable private record. Missing or corrupt
**committed** material is damage, not an interrupted initialization: it fails
closed and requires restoration from verified backups. It cannot trigger key
regeneration. An expired authority also cannot be reinitialized.

These guarantees require a local Linux filesystem supporting advisory locks,
hard links, `fsync`, and atomic `RENAME_NOREPLACE`. Process-death and injected
write/commit failures are tested; physical power-loss behavior still depends
on the filesystem and storage honoring synchronization. Unsupported operations
fail without making a partially populated authority slot visible.

The custody home is the durable registry of network identities. Keep using it
and preserve its backups. An offline command cannot detect a second independent
home or a deleted registry; changing homes is not a recovery procedure. A
production workflow must establish the canonical registry, encrypted key storage,
and tested backups before public activation. The proposed first profile permits
one connected operator workstation.

### Files, custody, and status

The network directory contains these public records, all mode `0600` in custody:

| File | Purpose |
| --- | --- |
| `authority.json` | Public-only schema-2 identity, fixed intent, public keys, and encrypted-file digests. |
| `1.root.json` | Verified, signed public initial TUF root. |
| `bundle.json` | Public network/repository/initial-root bundle for the client. |
| `complete.json` | Integrity receipt binding the committed files and root fingerprint. |

Back up the private custody home using protected storage and verify a restored
copy with `omega status`. Only the root and bundle are public handoff artifacts;
never copy the whole home to a site, repository, node host, or logs. The receipt
checks consistency; it is not protection against someone who controls the
operator account and can replace all custody material.

Production initialization always uses encrypted keys. Schema 1, which embeds
six plaintext keys, remains permanently disposable-only. Schema 2 separates
public identity from private encrypted files and rejects mixed custody modes.
Windows public-client support remains a
[separate required gate](public-network-plan.md#windows-public-client-gate).

`status` verifies signed public identity, history, and the completion receipt without a password.
Text and `--json` output include state, network, repository, initial-root
fingerprint, current root version/expiration, and role thresholds/key IDs. Missing, pending,
corrupt, or expired authority returns a nonzero exit with a corrective action.
Unknown expiration dates are omitted. No output contains private keys.

## Encrypted custody and backup restoration

New initialization prompts for one strong passphrase and confirmation on the
controlling terminal with echo disabled. New passphrases require **at least 12
characters**, including when creating a rotated encrypted key. Choose a long,
unpredictable phrase; length alone does not prevent guessing. Older short
passphrases still unlock existing copies for recovery. Rotation creates new keys
and therefore requires the creation policy; there is no password-change command.

A retry unlocks an existing allocation; completed initialization, provisioning,
and prepared-rotation inspection do not prompt. Passwords are not accepted in
arguments, environment variables, or stdin. JSON stays on stdout and prompts
stay on the terminal. Ctrl-C, cancellation, and ordinary errors restore echo.
Commands that unlock keys default to two minutes including password entry;
an explicit global `--timeout`, before `omega`, overrides this. Ordinary status,
HTTPS verification, and unattended renewal never request a password.

### Encrypted files and interruption recovery

The network directory contains the four files listed above and six separate
`root-1.key.age`, `root-2.key.age`, `root-3.key.age`, `targets.key.age`,
`snapshot.key.age`, and `timestamp.key.age` files. All remain private mode `0600`.
Here `authority.json` is **public-only schema 2**: network/repository, creation
transaction, initial fingerprint, public keys, custody profile, and encrypted-file
digests. `complete.json` binds the public authority, root, and bundle. Neither
record embeds a private key. There is no conversion from a schema-1 authority.

The pinned [age Go library](https://pkg.go.dev/filippo.io/age@v1.3.2) provides
authenticated passphrase encryption. Each payload binds the private/public pair
to its authority transaction, network, repository, role, and generation. The
backend uses scrypt work factor 18 (roughly 256 MiB per operation), processes keys
sequentially, and rejects higher work factors, ciphertext over 16 KiB, and
plaintext over 4 KiB. It authenticates the complete decrypted stream before use.
No plaintext operator key file is written. Clearing mutable buffers is best effort;
Go/library copies, swap, and process dumps prevent a perfect memory-erasure claim.

Before any public output, staging durably fixes all six **encrypted** allocations
in `init-keys.json`. Retries install the same ciphertext bytes into their separate
files and reproduce the same signed root. The aggregate is removed and the
directory synchronized before atomic promotion. A torn first allocation write
may be retried only before any derived output exists; a complete allocation with
a wrong passphrase or a damaged committed allocation is preserved. Missing keys
never cause replacement authority creation. Rerun the same encrypted init after
interruption, preserving the original home, network, and repository.

### Verify a restored copy

Stop the renewal timer and operator commands before taking a consistent backup.
Copy **both complete private homes**, including bindings, every key generation,
publication and rotation journals, and complete pending transactions; also retain
the full public repository. Preserve ownership and permissions. Use encrypted
backup storage: the running service's snapshot/timestamp files are plaintext.
Keep passphrase recovery information separately, recoverable without the original
workstation. Update the backup after rotations and retain current publication
history; old keys alone cannot safely resume allocated versions.

Before activation, restore with the working copies unavailable. Initial,
unprovisioned custody can be checked at a temporary private path. After
provisioning, restore the operational home and public repository at their
**original bound absolute paths**, on an isolated replacement or with the originals
safely moved aside and the old publisher disabled. Restore the operator home too.
Do not edit bindings or reset journals to make a backup run somewhere else.
Never operate a second publisher against the live repository.

```bash
sudo soph --json omega status --home /srv/omega-offline --network sopholeth
sudo soph --json omega status --check-keys --home /srv/omega-offline --network sopholeth
sudo soph --json --timeout 2m omega status --verify --home /srv/omega-online --network sopholeth
```

Compare the initial fingerprint and current root/release versions with independent
operator notes and trustworthy published history. Ordinary status inspects public
identity without unlocking. Its active `key_files` inventory reports `locked`
(matching encrypted bytes), `protected` (service-owned online files), `missing`,
or `damaged`. Public identity can remain initialized with an unavailable signer.
`--check-keys` prompts once and verifies all six **active** keys against the
current root. Retired encrypted files are not fallback signers and need not be
present for that check. Wrong passwords, missing/corrupt active keys, or identity
mismatches return a problem and action. No metadata is signed or published.
An expired root can have matching keys but still returns an expiry error; stale
timestamps do not prevent checking recovered keys. HTTPS verification is separate.

Record the fingerprint, active versions, restore date, backup location, and
unlock-recovery location privately. Rehearse signing and renewal with throwaway
authorities. Only resume the real publisher when identity and the latest allocated
versions are established; HTTP 404 or an old backup cannot prove a version unused.

### Deliberate reset

If the root quorum is compromised or trustworthy keys/history cannot be restored,
stop the old deployment and archive its state. On a clean workstation, explicitly
choose a new network ID and fresh custody home, then initialize and back up a new
authority. Distribute its new trust bundle independently of the compromised
repository. Clients must deliberately adopt that bundle with fresh trust state;
deleting the old server does not revoke their old anchor. There is no force/reset
flag and `init` never replaces an existing identity. This experimental-network
recovery path accepts loss of continuity; it is not a normal rotation.

## Publish a three-root manifest

Prepare a dedicated directory to be served at the authority bundle's exact
HTTPS repository URL, including its base path. Its parent must exist, and it must
be outside the custody home; neither directory tree may contain the other. The publisher
creates the final directory if absent. A first publication requires an empty
directory. Existing site files cannot be adopted as a repository.

This first backend writes to a **local filesystem directory**. The HTTPS
server is a separate, already configured service; `publish` does not start it,
upload through an HTTP API, configure Vercel, or deploy root nodes. Verification
uses normal TLS certificate/hostname checks and refuses redirects. A private
rehearsal CA can be supplied through the platform's trusted CA configuration
(for example `SSL_CERT_FILE` on Linux); there is no insecure TLS flag. Hosting
at `sopholeth.io/omega/` has base-path support and committed Vercel routing/cache
rules; the deployment adapter is still pending. See the
[hosting notes](../sites/README.md#omega-metadata-hosting).

Create the approved manifest, for example `bootstrap.json`:

```json
{
  "schema": 1,
  "network": "sopholeth",
  "enclave": "default",
  "roots": [
    {"id": "root-a", "origin": "https://root-a.example.invalid"},
    {"id": "root-b", "origin": "https://root-b.example.invalid"},
    {"id": "root-c", "origin": "https://root-c.example.invalid"}
  ]
}
```

The initial publisher requires exactly three distinct node IDs and HTTPS
origins, the matching network, and `default`. The manifest schema rejects
unknown/duplicate fields and malformed endpoints before signing. Node
reachability and gossip identity are separate deployment checks.

```bash
sudo ./bin/soph --timeout 2m omega publish \
  --home /path/to/private/omega-home --network sopholeth \
  --manifest bootstrap.json --repository-dir /path/to/public/omega-repository \
  --version 1
sudo ./bin/soph --json --timeout 2m omega status \
  --home /path/to/private/omega-home --network sopholeth --verify
```

Version 1 starts publication. Repeat the **same approval version and manifest**
to recover or reverify it while it is the latest release. Use the next
consecutive release version for a new approval. Renewal also consumes release
versions, so inspect status before approving a
change. Versions cannot be skipped or decreased. Reusing a version with different
membership is an error. Formatting and equivalent origin spellings are
normalized before the approval is recorded; root-array order is preserved.
The explicit version makes retries unambiguous, including after the command
commits but loses its terminal output.

Each approval signs targets, snapshot, and timestamp metadata with their
respective keys; it copies the existing signed root without using root keys
to sign again. Lifetimes are 90 days, 7 days, and 7 days, capped by
root expiration. Signing requires at least 24 hours of remaining root validity.
This is an explicit operator approval using the active membership key. Unattended renewal
using only separately provisioned online keys is described below.

### Publication journal and write order

The authority directory stays immutable. Publication state lives separately
at `<operational-home>/<network>.publication` (mode `0700`). Before renewal
provisioning, the authority home also serves as the operational home:

- `binding.json` binds the journal to the authority fingerprint, network, and
  canonical local repository directory.
- `N.release.json` contains the exact public signed bytes, canonical manifest,
  creation time, and the preceding release's digest. These immutable records
  form the durable version history. They contain no private keys.
- `R.rotation-intent.json` is a public handoff: schema 2 for a replacement
  membership key, schema 3 for successor root keys and expiry, schema 4 for
  replacement online keys. Encrypted custody includes the mode and exact private
  file digests without embedding secrets. Schema 1, with inline online secrets,
  remains legacy disposable-only. `R.rotation.json` binds the intent to the
  exact signed successor root.
- Encrypted operator generations live in
  `<operator-home>/<network>.rotations/R.<role>-key.age.json`, with
  `R.encrypted-plan.json` fixing identity, role, and clock. Online generations
  live separately in `<operational-home>/<network>.online-keys/` as
  `R.snapshot-key.json` and `R.timestamp-key.json`. All use private permissions.
- `N.rotation-apply.json` records the reviewed root digest, next release version,
  predecessor, and signing time before the transition release is signed.
- `N.rotation-targets.json` is the public, offline-signed targets handoff for
  a membership-key or root transition. The scheduler can finish either only
  after this handoff is complete and verifies. Release schema 5 records the
  new targets version while preserving the original approval time and expiry.
  Schema 6 records an explicit root transition with freshly approved lifetime
  for the unchanged membership.
- `N.renewal-intent.json` fixes the next renewal's version, predecessor, and
  signing time before signing, allowing recovery of torn writes.
- `N.verification.json` records the last check/attempt, its failure if any,
  and the most recent successful check time for that release.

Client-verification scratch is private temporary data outside the custody homes,
removed on ordinary completion. A killed process may leave a `soph-omega-verify-*`
temporary directory for system cleanup; it has no ordering authority or keys.

The network lock serializes operator work; a separate destination lock prevents
concurrent publishers from racing the mutable timestamp. The entire signed
release is durably recorded before any public object is written. There are
currently at most 10,000 sequential releases per journal; there is
no history compaction or reset command.

Public files are installed in this order:

1. `1.root.json`, retained unchanged.
2. `targets/<sha256>.bootstrap.json`, the content-addressed manifest.
3. `T.targets.json` and `N.snapshot.json`, where `T` is the offline approval
   version and `N` is the current release version.
4. Numbered successor roots, in order, when the release uses rotated role keys.
5. `timestamp.json`, atomically replaced only after the immutable objects are
   durable. It can replace only an exact known older timestamp or retry the
   current one.

Immutable filenames are never overwritten with different bytes. Public data
files use mode `0644`. Newly created repository and `targets/` directories use
mode `0755`, including under `umask 077`. Existing directory modes are preserved;
the operator must configure access through those directories and their parents
for the HTTPS serving account. The destination also contains a private, empty
`.omega-publish.lock`; temporary `.pending` public files may survive a killed
process. Publishing again sweeps the journal and public repository, including
`targets/` and older releases, removing `.pending` twins only when the final
object exists with identical bytes. Pending data without a matching final
object is left for normal recovery or investigation. Serve only the
documented metadata/target paths and disable directory listing. Never serve the
custody home.

A retry uses the recorded signatures and expiration; it does not re-sign the
same version with a new clock. Ordinary approval/renewal interruption before
the timestamp leaves the previous release in place. Rotation can expose the
new root before its timestamp: clients that adopt it reject the old signatures
until the apply finishes. Interruption after the timestamp
may leave the new release live but unverified; rerunning the command completes
verification without changing its bytes. A missing public object can be
restored from its exact journal record. A conflicting object requires explicit
investigation and is never replaced automatically.

Expired prepared metadata cannot be refreshed in place. Approve the next
version, retaining the older record and any objects already published. Keep
all root transitions and all publication history; this implementation performs
no garbage collection. Missing/corrupt history or a destination ahead of the
journal requires restoration of the correct history, not initialization of
fresh counters. Preserve the journal with custody backups. An offline tool
cannot detect an independent or deleted copy of all ordering state.

### What verification establishes

`publish` reports success only after a fresh instance of the real durable
bootstrap client fetches the served metadata and accepts its complete chain.
The accepted versions, root manifest, and SHA-256 hashes of all four metadata
roles must match the prepared release exactly. Additional fetches check every
retained numbered root byte-for-byte, including `1.root.json`, which a freshly
bundled client normally does not download.
Stale propagation, missing files, wrong signatures, altered bytes, and HTTPS
failures return nonzero status even if local publication completed.

`soph omega status --verify` repeats this check without publishing or signing.
Plain `status` remains offline and reports `publication: "not_checked"` along
with the latest prepared release, all role deadlines, and historical check
information. A recorded failure or expired release returns nonzero; a previous
successful check is not a claim of current availability. Live verification
reports `publication: "verified"` only after its receipt is persisted. A lost
verification receipt cannot authorize a write or reset version history.

The global `--timeout` bounds the operator command, including lock waits and
verification (15 seconds by default); allow more time for slow storage or
remote rehearsal. Expiry is checked again before reporting verification
success. Publication checks concern the metadata origin seen by this operator;
external clients and regional caches still need the planned remote rehearsal.

## Provision and schedule unattended renewal

Provisioning decrypts only the initial snapshot/timestamp keys and puts them in
separate service-owned `0600` files under `<network>.online-keys/`. The schema-2
`<network>.renewal.json` contains public identity, mode, and exact file digests.
It never generates an authority, changes roles, or exports root/membership secrets.
Legacy disposable schema-1 renewal records retain their original inline keys.

Use two disjoint homes with stable paths. Create a dedicated `soph-omega` system
account using the host's account-management tools, then prepare these directories:

```bash
sudo install -d -o root -g root -m 0700 /srv/omega-offline
sudo install -d -o soph-omega -g soph-omega -m 0700 /srv/omega-online
sudo install -d -o soph-omega -g soph-omega -m 0755 /srv/omega-public
```

Initialize the authority at `/srv/omega-offline` with the earlier command and
approved network/repository values. Privileged operator commands can maintain
both homes; newly created files and directories retain their destination home's
ownership. Unprivileged commands still require ownership. The renewal account
cannot traverse the root-owned operator home and receives no passphrase. The
HTTPS server needs read access only to `/srv/omega-public`. The term *offline*
here means outside unattended renewal; the workstation can remain connected.

```bash
sudo soph --json --timeout 2m omega provision-renewal \
  --home /srv/omega-offline --network sopholeth \
  --renewal-home /srv/omega-online

# Publish the first approval, or a later membership change, from the offline home.
# The durable binding routes its publication writes to the operational home.
sudo soph --json --timeout 2m omega publish \
  --home /srv/omega-offline --network sopholeth \
  --manifest /path/to/approved-roots.json \
  --repository-dir /srv/omega-public --version 1

# No offline mount or authority files are needed for either command below.
sudo -u soph-omega soph --json --timeout 2m omega publish \
  --home /srv/omega-online --network sopholeth --renew
sudo soph --json --timeout 2m omega status \
  --home /srv/omega-online --network sopholeth --verify
```

Provisioning works before the first approval or with an existing publication.
For an existing publication, omit the version-1 publish example. The operation:

1. Records `<network>.publisher.json` in the offline home, binding the authority
   fingerprint to the operational home. A complete or pending binding prevents
   continued publication through the old journal.
2. Copies existing immutable history, verification receipts, and pending
   publication records under both home locks and the repository lock. Public
   filenames and signed bytes do not change. Existing history in the offline
   home becomes an archive; publishing and status follow the binding.
3. Commits `<network>.renewal.json` in the operational home only after its
   journal and online key files are durable. Its public bundle and file digests
   bind the two online keys. Only this final record enables renewal.

After interruption, rerun the same provisioning command with both homes
available. It completes the same handoff; it cannot redirect the authority to
another home or overwrite a different key record. Repeating it after renewal
has started checks custody without copying the old archive over live history.
Do not remove the binding, copy an archive over live state, run independent
copies of the operational home, or reset counters. Preserve both homes in
backups; restore the complete current operational journal after state loss.
The absolute binding is for this local filesystem backend; remote custody
transport and relocation require a later explicit workflow.

`publish --renew` takes no manifest, output-directory, or version flags. It uses
the bound repository and latest approved membership in the journal:

- Before 24 hours have elapsed, verify the existing release without creating
  a new version. An unsuccessful verification is a failed invocation.
- When due, reserve the next version and signing time durably. Sign only
  snapshot and timestamp, retain the exact manifest and signed targets bytes,
  publish timestamp last, then verify the served result through the real client.
- Complete an interrupted, unexpired release before preparing another one.
  A torn renewal write is recoverable from its durable intent. A pending
  offline approval must be completed through its original offline command;
  renewal cannot replace or approve it.
- If the latest release expired, retain it and publish a higher renewal while
  root and membership approval remain valid. If an interrupted intent itself
  expired, the first retry finishes recording that expired version and fails;
  the next invocation can prepare the higher repair. If membership has also
  expired, finish recording the valid intent, then publish a higher offline
  approval. A torn intent that never committed or signed anything is cleared
  when membership expires so it cannot block that approval. Never rewrite a
  committed version.
- Cap new snapshot and timestamp expiration at the earlier root/membership
  deadline. Neither offline approval can be extended by the online keys.
  Backward clocks, corrupt history, conflicting prepared bytes, and unknown
  destination metadata fail without resetting state.

For example, approval release 1 creates targets 1, snapshot 1, and timestamp 1.
Renewal release 2 retains targets 1 and creates snapshot/timestamp 2. A new
offline approval at release 3 creates targets/snapshot/timestamp 3. Release
records use schema 1 for approvals and schema 2 for renewals before rotation.
Schemas 3 and 4 serve the same purposes after rotation and also retain the
authenticated root chain. This does not change the private `authority.json`
schema or make it production-ready.

Release and renewal/rotation-apply records pin `timestamp_days: 7` for the new
policy. Older records omit the field and retain their exact one-day timestamps
and six-hour renewal trigger. Interrupted old reservations finish with that
policy; the next new release adopts seven days. Existing journal bytes and
signatures are never rewritten by this upgrade.

### Scheduler and monitoring

The example [service](examples/omega/soph-omega-renewal@.service) and
[timer](examples/omega/soph-omega-renewal@.timer) run hourly. Successful runs
verify the current release; the first invocation at least 24 hours after
creation renews it. Hourly retries continue after failure, before the normal
seven-day snapshot/timestamp expiry. A missed daily renewal normally leaves
six days of validity, unless root/membership approval expires sooner. The timer
catches missed runs after downtime. These are opt-in examples; installing Sopholeth does not enable them.

Adjust the executable, user, network, and paths, provision custody, publish and
verify the first approval, then install the reviewed units:

```bash
sudo install -m 0644 docs/examples/omega/soph-omega-renewal@.{service,timer} /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now soph-omega-renewal@sopholeth.timer
systemctl status soph-omega-renewal@sopholeth.timer
journalctl -u soph-omega-renewal@sopholeth.service
```

The service allows writes only to the operational and public directories and
makes the offline path inaccessible. Run it under the owner of the operational
files; Unix ownership checks reject a different account. The HTTPS server
needs read access only to the public files. Keep synchronized clocks and allow
network access to the exact repository origin. No DNS/hosting credentials or
root-node credentials are required by this filesystem backend.

The operator running this timer owns its failure alerts and offline approval
calendar. Connect the existing monitoring system to the unit's nonzero exits
and JSON reports; these examples do not send notifications themselves. Monitor:

- `publication`, `release.last_error`, `checked_at`, and `last_verified_at` for
  failed verification, stale successful checks, and a stopped timer. A prior
  successful receipt never proves current availability.
- `release.renew_after` and `renewal_due`. Alert when renewal remains due beyond
  one scheduled invocation plus timer jitter; a stopped process cannot report
  its own failure. Run an independent health check as well as watching job exits.
- `release.expires` and `warnings`. Reapprove membership with 30 days remaining
  and plan root rotation with 180 days remaining. Warnings are actionable
  even when the current release is valid and the command exits zero.

A failed invocation exits nonzero. Fix hosting, connectivity, permissions, or
clock errors and rerun `publish --renew`; status retains the recorded failure
and prior successful verification time. With `--json`, missing or unprovisioned
operational homes also produce a report with `problem` and `action`, identifying
when to provision custody or restore existing state. Expired membership requires
bringing the offline authority back for a new approval at the next available release
version. Root expiry requires `rotate --role root` and an explicit membership
approval as described below. Do not claim healthy discovery from expired metadata.

## Rotate the online keys

This slice replaces **both snapshot and timestamp keys** through a successor
root signed by the existing 2-of-3 root quorum. Root and membership keys, their
thresholds, root expiration, and the exact approved membership remain unchanged.
The original bundle and its fingerprint remain the client's trust anchor;
clients learn new assignments through the retained, authenticated root chain.
The [TUF update protocol](https://theupdateframework.github.io/specification/latest/#update-root-metadata)
checks the old and successor root thresholds. Both use the same root quorum in
this mode. Use `--role targets` for membership-key rotation as described below;
use `--role root` for authority rotation and extension of root validity.

Provision the separate operational home and publish membership first. Mount
the offline authority for preparation and application. The service and operator
share a network lock; renewal may continue while a transition awaits review.
Preparation does not publish anything or reserve a release number.

```bash
# Prepare root 2; subsequent rotations use consecutive root versions.
sudo soph --json --timeout 2m omega rotate \
  --home /srv/omega-offline --network sopholeth \
  --root-version 2

# After reviewing rotation.root_sha256 and the old/new key IDs, paste that
# exact digest in place of <root_sha256> below.
sudo soph --json --timeout 2m omega rotate \
  --home /srv/omega-offline --network sopholeth \
  --root-version 2 --apply '<root_sha256>'

sudo soph --json --timeout 2m omega status \
  --home /srv/omega-online --network sopholeth --verify
```

Review the network, initial fingerprint, current root version, successor digest,
and replacement key IDs. `rotation.replaces` and `rotation.keys` show the old and
new online assignments; no command output includes private keys. `--apply`
requires an existing preparation with an exactly matching digest. Both commands
are idempotent for the explicit root version. Repeating an older applied version
verifies the latest release rather than restoring its old timestamp.

The new keys become durable in separate operational key files before the root
is signed; the public journal binds their digests. Complete pending writes retain their bytes; torn first writes before
signing can be reconstructed. Application reserves a release version and clock
before signing with the new keys. The release commits the active generation;
ordinary renewal and offline membership approval select those keys from then
on. No in-place key replacement or mutable key-selection pointer is needed.

The apply release retains the latest approved targets and manifest, including
an approval made after preparation. Snapshot and timestamp advance to the next
release number. Dependencies are durable before the successor root is exposed;
timestamp is replaced last. Success means the exact release and all retained
roots were verified through HTTPS. There are at most 64 root versions and
10,000 releases in a journal; there is no reset or compaction command.

### Interrupted application and recovery

| Observed state | Recovery |
| --- | --- |
| `rotation.state: preparing` | Rerun preparation for that root version with offline custody available. |
| `prepared` | Review the digest, then apply it. Scheduled renewal continues using the current keys. |
| `applying` or a failed apply command | Retry the same version/digest, or run `publish --renew` from the online home after the apply reservation is complete. |
| Torn, uncommitted apply reservation | Rerun the original `rotate --apply`; the scheduler reports the problem and leaves it intact. |
| Applied release expired during interruption | Finish recording it privately, then run `publish --renew` again for a higher repair while membership remains valid. Expired timestamps are never served. |
| Membership also expired | Finish the valid reservation, then publish a higher offline membership approval. For a torn apply that never signed a release, the original apply command removes the empty reservation and reports the need for reapproval. |
| Corrupt or missing committed intent, keys, or history | Restore the complete verified operational journal and custody. Never delete counters, edit immutable records, or initialize a replacement authority. |

Once the reviewed apply intent is complete, recovery needs only the online
home: it holds the replacement online keys and signed transition, but no root
or membership private keys. A scheduler can finish a killed apply, reconstruct
a torn release, and verify publication with the offline mount absent. A pending
offline approval or renewal already owns its release number and must finish
before application can reserve the next one.

This local filesystem backend does not switch the entire HTTPS directory
atomically. Between successor-root visibility and timestamp replacement, a
client may authenticate the new root and reject the old timestamp. Its durable
client state then revokes the old accepted lease, including across restart.
Finish the transition and refresh again; do not put the retired root or
timestamp back. A metadata outage or stale cache returns failure and retains
the exact prepared release for retry.

### Adoption, retirement, and custody limits

After publication verifies, confirm that all three roots and representative
fresh and returning clients report the successor root and expected release.
Use the same initial bundle, including a client absent for multiple rotations.
Record this evidence during deployment rehearsal; this command does not poll
root-node adoption. `rotation.state: applied` records successful publication
verification, not fleet adoption; `publication: not_checked` remains an offline
status result.

The successor root no longer authorizes either old online key. Clients that
have not received it can retain their earlier valid lease until its deadline;
rotation is not instantaneous revocation of disconnected clients. Keep all
numbered roots permanently, even after expiration, so returning clients can
authenticate the full transition chain. Preserve publication and custody backups.

Encrypted custody separates private generations from retained public history.
New signatures use only the current generation. Once the successor is verified,
adoption is confirmed, and its backup is tested, retired private generations may
be removed from working custody; keep protected recovery copies while needed.
Retain the public authority, receipts, plans, every handoff and release, and all
numbered roots. The command never erases keys or falls back to old copies.
Legacy schema-1 records must remain intact; do not edit their embedded secrets.
Missing journal history requires verified restoration, not reconstruction from
whatever an HTTPS server currently returns.

## Rotate the membership signing key

`soph omega rotate --role targets` replaces the offline key that signs the
bootstrap membership manifest. It uses the same prepare/review/apply workflow
and consecutive root versions as online rotation; `--role online` remains the
default. A prepared root version belongs to one role and cannot be repurposed.
The unchanged root quorum authorizes the successor key. Root keys and expiry,
online keys, the latest approved root list, and its expiration stay unchanged.
Changing the list or extending its approval requires a separate `publish`.

```bash
# Use the next root version; this example follows an applied online root 2.
sudo soph --json --timeout 2m omega rotate \
  --home /srv/omega-offline --network sopholeth \
  --role targets --root-version 3

# Review rotation.role, root_sha256, replaces.targets, and keys.targets.
sudo soph --json --timeout 2m omega rotate \
  --home /srv/omega-offline --network sopholeth \
  --role targets --root-version 3 --apply '<root_sha256>'

sudo soph --json --timeout 2m omega status \
  --home /srv/omega-online --network sopholeth --verify
```

Preparation durably stores the new private generation in the offline home
before handing its public key to the operational journal and signing the
successor root. Back up that generation with the existing offline custody.
It does not publish or reserve a release number. An interruption before the
public handoff may leave only the private generation; rerun the same preparation
even if operational status does not yet show a pending rotation. Complete
pending key writes are retained; missing/corrupt committed custody requires
restoration, never generation of a different key for the same root version.

Applying reserves the next release and clock before signing new targets with
the replacement key. The targets version advances to that release number,
while its contents and expiration match the latest approved targets exactly.
An independent approval made during review is retained. Once application is
reserved, other approvals and renewals cannot take its release number.
Snapshot and timestamp use the active online generation. Later explicit
membership approvals use the new offline key; scheduled renewals preserve
the handoff's signed targets and original approval deadline.

The new membership private key never enters the operational home, public
repository, or reports. The initial bundle and fingerprint remain unchanged.
Use the same HTTPS verification, client-adoption checks, retained root chain,
and physical-key-retirement limits as online rotation. Publication success
does not prove fleet adoption or revoke disconnected clients immediately.

### Membership handoff recovery

| Observed state | Recovery |
| --- | --- |
| Preparation interrupted | Rerun the same `--role targets --root-version N` preparation with the offline home available. |
| Apply reserved, targets handoff missing or torn | Rerun the original apply command with the offline home. Renewal returns a problem and action; it cannot produce the missing offline signature. |
| Complete signed handoff, application interrupted | Retry the original apply, or use `publish --renew` with the offline home unmounted. The scheduler validates and completes the exact handoff, including a complete pending write. |
| Reserved release expired during interruption | Complete it privately, then renew at a higher version while membership remains valid. If membership expired too, use a higher explicit offline approval. An expired timestamp is never served. |
| Membership already expired before a new apply reservation | Reapprove through the offline `publish` path, then apply the same prepared root. Rotation cannot extend approval. |
| Committed handoff damaged, or apply reservation missing beside signed handoff | Restore matching journal records. Do not discard the handoff or reuse its release number. |
| Active rotated membership private key lost, approval still valid | With the initial authority, root quorum, and operational journal intact, prepare and apply the next `--role targets` generation. Then approve future membership with that key; never fall back to a retired key. |

Loss of the public `authority.json`, an expired approval combined with a lost
active membership key, or journal damage requires verified restoration. Keep
all public numbered roots and current custody/journal backups. The command does
not delete old private generations or recover a missing current journal.

## Rotate the root authority and recover expiration

`soph omega rotate --role root` replaces all three root signing keys, retaining
the 2-of-3 threshold and all other role assignments. The successor carries two
signatures from the current quorum and two from the new quorum. Preparation fixes
its expiry at 365 days after preparation, or one second beyond the previous
expiry if that is later. The initial bundle and authority fingerprint remain
unchanged; clients authenticate the retained numbered transition chain.

Root application explicitly reapproves the **unchanged current membership**.
This is necessary when root expiration has also expired its capped targets
approval. The command requires `--renew-approval` with `--apply`; it is rejected
on preparation and on other roles. Review `release.roots`, the current approval
deadlines, `rotation.root_expires`, and the old/new root key IDs before applying.
If another operator publishes a new membership while preparation is pending,
review the latest report again: application reapproves the latest membership
under the shared lock, not the list that happened to exist at preparation time.

```bash
# Use the next consecutive root version; this follows the preceding examples.
sudo soph --json --timeout 2m omega rotate \
  --home /srv/omega-offline --network sopholeth \
  --role root --root-version 4

# Approve the reviewed root digest and renew the unchanged current membership.
sudo soph --json --timeout 2m omega rotate \
  --home /srv/omega-offline --network sopholeth \
  --role root --root-version 4 --apply '<root_sha256>' \
  --renew-approval

sudo soph --json --timeout 2m omega status \
  --home /srv/omega-online --network sopholeth --verify
```

New encrypted root signers live in `R.root-1-key.age.json`,
`R.root-2-key.age.json`, and `R.root-3-key.age.json` under
`<offline-home>/<network>.rotations/`, with the same permissions as membership
keys. `R.encrypted-plan.json` fixes the
preparation identity and clock before creating keys. Preserve complete pending
writes and all committed files; retries never generate replacement material for
a handed-off identity. Only public root keys, metadata, and signed approvals
enter the operational home or HTTPS repository.

Application reserves the next release number and signing time, then uses the
current offline membership key to sign a new targets version. Its approval lasts
90 days from that reserved time, capped by the successor root expiry. The root
must have at least 24 hours of validity when reserving application. Online roles
keep their current keys and normal snapshot/timestamp lifetimes. Once the signed
handoff is complete, `publish --renew` can finish with the entire offline home
unmounted. Before that boundary, the scheduler reports the missing handoff and
requires the original offline apply command. A retry cannot move the reserved
approval clock forward.

| Observed state | Recovery |
| --- | --- |
| Preparation interrupted | Rerun the same root preparation. Each complete key write is recovered exactly; another role cannot reuse the version. |
| Current root and membership expired | With custody and the journal intact, prepare the next root and apply with `--renew-approval`. Old metadata remains unusable until the successor and fresh approval verify. |
| Root apply interrupted before signed handoff | Rerun the same offline apply with its digest and `--renew-approval`; do not reset the reservation. |
| Signed handoff complete, publication interrupted | Retry apply or run `publish --renew` without offline custody. Retain all numbered root files. |
| Reserved timestamp expired | Complete the reserved release privately, then retry renewal at a higher version while its membership/root approval remains valid. If targets expired too, issue a new offline approval at the next release version. Expired timestamps are never published. |
| One signer in the current rotated generation unavailable | The other two can authorize the next complete root generation. Never fabricate the missing key or fall back to retired initialization keys. |
| Two current root signers unavailable | Restore protected backups. This command cannot reconstruct a lost quorum. |
| Committed plan, signer, or journal corrupted | Preserve the evidence and restore matching records; corruption is not treated as an absent signer. |

Fresh and returning clients can traverse an expired initial or intermediate root
only through its authenticated successor chain, ending in currently valid metadata.
An expired chain with no valid successor still fails. Verify adoption by all three
nodes and representative clients before retiring keys; `applied` confirms served
publication, not adoption by every node. Rotation cannot instantly revoke a
disconnected client's existing lease.

The public schema-2 authority and completion receipt stay intact after rotation.
The first-network custody profile permits one connected operator workstation
and an explicit network reset after authority compromise; clients must deliberately
adopt an independently distributed replacement anchor. Keep public transition
history and protected recovery material. A prepared successor that itself
expires before apply cannot be refreshed in place or overwritten; preserve its
records for a separately reviewed recovery procedure.

## Release integration and legacy lab tooling

The standalone `omega` binary is retired. The old DNS signer lives only in
[test/burnin/legacy-omega](../test/burnin/legacy-omega/README.md), for existing
burn-in consumers awaiting TUF migration. It must not create the public network.

Ordinary builds still have no public trust anchor and reject public discovery.
The existing `make check-public-release` gate compares `OMEGA_EXPECTED_SHA256`
with the **legacy decoded 32-byte public key**, not this suite's initial signed
TUF-root fingerprint. The compiled bundle and release gate must be migrated
together before a public release; do not paste the new fingerprint into the old
gate to bypass that work. Private-network development remains available.
