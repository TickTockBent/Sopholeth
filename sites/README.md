# Sopholeth websites

Each domain is a separate static site and a separate Vercel project. There is
no shared asset build or runtime dependency on files outside a site's root
directory. Deployment filtering uses a shared Git-based check.

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
404. No custom environment variables are required.

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

## Deploy only changed sites

Each `vercel.json` sets an
[`ignoreCommand`](https://vercel.com/docs/project-configuration/vercel-json#ignorecommand)
that loads [vercel-ignore.sh](../scripts/vercel-ignore.sh) from the checked-out
Git commit. It does not require filesystem access outside the Vercel Root
Directory. The check compares only the current site's folder:

- Existing branches compare against `VERCEL_GIT_PREVIOUS_SHA`, the last
  successful deployment for that project/branch. Skipped or failed deployments
  cannot hide an earlier site change.
- A new preview branch compares its full diff from the merge base with `main`,
  so a final docs-only commit cannot hide earlier site edits.
- First production deployments and unavailable history build conservatively.
  The check fetches missing history when possible; Git failures allow a build.

Keep `.git` out of each site's `.vercelignore`: Vercel removes matching files
before running `ignoreCommand`, so excluding Git metadata prevents the check
from comparing commits. The repository's `.git` directory is outside each
site's Root Directory and static output.

Keep Vercel system environment variables exposed. The command returns zero to
skip an unchanged site and one to deploy. Repository docs, Go changes, and
sibling sites do not cause a deployment when the comparison is available.
Vercel can still show a short canceled build request for this check; its
[Ignored Build Step](https://vercel.com/docs/monorepos#ignoring-the-build-step)
uses a build slot. A manual redeploy can bypass the ignore check in Vercel.

Run `python3 -m unittest discover -s test/ci -v` to exercise the actual commands
against disposable Git histories, including new branches and shallow clones.
The filter checks have their own small CI workflow and do not require Go or
Docker. If a site gains shared asset inputs, extend its filter before relying
on changes outside its folder to trigger a deployment.

## Omega metadata hosting

`sopholeth.io` reserves `/omega/` for the public metadata repository. The config
is committed in `sites/sopholeth.io/vercel.json`; this slice needs no new Vercel
project, environment variables, or cron job. The existing project keeps Root
Directory `sites/sopholeth.io` and deploys the configuration through its normal Git
integration. There are no authority keys or signed metadata in this checkout;
the site routes return a real 404 until the publisher is integrated.

**Before public authority initialization, settle the production domain.** On
2026-09-23, the apex returned a 308 redirect to `www.sopholeth.io`, including for
`/omega/timestamp.json`. The trust client and publisher reject all redirects.
The redirect was reversed and direct apex responses verified later that day.
For a new setup using `https://sopholeth.io/omega/`, open the project's
Settings → Domains, remove the apex-to-www redirect so `sopholeth.io` serves
Production directly, then redirect `www.sopholeth.io` to the apex. See Vercel's
[domain redirect settings](https://vercel.com/docs/domains/working-with-domains/deploying-and-redirecting).
This is a domain setting; committing `vercel.json` does not change it.

Check the exact metadata URL without following redirects before `soph omega init`:

```bash
curl --silent --show-error --dump-header - --output /dev/null \
  https://sopholeth.io/omega/timestamp.json
```

With these routes deployed but no metadata published, expect a direct 404 with
`Cache-Control: no-store`, no `Location` header, and no login or challenge. After
publication, require a direct 200 with the verified metadata bytes. The hostname
and base path are bound into the authority and bundle; the current implementation
has no in-place repository migration. If www is chosen instead, update the plan
and examples before initialization. Do not work around the domain setting by
allowing client redirects.

| Request | Response and caching |
| --- | --- |
| Existing `/omega/timestamp.json` | Exact JSON file; `Cache-Control: no-store` and `Vercel-CDN-Cache-Control: no-store` |
| Existing `/omega/N.root.json`, `N.snapshot.json`, `N.targets.json` | Exact file; public one-year immutable caching |
| Existing `/omega/targets/<sha256>.bootstrap.json` | Exact file; public one-year immutable caching |
| Missing metadata, including the next numbered root | JSON 404; `no-store` for clients and Vercel's CDN |
| Other paths under `/omega/`, including `.pending`, lock files, and custody filenames | Uncached JSON 404 even if such a file accidentally reaches static output |

Immutable headers run only in the filesystem **hit** phase. Applying them to
all matching filenames before checking existence would cache future-version 404s.
Metadata misses use `omega-not-found.json` with status 404; normal docs pages and
the site's HTML 404 retain their existing behavior. These rules use Vercel's
[route phases](https://vercel.com/docs/build-output-api/configuration#routes) and
[cache header controls](https://vercel.com/docs/caching/cache-control-headers).

Run the actual local Vercel router with throwaway files:

```bash
python3 test/hosting/omega_routes.py
# Or use an already installed CLI:
python3 test/hosting/omega_routes.py vercel
```

The default uses `npx --yes vercel@59.7.0 dev --local`, creates a temporary site,
checks exact bytes, CDN cache directives, true 404s, blocked filenames, and docs
routing, then removes it. It does not link or deploy a Vercel project. Vercel dev
overrides browser `Cache-Control` to `max-age=0`; the check exercises the CDN
header and verifies the matching browser directive in the config. Confirm both
on a deployed preview and again at the public endpoint during publication rehearsal.

**Deployment adapter and hosted rehearsal complete.** The
`sopholeth-omega` project owns metadata deployments without a Git connection.
`soph omega` stages and verifies retained public history before promotion, then
checks the canonical public URL. A project-level regex rewrite, `^/omega/(.*)$`
to `https://sopholeth-omega-chi.vercel.app/omega/$1`, is installed independently of
docs deployments. The exact project
IDs, route, credential setup, and rehearsal steps are in
[Vercel publication](../docs/omega-vercel.md). Keep metadata out of this site's
Git output. A docs rollback must not change the project-level metadata route.

The public endpoint must be reachable by unauthenticated clients with ordinary
TLS: no login redirect or interactive challenge. Preview deployment protection
can remain enabled; any preview-only verification access must not become a public
client dependency. The metadata-only project needs anonymous access even at its
isolated deployment URLs so staging can be verified before promotion.

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

`soph serve` embeds `soph.stream/index.html`, `styles.css`, `viewer.js`, and `config.json`
through the Go package in [stream.go](stream.go). These files are the single
source for both distributions; rebuild `bin/soph` after changing them. The
static Vercel project continues to serve its own directory without a build.
The static `config.json` is empty; `soph serve` supplies the selected node
and initial filter at that path and forwards viewer reads to the selected
node. Document-relative asset and API URLs support port-forwarding prefixes.
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
