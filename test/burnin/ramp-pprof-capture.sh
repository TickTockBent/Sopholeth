#!/usr/bin/env bash
# Capture pprof snapshots at regular intervals during a ramp-to-failure run.
#
# Run alongside the k6 ramp test. Captures Go heap and goroutine profiles
# every INTERVAL seconds. Output goes to a timestamped directory
# for post-run analysis.
#
# Usage:
#   PPROF_TARGETS=http://127.0.0.1:6060 ./test/burnin/ramp-pprof-capture.sh
#
# The goroutine profile is the key diagnostic: pileup on sync.Mutex means
# contention (pendingWrites, peer map); pileup on net.(*netFD).connect means
# connection pool exhaustion.

set -uo pipefail

IFS=',' read -r -a PROFILE_TARGETS <<< "${PPROF_TARGETS:-http://127.0.0.1:6060}"
INTERVAL="${CAPTURE_INTERVAL_SEC:-120}"

burnin_state_dir="${BURNIN_STATE_DIR:-$HOME/.local/state/sopholeth/burnin}"
OUTDIR="${RAMP_CAPTURE_DIR:-$burnin_state_dir/ramp-captures/$(date +%Y%m%d-%H%M%S)}"
mkdir -p "$OUTDIR"

echo "$(date -Is) pprof capture started — interval ${INTERVAL}s, output: $OUTDIR"

trap 'echo "$(date -Is) pprof capture stopped"; exit 0' INT TERM

cycle=0
while :; do
    cycle=$((cycle + 1))
    ts=$(date +%s)

    for index in "${!PROFILE_TARGETS[@]}"; do
        target="${PROFILE_TARGETS[$index]%/}"
        curl -s -m 10 "$target/debug/pprof/heap" \
            > "$OUTDIR/heap-node-${index}-${ts}.pprof" 2>/dev/null || true
        curl -s -m 10 "$target/debug/pprof/goroutine" \
            > "$OUTDIR/goroutine-node-${index}-${ts}.pprof" 2>/dev/null || true
    done

    echo "$(date -Is) cycle=$cycle captured ($(ls "$OUTDIR" | wc -l) files)"
    sleep "$INTERVAL"
done
