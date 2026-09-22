# Omega TUF spike: completed

The experiment from [PR #203](https://github.com/TickTockBent/Sopholeth/pull/203)
selected go-tuf v2.4.2 and established the [trust design](../../docs/omega-trust-design.md).
Its disposable repository fixture and useful lifecycle scenarios now exercise
the real client in [internal/trust/bootstrap](../../internal/trust/bootstrap/README.md).
The isolated module and its workflow have been retired.

From the repository root:

```sh
go test -race ./internal/trust/bootstrap
```

The original experiment, including the Go 1.22 incompatibility observation,
remains available in PR #203. Its historical test wrapper was not a production
state store; the current client adds durable checkpoints, process locks,
strict manifest validation, bounded HTTPS, and independently checked leases.
