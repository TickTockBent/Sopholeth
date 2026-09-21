// Network dashboard — vanilla JS, no build step.
//
// Polls /api/snapshot every 10s and re-renders the graph + tables. The
// server-side poll cycle is 60s, so a tighter client poll just catches the
// stale→fresh transition faster without adding load on the cluster.
//
// The force-directed graph is a small from-scratch SVG implementation
// rather than vis-network. Trade-off: 60 lines here vs 200KB of vendored
// JS. If/when we vendor a graph library, add cmd/dashboard/web/UPSTREAM.md
// (origin URL, license, sha256) per the design's supply-chain note.
//
// Security note: all text that originates from a polled node — id, region,
// enclave — flows through textContent/appendChild, NEVER through innerHTML
// or template strings interpolating untrusted values. A compromised node
// could otherwise return a script payload as its ID and execute it in the
// browser of anyone viewing the dashboard (stored XSS).

const POLL_INTERVAL_MS = 10_000;

function setText(el, value) {
    el.textContent = value == null || value === '' ? '—' : String(value);
}

const fmtDuration = (sec) => {
    if (!sec) return '—';
    const d = Math.floor(sec / 86400);
    const h = Math.floor((sec % 86400) / 3600);
    const m = Math.floor((sec % 3600) / 60);
    if (d) return `${d}d${h}h`;
    if (h) return `${h}h${m}m`;
    return `${m}m`;
};

const fmtRelativeAge = (iso) => {
    if (!iso) return '—';
    const t = new Date(iso).getTime();
    if (!Number.isFinite(t)) return '—';
    const ageSec = Math.floor((Date.now() - t) / 1000);
    if (ageSec < 60) return `${ageSec}s ago`;
    return fmtDuration(ageSec) + ' ago';
};

const fmtDate = (iso) => {
    if (!iso) return '—';
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return '—';
    return d.toISOString().slice(0, 19) + 'Z';
};

let lastSnapshot = null;
let selectedNodeID = null;

async function fetchSnapshot() {
    try {
        const res = await fetch('/api/snapshot', { cache: 'no-store' });
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const snap = await res.json();
        lastSnapshot = snap;
        render(snap);
    } catch (err) {
        document.getElementById('last-update').textContent = `fetch failed: ${err.message}`;
    }
}

function render(snap) {
    document.getElementById('last-update').textContent =
        `last update ${fmtRelativeAge(snap.generated_at)}` +
        (snap.stale ? ' [STALE]' : '');

    renderBanners(snap);
    renderStats(snap.stats || {});
    renderGraph(snap);
    renderTable(snap.nodes || []);
    if (selectedNodeID) {
        const n = (snap.nodes || []).find(x => x.id === selectedNodeID);
        if (n) renderNodeDetail(n);
    }
}

function renderBanners(snap) {
    const el = document.getElementById('banners');
    el.innerHTML = '';
    const banners = [];
    if (snap.seed_override) banners.push({ text: 'SEED OVERRIDE // trust chain bypassed', warn: true });
    if (snap.roots_unreachable) banners.push({ text: 'ROOTS UNREACHABLE // serving from peers', warn: true });
    if (snap.omega_refresh_failed) banners.push({ text: 'OMEGA REFRESH FAILED', warn: true });
    if (snap.loaded_from_disk) banners.push({ text: 'loaded from disk — refreshing…' });
    if (snap.stale && !snap.loaded_from_disk) banners.push({ text: 'snapshot stale', warn: true });
    for (const b of banners) {
        const span = document.createElement('span');
        span.className = b.warn ? 'banner warn' : 'banner';
        span.textContent = b.text;
        el.appendChild(span);
    }
}

function renderStats(s) {
    const set = (id, v) => document.getElementById(id).textContent = v ?? '—';
    set('stat-nodes', s.nodes);
    set('stat-enclaves', s.enclaves);
    set('stat-roots', s.roots_reachable);
    set('stat-uptime', fmtDuration(s.oldest_uptime_seconds));
    set('stat-omega-refresh', fmtRelativeAge(s.omega_refreshed_at));
    set('stat-omega-expires', fmtDate(s.omega_expires_at));
}

