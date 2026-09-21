// Node burn-in workload (k6).
//
// Spec: test/burnin/README.md, "Workload" section.
//
// Run:
//   k6 run \
//     -e BURNIN_NODES=http://localhost:8080 \
//     -e BURNIN_DURATION=48h \
//     test/burnin/workload.js
//
// Output: stdout summary at end-of-run, plus k6's built-in metrics. Pipe
// metrics to Prometheus via `--out experimental-prometheus-rw=...` if you
// want them on the burn-in dashboard alongside node metrics.

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter } from 'k6/metrics';

const NODES = (__ENV.BURNIN_NODES || 'http://localhost:8080').split(',').map(s => s.trim());
const REF_SET_SIZE = 200;
const GRAVEYARD_SIZE = 200;
const AGENT_COUNT = 100;

// Keys must not contain '/' — server returns 400. Keys are opaque,
// single-segment strings; use ':' or '-' as a hierarchy delimiter
// (see docs/patterns.md "Key Naming Conventions").

// NODE_MIN_TTL defaults to 300 (5 min). Graveyard set is written with this
// TTL and the setup() sleeps long enough for it to expire before the main
// workload starts hitting it — that way GET-expired actually exercises the
// expired-key path, not just a miss.
const GRAVEYARD_TTL_SEC = 300;
const GRAVEYARD_WARMUP_SEC = 360;

// Long-lived reference set: 72h TTL covers the full run. After expiry,
// GET-existing will start returning 404 from the ref-set arm. The ref set
// is a retention probe, not an invariant — if the run exceeds REF_TTL,
// that's expected behavior.
const REF_TTL_SEC = 72 * 3600;

const SEGMENT_DURATION = __ENV.BURNIN_DURATION || '12h';
const SKIP_SETUP = (__ENV.SKIP_SETUP || '').toLowerCase() === 'true';

export const options = {
    setupTimeout: '15m',
    scenarios: {
        workload: {
            executor: 'constant-arrival-rate',
            rate: 50,
            timeUnit: '1s',
            duration: SEGMENT_DURATION,
            preAllocatedVUs: 30,
            maxVUs: 60,
        },
    },
    thresholds: {
        // Hard failures should be near zero. 202s tracked separately.
        'http_req_failed{op:put}': ['rate<0.01'],
        'http_req_failed{op:get_existing}': ['rate<0.02'],
    },
};

const soft202 = new Counter('soft_failures_202');
const refMissUnexpected = new Counter('ref_get_unexpected_404');
const expiredHitUnexpected = new Counter('graveyard_get_unexpected_200');

export function setup() {
    const segment = __ENV.SEGMENT || '1';
    console.log(`burn-in workload: ${NODES.length} nodes — ${NODES.join(', ')}`);
    console.log(`  segment: ${segment}  duration: ${SEGMENT_DURATION}  skip_setup: ${SKIP_SETUP}`);

    if (SKIP_SETUP) {
        console.log('skipping ref/graveyard setup (SKIP_SETUP=true)');
        return { startedAt: Date.now(), segment };
    }

    console.log(`  ref set: ${REF_SET_SIZE} keys @ ${REF_TTL_SEC}s TTL`);
    console.log(`  graveyard: ${GRAVEYARD_SIZE} keys @ ${GRAVEYARD_TTL_SEC}s TTL (warmup ${GRAVEYARD_WARMUP_SEC}s)`);

    const seed = NODES[0];

    for (let i = 0; i < REF_SET_SIZE; i++) {
        http.put(
            `${seed}/v1/data/bench-ref-${i}`,
            JSON.stringify({ i, kind: 'ref' }),
            {
                headers: { 'Content-Type': 'application/json', 'X-TTL': String(REF_TTL_SEC) },
                name: 'PUT /v1/data/{ref}',
            },
        );
    }
    for (let i = 0; i < GRAVEYARD_SIZE; i++) {
        http.put(
            `${seed}/v1/data/bench-graveyard-${i}`,
            JSON.stringify({ i, kind: 'graveyard' }),
            {
                headers: { 'Content-Type': 'application/json', 'X-TTL': String(GRAVEYARD_TTL_SEC) },
                name: 'PUT /v1/data/{graveyard}',
            },
        );
    }

    console.log(`waiting ${GRAVEYARD_WARMUP_SEC}s for graveyard to expire before main run`);
    sleep(GRAVEYARD_WARMUP_SEC);
    console.log('warmup complete; starting workload');
    return { startedAt: Date.now(), segment };
}

export default function () {
    const node = NODES[__ITER % NODES.length];
    const r = Math.random();
    if (r < 0.40)       doPut(node);
    else if (r < 0.70)  doGetExisting(node);
    else if (r < 0.85)  doGetExpired(node);
    else if (r < 0.95)  doHead(node);
    else                doList(node);
}

function doPut(node) {
    const agentId = __VU % AGENT_COUNT;
    const key = `bench-agent-${agentId}-${__ITER}`;
    const ttlSec = 300 + Math.floor(Math.random() * (7200 - 300));
    const body = JSON.stringify({ vu: __VU, iter: __ITER, ts: Date.now() });
    // `name` collapses unique URLs into one metric series; without it k6's
    // per-URL metric map grows unbounded (#95: OOM at 27 GB after 2.4M URLs).
    const res = http.put(
        `${node}/v1/data/${key}`,
        body,
        {
            headers: { 'Content-Type': 'application/json', 'X-TTL': String(ttlSec) },
            tags: { op: 'put' },
            name: 'PUT /v1/data/{key}',
        },
    );
    if (res.status === 202) soft202.add(1);
    check(res, { 'PUT 200/201/202': (r) => r.status === 200 || r.status === 201 || r.status === 202 });
}

function doGetExisting(node) {
    const i = Math.floor(Math.random() * REF_SET_SIZE);
    const key = `bench-ref-${i}`;
    const res = http.get(`${node}/v1/data/${key}`, {
        tags: { op: 'get_existing' },
        name: 'GET /v1/data/{ref}',
    });
    if (res.status === 404) refMissUnexpected.add(1);
    check(res, { 'GET ref 200': (r) => r.status === 200 });
}

function doGetExpired(node) {
    const i = Math.floor(Math.random() * GRAVEYARD_SIZE);
    const key = `bench-graveyard-${i}`;
    const res = http.get(`${node}/v1/data/${key}`, {
        tags: { op: 'get_expired' },
        name: 'GET /v1/data/{graveyard}',
    });
    if (res.status === 200) expiredHitUnexpected.add(1);
    check(res, { 'GET expired 404': (r) => r.status === 404 });
}

function doHead(node) {
    const i = Math.floor(Math.random() * REF_SET_SIZE);
    const key = `bench-ref-${i}`;
    const res = http.request('HEAD', `${node}/v1/data/${key}`, null, {
        tags: { op: 'head' },
        name: 'HEAD /v1/data/{ref}',
    });
    check(res, { 'HEAD 200': (r) => r.status === 200 });
}

function doList(node) {
    const agentId = Math.floor(Math.random() * AGENT_COUNT);
    const prefix = `bench-agent-${agentId}-`;
    const res = http.get(
        `${node}/v1/keys?prefix=${encodeURIComponent(prefix)}&limit=100`,
        {
            tags: { op: 'list' },
            name: 'GET /v1/keys?prefix={agent}',
        },
    );
    check(res, { 'list 200': (r) => r.status === 200 });
}
