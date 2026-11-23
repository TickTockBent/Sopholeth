# Public test network: validation and next steps

Kraid, Ridley, and Motherbrain are running in the `default` enclave. The
2026-09-29 [live validation](public-network-validation.md) covers verified
public discovery, anonymous operations, cross-root replication, payload/TTL
limits, expiration, and an unlisted fourth node. Streaming is enabled on all
three roots, and [soph.stream](https://soph.stream/) watches them by default.

Use the [bring-up runbook](public-network-bringup.md) for deployment and service
commands, the [authority record](public-network-authority.md) for the initial
fingerprint and deployed artifacts, and [omega operations](omega-operations.md)
for signing, renewal, rotation, and recovery. The
[open issues](https://github.com/TickTockBent/Sopholeth/issues) track current
findings; avoid maintaining a second dated issue inventory in the repository.

## Contract and scope

The [core principles](core-principles.md) constrain the work:

- Any compatible node can bootstrap, join, and gossip without approval. Three
  roots are entry points, not a membership limit.
- Writes are anonymous. There are no client accounts, key owners, write
  credentials, or required payload signatures.
- Omega authenticates the bootstrap directory and its updates. It does not
  authorize ordinary peers or writes. Its signing threshold is unrelated to
  replication acknowledgments.
- Peer IDs are protocol handles. Observed confirmations do not establish
  honest operators, independent copies, or consensus.
- Values stay opaque. TTL starts at local acceptance; restarts lose local
  payloads, and reconnecting does not backfill missed writes.

This is a Linux standalone-node test network, with HTTP gossip and bounded SSE.
WebSocket ingress and public metrics are excluded. Cloudflare is a shared
public-ingress dependency; Motherbrain also depends on workstation uptime.
The connected-host omega custody profile is already implemented. Further
custody ceremony, MCP, and dashboard deployment are outside this work block.

## Next work

1. **Observe unattended renewal.** Kraid's hourly timer is armed; daily refresh
   uses seven-day timestamp/snapshot validity. The first service-driven renewal
   passed. Record the first timer-driven due renewal and check failure/retry
   reporting. Use `soph omega status --verify` for current deadlines.
2. **Exercise failures on the running network.** Stop/restart one root, introduce
   a slow peer, and test partitions, repeated/missing ACKs, capacity pressure,
   and recovery. Record source versions, configuration, workload, fault timing,
   outcomes, and resource use under #180 using #183's data contract. The guest
   join used public HTTPS bootstrap and a Tailscale return route; repeat with
   an independently reachable public guest origin.
3. **Fix what the runs expose.** Prioritize problems that prevent joining,
   healthy replication, or useful testing. Use small reproductions and scoped
   fixes; the table below identifies the existing queue.
4. **Simplify setup, then rebuild.** Turn the validated operator procedure into
   a few scripts that prompt for passphrases, verify results, and save backups
   and hashes. After that work, deliberately tear down and rebuild the testnet.
   A reset requires an explicit new trust adoption; do not silently replace an
   existing authority or its rollback history.

The healthy-network milestone is complete. These tests do not require clearing
the whole public-alpha backlog first. Record actual peer views and thresholds:
a `201` is an observed quorum result, not a promise of three independent copies.

## Repair queue

| Area | Remaining work |
| --- | --- |
| Peer bookkeeping (#211, #213) | Bound referrals, correlate liveness, and remove ghost records. Signed-root and established-peer route replacement is already rejected. |
| Write outcomes (#170) | Deadline reporting. Unique write IDs (#166), per-sender ACK counting against a fixed target (#164) and quorum-of-one forwarding (#215) are done. |
| Shutdown (#151) | Complete orderly stop/restart and verify writes after rejoining. |
| Storage and listing (#162, #217, #220, #280) | Expired-entry accounting, entry overhead, and bounded listing work, with tests that catch requests stalled behind the store lock. The ordered-index change from #248 was reverted in #255; a second attempt (#279) had changes requested. |
| Public resource handling (#218, #219, #221, #222) | Stream subscriber policy, HTTP deadlines/connections, rate-limiter state, and diagnostic exposure. Deployment limits do not close these issues. |
| Payload transport (#214, #246) | Keep configurable value caps within the gossip envelope; evaluate raw-byte peer values for the next iteration. |
| Larger meshes (#165) | Asymmetric eviction. Keeping the newest IDs in a full dedup cache (#168) and bounding re-propagation of late copies (#152) are done. |
| Reset bundles (#231) | Explicit bundle overrides. Omega polish (#234–#236) is done. |
| Release distribution (#224) | Verifiable artifacts before a general release. Gated publishing (#223) is done. |

The mixed audits (#177/#178) retain residual findings. Native Windows public
clients (#198), dashboard/WSS discovery (#194), and WebSocket lifecycle work
remain separate. Apply each finding to the paths being exercised without adding
controlled admission or writer identity.

## Platform and viewer follow-ups

Windows public discovery needs native locking, account/ACL and path validation,
durable replacement, and native recovery/rotation/rollback tests. Keep shared
trust verification; no unlocked or memory-only fallback. Windows operator
custody is a separate concern.

The [stream plan](soph-stream-plan.md) covers viewer behavior and remaining
interaction tests. Follow the [roadmap](roadmap.md) for broader public-alpha
work, the demo application, and simulation. Keep bulky test outputs outside
Git; retain concise reproduction steps and conclusions with the relevant issue.
