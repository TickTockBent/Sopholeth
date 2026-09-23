# Omega publication on Vercel

The existing `soph omega publish`, `publish --renew`, and `rotate --apply`
commands accept `--vercel-config <file>`. They upload only public metadata from
the verified journal, retain every numbered object, and verify the served bytes
and cache policy before reporting success. There is no second operator CLI.

The metadata home remains `https://sopholeth.io/omega/`. A separate Vercel project
owns its deployments. The docs project's **project-level** rewrite forwards that
path internally, so ordinary docs builds and rollbacks cannot remove or restore
metadata. The trust client still rejects HTTP redirects.

## Project and route setup

The project created on 2026-09-23 is `sopholeth-omega`, with production hostname
`sopholeth-omega-chi.vercel.app`. Its non-secret IDs are recorded in
[vercel.json](examples/omega/vercel.json). It has no Git integration, uses the
Other framework preset, has no install/build commands, and serves output `.`
from the project root. Login protection is disabled for this public-data project.
The publisher does not upload custody files or credentials.

Disposable signed metadata is hosted under a unique rehearsal subpath. The final
`/omega/` timestamp and root locations remain empty and return anonymous JSON 404s
with `Cache-Control: no-store`. No production authority has been created.

The apex domain now serves the docs directly, with www redirecting to it.
The metadata rewrite is installed as a project-level rule, separate from docs
commits and `soph omega publish`. The following steps describe that setup.

1. Establish an empty metadata deployment with the serving config and error pages.
   Promote that empty scaffold before routing traffic
   to it. Do not promote unsigned test fixtures containing timestamp/root files.
2. In the existing **sopholeth.io** project's **CDN → Routing Rules**, add a rule
   matching regex `^/omega/(.*)$`. Set its action to **Rewrite** with destination
   `https://sopholeth-omega-chi.vercel.app/omega/$1`. Preserve the `/omega/`
   prefix, and place this rule before any broader matching project rule.
3. Stage and test the rule, then publish it. This must be a project-level rule,
   separate from the deployment configuration in `vercel.json`. Do not use a
   redirect or a commit-specific deployment URL.
4. Before public `soph omega init`, request the apex metadata URL without following
   redirects. An empty repository must return a direct JSON 404 with
   `Cache-Control: no-store`, without login or a challenge. Check that a docs
   preview/deployment and rollback leave the metadata route intact during rehearsal.

Use the explicit regex and `$1` capture above. In the live rehearsal, the CLI's
path-pattern rule `/omega/:path*` with `:path*` in the destination returned 404
for objects present at the origin. The regex rule forwarded their full paths.
An empty-repository 404 alone cannot prove that forwarding works; verify actual
signed objects before activation.

To stage the empty scaffold from this checkout using the authenticated Vercel CLI:

```bash
omega_scaffold=$(mktemp -d)
cp sites/sopholeth.io/{vercel.json,omega-not-found.json,404.html} "$omega_scaffold/"
VERCEL_ORG_ID=team_EmLHcrptXvmrTsYZJezbZqIX \
VERCEL_PROJECT_ID=prj_17NHeILhCKsH3ULcLFFrDtLIgpvJ \
  vercel deploy "$omega_scaffold" --prod --skip-domain --yes \
  --scope clocktower-and-associates-llcs-projects
```

Check `/omega/timestamp.json` and `/omega/2.root.json` on the printed deployment
URL: both must return JSON 404s with `Cache-Control: no-store`. Then promote that
specific empty deployment with `vercel promote <deployment-url> --yes --scope
clocktower-and-associates-llcs-projects` and configure the rewrite above. The
scaffold contains no metadata, private files, or Git connection. Remove the
temporary local directory afterward.

