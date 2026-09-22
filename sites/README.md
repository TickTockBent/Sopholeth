# Sopholeth websites

Each domain is a separate static site and a separate Vercel project. There is
no shared build step or dependency on files outside a site's root directory.

| Domain | Vercel Root Directory | Contents |
| --- | --- | --- |
| `sopholeth.com` | `sites/sopholeth.com` | Rebranded marketing and showcase site, moved from `web/`. |
| `sopholeth.io` | `sites/sopholeth.io` | Documentation landing page, private-node quickstart, and links to the maintained repository guides. |
| `sopholeth.dev` | `sites/sopholeth.dev` | Devlog and release-notes landing page, starting with the merged rebrand. |
| `soph.stream` | `sites/soph.stream` | Live node viewer; connect through the node field or `?node=`. No public default endpoint is configured yet. |

## Vercel setup

Import `TickTockBent/Sopholeth` once for each project. Select the matching
**Root Directory** from the table and use **Other** as the framework preset.
Each folder's `vercel.json` skips installation and builds, serves `.` as the
output directory, and sends missing paths to its own `404.html` with status
404. No environment variables are required.

Set the production branch to `main` and attach the matching domain in each
project's domain settings. For an existing marketing project, change its
Root Directory from `web` to `sites/sopholeth.com` before deploying this
layout. The repository no longer includes a GitHub Pages `CNAME` file.

This follows Vercel's [monorepo project setup](https://vercel.com/docs/monorepos)
and [static build configuration](https://vercel.com/docs/builds/configure-a-build#skip-build-step).
Vercel project links and DNS changes are managed separately; these folders
do not create projects or provision domains.

Website DNS for `sopholeth.io` does not replace the network's
`_bootstrap.sopholeth.io` and `_omega.sopholeth.io` TXT records.

## Local preview

From the repository root, serve any one site with Python 3:

```bash
python3 -m http.server 3000 --bind 127.0.0.1 --directory sites/sopholeth.com
```

Open `http://localhost:3000`. Change the directory for another domain, or use
different ports to preview several together. Cross-site links intentionally
use the public domains. Python's server uses its own response for a missing
path; visit `/404.html` to preview the custom error page locally.

The marketing site keeps its existing React/Babel and font CDN dependencies.
The docs and devlog sites use plain HTML and local CSS. The stream viewer
also uses local JavaScript and has no external asset dependencies. Edit each site's files directly;
their styles are intentionally local so deployments remain independent.

`soph serve` embeds `soph.stream/index.html`, `styles.css`, and `viewer.js`
through the Go package in [stream.go](stream.go). These files are the single
source for both distributions; rebuild `bin/soph` after changing them. The
static Vercel project continues to serve its own directory without a build.
See the [CLI guide](../docs/cli.md#local-stream-viewer) and
[local validation instructions](../test/stream/README.md).

## Content and next steps

- The canonical technical guides remain in [`docs/`](../docs/) until the docs
  publishing workflow is chosen. The docs landing page links to them on GitHub.
- Add devlog entries to `sopholeth.dev/index.html`. Release ingestion, feeds,
  and semi-automated publishing are not implemented in this scaffold.
- Validate `soph.stream` locally and in remote staging before configuring a
  public network endpoint; follow the [stream plan](../docs/soph-stream-plan.md).
- The Go dashboard is a separate service under `cmd/dashboard`; it is not
  one of these static sites.
