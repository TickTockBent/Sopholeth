# Sopholeth changelog

## Unreleased

### Documentation and identity

- Adopt **Sopholeth**, pronounced **SOF-oh-leth**, with the identity line
  **Wisdom through intentional forgetting** and the intended command `soph`.
- Replace the README with a private-network quick start and local builds that
  work before artifact and repository renames.
- Consolidate the project overview and whitepaper into an architecture guide.
  Add focused API, configuration, discovery, and rebrand references.
- Correct retention, access-control, quorum, and coordination claims.
  Document known implementation limits as public-launch work.
- Focus the roadmap on public alpha, a demo web application, and probe simulation.
- Retire the obsolete host-specific burn-in runbook and TS deployment
  instructions. Preserve historical releases and experiment observations.

This rebrand pass changes documents only. Source, configuration, website
assets, published artifacts, and live infrastructure retain their existing
identifiers.

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
