// Global reactive state. One WebSocket feeds this; pages read it and call `call()` for RPC.
import { untrack } from 'svelte';
import { FA } from './icons.js';
import { connect, on, onOpen, onConnState, rpc, setRpcGuard } from './ws.js';

const lsGet = (k, d) => { try { const v = localStorage.getItem(k); return v === null ? d : JSON.parse(v); } catch { return d; } };
const lsSet = (k, v) => { try { localStorage.setItem(k, JSON.stringify(v)); } catch {} };

export const S = $state({
  conn: 'connecting',
  status: null,
  page: lsGet('prism.page', 'today'),
  memoryBank: 0, // set before go('memory') to open the Memory page on that bank
  navOpen: lsGet('prism.navOpen', true),
  widgetOpen: lsGet('prism.widgetOpen', true),
  chat: [],
  runs: {}, // run id → live run
  askTrail: [], // finished ask_colleague requests, kept a few seconds so the graph can show the answer coming back
  chats: [], // the user's web chats (main first); see loadChats
  chatTopic: lsGet('prism.chatTopic', ''), // the chat shown on the Chat page ('' = main)
  chatBusy: {}, // topic -> an Atlas turn is running in that chat
  chatUnread: {}, // topic -> new messages seen while another chat was open
  chatRead: lsGet('prism.chatRead', {}), // topic -> id of the last message the user has seen
  asks: [], // pending questions/confirmations
  agents: [],
  models: null, // {models, lists} for pickers
  toasts: [],
  confirm: null,
  selectedAgent: null, // agent id shown in the graph inspector
  selectedTool: null, // tool name shown in the tools sidebar
  selectedTask: null, // task id to open in the Tasks page's detail modal (set before go('tasks'))
  reader: { open: false, id: null }, // the full-screen briefing reader
  briefRev: 0, // bumped when the reader changes a briefing, so other views reload
  narrow: false, // phone layout in use (set by App)
  autonomyTab: null, // tab the Autonomy page should open on (set before go('autonomy'))
  openBriefing: null, // briefing id to open for reading on arrival
  learnFile: null, // inbox file the Memory page should open in its "learn from document" dialog
  selectedFact: null, // memory fact id to open (set before go('memory'), e.g. from the search palette)
  selectedTrackerName: null, // tracker name to open (set before go('trackers'))
  selectedKbPage: null, // kb page id to open (set before go('kb'))
  wallOpen: false, // full-screen thinking wall — see ThinkingWall.svelte
  searchOpen: false, // universal search palette (Cmd+K) — see CommandPalette.svelte
  notifs: { items: [], unread: 0 },
  llm: { active: 0 }, // model calls in flight (any caller)
  peekRun: null, // run whose live view is open (held by reference, so it outlives its removal from `runs`)
  onboardingOpen: false,
  skipOnboarding: lsGet('prism.skipOnboarding', false),
  refresh: 0, // bumped when a page should reload its data
  activity: [], // agent activity feed (run/tool events) for the chat's activity mode
  activityMode: lsGet('prism.activity', 'off'), // off | agents | tools
  editor: { editor: true, taken: true }, // this tab may change things; other tabs are read-only (single-user app)
  proposals: 0, // pending evolution proposals (badge on the Agents page)
  review: null, // proposal open in the review dialog
  tools: null, // tool catalog cache
  skills: null,
});

export function setActivityMode(m) { S.activityMode = m; lsSet('prism.activity', m); }
function pushActivity(a) { S.activity.push({ ts: Date.now(), ...a }); if (S.activity.length > 300) S.activity.splice(0, S.activity.length - 300); }

