# Bootstrap trust client

This package is the production TUF integration component for the
[omega design](../../../docs/omega-trust-design.md). It is exercised by the
ordinary Go suite:

```sh
go test -race ./internal/trust/bootstrap
```

The node, CLI, and dashboard still use the interim discovery path. Connecting
this package to those consumers, building `soph omega`, and replacing the
legacy public-release anchor are subsequent changes. Public discovery remains
disabled in ordinary builds; this package creates no production authority.

## API and formats

`New(ctx, Config)` accepts a validated `Bundle`, a state directory whose parent
already exists, and an optional standard HTTP client transport for custom
certificate roots. It creates state only when the final directory is absent.
State must belong to the running user, with a private directory and regular
private files. An existing empty directory, missing state, bad checksum,
symlinks, or incompatible network/bundle produce an error. There is no implicit
trust reset. Custom TLS dialers, disabled certificate validation, and overridden
TLS hostnames are rejected.

Parent symlinks are resolved once; all ancestors must be owned by the current
user or root and protected against replacement by another user. Shared writable
parents require the sticky bit (as on `/tmp`). This also protects the path-based
scratch cache used by go-tuf, rather than relying only on the state directory's
own permissions.

The public bundle schema is:

```json
{
  "schema": 1,
  "network": "example-public",
  "repository": "https://metadata.example.invalid",
  "root": { "signed": "a complete signed TUF root object goes here", "signatures": [] }
}
```

The `root` above is illustrative, not a usable anchor. Supply complete,
self-signed TUF root metadata, including all four roles, Ed25519 keys, positive
version, expiry, and consistent snapshots. Empty and zero keys are rejected.
`Bundle.Fingerprint()` hashes go-tuf's normalized serialization of the complete
initial root; it is distinct from the legacy single-key fingerprint. Verify
the initial bundle independently, never by trusting its download location.

The initial bundle identity is retained across client restarts and software
updates. Numbered root transitions advance the stored root. Replacing the
initial bundle, network identity, or repository location requires an explicit
migration, which this package does not yet provide; deleting state discards
rollback history and is not a routine recovery procedure.

The signed top-level `bootstrap.json` target has this schema:

```json
{
  "schema": 1,
  "network": "example-public",
  "enclave": "default",
  "roots": [
    { "id": "root-1", "origin": "https://root-1.example.invalid" }
  ]
}
```

Network identity must match the bundle. The client accepts 1–16 distinct roots;
the first public deployment's publishing policy will require three. Identifiers
use ASCII letters, digits, dots, underscores, and hyphens, starting with a
letter or digit, with a maximum of 128 characters. Origins must use HTTPS,
without userinfo, queries, fragments, or application paths. Equivalent host
case, default ports, and trailing slashes normalize for duplicate detection.
Explicit private/loopback origins are permitted for rehearsal. Unknown and
duplicate JSON fields, unsupported schemas/enclaves, and oversized manifests
are rejected. Node identity and advertised gossip/WebSocket endpoint checks
belong to the upcoming transport integration; a manifest alone does not
authenticate those connections.

## Refresh, durable progress, and leases

`Refresh(ctx)` creates a fresh go-tuf updater in a private scratch directory
populated from established state. The library performs root transitions,
signature/threshold verification, ordering, expiry, and target hash checks.
Only the fixed top-level `bootstrap.json` target is activated. Downloads stay
on the configured HTTPS origin, reject redirects, use a ten-second request
timeout and a two-minute overall refresh limit, and honor earlier caller
cancellation. Root/snapshot/targets metadata are limited to 512 KiB, timestamps
to 16 KiB, and the manifest to 64 KiB. Response reads enforce limits even when
Content-Length is absent or dishonest.

Before every next download, and when the updater returns, the client copies
verified metadata progress into a single state envelope. It writes a private
pending file, syncs it, renames it over established state, and syncs the parent
directory. It does not expose a newly accepted manifest before that durable
commit. A failed refresh still preserves already-verified progress: accepting
timestamp N and failing to fetch snapshot N must not allow N-1 after restart.
The library's ordinary cache files are never the durable source of truth.

Each operation takes a context-aware OS file lock. This serializes goroutines
and separate processes using the same state directory; process termination
releases the lock without a stale PID-file recovery procedure. Existing
private state has a checksum to detect accidental corruption. This checksum
is not an authentication mechanism against an attacker who can modify the
owner's files. The local user and filesystem remain part of the trusted base.

`Current(ctx)` performs no network requests. It revalidates the complete
accepted metadata/manifest and checks expiry on every call, including after
an offline restart. The returned `View.Expires` is the earliest expiry in the
four metadata roles. Equality with the deadline is expired. Successful renewal
can extend the lease; a failed refresh cannot. Timestamp/snapshot progress can
coexist with a previous still-valid manifest. Any verified change to root or
targets metadata conservatively revokes that manifest until full acceptance,
even if later downloads fail. Consumers retaining a root-status flag must
schedule its revocation at this deadline independently of refresh success.

## Storage and validation limits

Use a local filesystem with working file locks, atomic rename, file sync, and
directory sync. Linux is the tested deployment platform. The lock implementation
is also enabled for macOS and the BSDs listed in `lock_unix.go`; those platforms
have not had deployment rehearsal. Other platforms return an unsupported-state
error. There is no fallback to unlocked or memory-only public trust. Existing
applications keep their current platform behavior until they adopt this API.

Native Windows public discovery is a required
[integration gate](../../../docs/public-network-plan.md#windows-public-client-gate)
before public `soph join`, profile renewal, and `soph serve` adopt this package.
The Windows backend must address ACL/account and path validation, locking,
and durable state replacement, with tests running on Windows itself. The
current unsupported-platform error is an interim limit, not the intended
public-client support policy.

Tests cover disposable HTTPS discovery, renewal without offline keys, old/new
rotation thresholds, returning clients, missing transitions, rollback at all
metadata levels, retired keys, all-role expiry, the exact runtime deadline,
malformed/tampered manifests, state corruption and network isolation, interrupted
commit boundaries, concurrent clients, cancellation, and lock release after
killing another process. Write failures are injected around write/sync/rename
boundaries. These tests verify the protocol and write ordering, not physical
power-loss behavior of a particular storage device or network filesystem.

Operator recovery/reset, encrypted key custody, publication, scheduled renewal,
runtime callbacks, and final node/CLI integration are not implemented here.
