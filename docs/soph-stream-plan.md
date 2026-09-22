# soph.stream implementation and launch plan

Status: Phase 1 implemented for local review, with the initial `soph serve`
from Phase 2. Local validation is documented in
[the stream test guide](../test/stream/README.md). Multi-node viewer failover,
remote staging, and public launch remain pending.

Sequencing update, 2026-09-22: the [public-network plan](public-network-plan.md)
takes priority. Build `soph omega`, then rehearse and stand up three public
roots in the `default` enclave using the existing CLI/viewer. The phases below
retain viewer feature scope and acceptance criteria; completing all Phase 2
polish is no longer a prerequisite for remote root work. MCP is deferred, and
the editor-proxy connection problem is parked.

The first deliverable is a live view of real Sopholeth data: write with
`soph put`, see a card appear, overwrite it in place, and watch it expire.
The public website and `soph serve` will share that experience.

The [discovery feature list](soph-stream-features.md) and
[simulated demo](soph-stream-demo.html) provide product and interaction
context. This plan incorporates the current repository and CLI behavior.

## Agreed direction

- Keep the existing Sopholeth aesthetic, using the current
  [site](../sites/soph.stream/index.html) and
  [styles](../sites/soph.stream/styles.css) as the starting point.
- Keep the CLI's named networks, current selection, and explicit overrides.
  Extend saved profiles with topology as needed.
- Implement a node-native `GET /v1/stream` SSE endpoint. The standalone
  browser viewer talks directly to the selected node. `soph serve` serves
  the same assets and forwards reads to its startup node, allowing HTTPS
  port forwarding without exposing a second port to the browser.
- Show the contacted node's local view, with its identity, enclave, and
  connection state visible. Counts and empty states have that same scope.
- Preserve actual local TTL behavior, including the five-minute minimum.
  Use controlled time in automated tests; exercise real expiration in
  interactive and cluster testing.
- Keep the viewer read-only, with URL-based state and no payload history,
  browser persistence, or analytics. Render payloads as text or a binary
  preview, never as executable markup.
- Use the existing `NODE_*` convention for node configuration, including
  `NODE_STREAM=off` to disable streaming.

## Phase 1: one local node and a working viewer

Build the smallest complete interaction using the actual Go node and CLI.
This phase establishes the stream contract and proves the visual experience.

### Deliver

- A stream snapshot containing the node's live local entries, node ID,
  enclave, and current node time.
- Live events for accepted local writes, including overwrites and writes
  arriving through replication. Add advisory expiration events from cleanup.
  Observe storage acceptance so the feed covers every ingestion path.
- Per-entry key, base64 payload preview, truncation flag, original size,
  applied local TTL, local creation time, and local expiration time. Limit
  payload previews to 4 KiB; fetch the current full value on demand.
- A defined snapshot-to-live handoff: writes during snapshot creation must
  reach the viewer in the correct order. An expiration event for an older
  value must not remove its replacement.
- Bounded subscriber queues and connection/resource limits. Slow viewers
  disconnect and receive a fresh snapshot on reconnect; socket writes must
  not hold storage locks or make storage wait for a viewer.
- A minimal page with stable card slots, overwrite feedback, payload
  preview, TTL bars, detail view, overflow count, mobile layout, and reduced
  motion support. Match the existing site aesthetic.
- Explicit connecting, connected, disconnected, and empty states. Reconnect
  replaces the previous view with a fresh snapshot. Account for browser/node
  clock differences when displaying remaining lifetime.
- A basic explicit node URL so the same page can be tested against different
  local endpoints. Confirm browser access for the stream and full-value read
  using the existing CORS behavior.

Full-value reads are later observations: a key may expire or be overwritten
after its preview arrives. The detail view must handle both without claiming
the fetched bytes belong to an earlier event.

### Acceptance gate

- `soph put` creates a card; an overwrite updates that card without moving
  it; expiration removes it without a page refresh.
- Joining the stream during active writes and reconnecting after missed
  events both produce a correct current view.
- Empty values, binary data, oversized previews, and expiration during a
  detail fetch have defined behavior.
- Tests cover snapshot ordering, overwrite/expiration races, cancellation,
  and slow-consumer handling. Compare write behavior with and without viewers.
- Review the experience at real TTLs before expanding the UI. Adjust this
  first slice if the live behavior is not yet compelling.

## Phase 2: local cluster, CLI integration, and complete viewer

Use three real nodes in the same enclave. Add a separate-enclave node for
isolation scenarios. The existing [Compose setup](../docker-compose.yml)
has two nodes in one enclave and a third in another; add a dedicated test
configuration or override instead of assuming all three replicate together.

### Deliver

- A repeatable local start/stop and workload procedure. Write through one
  node while watching another; include overwrites, bursts, and natural expiry.
