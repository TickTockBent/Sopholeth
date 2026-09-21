#!/usr/bin/env bash
# Build the burn-in Go node image with a baked-in test omega pubkey.
#
# Usage: build-images.sh [<pubkey-file>]
#   pubkey-file: defaults to ~/.local/state/sopholeth/burnin/omega.pub
#                (produced by setup-keypair.sh)
#
# Output: docker image sopholeth/node:burnin, ready to run on any
# Docker host. The companion TS node image was removed when the TS
# implementation was retired (issue #123).
#
# Distribute via:
#   docker save sopholeth/node:burnin | gzip > go-node.tar.gz
#   scp go-node.tar.gz <host>:
#   ssh <host> 'gunzip -c go-node.tar.gz | docker load'
# or push to a private/local registry. Do NOT push to a public registry —
# the corresponding private key would let anyone impersonate roots.

set -euo pipefail

repo_root=$(git -C "$(dirname "$0")" rev-parse --show-toplevel)
burnin_state_dir="${BURNIN_STATE_DIR:-$HOME/.local/state/sopholeth/burnin}"
pubkey_file="${1:-$burnin_state_dir/omega.pub}"

if [[ ! -f "$pubkey_file" ]]; then
    echo "pubkey file not found: $pubkey_file" >&2
    echo "run setup-keypair.sh first, or pass the path as arg 1" >&2
    exit 1
fi

pubkey=$(tr -d '[:space:]' < "$pubkey_file")
if [[ -z "$pubkey" ]]; then
    echo "pubkey file is empty: $pubkey_file" >&2
    exit 1
fi

echo "Building with pubkey: $pubkey"
echo

echo "==> sopholeth/node:burnin"
docker build \
    --build-arg "TEST_OMEGA_PUBKEY=$pubkey" \
    -f "$repo_root/test/burnin/Dockerfile.go-node" \
    -t sopholeth/node:burnin \
    "$repo_root"

echo
echo "Done. To verify the bake:"
echo "  docker run --rm --entrypoint sh sopholeth/node:burnin -c 'strings server | grep -c $pubkey'"
echo "  (should print 1 — the constant is embedded in the binary)"