function renderTable(nodes) {
    const tbody = document.querySelector('#node-table tbody');
    tbody.replaceChildren();
    for (const n of nodes) {
        const tr = document.createElement('tr');
        if (n.unreachable) tr.classList.add('unreachable');
        if (n.is_root) tr.classList.add('root');
        const cells = [
            n.id,
            n.enclave,
            n.region,
            fmtDuration(n.uptime_seconds),
            `${(n.heap_mb || 0).toFixed(1)} MB`,
            n.is_root ? 'yes' : '',
            n.unreachable ? 'unreachable' : 'ok',
        ];
        for (const value of cells) {
            const td = document.createElement('td');
            setText(td, value);
            tr.appendChild(td);
        }
        tr.style.cursor = 'pointer';
        tr.addEventListener('click', () => {
            selectedNodeID = n.id;
            renderNodeDetail(n);
            highlightInGraph(n.id);
        });
        tbody.appendChild(tr);
    }
}

function renderNodeDetail(n) {
    const el = document.getElementById('node-detail');
    const rows = [
        ['ID', n.id],
        ['Enclave', n.enclave],
        ['Region', n.region],
        ['Uptime', fmtDuration(n.uptime_seconds)],
        ['Heap', `${(n.heap_mb || 0).toFixed(2)} MB`],
        ['Goroutines (approx)', n.goroutines_approx],
        ['Is root', n.is_root ? 'yes' : 'no'],
        ['Status', n.unreachable ? 'unreachable' : 'ok'],
        ['peer_joins_total', n.metrics?.peer_joins_total ?? 0],
        ['peer_evictions_total', n.metrics?.peer_evictions_total ?? 0],
        ['ping_failures_total', n.metrics?.ping_failures_total ?? 0],
    ];
    el.replaceChildren();
    for (const [k, v] of rows) {
        const row = document.createElement('div');
        row.className = 'row';
        const kEl = document.createElement('span');
        kEl.className = 'k';
        kEl.textContent = k;
        const vEl = document.createElement('span');
        vEl.className = 'v';
        setText(vEl, v);
        row.appendChild(kEl);
        row.appendChild(vEl);
        el.appendChild(row);
    }
}

// --- minimal force-directed graph ---
// Nodes start placed on a circle and relax via repulsion + edge springs
// over a fixed number of ticks. Deterministic enough for a steady cluster
// of 3–50 nodes; we can swap in vis-network if it ever exceeds that.

const GRAPH_TICKS = 240;
const REPULSION = 9000;
const SPRING_LENGTH = 120;
const SPRING_K = 0.02;
const DAMPING = 0.85;

