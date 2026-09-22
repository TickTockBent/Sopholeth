# soph: the Sopholeth client

`soph` is a command-line client for the Sopholeth node API. It is a direct
HTTP client: it talks to one node and runs no node of its own, so it needs no
inbound connectivity and no background process. Join a network once, then
`put`, `get`, `exists`, and `list` use that node until you switch.

The client covers the [HTTP API](api.md). It adds no operations the network
does not have: no delete, no TTL extension, no ownership, no history.

## Install

Build from source with the other binaries:

```bash
make build            # bin/server, bin/omega, bin/dashboard, bin/soph
go build -o bin/soph ./cmd/soph
```

Published binaries and packages are a release step that has not happened
yet; see the [roadmap](roadmap.md#before-public-alpha).

## Authority operations

`soph omega init` atomically creates a disposable TUF authority.
`soph omega publish` publishes an approved numbered release to a local
HTTPS-served repository and verifies it through the trust client.
`soph omega status` inspects local material; `--verify` checks the served release.
These commands use a separate private custody home and do not select or modify
a saved client network.
See [omega operations](omega-operations.md) for the commands, all-or-nothing
commit/retry contract, publication journal, custody limits, and JSON status.
Unattended renewal, rotation, production hosting, and production authority
creation remain pending.

## Join a network

```bash
soph join 192.168.0.1                 # private network through that node, port 8080
soph join node.example:9000 --name lab
soph join https://node.example        # TLS if a proxy terminates it
```

Join normalizes what you typed to a base URL, calls `/v1/health`, and on
success saves the connection and makes it current. The response must report
`healthy`, a non-empty node ID and enclave, and a `private` or `public` network.
A failed or invalid health check leaves saved profiles and the current
selection unchanged. The default name is the host you typed; `--name` overrides it.
Joining a name that already exists replaces it.

Any working node will do. The client never calls `/v1/bootstrap`, so the
public-network root gate does not apply to it.

### Public network

```bash
soph join                             # verified signed discovery
soph join host:port --public          # public network through a node you name
```

With no endpoint, `soph join` resolves the signed root list over DNS, verifies
it against the compiled omega key, and saves the first root that answers a
health check under the name `public`. The verified roots and the list's
expiration are recorded with the profile; once the list expires, commands
refuse the profile until you run `soph join` again.

The public network is not live. Until the [trust anchor and DNS records
exist](discovery.md), `soph join` reports that public discovery is
unavailable. Tests exercise this path with an injected test anchor.

Naming a node with `--public` records it as an operator-supplied entry point.
It is never verified against the signed list and never becomes a trust
anchor; `soph networks` shows it as `public, operator-supplied`. The client
warns when the node's own health report disagrees with the mode you chose.

## Manage saved networks

```bash
soph networks                         # * marks the current network
soph use lab
soph forget lab
```

A network name is a local label. It says nothing about enclave membership
or authentication; the node's reported enclave is shown for information.

Which network a command uses is decided in this order, and nothing falls
back to anything else:

1. `--network <name>` on the command line.
2. `SOPH_NETWORK` in the environment.
3. The saved current selection.

An unknown name, or no selection at all, is a usage error (exit 2).

Saved networks live in `$SOPH_CONFIG_DIR/soph.json`, defaulting to
`$XDG_CONFIG_HOME/sopholeth/soph.json` and then
`~/.config/sopholeth/soph.json`. The file is written atomically with
owner-only permissions. It holds endpoints and node identity reported at
join time, never payloads. `--config <path>` points at a different file.

## Data commands

```bash
printf 'hello' | soph put greeting --ttl 300
soph put --file report.bin --ttl 3600   # key is generated and printed
soph get greeting
soph get report --output report.bin
soph exists greeting
soph list --prefix demo: --limit 50
soph list --prefix demo: --all
```

`put` reads the value from stdin or `--file`. Without a key it generates a
UUID-shaped one. The key is printed to stdout so scripts can capture it;
everything else goes to stderr. Writing to an existing key replaces the value
and restarts its TTL, as the protocol defines. If stdout fails after the node
accepts a write, the client exits 1 and reports the key and known quorum
outcome on stderr. It does not retry the write.

`get` writes the value's bytes to stdout exactly as stored, with nothing
added. An empty value writes nothing and exits 0; a missing key writes
nothing and exits 3. Use `--output` to write to a file instead.

`exists` (alias `head`) reports presence and the contacted node's local TTL
metadata without transferring the payload.

`list` prints one key per line. With `--limit`, a further page is announced
on stderr with the cursor to pass next; `--all` walks every page. Pages are
not a snapshot: keys can appear or expire between requests. The node's
empty listing may arrive as `null`; the client always prints `[]`.

Keys are opaque strings that occupy one URL path segment. They are
URL-escaped on the wire. A key containing `/` is rejected by the node and
reported as a usage error.

### TTL

TTL is mandatory and local to each replica. When `--ttl` is omitted the node
applies its default (1800 seconds). The node clamps requests to its bounds
(300 to 86400 seconds by default) without saying so in the write response.
After each write the client makes a best-effort `HEAD` request and reports
the current key's metadata separately:

```
stored 5 bytes under "greeting" on http://192.168.0.1:8080: quorum confirmed, requested ttl 5s
observed current key "greeting": local ttl 300s (may reflect a subsequent write)
```

Another writer can replace the key before that follow-up read. Its TTL and
creation time therefore describe the value observed at read time; they do
not establish the TTL applied to this write or prove clamping. JSON output
puts these fields under `observed_current_key`, omitted if the read fails.
The client never computes a network-wide expiration. Each replica starts
its own TTL when it stores the value.

### Confirmed and pending writes

A node answers `201` when it stored the value and observed its dynamic
quorum, and `202` when it stored the value but the quorum wait expired.
`soph put` reports these as `quorum confirmed` and `quorum pending`. Pending
is not a failure: the value is on the contacted node and replication
continues. It is also not a delivery promise.

Both exit 0 by default. Pass `--require-confirmed` to exit 5 on a pending
write; the key is still printed and the value is still stored.

The client never retries a write on its own. A retry is a new write that
restarts the TTL, and an interrupted request may already have stored data.

## Local stream viewer

```bash
soph join localhost:18080 --name local
soph serve                             # http://127.0.0.1:8181
soph --network local serve --q hello
soph serve --node localhost:18080 --port 8182 --open
```

`serve` embeds the same assets as `sites/soph.stream`; it works from any
directory and needs no separate frontend build. It prints the viewer URL
to stdout and the watched node/enclave to stderr. `--json` prints one object
with `url`, `endpoint`, `node`, and `enclave` before serving. Ctrl-C or SIGTERM
stops the viewer server and exits 0.

Selection is `--node`, otherwise the normal named-network selection. With
no selection at all it uses `localhost:8080`; an invalid or expired selected
profile remains an error. Startup checks node health without changing saved
profiles. `--bind` defaults to `127.0.0.1`; `--port` defaults to `8181` and
accepts `0` for a free port. `--open` launches the system browser. `--q` sets
an initial case-insensitive search across keys and payload previews.

`soph serve` provides the startup node and filter to the browser, so opening
the viewer without query parameters still connects to the selected network.
It forwards streaming and full-value reads to that node through the viewer's
own address. This also works through HTTPS port forwarding such as
`https://editor.example/proxy/8181/`; only the viewer port needs forwarding.
Assets and reads retain the proxy prefix. Forwarded reads target only the
startup node, and the viewer does not expose write operations.

Query parameters `node` and `q` carry the viewer state and override startup
defaults. Selecting a different node in the browser connects to it directly.
The standalone site also connects directly, so an HTTPS-hosted standalone
viewer requires an HTTPS node. No public default endpoint is configured yet.

The initial viewer supports stable card slots, overwrite feedback, local
TTL countdowns, preview search, full-value inspection, and a mobile column.
Reconnects replace the view with a new snapshot from the same node. Peer
failover, topology refresh, sorting, and theme selection remain later work
in the [stream plan](soph-stream-plan.md).

## Diagnostics

```bash
soph health
soph status
soph topology
soph metrics
```

These pass the node's read-only endpoints through. `health`, `status`, and
`topology` print indented JSON, or compact JSON with `--json`. `metrics`
prints Prometheus text.

## Output formats

Human output is the default. `--json` (a global flag, before the command)
prints one JSON document to stdout. Binary values in JSON mode are carried
as `value_base64` with `"encoding": "base64"`.

```bash
soph --json put greeting --ttl 300 < value
# {"bytes":5,"endpoint":"http://...","generated_key":false,"key":"greeting","network":"lab","observed_current_key":{"key":"greeting","created_at":"...","remaining_seconds":298,"ttl_seconds":300},"quorum_status":"confirmed","ttl_requested":300}
soph --json get greeting
# {"bytes":5,"created_at":"...","encoding":"base64","key":"greeting","remaining_seconds":298,"ttl_seconds":300,"value_base64":"aGVsbG8="}
soph --json exists missing
# {"endpoint":"http://...","exists":false,"key":"missing"}   (exit 3)
```

Flags may appear before or after positional arguments. A literal `--` ends
flag parsing for keys that start with a dash.

Use `soph <command> --help` (or `soph help <command>`) for the command's
arguments and flags. Help exits 0 and does not load config or contact a node.

## Timeouts and cancellation

`--timeout` bounds each request (default 15s). `list --all` applies it per
page. Ctrl-C or SIGTERM cancels a request or a blocked input read. Cancelling
while `put` reads stdin or `--file` exits 4 without sending the value. Input
reading itself has no timeout. If the write request has started but no node
response arrives, timeout or cancellation also exits 4; the write's outcome
is then unknown. Once the node has acknowledged a write, interruption of the
optional metadata read preserves that known write outcome.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Success. A pending write is a success unless `--require-confirmed`. |
| 1 | The node returned an error (for example `507` capacity), or a local failure such as unreadable input or failed output. An output failure can follow a successful write. |
| 2 | Usage: bad arguments, no network selected, unknown network, expired public profile, or a key the node rejects. |
| 3 | The contacted node has no live value for the key. |
| 4 | Connection refused, DNS failure, timeout, or cancellation (including while reading input). |
| 5 | Write stored locally with quorum pending, only with `--require-confirmed`. |

## What the client cannot tell you

Reads, existence checks, and listings describe the contacted node's local
store. A missing key there does not establish that the key is absent from
the network; another node may hold it. Ask a different node if that matters
to you. The client does not aggregate across nodes.

## Node and MCP modes

`soph` is the client only. Run a node with `server`, and an embedded node
for an MCP host with `server --mcp`. See [configuration](configuration.md).
