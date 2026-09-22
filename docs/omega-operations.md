# Sopholeth omega operations

Omega signs the public network's bootstrap root list. This document describes
the interim standalone tool for development/reference. Production authority
setup and DNS cutover remain pending; use the
[public-network plan](public-network-plan.md) for the agreed path to a working
`soph omega` suite and three public roots. Replace this reference with the
rehearsed production runbook as that implementation ships.

The [2026-09-22 signing audit](omega-signing-audit.md) records implementation
findings and their tracking issues. The planned production workflow moves
operator commands under `soph omega` and defines automated renewal and
graceful key rotation. The standalone commands below describe the current
interim tool; the redesign has not been implemented.

Read the [discovery contract](discovery.md) before deploying roots. Keep
production signing keys separate from node hosts and test keys.

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