export async function loadProposals() {
  const r = await call('agents.proposals', { status: 'pending' }, { quiet: true });
  if (r) S.proposals = r.length;
}
/** Open the review dialog for a proposal (by id) — used by notifications, the agent inspector and the Evolution tab. */
export async function openProposal(id) {
  const r = (await call('agents.proposals', { status: '' }, { quiet: true })) || [];
  const p = r.find((x) => x.id === id);
  if (p) S.review = p;
}
// Methods a view-only tab may still call: everything that only reads. Unknown methods are refused, so a
// newly added writing RPC is safe by default.
const READ_ONLY_OK = new Set(`agents.get agents.history agents.list agents.proposals artifacts.list autonomy.audit bookmarks.list briefings.list
browser.status chat.commands chat.history chats.list crons.list docs.search docs.sources downloads.list editor.claim elevenlabs.status foldermap.entries
foldermap.list fs.browse intents.list kb.page_get kb.tree lists.list logs.list mail.accounts mail.himalaya mcp.list memory.banks memory.entity_facts
memory.entity_graph memory.export memory.fact memory.facts memory.find memory.full_graph memory.graph memory.links memory.ops memory.review
memory.stats models.list notifications.list obsidian.status onboarding.state onboarding.templates ops.dashboard ops.delegation plugins.get
plugins.info plugins.list plugins.status processes.list processes.log providers.list roles.get runs.snapshot search.all settings.get setup.info
shortcuts.list skills.get skills.hubs skills.list tasks.get tasks.list tasks.tree telegram.status today.get tools.list trackers.get trackers.list
web.providers`.split(/\s+/));
setRpcGuard((method) => (S.editor.editor || READ_ONLY_OK.has(method) ? '' : 'This tab is view-only — another window is the master. Use the badge in the corner to make this one the master.'));
export function takeOver() { return rpc('editor.claim'); }
export function go(page) { S.page = page; lsSet('prism.page', page); }
export function toggleNav() { S.navOpen = !S.navOpen; lsSet('prism.navOpen', S.navOpen); }
export function toggleWidget() { S.widgetOpen = !S.widgetOpen; lsSet('prism.widgetOpen', S.widgetOpen); }
export function skipOnboarding() { S.skipOnboarding = true; lsSet('prism.skipOnboarding', true); }
export function reopenOnboarding() { S.skipOnboarding = false; lsSet('prism.skipOnboarding', false); S.onboardingOpen = true; }

// ── toasts / confirm ────────────────────────────────────────────────────────
let toastId = 1;
export function toast(text, tone = 'ok', ms = 4200) {
  const id = toastId++;
  S.toasts.push({ id, text: String(text), tone });
  if (ms) setTimeout(() => dismissToast(id), ms);
}
export function dismissToast(id) { S.toasts = S.toasts.filter((t) => t.id !== id); }

export function confirmBox({ title = 'Confirm', text = '', ok = 'OK', danger = false } = {}) {
  return new Promise((resolve) => { S.confirm = { title, text, ok, danger, resolve }; });
}

/** RPC with error toasts. Returns undefined on failure (unless quiet=false is overridden by throw:true). */
export async function call(method, params = {}, opts = {}) {
  try {
    return await rpc(method, params);
  } catch (e) {
    if (opts.throw) throw e;
    if (!opts.quiet) toast(`${method}: ${e.message}`, 'err', 7000);
    return undefined;
  }
}

/** Subscribe to a server event; use inside $effect so it is cleaned up. */
export function listen(event, fn) { return on(event, fn); }

// ── runs / thinking ─────────────────────────────────────────────────────────
const BUF = 1800;
function short(s, n = 70) { s = String(s || '').replace(/\s+/g, ' '); return s.length > n ? s.slice(0, n) + '…' : s; }

export function activeRuns() {
  return Object.values(S.runs).sort((a, b) => a.id - b.id);
}

function newRun(e) {
  return { id: e.run, agent: e.agent, depth: e.depth || 0, task: e.task || 0, parent_run: e.parent_run || 0, kind: e.kind || '', title: e.title || '', task_text: '',
    tokens_in: 0, tokens_out: 0, context: 0, window: 0, buf: '', started: Date.now(), phase: 'thinking', done: false, calls: 0, live: false, liveMs: LIVE_MS_DEFAULT, kb: e.kb || 0, compactions: 0,
    chat: e.is_chat ? (e.chat_topic || '') : S.runs[e.parent_run]?.chat }; // which chat a run belongs to (sub-agents inherit it)
}

