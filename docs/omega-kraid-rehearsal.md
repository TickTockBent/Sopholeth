# Kraid operator rehearsal

Run this on Kraid after the hosted-rehearsal fixes are merged. It uses the normal
operator commands and encrypted **disposable** custody. It does not create the
public authority. No agent access to Kraid is needed.

The metadata project currently serves only the earlier disposable rehearsal.
Only one publisher may use it during this handoff. Stop this rehearsal before
publishing the final authority: each deployment replaces the project's served
repository. Use the final `/omega/` path only for the intended public authority.

## Install and prepare

Use a current checkout on Kraid with Go 1.27.1, a working system clock, and
outbound HTTPS. Run these commands from the checkout as your normal login user
with sudo access. `make build-soph` builds for the machine's own architecture;
the installation and administration commands explicitly use `sudo`.

```bash
make build-soph
sudo install -m 0755 bin/soph /usr/local/bin/soph
id soph-omega >/dev/null 2>&1 || sudo useradd --system --no-create-home --shell /usr/sbin/nologin soph-omega
sudo install -d -o root -g root -m 0700 /srv/omega-offline
sudo install -d -o soph-omega -g soph-omega -m 0700 /srv/omega-online
sudo install -d -o soph-omega -g soph-omega -m 0755 /srv/omega-public
sudo install -d -o root -g root -m 0755 /etc/soph
sudo install -m 0644 docs/examples/omega/vercel.json /etc/soph/omega-vercel.json
```

Create `/etc/soph/omega-vercel.env` as root, mode `0600`, and enter
`VERCEL_TOKEN=<a deployment token for the configured Vercel team>` using an editor.
Do not put the token in shell arguments, Git, or chat. These Bash commands open
the file for editing, then load it into your shell through sudo without printing
it. The publish command below preserves only `VERCEL_TOKEN` across sudo:

```bash
sudo touch /etc/soph/omega-vercel.env
sudo chown root:root /etc/soph/omega-vercel.env
sudo chmod 0600 /etc/soph/omega-vercel.env
sudoedit /etc/soph/omega-vercel.env
set -a
. <(sudo cat /etc/soph/omega-vercel.env)
set +a
```

The root-owned operator home stores encrypted authority keys outside the
service account's access. The online home will contain only renewal keys and
publication history. This follows the connected-host launch custody profile;
there is no air-gap requirement. The homes must use stable local paths: copying
the online journal to another machine does not migrate its binding.

## Create and publish disposable authority

Use this reserved rehearsal URL, retaining it on retries. Check it without
following redirects; before publication it must return JSON 404 with
`Cache-Control: no-store`.

```bash
curl -i https://sopholeth.io/omega/rehearsal-kraid-20260923/timestamp.json
sudo /usr/local/bin/soph --json omega init --home /srv/omega-offline --network kraid-rehearsal \
  --repository https://sopholeth.io/omega/rehearsal-kraid-20260923 \
  --disposable --encrypted
sudo /usr/local/bin/soph --json omega status --home /srv/omega-offline --network kraid-rehearsal --check-keys
```

Choose a strong passphrase when prompted. Save the reported fingerprint and a
separate backup of the encrypted authority and public bundle. Keep the passphrase
separately. The [operator guide](omega-operations.md#encrypted-custody-and-backup-restoration) covers
restoring and checking the backup.

This manifest uses placeholder origins because the rehearsal checks discovery
metadata, without requiring live root nodes:

```bash
sudo tee /etc/soph/kraid-rehearsal-roots.json >/dev/null <<'EOF'
{"schema":1,"network":"kraid-rehearsal","enclave":"default","roots":[{"id":"one","origin":"https://one.example.invalid"},{"id":"two","origin":"https://two.example.invalid"},{"id":"three","origin":"https://three.example.invalid"}]}
EOF
sudo /usr/local/bin/soph --json omega provision-renewal --home /srv/omega-offline \
  --network kraid-rehearsal --renewal-home /srv/omega-online --disposable
sudo --preserve-env=VERCEL_TOKEN /usr/local/bin/soph --json omega publish --home /srv/omega-offline --network kraid-rehearsal \
  --manifest /etc/soph/kraid-rehearsal-roots.json \
  --repository-dir /srv/omega-public/kraid-rehearsal --version 1 \
  --disposable --vercel-config /etc/soph/omega-vercel.json
sudo /usr/local/bin/soph --json --timeout 2m omega status --home /srv/omega-online \
  --network kraid-rehearsal --verify
```

Expect `publication: "verified"`. Preserve both homes and the public spool on
failure, fix the reported problem, and retry the same operation. After publication,
back up the operational home too: it now owns the current journal.

## Test the real service account and scheduler

Install the existing service/timer and a drop-in **only for this disposable
instance**. The root service manager reads the private token file; the service
runs as `soph-omega`, with the operator home inaccessible.

```bash
sudo install -m 0644 docs/examples/omega/soph-omega-renewal@.service /etc/systemd/system/
sudo install -m 0644 docs/examples/omega/soph-omega-renewal@.timer /etc/systemd/system/
sudo install -d -m 0755 /etc/systemd/system/soph-omega-renewal@kraid-rehearsal.service.d
sudo tee /etc/systemd/system/soph-omega-renewal@kraid-rehearsal.service.d/vercel.conf >/dev/null <<'EOF'
[Service]
EnvironmentFile=/etc/soph/omega-vercel.env
ExecStart=
ExecStart=/usr/local/bin/soph --json --timeout 5m omega publish --home /srv/omega-online --network %i --renew --disposable --vercel-config /etc/soph/omega-vercel.json
TimeoutStartSec=330
EOF
sudo systemctl daemon-reload
sudo systemctl start soph-omega-renewal@kraid-rehearsal.service
sudo journalctl -u soph-omega-renewal@kraid-rehearsal.service -n 20 --no-pager
sudo systemctl enable --now soph-omega-renewal@kraid-rehearsal.timer
systemctl list-timers soph-omega-renewal@kraid-rehearsal.timer
```

The first run verifies release 1 without redeploying. The first successful hourly
run after 24 hours creates snapshot/timestamp 2 with seven-day validity and leaves
targets 1 unchanged. Check the journal and `status --verify` afterward. Do not edit
the clock or journal to force a renewal. Reports, version numbers, and error output
are enough to review the result; do not share keys, passwords, or token files.

When finished, stop this disposable timer and preserve its custody/history:

```bash
sudo systemctl disable --now soph-omega-renewal@kraid-rehearsal.timer
```

Production activation is a separate step using the approved root endpoints, final
network name, final repository URL, and a new encrypted production authority.
Never relabel or reuse this disposable identity. See the
[launch plan](public-network-plan.md) for the remaining gates.
