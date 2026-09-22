// Deterministic browser coverage for the real viewer assets and SSE protocol.
// PLAYWRIGHT_MODULE may point to an existing playwright/playwright-core install.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const root = path.resolve(__dirname, '../../sites/soph.stream');
const viewers = new Set();
let items = [], clock = Date.now(), connectionCount = 0;
const entry = (key, payload, revision = '1', ttl = 300) => ({
  key, payload: Buffer.from(payload).subarray(0, 4096).toString('base64'),
  size: Buffer.byteLength(payload), truncated: Buffer.byteLength(payload) > 4096,
  revision, ttl_seconds: ttl, written_at: new Date(clock).toISOString(), expires_at: new Date(clock + ttl * 1000).toISOString()
});
const emit = (kind, data) => { for (const response of viewers) response.write(`event: ${kind}\ndata: ${JSON.stringify(data)}\n\n`); };
const server = http.createServer((req, res) => {
  const url = new URL(req.url, 'http://localhost');
  if (url.pathname === '/v1/stream') {
    connectionCount++;
    res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-store' });
    res.write('retry: 100\n');
    res.write(`event: snapshot\ndata: ${JSON.stringify({ now: new Date(clock).toISOString(), node: 'fixture', enclave: 'test', entries: items })}\n\n`);
    viewers.add(res); req.on('close', () => viewers.delete(res)); return;
  }
  if (url.pathname.startsWith('/v1/data/')) {
    const key = decodeURIComponent(url.pathname.slice('/v1/data/'.length));
    if (key === 'vanished') { res.writeHead(404).end(); return; }
    res.writeHead(200, { 'Content-Type': 'application/octet-stream' }).end(key === 'large' ? 'z'.repeat(6000) : 'current value'); return;
  }
  const file = url.pathname === '/' ? 'index.html' : url.pathname.slice(1);
  if (!['index.html', 'styles.css', 'viewer.js'].includes(file)) { res.writeHead(404).end(); return; }
  const types = { '.html': 'text/html', '.css': 'text/css', '.js': 'text/javascript' };
  res.writeHead(200, { 'Content-Type': types[path.extname(file)] }).end(fs.readFileSync(path.join(root, file)));
});
(async () => {
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  let browser;
  try {
    browser = await chromium.launch({ headless: true, ...(process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {}) });
    const page = await browser.newPage({ viewport: { width: 1200, height: 900 }, reducedMotion: 'reduce' });
    const errors = [];
    page.on('pageerror', err => errors.push(err.message));
    const origin = `http://127.0.0.1:${server.address().port}`;
    items = [entry('alpha', 'first'), entry('large', 'z'.repeat(6000)), entry('binary', Buffer.from([255,128])), entry('empty', ''), entry('vanished', 'preview'), entry('markup', '<img src=x onerror="window.injected=true">')];
    await page.goto(origin + '/?node=' + encodeURIComponent(origin));
    await page.waitForFunction(() => document.querySelector('#connection').dataset.state === 'connected');
    await page.waitForFunction(() => document.querySelectorAll('.value-card').length === 6);
    const alpha = page.locator('[data-key="alpha"]');
    const position = await alpha.boundingBox();
    emit('put', { now: new Date(clock).toISOString(), node: 'fixture', entry: entry('alpha', 'second', '2') });
    await page.waitForFunction(() => document.querySelector('[data-key="alpha"] .card-payload').textContent === 'second');
    assert.deepEqual(await alpha.boundingBox(), position);
    emit('expire', { key: 'alpha', revision: '1', node: 'fixture' });
    emit('put', { now: new Date(clock).toISOString(), node: 'fixture', entry: entry('alpha', 'stale', '1') });
    await page.waitForTimeout(100);
    assert.equal(await alpha.locator('.card-payload').textContent(), 'second');
    await page.locator('#query').fill('second');
    assert.equal(await alpha.isVisible(), true);
    assert.equal(await page.locator('[data-key="large"]').isVisible(), false);
    assert.deepEqual(await alpha.boundingBox(), position);
    assert.equal(new URL(page.url()).searchParams.get('q'), 'second');
    await page.locator('#query').fill('');
    await page.locator('[data-key="large"]').click();
    await page.waitForFunction(() => document.querySelector('#detail-status').textContent.startsWith('Current value fetched'));
    assert.equal((await page.locator('#detail-payload').textContent()).length, 6000);
    await page.keyboard.press('Escape');
    await page.locator('[data-key="vanished"]').click();
    await page.waitForFunction(() => document.querySelector('#detail-status').textContent.includes('no longer available'));
    await page.locator('#close-detail').click();
    assert.match(await page.locator('[data-key="binary"] .card-size').textContent(), /hex/);
    assert.equal(await page.evaluate(() => window.injected), undefined);
    assert.equal(await page.locator('[data-key="empty"] .card-payload').textContent(), '[empty value]');
    console.log('PASS: snapshot, stable overwrite, stale event rejection, filter slots, full reads, missing reads, inert payloads');

    // A later server clock expires entries without shortening production TTLs.
    clock += 301000;
    emit('clock', { now: new Date(clock).toISOString() });
    await page.waitForFunction(() => document.querySelectorAll('.value-card').length === 0);
    assert.equal(await page.locator('#empty-title').textContent(), 'This node is empty.');
    items = [entry('reconnected', 'new snapshot')];
    const previousConnections = connectionCount;
    for (const response of viewers) response.end();
    await page.waitForFunction(() => document.querySelector('[data-key="reconnected"]'));
    assert.ok(connectionCount > previousConnections);
    assert.equal(await page.locator('[data-key="alpha"]').count(), 0);
    console.log('PASS: clock-aware expiry and fresh snapshot after reconnect');

    for (let i=0; i<50; i++) emit('put', { now: new Date(clock).toISOString(), node: 'fixture', entry: entry('queued-'+i, 'value') });
    await page.waitForFunction(() => document.querySelector('#count').textContent === '51');
    assert.match(await page.locator('#queue').textContent(), /waiting/);
    const displayed = await page.locator('.value-card').first().getAttribute('data-key');
    emit('expire', { key: displayed, revision: '1', node: 'fixture' });
    await page.waitForFunction(key => !document.querySelector(`[data-key="${key}"]`), displayed);
    await page.setViewportSize({ width: 390, height: 844 });
    await page.waitForFunction(() => document.querySelectorAll('.value-card').length === 50);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    assert.equal(await page.locator('#queue').textContent(), '');
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.waitForTimeout(100);
    assert.equal(await page.locator('#count').textContent(), '50');
    assert.deepEqual(errors, []);
    console.log('PASS: bounded visible grid, overflow promotion, mobile column, resize, reduced motion');

    // Explicit standard HTTP ports must not be rewritten to the bare-host default.
    let standardPortRequested = false;
    await page.route('http://127.0.0.1/v1/stream', route => {
      standardPortRequested = true;
      return route.fulfill({ status: 200, headers: { 'Content-Type': 'text/event-stream', 'Access-Control-Allow-Origin': '*' }, body: `event: snapshot\ndata: ${JSON.stringify({ now: new Date(clock).toISOString(), node: 'port-80', enclave: 'test', entries: [] })}\n\n` });
    });
    await page.locator('#node').fill('http://127.0.0.1:80');
    await page.locator('#connect-form button').click();
    await page.waitForFunction(() => document.querySelector('#identity').textContent === 'port-80 / test');
    assert.ok(standardPortRequested);
    console.log('PASS: explicit URL port handling');
  } finally {
    if (browser) await browser.close();
    for (const response of viewers) response.destroy();
    await new Promise(resolve => server.close(resolve));
  }
})().catch(err => { console.error(err); process.exitCode = 1; });
