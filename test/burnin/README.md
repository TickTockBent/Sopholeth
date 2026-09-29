# Sopholeth validation harness

This directory contains reusable load generators, metrics capture, and lab
helpers with explicit targets and state directories. The signing loop still
requires a dedicated dnsmasq lab host. Choose test scenarios from the
[network plan](../../docs/public-network-plan.md).

## Available tools

| File | Purpose and current limits |
| --- | --- |
| [workload.js](workload.js) | k6 mixed reads and writes, reference keys, and expired-key probes. |
| [ramp-to-failure.js](ramp-to-failure.js) | k6 request-rate and payload-size ramps; `TTL_MODE=short` is clamped by the node's five-minute floor. |
| [run-segments.sh](run-segments.sh) | Segmented Docker/k6 soak runner; accepts `BURNIN_NODES`, `BURNIN_STATE_DIR`, and a Prometheus remote-write URL. |
| [setup-keypair.sh](setup-keypair.sh) | Creates a disposable lab signing key under `BURNIN_STATE_DIR`. |
| [build-images.sh](build-images.sh) | Builds the Go test-key image using [Dockerfile.go-node](Dockerfile.go-node); the TS image has been removed. |
| [sign-loop.sh](sign-loop.sh) | Lab-only online signing and dnsmasq restart loop; modifies host service configuration. |
| [snapshot.sh](snapshot.sh) | Captures metrics from `BURNIN_NODES`; writes to `SNAPSHOT_LOG` or the state directory. |
| [ramp-pprof-capture.sh](ramp-pprof-capture.sh) | Captures Go profiles from comma-separated `PPROF_TARGETS`; requires enabled node profiling listeners. |
| [prometheus-scrape.yml](prometheus-scrape.yml) and [grafana-dashboard.json](grafana-dashboard.json) | Go metrics dashboard and host-side scrape targets for the three-node Compose setup; adjust targets for other deployments. |

`BURNIN_NODES` defaults to `http://localhost:8080`; `BURNIN_STATE_DIR` defaults
to `$HOME/.local/state/sopholeth/burnin`. Profiling defaults to
`http://127.0.0.1:6060`. The signing loop requires `PRIVATE_KEY`, `ROOT_NODES`
(`host:http-port` entries), and `DNSMASQ_HOSTS_FILE`; `BOOTSTRAP_NAME` and
`BOOTSTRAP_TARGET` select its DNS records.

Use [omega operations](../../docs/omega-operations.md) for the production
signing workflow. A test-key image is not a public release.

## Small local workload

With Go and k6 installed, start a disposable private node from the repository
root. The higher request limit accommodates the setup writes:

```bash
go build -o bin/server ./cmd/server
NODE_NETWORK=private NODE_MAX_STORAGE_MB=50 NODE_RATE_LIMIT=1000 \
  ./bin/server
```

In another terminal, from the same checkout:

```bash
k6 run \
  -e BURNIN_NODES=http://localhost:8080 \
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

## Recording a run

Record the commit, configuration, topology, workload, fault timings, and expected
outcomes before starting. Capture driver results and node metrics: response
codes, latency, quorum outcomes, dropped iterations, process memory, recovery
time, and actual accepted TTLs. Allocation totals and uptime cannot substitute
for request or correctness measurements.

Use the [network plan](../../docs/public-network-plan.md) to select a bounded
scenario. Put raw logs, metrics, profiles, and screenshots outside the checkout
under `BURNIN_STATE_DIR` or in ignored `test-results/`. Summarize measured results
and reproduction steps in the relevant issue; link large artifacts separately.
The retired mixed Go/TypeScript reports and raw logs remain in Git history.