// LED "live" pulse speed tracks how fast tokens are actually arriving: a smoothed (EMA) inter-arrival gap
// between run.delta events, scaled and clamped so a fast stream blinks quickly and a slow/trickling one
// blinks gently — never a strobe, never so slow it reads as dead.
const LIVE_MS_DEFAULT = 750;
const LIVE_MS_MIN = 260;
const LIVE_MS_MAX = 900;
const liveTimers = new Map();
function markLive(r) {
  const now = Date.now();
  if (r.lastDeltaAt) {
    const gap = now - r.lastDeltaAt;
    r.deltaGapMs = r.deltaGapMs ? r.deltaGapMs * 0.7 + gap * 0.3 : gap;
    r.liveMs = Math.max(LIVE_MS_MIN, Math.min(LIVE_MS_MAX, Math.round(r.deltaGapMs * 1.8)));
  }
  r.lastDeltaAt = now;
  r.live = true;
  clearTimeout(liveTimers.get(r.id));
  liveTimers.set(r.id, setTimeout(() => { r.live = false; liveTimers.delete(r.id); }, 650));
}

function wire() {
  on('notifications.changed', loadNotifs);
  on('foldermap.done', (m) => toast(m.status === 'done' ? `Folder map ready: ${m.root.split('/').pop() || m.root} (${m.entries} entries)` : `Folder map of ${m.root.split('/').pop() || m.root} failed: ${m.error}`, m.status === 'done' ? 'ok' : 'warn'));
  on('evolution.proposal', loadProposals);
  on('evolution.decided', loadProposals);
  on('editor', (e) => {
    S.editor = e;
    // the editor tab went away: a visible tab takes over instead of leaving everyone read-only
    if (!e.editor && !e.taken && !document.hidden) rpc('editor.claim').catch(() => {});
  });
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden && !S.editor.editor && !S.editor.taken) rpc('editor.claim').catch(() => {});
  });
  on('llm.busy', (e) => { S.llm.active = e.active || 0; });
  on('status', (st) => { S.status = st; });
  on('run.start', (e) => {
    S.runs[e.run] = newRun(e);
    // a "quick" run (see QuickAsk) is a separate one-off request, not this conversation continuing — it
    // must never make the chat page look busy or steerable.
    if (e.is_chat && (e.depth || 0) === 0 && e.kind !== 'quick') S.chatBusy[e.chat_topic || ''] = true;
    pushActivity({ kind: 'start', agent: e.agent, depth: e.depth || 0, run: e.run, runKind: e.kind || '', chat: S.runs[e.run].chat, text: (e.kind === 'ask' ? 'answering a colleague’s request' : e.title) || '' });
  });
  on('run.delta', (e) => {
    const r = S.runs[e.run];
    if (!r) return;
    if (e.kind === 'break') r.buf += '\n';
    else {
      r.buf = (r.buf + e.text).slice(-BUF);
      r.phase = e.kind === 'thinking' ? 'thinking' : 'acting'; // blue while reasoning, green while writing/acting
      markLive(r); // the LED swells while tokens keep arriving
    }
  });
  on('run.tool', (e) => {
    const r = S.runs[e.run];
    if (!r) return;
    if (e.phase === 'start') {
      r.calls++;
      r.phase = 'acting';
      markLive(r);
      r.buf = (r.buf + `\n▸ ${e.tool} ${short(e.args)}\n`).slice(-BUF);
      pushActivity({ kind: 'tool', agent: e.agent, depth: r.depth, run: e.run, runKind: r.kind, chat: r.chat, tool: e.tool, text: short(e.args, 120) });
    }
    else if (e.phase === 'end') r.phase = 'thinking';
    if (e.phase === 'denied') r.buf = (r.buf + `\n✗ ${e.tool} denied\n`).slice(-BUF);
    else if (e.phase === 'end' && e.ok === false) r.buf = (r.buf + `✗ ${e.tool} failed${e.error ? ': ' + e.error : ''}\n`).slice(-BUF);
  });
  on('run.usage', (e) => {
    const r = S.runs[e.run];
    if (r) Object.assign(r, { tokens_in: e.tokens_in, tokens_out: e.tokens_out, context: e.context, window: e.window });
  });
  on('run.compacted', (e) => { const r = S.runs[e.run]; if (r) { r.buf += '\n⟲ context compacted\n'; r.compactions = (r.compactions || 0) + 1; } });
  on('run.end', (e) => {
    const r = S.runs[e.run];
    if (r) { r.done = true; r.phase = e.status; }
    if (r?.kind === 'ask') { // remember who asked whom for the fading line on the graph
      const from = S.runs[r.parent_run]?.agent;
      if (from) {
        const t = { id: e.run, from, to: r.agent, ok: e.status === 'done' };
        S.askTrail.push(t);
        setTimeout(() => { S.askTrail = S.askTrail.filter((x) => x.id !== t.id); }, 5200);
      }
    }
    pushActivity({ kind: 'end', agent: e.agent, depth: e.depth || 0, run: e.run, runKind: r?.kind || '', chat: r?.chat, status: e.status, text: e.error || '' });
    if (e.is_chat && (e.depth || 0) === 0 && r?.kind !== 'quick') S.chatBusy[e.chat_topic || ''] = false;
    setTimeout(() => { delete S.runs[e.run]; }, 700);
    if (e.status === 'failed' && e.error) toast(`${e.agent}: ${e.error}`, 'err', 8000);
  });
  on('ask.request', (a) => { if (!S.asks.find((x) => x.id === a.id)) S.asks.push(a); });
  on('ask.done', (a) => { S.asks = S.asks.filter((x) => x.id !== a.id); });
  on('chat.message', (m) => {
    if (m.channel !== 'web' && m.channel) return;
    const topic = m.topic || '';
    if (topic === S.chatTopic) {
      if (!S.chat.find((x) => x.id === m.id)) S.chat.push(m);
      if (S.chat.length > 400) S.chat.splice(0, S.chat.length - 400);
      markRead(topic, m.id);
    } else if (m.role === 'agent') S.chatUnread[topic] = (S.chatUnread[topic] || 0) + 1; // something new in a chat that is not open
    const c = S.chats.find((x) => x.topic === topic);
    if (c) { c.last_at = m.created_at; c.last_msg_id = m.id; if (m.role === 'user' || m.role === 'agent') c.last_text = String(m.text || '').slice(0, 120); }
    else if (topic) loadChats(); // a chat this tab has not heard of yet
  });
  on('chat.cleared', (e) => { if (e.channel === 'web' && (e.topic || '') === S.chatTopic) S.chat = []; });
  on('chats.update', () => loadChats());
  on('chat.reload', async (e) => { if ((e.topic || '') === S.chatTopic) { const h = await call('chat.history', { limit: 120, topic: S.chatTopic }, { quiet: true }); if (h && S.chatTopic === (e.topic || '')) S.chat = h; } });
  on('notice', (n) => { if (n.level === 'attention' || n.level === 'warning' || n.level === 'error') toast(`${n.agent}: ${short(n.text, 140)}`, n.level === 'attention' ? 'attn' : n.level === 'warning' ? 'warn' : 'err', 9000); });
  on('log', (l) => { if (l.level === 'error') toast(`${l.source}: ${short(l.message, 160)}`, 'err', 8000); });
  on('agents.update', () => { loadAgents(); S.refresh++; });
  on('llm.usage', () => {});
  on('notification', (n) => {
    S.notifs.items.unshift(n);
    if (S.notifs.items.length > 50) S.notifs.items.pop();
    S.notifs.unread++;
    toast(`${n.title}${n.text ? ' — ' + short(n.text, 120) : ''}`, n.level === 'error' ? 'err' : n.level === 'attention' || n.level === 'warning' ? 'attn' : 'ok', n.level === 'info' ? 5000 : 10000);
  });
  on('toast', (e) => toast(e.text, e.level === 'err' ? 'err' : 'ok', 8000));
}

