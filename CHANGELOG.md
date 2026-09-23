# Sopholeth changelog

## Unreleased

### Public discovery

- Add `soph omega init --encrypted --disposable` with a public-only schema-2
  authority and six separate age-encrypted key files. Recover interrupted
  initialization without replacing allocations, inspect public identity without
  passwords, and verify a restored backup with `status --check-keys`. Document
  terminal unlock, missing/damaged key reports, and backup recovery. Encrypted
  publication/rotation and production creation remain gated for the next slice.

- Renew omega snapshot/timestamp metadata daily with seven-day validity, retaining
  hourly scheduler checks/retries and root/membership expiry caps. Preserve older
  one-day releases and interrupted signing reservations byte-for-byte; their
  next new release adopts the longer window. Signing keys do not rotate on this
  schedule.

- Add `soph omega rotate --role root` for disposable Linux authorities, with
  old/new 2-of-3 signatures, offline signer generations, reviewed root expiry,
  and explicit `--renew-approval` of the unchanged membership. Recover expired
  roots, one missing rotated signer, and interrupted publication using retained
  history and a signed public handoff. Operator status and later approvals use
  the active root instead of rejecting an expired initial bundle. Production
  custody and independent backup/compromise recovery remain pending.

- Add `soph omega rotate --role targets` for disposable Linux membership-key
  rotation. Keep replacement private keys offline, preserve the approved root
  list and expiry, recover publication from a signed public handoff, and select
  the active membership key for later approvals. Test interrupted/killed
  processes, retired keys, alternating role generations, expiry repair, and
  recovery from a lost rotated key while approval remains valid. Root-quorum
  rotation, root-expiry recovery, and production custody remain pending.

- Add `soph omega rotate` for disposable Linux online-key rotation. Prepare and
  review replacement snapshot/timestamp keys, apply the exact successor-root
  digest, preserve the original trust bundle and approved membership, and verify
  the retained root chain over HTTPS. Recover interrupted application through
  the online renewal command, including higher-version expiry repair. Document
  adoption, retirement, and recovery limits; root/membership-key rotation and
  production custody remain pending.

- Add `soph omega provision-renewal` and scheduled `publish --renew` for disposable
  Linux authorities. Hand off the journal recoverably to a separate home with
  only snapshot/timestamp keys, preserve offline-approved membership, recover
  interrupted renewal without offline custody, and cap freshness at approval
  deadlines. Expose renewal timing and warnings in status; document scheduling,
  monitoring, and recovery with example systemd units. Production custody and
  root/membership-key rotation remain pending.

- Add `soph omega publish` and `status --verify` for disposable Linux authorities.
  Journal exact signed releases separately from authority material, install
  immutable public objects before the timestamp, and verify the served release
  through the durable HTTPS client. Preserve versions and bytes across retries,
  report publication failures and role deadlines, and retain history for recovery.
  The first backend writes a local directory served by separately configured
  HTTPS; production custody/hosting remain pending.
