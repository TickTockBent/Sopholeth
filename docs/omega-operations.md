# Sopholeth omega operations

`soph omega init`, `publish`, and `status` support disposable TUF authorities
and verified publication to a local HTTPS-served repository on Linux. Online
renewal, rotation, production custody/hosting, and migration of discovery
consumers remain the next steps in the
[public-network plan](public-network-plan.md). No production authority exists.

## Initialize a disposable authority

Build the existing CLI, then choose **one private custody home** for all
operator work. Its parent must exist and be protected from replacement by
other users. The home is created with mode `0700` if absent.

```bash
make build-soph
./bin/soph omega init \
  --home /path/to/private/omega-home \
  --network rehearsal \
  --repository https://metadata.example.invalid \
  --disposable
./bin/soph --json omega status \
  --home /path/to/private/omega-home --network rehearsal
```

`--network` here identifies the authority, not a saved client profile. It goes
after `omega init` or `omega status`. IDs contain 1–64 ASCII letters, digits,
underscores or hyphens, beginning with a letter or digit, and are case-sensitive.
The repository currently accepts HTTPS origins only. The planned
`sopholeth.io/omega/` base-path and hosting support is separate future work.
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
keys and the fixed intent are committed together in `authority.json` before
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
production ceremony must establish the canonical registry and independent
custody before this command can create production material.

### Files, custody, and status

A committed network directory contains exactly these files, all mode `0600`:

| File | Purpose |
| --- | --- |
| `authority.json` | **Private** initialization record containing all six keys and fixed intent. |
| `1.root.json` | Verified, signed public initial TUF root. |
| `bundle.json` | Public network/repository/initial-root bundle for the client. |
| `complete.json` | Integrity receipt binding the committed files and root fingerprint. |

Back up the private custody home using protected storage and verify a restored
copy with `omega status`. Only the root and bundle are public handoff artifacts;
never copy the whole home to a site, repository, node host, or logs. The receipt
checks consistency; it is not protection against someone who controls the
operator account and can replace all custody material.

