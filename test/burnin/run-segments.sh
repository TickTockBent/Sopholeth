#!/usr/bin/env bash
# Run the burn-in workload in 12-hour segments. Each segment writes its
# summary to a separate file, then k6 exits and restarts fresh — no
# unbounded memory accumulation.
#
# Segment 1 runs full setup (ref set + graveyard + warmup).
# Segments 2+ skip setup (SKIP_SETUP=true) so there's no 6-minute gap.
#
# Usage:
#   ./test/burnin/run-segments.sh
#
# Env vars (all optional):
#   BURNIN_NODES         — comma-separated node URLs (default: localhost:8080)
#   BURNIN_STATE_DIR     — log directory (default: ~/.local/state/sopholeth/burnin)
#   SEGMENT_DURATION     — per-segment duration (default: 12h)
#   TOTAL_SEGMENTS       — how many segments (default: 6 = 72h)
#   K6_PROMETHEUS_RW_SERVER_URL — Prometheus remote-write URL

set -uo pipefail

NODES="${BURNIN_NODES:-http://localhost:8080}"
DURATION="${SEGMENT_DURATION:-12h}"
SEGMENTS="${TOTAL_SEGMENTS:-6}"
PROM_URL="${K6_PROMETHEUS_RW_SERVER_URL:-http://localhost:9090/api/v1/write}"
WORKDIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
LOGDIR="${BURNIN_STATE_DIR:-$HOME/.local/state/sopholeth/burnin}"

mkdir -p "$LOGDIR"

echo "$(date -Is) burn-in: ${SEGMENTS} × ${DURATION} segments"
echo "  nodes: $NODES"
echo "  summaries: $LOGDIR/k6-segment-*.txt"

for seg in $(seq 1 "$SEGMENTS"); do
    skip_setup="false"
    if [[ "$seg" -gt 1 ]]; then
        skip_setup="true"
    fi

    echo ""
    echo "$(date -Is) === SEGMENT ${seg}/${SEGMENTS} (${DURATION}, skip_setup=${skip_setup}) ==="

    docker run --rm -i --network host \
        -v "$WORKDIR":/work \
        -e BURNIN_NODES="$NODES" \
        -e BURNIN_DURATION="$DURATION" \
        -e SKIP_SETUP="$skip_setup" \
        -e SEGMENT="$seg" \
        -e K6_PROMETHEUS_RW_SERVER_URL="$PROM_URL" \
        grafana/k6 run --out experimental-prometheus-rw /work/workload.js \
        2>&1 | tee "$LOGDIR/k6-segment-${seg}.txt"

    exit_code=${PIPESTATUS[0]}
    echo "$(date -Is) segment ${seg} exited with code ${exit_code}"

    # Exit 99 = k6 threshold crossed — not a fatal error, continue to next segment.
    # Only stop on real failures (connection errors, script errors, etc.).
    if [[ "$exit_code" -ne 0 && "$exit_code" -ne 99 ]]; then
        echo "$(date -Is) segment ${seg} FAILED (exit ${exit_code}) — stopping"
        exit "$exit_code"
    fi
    if [[ "$exit_code" -eq 99 ]]; then
        echo "$(date -Is) segment ${seg} threshold crossed (exit 99) — continuing"
    fi
done

echo ""
echo "$(date -Is) burn-in complete: all ${SEGMENTS} segments finished"