export async function loadAgents() {
  const a = await call('agents.list', {}, { quiet: true });
  if (a) S.agents = a;
}

export async function loadModels() {
  const [models, lists] = await Promise.all([call('models.list', {}, { quiet: true }), call('lists.list', {}, { quiet: true })]);
  if (models !== undefined && lists !== undefined) S.models = { models: models || [], lists: lists || [] };
}

// The cache reads are untracked: these are called from $effect, and reading S.tools before
// writing it would make every caller re-run itself forever.
export async function loadTools(force = false) {
  if (!force && untrack(() => S.tools)) return untrack(() => S.tools);
  const t = await call('tools.list', {}, { quiet: true });
  if (t) S.tools = t;
  return S.tools;
}
export async function loadSkills(force = false) {
  if (!force && untrack(() => S.skills)) return untrack(() => S.skills);
  const t = await call('skills.list', {}, { quiet: true });
  if (t) S.skills = t;
  return S.skills;
}

/** Model picker options: models and fallback lists. */
export function modelOptions(kind = 'chat') {
  if (!S.models) return [];
  const ms = S.models.models.filter((m) => m.kind === kind).map((m) => ({ value: m.name, label: m.name, hint: `${Math.round(m.context_window / 1024)}k` }));
  const ls = S.models.lists.filter((l) => l.kind === kind).map((l) => ({ value: l.name, label: `⛓ ${l.name}`, hint: `${l.models.length} models` }));
  return [...ls, ...ms];
}

