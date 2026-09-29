# Public test-network authority

The operator initialized this authority on Kraid with production `age-scrypt`
custody. Its public bundle was retrieved separately and checked against the
initial fingerprint supplied in the operator's initialization report.

| Field | Value |
| --- | --- |
| Network | `sopholeth` |
| Repository | `https://sopholeth.io/omega` |
| Initial root version | `1` |
| Initial root expiry | `2027-09-27T00:09:22Z` |
| Initial root SHA-256 | `8e3285517366aa2ec80366213ccde26bda0ed577679242ebcebdeef8748b0c82` |
| Root threshold | 2 of 3 |
| Operator custody | Kraid, `/srv/omega-offline/sopholeth` |
| Public bundle | [`internal/discovery/bundle.json`](../internal/discovery/bundle.json) |

This is the initial trust identity; normal signed rotations retain its bundle
and fingerprint. The operator keeps an independent copy of the fingerprint.
Only the public bundle is adopted into source. Signing keys and custody homes
remain outside the repository.

Activation status:

- Authority initialization: complete; all six encrypted key files are present.
- Public bundle: signature/fingerprint validation passed against the operator report.
- Backup restoration: all six active keys verified from a separate restored
  home, with the same fingerprint. The encrypted operator archive is retained
  off Kraid with private permissions.
- Renewal provisioning: complete at `/srv/omega-online`, owned by `soph-omega`.
- First publication: release 1 verified over HTTPS by the publisher and again
  as `soph-omega`. A fresh trust client on Motherbrain independently verified
  the signed metadata and approved three-root manifest.
- Post-publication backup: complete before renewal, covering release 1 and all
  three homes. The operator verified archive decryption; the encrypted copy on
  Motherbrain matches Kraid's SHA-256 and has private permissions.
- First production freshness renewal: release 2 verified by the service on
  2026-09-29. The production Vercel drop-in is installed and the hourly timer is
  enabled and active. The disposable rehearsal timer remains disabled.
- Public ingress: all three roots return healthy public HTTPS responses through
  their own tunnels, with valid TLS and the expected IDs/network/enclave.
- Root deployment: all three report `official root=true` from verified discovery
  and list both other roots at their signed HTTPS origins. Fresh public
  `soph join`, anonymous cross-root operations, admission limits, five-minute
  expiry, and an unlisted fourth node passed [live validation](public-network-validation.md).

## First publication

At first publication, all four metadata roles were at version 1. The approved
manifest lists Kraid, Ridley, and Motherbrain at their
`https://<node-id>.sopholeth.io` origins in the `default` enclave.

| Checkpoint | UTC |
| --- | --- |
| Publication verified by publisher | `2026-09-27T00:28:14Z` |
| Next renewal due | `2026-09-28T00:26:46Z` |
| Initial timestamp/snapshot expiry | `2026-10-04T00:26:46Z` |
| Initial targets approval expiry | `2026-12-26T00:26:46Z` |

These are the first release's deadlines; use `soph omega status --verify` for
current versions and expiration dates. Public fetches returned direct HTTPS
200 responses. `timestamp.json` has `Cache-Control: no-store`; `1.root.json`
has `public, max-age=31536000, immutable`. The fresh trust client verified
root, targets, snapshot, timestamp, and the hashed `bootstrap.json` target
using the adopted bundle, without a preexisting cache or TLS bypass.

## First production renewal

The operator started `soph-omega-renewal@sopholeth.service` on 2026-09-29. It
published and verified release 2 as `soph-omega`; root and targets remain at
version 1, while timestamp and snapshot are now version 2.

| Checkpoint | UTC |
| --- | --- |
| Renewal verified by service | `2026-09-29T11:51:03Z` |
| Next renewal due | `2026-09-30T11:50:53Z` |
| Timestamp/snapshot expiry | `2026-10-06T11:50:53Z` |

The timer was enabled afterward and its active/enabled state confirmed on Kraid.
It checks hourly, verifies without deploying before renewal is due, and refreshes
daily with seven-day validity. A failed renewal is retried on the next hourly
run. Record the first timer-driven due renewal when it occurs.

## Next operator work

Authority adoption is merged in #249 (`03ff9b2`), and all three roots are running.
The healthy-network checks passed. Continue testing the running network, observe
the first timer-driven due renewal, and use the setup findings to simplify the
operator process before deliberately tearing down and rebuilding. The
[validation record](public-network-validation.md) distinguishes completed checks
from remaining fault and transport tests.