This first implementation deliberately requires `--disposable`: all six keys
are stored together for development. It does not claim independent root-key
custody or create independent recovery copies. Production initialization must
wait for the custody/recovery workflow and rehearsal. `authority.json` schema 1
is permanently reserved for disposable authorities: production custody requires
a new schema, not an extension that enables production use of schema 1.
Windows public-client support remains a
[separate required gate](public-network-plan.md#windows-public-client-gate).

`status` verifies local private/public consistency and the completion receipt.
Text and `--json` output include state, network, repository, initial-root
fingerprint/version/expiration, and role thresholds/key IDs. Missing, pending,
corrupt, or expired authority returns a nonzero exit with a corrective action.
Unknown expiration dates are omitted. No output contains private keys.

## Publish a disposable three-root manifest

Prepare a dedicated directory to be served at the authority bundle's exact
HTTPS repository origin. Its parent must exist, and it must be outside the
custody home; neither directory tree may contain the other. The publisher
creates the final directory if absent. A first publication requires an empty
directory. Existing site files cannot be adopted as a repository.

This first backend writes to a **local filesystem directory**. The HTTPS
server is a separate, already configured service; `publish` does not start it,
upload through an HTTP API, configure Vercel, or deploy root nodes. Verification
uses normal TLS certificate/hostname checks and refuses redirects. A private
rehearsal CA can be supplied through the platform's trusted CA configuration
(for example `SSL_CERT_FILE` on Linux); there is no insecure TLS flag. Hosting
at `sopholeth.io/omega/` still needs the planned base-path and serving work.

Create the approved manifest, for example `bootstrap.json`:

```json
{
  "schema": 1,
  "network": "rehearsal",
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
./bin/soph --timeout 2m omega publish \
  --home /path/to/private/omega-home --network rehearsal \
  --manifest bootstrap.json --repository-dir /path/to/public/omega-repository \
  --version 1 --disposable
./bin/soph --json --timeout 2m omega status \
  --home /path/to/private/omega-home --network rehearsal --verify
```

Version 1 starts publication. Repeat the **same version and manifest** to
recover or reverify it. Use the next consecutive version for a new approval;
versions cannot be skipped or decreased. Reusing a version with different
membership is an error. Formatting and equivalent origin spellings are
normalized before the approval is recorded; root-array order is preserved.
The explicit version makes retries unambiguous, including after the command
commits but loses its terminal output.

Each approval signs targets, snapshot, and timestamp metadata with their
respective keys; it copies the existing signed root without using root keys
to sign again. Proposed lifetimes are 90 days, 7 days, and 24 hours, capped by
root expiration. Signing requires at least 24 hours of remaining root validity.
This is a manual approval path with disposable custody. Unattended renewal
using only separately provisioned online keys is the next slice.

### Publication journal and write order

The authority directory stays immutable. Publication state lives separately
at `<home>/<network>.publication` (mode `0700`):

- `binding.json` binds the journal to the authority fingerprint, network, and
  canonical local repository directory.
- `N.release.json` contains the exact public signed bytes, canonical manifest,
  creation time, and the preceding release's digest. These immutable records
  form the durable version history. They contain no private keys.
- `N.verification.json` records the last check/attempt, its failure if any,
  and the most recent successful check time for that release.
- `.verify-*` directories are disposable client-verification scratch. They
  are removed on completion or the next verification after process death.

The network lock serializes operator work; a separate destination lock prevents
concurrent publishers from racing the mutable timestamp. The entire signed
release is durably recorded before any public object is written. There are
currently at most 10,000 sequential releases per disposable journal; there is
no history compaction or reset command.

Public files are installed in this order:

1. `1.root.json`, retained unchanged.
2. `targets/<sha256>.bootstrap.json`, the content-addressed manifest.
3. `N.targets.json` and `N.snapshot.json`.
4. `timestamp.json`, atomically replaced only after the immutable objects are
   durable. It can replace only an exact known older timestamp or retry the
   current one.

Immutable filenames are never overwritten with different bytes. Public data
files use mode `0644`; directory access for the HTTPS serving account must be
configured by the operator. The destination also contains a private, empty
`.omega-publish.lock`; temporary `.pending` public files may survive a killed
process. Publishing again sweeps the journal and public repository, including
`targets/` and older releases, removing `.pending` twins only when the final
object exists with identical bytes. Pending data without a matching final
object is left for normal recovery or investigation. Serve only the
documented metadata/target paths and disable directory listing. Never serve the
custody home.

A retry uses the recorded signatures and expiration; it does not re-sign the
same version with a new clock. Interrupted writes before the timestamp leave
the previously advertised release in place. Interruption after the timestamp
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
roles must match the prepared release exactly. An additional fetch checks
`1.root.json`, which a freshly bundled client normally does not download.
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

## Legacy DNS tooling reference

The remaining sections describe the interim standalone `omega` tool and
current DNS discovery consumers, which are still present until the TUF
publishing and consumer migration ships. Do not use this legacy workflow for
the first public network. The [signing audit](omega-signing-audit.md) records
its known defects; the [trust design](omega-trust-design.md) defines its replacement.

## Build the operator tool

From the checkout:

```bash
go build -o bin/omega ./cmd/omega
```

The operator tool is `omega`. Use **HTTP ports** in root addresses.
Its publication hints share the discovery defaults used by the node.

## Establish the trust anchor

On the offline signing machine, using a private working directory:

```bash
umask 077
./bin/omega keygen \
  --out-private omega-v1.key \
  --out-public omega-v1.pub
```

The tool checks for existing output paths and creates the private key with
mode `0600`, but the audit found a concurrent overwrite race and partial-pair
failure. These must be resolved in `soph omega init` before production use.
For disposable development keys, retain the private key and a recoverable backup.
Copy only the public key into the release's
[trust anchor](../internal/trust/omega.go), replacing its empty value.
Record the public key, release revision, and operator custody procedure.

No production key generation or trust-anchor change is part of the rebrand.
Disposable lab keys must never become the public network's trust anchor.

## Public release validation

Ordinary builds leave the authority unset and reject public discovery before
consulting DNS or cached roots. The retired all-zero placeholder is rejected
even when supplied explicitly. Private-network development does not require
an authority.

After the planned authority ceremony, a public release must match an
independently recorded fingerprint: SHA-256 of the decoded 32-byte public
key, represented as 64 hexadecimal digits. Validate the release checkout:

```bash
OMEGA_EXPECTED_SHA256='paste-the-reviewed-64-hex-digit-fingerprint' make check-public-release
```

The check rejects a missing or malformed expected fingerprint, an
unconfigured/malformed anchor, or a fingerprint mismatch. Normal development
tests skip this release-only check unless the variable is present. The Docker
workflow requires it before publishing any `v*` tag, using the repository
variable `OMEGA_EXPECTED_SHA256`. Main and manual branch builds continue to
support private development. The expected value must come from the authority
record, not be calculated from the checkout being verified merely to make the
check pass.

This validates the current anchor, not public-network readiness. The trust
design and `soph omega` lifecycle still precede any production key ceremony.

## Sign and publish

The following addresses are placeholders. Replace them with the deployed
roots' advertised hostnames and HTTP ports:

```bash
./bin/omega sign \
  --key omega-v1.key \
  --version omega-v1 \
  --expires-in 24h \
  --nodes root-a.example:8080,root-b.example:8080 \
  > omega-record.txt
```

`--expires-in` takes a Go duration such as `24h` or `30m`, not a bare
number of seconds. Standard output contains the signed TXT value; explanatory
output goes to stderr. Transfer the signed record to the DNS publisher,
keeping the private key offline.

For the intended launch namespace, publish the signed record at
`_omega.sopholeth.io` and this pointer at `_bootstrap.sopholeth.io`:

```text
omega=_omega.sopholeth.io
```

**Prerequisite:** domain ownership and a release containing the real public
key. Current source queries `_bootstrap.sopholeth.io`; its trust anchor is
unset and public discovery is disabled.

After publication, inspect both records:

```bash
dig +short TXT _bootstrap.sopholeth.io
dig +short TXT _omega.sopholeth.io
```

DNS visibility is only the first check. Start the intended release with an
empty cache and verify signature acceptance, the chosen seeds, and root
status. Choose a publication schedule with enough margin before expiration
for DNS propagation and operator recovery. DNS TTL and the signed `exp`
are separate clocks.

## Root configuration

- Use public mode and leave manual peers unset so verified discovery runs.
- Make each root's advertised `address:httpPort` exactly match its signed
  entry. The address must be reachable from intended participants.
- Set a writable cache directory, explicit payload capacity, and appropriate
  transport exposure.
- Enable inbound WebSocket attachments on roots intended to serve as
  substrates.
- Observe discovery refresh, peer reachability, storage usage, and quorum
  outcomes. A healthy HTTP response alone does not validate the network.

Environment variables are documented in
[node configuration](configuration.md). Changing the root set requires
signing and publishing a new list; nodes adopt it on refresh.

## Renewal, rotation, and recovery

For routine renewal, sign a fresh list with the same trusted key before the
previous list expires. For a root-set change, remember that cached or replayed
older lists remain valid until their signed expiration.

A new trust key or signed format requires a release plan. Current clients
trust one compiled key and version. Define the discovery path, overlap,
upgrade requirements, and retirement behavior before changing either.
Parallel TXT records behind one shared pointer are not sufficient for
automatic version selection.

If signing is unavailable, an already signed list remains usable only until
its expiration. Fresh startup has no valid discovery source once both DNS and
cache are expired. Existing processes may continue peer traffic, but the
[current running-node expiration gap](discovery.md#refresh-and-current-limits)
must be resolved before relying on a safe public cutover. Restore the signer
from its offline recovery procedure and publish a fresh record.

The old dnsmasq signing loop is a lab helper with online test keys and
host-service side effects. It is not the production signing workflow.
