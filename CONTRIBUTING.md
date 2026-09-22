# Contributing to Sopholeth

Read the [core principles](docs/core-principles.md) and
[architecture](docs/architecture.md) before changing behavior. Use the
[roadmap](docs/roadmap.md) to find current priorities. Discuss changes to
protocol semantics or project scope in the repository's issue tracker.

## Development

Requirements: Go 1.22 or later, Git, and Make. Docker is optional.

From your checkout:

```bash
git switch -c your-change
make build
make test
```

Build targets produce `bin/server`, `bin/omega`, `bin/dashboard`, and `bin/soph`.
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

The [omega trust spike](test/omega-tuf/README.md) is a separate Go module with
its own pinned toolchain and checks. Root-level `go test ./...` does not test
that module; use its documented commands when changing the spike.

## Changes and review

- Use `gofmt` on changed Go files and keep changes focused.
- Add tests for changed behavior, including relevant failure paths. Protocol
  changes need coverage at the storage, transport, or integration boundary
  they affect.
- For code changes, run `make build` and `go test -race ./...`, matching CI.
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

GitHub workflows use
[path filters](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#onpushpull_requestpull_request_targetpathspaths-ignore)
on main pushes and pull requests. They evaluate the push/PR diff, not just its
last commit. The expected work is:

| Change | Go build/race tests | Node Docker image | Vercel |
| --- | --- | --- | --- |
| Repository documentation | Skip | Skip | Skip |
| Marketing, docs, or devlog site | Skip | Skip | Changed site only |
| Embedded stream viewer assets | Run | Skip | soph.stream only |
| CLI, omega tool, or dashboard code/assets | Run | Skip | Skip |
| Node runtime or its internal packages | Run | Run | Skip |
| Go test files or testdata | Run | Skip | Skip |
| Go dependency manifests/vendor | Run | Run | Skip |
| Isolated omega TUF spike | Separate spike workflow | Skip | Skip |
| Dockerfile or .dockerignore | Skip | Run | Skip |

The Go workflow also watches `Makefile` and its own definition; Docker watches
its workflow. Embedded inputs include `cmd/dashboard/web/` and the four
explicit files in `sites/stream.go`. The image builds only `cmd/server`, so
`internal/client` and `internal/dashboard` are excluded. Update filters when
adding an imported package, embedded resource, or build input that changes
these boundaries.

Docker still runs for `v*` tags and manual dispatch, including deliberate
base-image refreshes. New commits cancel superseded PR runs; main/release
publishing runs finish normally. A separate fast workflow tests the deployment
filters when their scripts, configurations, tests, or workflow files change.

Main currently has no required checks. A workflow skipped by native path
filters has no completed check; if branch protection later requires one,
introduce an always-reported gate instead of requiring a filtered workflow.
See [site deployment filtering](sites/README.md#deploy-only-changed-sites) for
Vercel comparison and first-deployment behavior.
