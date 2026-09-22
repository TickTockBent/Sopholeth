// Exercise the real soph serve binary behind an HTTPS port-forwarding prefix.
// Build bin/soph first. Requires Playwright/Chromium and openssl on PATH.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const { spawn, execFileSync } = require('node:child_process');
const http = require('node:http');
const https = require('node:https');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const assert = require('node:assert/strict');
const { once } = require('node:events');

(async () => {
  const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'soph-viewer-proxy-'));
  const streams = new Set();
  let node, proxy, child, exited, browser;
  let connections = 0;
  const key = 'remote:hello ?#';
  let payload = 'remote ' + 'z'.repeat(6000), revision = 1;
  const entry = () => ({ key, payload: Buffer.from(payload).subarray(0, 4096).toString('base64'),
    size: Buffer.byteLength(payload), truncated: payload.length > 4096, revision: String(revision),
    ttl_seconds: 300, written_at: new Date().toISOString(), expires_at: new Date(Date.now() + 300000).toISOString() });
  try {
    execFileSync('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
      '-subj', '/CN=localhost', '-keyout', path.join(temp, 'key.pem'), '-out', path.join(temp, 'cert.pem')], { stdio: 'ignore' });
    node = http.createServer((req, res) => {
      if (req.url === '/v1/health') {
        res.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify({
          status: 'healthy', node_id: 'proxy-fixture', network: 'private', enclave: 'lab'
        }));
      } else if (req.url === '/v1/stream') {
        connections++;
        res.writeHead(200, { 'Content-Type': 'text/event-stream' });
        res.write('retry: 100\n');
        res.write(`event: snapshot\ndata: ${JSON.stringify({ now: new Date().toISOString(), node: 'proxy-fixture', enclave: 'lab', entries: [entry()] })}\n\n`);
        streams.add(res); res.on('close', () => streams.delete(res));
      } else if (decodeURIComponent(req.url) === '/v1/data/' + key) {
        res.writeHead(200, { 'Content-Type': 'application/octet-stream' }).end(payload);
      } else res.writeHead(404).end();
    });
    await new Promise(resolve => node.listen(0, '127.0.0.1', resolve));
    const nodeURL = `http://127.0.0.1:${node.address().port}`;
    child = spawn(path.resolve(__dirname, '../../bin/soph'), ['--config', path.join(temp, 'soph.json'),
      '--json', 'serve', '--node', nodeURL, '--port', '0', '--q', 'remote'], { stdio: ['ignore', 'pipe', 'pipe'] });
    exited = once(child, 'exit');
    let stderr = '';
    child.stderr.on('data', chunk => { stderr += chunk; });
    const result = await new Promise((resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error('soph serve did not start: ' + stderr)), 10000);
      let output = '';
      child.once('error', reject);
      child.stdout.on('data', chunk => {
        output += chunk;
        if (output.includes('\n')) {
          clearTimeout(timeout);
          try { resolve(JSON.parse(output.split('\n')[0])); } catch (err) { reject(err); }
        }
      });
      child.once('exit', code => { clearTimeout(timeout); reject(new Error(`soph serve exited ${code}: ${stderr}`)); });
    });
    const prefix = '/proxy/8181/';
    proxy = https.createServer({ key: fs.readFileSync(path.join(temp, 'key.pem')), cert: fs.readFileSync(path.join(temp, 'cert.pem')) }, (req, res) => {
      if (!req.url.startsWith(prefix)) { res.writeHead(404).end(); return; }
      const upstream = http.request(new URL('/' + req.url.slice(prefix.length), result.url), { method: req.method, headers: req.headers }, response => {
        res.writeHead(response.statusCode, response.headers);
        response.pipe(res);
      });
      upstream.on('error', () => { if (!res.headersSent) res.writeHead(502); res.end(); });
      res.on('close', () => upstream.destroy());
      req.pipe(upstream);
    });
    await new Promise(resolve => proxy.listen(0, '127.0.0.1', resolve));
    const base = `https://127.0.0.1:${proxy.address().port}${prefix}`;
    browser = await chromium.launch({ headless: true, ...(process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {}) });
    const page = await browser.newPage({ ignoreHTTPSErrors: true, viewport: { width: 1200, height: 900 }, reducedMotion: 'reduce' });
    const requests = [], errors = [], failures = [];
    page.on('request', req => requests.push(req.url()));
    page.on('pageerror', err => errors.push(err.message));
    page.on('requestfailed', req => failures.push(req.url()));
    await page.goto(base + '?'); // Port forwarders can omit the printed query.
    await page.waitForFunction(() => document.querySelector('#connection').dataset.state === 'connected');
    assert.equal(await page.locator('#identity').textContent(), 'proxy-fixture / lab');
    assert.equal(await page.locator('#node').inputValue(), nodeURL);
    assert.equal(await page.locator('#query').inputValue(), 'remote');
    assert.equal(await page.evaluate(() => getComputedStyle(document.body).backgroundColor), 'rgb(10, 10, 15)');
    await page.locator('.value-card').click();
    await page.waitForFunction(() => document.querySelector('#detail-status').textContent.startsWith('Current value fetched'));
    assert.equal(await page.locator('#detail-payload').textContent(), payload);
    await page.locator('#close-detail').click();
    console.log('PASS: HTTPS proxy prefix, styled page, saved node/filter without query, streamed snapshot, full read');

    payload = 'remote update'; revision++;
    for (const stream of streams) stream.write(`event: put\ndata: ${JSON.stringify({ now: new Date().toISOString(), node: 'proxy-fixture', entry: entry() })}\n\n`);
    await page.waitForFunction(() => document.querySelector('.card-payload').textContent === 'remote update');
    const previousConnections = connections;
    payload = 'remote reconnected update'; revision++;
    for (const stream of streams) stream.end();
    await page.waitForFunction(() => document.querySelector('#connection').dataset.state === 'connected' && document.querySelector('.card-payload')?.textContent === 'remote reconnected update');
    assert.ok(connections > previousConnections);
    await page.locator('.brand').click();
    await page.waitForFunction(() => document.querySelector('#connection').dataset.state === 'connected');
    assert.equal(new URL(page.url()).pathname, prefix);
    await page.goto(base + '?q=update');
    await page.waitForFunction(() => document.querySelector('#connection').dataset.state === 'connected');
    assert.equal(await page.locator('#query').inputValue(), 'update');
    assert.ok(requests.every(url => url.startsWith(base)), 'browser attempted to bypass the HTTPS port forward');
    assert.deepEqual(errors, []);
    // Navigating away can cancel the old SSE request; asset/data failures cannot.
    assert.ok(failures.every(url => url === base + 'v1/stream'), JSON.stringify(failures));
    console.log('PASS: live updates, reconnect, home navigation, query override, all browser traffic under proxy prefix');
    child.kill('SIGTERM');
    assert.deepEqual(await exited, [0, null]);
    console.log('PASS: clean CLI shutdown with an active proxied stream');
  } finally {
    if (browser) await browser.close();
    if (child && child.exitCode === null && child.signalCode === null) { child.kill('SIGTERM'); await exited; }
    for (const stream of streams) stream.destroy();
    for (const server of [proxy, node]) if (server) {
      server.closeAllConnections();
      await new Promise(resolve => server.close(resolve));
    }
    fs.rmSync(temp, { recursive: true, force: true });
  }
})().catch(err => { console.error(err); process.exitCode = 1; });
