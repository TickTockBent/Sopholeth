# soph.stream: current behavior and remaining work

The public viewer is live and streaming from Kraid, Ridley, and Motherbrain.
The same assets ship in `soph serve`. The original simulated mockup and discovery
feature list have been superseded by the implementation and this plan.

## Current behavior

- Keep the Sopholeth aesthetic and emphasize payloads in a responsive card
  grid. Overwrites update the existing card; local expiration removes it.
- The node's `GET /v1/stream` sends a local snapshot, accepted writes, advisory
  expiry events, and clock updates. Previews are limited to 4 KiB; detail reads
  fetch the current full value, which may differ from an earlier preview.
- The viewer is read-only. Payloads render as text or a binary preview, never
  executable markup. There is no payload history, browser persistence, or
  analytics. Node selection and text search are reflected in the URL.
- The selected node, enclave, and connection state remain visible. The page
  represents that node's local view, including its actual TTLs; counts and
  empty states are not network-wide claims.
- The static site selects a root from `config.json` and tries the other public
  roots on stream failure. Each connection starts with a fresh snapshot.
  An explicit `?node=` selects only that endpoint.
- `soph join` retains named networks. Verified public profiles follow the omega
  discovery rules, and `soph serve` refreshes that profile while running.
  The viewer server forwards reads through its own origin so HTTPS port
  forwarding works. It does not retry a client PUT.
- Subscriber queues and snapshot size are bounded. Slow viewers disconnect;
  writes do not wait for a viewer. `NODE_STREAM=off` disables the endpoint.
  The current root ingress allows two streams per visitor; each node allows
  eight total. Snapshot growth remains an open limitation under #218.

See the [API stream contract](api.md), [CLI guide](cli.md#local-stream-viewer),
[shared asset setup](../sites/README.md#local-preview), and
[browser test guide](../test/stream/README.md) for details and runnable checks.
The [live validation record](public-network-validation.md) and
[bring-up runbook](public-network-bringup.md#check-the-live-network-and-operate-it)
record the deployed path.

## Next validation

Use the running testnet and disposable local clusters. This work accompanies
the [network tests](public-network-plan.md#next-work); it is not a new launch gate.

1. Exercise node shutdown/restart, browser disconnect, and public-root failover.
   Verify the displayed node changes and the old view is replaced by a fresh
   snapshot. A partition healing does not imply payload backfill.
2. Compare write latency and resource use with no viewers, normal viewers, and
   slow/disconnected viewers. Reproduce and fix snapshot-growth limits (#218).
3. Exercise empty/binary values, preview truncation, overwrite/expiry during
   detail fetch, bursts, overflow, mobile layout, keyboard use, and reduced
   motion. Retain focused automated coverage of ordering and reconnects.
4. Verify both the static site and `soph serve` through their intended HTTPS
   paths: CORS, stream flushing, proxy buffering, keepalives, and idle timeouts.

Record commit, configuration, viewer count, workload, faults, and observed
limits with the relevant issue. Preserve local TTL semantics and do not log
payload contents as operational telemetry.

## Optional product work

Sorting, theme selection, and broader same-enclave peer failover remain product
options. If added, keep query semantics consistent across the site and
`--q`, preserve stable card positions where possible, and rebuild the local view
on every node change. General peer referrals must not become verified public
roots simply because a node advertised them.

The separate conversation application and probe simulation remain in the
[roadmap](roadmap.md). Neither requires turning this viewer into a writer or
adding application identity to the node.
