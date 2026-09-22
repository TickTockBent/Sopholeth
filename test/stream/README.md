# Local stream validation

The first slice uses a real Go node, the `soph` CLI, and the same viewer assets
that the standalone site deploys. There is no simulated feed in the viewer.

## Run locally

From the repository root:

```bash
make build
NODE_NETWORK=private NODE_ID=stream-local NODE_ENCLAVE=stream-lab \
  NODE_HTTP_PORT=18080 NODE_REPLICATION=1 NODE_MAX_STORAGE_MB=50 \
  NODE_INBOUND=true NODE_RATE_LIMIT=1000 ./bin/server
```

The current node listener binds all interfaces, as described in
[configuration](../../docs/configuration.md). Use an isolated CLI config for
this lab. In a second terminal:

```bash
./bin/soph --config /tmp/soph-stream-local/soph.json \
  join localhost:18080 --name stream-local
./bin/soph --config /tmp/soph-stream-local/soph.json serve --open
```

Open the printed URL (port 8181 by default). When using an HTTPS development
proxy, open its forwarded viewer URL, such as
`https://editor.example/proxy/8181/`. The CLI supplies the selected node even
without query parameters, and forwards reads through that same viewer port.
In another terminal:

```bash
printf 'Hello from soph put' | ./bin/soph \
  --config /tmp/soph-stream-local/soph.json put hello --ttl 300
./bin/soph --config /tmp/soph-stream-local/soph.json get hello
./bin/soph --config /tmp/soph-stream-local/soph.json exists hello
./bin/soph --config /tmp/soph-stream-local/soph.json list
printf 'Updated in place' | ./bin/soph \
  --config /tmp/soph-stream-local/soph.json put hello --ttl 300
```

The card should appear, update without moving, and disappear five minutes
after its last write. A subsequent `get` should exit 3. Also exercise an empty
value, a binary file, and a value exceeding the 4096-byte preview cap. Opening
the latter's details should fetch the full current value.

For the standalone site, run a local static server:

```bash
python3 -m http.server 3000 --bind 127.0.0.1 --directory sites/soph.stream
```

Open `http://localhost:3000/?node=http://localhost:18080`. Stop and restart the
node while the page is open: it should show disconnection, reconnect, and
replace the old view with the restarted node's empty snapshot. Ctrl-C stops
each development process. Configuration contains named connections, never
payloads; the node's in-memory values disappear when it stops.

## Automated checks

```bash
make build
go test -race ./...
go test ./internal/storage -run '^$' -bench BenchmarkPutWithStream -benchmem
```

The storage/server tests cover the snapshot-to-event handoff under concurrent
writes, revision-specific expiration, rejected writes, preview truncation,
oversized snapshots, slow subscriber eviction, connection limits, replicated
writes, stream shutdown, and CORS. CLI tests cover viewer assets, selected
profiles and overrides, help, JSON output, and cancellation. They also cover
read forwarding, encoded keys, upstream error responses, exclusion of proxy
credentials and write operations, and shutdown with an active stream.

[viewer.cjs](viewer.cjs) drives the actual static files in Chromium using a
controlled SSE fixture. It covers overwrite positions, stale events, preview
filtering, full-value reads and 404s, binary/empty/HTML payloads, clock-aware
expiration, snapshot replacement on reconnect, overflow, mobile resizing,
and reduced motion.

With Playwright and its Chromium installed in your development environment:

```bash
node test/stream/viewer.cjs
node test/stream/proxy.cjs
```

For an existing installation outside this repository, set `PLAYWRIGHT_MODULE`
to its module path. Set `CHROMIUM_PATH` to use an existing Chromium executable.
The fixture listens on a disposable loopback port. Its clock advancement
tests browser expiry without changing the production TTL floor. Run the
real-node five-minute check above as well.

[proxy.cjs](proxy.cjs) runs the built `bin/soph` behind an HTTPS proxy mounted
at `/proxy/8181/`. It requires `openssl` to generate a temporary test
certificate. Chromium checks styling, startup selection without a query,
streaming updates and reconnects, full-value reads, navigation, and CLI
shutdown. It verifies that browser requests stay under the HTTPS proxy path.

## Scope of evidence

The initial local run on 2026-09-22 passed `make build`, the full Go race
suite, and the Chromium fixture checks. Against a real node on port 18080,
the CLI passed named join, put/get, binary and empty round trips, listing,
existence, missing-key exit codes, and overwrite checks. Chromium observed
those writes through `soph serve`, verified stable overwrite placement and
full-value reads, and watched a real 300-second value disappear; the node
then returned `404` for that key.

On this development host, the 4 KiB storage microbenchmark measured roughly
0.74 microseconds/write without viewers, 1.50 with one drained subscriber,
and 1.91 with eight. Streaming added one 4 KiB preview allocation per write,
shared across subscribers. These are local microbenchmark observations,
not throughput or latency guarantees for a deployed network.

These checks validate the initial local slice and embedded viewer. The
storage microbenchmark compares writes with zero, one, and eight drained
subscriber queues; it is not a network load test. The next plan phase adds
multi-node faults, same-enclave peer failover, and comparable end-to-end load
measurements. Remote endpoints, HTTPS hosting, signed public discovery, and
public-alpha soak evidence remain separate gates in the
[implementation plan](../../docs/soph-stream-plan.md).