export async function refreshAll() {
  const st = await call('app.state', {}, { quiet: true });
  if (st) S.status = st;
  if (!st || st.setup) return;
  await loadChats();
  const [hist, snap] = await Promise.all([call('chat.history', { limit: 120, topic: S.chatTopic }, { quiet: true }), call('runs.snapshot', {}, { quiet: true })]);
  if (hist) { S.chat = hist; if (hist.length) markRead(S.chatTopic, hist[hist.length - 1].id); }
  if (snap) {
    S.runs = {};
    for (const r of snap.runs || []) S.runs[r.run] = { ...newRun(r), task_text: r.task_text, tokens_in: r.tokens_in, tokens_out: r.tokens_out, context: r.context, window: r.window, started: r.started };
    S.asks = snap.asks || [];
    S.chatBusy = {};
    for (const r of snap.runs || []) if (r.is_chat && (r.depth || 0) === 0 && r.kind !== 'quick') S.chatBusy[r.chat_topic || ''] = true;
  }
  loadAgents();
  loadModels();
  loadNotifs();
  loadProposals();
}

// ── several web chats ──
export function markRead(topic, id) {
  S.chatUnread[topic] = 0;
  if (id && (S.chatRead[topic] || 0) < id) { S.chatRead[topic] = id; lsSet('prism.chatRead', $state.snapshot(S.chatRead)); }
}
export async function loadChats() {
  const r = await call('chats.list', {}, { quiet: true });
  if (!r) return;
  S.chats = r;
  for (const c of r) if (c.busy) S.chatBusy[c.topic] = true;
  if (!r.find((c) => c.topic === S.chatTopic)) S.chatTopic = ''; // the open chat was deleted
}
/** A chat other than the open one has something the user has not seen yet. */
export function chatHasNew(c) {
  if (c.topic === S.chatTopic) return false;
  if ((S.chatUnread[c.topic] || 0) > 0) return true;
  const seen = S.chatRead[c.topic];
  return seen !== undefined && c.last_msg_id > seen;
}
export async function selectChat(topic) {
  if (topic === S.chatTopic) return;
  S.chatTopic = topic;
  lsSet('prism.chatTopic', topic);
  S.chat = [];
  const hist = await call('chat.history', { limit: 120, topic }, { quiet: true });
  if (S.chatTopic !== topic) return; // the user moved on while it loaded
  S.chat = hist || [];
  markRead(topic, S.chat.length ? S.chat[S.chat.length - 1].id : 0);
}
export async function newChat(title = '', projectBankId = 0) {
  const c = await call('chats.create', { title, project_bank_id: projectBankId });
  if (c) { await loadChats(); await selectChat(c.topic); }
  return c;
}