function renderGraph(snap) {
    const container = document.getElementById('graph');
    container.innerHTML = '';
    const nodes = (snap.nodes || []).map(n => ({ ...n, x: 0, y: 0, vx: 0, vy: 0 }));
    const edges = snap.edges || [];
    if (!nodes.length) return;

    // Build edge set for asymmetry detection (renderer treats edge as
    // asymmetric when the reverse direction is absent — see the design
    // doc's "edges are directional" section).
    const edgeKey = (a, b) => `${a}\x00${b}`;
    const edgeSet = new Set(edges.map(e => edgeKey(e.from, e.to)));
    const renderedEdges = [];
    const seenPair = new Set();
    for (const e of edges) {
        const pair = e.from < e.to ? edgeKey(e.from, e.to) : edgeKey(e.to, e.from);
        if (seenPair.has(pair)) continue;
        seenPair.add(pair);
        const symmetric = edgeSet.has(edgeKey(e.to, e.from));
        renderedEdges.push({ from: e.from, to: e.to, symmetric });
    }

    const w = container.clientWidth || 800;
    const h = container.clientHeight || 480;
    const cx = w / 2, cy = h / 2;
    const radius = Math.min(w, h) * 0.35;

    nodes.forEach((n, i) => {
        const theta = (2 * Math.PI * i) / nodes.length;
        n.x = cx + radius * Math.cos(theta);
        n.y = cy + radius * Math.sin(theta);
    });
    const byID = new Map(nodes.map(n => [n.id, n]));

    for (let tick = 0; tick < GRAPH_TICKS; tick++) {
        // pairwise repulsion
        for (let i = 0; i < nodes.length; i++) {
            for (let j = i + 1; j < nodes.length; j++) {
                const a = nodes[i], b = nodes[j];
                const dx = b.x - a.x, dy = b.y - a.y;
                const d2 = dx * dx + dy * dy + 0.01;
                const force = REPULSION / d2;
                const dist = Math.sqrt(d2);
                const fx = (dx / dist) * force, fy = (dy / dist) * force;
                a.vx -= fx; a.vy -= fy;
                b.vx += fx; b.vy += fy;
            }
        }
        // edge springs
        for (const e of renderedEdges) {
            const a = byID.get(e.from), b = byID.get(e.to);
            if (!a || !b) continue;
            const dx = b.x - a.x, dy = b.y - a.y;
            const dist = Math.sqrt(dx * dx + dy * dy + 0.01);
            const f = SPRING_K * (dist - SPRING_LENGTH);
            const fx = (dx / dist) * f, fy = (dy / dist) * f;
            a.vx += fx; a.vy += fy;
            b.vx -= fx; b.vy -= fy;
        }
        // integrate
        for (const n of nodes) {
            n.vx *= DAMPING; n.vy *= DAMPING;
            n.x += n.vx; n.y += n.vy;
            n.x = Math.max(20, Math.min(w - 20, n.x));
            n.y = Math.max(20, Math.min(h - 20, n.y));
        }
    }

    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('viewBox', `0 0 ${w} ${h}`);
    svg.setAttribute('preserveAspectRatio', 'xMidYMid meet');

    for (const e of renderedEdges) {
        const a = byID.get(e.from), b = byID.get(e.to);
        if (!a || !b) continue;
        const line = document.createElementNS('http://www.w3.org/2000/svg', 'line');
        line.setAttribute('x1', a.x); line.setAttribute('y1', a.y);
        line.setAttribute('x2', b.x); line.setAttribute('y2', b.y);
        line.setAttribute('class', e.symmetric ? 'edge' : 'edge asymmetric');
        svg.appendChild(line);
    }

    for (const n of nodes) {
        const g = document.createElementNS('http://www.w3.org/2000/svg', 'g');
        const classes = ['node'];
        if (n.is_root) classes.push('root');
        if (n.unreachable) classes.push('unreachable');
        g.setAttribute('class', classes.join(' '));
        g.setAttribute('data-id', n.id);
        g.setAttribute('transform', `translate(${n.x},${n.y})`);
        g.style.cursor = 'pointer';
        const r = 16 + Math.log10((n.uptime_seconds || 1) + 10) * 4;
        const c = document.createElementNS('http://www.w3.org/2000/svg', 'circle');
        c.setAttribute('r', r);
        g.appendChild(c);
        const t = document.createElementNS('http://www.w3.org/2000/svg', 'text');
        t.setAttribute('dy', r + 14);
        t.textContent = n.id;
        g.appendChild(t);
        g.addEventListener('click', () => {
            selectedNodeID = n.id;
            renderNodeDetail(n);
            highlightInGraph(n.id);
        });
        svg.appendChild(g);
    }
    container.appendChild(svg);
    if (selectedNodeID) highlightInGraph(selectedNodeID);
}

function highlightInGraph(id) {
    document.querySelectorAll('#graph svg .node').forEach(g => {
        g.querySelector('circle').setAttribute(
            'stroke-width',
            g.getAttribute('data-id') === id ? 4 : (g.classList.contains('root') ? 3 : 2)
        );
    });
}

fetchSnapshot();
setInterval(fetchSnapshot, POLL_INTERVAL_MS);
