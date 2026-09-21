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

Build targets produce `bin/server`, `bin/omega`, and `bin/dashboard`.
To build and run just the node:

```bash
go build -o bin/server ./cmd/server
NODE_NETWORK=private NODE_MAX_STORAGE_MB=50 ./bin/server
```

See [configuration](docs/configuration.md) for network and listener behavior.
The [Compose example](README.md#try-a-cluster) contains two enclaves.

For website work, see [sites/README.md](sites/README.md). Each domain has a
self-contained static site that can be previewed and deployed independently.

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
