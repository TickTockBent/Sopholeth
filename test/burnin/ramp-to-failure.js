// Node ramp-to-failure test.
//
// Two independent scenarios that run sequentially:
//
//   1. OPS CEILING — tiny payload, ramping request rate
//      Finds the maximum ops/sec the cluster can sustain before errors or
//      latency degrade past the threshold. Bottleneck is gossip amplification,
//      event loop / goroutine saturation, or rate limiter headroom.
//
//   2. DATA THROUGHPUT CEILING — fixed moderate rate, ramping payload size
//      Finds the maximum bytes/sec the cluster can move. Bottleneck is memory
//      pressure, gossip serialization cost, or MaxBytesReader.
//
// Each scenario ramps in stepped plateaus — hold each level long enough for
// the system to either stabilize or visibly degrade (~2-3 min per step).
//
// Inflection indicators to watch:
//   - 202 rate: the moment 202s appear, you've crossed the quorum reliability
//     threshold. That's the first meaningful ceiling.
//   - Latency shape: if latency climbs but throughput is flat → contention
//     (pendingWrites mutex, connection pool). If throughput climbs then drops
//     sharply → resource exhaustion (memory, file descriptors).
//   - Process memory: compare heap and RSS over each plateau and recovery.
//
// Run:
//   k6 run \
//     -e BURNIN_NODES=http://localhost:8080 \
//     test/burnin/ramp-to-failure.js
//
// To run only one scenario:
//   -e SCENARIO=ops      (skip data throughput)
//   -e SCENARIO=data     (skip ops ceiling)
//
// TTL mode (affects GC pressure profile):
//   -e TTL_MODE=short    (requests 10-30s; the node clamps these to its TTL floor)
//   -e TTL_MODE=long     (300-600s TTLs — default, matches burn-in)

import http from 'k6/http';
import { check } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

const NODES = (__ENV.BURNIN_NODES || 'http://localhost:8080').split(',').map(s => s.trim());
const SCENARIO_FILTER = (__ENV.SCENARIO || 'both').toLowerCase();
const TTL_MODE = (__ENV.TTL_MODE || 'long').toLowerCase();

// --- TTL profiles -----------------------------------------------------------

function getTtl() {
    if (TTL_MODE === 'short') {
        return 10 + Math.floor(Math.random() * 20); // 10-30s
    }
    return 300 + Math.floor(Math.random() * 300); // 300-600s
}

// --- Payload generators ------------------------------------------------------

function tinyPayload() {
    return JSON.stringify({ t: Date.now(), v: __VU, i: __ITER });
}

const DATA_SIZES = [1024, 10240, 102400, 524288, 1048576]; // 1K, 10K, 100K, 512K, 1M
const DATA_STAGE_DURATION = '3m';

const FILL_STRINGS = {};
for (const size of DATA_SIZES) {
    const overhead = 40;
    FILL_STRINGS[size] = 'X'.repeat(Math.max(0, size - overhead));
}

function sizedPayload(sizeBytes) {
    return JSON.stringify({ t: Date.now(), d: FILL_STRINGS[sizeBytes] });
}

// --- Scenarios ---------------------------------------------------------------

const opsStages = [
    { target: 100,  duration: '2m' },
    { target: 250,  duration: '2m' },
    { target: 500,  duration: '2m' },
    { target: 1000, duration: '2m' },
    { target: 2000, duration: '2m' },
    { target: 3000, duration: '2m' },
    { target: 4000, duration: '2m' },
    { target: 5000, duration: '2m' },
    { target: 6000, duration: '2m' },
    { target: 8000, duration: '2m' },
    { target: 10000, duration: '2m' },
];

const dataStages = DATA_SIZES.map(() => ({
    target: 50,
    duration: DATA_STAGE_DURATION,
}));

function buildScenarios() {
    const scenarios = {};

    if (SCENARIO_FILTER === 'both' || SCENARIO_FILTER === 'ops') {
        scenarios.ops_ceiling = {
            executor: 'ramping-arrival-rate',
            startRate: 50,
            timeUnit: '1s',
            preAllocatedVUs: 50,
            maxVUs: 200,
            stages: opsStages,
            exec: 'opsTest',
        };
    }

    if (SCENARIO_FILTER === 'both' || SCENARIO_FILTER === 'data') {
        scenarios.data_ceiling = {
            executor: 'ramping-arrival-rate',
            startRate: 50,
            timeUnit: '1s',
            preAllocatedVUs: 30,
            maxVUs: 100,
            stages: dataStages,
            exec: 'dataTest',
            startTime: SCENARIO_FILTER === 'both'
                ? `${opsStages.length * 2}m`
                : '0s',
        };
    }

    return scenarios;
}

