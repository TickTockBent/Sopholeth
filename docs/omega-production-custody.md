# Omega production custody and recovery

Status: encrypted offline files selected; operator workflow proposed,
2026-09-23. This specifies the next slice of
[#195](https://github.com/TickTockBent/Sopholeth/issues/195) and the
[public-network plan](public-network-plan.md#1-build-the-omega-suite). The
[implemented commands](omega-operations.md) still require disposable custody.
Command extensions below are proposals, not runnable production instructions.

## First production profile

The selected first backend uses individually encrypted offline key files and
separately stored recovery copies. Keep the existing Ed25519 roles: three root
signers with a 2-of-3 threshold, one membership signer, and separate
snapshot/timestamp keys.
Use the existing `soph` binary for every operation. Hardware-backed signers can
be added behind the same signing boundary later; the initial profile does not
require an HSM service or a second operator CLI.

The first operator backend targets Linux, matching the existing filesystem
guarantees. This does not defer the separate native Windows public-client gate.

| Material | Normal location and use | Recovery material |
| --- | --- | --- |
| Each root private key | Its own offline signing environment; used for initial authority and role rotations | Individually encrypted copy on separate media, with independently recoverable unlock information |
| Membership private key | A separate offline signer; used to approve membership and its expiry | Encrypted backup kept apart from its working copy |
| Snapshot/timestamp private keys | Dedicated online publisher account, never a node host | Protected backup of current keys and the complete operational journal |
| Authority registry and signing decisions | Coordinator and the relevant offline signers; contains public identities and transaction history | Complete recoverable archives, including prepared but unpublished decisions |
| Published metadata and manifest | HTTPS repository | All numbered roots, immutable release objects, and the recorded publication history |

One person can operate the signers. Independence concerns the places where
keys can be stolen or lost, not a requirement to recruit three administrators.
Different filenames, passwords, or USB drives used on one persistently
compromised computer do not make independent signing environments. No ordinary
host should accumulate a root quorum's decrypted keys, even across separate
sessions. The operator must record the actual signing environments and backup
locations before activation; software cannot prove their physical independence.

The coordinator moves public proposals and signatures between signers. It
never imports root or membership private keys. The publisher can run unattended
with just its two online keys. Root-node credentials remain a separate concern.
These choices preserve the existing [role separation](omega-trust-design.md#authority-and-custody).

## File protection and signing access

Use the maintained Go implementation of **age**, embedded in `soph`, for the
offline file backend. Its passphrase profile supplies an established encrypted
file format; do not invent an encryption envelope or shell out to an installed
`age` command. Pin and review the library version, dependency changes, and
resource limits in the first implementation PR. The library exposes a maximum
scrypt work factor for bounding hostile inputs.
[Format](https://c2sp.org/age@v1.1.0#the-scrypt-recipient-type),
[Go API](https://pkg.go.dev/filippo.io/age#ScryptIdentity.SetMaxWorkFactor).

Each encrypted payload holds exactly one key, its public identity, role,
generation, and binding to the authority-creation transaction. Validate the
whole decrypted record and derived public key before signing. Reject malformed,
truncated, oversized, foreign-authority, or wrong-role files. Use separate
high-entropy unlock secrets for independent signers and recover those secrets
separately from their encrypted media. Changing a password does not change a
signing key or revoke an older encrypted copy.

Offline unlocking uses a non-echoing terminal prompt. Secrets never appear in
flags, environment variables, JSON reports, logs, or plaintext temporary files.
A future noninteractive offline interface would need an explicit protected
input channel; it is unnecessary for the first profile. Decrypted keys exist
only for the signing operation. Do not promise perfect memory erasure in Go;
the signing environment must also protect swap and crash dumps.

For unattended renewal, start with separate service-owned key files protected
by the operating system and filesystem permissions. Password encryption with
the password stored beside an online key does not protect against compromise
of that service. Encrypt its recovery archive separately and keep the recovery
secret off the publisher. Offline keys must not be copied into that archive.

## Public authority records and local state

Reserve `authority.json` **schema 1** permanently for disposable authorities.
Introduce **schema 2** for production. It records the network, repository,
creation transaction, initial root fingerprint, public key assignments, and
custody profile; it contains no private keys or passwords. Bind completion to
the exact public root/bundle and recovery-verification records. Keep portable
authority identity separate from machine-specific paths and signer locations.

Give key payloads, signing requests, receipts, and operational bindings explicit
record types and independently checked versions. Existing disposable rotation
and renewal records also contain private material: changing `authority.json`
alone is insufficient. Production parsers must reject a disposable key map or
record substituted into the new workflow. Do not offer an in-place conversion
that relabels previously co-located disposable keys as independent production
custody.

The existing bundle fingerprint remains the hash of the exact initial signed
root. Use a separately persisted creation transaction ID to bind keys generated
before that root exists. Once the public assignments and signing template are
fixed, retries must preserve their bytes, clock, versions, and transaction ID.
Freeze the signature set and serialized root before recording its fingerprint;
late signatures cannot rewrite the committed initial root.

`status` must verify production public state with every offline key unmounted.
Report authority state, missing ceremony inputs, collected signing quorum,
recorded backup verification, and publication state separately. A historical
backup check does not mean that a removable drive is connected or healthy now.

## Initialization: prepare, recover, commit

An interrupted ceremony may leave durable pending work, but must never expose
a partly initialized authority as usable. Atomicity applies to activation in
the authority registry, not simultaneous writes across independent machines
and removable drives. Preserve completed key allocations during recovery.

1. **Reserve the network transaction.** Lock the established registry, validate
   configuration, and durably create its pending transaction before exporting
   requests. An existing, damaged, or interrupted authority is inspected or
   recovered, never replaced. Reusing a different empty registry cannot prove
   that the network has never existed; preserving the registry is an operator
   responsibility, as in the disposable workflow.
2. **Allocate keys at their owners.** Each root/membership signer creates and
   durably stores one encrypted key locally. The publisher allocates its two
   online keys. Export a public descriptor only after its private allocation is
   durable and can be reopened. An established descriptor or complete key
   record forbids silent regeneration. Define torn first-write handling before
   implementing this step; no identity may escape from an incomplete allocation.
3. **Verify recovery copies.** Reopen the independently stored copy with the
   working copy unavailable, verify its expected key identity, and produce a
   signature over a fresh, domain-separated recovery challenge. Bind the receipt
   to the transaction, key, encrypted-copy digest, and verification time. Check
   all three root keys, membership, and both online keys. The receipt proves key
   possession during that check; media separation remains an operator assertion.
4. **Review and sign one root.** Freeze the public template and designated
   initial signing quorum. Each signer independently displays the network,
   repository, all role keys and thresholds, expiry, and proposal digest before
   signing. Verify distinct authorized signatures through go-tuf. An encrypted
   file, copied receipt, or matching label alone never counts as a TUF signature.
5. **Prepare complete activation material.** Assemble the signed root, public
   bundle, schema-2 record, receipts, and completion record. Verify their exact
   relationship without reopening private keys. Archive the complete creation
   record and verify it can be restored. Partial signature packets remain
   ceremony inputs; `soph` must not export an activatable bundle from pending
   state.
6. **Commit once.** Use the existing exclusive promotion and directory-sync
   semantics to expose the complete authority. Only this state permits normal
   bundle export and provisioning. After interruption, return the same committed
   identity or an actionable pending/invalid report. Never report `initialized`
   merely because some keys or signatures exist.

Bad passwords, unavailable signers, failed backup checks, or filesystem errors
before promotion leave pending work inactive; damaged material requires its
recovery copies. A failure after promotion must report the complete committed
identity and any unconfirmed durability, never imply that creation can restart.
Do not hold a process or lock open while waiting for another signing
environment: each invocation checkpoints
its progress. Never delete pending custody as an automatic rollback. Expired or
conflicting signed proposals require explicit recovery; they are not permission
to create another root 1. Recovery of an expired initial root must preserve that
anchor and establish a signed successor before activation.

No tool can recall material an operator exports manually or make multiple
independently copied registries globally exclusive. Release fingerprint checks
and the recorded activation ceremony remain necessary.

## Operator commands and signing decisions

Extend the existing command family with a small shared custody/signing workflow.
The exact flags belong to the implementation PRs; these are required behaviors.

| Proposed surface | Responsibility |
| --- | --- |
| `soph omega key` | Create a local key allocation, export its public descriptor, and inspect its identity; no general plaintext-key export |
| `soph omega backup` | Make and verify a key recovery copy or a complete transaction/journal archive; distinguish key recovery from history recovery |
| `soph omega sign` | Review and sign a typed public proposal using one local signer; return public signatures only |
| `soph omega init` | Prepare/resume the creation ceremony and commit only when its required inputs and recovery evidence are complete |
| `soph omega publish` / `rotate` | Prepare fixed approval/rotation requests, accept verified signatures, and finish their existing publication/recovery lifecycle |
| `soph omega provision-renewal` / `status` | Bind the publisher to its authorized keys and journal; inspect public state without offline unlocks |

A signing request carries the operation, creation ID or established fingerprint,
network/repository, predecessor metadata, exact proposed metadata, versions,
expiry, and request digest. Use go-tuf's canonical signing representation and
signature verification. Signers validate the retained root chain against their
own pinned identity and compare explicit policy changes; a coordinator-supplied
digest is not sufficient authorization. Review the actual endpoint changes for
membership, and require explicit reapproval when a root rotation renews it.

Each signer keeps a durable local record of accepted root history and signed
decisions. Repeating a decision returns its exact signature; another body for
the same authority, role, and metadata version is rejected. Persist and back up
the decision before exporting its signature. This prevents a fresh coordinator
or a replayed request from inducing conflicting signatures. Pending signatures
do not by themselves advance the signer's trusted active root; that requires
the complete authenticated successor. Catch-up validates all missing roots.

Import verifies the request binding, canonical bytes, key IDs, and thresholds.
Reject duplicate signers, signatures for another transaction, altered metadata,
and unexpected role changes. Root replacement requires both the old and new
quorums. These remain TUF signatures, not a new network trust protocol.
[TUF root verification, section 5.3](https://theupdateframework.github.io/specification/latest/).

Freshness renewal must continue while an offline approval is being reviewed.
Reserve the relevant root/targets version and exact signed content separately
from the next online release; do not hold a release-number reservation across
human signing. After importing approval, use current snapshot/timestamp
versions and clock, bounded by that approval's fixed expiry. Reject superseded
membership or predecessor roots rather than silently rebasing a signed request.
The disposable code's coupled release/targets counters and apply-time signing
therefore need an explicit production journal design in slice 2.

Unattended renewal keeps one fenced publisher and the existing timestamp-last
publication order. Its role-scoped signer receives only snapshot/timestamp
requests. Replacing absolute same-host custody bindings with public signing
handoffs is part of this work; independent offline signers cannot depend on
mounting the live publisher home.

## Recovery and retirement

Back up the complete signing decisions and publication journal as well as keys.
A decryptable old key backup does not establish the latest allocated version.
Retain prepared, partially signed, and published transactions: valid signatures
may have escaped even when publication was not acknowledged. Archives need a
manifest of exact bytes and versions, bound to an independently retained latest
checkpoint. Checksums detect corruption; they do not prove freshness against
an attacker replacing the whole archive.

| Failure | Required operator path |
| --- | --- |
| Interrupted initialization or signing | Resume the existing transaction and exact allocations; recover missing material from its verified copies |
| One root signer lost or unavailable | Use the surviving quorum to authorize replacements; preserve the root chain and verify new backups before retirement |
| Two root signers lost, recoverable copies intact | Restore enough current signers and their decision histories into clean environments, then rotate if exposure is possible |
| Membership key lost or compromised | Use the root quorum to revoke/replace it and explicitly approve safe membership; a compromise can have authorized hostile endpoints already |
| Online keys or publisher compromised | Fence the old publisher, recover trustworthy history, rotate both online keys through the root quorum, and resume on a clean host |
| Journal or signer checkpoint missing/stale | Recover the latest complete archives, including unpublished decisions; compare with independent checkpoints and served objects. Refuse signing if the high-water mark cannot be established |
| Root quorum compromised, or quorum and every recovery copy lost | Use an independently distributed new trust anchor and an explicit client recovery/release procedure; the compromised repository cannot authorize its own replacement |

The last boundary follows the
[TUF compromise guidance](https://theupdateframework.io/docs/faq/).
Recovery cannot undo an attack already accepted by a client.

Before restoring a publisher, stop and disable the old instance; a local file
lock does not fence a second host. Restore to a new working location, verify
the known authority and complete history, and resume or repair forward. Never
roll counters back, overwrite numbered public metadata, or infer unused
versions solely from an HTTP 404. The first implementation requires the latest
archive; automatic reconstruction of lost journals is a later feature.

Retire replaced private keys only after verified publication and recorded
adoption, retaining the public root chain permanently. Inventory encrypted
backups and recovery secrets too: deleting one working file is not destruction
of every copy. Old backups must not become an automatic fallback signer.

## Implementation slices and evidence

The existing [authority verifier](../internal/omega/authority.go) derives role
identities from the six-key private record, and
[release signing](../internal/omega/release.go) accepts encoded private keys
directly. The first slice replaces those assumptions for production while
preserving the existing metadata validation and disposable behavior.

1. **Public authority model and encrypted single-key backend.** Introduce the
   schema-2 public record and role-scoped signing interface; separate inspection
   from key access. Implement one-key creation, unlock, and restored-copy
   verification. Resolve first-write recovery and library/resource limits here.
2. **Portable signing and recovery archives.** Implement reviewed requests,
   signature collection, durable signer decisions, and verified archive restore.
   Separate offline approval reservations from continuing online renewal.
   Exercise a coordinator with no private keys and disjoint signer homes.
3. **Atomic production initialization.** Connect key descriptors, all recovery
   checks, frozen signatures, and complete archival evidence to exclusive
   activation. Rehearse interruption, concurrent retry, and expired preparation
   without creating a second identity.
4. **Complete operator lifecycle.** Carry the new backend through membership
   approval, all rotation roles, renewal provisioning, status, and recovery.
   Retire the standalone `omega` binary and its build/release consumers when the
   replacement is complete. Rehearse the recovery table before enabling real
   production initialization.

Use throwaway keys throughout implementation and rehearsal. Keep production
activation gated until these slices and the custody inventory are complete.
Hosting at `https://sopholeth.io/omega/`, the compiled bundle/release gate,
Windows public-client support, and peer/public-ingress blockers keep their
places in the launch plan; custody work does not satisfy those gates.

Follow the [test guidance](../CONTRIBUTING.md#development) from #229: use fast
in-memory policy/signature cases, test shared file transactions once, and add
only the new custody boundaries. Cover wrong unlock secrets, tampered/truncated
files, mixed identities, duplicate signatures, conflicting same-version
requests, backup-only restoration, and refusal of stale/unknown history.
Keep one end-to-end ceremony with real files and a process interruption, then
one disposable recovery rehearsal across the distinct custody failures.
Do not multiply the existing publisher matrix by every signer and backend.