- Add `soph omega init` and local `status` (#208) for disposable Linux authorities.
  Verify and durably commit the complete authority atomically; retries recover
  the same transaction or inspect the existing keys. Report local identity,
  expiration, and actionable failures in text or JSON. Production custody,
  root/membership-key rotation and discovery integration remain pending.
- Add the TUF bootstrap client with validated public bundles/manifests,
  bounded HTTPS, durable rollback state, process locks, and expiring cached
  authority. Move disposable lifecycle tests into the application suite and
  retire the isolated spike. Node/CLI discovery integration remains pending.
  Upgrade builds, Docker builders, and CI to Go 1.27.1.
- Reject unset and all-zero omega trust anchors before DNS lookup or cached
  root authorization (#192). Ordinary builds have public discovery disabled;
  private networks, explicit dashboard seeds, and disposable test keys remain
  supported. Require an independently recorded authority fingerprint before
  publishing version-tagged node images.

### Live stream

- Add `/v1/stream`: atomic local snapshots followed by accepted writes and
  advisory expiration events, including replicated writes. Preview payloads
  are bounded to 4 KiB. Subscriber queues, snapshot sizes, connections, and
  network writes have explicit limits; `NODE_STREAM=off` disables the feed.
- Replace the soph.stream holding page with a live viewer in the existing
  terminal aesthetic: stable slots, overwrite feedback, local TTLs, preview
  filtering, full-value inspection, mobile layout, and fresh-snapshot reconnects.
- Add `soph serve` with named-network selection, explicit node overrides,
  bind/port controls, initial search, and browser opening. It embeds the same
  files as the standalone static site. Public deployment and peer failover
  remain later phases.

### Client

- Add the `soph` client CLI (#187): a direct HTTP client in `cmd/soph` with
  `join`, `use`, `networks`, `forget`, `put`, `get`, `exists`, `list`, and
  the read-only diagnostics. Joining validates a node's health and saves it
  as the current network; multiple named networks persist in
  `$XDG_CONFIG_HOME/sopholeth/soph.json` with no fallback between them.
  Public joining resolves signed discovery and reports clearly that it is
  unavailable until the trust anchor exists. Writes report confirmed versus
  pending quorum, with subsequent current-key TTL observations reported
  separately. Stable exit codes distinguish missing keys, unreachable nodes,
  and usage errors.
- Cancel blocked write input on interruption, reject invalid join health
  reports without changing saved profiles, and report output failures while
  preserving known write outcomes. Add command-specific help.
- Add `internal/client`, the reusable node API client the CLI is built on,
  and `internal/client/clienttest`, an in-memory node for tests.

### Websites

- Move the marketing site to `sites/sopholeth.com` and add starter sites for
  documentation (`sopholeth.io`), development updates (`sopholeth.dev`), and
  `soph.stream`. Each folder has its own static Vercel configuration and 404.
- Link the domain sites and document their independent deployment roots.
  Devlog automation remains future work.

### Build and deployment

- Deploy each Vercel site only when its own folder changes. Limit Go checks
  and node image builds to their source and build inputs, while retaining
  Docker release/manual builds. Cancel superseded PR runs and test the
  deployment filters in a separate fast workflow.

### Documentation and identity

- Record the omega signing audit and first public-network plan: implement
  the unified `soph omega` suite, then rehearse and activate three roots in
  the `default` enclave. Defer MCP and reconcile the viewer/roadmap sequence.
- Adopt **Sopholeth**, pronounced **SOF-oh-leth**, with the identity line
  **Wisdom through intentional forgetting**. Use `soph` for the client CLI;
  the node executable is `server`.
- Replace the README with a private-network quick start and local builds that
  work before artifact and repository renames.
- Consolidate the project overview and whitepaper into an architecture guide.
  Add focused API, configuration, discovery, and rebrand references.
- Correct retention, access-control, quorum, and coordination claims.
  Document known implementation limits as public-launch work.
- Focus the roadmap on public alpha, a demo web application, and probe simulation.
- Retire the obsolete host-specific burn-in runbook and TS deployment
  instructions. Preserve historical releases and experiment observations.

### Code and interfaces

- Rename the Go module to `sopholeth` and service commands to `server`,
  `omega`, and `dashboard`; update imports, containers, CI, and examples.
- Replace branded configuration with `NODE_*` and `DASHBOARD_STATE_DIR`,
  and give MCP tools plain names: `store`, `retrieve`, `exists`, `list_keys`.
- Use `gossip_*`, `http_*`, and `discovery_*` metrics with matching dashboard
  and burn-in consumers. Authenticate HTTP gossip with `X-Gossip-Signature`.
- Move default application state to Sopholeth directories and configure
  discovery names under `sopholeth.io`. No compatibility aliases or automatic
  state migration are provided.
- Rebrand the active site and dashboard, replace obsolete installation
  examples, and make burn-in targets and output directories configurable.

Published artifacts, repository hosting, DNS, and running infrastructure are
separate launch steps. See the [naming guide](docs/rebrand.md).

### Existing implementation awaiting release

The implementation work recorded before this documentation pass includes:

- Go-native MCP over stdio with an embedded node, replacing the TypeScript
  implementation.
- Restored substrate/transient WebSocket attachments in Go, with relayed
  acknowledgements, enclave filtering, and reconnection.
- HTTP key listing with pagination, enclave gossip, resource limits, and
  signed public discovery.

The restored Go tree still needs sustained multi-substrate validation, and
the public trust anchor remains unset. See the
[roadmap](docs/roadmap.md) for launch gates.

## Earlier history

The [pre-rebrand changelog](docs/archive/repram-changelog.md) preserves the
original development record and 2.0.0 release notes. It includes superseded
implementations and historical claims; use current documentation for setup
and behavior.
