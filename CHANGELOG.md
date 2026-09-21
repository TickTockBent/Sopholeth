# Sopholeth changelog

## Unreleased

### Client

- Add the `soph` client CLI (#187): a direct HTTP client in `cmd/soph` with
  `join`, `use`, `networks`, `forget`, `put`, `get`, `exists`, `list`, and
  the read-only diagnostics. Joining validates a node's health and saves it
  as the current network; multiple named networks persist in
  `$XDG_CONFIG_HOME/sopholeth/soph.json` with no fallback between them.
  Public joining resolves signed discovery and reports clearly that it is
  unavailable until the trust anchor exists. Writes report confirmed versus
  pending quorum and the TTL the node actually applied. Stable exit codes
  distinguish missing keys, unreachable nodes, and usage errors.
- Add `internal/client`, the reusable node API client the CLI is built on,
  and `internal/client/clienttest`, an in-memory node for tests.

### Websites

- Move the marketing site to `sites/sopholeth.com` and add starter sites for
  documentation (`sopholeth.io`), development updates (`sopholeth.dev`), and
  `soph.stream`. Each folder has its own static Vercel configuration and 404.
- Link the domain sites and document their independent deployment roots.
  Devlog automation and the stream experience remain future work.

### Documentation and identity

- Adopt **Sopholeth**, pronounced **SOF-oh-leth**, with the identity line
  **Wisdom through intentional forgetting**. Reserve `soph` for the future
  client CLI; the current node executable is `server`.
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
the public trust anchor remains a placeholder. See the
[roadmap](docs/roadmap.md) for launch gates.

## Earlier history

The [pre-rebrand changelog](docs/archive/repram-changelog.md) preserves the
original development record and 2.0.0 release notes. It includes superseded
implementations and historical claims; use current documentation for setup
and behavior.
