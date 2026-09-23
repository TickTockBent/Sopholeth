# Hosted omega rehearsal — 2026-09-23

The Vercel publication path passed a live disposable rehearsal. The final
`https://sopholeth.io/omega/` authority location is still empty. Node discovery,
the compiled public trust bundle, and the remaining launch gates are unchanged.

The disposable repository is
`https://sopholeth.io/omega/rehearsal-20260923T180221Z`. Its initial root fingerprint
is `0e48beec66a95ea86dfa8bae5abf07803cb4e46ff2f060099b8f5906438bee66`.
It names three `.invalid` node origins: this tests metadata hosting and the real
trust client, without claiming to run a three-node network. Do not ship this bundle.

| Check | Result |
| --- | --- |
| First signed publication | Root/targets/snapshot/timestamp 1 accepted through the canonical HTTPS URL. |
| Due renewal through `soph omega publish --renew` | Snapshot/timestamp 2 accepted, targets 1 unchanged; offline custody was unavailable. |
| Immediate hourly retry | Verified release 2, retaining the same deployment receipt. |
| CLI online-key rotation | Prepared and applied root 2; snapshot/timestamp advanced to 3, targets stayed at 1. |
| Fresh and returning clients | Both accepted renewal and the retained root transition from the original bundle. |
| Hosted object checks | Retained bytes matched; immutable objects had one-year caching; timestamps and missing next roots had `no-store`. |
| Docs preview | Authenticated access to the existing protected preview returned the same public root bytes. |
| New docs deployment and actual rollback | Signed release 3 remained verifiable before and after both changes. |

The first fixture was signed with a test clock 25 hours in the past so the real
CLI could exercise a due renewal without a day-long wait. No host clock or
production command was changed. Publication and clients used normal HTTPS/TLS.

The final metadata deployment is `dpl_5byQGPFCK5RkrnS2WSLuBrYSZhUU`. For docs
isolation, an identical static copy was staged as
`dpl_2YYtNjZkDzZavZhqt5HFex4yz2xx`, checked, promoted, and rolled back to the
original `dpl_49yDx4MAeSKy2udJ4sC8GeguLHVc`. Vercel's rollback also paused automatic
domain assignment; the original `autoAssignCustomDomains=true` setting was
restored afterward. The metadata project was never rolled back.
The protected-preview check used Vercel CLI's generated automation bypass;
the docs project's preview protection remains enabled.

## Findings fixed

- The config parser applied private-journal canonical formatting rules to an
  operator-edited JSON file, rejecting the committed example. It now accepts
  whitespace and field ordering while rejecting unknown, duplicate, or missing
  fields. The existing config test reads the actual example.
- The path-pattern project rewrite returned 404 for objects present at the
  origin. The working rule is regex `^/omega/(.*)$` with destination
  `https://sopholeth-omega-chi.vercel.app/omega/$1`. An empty 404 preflight alone
  did not expose this; fetching signed objects did.
- Vercel acknowledged renewal promotion before the serving edge exposed the
  new snapshot. Verification now retries for up to 30 seconds within the command
  timeout, retaining the same release and deployment. Persistent failures still
  return nonzero. The consolidated publication test covers that delay.

## Repeat the developer rehearsal

The opt-in [live test](../internal/omega/hosted_rehearsal_test.go) is excluded from
ordinary builds and CI. It deploys to the metadata project and retains local
state. Run it only before production activation, while no other publisher uses
that project; a new authority's deployment would replace its served repository.

Set `VERCEL_TOKEN` securely in the process environment, then:

```bash
make build-soph
export OMEGA_HOSTED_HOME="$(mktemp -d)"
export OMEGA_HOSTED_REPOSITORY="https://sopholeth.io/omega/rehearsal-$(date -u +%Y%m%dT%H%M%SZ)"
for stage in publish renew rotate verify; do
  OMEGA_HOSTED_STAGE="$stage" go test -tags omega_hosted_rehearsal \
    ./internal/omega -run '^TestHostedVercelRehearsal$' -count=1 -v -timeout 9m || break
done
```

Preserve the home and URL on failure; rerun the failed stage, not a new authority.
Reports, public bundle, disposable custody, and client checkpoints stay in that
private home. The `verify` stage can be repeated around external hosting changes.
Deployment credentials are not saved there. These tests do not enable a timer.

## Operator handoff: Kraid

The operator selected Kraid for permanent renewal and will perform its setup.
No SSH connection or remote modification was made during this rehearsal.
Use the [Kraid rehearsal steps](omega-kraid-rehearsal.md) to exercise the real
operator commands, encrypted custody, service permissions, and systemd on that
host before creating production authority.

The remaining hosting work is the remote service/token setup, an actual scheduled
run, and checking the result from that second location. The final authority,
its independent backup/recovery record, and release fingerprint are still future
steps. Continue with the [launch plan](public-network-plan.md); successful metadata
hosting does not close peer identity, admission, Windows-client, or other root gates.