- `soph serve`, defaulting to loopback port 8181, with `--node`, `--port`,
  `--bind`, `--open`, and `--q`. Use the selected named network when present;
  make explicit overrides and the localhost fallback clear in help.
- One maintained viewer implementation packaged for the static site and
  embedded in the CLI. Preserve the independent Vercel site root; document
  and verify how the two distributions receive identical assets.
- Peer metadata scoped to each saved network: selected endpoint, last-used
  node, enclave, and known peers. Validate identity and enclave before using
  a failover candidate. Root flags describe discovery roles, not authority
  over stored values.
- Viewer failover within the selected network's enclave: last-used node,
  then known roots, then other peers. Exhaustion produces a clear reconnect
  or rejoin state. Every node switch rebuilds from that node's snapshot.
- Bounded topology refresh during successful CLI interactions, with no
  background daemon. A failed metadata refresh must not obscure an already
  acknowledged write. Preserve the CLI's rule against automatic PUT retries;
  broader command failover needs explicit operation-specific behavior.
- Live search, supported sort modes, and URL state for node, query, sort,
  and theme. Define `--q` consistently with the page's query semantics.
  Searching preserves grid slots; leaving sorted mode may rebuild them.

### Acceptance gate

- Writes accepted on node A appear in node B's stream after replication.
  Display each node's actual local TTL, including differences caused by delay.
- Node shutdown, restart, browser disconnect, and peer failure recover through
  a fresh snapshot. Failover stays inside the selected enclave and profile.
- Partition/healing tests reflect current protocol behavior: healing alone
  does not promise to backfill missed values.
- CLI profile switching remains intact. Both site and embedded viewer pass
  the same interaction checks, including keyboard use and reduced motion.
- Keep race tests green and record cluster scenarios, faults, and results.

## Phase 3: remote endpoints and hosted staging

Remote test endpoints are available from the project owner for this phase.
Their addresses and deployment details will be recorded during the three-root
rehearsal. Viewer polish does not gate preparing those endpoints; discovery
and root-facing correctness requirements follow the public-network plan.

### Deliver

- An endpoint inventory covering URL, running version, network/enclave,
  browser-reachable peer addresses, and any proxy or TLS termination.
  Select a disposable test network and define the intended fault/load runs.
- A hosted preview of the static viewer connected to the remote test nodes,
  plus `soph serve` exercised against those same nodes.
- Verification of HTTPS, browser CORS, stream flushing, proxy buffering,
  idle timeouts, keepalives, reconnect behavior, and reachable failover URLs.
  Container-internal peer names must not silently become browser targets.
- A repeatable remote workload and fault run, followed by a soak. Set the
  workload, duration, viewer count, resource budgets, and pass/fail thresholds
  before running it; use the [validation harness](../test/burnin/README.md)
  where applicable.

### Acceptance gate

- Both viewer distributions work through the intended hosting path and
  recover from idle disconnects and node loss.
- Slow or disconnected browsers do not accumulate unbounded resources or
  prevent writes from completing.
- Record the commit, configuration, topology, workload, fault timings,
  latency, write outcomes, resource use, and recovery results. Keep payloads
  out of operational logs and metrics.
- Resolve failures exposed remotely before public rollout. Private staging
  demonstrates the application path; public discovery has its own launch
  checks in the next phase.

## Phase 4: public network and soph.stream launch

Complete the [public-alpha gates](roadmap.md#before-public-alpha) alongside
the final staging work. The stream provides an additional observation tool;
network correctness and load evidence also come from tests and the workload
driver.

### Deliver

- Validated quorum, replication, enclave, lifecycle, and discovery-expiration
  behavior with the required multi-node failure and sustained-load evidence.
- The working `soph omega` suite, a production public trust bundle, three
  independent reachable roots, verified publication, monitoring, and operator
  recovery procedures. Follow the [public-network plan](public-network-plan.md);
  [omega operations](omega-operations.md) remains an interim reference until
  the production workflow replaces it.
- Published release artifacts and an HTTPS stream endpoint for the intended
  public enclave, with tested resource limits and the stream disable switch.
- The viewer at `soph.stream`, configured to use that endpoint by default,
  and a matching released `soph serve`. Retain explicit node selection and
  visible node/enclave identity.
- Updated site copy, CLI/API documentation, and release notes describing
  shipped behavior and observed limits.

### Acceptance gate

A fresh client can discover and join the public network, write a value,
observe its replication through soph.stream, and observe local expiration.
The deployed viewer survives a tested node interruption and clearly reports
the node it subsequently observes. Public-alpha validation evidence and
operator procedures accompany the release.

## Work that can proceed alongside local development

Phase 1 and initial `soph serve` are available. The next implementation
milestone is the omega suite, followed by the three-root runbook and remote
rehearsal. Use the viewer to observe those nodes and address failures on that
path. Broader viewer features can follow without becoming prerequisites for
the trust and network work.