Project-level rules run before deployment routes and are managed independently
of them. See [Vercel's routing rules](https://vercel.com/docs/routing/project-routing-rules).
Upstream responses provide the cache directives; avoid a project-level cache
override that caches timestamps or missing objects. Direct and rewritten responses
must be checked in the live rehearsal.

## Configure the publisher

Install the non-secret project config at `/etc/soph/omega-vercel.json`, owned by
the operator and readable by the renewal service. For another project, substitute
its IDs and name before the first publication. The first use binds that config
to the journal; subsequent publishing commands must supply the same config.
It is copied with the journal during `provision-renewal`.
The config accepts ordinary JSON whitespace and field ordering, while rejecting
missing, unknown, or duplicate fields. Journal bindings remain canonical.

Create a Vercel deployment token with access to the metadata project's team.
Supply it as `VERCEL_TOKEN` in the operator or renewal-service environment.
Never put it in the JSON config, a command argument, a commit, or chat. It is
sent only to `api.vercel.com`, with redirects disabled; it is never sent to the
metadata URL, persisted in the journal, or included in an upload. Token rotation
only requires replacing the environment secret.

After the normal authority/backup/provisioning rehearsal, publish with the existing
command plus the hosting config:

```bash
sudo --preserve-env=VERCEL_TOKEN ./bin/soph --json omega publish \
  --home /srv/omega-offline --network sopholeth \
  --manifest bootstrap.json --repository-dir /srv/omega-public --version 1 \
  --vercel-config /etc/soph/omega-vercel.json
```

The local directory remains a dedicated public spool, separate from both custody
homes. It must not be the docs checkout. Uploads are reconstructed from signed
journal records, not from directory enumeration. Extra files in that spool,
including `.pending`, lock files, or accidental custody copies, are not uploaded.

Pass the same `--vercel-config` on `rotate --apply` and `publish --renew`.
`status --verify` requires no Vercel credential: it checks the canonical public
URL, exact retained bytes, cache headers, and the next-root 404. Hosted commands
default to five minutes; an explicit global `--timeout` overrides that budget.

For unattended renewal, create a root-owned mode-0600
`/etc/soph/omega-vercel.env` containing `VERCEL_TOKEN=<token>`. Install the
[Vercel service drop-in](examples/omega/vercel-renewal.conf) under
`/etc/systemd/system/soph-omega-renewal@.service.d/vercel.conf`, reload systemd,
and rehearse one successful invocation before enabling the existing timer.
Daily refresh, seven-day expiration, and hourly checks/retries remain unchanged.
An hourly check of an already deployed release does not create a deployment.

## Publication and recovery

The publisher checks that the project matches its IDs and has no Git integration
or rolling releases. It refuses to replace a live timestamp absent from its
retained history, including when a restored local journal is behind the server.

Each changed release is staged with automatic production alias assignment
disabled. The publisher records the returned deployment ID, waits for READY,
checks every retained object's exact bytes and browser cache policy at the isolated
deployment, and confirms the next root returns an uncached JSON 404. Only then does
it request promotion. It waits for Vercel's promotion result, allows up to 30
seconds for the serving edge to expose the expected objects, and verifies the
same objects plus TUF signatures/ordering at the canonical public URL. This wait
stays within the command's overall timeout and does not allocate another deployment.

`vercel-binding.json` and `vercel-deployment.json` are private journal records,
not public repository objects. The deployment receipt records the release/config
digests, deployment ID, and stage/promotion state. Back them up with the complete
journal. Do not delete or edit them to bypass a failed check.

- A lost create response can leave an unused staged deployment. It cannot assign
  production aliases; retry can safely stage the same signed release again.
- A recorded READY deployment is reused after interruption. A build error or
  cancellation permits staging that same release again. Signed bytes and versions
  are not replaced.
- A submitted promotion is reconciled before a later release may be promoted.
  A terminal failed/skipped job permits a newer release to proceed; a synchronous
  promotion is confirmed by the project's production deployment ID.
  An unknown outcome fails closed: inspect the recorded deployment and Vercel's
  promotion status, preserve the journal, and resolve that operation before moving
  on. Do not run competing publishers or manually promote other deployments.
- A serving, protection, rewrite, or cache failure leaves publication unverified
  and returns nonzero. Correct hosting and retry; an expired release needs the
  existing renewal/offline-approval recovery path. Never reset the authority to
  fix hosting.
- The current upload cap is 32 MiB of retained public objects. Exceeding it stops
  deployment; preserve history and review hosting capacity instead of pruning
  numbered objects. Old deployment retention does not replace journal backups.

Metadata project rollback is not an operational recovery procedure. Docs project
rollback is safe once the independent rewrite is established; changing or removing
that project-level rule can still interrupt availability.

## Validation and remaining activation

The [hosted rehearsal report](omega-hosted-rehearsal.md) records the live run and
the config, routing, and propagation findings it exposed. Permanent renewal will
run on an operator-selected remote host; service credentials and scheduling are
still deployment work.

Local tests cover interrupted promotion, failed staged cache checks, idempotent
retry, renewal, history retention, custody handoff, conflicting remote history,
config binding, and credential/redirect confinement in one publication flow.
To exercise the generated config with the actual local Vercel router:

```bash
OMEGA_ROUTING_FIXTURE=/tmp/omega-vercel.json \
  go test ./internal/omega -run '^TestVercelConfigAndAPIConfinement$' -count=1
python3 test/hosting/omega_routes.py --metadata-config /tmp/omega-vercel.json vercel
```

These checks use throwaway files and create no public authority. Before activation,
complete a hosted disposable rehearsal under a unique base path, for example
`https://sopholeth.io/omega/rehearsal-20260923/`. Never publish a throwaway authority
at the final `/omega/` location: its numbered immutable objects could remain cached
after a reset. The project-level rewrite preserves this rehearsal prefix.
Configure the service token locally, publish and renew signed disposable metadata,
check fresh and returning clients, and verify docs deployment/rollback isolation.
Only then initialize the intended public authority and proceed through the remaining
release and peer-identity gates in the [launch plan](public-network-plan.md).