## Deployed artifacts

All three roots started on 2026-09-29 from the same reviewed source. Their
running image IDs and CLI checksums were checked on each host. Public health
returns the correct ID/network/enclave with `Cache-Control: no-store`, and each
topology lists both other roots at their signed HTTPS origins. The topology
`role: transient` describes the separate WebSocket attachment role; the discovery
startup logs confirm each node's official root role.

| Kraid artifact | Value |
| --- | --- |
| Source commit | `03ff9b2287d0d9e36893a05ceb7530a9860bdf70` |
| Node image | `sha256:971c642c0fd86874d39a84e581c6372f1aceea80f59c6ffdb83c7af3845d93f4` |
| Installed CLI SHA-256 | `8bd0ffd4c797bbfde7544c30c01645e704f1cdfd2225c503a4b75450ea010be9` |

Ridley's running image and release record were checked on the host; local and
public health passed. Its operator script saves the artifact details in
`~/.local/share/sopholeth/root/release.txt`.

| Ridley artifact | Value |
| --- | --- |
| Source commit | `03ff9b2287d0d9e36893a05ceb7530a9860bdf70` |
| Node image | `sha256:983a3b1ae43a8100566ff75afe8888255e47b0c5e72f7a6dd8d0dba4899e61f4` |
| Built CLI SHA-256 | `8bd0ffd4c797bbfde7544c30c01645e704f1cdfd2225c503a4b75450ea010be9` |

Motherbrain joined on 2026-09-29. All three roots now list both others at their
signed HTTPS origins. Its operator script built an archive of the same reviewed
source, outside the working checkout, and saved its release record and CLI under
`~/.local/share/sopholeth/root/`. That CLI used `-buildvcs=false`; its checksum
differs from the checkout builds above. Local image IDs are recorded per host.

| Motherbrain artifact | Value |
| --- | --- |
| Source commit | `03ff9b2287d0d9e36893a05ceb7530a9860bdf70` |
| Node image | `sha256:86c7c4cac62c6b876281db9030cc49102e38a5e8f08c0b55a558f978265c36df` |
| Built CLI SHA-256 | `23dd4337215481bffb8492fddf5890e7708782b71c5fcc983afaef6f9891731e` |

All three use ingress image
`nginx@sha256:985220252f3863977e468f611ef118ebd01421289dd86ee1ae99cb068c3bce2b`.

## Repeat the full backup

Take a new backup of all three homes on Kraid with the renewal timer stopped and
no other omega commands running. Wait for an active publication to finish before
copying. The operational home contains
unencrypted renewal keys as well as the current publication journal, so encrypt
the complete archive. GnuPG is already installed on Kraid. The following Bash
commands stream directly into an encrypted file, then check that the archive
decrypts using a freshly entered passphrase. No plaintext archive is written.

```bash
(
  set -euo pipefail
  umask 077
  sudo -v
  backup_dir=$(mktemp -d "$HOME/sopholeth-omega-backup.XXXXXX")
  sudo tar -C /srv -cpf - omega-offline omega-online omega-public |
    gpg --pinentry-mode loopback --symmetric --cipher-algo AES256 \
      --output "$backup_dir/omega-full.tar.gpg"
  gpg --no-symkey-cache --pinentry-mode loopback \
    --decrypt "$backup_dir/omega-full.tar.gpg" | tar -tf - >/dev/null
  printf 'Verified encrypted backup: %s\n' "$backup_dir/omega-full.tar.gpg"
)
```

Save the printed `.gpg` file off Kraid, keeping its passphrase recoverable
separately, then resume the production renewal timer.
The archive includes the earlier rehearsal's files too; its timer stays disabled.
Preserve ownership and the original `/srv/omega-offline`, `/srv/omega-online`,
and `/srv/omega-public` paths on later restoration, following
[omega operations](omega-operations.md#verify-a-restored-copy). The archive check
above verifies decryption and readability; it is not a second full service-restore
rehearsal. Keep this backup current after renewals and rotations.

The encryption/decryption command was exercised with disposable data and a
temporary GnuPG home. See GnuPG's [symmetric encryption command](https://www.gnupg.org/documentation/manuals/gnupg/Operational-GPG-Commands.html)
and [terminal passphrase options](https://www.gnupg.org/documentation/manuals/gnupg/GPG-Esoteric-Options.html).
