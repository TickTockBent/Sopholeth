# Public test-network authority

The operator initialized this authority on Kraid with production `age-scrypt`
custody. Its public bundle was retrieved separately and checked against the
initial fingerprint supplied in the operator's initialization report.

| Field | Value |
| --- | --- |
| Network | `sopholeth` |
| Repository | `https://sopholeth.io/omega` |
| Initial root version | `1` |
| Initial root expiry | `2027-09-27T00:09:22Z` |
| Initial root SHA-256 | `8e3285517366aa2ec80366213ccde26bda0ed577679242ebcebdeef8748b0c82` |
| Root threshold | 2 of 3 |
| Operator custody | Kraid, `/srv/omega-offline/sopholeth` |
| Public bundle | [`internal/discovery/bundle.json`](../internal/discovery/bundle.json) |

This is the initial trust identity; normal signed rotations retain its bundle
and fingerprint. The operator keeps an independent copy of the fingerprint.
Only the public bundle is adopted into source. Signing keys and custody homes
remain outside the repository.

Activation status:

- Authority initialization: complete; all six encrypted key files are present.
- Public bundle: signature/fingerprint validation passed against the operator report.
- Backup restoration and passphrase recovery: awaiting operator verification.
- Renewal provisioning and first publication: pending.
- Three-root deployment and real-network checks: pending.

## Next operator step

Before provisioning, create a private archive on Kraid and verify a restored
copy at a separate path. These commands run as the normal login user; `sudo`
reads the protected home and restores its ownership and permissions. The
archive includes the whole operator home, including the earlier rehearsal.
Keep the rehearsal timer disabled and run no other omega operations during
the copy. At this stage no operational binding has been created for `sopholeth`,
so its restored home can be checked at a temporary path.

```bash
(
  set -e
  umask 077
  backup_dir=$(mktemp -d "$HOME/sopholeth-omega-backup.XXXXXX")
  sudo tar -C /srv -cpf - omega-offline > "$backup_dir/omega-offline.tar"

  restore_dir=$(sudo mktemp -d /srv/omega-restore.XXXXXX)
  sudo tar -C "$restore_dir" -xpf "$backup_dir/omega-offline.tar"
  printf 'Backup archive: %s\nRestore directory: %s\n' \
    "$backup_dir/omega-offline.tar" "$restore_dir"

  sudo soph --json --timeout 5m omega status \
    --home "$restore_dir/omega-offline" --network sopholeth --check-keys
)
```

Expect successful key checks and the exact fingerprint above. Save the archive
to private backup storage off Kraid, with passphrase recovery kept separately.
The copy on Kraid alone is a restore check, not protection against losing Kraid.
Share only the status report when recording the result.

Then continue with [renewal provisioning and first publication](public-network-bringup.md#create-and-back-up-the-intended-authority-on-kraid).
After that handoff, take a fresh backup including the operational home and
public spool. Later restoration must preserve their original bound paths, as
described in [omega operations](omega-operations.md#verify-a-restored-copy).
