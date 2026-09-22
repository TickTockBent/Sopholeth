# Disposable omega TUF integration spike

This nested module evaluates `go-tuf/v2` v2.4.2 for
[#195](https://github.com/TickTockBent/Sopholeth/issues/195). It is not linked
into the node or CLI. See the [design recommendation](../../docs/omega-trust-design.md)
for the proposed production workflow and remaining work.

## Run

From this directory, with Go toolchain auto-download enabled or Go 1.27.1
installed:

```sh
go mod download
go mod verify
go test -mod=readonly -race -v ./...
```

The module requires at least Go 1.25.0 and pins Go 1.27.1 as its toolchain.
`GOTOOLCHAIN=local go test ./...` with the application's Go 1.22.2 fails with
the expected minimum-version error. No root-module dependency or toolchain
change is necessary to run this experiment. Root-level `go test ./...`
does not include nested modules.

For disposable dependency/build caches, for example:

```sh
GOMODCACHE=/tmp/soph-omega-tuf-modcache GOCACHE=/tmp/soph-omega-tuf-cache go test -mod=readonly -race -v ./...
```

Initial dependency/toolchain downloads need Internet access. The tests then
need only local filesystem access and loopback HTTPS listeners. No public DNS
records, production endpoints, or external signing service are used. Keys
are generated in memory for each fixture and discarded; only public metadata
and the dummy manifest are written under temporary directories.

## Scenarios

| Test | Observed boundary |
| --- | --- |
| Fresh client / identical restart | Signed metadata and hashed target download, idempotent refresh |
| Online renewal | Snapshot/timestamp renewal works with offline signing keys removed |
| Rollback after restart | Persisted timestamp version rejects an older signed publication |
| Online signer rollback | New signed freshness metadata cannot roll an existing client back to older snapshot or membership versions |
| Root quorums | Both old and new 2-of-3 thresholds must be met |
| Returning client | Sequential transitions recover expired starting/intermediate roots; missing history strands old clients |
| Retired online key | Its signature fails after the client accepts the successor root |
| Interrupted publication | Missing referenced metadata fails; partial accepted progress survives; a newer repair succeeds |
| Target tampering | Same-length altered content fails its signed digest |
| Each role expires | Expired root, timestamp, snapshot, or targets prevents fresh acceptance |
| Runtime lease | Cached target lookup does not revoke external runtime authority; the consumer must enforce the deadline |
| Existing state | The wrapper rejects a missing/corrupt established root rather than silently starting over |

The fixture uses the real library for signatures, update verification, cache
persistence, and target downloads. `UnsafeSetRefTime` controls time only in
tests; TLS verification remains enabled. The manifest is opaque dummy content,
not an implementation of the proposed schema.

The restart wrapper is deliberately limited: it chooses the newest local root
and rejects missing/corrupt established root state. It is not a crash-durable
store, and does not detect all cache corruption or coordinate concurrent
writers. The in-memory repository does not model production key custody,
hosting, delayed replicas, or atomic remote publication. No production CLI,
runtime expiry callback, network transport migration, or operator recovery
procedure is implemented here.

## CI

The `Omega TUF spike` workflow runs this module on changes to its non-Markdown
files or its workflow. The ordinary Go workflow excludes this directory;
Docker and Vercel do not rebuild for spike changes. Move useful tests into
the application suite when the real trust client lands, then retire the
isolated module/workflow instead of maintaining two implementations.
