(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  const grid = $('grid'), viewport = $('viewport'), dialog = $('detail');
  const entries = new Map(), cards = new Map(), retiring = new Map();
  let slots = [], slotElements = [], source = null, endpoint = '', identity = null;
  let servedNode = '';
  let state = 'idle', query = '', mobile = false, capacity = 0;
  let epoch = Date.now(), anchor = performance.now(), detailKey = null, detailRequest = null;
  const reducedMotion = matchMedia('(prefers-reduced-motion: reduce)');
  const now = () => epoch + performance.now() - anchor;
  const syncClock = value => { const time = Date.parse(value); if (!Number.isFinite(time)) throw new Error('Invalid node clock'); epoch = time; anchor = performance.now(); };
  const formatTime = ms => {
    const seconds = Math.max(0, Math.ceil(ms / 1000));
    return [Math.floor(seconds / 3600), Math.floor(seconds / 60) % 60, seconds % 60].map(n => String(n).padStart(2, '0')).join(':');
  };
  function normalizeNode(value) {
    const explicitURL = value.includes('://');
    const url = new URL(explicitURL ? value : 'http://' + value);
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash || url.pathname !== '/') throw new Error('Use an HTTP or HTTPS node address, without a path.');
    if (!explicitURL && !url.port) url.port = '8080';
    if (location.protocol === 'https:' && url.protocol !== 'https:' && url.origin !== servedNode) throw new Error('This HTTPS page needs an HTTPS node. Use soph serve for a local HTTP node.');
    return url.origin;
  }
  function nodeURL(path) {
    // soph serve forwards reads to its selected node. Relative URLs retain
    // a port-forwarding prefix and the browser's HTTPS origin.
    return endpoint === servedNode ? new URL('./' + path, location.href).href : endpoint + '/' + path;
  }
  function decodePayload(bytes, truncated = false) {
    try {
      const decoder = new TextDecoder('utf-8', { fatal: true });
      const text = decoder.decode(bytes, { stream: truncated });
      return { text: text || (bytes.length ? '[incomplete UTF-8 preview]' : '[empty value]'), binary: false };
    } catch {
      return { text: Array.from(bytes, b => b.toString(16).padStart(2, '0')).join(' '), binary: true };
    }
  }
  function parseEntry(raw) {
    if (!raw || typeof raw.key !== 'string' || typeof raw.revision !== 'string' || !/^\d+$/.test(raw.revision) || typeof raw.payload !== 'string' || raw.payload.length > 5464 || !Number.isFinite(raw.ttl_seconds) || !Number.isSafeInteger(raw.size) || raw.size < 0) throw new Error('Invalid stream entry');
    const expires = Date.parse(raw.expires_at), written = Date.parse(raw.written_at);
    if (!Number.isFinite(expires) || !Number.isFinite(written)) throw new Error('Invalid entry lifetime');
    const bytes = Uint8Array.from(atob(raw.payload), ch => ch.charCodeAt(0));
    if (bytes.length > 4096) throw new Error('Preview too large');
    return { ...raw, expires, written, ...decodePayload(bytes, raw.truncated) };
  }
  function saveURL() {
    const url = new URL(location.href);
    if (endpoint) url.searchParams.set('node', endpoint); else url.searchParams.delete('node');
    if (query) url.searchParams.set('q', query); else url.searchParams.delete('q');
    history.replaceState(null, '', url);
  }
  function setState(next, message) {
    state = next;
    $('connection').dataset.state = next;
    $('connection').textContent = message;
    render();
  }
  function clearView() {
    for (const value of retiring.values()) clearTimeout(value.timer);
    retiring.clear(); entries.clear(); cards.clear(); slots = Array(capacity).fill(null);
    for (const slot of slotElements) slot.replaceChildren();
    grid.replaceChildren(...slotElements);
    closeDetail();
  }
  function connect(value) {
    let target;
    try { target = normalizeNode(value.trim()); }
    catch (err) { $('node').setCustomValidity(err.message); $('node').reportValidity(); return; }
    if (source) source.close();
    endpoint = target; identity = null; clearView(); saveURL();
    $('node').value = endpoint; $('identity').textContent = endpoint;
    setState('connecting', 'Connecting to node…');
    const connection = new EventSource(nodeURL('v1/stream'));
    source = connection;
    const event = (name, callback) => connection.addEventListener(name, message => {
      if (source !== connection) return;
      try { callback(JSON.parse(message.data)); }
      catch {
        connection.close(); clearView();
        setState('disconnected', 'Invalid stream response. Check the node and reconnect.');
      }
    });
    event('snapshot', data => {
      if (!Array.isArray(data.entries) || data.entries.length > 4096 || typeof data.node !== 'string' || typeof data.enclave !== 'string') throw new Error('Invalid snapshot');
      const snapshot = data.entries.map(parseEntry);
      clearView(); syncClock(data.now);
      identity = { node: data.node, enclave: data.enclave };
      $('identity').textContent = data.node + ' / ' + data.enclave;
      for (const entry of snapshot) if (entry.expires > now()) entries.set(entry.key, entry);
      setState('connected', 'Connected · live local view');
    });
    event('put', data => {
      if (state !== 'connected' || !identity || data.node !== identity.node) return;
      syncClock(data.now);
      const entry = parseEntry(data.entry), previous = entries.get(entry.key);
      if (previous && BigInt(previous.revision) >= BigInt(entry.revision)) return;
      if (entry.expires <= now()) { if (previous) expire(entry.key); return; }
      const fading = retiring.get(entry.key);
      if (fading) { clearTimeout(fading.timer); retiring.delete(entry.key); cards.get(entry.key)?.classList.remove('out'); }
      entries.set(entry.key, entry);
      render();
      if (previous || fading) {
        const card = cards.get(entry.key);
        if (card) { card.classList.remove('flash'); void card.offsetWidth; card.classList.add('flash'); }
      }
      if (detailKey === entry.key) openDetail(entry.key);
    });
    event('expire', data => {
      const entry = entries.get(data.key);
      if (identity && data.node === identity.node && entry && entry.revision === data.revision) expire(data.key);
    });
    event('clock', data => { if (state === 'connected') { syncClock(data.now); tick(); } });
    connection.onerror = () => {
      if (source !== connection) return;
      clearView();
      setState('disconnected', 'Stream unavailable · retrying with a fresh snapshot…');
    };
  }
  function matches(entry) { return !query || (entry.key + '\n' + entry.text).toLocaleLowerCase().includes(query.toLocaleLowerCase()); }
  function fillSlots() {
    const assigned = new Set(slots.filter(key => key !== null));
    const free = slots.flatMap((key, i) => key === null ? [i] : []);
    for (const key of entries.keys()) {
      if (assigned.has(key)) continue;
      if (!free.length) break;
      const choice = Math.floor(Math.random() * free.length);
      slots[free.splice(choice, 1)[0]] = key;
    }
  }
  function cardFor(entry) {
    let card = cards.get(entry.key);
    if (!card) {
      card = $('card-template').content.firstElementChild.cloneNode(true);
      card.dataset.key = entry.key;
      const key = entry.key;
      card.addEventListener('click', () => openDetail(key));
      cards.set(entry.key, card);
    }
    card.querySelector('.card-key').textContent = entry.key || '[empty key]';
    card.querySelector('.card-payload').textContent = entry.text;
    card.querySelector('.card-size').textContent = entry.size.toLocaleString() + ' B' + (entry.binary ? ' · hex' : '') + (entry.truncated ? ' · preview' : '');
    card.hidden = !matches(entry);
    return card;
  }
  function render() {
    if (mobile) {
      for (const entry of entries.values()) {
        const card = cardFor(entry);
        if (card.parentElement !== grid) grid.append(card);
      }
    } else {
      fillSlots();
      slots.forEach((key, i) => {
        const entry = entries.get(key), card = entry ? cardFor(entry) : cards.get(key);
        const slot = slotElements[i];
        if (card) { if (slot.firstChild !== card) slot.replaceChildren(card); }
        else slot.replaceChildren();
      });
    }
    $('count').textContent = entries.size;
    const visibleKeys = new Set(slots.filter(key => entries.has(key)));
    const waiting = mobile ? 0 : entries.size - visibleKeys.size;
    $('queue').textContent = waiting ? ' / ' + waiting + ' waiting' : '';
    const matchesAny = [...entries.values()].some(matches);
    $('empty').hidden = state === 'connected' && entries.size > 0 && matchesAny;
    if (state === 'connected' && !entries.size) {
      $('empty-title').textContent = 'This node is empty.';
      $('empty-message').textContent = 'That is its default state. Write a value and watch its lifetime unfold.';
    } else if (state === 'connected' && !matchesAny) {
      $('empty-title').textContent = 'No matching values.';
      $('empty-message').textContent = 'Search covers keys and payload previews. Filtered values keep their slots.';
    } else if (state === 'disconnected') {
      $('empty-title').textContent = 'Waiting for the node.';
      $('empty-message').textContent = 'A new snapshot will replace this view when the connection returns.';
    } else if (state === 'connecting') {
      $('empty-title').textContent = 'Opening a window…';
      $('empty-message').textContent = 'Waiting for the node’s current snapshot.';
    }
    updateTimers();
  }
  function layout() {
    const wasMobile = mobile;
    mobile = matchMedia('(max-width: 680px)').matches;
    const columns = mobile ? 1 : Math.max(1, Math.floor((viewport.clientWidth + 12) / 262));
    const height = (viewport.clientWidth - (columns - 1) * 12) / columns / 1.5;
    const rows = Math.max(1, Math.floor((viewport.clientHeight + 12) / (height + 12)));
    const nextCapacity = mobile ? 0 : columns * rows;
    grid.style.gridTemplateColumns = 'repeat(' + columns + ', minmax(0, 1fr))';
    grid.style.gridTemplateRows = mobile ? '' : 'repeat(' + rows + ', ' + height + 'px)';
    if (nextCapacity !== capacity || mobile !== wasMobile) {
      for (const value of retiring.values()) clearTimeout(value.timer);
      for (const key of retiring.keys()) { cards.get(key)?.remove(); cards.delete(key); }
      retiring.clear();
      capacity = nextCapacity;
      slots = slots.filter(key => key !== null && entries.has(key)).slice(0, capacity);
      while (slots.length < capacity) slots.push(null);
      slotElements = Array.from({ length: capacity }, () => { const el = document.createElement('div'); el.className = 'value-slot'; return el; });
      grid.replaceChildren(...slotElements);
    }
    render();
  }
  function expire(key) {
    if (!entries.delete(key)) return;
    if (detailKey === key) closeDetail();
    const card = cards.get(key);
    const finish = () => {
      retiring.delete(key); card?.remove(); cards.delete(key);
      slots = slots.map(value => value === key ? null : value);
      render();
    };
    if (card && !reducedMotion.matches) {
      card.classList.add('out');
      retiring.set(key, { timer: setTimeout(finish, 250) });
    } else finish();
    render();
  }
  function updateTimers() {
    for (const [key, entry] of entries) {
      const card = cards.get(key);
      if (!card) continue;
      const remaining = entry.expires - now();
      const fraction = Math.max(0, Math.min(1, remaining / (entry.ttl_seconds * 1000)));
      card.querySelector('.ttl-left').textContent = formatTime(remaining);
      const fill = card.querySelector('.ttl-fill');
      fill.style.transform = 'scaleX(' + fraction + ')';
      fill.classList.toggle('late', fraction < .2);
    }
  }
  function tick() {
    for (const [key, entry] of entries) if (entry.expires <= now()) expire(key);
    updateTimers();
  }
  function closeDetail() {
    detailRequest?.abort(); detailRequest = null; detailKey = null;
    if (dialog.open) dialog.close();
    $('detail-payload').textContent = '';
    $('detail-meta').replaceChildren();
  }
  async function openDetail(key) {
    const entry = entries.get(key);
    if (!entry) return;
    detailRequest?.abort();
    const request = new AbortController(); detailRequest = request; detailKey = key;
    $('detail-key').textContent = key;
    const meta = $('detail-meta'); meta.replaceChildren();
    for (const [label, value] of [['node / enclave', identity.node + ' / ' + identity.enclave], ['size', entry.size + ' bytes'], ['local TTL', entry.ttl_seconds + ' seconds'], ['written', new Date(entry.written).toLocaleString()], ['expires locally', new Date(entry.expires).toLocaleString()], ['revision', entry.revision]]) {
      const dt = document.createElement('dt'), dd = document.createElement('dd');
      dt.textContent = label; dd.textContent = value; meta.append(dt, dd);
    }
    $('detail-payload').textContent = entry.text;
    $('detail-status').textContent = 'Stream preview · fetching the current full value…';
    if (!dialog.open) dialog.showModal();
    try {
      const response = await fetch(nodeURL('v1/data/' + encodeURIComponent(key)), { signal: request.signal, cache: 'no-store' });
      if (response.status === 404) throw new Error('This key is no longer available on the node.');
      if (!response.ok) throw new Error('Full-value read failed (HTTP ' + response.status + ').');
      const bytes = new Uint8Array(await response.arrayBuffer());
      if (request !== detailRequest) return;
      const decoded = decodePayload(bytes);
      $('detail-payload').textContent = decoded.text;
      $('detail-status').textContent = 'Current value fetched now · ' + bytes.length + ' bytes' + (decoded.binary ? ' · hexadecimal' : '');
    } catch (err) {
      if (request !== detailRequest || request.signal.aborted) return;
      $('detail-status').textContent = err.message + ' Showing the stream preview.';
    }
  }
  $('connect-form').addEventListener('submit', event => { event.preventDefault(); connect($('node').value); });
  $('node').addEventListener('input', () => $('node').setCustomValidity(''));
  $('query').addEventListener('input', () => { query = $('query').value; saveURL(); render(); });
  $('close-detail').addEventListener('click', closeDetail);
  dialog.addEventListener('close', () => { if (!dialog.open) closeDetail(); });
  dialog.addEventListener('click', event => { if (event.target === dialog) closeDetail(); });
  layout(); new ResizeObserver(layout).observe(viewport);
  setInterval(tick, 250);
  async function start() {
    const response = await fetch('./config.json', { cache: 'no-store' });
    if (!response.ok) throw new Error('Viewer configuration unavailable');
    const config = await response.json();
    servedNode = config.node ? new URL(config.node).origin : '';
    const params = new URLSearchParams(location.search);
    query = params.get('q') ?? config.q ?? ''; $('query').value = query;
    const node = params.get('node') || servedNode;
    if (node) connect(node); else render();
  }
  start().catch(() => setState('disconnected', 'Unable to load viewer configuration. Reload to try again.'));
})();
