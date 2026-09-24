// WebSocket RPC + event client with automatic reconnect.
let ws = null;
let nextId = 1;
const pending = new Map();
const handlers = new Map(); // event → Set<fn>
const openHooks = new Set();
let backoff = 400;
let stateCb = () => {};
let closedByUs = false;

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
  stateCb('connecting');
  ws = new WebSocket(url());
  ws.onopen = () => {
    backoff = 400;
    stateCb('open');
    openHooks.forEach((f) => { try { f(); } catch (e) { console.error(e); } });
  };
  ws.onmessage = (m) => {
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
  ws.onclose = () => {
    stateCb('closed');
    pending.forEach((p) => p.reject(new Error('connection lost')));
    pending.clear();
    if (!closedByUs) setTimeout(connect, backoff = Math.min(backoff * 1.6, 5000));
  };
  ws.onerror = () => {};
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
