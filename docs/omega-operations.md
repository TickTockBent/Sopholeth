# Sopholeth omega operations

Omega signs the public network's bootstrap root list. The signing tool exists,
but the production trust anchor and Sopholeth DNS cutover are still pending.
This is the operator workflow for that launch; the examples do not imply a
live public service.

Read the [discovery contract](discovery.md) before deploying roots. Keep
production signing keys separate from node hosts and test keys.

## Build the operator tool

From the checkout:

```bash
go build -o bin/soph-omega ./cmd/repram-omega
```

This gives the existing tool its intended local command name. Its built-in
help and publication hints still mention the old project and domain.
Use **HTTP ports** in root addresses even where legacy help says gossip port.

## Establish the trust anchor

On the offline signing machine, using a private working directory:

```bash
umask 077
./bin/soph-omega keygen \
  --out-private omega-v1.key \
  --out-public omega-v1.pub
```

The tool refuses to overwrite existing files and creates the private key
with mode `0600`. Retain the private key and a recoverable offline backup.
Copy only the public key into the release's
[trust anchor](../internal/trust/omega.go), replacing the placeholder.
Record the public key, release revision, and operator custody procedure.

No key generation or trust-anchor change is part of the documentation rebrand.
Disposable lab keys must never become the public network's trust anchor.

## Sign and publish

The following addresses are placeholders. Replace them with the deployed
roots' advertised hostnames and HTTP ports:

```bash
./bin/soph-omega sign \
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

**Prerequisite:** domain ownership and a release configured to query
`_bootstrap.sopholeth.io`. Current source still queries the old name, so
publishing these records alone will not migrate it.

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

The current environment variable spellings are in
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
