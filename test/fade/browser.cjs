// Exercise the actual Fade assets against two controlled HTTP/SSE nodes.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const { randomUUID } = require('node:crypto');
const assert = require('node:assert/strict');
const root = path.resolve(__dirname, '../../sites/sopholeth.com');
const values = new Map(), streams = new Set(), requests = [];
let now = Date.now(), revision = 0, putStatus = 201, unavailable = false, pollingOnly = false, omitted = 0;
const valid = (text, callsign = 'Test caller') => ({ app: 'fade', schema: 1, text, callsign, location: 'Test lab' });
function entry(key, data, ttl = 300) {
  const raw = Buffer.from(JSON.stringify(data));
  return { key, data, payload: raw.toString('base64'), size: raw.length, truncated: false, revision: String(++revision), ttl_seconds: ttl,
    written_at: new Date(now).toISOString(), expires_at: new Date(now + ttl * 1000).toISOString() };
}
function emit(name, data) { for (const s of streams) s.write(`event: ${name}\ndata: ${JSON.stringify(data)}\n\n`); }
function put(key, data) { const e = entry(key, data); values.set(key, e); emit('put', { now: new Date(now).toISOString(), node: 'fixture', entry: e }); return e; }
const node = label => http.createServer(async (req, res) => {
  res.setHeader('Access-Control-Allow-Origin', '*');
  res.setHeader('Access-Control-Allow-Methods', 'GET, PUT, OPTIONS');
  res.setHeader('Access-Control-Allow-Headers', 'Content-Type, X-TTL');
  res.setHeader('Access-Control-Expose-Headers', 'X-Original-TTL, X-Remaining-TTL');
  if (req.method === 'OPTIONS') { res.writeHead(200).end(); return; }
  const url = new URL(req.url, 'http://fixture'); requests.push(label + ' ' + req.method + ' ' + url.pathname);
  if (label === 'node-a' && unavailable) { res.writeHead(503).end(); return; }
  if (url.pathname === '/v1/health') { res.end(JSON.stringify({ status: 'healthy', node_id: label, enclave: 'default', network: 'public' })); return; }
  if (url.pathname === '/v1/stream') {
    if (pollingOnly) { res.writeHead(503).end(); return; }
    res.writeHead(200, { 'Content-Type': 'text/event-stream' });
    res.write(`event: snapshot\ndata: ${JSON.stringify({ now: new Date(now).toISOString(), node: label, enclave: 'default', omitted, entries: [...values.values()] })}\n\n`);
    streams.add(res); req.on('close', () => streams.delete(res)); return;
  }
  if (url.pathname === '/v1/keys') {
    res.end(JSON.stringify({ keys: [...values.keys()].filter(k => k.startsWith(url.searchParams.get('prefix'))).sort().slice(0, Number(url.searchParams.get('limit'))) })); return;
  }
  if (url.pathname.startsWith('/v1/data/')) {
    const key = decodeURIComponent(url.pathname.slice('/v1/data/'.length));
    if (req.method === 'PUT') {
      let raw = ''; for await (const chunk of req) raw += chunk;
      assert.equal(req.headers['x-ttl'], '300');
      if (putStatus === 201 || putStatus === 202) put(key, JSON.parse(raw));
      res.writeHead(putStatus).end(); return;
    }
    const e = values.get(key);
    if (!e || Date.parse(e.expires_at) <= now) { res.writeHead(404).end(); return; }
    res.setHeader('X-Original-TTL', String(e.ttl_seconds));
    res.setHeader('X-Remaining-TTL', String(Math.floor((Date.parse(e.expires_at) - now) / 1000)));
    res.end(JSON.stringify(e.data)); return;
  }
  res.writeHead(404).end();
});
const a = node('node-a'), b = node('node-b');
const origin = s => 'http://127.0.0.1:' + s.address().port;
const site = http.createServer((req, res) => {
  let file = new URL(req.url, 'http://fixture').pathname;
  if (file === '/fade/config.json') { res.end(JSON.stringify({ nodes: [origin(a), origin(b)] })); return; }
  if (file.endsWith('/')) file += 'index.html';
  const dest = path.resolve(root, '.' + file);
  if (!dest.startsWith(root + path.sep) || !fs.existsSync(dest)) { res.writeHead(404).end(); return; }
  const types = { '.html': 'text/html', '.css': 'text/css', '.js': 'text/javascript' };
  res.setHeader('Content-Type', types[path.extname(dest)] || 'application/octet-stream'); res.end(fs.readFileSync(dest));
});
const wait = (page, condition, argument) => page.waitForFunction(condition, argument, { timeout: 10000 });
(async () => {
  for (const server of [a, b, site]) await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  let browser;
  try {
    browser = await chromium.launch({ headless: true, ...(process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {}) });
    const ctx = await browser.newContext({ viewport: { width: 1280, height: 960 }, reducedMotion: 'reduce' });
    await ctx.route('https://fonts.googleapis.com/**', route => route.abort());
    const first = await ctx.newPage(), second = await ctx.newPage(), errors = [];
    for (const page of [first, second]) page.on('pageerror', e => errors.push(e.message));
    const address = origin(site) + '/fade/?node=';
    await Promise.all([first.goto(address + encodeURIComponent(origin(a))), second.goto(address + encodeURIComponent(origin(b)))]);
    await Promise.all([first, second].map(page => wait(page, () => document.querySelector('#connection').textContent.includes('· live'))));
    assert.equal(await first.locator('body').evaluate(e => getComputedStyle(e).backgroundColor), 'rgb(10, 10, 15)');
    assert.equal(await first.locator('h1 em').evaluate(e => getComputedStyle(e).color), 'rgb(57, 255, 20)');
    await first.locator('#callsign').fill('Fixture caller');
    await first.locator('#message').fill('<img src=x onerror="alert(1)"> Hello from Fade');
    await first.locator('#send').click();
    await wait(second, () => document.querySelector('#messages').textContent.includes('Hello from Fade'));
    assert.equal(await second.locator('#messages img').count(), 0, 'network payload rendered as markup');
    const key = await first.locator('#lookup-key').inputValue();
    assert.match(key, /^fade:v1:/); assert.equal(values.get(key).data.callsign, 'Fixture caller');
    await second.locator('.lookup summary').click(); await second.locator('#lookup-key').fill(key); await second.locator('#retrieve').click();
    await wait(second, () => document.querySelector('#lookup-result').textContent.includes('Hello from Fade'));
    const oldRevision = values.get(key).revision;
    put(key, valid('Replaced in place')); emit('expire', { key, revision: oldRevision });
    await wait(second, () => document.querySelector('#messages').textContent.includes('Replaced in place'));
    assert.equal(await second.locator('#messages article').count(), 1, 'old expiry removed a replacement');
    put('unrelated:key', valid('Not a Fade key'));
    put('fade:v1:' + randomUUID(), { app: 'other', schema: 1, text: 'Wrong app' });
    assert.equal(await second.locator('#messages article').count(), 1);
    putStatus = 202; await first.locator('#message').fill('Pending is accepted'); await first.locator('#send').click();
    await wait(first, () => document.querySelector('#send-status').textContent.includes('replication pending'));
    assert.equal(await first.locator('#message').inputValue(), '');
    putStatus = 507; await first.locator('#message').fill('Keep this draft'); await first.locator('#send').click();
    await wait(first, () => document.querySelector('#send-status').textContent.includes('507'));
    assert.equal(await first.locator('#message').inputValue(), 'Keep this draft');
    putStatus = 502; const putsBefore = requests.filter(r => r.includes(' PUT ')).length;
    await first.locator('#send').click(); await wait(first, () => document.querySelector('#send-status').textContent.includes('unknown'));
    assert.equal(requests.filter(r => r.includes(' PUT ')).length, putsBefore + 1, 'ambiguous PUT was retried');
    assert.equal(await first.locator('#message').inputValue(), 'Keep this draft');
    let abortedPuts = 0;
    await first.route('**/v1/data/**', route => {
      if (route.request().method() === 'PUT') { abortedPuts++; return route.abort('failed'); }
      return route.continue();
    });
    await first.locator('#send').click();
    await wait(first, () => document.querySelector('#send-status').textContent.startsWith('Delivery is unknown.'));
    assert.equal(abortedPuts, 1, 'application retried a failed fetch');
    assert.equal(await first.locator('#message').inputValue(), 'Keep this draft');
    await first.unroute('**/v1/data/**');
    now += 301000; emit('clock', { now: new Date(now).toISOString() });
    await wait(second, () => document.querySelectorAll('#messages article').length === 0 && document.querySelectorAll('#lookup-result article').length === 0);
    values.clear(); putStatus = 201; pollingOnly = true;
    const pollingKey = 'fade:v1:' + randomUUID(); put(pollingKey, valid('Polling fallback'));
    await second.reload(); await wait(second, () => document.querySelector('#messages').textContent.includes('Polling fallback'));
    assert.ok(await second.locator('#connection').textContent().then(t => t.includes('refreshing')));
    assert.ok(requests.some(r => r.includes(' GET /v1/keys')));
    // A partial stream snapshot must reconcile via the ordinary key API too.
    pollingOnly = false; omitted = 10; await second.reload();
    await wait(second, () => document.querySelector('#connection').textContent.includes('refreshing'));
    await wait(second, () => document.querySelector('#messages').textContent.includes('Polling fallback'));
    omitted = 0; unavailable = true; await first.locator('#node').selectOption('');
    await wait(first, () => document.querySelector('#connection').textContent === 'node-b · live');
    assert.equal(await first.locator('#node').inputValue(), '', 'automatic failover changed the saved selection');
    await first.setViewportSize({ width: 390, height: 844 });
    assert.ok(await first.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'mobile horizontal overflow');
    assert.deepEqual(errors, []);
    if (process.env.FADE_SCREENSHOT) await first.screenshot({ path: process.env.FADE_SCREENSHOT, fullPage: true });
    console.log('Fade browser checks passed: cross-node messages, literal payloads, overwrite/expiry, lookup, pending/failed writes, polling, failover, mobile, shared styling.');
  } finally {
    if (browser) await browser.close();
    for (const s of streams) s.end();
    for (const server of [a, b, site]) { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); }
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
