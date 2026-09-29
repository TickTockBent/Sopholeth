# Sopholeth public discovery

The Go node and `soph` use omega's HTTPS/TUF metadata. The embedded public
bundle identifies the intended `sopholeth` test-network authority at
`https://sopholeth.io/omega`. See the [authority record](public-network-authority.md)
for its initial fingerprint and deployed artifacts. All three roots are running;
the bundle alone does not publish metadata or start nodes.

## Trust boundary

Omega identifies the network and its official bootstrap roots. A release
contains the public bundle: network name, fixed HTTPS repository URL, and
initial signed TUF root. The durable trust client follows signed rotations and
verifies the `bootstrap.json` target. TLS authenticates connections to the
approved root origins. Metadata and node requests refuse redirects.

Any compatible node may join and gossip. The CLI needs no identity or admission
step, and writes remain anonymous. Peer IDs and advertised origins are routing
information; referrals are not certified identities or trusted voters. Existing
peer traffic and payload TTLs continue independently of discovery expiration.

For listed root IDs, the node pins the origin and enclave from verified
discovery, including roots it has not contacted yet. Bootstrap requests,
bootstrap referrals, and SYNC cannot contradict those bindings. A new verified
manifest can move a root's route. Pins survive expiry and failed refreshes;
the official role and seeds still require a current valid view. Removing a root
from the manifest leaves any established ordinary peer entry in place.

Unsigned advertisements also cannot change an established ordinary peer's
origin or enclave. PONG supplies a liveness hint only, never a route update.
An ordinary node moving endpoints or enclaves should use a fresh node ID, or
wait for the old entry to be evicted before rejoining. Ordinary IDs remain
unauthenticated first claims after eviction or restart; this protects existing
routes without certifying joiners or adding admission requirements.

The [trust client reference](../internal/trust/bootstrap/README.md) specifies
bundle/manifest formats, rollback protection, supported filesystems, and
rotation behavior. [Omega operations](omega-operations.md) describes custody,
publication, daily renewal, and seven-day timestamp validity.

## Adopting the public bundle

After creating the intended authority and independently recording its initial
TUF root fingerprint, copy **only its public `bundle.json`** into
[`internal/discovery/bundle.json`](../internal/discovery/bundle.json). Review that
public source change as the network's explicit trust adoption. Do not copy
private keys or authority homes into this repository.

```sh
install -m 0644 /path/to/public/bundle.json internal/discovery/bundle.json
make check-public-release OMEGA_EXPECTED_SHA256=<independently-recorded-fingerprint>
make build
```

The gate compares the embedded bundle's initial TUF-root fingerprint with the
independent record. It no longer hashes the legacy DNS signing key. The checked
bundle is embedded in both `bin/server` and `bin/soph`, including Docker builds
from the same source. Builds from this checkout carry the intended public
authority. A deliberately unconfigured `{}` bundle rejects public discovery;
private connections still work. Bundle adoption and the
actual host setup belong to the [bring-up runbook](public-network-bringup.md).

