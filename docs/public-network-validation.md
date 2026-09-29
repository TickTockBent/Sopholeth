# First public test-network validation

The three-root network was brought up on 2026-09-29 using reviewed source
`03ff9b2287d0d9e36893a05ceb7530a9860bdf70` and the adopted public authority.
Kraid, Ridley, and Motherbrain expose their signed HTTPS origins through their
own Cloudflare tunnels. See the [authority and artifact record](public-network-authority.md)
for image IDs, CLI hashes, and renewal checkpoints.

## Live results

The main live checks ran from 13:06 to 13:13 UTC on 2026-09-29. They used
fresh CLI profiles, normal TLS verification without redirect
following, unique synthetic keys, and a small sequential workload. Sixteen
accepted probe keys used the five-minute TTL. The ten-year peer-TTL probes were
immediately overwritten with five-minute values after inspecting the clamp.

| Check | Result |
| --- | --- |
| Public health and topology | All three roots report the expected ID, public network, and default enclave; every root sees both others at their signed HTTPS origins. |
| Verified public CLI discovery | A fresh `soph join` discovers the network from the embedded bundle and HTTPS metadata. |
| Anonymous put/get/list | Confirmed CLI writes through each root are readable and listed through the other two. |
| Overwrite and empty values | Another root can overwrite an existing key; empty values are accepted and replicated. |
| Maximum accepted value and key | A 102400-byte value with a 1024-byte key replicates from every root to both others, with known-length and chunked HTTP uploads. |
| Rejected value and key | Client and peer writes with 102401-byte values or 1025-byte keys return 413 and remain absent from all roots. |
| Ingress envelopes | The separate 128 KiB client and 192 KiB gossip boundaries behave as configured, including chunked uploads. |
| Peer TTL bounds | Ten-year TTLs store as 86400 seconds; negative TTLs store as 300 seconds on all roots. |
| Expiration | All 16 probe keys disappear from reads and listing on all three roots after five minutes; CLI reads return the missing-value exit code, 3. |
| Cache and optional routes | Reads carry `Cache-Control: no-store`; WebSocket, stream, metrics, and unknown routes return 404. |
| Direct ingress reachability | Kraid/Ridley port 18080 is unreachable at their public and Tailscale addresses from Motherbrain; Motherbrain's Tailscale port 18080 is unreachable from Kraid. |

## Unlisted fourth node

A temporary node, `soph-live-guest-5cf50e58`, started with the same public bundle,
`NODE_NETWORK=public`, empty manual seeds, and the default enclave. Its return
route was `http://100.86.194.72:18081`, bound to Motherbrain's Tailscale interface.
It used an isolated Docker project, temporary trust-state directory, and an
8 MiB payload capacity.

The guest discovered all three public roots, and every root accepted its
unsigned route without a manifest update or membership credential. A guest
write was confirmed and readable from every root. A Kraid write was readable
from the guest and both other roots. The guest's own bootstrap endpoint returned
403, consistent with having no official root role.

The temporary containers, network, and state directory were removed after the
check. All three roots subsequently evicted the guest and returned to peer views
containing just the other two roots. Fourth-node return traffic used Tailscale;
a separate public guest endpoint was not exercised.

## Resource observation and follow-up

One Docker statistics sample after the small write workload reported:

| Host | Node memory / 512 MiB limit | Ingress memory / 96 MiB limit |
| --- | --- | --- |
| Kraid | 9.60 MiB | 2.44 MiB |
| Ridley | 9.75 MiB | 2.43 MiB |
| Motherbrain | 10.41 MiB | 4.14 MiB |

These are point-in-time observations. Saturation, slow peers, acknowledgement
faults, and stop/restart behavior remain follow-up testing. Motherbrain's known
Docker 27 same-L2 port-publishing caveat remains documented in the
[runbook](public-network-bringup.md#hosts-and-public-ingress).

Kraid's first due freshness renewal published and verified release 2. Its timer
checks hourly and renews daily with seven-day validity. The next due renewal is
2026-09-30 at 11:50:53 UTC; record the first timer-driven due renewal when it
occurs. No 24-hour wait is required for initial network testing.

The operator follow-up is to simplify startup into a few resumable scripts with
interactive passphrase prompts, verification, and saved backups/hashes, then
tear down and rebuild this test network using that process. The current roots
remain running for testing until that teardown is deliberately started.
