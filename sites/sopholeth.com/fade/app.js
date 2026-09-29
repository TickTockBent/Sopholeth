(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  const prefix = 'fade:v1:', keyPattern = /^fade:v1:[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$/;
  const messages = new Map(), cards = new Map();
  const maxMessages = 200, pollLimit = 80;
  let nodes = [], nextNode = 0, session = null, reconnect = null, failures = 0, sending = false, mutation = 0;
  let lookup = null, lookupSerial = 0;
  const allowedTTLs = new Set([300, 900, 3600, 21600, 86400]);
  const clock = () => session ? session.epoch + performance.now() - session.anchor : Date.now();
  const active = s => session === s && !s.abort.signal.aborted;
  const timeLeft = ms => {
    const n = Math.max(0, Math.ceil(ms / 1000));
    return [Math.floor(n / 3600), Math.floor(n / 60) % 60, n % 60].map(v => String(v).padStart(2, '0')).join(':');
  };
  function feedback(id, text, error = false) { $(id).textContent = text; $(id).dataset.error = String(error); }
  function controls() { $('send').disabled = sending || !session?.ready; $('retrieve').disabled = !session?.ready; }
  function status(text, state) { $('connection').textContent = text; $('connection').dataset.state = state; }
  function normalizeNode(value) {
    const u = new URL(value);
    if (!['http:', 'https:'].includes(u.protocol) || u.username || u.password || u.pathname !== '/' || u.search || u.hash) throw new Error('Use an HTTP or HTTPS node origin.');
    if (location.protocol === 'https:' && u.protocol !== 'https:') throw new Error('An HTTPS page needs an HTTPS node.');
    return u.origin;
  }
  async function request(s, path, options = {}) {
    const signal = AbortSignal.any([s.abort.signal, AbortSignal.timeout(12000)]);
    return fetch(s.origin + path, { ...options, signal, cache: 'no-store', credentials: 'omit', redirect: 'error' });
  }
  function payload(text) {
    if (text.length > 4096) return null;
    try {
      const p = JSON.parse(text);
      if (p?.app !== 'fade' || p.schema !== 1 || typeof p.text !== 'string' || !p.text.trim() || p.text.length > 280 ||
          typeof p.callsign !== 'string' || p.callsign.length > 20 || typeof p.location !== 'string' || p.location.length > 30) return null;
      return { text: p.text, callsign: p.callsign, location: p.location };
    } catch { return null; }
  }
  function streamEntry(raw) {
    if (!raw || !keyPattern.test(raw.key) || raw.truncated || typeof raw.payload !== 'string' || raw.payload.length > 5464 ||
        typeof raw.revision !== 'string' || !/^\d+$/.test(raw.revision) || !Number.isFinite(raw.ttl_seconds) || raw.ttl_seconds <= 0) return null;
    try {
      const data = payload(new TextDecoder('utf-8', { fatal: true }).decode(Uint8Array.from(atob(raw.payload), ch => ch.charCodeAt(0))));
      const written = Date.parse(raw.written_at), expires = Date.parse(raw.expires_at);
      if (!data || !Number.isFinite(written) || !Number.isFinite(expires) || expires <= clock()) return null;
      return { key: raw.key, ...data, written, expires, ttl: raw.ttl_seconds, revision: raw.revision };
    } catch { return null; }
  }
  function accept(entry) {
    if (!entry || entry.expires <= clock()) return;
    messages.set(entry.key, { ...entry, changed: ++mutation });
    if (messages.size > maxMessages) {
      const ordered = [...messages.values()].sort((a, b) => b.written - a.written || a.key.localeCompare(b.key));
      for (const old of ordered.slice(maxMessages)) messages.delete(old.key);
    }
  }
  function setClock(s, value) {
    const epoch = Date.parse(value);
    if (!Number.isFinite(epoch)) throw new Error('Invalid stream clock');
    s.epoch = epoch; s.anchor = performance.now();
  }
  function closeSession() {
    clearTimeout(reconnect);
    if (session) { session.abort.abort(); session.source?.close(); clearTimeout(session.timer); clearInterval(session.pollTimer); }
  }
  function retry(s) {
    if (!active(s)) return;
    closeSession(); s.ready = false; controls();
    messages.clear(); render(); clearLookup();
    status('Connection lost · trying again…', 'disconnected');
    const delay = Math.min(30000, 1000 * 2 ** Math.min(failures++, 5));
    reconnect = setTimeout(() => connect(), delay);
  }
  async function connect() {
    closeSession(); messages.clear(); cards.clear(); $('messages').replaceChildren(); clearLookup();
    const origin = $('node').value || nodes[nextNode++ % nodes.length];
    if (!origin) { status('No nodes configured.', 'disconnected'); return; }
    const s = session = { origin, abort: new AbortController(), ready: false, source: null, epoch: Date.now(), anchor: performance.now(), polling: false };
    controls(); render(); status('Connecting to ' + new URL(origin).host + '…', 'connecting');
    try {
      const r = await request(s, '/v1/health');
      if (!r.ok) throw new Error('Node unavailable');
      const health = await r.json();
      if (health.status !== 'healthy' || typeof health.node_id !== 'string' || !health.node_id || typeof health.enclave !== 'string') throw new Error('Invalid health response');
      if (!active(s)) return;
      s.label = health.node_id; s.ready = true; controls(); startStream(s);
    } catch { retry(s); }
  }
  function startStream(s) {
    const source = s.source = new EventSource(s.origin + '/v1/stream');
    s.timer = setTimeout(() => startPolling(s), 10000);
    function event(name, fn) {
      source.addEventListener(name, e => {
        if (!active(s) || s.source !== source) return;
        try { fn(JSON.parse(e.data)); } catch { startPolling(s); }
      });
    }
    event('snapshot', data => {
      if (!Array.isArray(data.entries) || data.entries.length > 4096) throw new Error('Invalid snapshot');
      setClock(s, data.now); clearTimeout(s.timer); messages.clear();
      for (const raw of data.entries) accept(streamEntry(raw));
      failures = 0; status(s.label + ' · live', 'connected');
      $('board-note').textContent = data.omitted > 0 ? 'The node’s snapshot is partial. Checking Fade messages separately.' : 'One node’s view · up to 200 recent messages · local lifetimes';
      render();
      if (data.omitted > 0) startPolling(s);
    });
    event('put', data => {
      setClock(s, data.now);
      if (!keyPattern.test(data.entry?.key)) return;
      const entry = streamEntry(data.entry);
      if (entry) accept(entry); else { messages.delete(data.entry.key); ++mutation; }
      render();
    });
    event('expire', data => {
      if (messages.get(data.key)?.revision === data.revision) { messages.delete(data.key); ++mutation; render(); }
    });
    event('clock', data => { setClock(s, data.now); render(); });
    source.onerror = () => { if (active(s) && s.source === source) startPolling(s); };
  }
  function startPolling(s) {
    if (!active(s) || s.pollTimer) return;
    clearTimeout(s.timer); s.source?.close(); s.source = null;
    status(s.label + ' · refreshing every 5 seconds', 'connected');
    poll(s); s.pollTimer = setInterval(() => poll(s), 5000);
  }
  async function readMessage(s, key) {
    const r = await request(s, '/v1/data/' + encodeURIComponent(key));
    if (r.status === 404) return null;
    if (!r.ok) throw new Error('Read failed (' + r.status + ')');
    if (Number(r.headers.get('Content-Length')) > 4096) return null;
    const data = payload(await r.text());
    const ttl = Number(r.headers.get('X-Original-TTL')), remaining = Number(r.headers.get('X-Remaining-TTL'));
    if (!data || !Number.isFinite(ttl) || ttl <= 0 || !Number.isFinite(remaining) || remaining <= 0 || remaining > ttl) return null;
    const now = s.epoch + performance.now() - s.anchor;
    return { key, ...data, written: now - (ttl - remaining) * 1000, expires: now + remaining * 1000, ttl, revision: null };
  }
  async function poll(s) {
    if (!active(s) || s.polling) return;
    s.polling = true; const began = mutation;
    try {
      const r = await request(s, '/v1/keys?prefix=' + encodeURIComponent(prefix) + '&limit=' + pollLimit);
      if (!r.ok) throw new Error('Listing failed');
      const page = await r.json();
      if (page.keys !== null && !Array.isArray(page.keys)) throw new Error('Invalid listing');
      const keys = (page.keys || []).slice(0, pollLimit).filter(k => typeof k === 'string' && keyPattern.test(k));
      const found = new Map(); let index = 0;
      await Promise.all(Array.from({ length: 4 }, async () => {
        while (index < keys.length && active(s)) {
          const key = keys[index++]; found.set(key, await readMessage(s, key));
        }
      }));
      if (!active(s)) return;
      for (const [key, entry] of messages) if (entry.changed <= began && !found.has(key)) messages.delete(key);
      for (const [key, entry] of found) {
        if ((messages.get(key)?.changed || 0) > began) continue;
        if (entry) accept(entry); else messages.delete(key);
      }
      failures = 0;
      $('board-note').textContent = page.next_cursor ? 'Showing a bounded selection of 80 keys from this node. Retrieve other messages by key.' : 'One node’s view · messages disappear at their local expiry';
      render();
    } catch { retry(s); }
    finally { s.polling = false; }
  }
  function makeCard(entry) {
    const card = document.createElement('article'); card.className = 'message'; card.dataset.key = entry.key;
    // Static structure only. Every network-provided string is assigned with textContent.
    card.innerHTML = '<div class="message-head"><div><span class="author"></span><span class="place"></span></div><span class="lifetime"></span></div><p class="message-text"></p><div class="message-foot"><span class="stamp"></span><button class="copy-key" type="button">Copy key</button></div><div class="life-track" aria-hidden="true"></div>';
    card.querySelector('.copy-key').addEventListener('click', async e => {
      $('lookup-key').value = entry.key;
      try { await navigator.clipboard.writeText(entry.key); e.target.textContent = 'Copied'; }
      catch { document.querySelector('.lookup').open = true; $('lookup-key').focus(); $('lookup-key').select(); e.target.textContent = 'Key ready below'; }
    });
    return card;
  }
  function updateCard(card, entry) {
    const remaining = Math.max(0, entry.expires - clock()), fraction = Math.min(1, remaining / (entry.ttl * 1000));
    card.querySelector('.author').textContent = entry.callsign || 'Anonymous';
    card.querySelector('.place').textContent = entry.location;
    card.querySelector('.message-text').textContent = entry.text;
    card.querySelector('.message-text').style.opacity = String(.45 + fraction * .55);
    card.querySelector('.lifetime').textContent = timeLeft(remaining) + ' left';
    card.querySelector('.stamp').textContent = new Date(entry.written).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    card.querySelector('.life-track').style.transform = 'scaleX(' + fraction + ')';
    card.classList.toggle('late', fraction < .2);
  }
  function render() {
    for (const [key, entry] of messages) if (entry.expires <= clock()) messages.delete(key);
    for (const [key, card] of cards) if (!messages.has(key)) { card.remove(); cards.delete(key); }
    const ordered = [...messages.values()].sort((a, b) => b.written - a.written || a.key.localeCompare(b.key));
    let previous = null;
    for (const entry of ordered) {
      let card = cards.get(entry.key);
      if (!card) { card = makeCard(entry); cards.set(entry.key, card); }
      updateCard(card, entry);
      const before = previous ? previous.nextSibling : $('messages').firstChild;
      if (before !== card) $('messages').insertBefore(card, before);
      previous = card;
    }
    $('empty').hidden = ordered.length > 0;
    $('count').textContent = ordered.length + (ordered.length === 1 ? ' message here' : ' messages here');
    if (lookup) {
      if (lookup.entry.expires <= clock()) { clearLookup(); feedback('lookup-status', 'This message has expired on the observed node.'); }
      else updateCard(lookup.card, lookup.entry);
    }
  }
  function clearLookup() { lookup = null; ++lookupSerial; $('lookup-result').replaceChildren(); feedback('lookup-status', ''); }
  $('lookup-form').addEventListener('submit', async e => {
    e.preventDefault(); clearLookup(); const serial = lookupSerial, key = $('lookup-key').value.trim(), s = session;
    if (!keyPattern.test(key)) { feedback('lookup-status', 'Enter a Fade message key beginning with fade:v1:.', true); return; }
    if (!s?.ready) return;
    feedback('lookup-status', 'Looking on ' + s.label + '…');
    try {
      const entry = await readMessage(s, key);
      if (!active(s) || lookupSerial !== serial) return;
      if (!entry) { feedback('lookup-status', 'No live Fade message at this key on ' + s.label + '.'); return; }
      const card = makeCard(entry); lookup = { entry, card }; $('lookup-result').replaceChildren(card); updateCard(card, entry);
      feedback('lookup-status', 'Read from ' + s.label + '.');
    } catch { if (active(s) && lookupSerial === serial) feedback('lookup-status', 'Could not read this message. Try again when connected.', true); }
  });
  $('compose').addEventListener('submit', async e => {
    e.preventDefault(); if (sending || !session?.ready) return;
    const s = session, draft = $('message').value, ttl = Number($('ttl').value);
    const data = { app: 'fade', schema: 1, text: draft.trim(), callsign: $('callsign').value.trim(), location: $('location').value.trim() };
    if (!payload(JSON.stringify(data)) || !allowedTTLs.has(ttl)) { feedback('send-status', 'Enter a message of 1–280 characters and choose a lifetime.', true); return; }
    const key = prefix + crypto.randomUUID(); $('lookup-key').value = key;
    sending = true; controls(); feedback('send-status', 'Transmitting through ' + s.label + '…');
    try {
      // One fetch, with no application retry. The browser's HTTP transport may
      // retry a PUT internally; a failed response can still mean it was stored.
      // Keep this request independent of read-side failover or node switching.
      const r = await fetch(s.origin + '/v1/data/' + encodeURIComponent(key), {
        method: 'PUT', headers: { 'Content-Type': 'application/json', 'X-TTL': String(ttl) }, body: JSON.stringify(data),
        signal: AbortSignal.timeout(20000), credentials: 'omit', cache: 'no-store', redirect: 'error'
      });
      if (r.status !== 201 && r.status !== 202) {
        const known = [400, 413, 429, 507].includes(r.status);
        feedback('send-status', known ? 'Not accepted (' + r.status + '). Your draft is kept.' : 'Response ' + r.status + '; delivery is unknown. Check the message key before sending again.', true);
        return;
      }
      if ($('message').value === draft) { $('message').value = ''; $('characters').textContent = '0'; }
      feedback('send-status', r.status === 202 ? 'Stored on ' + s.label + ' · replication pending. Your message key is ready below.' : 'Stored on ' + s.label + ' · quorum observed. Your message key is ready below.');
      if (active(s) && !messages.has(key)) {
        const began = mutation;
        try { const entry = await readMessage(s, key); if (active(s) && (messages.get(key)?.changed || 0) <= began) { accept(entry); render(); } } catch { /* The accepted write remains successful. */ }
      }
    } catch { feedback('send-status', 'Delivery is unknown. Your draft is kept; check the message key before sending again.', true); }
    finally { sending = false; controls(); }
  });
  $('message').addEventListener('input', () => { $('characters').textContent = String($('message').value.length); });
  $('node').addEventListener('change', () => {
    const u = new URL(location.href);
    if ($('node').value) u.searchParams.set('node', $('node').value); else u.searchParams.delete('node');
    history.replaceState(null, '', u); failures = 0; connect();
  });
  let ticker = setInterval(render, 1000);
  window.addEventListener('pagehide', () => { closeSession(); clearInterval(ticker); });
  window.addEventListener('pageshow', e => { if (e.persisted) { ticker = setInterval(render, 1000); connect(); } });
  async function init() {
    try {
      const r = await fetch('./config.json', { cache: 'no-store' });
      if (!r.ok) throw new Error('Cannot load node configuration.');
      const config = await r.json(); nodes = [...new Set(config.nodes.map(normalizeNode))];
      const explicit = new URL(location.href).searchParams.get('node');
      if (explicit) { const origin = normalizeNode(explicit); if (!nodes.includes(origin)) nodes.push(origin); }
      for (const origin of nodes) { const option = document.createElement('option'); option.value = origin; option.textContent = new URL(origin).host; $('node').append(option); }
      if (explicit) $('node').value = normalizeNode(explicit);
      nextNode = Math.floor(Math.random() * nodes.length); await connect();
    } catch (error) { status(error.message || 'Could not load Fade.', 'disconnected'); }
  }
  init();
})();