The initial bundle stays fixed through ordinary signed key rotations. A network
reset needs a deliberately adopted new bundle/build and fresh state; a saved
public profile never silently switches authorities. A runtime bundle-override
command is follow-up work (#231).

## Node startup and transport

With `NODE_NETWORK=public` and no `NODE_PEERS`, the node:

1. Validates the embedded public bundle.
2. Opens its private durable trust directory and attempts a bounded HTTPS refresh.
3. Uses only a freshly verified or still-valid durable manifest. No valid view
   means startup fails; corrupt or incompatible state is not silently reset.
4. Recognizes its official root role only when `NODE_ID`, `NODE_HTTP_ORIGIN`,
   and enclave match a listed root. Non-roots can join normally.
5. Bootstraps over the signed HTTPS origins. A root's response must advertise
   its signed ID, HTTPS origin, and enclave. Other entries remain ordinary
   peer referrals.

`NODE_HTTP_ORIGIN=https://root.example` is the externally reachable origin,
including a nonstandard port when needed. It is independent of the backend
`NODE_HTTP_PORT`. Set it for each root and for any other node whose API/gossip
listener is behind a TLS proxy. The `http_origin` field travels through HTTP
bootstrap and gossip messages. Outgoing gossip preserves that origin; nodes
without it retain the explicit legacy `http://address:http_port` route.
A malformed explicit origin fails rather than falling back to plaintext.
Use matching builds: older peers do not preserve this field.

Explicit HTTP(S) URLs use their scheme's default port; bare addresses still
use HTTP port 8080. Neither bootstrap nor gossip follows redirects. System CA
and hostname verification stay enabled. The signed origin authenticates the
bootstrap connection, not the honesty of subsequent peer advertisements.

In public mode, a node with no current official root role returns `403` from
`POST /v1/bootstrap`. Private nodes answer without that gate. `NODE_PEERS`
remains a deliberate unverified override and never grants a root role.
Automatic outbound WebSocket attachment is disabled in public mode pending the
separate transient/WSS work; exclude unsupported WS ingress during bring-up.

## Durable state and runtime refresh

Nodes store discovery under `$NODE_STATE_DIR/discovery`, defaulting to
`$HOME/.sopholeth/state/discovery`. Use a persistent directory owned by the
service account. It contains public metadata and rollback history, not signing
keys or application payloads. Preserve it across restarts and deployments.
There is no root-owned fallback directory. Legacy `NODE_CACHE_DIR` and
`root-list.json` are not consumed by the node's HTTPS path.

After a successful refresh, nodes check again hourly, sooner near expiry.
Failures retry after 30 seconds, doubling to at most ten minutes; retries do
not wait another normal refresh period. Failed fetches retain only a
revalidated, still-valid durable view. Verified root or targets transitions
withdraw the previous view before any subsequent download can stall.

Every root-role and recovery-seed lookup checks the accepted deadline without
waiting on network or disk I/O. At the deadline, the official role and seeds
are unavailable even during a blocked refresh. A valid later refresh restores
them. This does not evict ordinary peers or invalidate stored values.

## CLI and viewer

`soph join` discovers a reachable official root, verifies its health response
against the signed ID/enclave, and saves a client profile. It starts no node or
background service. An explicit `soph join <node>` continues to work through
any reachable compatible node; it does not claim omega verification.

Public profiles bind to the initial bundle fingerprint, network, and repository.
Each command revalidates durable state in `<config-file>.trust`; hourly checks
or an unusable cached view trigger bounded refresh. Saved endpoint lists and
expiry fields are informational, never a substitute for verified metadata.
The client prefers the selected root while it remains listed and healthy,
then probes other current roots. It does not switch to another saved network.
Legacy DNS profiles require an explicit new `join`. A saved public profile
whose trust directory is missing reports the missing rollback history instead
of silently initializing it again.

Discovery and health selection finish before a data request. No failure or
renewal implicitly retries a PUT, and no metadata work after acceptance can
replace its reported outcome. Discovery rotation does not require manual
rejoining. Private and explicitly supplied public connections do not acquire a
metadata-expiry requirement.

`soph serve` uses this same saved-profile path and retains the network selected
at startup, even if another command changes the current profile. For verified
public profiles it refreshes in-process, suspends upstream requests during the
check, and cancels existing streams on refresh or expiry. Reconnection receives
a fresh node-local snapshot. Failed verification leaves the viewer unavailable
until valid discovery returns; it does not select an unverified endpoint.

## Validation and remaining scope

Local fixtures cover actual omega publication through TUF discovery, saved CLI
profile refresh, HTTPS bootstrap and gossip to an ordinary unlisted node,
root/ordinary peer route-substitution rejection and signed root-route updates,
certificate/hostname and redirect rejection, runtime expiry during a stalled
refresh, retry scheduling, and viewer stream withdrawal/reconnection. Existing
trust tests cover signatures, rotation, rollback, and durable writes.

The [live testnet checks](public-network-validation.md) additionally cover
three-root operation, public HTTPS ingress, fresh `soph join`, replication, and
an unlisted node. Host failures and sustained load remain to be tested.
Native Windows public discovery is still
unsupported pending its storage backend. The standalone dashboard remains on
the disabled legacy DNS path and is outside this test-network milestone; its
explicit private seeds remain available. Historical DNS code and the legacy
burn-in signer are not public-network fallbacks.
