# soph.stream + `soph serve` + `soph join` — feature list

Side quest inside Sopholeth. One static page, one node endpoint, two CLI commands. Reference build: soph-stream-demo.html.

## Node: `GET /v1/stream` (SSE)
- `snapshot` on connect: every live key in the local store, plus `now`, node id, enclave.
- `put` on every accepted write, including overwrites and gossip-replicated ones.
- `expire` on TTL sweep. Advisory; the page counts down on its own clock.
- Soph shape: key, payload (base64), truncated flag, size, ttl, written_at, expires_at, node.
- Payloads cut at 4 KB in the stream; full value via `GET /v1/data/<key>` on demand.
- CORS `*` on this endpoint only. Never blocks writes. Slow consumer gets dropped; it reconnects and gets a fresh snapshot.
- `SOPH_STREAM=off` disables it.

## Page
- Viewport-sized slot grid, 3:2 cards, no default sort. New soph → random empty slot. Expired soph → fades, frees slot. Overflow waits in arrival order; footer shows the count.
- Card: key (one line), payload (text only, three lines, hex preview if not UTF-8), TTL bar + `hh:mm:ss`. Bar goes late-colored under 20%.
- Overwrite = same slot, one flash. Never moves.
- Click → full metadata and payload.
- Search filters keys and payloads live; filtered-out sophs keep their slots.
- Sort (expiring first / last, newest, largest) switches to a scrollable list. Back to no-sort rebuilds the grid; positions are not preserved.
- Mobile: single scrollable column, no slots.
- Empty state: "The network is empty. That is its default state."
- Reconnect: discard everything, rebuild from snapshot. On failure, walk the enclave peer list and connect to the next one; dialog shows which node you're on.
- URL carries all state: `?node=`, `?q=`, `?sort=`, `?theme=`. Nothing in browser storage. No history, no analytics, no writes.
- Reduced motion respected. Dark default, light optional.

## CLI: `soph join <host:port>`
- Hits `/v1/status`, pulls topology, saves: target, its enclave, and the peer list with root flags. The only thing the CLI keeps on disk.
- Every command refreshes the saved topology on success. No daemon, no background refresh.
- Failover order: last-used node, then roots, then any peer. Same enclave only, always. No same-enclave peer answers → fail and say "run `soph join` again."
- One target at a time. Joining again replaces it.

## CLI: `soph serve`
- Serves the embedded page on `localhost:8181`, pointed at the joined target (or `--node` override, or localhost).
- Hands the page the enclave peer list for failover.
- Flags: `--node`, `--port`, `--q <prefix>`, `--open`, `--bind`.
- Prints one link and the node/enclave it's watching, then nothing.
- Proxies nothing. Browser talks to the node directly.

## Node: topology change
- `TopologyPeer` gains `root: bool`, true for peers learned from bootstrap config. Relative to the reporting node.

## Public deploy
- soph.stream = the page on a static host, `?node=` defaulting to a Clocktower node in `default`. It shows that node's view; the dialog says so.
- Needs a public HTTPS stream endpoint and SSE keepalive that survives the host's idle timeout.

## Build order
1. Stream endpoint + page against a local node. Stop here if the live feed isn't more compelling than the sim.
2. `soph join` + `soph serve`.
3. URL state, sort, search, failover.
4. Public deploy.

## Still open
- Stream in the node vs. `serve` polling `/v1/keys`. Node-native assumed above.
- "soph" as both command and unit noun.
- `REPRAM_*` → `SOPH_*` env prefix cutover.
