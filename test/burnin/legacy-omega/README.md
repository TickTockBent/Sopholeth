# Legacy DNS burn-in helper

This preserves the old single-key DNS format for disposable burn-in tests while
node discovery consumers migrate to TUF. It is excluded from `make build`.
The [signing audit](../../../docs/omega-signing-audit.md) records its limitations;
use [`soph omega`](../../../docs/omega-operations.md) for the public authority.

```bash
go build -o bin/omega-lab ./test/burnin/legacy-omega
umask 077
./bin/omega-lab keygen --out-private /private/lab.key --out-public /private/lab.pub
./bin/omega-lab sign --key /private/lab.key --expires-in 30m \
  --nodes root-a.example:8080,root-b.example:8080 > /private/lab-record.txt
```

Use HTTP ports in node addresses. Publish the signed value through the lab DNS
configuration and give lab images only the public key. Never commit either key
or reuse it for a public authority; delete lab keys at teardown. The adjacent
`setup-keypair.sh` and `sign-loop.sh` build this helper automatically. The latter
modifies dnsmasq and restarts it using the existing lab sudo setup.