export async function loadNotifs() {
  const r = await call('notifications.list', { limit: 50 }, { quiet: true });
  if (r) S.notifs = { items: r.items || [], unread: r.unread || 0 };
}

export async function answerAsk(id, answer) {
  const r = await call('ask.answer', { id, answer });
  if (r) S.asks = S.asks.filter((a) => a.id !== id);
}

export function init() {
  onConnState((s) => { S.conn = s; });
  onOpen(refreshAll);
  wire();
  connect();
}

// ── formatting helpers ──────────────────────────────────────────────────────
export const fmtTokens = (n) => (n >= 1e6 ? (n / 1e6).toFixed(1) + 'M' : n >= 1e4 ? Math.round(n / 1e3) + 'k' : n >= 1e3 ? (n / 1e3).toFixed(1) + 'k' : String(n || 0));
export function ago(ts) {
  if (!ts) return '—';
  const s = Math.max(0, (Date.now() - new Date(ts).getTime()) / 1000);
  if (s < 60) return Math.round(s) + 's';
  if (s < 3600) return Math.round(s / 60) + 'm';
  if (s < 86400) return Math.round(s / 3600) + 'h';
  return Math.round(s / 86400) + 'd';
}
export function until(ts) {
  if (!ts) return '—';
  const s = (new Date(ts).getTime() - Date.now()) / 1000;
  if (s <= 0) return 'now';
  if (s < 90) return Math.round(s) + 's';
  if (s < 5400) return Math.round(s / 60) + 'm';
  if (s < 172800) return Math.round(s / 3600) + 'h';
  return Math.round(s / 86400) + 'd';
}
export const clock = (ts) => (ts ? new Date(ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : '');
export const stamp = (ts) => (ts ? new Date(ts).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : '—');

/** Font Awesome glyph (a character of the "FA" font) for an agent; `robot` until one is assigned. */
export function iconOf(name) {
  if (name === 'Scribe') return FA['pen-nib']; // the knowledge-base writer is not a profile
  const a = S.agents.find((x) => x.name === name);
  return FA[a?.icon] || FA.robot;
}

// ── settings helpers ────────────────────────────────────────────────────────
export async function loadSetting(key, def) {
  const v = await call('settings.get', { key }, { quiet: true });
  return v && typeof v === 'object' ? { ...$state.snapshot(def), ...v } : structuredClone($state.snapshot(def));
}
export async function saveSetting(key, value, msg = 'Saved') {
  const r = await call('settings.set', { key, value: $state.snapshot(value) });
  if (r && msg) toast(msg);
  return !!r;
}

/** Last meaningful line of an agent's live output (for the "what is it doing" hint). */
export function lastLine(buf) {
  const ls = String(buf || '').split('\n').map((l) => l.trim()).filter(Boolean);
  return ls.length ? ls[ls.length - 1] : '';
}

/** Open whatever a `ref` from Today, the side rail or a notification points at (task:, briefing:, ingest:, hire:, proposal:, a page id). */
export function openRef(ref) {
  if (!ref) return;
  if (ref.startsWith('task:')) { S.selectedTask = Number(ref.slice(5)); go('tasks'); }
  else if (ref.startsWith('briefing:')) {
    // tablets and phones read briefings full-screen; a desktop window keeps the Autonomy tab and its dialog
    if (typeof innerWidth === 'number' && innerWidth <= 1180) { S.reader.id = Number(ref.slice(9)); S.reader.open = true; }
    else { S.autonomyTab = 'brief'; S.openBriefing = Number(ref.slice(9)); go('autonomy'); }
  }
  else if (ref.startsWith('ingest:')) { S.learnFile = ref.slice(7); go('memory'); }
  else if (ref.startsWith('hire:')) { go('agents'); S.selectedAgent = Number(ref.slice(5)); }
  else if (ref.startsWith('proposal:')) { go('agents'); openProposal(Number(ref.slice(9))); }
  else if (ref.startsWith('ask:')) go('chat');
  else go(ref);
}