export const options = {
    scenarios: buildScenarios(),
};

// --- Custom metrics ----------------------------------------------------------

// Ops scenario
const opsQuorumOk = new Counter('ops_quorum_ok_201');
const opsQuorumTimeout = new Counter('ops_quorum_timeout_202');
const opsRateLimited = new Counter('ops_rate_limited_429');
const opsErrors = new Counter('ops_errors_other');
const opsQuorumTimeoutRate = new Rate('ops_quorum_timeout_rate');
const opsLatency = new Trend('ops_put_latency_ms', true);

// Data scenario
const dataQuorumOk = new Counter('data_quorum_ok_201');
const dataQuorumTimeout = new Counter('data_quorum_timeout_202');
const dataRateLimited = new Counter('data_rate_limited_429');
const dataErrors = new Counter('data_errors_other');
const dataQuorumTimeoutRate = new Rate('data_quorum_timeout_rate');
const dataLatency = new Trend('data_put_latency_ms', true);

// Shared
const putBytes = new Counter('put_payload_bytes');

// --- VU functions ------------------------------------------------------------

export function opsTest() {
    const node = NODES[__ITER % NODES.length];
    const key = `ramp-ops-${__VU}-${__ITER}`;
    const body = tinyPayload();
    const ttl = getTtl();

    const res = http.put(
        `${node}/v1/data/${key}`,
        body,
        {
            headers: { 'Content-Type': 'application/json', 'X-TTL': String(ttl) },
            tags: { scenario: 'ops_ceiling' },
            name: 'PUT /v1/data/{key}',
        },
    );

    opsLatency.add(res.timings.duration);
    putBytes.add(body.length);

    if (res.status === 200 || res.status === 201) {
        opsQuorumOk.add(1);
        opsQuorumTimeoutRate.add(false);
    } else if (res.status === 202) {
        opsQuorumTimeout.add(1);
        opsQuorumTimeoutRate.add(true);
    } else if (res.status === 429) {
        opsRateLimited.add(1);
        opsQuorumTimeoutRate.add(false);
    } else {
        opsErrors.add(1);
        opsQuorumTimeoutRate.add(false);
    }

    check(res, {
        'ops: stored (2xx)': (r) => r.status >= 200 && r.status < 300,
    });
}

export function dataTest(setupData) {
    const node = NODES[__ITER % NODES.length];
    const key = `ramp-data-${__VU}-${__ITER}`;
    const ttl = getTtl();

    const opsOffsetMs = (SCENARIO_FILTER === 'both')
        ? opsStages.reduce((s, st) => s + parseInt(st.duration), 0) * 60 * 1000
        : 0;
    const elapsedMs = Date.now() - setupData.startedAt - opsOffsetMs;
    const stageDurationMs = parseInt(DATA_STAGE_DURATION) * 60 * 1000;
    const stageIdx = Math.min(
        Math.max(0, Math.floor(elapsedMs / stageDurationMs)),
        DATA_SIZES.length - 1,
    );
    const payloadSize = DATA_SIZES[stageIdx];
    const body = sizedPayload(payloadSize);

    const res = http.put(
        `${node}/v1/data/${key}`,
        body,
        {
            headers: { 'Content-Type': 'application/json', 'X-TTL': String(ttl) },
            tags: { scenario: 'data_ceiling', payload_kb: String(payloadSize / 1024) },
            name: `PUT /v1/data/{key} [${payloadSize >= 1024 ? payloadSize/1024 + 'KB' : payloadSize + 'B'}]`,
        },
    );

    dataLatency.add(res.timings.duration);
    putBytes.add(body.length);

    if (res.status === 200 || res.status === 201) {
        dataQuorumOk.add(1);
        dataQuorumTimeoutRate.add(false);
    } else if (res.status === 202) {
        dataQuorumTimeout.add(1);
        dataQuorumTimeoutRate.add(true);
    } else if (res.status === 429) {
        dataRateLimited.add(1);
        dataQuorumTimeoutRate.add(false);
    } else {
        dataErrors.add(1);
        dataQuorumTimeoutRate.add(false);
    }

    check(res, {
        'data: stored (2xx)': (r) => r.status >= 200 && r.status < 300,
    });
}

// --- Lifecycle ---------------------------------------------------------------

