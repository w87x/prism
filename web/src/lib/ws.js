// WebSocket RPC + event client with automatic reconnect.
let ws = null;
let nextId = 1;
const pending = new Map();
const handlers = new Map(); // event → Set<fn>
const openHooks = new Set();
let backoff = 400;
let attempt = 0; // reconnect attempts since the last successful open
let nextRetryAt = 0; // Date.now() timestamp of the next scheduled retry, while waiting to reconnect
let stateCb = () => {};
let closedByUs = false;
let retryTimer = 0, connectTimer = 0;
let downSince = 0; // when the connection was last lost (0 while open) — shown on the splash
const CONNECT_TIMEOUT = 6000; // a handshake that has not finished by now is stuck (half-open socket after a restart, a sleeping phone)

function url() {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  let token = '';
  try {
    const p = new URLSearchParams(location.search).get('token');
    if (p) {
      localStorage.setItem('prism.token', p);
      const u = new URL(location.href); // keep the secret out of the address bar, history and screenshots
      u.searchParams.delete('token');
      history.replaceState(null, '', u.pathname + u.search + u.hash);
    }
    token = localStorage.getItem('prism.token') || '';
  } catch {}
  return `${proto}://${location.host}/ws${token ? '?token=' + encodeURIComponent(token) : ''}`;
}

export function onConnState(fn) { stateCb = fn; }
export function onOpen(fn) { openHooks.add(fn); return () => openHooks.delete(fn); }

export function on(event, fn) {
  if (!handlers.has(event)) handlers.set(event, new Set());
  handlers.get(event).add(fn);
  return () => handlers.get(event)?.delete(fn);
}

export function connect() {
  closedByUs = false;
  clearTimeout(retryTimer); clearTimeout(connectTimer);
  if (!downSince) downSince = Date.now();
  stateCb('connecting', { attempt, nextRetryAt: 0, since: downSince });
  const sock = new WebSocket(url());
  ws = sock;
  // no answer in time: give up on this socket (its close triggers the next attempt) instead of "connecting…" forever
  connectTimer = setTimeout(() => { if (sock.readyState === 0) { try { sock.close(); } catch {} } }, CONNECT_TIMEOUT);
  sock.onopen = () => {
    clearTimeout(connectTimer);
    backoff = 400;
    attempt = 0;
    downSince = 0;
    stateCb('open', {});
    openHooks.forEach((f) => { try { f(); } catch (e) { console.error(e); } });
  };
  sock.onmessage = (m) => {
    let d;
    try { d = JSON.parse(m.data); } catch { return; }
    if (d.id) {
      const p = pending.get(d.id);
      if (p) {
        pending.delete(d.id);
        d.error ? p.reject(new Error(d.error)) : p.resolve(d.result);
      }
      return;
    }
    handlers.get(d.event)?.forEach((f) => { try { f(d.data); } catch (e) { console.error(d.event, e); } });
    handlers.get('*')?.forEach((f) => { try { f(d.event, d.data); } catch (e) { console.error(e); } });
  };
  sock.onclose = () => {
    if (sock !== ws) return; // a socket we already replaced
    clearTimeout(connectTimer);
    if (!downSince) downSince = Date.now();
    if (!closedByUs) {
      attempt++;
      backoff = Math.min(backoff * 1.6, 5000);
      nextRetryAt = Date.now() + backoff;
      clearTimeout(retryTimer);
      retryTimer = setTimeout(connect, backoff);
    }
    stateCb('closed', { attempt, nextRetryAt, since: downSince });
    pending.forEach((p) => p.reject(new Error('connection lost')));
    pending.clear();
  };
  sock.onerror = () => {};
}

// Coming back to the tab, or the network returning, is the moment to retry at once rather than wait out the backoff
// (a phone that slept, a laptop that changed networks): reconnect now unless a fresh attempt is already under way.
export function reconnectNow() {
  if (closedByUs) return;
  if (ws && ws.readyState === 1) return;
  if (ws && ws.readyState === 0) { try { ws.close(); } catch {} } // stale attempt
  backoff = 400;
  connect();
}
// Throw the current socket away and dial again at once — for a socket that looks open but is dead (the server
// process behind it exited without the close reaching us).
export function forceReconnect() {
  if (closedByUs) return;
  const old = ws;
  if (old) { try { old.close(); } catch {} }
  pending.forEach((p) => p.reject(new Error('connection lost')));
  pending.clear();
  backoff = 400;
  connect();
}
if (typeof window !== 'undefined') {
  window.addEventListener('online', reconnectNow);
  window.addEventListener('pageshow', reconnectNow);
  document.addEventListener('visibilitychange', () => { if (!document.hidden) reconnectNow(); });
}

// Pages restored from localStorage mount (and fire their first RPCs) before the socket is open:
// wait for the connection instead of failing.
function whenOpen(timeoutMs = 20000) {
  if (ws && ws.readyState === 1) return Promise.resolve();
  return new Promise((resolve, reject) => {
    const t = setTimeout(() => { openHooks.delete(h); reject(new Error('not connected')); }, timeoutMs);
    const h = () => { clearTimeout(t); openHooks.delete(h); resolve(); };
    openHooks.add(h);
  });
}

// A view-only tab (another tab is the master) may look but not change anything: the store installs a guard
// that decides per method. It is enforced here because the server does not know which methods write.
let guard = null;
export function setRpcGuard(fn) { guard = fn; }

export async function rpc(method, params = {}) {
  if (guard) { const why = guard(method); if (why) throw new Error(why); }
  await whenOpen();
  return new Promise((resolve, reject) => {
    if (!ws || ws.readyState !== 1) return reject(new Error('not connected'));
    const id = nextId++;
    pending.set(id, { resolve, reject });
    ws.send(JSON.stringify({ id, method, params }));
  });
}

/** URL of an agent artifact, carrying the access token when the server requires one. */
export function artifactUrl(id) {
  let t = '';
  try { t = localStorage.getItem('prism.token') || ''; } catch {}
  return `/artifacts/${id}${t ? '?token=' + encodeURIComponent(t) : ''}`;
}
