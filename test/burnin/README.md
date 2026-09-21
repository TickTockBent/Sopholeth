# Sopholeth validation harness

This directory contains load generators, lab helpers, and historical
observations. The scripts still use legacy identifiers and some assume the
original private lab. They need an implementation pass before they form a
portable public-alpha validation suite.

## Available tools

| File | Purpose and current limits |
| --- | --- |
| [workload.js](workload.js) | k6 mixed reads and writes, reference keys, and expired-key probes. |
| [ramp-to-failure.js](ramp-to-failure.js) | k6 request-rate and payload-size ramps; `TTL_MODE=short` is clamped by the node's five-minute floor. |
| [run-segments.sh](run-segments.sh) | Segmented soak runner with a hardcoded checkout path, lab endpoints, and Prometheus defaults. |
| [setup-keypair.sh](setup-keypair.sh) | Creates a disposable lab signing key under the legacy home-directory path. |
| [build-images.sh](build-images.sh) | Builds the Go test-key image using [Dockerfile.go-node](Dockerfile.go-node); the TS image has been removed. |
| [sign-loop.sh](sign-loop.sh) | Lab-only online signing and dnsmasq restart loop; modifies host service configuration. |
| [snapshot.sh](snapshot.sh) | Captures metrics from hardcoded lab hosts. |
| [ramp-pprof-capture.sh](ramp-pprof-capture.sh) | Profiling helper; review targets and output paths before use. |
| [prometheus-scrape.yml](prometheus-scrape.yml) and [grafana-dashboard.json](grafana-dashboard.json) | Historical monitoring configuration that needs target and metric review. |

Use [omega operations](../../docs/omega-operations.md) for the production
signing workflow. A test-key image is not a public release.

## Small local workload

With Go and k6 installed, start a disposable private node from the repository
root. The higher request limit accommodates the setup writes:

```bash
go build -o bin/soph ./cmd/repram
REPRAM_NETWORK=private REPRAM_MAX_STORAGE_MB=50 REPRAM_RATE_LIMIT=1000 \
  ./bin/soph
```

In another terminal, from the same checkout:

```bash
k6 run \
  -e REPRAM_NODES=http://localhost:8080 \
  -e BURNIN_DURATION=1m \
  test/burnin/workload.js
```

Setup writes 200 reference keys and 200 graveyard keys, then waits six minutes
for the graveyard to expire before the timed workload. This is a local
exercise, not a replication or public-network validation.

The reference set requests a 72-hour TTL, which normal nodes clamp to the
default 24-hour maximum. A longer soak must configure a matching maximum or
change the workload to refresh reference keys. Do not skip setup on a fresh
cluster. When testing multiple nodes, use one enclave; the repository's
Compose configuration deliberately separates node 3.

The segmented wrapper continues after k6 threshold failures, so a completed
wrapper run does not by itself mean validation passed. Preserve and inspect
each segment's results.

## Evidence required for launch

The next all-Go run must include multiple substrates and transient clients,
with parent failure, root changes, partitions, recovery, and capacity
pressure. Record the commit, node configuration, topology, workload settings,
fault timings, and acceptance criteria before starting.

Capture driver results as well as node metrics: request rates, response
codes, latency, quorum outcomes, dropped iterations, process memory, and
recovery time. Preserve the actual accepted TTLs. Allocation totals and
process uptime cannot substitute for request or correctness measurements.

See the [roadmap](../../docs/roadmap.md#before-public-alpha) for the complete
launch gates. No new burn-in result is claimed by this documentation pass.

## Historical evidence

- [May 2026 mixed Go/TS report](archive/2026-05-burnin.md): observations from
  the retired mixed implementation, with evidence limits stated explicitly.
- [April 2026 raw artifacts](archive-2026-04-25/): original metrics and logs
  from an earlier run. These are distinct from the May report.

Raw artifacts retain their original metric and project names. The old
host-by-host command transcript has been removed; it depended on personal
infrastructure and the deleted TypeScript node.
