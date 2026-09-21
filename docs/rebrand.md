# Sopholeth identity and naming

| Element | Decision |
| --- | --- |
| Project and network | **Sopholeth** |
| Pronunciation | **SOF-oh-leth** |
| Meaning and lead line | **Wisdom through intentional forgetting.** |
| Future client CLI | **`soph`**; not implemented yet |
| Intended domains | `sopholeth.io` and `sopholeth.dev` |
| Intended repository name | `sopholeth`; hosting links follow the actual repository until it moves |

Use **Sopholeth** in prose and **sopholeth** for the Go module, distribution
identity, and application state directories. Name implementation components
by their purpose. Lead with temporary shared state and intentional forgetting;
do not imply that expiration erases copies retained elsewhere.

## Code naming

This is a direct cutover. Old environment variables, tool IDs, metric names,
and HTTP signature headers have no compatibility aliases. Existing state
directories are not migrated automatically.

| Surface | Current name |
| --- | --- |
| Node executable and entry point | `bin/server`, `cmd/server`; `--mcp` runs the embedded MCP server |
| Operator executables | `bin/omega`, `cmd/omega`; `bin/dashboard`, `cmd/dashboard` |
| Go module and imports | `sopholeth` |
| Node settings | `NODE_*`, with `NODE_ID` for node identity |
| Dashboard state override | `DASHBOARD_STATE_DIR` |
| Burn-in targets and state | `BURNIN_NODES`, `BURNIN_STATE_DIR` |
| MCP tool IDs | `store`, `retrieve`, `exists`, `list_keys` |
| Metrics | `gossip_*`, `http_*`, `discovery_*`; dashboard metrics remain `dashboard_*` |
| HTTP gossip and bootstrap authentication | `X-Gossip-Signature` |
| Node discovery cache | `$HOME/.sopholeth/cache`; override with `NODE_CACHE_DIR` |
| Dashboard state | `$HOME/.local/state/sopholeth/dashboard` |
| Burn-in state | `$HOME/.local/state/sopholeth/burnin` |
| Local container image | `sopholeth/node:local`; burn-in image `sopholeth/node:burnin` |
| CI publishing target | `ticktockbent/sopholeth-node`; publication remains a release step |
| Public discovery names | `_bootstrap.sopholeth.io`, `_omega.sopholeth.io` |

See [configuration](configuration.md) for settings and fallback paths, and
the [API reference](api.md) for the current interfaces. `soph` is reserved for
the future client; it is not a synonym for the node service. The `omega-v1`
signed format, WebSocket fields, and `/v1/` routes retain their protocol names.
The old product/version response header has been removed.

The site and dashboard display Sopholeth. `web/CNAME` names the intended site
domain; editing it does not provision DNS or deploy a site. The public trust
anchor remains a placeholder, and no public network is claimed to be live.

## Remaining launch work

- Confirm domain ownership, move the repository, and update hosting links.
- Publish and verify renamed binaries and container images before linking them.
- Reconcile the historical component exclusions in [LICENSE](../LICENSE).
- Retire or replace unused legacy visual assets before adding social previews.
- Replace the placeholder public trust anchor through the operator key ceremony.
- Deploy roots and publish signed discovery records for the new network.
- Complete the [public-alpha validation gates](roadmap.md#before-public-alpha).

Historical release notes, experiment artifacts, and legal attributions retain
their original identifiers. They describe the software that actually ran.
