# Omega production custody and recovery

Status: proposal for the first experimental public network, revised 2026-09-23.
This scopes the next slice of
[#195](https://github.com/TickTockBent/Sopholeth/issues/195) and the
[public-network plan](public-network-plan.md#1-build-the-omega-suite). The
[implemented commands](omega-operations.md) still require disposable custody.
The production behavior described here is not implemented yet.

## Launch criteria and accepted limits

For this launch, secure operation has two requirements:

1. **Discovery cannot be forged without the authorized signing keys.** Clients
   authenticate metadata against the pinned authority, enforce role thresholds,
   and reject tampering, rollback, and expired approval. The publisher cannot
   approve membership or replace the authority using its online keys alone.
2. **The operator has a recovery path.** Interrupted work preserves the same
   authority; a tested backup restores keys and state; ordinary expiry and key
   rotation remain recoverable. If keys or trustworthy history cannot be
   recovered, an explicit network reset is acceptable.

Use one operator's secured, normally connected Linux workstation, encrypted key
files, and a tested backup on separate storage. An air gap, dedicated signing
laptop, multiple administrators, and separate root signing machines are **not
launch requirements**.

This network has no current users, financial assets, or critical data. We accept
that a workstation compromise could expose its authority and require starting
a new network. Encryption protects stored copies, not keys unlocked on a
compromised host. Revisit this profile when others depend on network continuity
or valuable data; stronger custody is future work.

## Keys and authority records

Keep the existing roles and metadata format: three root keys with a 2-of-3
threshold, one membership key, and separate snapshot/timestamp keys. All three
root files may live on the workstation and be used in one session. This threshold
permits replacement of one lost key; it does not provide host-compromise
protection or independent human approval in this profile.

| Material | Location and use |
| --- | --- |
| Root and membership keys | Encrypted files in the operator's private home; unlocked for explicit initialization, approval, or rotation |
| Snapshot/timestamp keys | Protected files in the renewal service's separate operational home; available for unattended publication |
| Authority record and journals | Public identities, signed history, and durable transaction state; retained alongside the corresponding private homes |
| Recovery copy | Encrypted backup on separate storage, including keys and complete journals; unlock information recoverable without relying on the workstation alone |
| Published metadata | Public HTTPS repository; no private keys or custody archives |

The renewal service must not have access to root/membership keys or their
passwords. Separate operating-system accounts and permissions on the same
machine are sufficient; a separate publisher host is optional. This boundary
protects against a compromise confined to the service account, not a whole-host
compromise. Node processes receive public trust material only. Use normal host
updates, access controls, private-file permissions, and protected storage.

The suite's term **offline approval** means an operator action outside unattended
renewal for this profile, not a disconnected computer. This deliberately relaxes
TUF's recommended offline root/targets custody while retaining its role separation
and verification rules. [TUF custody guidance](https://theupdateframework.io/docs/faq/).

Embed the maintained Go implementation of **age** in `soph` for passphrase
encryption. Pin and review the dependency and bound file sizes and scrypt work;
do not invent cryptography or require another CLI.
[Format](https://c2sp.org/age@v1.1.0#the-scrypt-recipient-type),
[Go API](https://pkg.go.dev/filippo.io/age#ScryptIdentity.SetMaxWorkFactor).
Store one key per encrypted payload, bound to its role, generation, and authority;
validate the complete payload and derived public identity before use.

Unlock through a non-echoing terminal prompt. One strong passphrase for the local
key set is acceptable; keep recovery information apart from backup media. No
secrets belong in arguments, environment variables, logs, JSON reports, or
plaintext temporary files. Avoid retaining decrypted keys between commands;
account for swap/dumps without promising perfect memory erasure in Go. The two
service keys may use service-owned files and filesystem permissions for unattended
renewal; encrypt their backups. Changing a password does not revoke an old key copy.

Reserve `authority.json` **schema 1** permanently for disposable authorities.
Use **schema 2** for a public-only record of the network, repository, creation
transaction, initial fingerprint, roles, and custody profile. Keep encrypted
private files separate, including rotated generations. Update renewal/rotation
records that currently embed plaintext keys too. Reject mixed identities and
disposable records substituted into production state; do not relabel existing
disposable keys as a production authority. `status` must inspect public state
without unlocking keys, including when one root key is unavailable.

## Initialization and routine operation

Keep initialization a local, recoverable transaction. Persist fixed intent and
encrypted key allocations, stage and verify the complete authority, then promote
it atomically without replacing an existing authority. Preserve the existing
locking and durability guarantees. Durable allocations and signed root bytes
must survive retries unchanged; corruption must not trigger silent regeneration.
Resolve incomplete first-write recovery in the encrypted backend implementation.

A failure before promotion leaves inactive pending work; after promotion, report
the committed identity and any unconfirmed durability. Never expose a partial
bundle as initialized. Preserve expired pending material for explicit recovery.
Keep the canonical registry and its backups: a second empty home cannot establish
that a network has never existed.

Verify a backup **after initialization and before public activation**. If copying
fails, retry the backup of the same committed authority. No cross-device commit,
signed backup receipt, or multi-machine ceremony is required.

Use the existing `soph omega init`, `publish`, `rotate`, `provision-renewal`, and
`status` commands. Root operations can unlock the needed keys locally in one
invocation. Keep prepare/review/apply where already needed for recoverable
rotation, without adding distributed signature collection, per-signer journals,
or standalone `key` and `sign` commands. Existing backup tools plus a documented
restore procedure are sufficient; a new backup subcommand is not a launch gate.

Retain go-tuf signature verification, both old and new root quorums for rotation,
immutable prepared outputs, monotonic counters, exact-byte retries, and
timestamp-last publication. Renew snapshot/timestamp daily with seven-day
validity, capped by root/membership approval, and retry failures hourly. This
provides publishing-outage headroom without changing signing keys. The existing
single publisher, local-directory repository, and same-host operational binding
are sufficient. Encryption does
not require a new publication protocol. Serve only the public repository over
HTTPS; keep operator keys behind an internal interface for future backends.

## Recovery and deliberate reset

Back up complete authority and operational state, including pending transactions
and signed but unpublished outputs. Copy consistently while signing/publication
is stopped or under its existing locks. Update backups after key changes and
retain current journal history; old keys alone cannot safely resume current
versions. One separately stored encrypted recovery set is sufficient initially.

Before launch, restore it to a temporary private location with the working copy
unavailable. Check the expected fingerprint, reopen recovered keys, and verify
the journal. Rehearse signing and publication with throwaway keys; never run a
second live publisher from a backup. Record the backup and unlock-recovery
locations, verified fingerprint, and restore date in private operator notes.

| Failure | First-network response |
| --- | --- |
| Interrupted initialization or signing | Resume the same transaction and allocations; restore damaged material rather than regenerate it |
| One root key lost | Use the remaining two to rotate; verify the replacement backup |
| Workstation or multiple keys lost, without suspected exposure | Restore the current backup on a replacement workstation and verify identity and history |
| Membership key compromised | Replace it through the root quorum and approve safe membership |
| Renewal service compromised, operator keys unaffected | Stop the old publisher, rotate both online keys, and resume verified history in a clean service environment |
| Root quorum or operator workstation compromised | Retire the experimental network and establish a new authority on a clean workstation |
| Required keys or current journal unrecoverable | Stop; deliberately reset the network instead of building forensic recovery tooling |

Before resuming an authority, establish the latest allocated versions from
trustworthy journals and published history. An old backup or HTTP 404 does not
establish that a version is unused. If history cannot be established, stop rather
than sign conflicting metadata. Disable the old publisher before restoring
another; a local file lock does not fence two hosts. Ordinary rotation retains
the public root chain; old private backups are never automatic fallback signers.

A reset is an explicit operator decision, never `init` silently replacing an
existing network. Stop the old deployment, archive its state, and create a new
network identity and trust bundle in a fresh home on a clean workstation.
Distribute that bundle through the release/operator channel independently of
the compromised repository. Existing clients must explicitly adopt it with
fresh trust state; deleting the server does not revoke their old anchor.
Data continuity across a reset is not required. This follows TUF's requirement
for out-of-band root replacement after root-quorum compromise.
[TUF compromise guidance](https://theupdateframework.io/docs/faq/).

## Implementation scope

1. **Encrypted custody and public inspection.** Add schema 2 and encrypted key
   access, separate public verification from unlocking, and reuse the existing
   atomic initialization transaction. Keep schema 1 disposable-only.
2. **Existing lifecycle and launch rehearsal.** Carry the backend through
   approval, rotation, renewal provisioning, and status. Document and test backup
   restore and deliberate reset with throwaway keys. Retire the standalone
   `omega` binary and update its consumers when the replacement is complete.

These slices must demonstrate the two launch criteria above. Air gaps, independent
signing devices, HSMs, multiparty approvals, portable signing requests, signed
backup attestations, and automatic reconstruction of lost journals are future
work, not gates. Other launch work remains in the public-network plan.

Follow the [test guidance](../CONTRIBUTING.md#development) from #229. Add focused
checks for encryption/unlock failures, wrong identity, interrupted encrypted
allocation, and backup restoration. Reuse existing verification, publication, and
rotation coverage. One throwaway end-to-end rehearsal should establish that this
profile can initialize, publish, renew, rotate, restore, and intentionally reset;
do not multiply existing fault matrices by every signer or backend.