export function setup() {
    console.log(`ramp-to-failure: ${NODES.length} nodes — ${NODES.join(', ')}`);
    console.log(`  scenario: ${SCENARIO_FILTER}`);
    console.log(`  ttl mode: ${TTL_MODE} (${TTL_MODE === 'short' ? '10-30s' : '300-600s'})`);

    if (SCENARIO_FILTER === 'both' || SCENARIO_FILTER === 'ops') {
        const maxRate = opsStages[opsStages.length - 1].target;
        const totalMin = opsStages.reduce((s, st) => s + parseInt(st.duration), 0);
        console.log(`  ops ceiling: ramp 50 → ${maxRate} ops/sec over ${totalMin}m`);
    }
    if (SCENARIO_FILTER === 'both' || SCENARIO_FILTER === 'data') {
        const sizes = DATA_SIZES.map(s => s >= 1024 ? `${s/1024}KB` : `${s}B`).join(' → ');
        console.log(`  data ceiling: ${sizes} at 50 ops/sec, ${DATA_STAGE_DURATION}/step`);
    }

    for (const node of NODES) {
        const res = http.get(`${node}/v1/health`);
        if (res.status !== 200) {
            console.error(`  WARN: ${node} health check failed (HTTP ${res.status})`);
        }
    }

    return { startedAt: Date.now() };
}

export function handleSummary(data) {
    const lines = ['\n=== RAMP-TO-FAILURE RESULTS ===\n'];

    // --- Ops scenario ---
    const has_ops = data.metrics.ops_quorum_ok_201;
    if (has_ops) {
        const ok = data.metrics.ops_quorum_ok_201?.values?.count || 0;
        const timeout = data.metrics.ops_quorum_timeout_202?.values?.count || 0;
        const rl = data.metrics.ops_rate_limited_429?.values?.count || 0;
        const err = data.metrics.ops_errors_other?.values?.count || 0;
        const total = ok + timeout + rl + err;
        const timeoutPct = total > 0 ? ((timeout / total) * 100).toFixed(2) : '0.00';

        lines.push('--- OPS CEILING ---');
        lines.push(`  Total requests:    ${total}`);
        lines.push(`  201 (quorum ok):   ${ok}`);
        lines.push(`  202 (quorum timeout): ${timeout} (${timeoutPct}%)`);
        lines.push(`  429 (rate limited): ${rl}`);
        lines.push(`  Other errors:      ${err}`);

        if (data.metrics.ops_put_latency_ms) {
            const p = data.metrics.ops_put_latency_ms.values;
            lines.push(`  Latency p50:       ${p['p(50)']?.toFixed(1)}ms`);
            lines.push(`  Latency p95:       ${p['p(95)']?.toFixed(1)}ms`);
            lines.push(`  Latency p99:       ${p['p(99)']?.toFixed(1)}ms`);
            lines.push(`  Latency max:       ${p['max']?.toFixed(1)}ms`);
        }
        lines.push('');
    }

    // --- Data scenario ---
    const has_data = data.metrics.data_quorum_ok_201;
    if (has_data) {
        const ok = data.metrics.data_quorum_ok_201?.values?.count || 0;
        const timeout = data.metrics.data_quorum_timeout_202?.values?.count || 0;
        const rl = data.metrics.data_rate_limited_429?.values?.count || 0;
        const err = data.metrics.data_errors_other?.values?.count || 0;
        const total = ok + timeout + rl + err;
        const timeoutPct = total > 0 ? ((timeout / total) * 100).toFixed(2) : '0.00';

        lines.push('--- DATA THROUGHPUT CEILING ---');
        lines.push(`  Total requests:    ${total}`);
        lines.push(`  201 (quorum ok):   ${ok}`);
        lines.push(`  202 (quorum timeout): ${timeout} (${timeoutPct}%)`);
        lines.push(`  429 (rate limited): ${rl}`);
        lines.push(`  Other errors:      ${err}`);

        if (data.metrics.data_put_latency_ms) {
            const p = data.metrics.data_put_latency_ms.values;
            lines.push(`  Latency p50:       ${p['p(50)']?.toFixed(1)}ms`);
            lines.push(`  Latency p95:       ${p['p(95)']?.toFixed(1)}ms`);
            lines.push(`  Latency p99:       ${p['p(99)']?.toFixed(1)}ms`);
            lines.push(`  Latency max:       ${p['max']?.toFixed(1)}ms`);
        }
        lines.push('');
    }

    // --- Totals ---
    if (data.metrics.put_payload_bytes) {
        const totalMB = data.metrics.put_payload_bytes.values.count / 1024 / 1024;
        lines.push(`Total data pushed:   ${totalMB.toFixed(1)} MB`);
    }

    lines.push('');
    lines.push('Look for the inflection point: the stage where 202s first');
    lines.push('appear is your quorum reliability ceiling. If 429s dominate,');
    lines.push('raise NODE_RATE_LIMIT and rerun to find the real wall.');
    lines.push('');

    console.log(lines.join('\n'));

    return {
        stdout: textSummary(data, { indent: '  ', enableColors: true }) + lines.join('\n'),
    };
}

import { textSummary } from 'https://jslib.k6.io/k6-summary/0.0.3/index.js';
