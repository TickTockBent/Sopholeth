# Contributing to Sopholeth

Read the [core principles](docs/core-principles.md) and
[architecture](docs/architecture.md) before changing behavior. Use the
[roadmap](docs/roadmap.md) to find current priorities. Discuss changes to
protocol semantics or project scope in the repository's issue tracker.

## Development

Requirements: Go 1.27.1 or later, Git, and Make. Docker is optional.

From your checkout:

```bash
git switch -c your-change
make build
make test
```

Build targets produce `bin/server`, `bin/dashboard`, and `bin/soph`.
To build and run just the node:

```bash
go build -o bin/server ./cmd/server
NODE_NETWORK=private NODE_MAX_STORAGE_MB=50 ./bin/server
```

See [configuration](docs/configuration.md) for network and listener behavior.
The [Compose example](README.md#try-a-cluster) contains two enclaves.

For website work, see [sites/README.md](sites/README.md). Each domain has a
self-contained static site that can be previewed and deployed independently.
The stream viewer is also embedded in `soph`; its
[browser checks](test/stream/README.md) exercise the shared assets.

The [bootstrap trust client](internal/trust/bootstrap/README.md) is tested by
the ordinary Go suite. Its integration tests use disposable keys, loopback
HTTPS, and temporary state directories. No public authority is required.

Omega tests use the same ordinary suite. Keep initialization's all-or-nothing
recovery coverage, and test shared journal/publication failures in the publisher
suite. Renewal and rotation tests should cover their distinct reservations,
offline handoffs, and approval rules. One alternating-role walkthrough covers
the retained chain and fresh/returning clients. Reuse a valid fixture for
in-memory policy checks; use separate homes when a case changes custody or
history. Keep real process-death and concurrency checks at the relevant
boundaries without repeating the whole failure matrix for every command.

## Changes and review

- Use `gofmt` on changed Go files and keep changes focused.
- Add tests for changed behavior, including relevant failure paths. Protocol
  changes need coverage at the storage, transport, or integration boundary
  they affect.
- For code changes, run `make build` and `make test-race`, matching CI. Both
  test targets keep temporary files in `/dev/shm` when it exists; omega tests
  fsync every durable write, which is slow on spinning disks.
  For documentation changes, check links, commands, and claims against the
  current implementation.
- Document externally visible behavior and update the changelog when relevant.
  Distinguish implemented features from plans and historical observations.
- In a pull request, explain the problem, resulting behavior, validation, and
  any compatibility implications.

Keep values opaque, TTL mandatory, and client access permissionless. Treat
encryption, application identity, and application conflict resolution as
client concerns. Avoid promises of global ordering, exclusive locks,
guaranteed delivery, or secure erasure that the node does not provide.

Work respectfully, address feedback directly, and keep discussion focused on
the project. See the project's [license](LICENSE) for the current terms.

## CI selection

GitHub workflows select work from the push or pull request diff, not just its
last commit. Docker and the deployment-filter tests use native
[path filters](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#onpushpull_requestpull_request_targetpathspaths-ignore).
The Test workflow runs on every main push and pull request and decides inside
the run, from [`.github/go-test-paths.txt`](.github/go-test-paths.txt), whether
the Go suite is needed. The expected work is:

| Change | Go build/race tests | Node Docker image | Vercel |
| --- | --- | --- | --- |
| Repository documentation | Skip | Skip | Skip |
| Marketing, docs, or devlog site | Skip | Skip | Changed site only |
| Embedded stream viewer assets | Run | Skip | soph.stream only |
| CLI (including omega), or dashboard code/assets | Run | Skip | Skip |
| Node runtime or its internal packages | Run | Run | Skip |
| Go test files or testdata | Run | Skip | Skip |
| Go dependency manifests/vendor | Run | Run | Skip |
| Dockerfile or .dockerignore | Skip | Run | Skip |

The Go suite also runs for `Makefile`, its workflow definitions, and its path
list and change-detection script; Docker watches its workflow. Embedded inputs include `cmd/dashboard/web/` and the four
explicit files in `sites/stream.go`. The image builds only `cmd/server`, so
`internal/client` and `internal/dashboard` are excluded. Update filters when
adding an imported package, embedded resource, or build input that changes
these boundaries.

Docker still runs for `v*` tags and manual dispatch, including deliberate
base-image refreshes. New commits cancel superseded PR runs; main/release
publishing runs finish normally. A separate fast workflow tests the deployment
filters when their scripts, configurations, tests, or workflow files change.

`main` requires the **Test gate** check. It always reports: it passes when no
Go-relevant path changed and otherwise requires the Go suite to pass. Do not
make a natively path-filtered workflow a required check; a skipped workflow
reports nothing and blocks the merge.

## Publishing and protected refs

The node binary embeds the omega trust anchor, so publishing is gated (#223):

- Pull requests build the Docker image and never push it.
- Every publishing run first runs the Go suite on the same commit
  ([`go-tests.yml`](.github/workflows/go-tests.yml)) and pushes nothing if it
  fails.
- `main` pushes (and manual runs on `main`) publish `:edge` and `:sha-<short>`.
  They never move `:latest`.
- A `v*` tag publishes the version tags and, for non-prerelease versions,
  `:latest`. Before that, the **Verify release** job checks that the tagged
  commit is reachable from `main` and runs `make check-public-release`. That job
  uses the `release` environment, which holds `OMEGA_EXPECTED_SHA256`, requires
  a reviewer, and accepts only `v*` tags. A repository variable of the same name
  is not enough: the job fails when the environment value is empty.
- Manual runs on any other ref publish nothing.

Repository settings that back this up are kept as JSON in
[`.github/rulesets/`](.github/rulesets/):

- `main.json`: pull requests required (no approval count, since the repository
  has one maintainer), Test gate required, linear history, squash or rebase
  merges, no force-push or deletion, and no bypass list. An emergency change
  means editing or disabling the ruleset, which only an admin can do and which
  is recorded in the repository's rule history.
- `release-tags.json`: only repository admins may create, move, or delete `v*`
  tags.

Apply or update them with the GitHub API, for example:

```sh
gh api -X POST repos/TickTockBent/Sopholeth/rulesets --input .github/rulesets/main.json
gh api -X PUT repos/TickTockBent/Sopholeth/rulesets/<id> --input .github/rulesets/main.json
```

Create the `release` environment before pushing any `v*` tag. A workflow that
names a missing environment creates it with no protection.
See [site deployment filtering](sites/README.md#deploy-only-changed-sites) for
Vercel comparison and first-deployment behavior.
