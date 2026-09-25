# Three-root public test-network bring-up

Prepared for Kraid, Ridley, and Motherbrain. This is the operator procedure;
the intended authority and public roots are not yet activated. Run commands
as the normal login user. Only host administration and omega custody use `sudo`.

## Hosts and public ingress

| Node ID | Host | Public host address | Tailscale address | Signed origin |
| --- | --- | --- | --- | --- |
| `kraid` | DigitalOcean | `134.199.209.61` | `100.98.104.37` | `https://kraid.sopholeth.io` |
| `ridley` | Hetzner | `88.99.36.65` | `100.80.176.69` | `https://ridley.sopholeth.io` |
| `motherbrain` | Operator workstation | Cloudflare Tunnel | `100.86.194.72` | `https://motherbrain.sopholeth.io` |

These placements and hostnames are approved. Each host runs its own root and
tunnel; no root proxies through another root. Cloudflare remains a shared
ingress dependency, and Motherbrain depends on the workstation staying awake.
Tailscale is for administration. Kraid also runs the separate omega renewal
service; root containers never receive signing keys or deployment tokens.

The host inventory on 2026-09-24 found Docker, Compose, and `cloudflared` on all
three machines. Kraid and Ridley run Ubuntu 24.04 and Docker 29.8.1. Motherbrain
has Docker 27.4.1 and already uses port 8080. The new ingress uses **127.0.0.1:18080**
on every host. Docker before 28 has a documented same-L2 exception to localhost
port isolation: LAN peers may reach that ingress. Its request limits still apply;
the node's own port is never published. Record this limitation and schedule the
Docker upgrade with Motherbrain's other workloads. See
[Docker's port-publishing notes](https://docs.docker.com/engine/network/port-publishing/).

For each hostname, configure its own host's Cloudflare Tunnel with:

| Tunnel setting | Value |
| --- | --- |
| Public hostname | `<node-id>.sopholeth.io` |
| Path | Unrestricted (all paths) |
| Service type / URL | **HTTP**, `127.0.0.1:18080` |
| HTTP Host Header | Leave unset; preserve the public hostname |
| Access / service-token requirement | None; this is a public node |
| Cache rule | Bypass cache for the entire hostname |
| Redirects / browser challenges | None on HTTPS API and gossip requests |

The tunnel terminates public TLS. No local certificate or `noTLSVerify` setting
is needed for the loopback HTTP hop. No public 8080/18080 port or Tailscale-only
client membership is required. The DNS record targets that host's tunnel,
not its public IPv4 address. Follow the existing fleet configuration owner for
Kraid/Ridley so manual edits are not overwritten later.
See [published tunnel applications](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/routing-to-tunnel/)
and [cache bypass](https://developers.cloudflare.com/cache/how-to/cache-rules/settings/).

For a locally managed Motherbrain tunnel, add this rule **before** its final
catch-all, retaining the existing routes and tunnel credentials:

```yaml
- hostname: motherbrain.sopholeth.io
  service: http://127.0.0.1:18080
```

For a dashboard-managed tunnel, enter the same hostname and HTTP service there.
A 502 is expected until the local stack starts. Do not route directly to the
node container: the [ingress configuration](examples/public-network/nginx.conf)
enforces the launch limits and excludes unsupported paths.

## Create and back up the intended authority on Kraid

Use the installed `soph` and custody/service-account layout from the successful
[Kraid rehearsal](omega-kraid-rehearsal.md#install-and-prepare). Update the binary
from the reviewed commit first. Keep its disposable timer disabled. The final
authority is **`sopholeth`**, with repository **`https://sopholeth.io/omega`**.
Retain those exact values on retries; never repurpose the rehearsal authority.

Before `init`, request `https://sopholeth.io/omega/timestamp.json` without following
redirects. Expect anonymous JSON 404 with `Cache-Control: no-store`. If metadata
already exists, inspect it instead of initializing another authority.

```bash
make build-soph
sudo install -m 0755 bin/soph /usr/local/bin/soph
curl --max-time 15 -i https://sopholeth.io/omega/timestamp.json
sudo soph --json omega init --home /srv/omega-offline --network sopholeth \
  --repository https://sopholeth.io/omega
sudo soph --json omega status --home /srv/omega-offline --network sopholeth --check-keys
```

Record the initial fingerprint outside this checkout. Back up the encrypted
authority, restore it to a separate private location **before provisioning**, and
run `status --check-keys` on that restored copy. Confirm the same fingerprint.
Keep the passphrase recoverable separately. See the
[backup procedure](omega-operations.md#verify-a-restored-copy).

Review [roots.json](examples/public-network/roots.json), then install and publish
it. Load `VERCEL_TOKEN` from the existing private environment file as in the
rehearsal; never put it in arguments or source control.

```bash
sudo install -m 0644 docs/examples/public-network/roots.json /etc/soph/public-roots.json
sudo soph --json omega provision-renewal --home /srv/omega-offline \
  --network sopholeth --renewal-home /srv/omega-online
sudo --preserve-env=VERCEL_TOKEN soph --json --timeout 5m omega publish \
  --home /srv/omega-offline --network sopholeth \
  --manifest /etc/soph/public-roots.json --version 1 \
  --repository-dir /srv/omega-public/sopholeth \
  --vercel-config /etc/soph/omega-vercel.json
sudo soph --json --timeout 2m omega status --home /srv/omega-online \
  --network sopholeth --verify
```

Expect `publication: verified`. Preserve both homes and the public spool on any
error and retry the same operation after correcting it. Take a fresh backup of
both homes and the repository after this handoff.

## Adopt the bundle and build the release

Copy **only** `/srv/omega-offline/sopholeth/bundle.json` into
`internal/discovery/bundle.json`. Review and commit that public trust adoption;
do not deploy the unconfigured development bundle. Build the node and CLI from
that exact commit on each host, or distribute identical built artifacts.

```bash
read -r -p 'Independently recorded initial fingerprint: ' omega_fingerprint
make check-public-release OMEGA_EXPECTED_SHA256="$omega_fingerprint"
make build-soph
release_commit=$(git rev-parse HEAD)
test -z "$(git status --porcelain)" || { echo 'Use the clean reviewed release checkout'; exit 1; }
docker build -t "sopholeth/public-test:$release_commit" .
node_image=$(docker image inspect "sopholeth/public-test:$release_commit" --format '{{.Id}}')
docker pull nginx@sha256:985220252f3863977e468f611ef118ebd01421289dd86ee1ae99cb068c3bce2b
sha256sum bin/soph
```

Record the source commit, fingerprint, CLI checksum, and local node image ID for
each deployment. The node image contains that checkout's public bundle. Compose
uses the recorded image ID and never silently pulls a newer node build.
The pinned NGINX image is shared by all three hosts.

## Start the root on each host

From the release checkout, set `root_id` to that machine's ID and install the
small [Compose stack](examples/public-network/compose.yml). Docker access is
already available to the operator on these hosts. The processes inside both
containers run with the operator's unprivileged UID/GID and read-only images.

```bash
root_id=motherbrain # Use kraid on Kraid and ridley on Ridley.
root_dir="$HOME/.local/share/sopholeth/root"
install -d -m 0700 "$root_dir" "$root_dir/state"
install -m 0644 docs/examples/public-network/{compose.yml,nginx.conf} "$root_dir/"
cat > "$root_dir/.env" <<EOF
SOPH_ROOT_ID=$root_id
SOPH_UID=$(id -u)
SOPH_GID=$(id -g)
SOPH_NODE_IMAGE=$node_image
SOPH_INGRESS_IMAGE=nginx@sha256:985220252f3863977e468f611ef118ebd01421289dd86ee1ae99cb068c3bce2b
EOF
docker compose --project-directory "$root_dir" -p soph-public-root config --quiet
docker compose --project-directory "$root_dir" -p soph-public-root up -d
docker compose --project-directory "$root_dir" -p soph-public-root logs --tail 40
curl --max-time 15 -i http://127.0.0.1:18080/v1/health
curl --max-time 15 -i "https://$root_id.sopholeth.io/v1/health"
```

Start Kraid, then Ridley, then Motherbrain. The first root can start with the
other signed origins offline. Later roots contact all seeds; periodic recovery
handles missed startup notifications. Allow a minute for topology to settle.
Do not work around a bootstrap error by setting `NODE_PEERS`: that bypasses
verified discovery. Check DNS, tunnel routing, hostname/ID, and logs instead.

Each root uses enclave `default`, replication factor 3, a five-second write
wait, 64 MiB payload capacity, a 512 MiB container memory limit, and one CPU.
The node admits at most **100 KiB (102400 bytes) per value** and **1 KiB per
decoded key** on client and peer writes, and clamps all stored TTLs to five
minutes–24 hours. Oversized writes return 413 without being stored, ACKed, or
forwarded. These are node settings, not permanent protocol size limits.
NGINX allows 128 KiB bodies on `/v1/data/` and 192 KiB on `/v1/gossip/message`,
leaving room for base64 and JSON around a full-size value. Raising the node's
cap also requires raising both ingress limits; budget about 1.4 times the
value cap plus key/metadata overhead on the peer route.

The ingress also bounds header/body idle times, active requests, and worker
connections. Start ordinary probes at 32 KiB or less. These bounds do not
repair the deferred resource/slow-peer audits or prevent storage saturation.
About 655 full-size values still fill a root; they expire according to local
TTL, and overwrites restart that clock. NGINX's
[connection limits](https://nginx.org/en/docs/http/ngx_http_limit_conn_module.html)
count requests after complete headers; the worker and header-timeout limits
also matter. [Proxy retries](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_next_upstream)
are disabled so ingress does not replay a write.

## Enable intended-network renewal on Kraid

Install the existing service/timer if needed, and give **this instance** its
Vercel drop-in. The token file and `soph-omega` account already exist from the
rehearsal. The old disposable timer stays disabled.

```bash
sudo install -m 0644 docs/examples/omega/soph-omega-renewal@.{service,timer} /etc/systemd/system/
sudo install -d -m 0755 /etc/systemd/system/soph-omega-renewal@sopholeth.service.d
sudo install -m 0644 docs/examples/omega/vercel-renewal.conf \
  /etc/systemd/system/soph-omega-renewal@sopholeth.service.d/vercel.conf
sudo systemctl daemon-reload
sudo systemctl start soph-omega-renewal@sopholeth.service
sudo journalctl -u soph-omega-renewal@sopholeth.service -n 20 --no-pager
sudo systemctl enable --now soph-omega-renewal@sopholeth.timer
systemctl list-timers soph-omega-renewal@sopholeth.timer
```

It checks hourly and refreshes daily with seven-day validity. Record the first
actual daily renewal; do not delay initial network testing for that observation.
Use `status --verify` and the reported role expirations for monitoring.

## Check the live network and operate it

From another machine, request every root's `/v1/health` and `/v1/topology` over
HTTPS without `-k` or redirect following. Confirm its exact node ID, public
network/default enclave, and both other roots at their signed origins. Verify
`Cache-Control: no-store`, no cached data responses, and anonymous access.
`/v1/ws`, `/v1/stream`, `/v1/metrics`, and unknown paths must return 404. Port 18080
must be unreachable through the public and Tailscale addresses. Check each
root's write limits with both known-length and chunked requests:

- A 102400-byte client value must be accepted and readable through both other
  roots. Its base64 peer envelope is about 137 KB, below the 192 KiB backstop.
- A 102401-byte value must return 413 on both client and peer PUTs and remain
  absent. For a peer PUT, encode the value as JSON/base64.
- A 1025-byte decoded key, even with an empty value, must return 413 on either
  write path. A 1024-byte key is valid; URL-encode client keys.
- Bodies above 128 KiB on `/v1/data/` and 192 KiB on `/v1/gossip/message` must
  return 413 at ingress. Do not set a uniform 100 KiB body limit: it would
  reject otherwise valid gossip envelopes.
- A peer PUT with a ten-year TTL must store with `X-Original-TTL: 86400`;
  a below-minimum peer TTL must store with `X-Original-TTL: 300`.

Use a fresh profile with the adopted CLI, then explicitly select each root for
cross-node checks. Explicit profiles are routing choices; `soph join` without
an endpoint is the verified-discovery test.

```bash
smoke_dir=$(mktemp -d)
bin/soph --config "$smoke_dir/client.json" join
printf 'hello from public bring-up' | bin/soph --config "$smoke_dir/client.json" put bringup-probe --ttl 300
bin/soph --config "$smoke_dir/client.json" get bringup-probe
bin/soph --config "$smoke_dir/client.json" list
for root_id in kraid ridley motherbrain; do
  bin/soph --config "$smoke_dir/$root_id.json" join "https://$root_id.sopholeth.io" --public
  bin/soph --config "$smoke_dir/$root_id.json" get bringup-probe
done
```

Write a distinct probe through each root and read it through both others.
Check expiry after the five-minute TTL floor. Start an unlisted fourth node
with a distinct ID/origin, the same bundle, `NODE_NETWORK=public`, empty
`NODE_PEERS`, and enclave `default`; give it an anonymously reachable gossip
endpoint. Verify replication both ways without changing the signed manifest.
Then stop that temporary node. Record results and versions in #180/#80.

From a host's `root_dir`, use `docker compose -p soph-public-root ps`,
`logs --tail 50`, `stop`, and `up -d` for routine management. Persist `state/`
across updates; it holds rollback history, not payloads. For an update, pull the
reviewed source, check the same initial fingerprint, build and record its image
ID, update `.env`, then `up -d --force-recreate`. Upgrade one root at a time.
Restart loses that node's payloads; rejoining does not backfill them. Check new
writes after each restart. A `201` reflects the current acknowledgment threshold,
not a guarantee of three independent copies.

See the [launch plan](public-network-plan.md#4-verify-the-healthy-network-then-test-its-failures)
for fault testing after the healthy path works. No shared peer secret, join
allowlist, or write identity is introduced by this deployment.

## Local validation record

On 2026-09-24, a disposable Docker network exercised node/CLI source `0044c83`
with a freshly created throwaway omega bundle. The Compose limits and ingress
configuration above ran as unprivileged users with read-only images. Test-only
origins, a local TLS frontend/CA, and dynamic loopback ports stood in for the
public tunnels. All three processes cold-started sequentially and learned the
other roots. Verified `soph join`, writes through each root, cross-root reads,
listing, `no-store`, excluded-route 404s, and known-length/chunked 413s passed.
An unlisted fourth node joined and replicated both ways, while its bootstrap
endpoint correctly returned 403. Test containers were removed afterward.

This does not validate Cloudflare routing, host firewalls, live TLS, TTL expiry,
or the daily renewal timer on these hosts. Those checks remain part of activation.
