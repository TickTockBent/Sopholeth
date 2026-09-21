# Sopholeth rebrand

## Identity

| Element | Decision |
| --- | --- |
| Project and network | **Sopholeth** |
| Pronunciation | **SOF-oh-leth** |
| Meaning and lead line | **Wisdom through intentional forgetting.** |
| Node command | **`soph`** |
| Intended domains | `sopholeth.io` and `sopholeth.dev` |
| Repository name | `sopholeth`; final owner and URL follow account setup |

Use **Sopholeth** in prose and lowercase **sopholeth** in names that require
lowercase. Lead with temporary shared state and intentional forgetting.
Describe encryption and retention limits precisely; avoid claims that
expiration guarantees secrecy or makes retained copies disappear.

The domains and GitHub name are being arranged by the maintainer. They are
not documented as active service endpoints.

## Documentation pass

- [x] Establish the name, pronunciation, meaning, and CLI direction.
- [x] Replace active documentation branding and remove unverified install links.
- [x] Consolidate the overview and whitepaper into one architecture document.
- [x] Separate current API and configuration from launch plans.
- [x] Shorten the roadmap around public alpha, a demo, and the probe laboratory.
- [x] Retire the host-specific burn-in command runbook and obsolete TS setup.
- [x] Preserve release and experiment history with explicit historical context.

This pass changes Markdown documents only. No source, configuration, release
artifact, website asset, remote, DNS record, or deployment is renamed here.

## Implementation pass

The README builds the current source to `bin/soph` with Go's output flag.
That executable works under the new local name, but does not change the
embedded strings or the repository's normal build outputs.

| Surface | Current state | Follow-up |
| --- | --- | --- |
| Node executable and entry point | `repram`, `cmd/repram` | Make `soph` the standard output and update the entry point. |
| Operator executables | `repram-omega`, `repram-dashboard` | Use `soph-omega` and `soph-dashboard`. |
| Go module and imports | `repram` | Update together after the repository location is settled. |
| Environment variables | `REPRAM_*` | Adopt a consistent new prefix; proposed `SOPH_*`. |
| MCP tool IDs | `repram_*` | Update tool names, server identity, prompts, and client examples together; proposed `soph_*`. |
| Metrics | `repram_*` | Rename with scrape rules, alerts, and dashboards; proposed `soph_*`. |
| State directories | `.repram`, `repram-dashboard`, burn-in paths | Define new paths and any migration behavior. |
| Containers and automation | Legacy image tags, service names, scripts, CI | Update the build and publishing chain as one change. |
| Public discovery | `_bootstrap.repram.io` and legacy operator output | Move to the acquired domain with release and DNS coordination. |
| Website and dashboard UI | Existing branding in HTML, CSS, JS, and assets | Apply the identity in a separate implementation pass. |
| Source comments and local guidance | Legacy names and old document paths | Update references after the document consolidation. |

Decide compatibility behavior for environment variables, MCP tools, metrics,
and state paths before release. A mechanical rename must not silently strand
existing clients or caches. The signed format identifier `omega-v1` is a
protocol version, not branding.

## Launch cutover

- [ ] Confirm domain ownership and the final repository URL.
- [ ] Complete the implementation rename and update documentation examples.
- [ ] Publish and verify renamed binaries and container images before linking them.
- [ ] Replace the placeholder public trust anchor through the operator key ceremony.
- [ ] Deploy roots and publish signed discovery records for the new network.
- [ ] Complete the [public-alpha validation gates](roadmap.md#before-public-alpha).
- [ ] Update the website, badges, install paths, and any old-name redirects.

Historical release notes and raw experiment artifacts retain their original
identifiers. They describe the software that actually ran.
