#!/usr/bin/env bash
# Generate a single-use omega keypair for the burn-in test.
#
# Output:
#   ~/.local/state/sopholeth/burnin/omega.priv  (mode 0600) — sign signed-list updates here only
#   ~/.local/state/sopholeth/burnin/omega.pub   (mode 0644) — bake into the burn-in images
#
# DO NOT commit either file. DO NOT reuse this key for anything else. Delete
# both files at burn-in teardown.
#
# Idempotent-ish: refuses to overwrite an existing keypair (rerunning is
# almost certainly an operator mistake).

set -euo pipefail

repo_root=$(git -C "$(dirname "$0")" rev-parse --show-toplevel)
out_dir="${BURNIN_STATE_DIR:-$HOME/.local/state/sopholeth/burnin}"

mkdir -p "$out_dir"
chmod 700 "$out_dir"

if [[ ! -x "$repo_root/bin/omega" ]]; then
    echo "==> building bin/omega"
    (cd "$repo_root" && make build-omega)
fi

"$repo_root/bin/omega" keygen \
    --out-private "$out_dir/omega.priv" \
    --out-public "$out_dir/omega.pub"

echo
echo "Pubkey ready at $out_dir/omega.pub"
echo "Next: ./test/burnin/build-images.sh"
