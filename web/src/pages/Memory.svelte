<script>
  import { untrack } from 'svelte';
  import { S, call, listen, toast, confirmBox, ago, stamp, go } from '../lib/store.svelte.js';
  import Panel from '../lib/ui/Panel.svelte';
  import Button from '../lib/ui/Button.svelte';
  import Input from '../lib/ui/Input.svelte';
  import Select from '../lib/ui/Select.svelte';
  import Checkbox from '../lib/ui/Checkbox.svelte';
  import Bar from '../lib/ui/Bar.svelte';
  import Badge from '../lib/ui/Badge.svelte';
  import Modal from '../lib/ui/Modal.svelte';
  import Field from '../lib/ui/Field.svelte';
  import Textarea from '../lib/ui/Textarea.svelte';
  import Tags from '../lib/ui/Tags.svelte';
  import NumberInput from '../lib/ui/NumberInput.svelte';
  import Empty from '../lib/ui/Empty.svelte';
  import Icon from '../lib/ui/Icon.svelte';
  import Led from '../lib/ui/Led.svelte';
  import Segmented from '../lib/ui/Segmented.svelte';
  import MemoryGraph from '../lib/MemoryGraph.svelte';
  import { nearEnd } from '../lib/nearend.js';
  import Provenance from '../lib/Provenance.svelte';

  let banks = $state([]);
  let facts = $state([]);
  let stats = $state(null);
  let bank = $state(untrack(() => { const b = S.memoryBank || 0; S.memoryBank = 0; return b; })); // 0 = all
  let q = $state('');
  let history = $state(false);
  let kind = $state(''); // '' | fact | conclusion
  let view = $state('list'); // list | graph
  let searching = $state(false);
  let busy = $state('');
  let edit = $state(null);
  let editOpen = $state(false);
  let addOpen = $state(false);
  let add = $state({ bank: 'user', text: '', tags: [] });
  let bankOpen = $state(false);
  let nb = $state({ kind: 'project', name: '', description: '' });
  let ops = $state([]); // merges and splits that can still be undone
  let importer = $state();
  let review = $state(null); // contradictions / stale conclusions / prune candidates, loaded on demand
  let reviewOpen = $state(false);
  async function loadReview() { review = await call('memory.review', {}, { quiet: true }); }
  $effect(() => { loadReview(); return listen('memory.update', loadReview); });
  const reviewCount = $derived(review ? review.contradictions.length + review.stale_conclusions.length + review.prune_candidates.length + review.open.length + review.unverified.length : 0);
  async function extractEntities() {
    busy = 'entities';
    const rs = await call('memory.entities_extract', { bank_id: bank });
    busy = '';
    if (!rs) return;
    const t = rs.reduce((a, r) => ({ e: a.e + r.entities, r: a.r + r.relations }), { e: 0, r: 0 });
    toast(t.e + t.r ? `${t.e} entities, ${t.r} relations` : (rs[0]?.skipped ? `Nothing to extract: ${rs[0].skipped}` : 'Nothing new to extract'));
  }
  async function resolveContradiction(c, keep) {
    const drop = keep === c.a.id ? c.b.id : c.a.id;
    await call('memory.fact_outdate', { id: drop });
    loadReview(); loadFacts();
  }
  async function dismissContradiction(c) { await call('memory.unlink', { a: c.a.id, b: c.b.id }); loadReview(); }
  async function pinFromReview(f) { if (await call('memory.fact_pin', { id: f.id, pinned: true })) loadReview(); }
  // ── research: send an agent to check and enrich something ──
  let rsOpen = $state(false);
  let rs = $state({ label: '', fact_id: 0, entity_id: 0, bank_id: 0, topic: '', note: '', agent: 'Atlas' });
  let rsBusy = $state(false);
  const rsAgents = $derived(S.agents.filter((a) => a.enabled !== false && a.role !== 'maint').map((a) => ({ value: a.name, label: a.name })));
  function research(t = {}) { rs = { label: t.label || '', fact_id: t.fact_id || 0, entity_id: t.entity_id || 0, bank_id: t.bank_id || 0, topic: '', note: '', agent: rs.agent || 'Atlas' }; editOpen = false; rsOpen = true; }
  async function startResearch() {
    rsBusy = true;
    const { label, ...req } = rs;
    const t = await call('memory.enrich', req);
    rsBusy = false;
    if (t) { rsOpen = false; toast(`${rs.agent} is on it — task #${t.id}`); go?.('tasks'); }
  }
  // ── learn from a document ──
  let learnOpen = $state(false);
  let files = $state([]);
  let jobs = $state([]);
  let pick = $state(''); // the inbox file chosen
  let det = $state(null); // { info, me } for it
  let lr = $state({ me: '', title: '', since: '' });
  let inspecting = $state(false);
  let starting = $state(false);
  let picker = $state();
  const b64 = (f) => new Promise((res, rej) => { const r = new FileReader(); r.onload = () => res(String(r.result).split(',')[1] || ''); r.onerror = () => rej(r.error); r.readAsDataURL(f); });
  async function loadLearn() { files = (await call('ingest.files', {}, { quiet: true })) || []; jobs = (await call('ingest.jobs', {}, { quiet: true })) || []; }
  $effect(() => listen('ingest.update', () => { if (learnOpen) loadLearn(); }));
  async function inspect(name) {
    pick = name; det = null; inspecting = true;
    const r = await call('ingest.inspect', { ref: name });
    inspecting = false;
    if (r) { det = r; lr = { me: r.me || '', title: r.info.title, since: '' }; }
  }
  async function uploadForLearn(e) {
    const f = e.target.files?.[0]; e.target.value = '';
    if (!f) return;
    if (f.size > 28 * 1024 * 1024) { toast('That file is larger than 28 MB — put it in the inbox folder instead', 'err'); return; }
    const name = await call('ingest.upload', { name: f.name, data: await b64(f) });
    if (name) { await loadLearn(); inspect(name); }
  }
  async function startLearn() {
    starting = true;
    const j = await call('ingest.start', { ref: pick, me: lr.me, title: lr.title, since: lr.since ? new Date(lr.since).toISOString() : null });
    starting = false;
    if (j) { toast(`Learning from ${j.name} in the background`); pick = ''; det = null; loadLearn(); }
  }
  async function removeFile(f) {
    if (!(await confirmBox({ title: 'Remove from inbox', text: `Remove “${f.name}” from the inbox?${f.facts ? ` The ${f.facts} facts learned from it stay in memory.` : ''}`, ok: 'Remove', danger: true }))) return;
    if (await call('ingest.delete', { name: f.name, forget: true })) { if (pick === f.name) { pick = ''; det = null; } loadLearn(); }
  }
  $effect(() => { if (S.learnFile) { const n = S.learnFile; S.learnFile = null; untrack(() => { learnOpen = true; loadLearn().then(() => inspect(n)); }); } });
  async function jobAction(j, m) { await call(m, { id: j.id }); loadLearn(); }
  // ── time view: what memory believed at a date, and what changed day by day ──
  let timeOpen = $state(false);
  let tv = $state({ mode: 'asof', at: new Date().toISOString().slice(0, 10), q: '', days: 30 });
  let tvFacts = $state([]);
  let tvDays = $state([]);
  let tvBusy = $state(false);
  async function loadTime() {
    tvBusy = true;
    if (tv.mode === 'asof') {
      const end = new Date(tv.at + 'T23:59:59');
      tvFacts = (await call('memory.at', { at: end.toISOString(), bank_id: bank, q: tv.q, limit: 300 }, { quiet: true })) || [];
    } else {
      tvDays = (await call('memory.timeline', { days: Number(tv.days) || 30, bank_id: bank }, { quiet: true })) || [];
    }
    tvBusy = false;
  }
  $effect(() => { if (timeOpen) { tv.mode; tv.at; tv.days; bank; untrack(loadTime); } });
  let healthOpen = $state(false);
  let activityOpen = $state(false);
  let act = $state({ status: { step: '', last_at: 0, next_at: 0 }, events: [] });
  async function loadActivity() { const r = await call('memory.status', {}, { quiet: true }); if (r) act = r; }
  $effect(() => { loadActivity(); const off1 = listen('memory.step', (p) => { act.status.step = p?.step || ''; if (!p?.step) loadActivity(); }); const off2 = listen('log', loadActivity); return () => { off1(); off2(); }; });
  const inSec = (t) => { const d = t - Math.floor(Date.now() / 1000); return d > 0 ? (d >= 60 ? Math.round(d / 60) + ' min' : d + ' s') : 'soon'; };
  let modelsOpen = $state(false);
  let models = $state([]);
  let nm = $state({ name: '', query: '', bank_id: 0 });
  let modelBusy = $state(0);
  async function loadModels() { models = (await call('memory.models', {}, { quiet: true })) || []; }
  $effect(() => listen('memory.update', () => { if (modelsOpen) loadModels(); }));
  async function addModel() {
    modelBusy = -1;
    const id = await call('memory.model_save', { id: 0, name: nm.name, query: nm.query, bank_id: nm.bank_id || null });
    modelBusy = 0;
    if (id) { nm = { name: '', query: '', bank_id: 0 }; loadModels(); }
  }
  async function refreshModel(m) { modelBusy = m.id; await call('memory.model_refresh', { id: m.id }); modelBusy = 0; loadModels(); }
  async function deleteModel(m) { if (await confirmBox({ title: 'Delete model', text: `Delete the model “${m.name}”?`, ok: 'Delete', danger: true })) { await call('memory.model_delete', { id: m.id }); loadModels(); } }
  let answers = $state({});
  async function confirmFromReview(f) { if (await call('memory.confirm', { id: f.id })) loadReview(); }
  async function resolveOpen(f, verdict) {
    if (await call('memory.resolve_insight', { id: f.id, verdict, answer: answers[f.id] || '' })) { delete answers[f.id]; loadReview(); loadFacts(); }
  }
  async function outdateFromReview(f) { await call('memory.fact_outdate', { id: f.id }); loadReview(); }

  async function loadBanks() {
    banks = (await call('memory.banks', {}, { quiet: true })) || [];
    stats = await call('memory.stats', {}, { quiet: true });
    ops = (await call('memory.ops', {}, { quiet: true })) || [];
  }
  const PAGE = 100;
  let hasMore = $state(false);
  // the list loads a page at a time and fetches the next one as you scroll; a refresh keeps as many rows as are showing
  async function loadMore() {
    if (!hasMore || searching) return;
    const r = (await call('memory.facts', { bank_id: bank, kind, history, limit: PAGE, offset: facts.length }, { quiet: true })) || [];
    const have = new Set(facts.map((f) => f.id));
    facts = [...facts, ...r.filter((f) => !have.has(f.id))];
    hasMore = r.length === PAGE;
  }
  async function loadFacts() {
    searching = !!q.trim();
    if (q.trim()) {
      const specs = bank ? [labelOf(bank)] : [];
      facts = ((await call('memory.find', { query: q.trim(), banks: specs, k: 30, history })) || []).filter((f) => !kind || f.kind === kind);
    } else {
      const want = Math.max(PAGE, facts.length);
      facts = (await call('memory.facts', { bank_id: bank, kind, history, limit: want })) || [];
      hasMore = facts.length >= want;
    }
  }
  function labelOf(id) { const b = banks.find((x) => x.id === id); return b ? (b.kind === 'user' ? 'user' : `${b.kind}:${b.name}`) : ''; }
  $effect(() => { bank; history; kind; untrack(() => { facts = []; loadFacts(); }); }); // typing in the search box must not fire a search per keystroke
  $effect(() => { loadBanks(); return listen('memory.update', () => { loadBanks(); loadFacts(); }); });
  // a search-palette link sets S.selectedFact before navigating here
  $effect(() => { if (S.selectedFact != null) { const id = S.selectedFact; S.selectedFact = null; untrack(() => openId(id)); } });

  const kinds = ['user', 'profile', 'project', 'domain'];
  const grouped = $derived(kinds.map((k) => ({ kind: k, items: banks.filter((b) => b.kind === k) })).filter((g) => g.items.length));
  const bankOpts = $derived(banks.map((b) => ({ value: b.kind === 'user' ? 'user' : `${b.kind}:${b.name}`, label: b.kind === 'user' ? 'user' : `${b.kind}:${b.name}` })));

  async function run(name, method, params = {}, msg) {
    busy = name;
    const r = await call(method, params);
    busy = '';
    if (r !== undefined) { toast(typeof msg === 'function' ? msg(r) : msg); loadBanks(); loadFacts(); }
  }
  async function saveFact() {
    const r = await call('memory.fact_update', { id: edit.id, text: edit.text, tags: edit.tags, rank: edit.rank });
    if (r) {
      editOpen = false;
      if (r.id !== edit.id) toast(`Corrected — the old wording is kept in history as #${edit.id}`);
      loadFacts();
    }
  }
  async function delFact(f, e) {
    e?.stopPropagation();
    if (await confirmBox({ title: 'Forget fact', text: f.text, ok: 'Forget', danger: true })) { await call('memory.fact_delete', { id: f.id }); loadFacts(); loadBanks(); }
  }
  async function togglePin(f, e) {
    e?.stopPropagation();
    if (await call('memory.fact_pin', { id: f.id, pinned: !f.pinned })) { f.pinned = !f.pinned; loadFacts(); }
  }
  async function markOutdated() {
    if (!edit || !(await confirmBox({ title: 'Mark outdated', text: 'Retire this fact with no replacement? It stays visible with "history" on, just excluded from normal recall.', ok: 'Mark outdated' }))) return;
    if (await call('memory.fact_outdate', { id: edit.id })) { editOpen = false; loadFacts(); }
  }
  function openFromTask(id, e) {
    e?.stopPropagation();
    S.selectedTask = id;
    go('tasks');
  }
  async function addFact() {
    const r = await call('memory.store', add);
    if (r) { toast(r.duplicate ? 'Already known — reinforced' : r.superseded?.length ? 'Stored; older fact retired' : 'Stored'); addOpen = false; add.text = ''; add.tags = []; loadFacts(); loadBanks(); }
  }
  async function createBank() {
    if (await call('memory.bank_save', nb)) { bankOpen = false; nb.name = ''; nb.description = ''; loadBanks(); }
  }
  async function delBank(b) {
    if (await confirmBox({ title: 'Delete bank', text: `Delete ${b.kind}:${b.name} with all ${b.facts} facts?`, ok: 'Delete', danger: true })) { await call('memory.bank_delete', { id: b.id }); if (bank === b.id) bank = 0; loadBanks(); }
  }
  async function undoOp() {
    if (!ops.length) return;
    const r = await call('memory.undo', { id: ops[0].id });
    if (r) { toast(`Undone: ${r}`); bank = 0; loadBanks(); loadFacts(); }
  }
  async function doExport() {
    const d = await call('memory.export', { bank_ids: bank ? [bank] : [] });
    if (!d) return;
    const url = URL.createObjectURL(new Blob([JSON.stringify(d, null, 1)], { type: 'application/json' }));
    const name = bank ? labelOf(bank).replace(/[^\w.-]+/g, '_') : 'all';
    Object.assign(document.createElement('a'), { href: url, download: `prism-memory-${name}-${new Date().toISOString().slice(0, 10)}.json` }).click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
    toast(`${d.facts.length} facts in ${d.banks.length} banks exported`);
  }
  async function doImport(e) {
    const f = e.target.files?.[0];
    e.target.value = '';
    if (!f) return;
    let dump;
    try { dump = JSON.parse(await f.text()); } catch { toast('That is not a JSON file', 'warn'); return; }
    if (!(await confirmBox({ title: 'Import memory', text: `Add the facts, banks and links of “${f.name}” to this memory?\nFacts that are already known are skipped.`, ok: 'Import' }))) return;
    const r = await call('memory.import', { dump });
    if (r) { toast(`${r.facts} facts imported (${r.skipped} already known), ${r.links} links`); loadBanks(); loadFacts(); }
  }
  async function reflect() {
    busy = 'reflect';
    const rs = await call('memory.reflect', { bank_id: bank });
    busy = '';
    if (!rs) return;
    const t = rs.reduce((a, r) => ({ n: a.n + r.added, s: a.s + r.strengthened, r: a.r + r.revised + r.retired }), { n: 0, s: 0, r: 0 });
    if (t.n + t.s + t.r) toast(`${t.n} new conclusions, ${t.s} strengthened, ${t.r} revised or retired`);
    else toast(rs[0]?.skipped ? `Nothing to reflect on: ${rs[0].skipped}` : 'Nothing new to conclude');
    loadBanks(); loadFacts();
  }

  async function analyze() {
    busy = 'analyze';
    const rs = await call('memory.analyze', { bank_id: bank });
    busy = '';
    if (!rs) return;
    const t = rs.reduce((a, r) => ({ i: a.i + r.insights, s: a.s + r.strengthened + r.revised, c: a.c + r.contradictions, d: a.d + r.duplicates }), { i: 0, s: 0, c: 0, d: 0 });
    if (t.i + t.s + t.c + t.d) toast(`${t.i} new insights, ${t.s} updated, ${t.c} contradictions flagged, ${t.d} duplicates retired`);
    else toast(rs[0]?.skipped ? `Nothing to analyse: ${rs[0].skipped}` : 'Nothing new found');
    loadBanks(); loadFacts(); loadHealth();
  }
  let health = $state([]);
  async function loadHealth() { health = (await call('memory.health', {})) || []; }
  const curHealth = $derived(health.find((h) => h.id === bank));
  $effect(() => { bank; loadHealth(); });

  // ── links: the facts a fact is tied to; click one to walk to it ──
  let links = $state([]);
  let linkQ = $state('');
  let linkHits = $state([]);
  let linkKind = $state('related');
  async function loadLinks(id) { links = (await call('memory.links', { id }, { quiet: true })) || []; }
  async function openId(id) { const f = await call('memory.fact', { id }); if (f) openFact(f); }
  function openFact(f) { edit = structuredClone($state.snapshot(f)); editOpen = true; linkQ = ''; linkHits = []; loadLinks(f.id); }
  async function findLink() {
    if (!linkQ.trim()) { linkHits = []; return; }
    linkHits = ((await call('memory.find', { query: linkQ.trim(), k: 8 }, { quiet: true })) || []).filter((x) => x.id !== edit.id && !links.some((l) => l.id === x.id));
    if (!linkHits.length) toast('No other matching facts', 'info');
  }
  async function addLink(o) { if (await call('memory.link', { a: edit.id, b: o.id, kind: linkKind })) { linkHits = []; linkQ = ''; loadLinks(edit.id); loadFacts(); } }
  async function unlink(l) { if (await call('memory.unlink', { a: edit.id, b: l.id })) { loadLinks(edit.id); loadFacts(); } }
  const linkTone = { evidence: 'accent', supports: 'ok', contradicts: 'err', related: 'mute' };

  // ── merge / split (project and domain banks) ──
  const cur = $derived(banks.find((x) => x.id === bank));
  const movable = $derived(cur && (cur.kind === 'project' || cur.kind === 'domain'));
  let mergeOpen = $state(false);
  let mg = $state({ sel: {}, into: 0, name: '' });
  let merging = $state(false);
  const mergeCands = $derived(banks.filter((b) => cur && b.kind === cur.kind));
  const mergeSources = $derived(mergeCands.filter((b) => mg.sel[b.id] && b.id !== mg.into));
  const mergeTargets = $derived([{ value: 0, label: '— a new bank —' }, ...mergeCands.map((b) => ({ value: b.id, label: `${b.name} (${b.facts})` }))]);
  const mergeMoves = $derived(mergeSources.reduce((a, b) => a + b.facts, 0));
  const mergeTarget = $derived(mergeCands.find((b) => b.id === mg.into));
  function openMerge() { mg = { sel: { [bank]: true }, into: 0, name: '' }; mergeOpen = true; }
  async function doMerge() {
    const names = mergeSources.map((b) => b.name).join(', ');
    merging = true;
    toast(`Merging ${mergeSources.length} bank${mergeSources.length === 1 ? '' : 's'} (${names})…`);
    const r = await call('memory.bank_merge', { sources: mergeSources.map((b) => b.id), into: mg.into || 0, name: mg.name });
    merging = false;
    if (r) { toast(`${r.moved} facts moved into ${r.bank.name}${r.dropped ? `, ${r.dropped} duplicates collapsed` : ''} — you can undo it from the bank panel (↶ Undo merge)`); mergeOpen = false; bank = r.bank.id; loadBanks(); loadFacts(); }
  }
  let splitOpen = $state(false);
  let sp = $state({ name: '', description: '', ids: [], groups: [], facts: [], busy: false });
  async function openSplit() {
    const fs = (await call('memory.facts', { bank_id: bank, kind: 'fact', limit: 500 })) || [];
    sp = { name: '', description: '', ids: [], groups: [], facts: fs, busy: false };
    splitOpen = true;
  }
  async function suggestSplit() {
    sp.busy = true;
    const gs = await call('memory.bank_suggest_split', { id: bank });
    sp.busy = false;
    sp.groups = gs || [];
    if (gs && !gs.length) toast('Looks like one topic — nothing to split off', 'info');
  }
  function pickGroup(g) { sp.name = g.name; sp.description = g.description; sp.ids = [...g.fact_ids]; }
  function toggleFact(id, on) { sp.ids = on ? [...sp.ids, id] : sp.ids.filter((x) => x !== id); }
  async function doSplit() {
    const r = await call('memory.bank_split', { id: bank, name: sp.name, description: sp.description, fact_ids: sp.ids });
    if (r) { toast(`${r.moved} facts moved into ${r.bank.name}`); splitOpen = false; loadBanks(); loadFacts(); }
  }
  // rank colour: red lowest · orange low · yellow middling, or high but not trusted · green high and trusted · blue highest · violet set by hand
  const rankColor = (f) => f.tags?.includes('user-rank') ? '#b07cff' : f.rank >= 3 ? 'var(--accent)'
    : f.rank >= 1.5 ? (f.confidence >= 0.7 ? 'var(--ok)' : '#e6d84a')
    : f.rank >= 0.8 ? '#e6d84a' : f.rank >= 0.4 ? 'var(--attn)' : 'var(--err)';
  const rankName = (f) => f.tags?.includes('user-rank') ? 'set by you' : f.rank >= 3 ? 'highest' : f.rank >= 1.5 ? (f.confidence >= 0.7 ? 'high, trusted' : 'high, not fully trusted') : f.rank >= 0.8 ? 'middling' : f.rank >= 0.4 ? 'low' : 'lowest';
</script>

<div class="pg">
  <div class="bar">
    <div class="srch"><Input bind:value={q} size="sm" placeholder="search memory (semantic)…" onenter={loadFacts} /></div>
    <Button size="sm" onclick={loadFacts}>Search</Button>
    {#if q}<Button size="sm" variant="ghost" onclick={() => { q = ''; loadFacts(); }}>Clear</Button>{/if}
    <Checkbox bind:checked={history} label="history" onchange={loadFacts} />
    <Segmented size="sm" bind:value={view} options={[{ value: 'list', label: 'list' }, { value: 'graph', label: 'graph' }]} />
    <Segmented size="sm" bind:value={kind} options={[{ value: '', label: 'all' }, { value: 'fact', label: 'facts' }, { value: 'conclusion', label: 'conclusions' }]} />
    <span class="grow"></span>
    <Button size="sm" variant="ghost" title="What memory believed on a given date, and what changed day by day" onclick={() => { timeOpen = true; }}>Time</Button>
    <Button size="sm" variant="ghost" title="How each memory job is doing and how healthy each bank is" onclick={() => { healthOpen = true; loadActivity(); loadHealth(); }}>Health</Button>
    <button type="button" class="act" title="What memory maintenance is doing — click for the activity log" onclick={() => { activityOpen = true; loadActivity(); }}><Led state={act.status.step ? 'ok' : 'off'} size={7} /> {act.status.step ? act.status.step + '…' : act.status.last_at ? 'idle · last pass ' + ago(new Date(act.status.last_at * 1000).toISOString()) : 'idle'}</button>
    {#if stats}<span class="sm dim nowrap"><Led state={stats.embedding ? 'ok' : 'off'} size={7} /> embeddings {stats.embedding ? 'on' : 'off'} · {stats.facts} facts · {stats.raw} raw</span>{/if}
    <input bind:this={importer} type="file" accept="application/json,.json" hidden onchange={doImport} />
    <Button size="sm" variant={reviewCount ? 'accent' : 'ghost'} title="Contradictions, stale conclusions and facts about to be auto-archived" onclick={() => (reviewOpen = true)}><Icon name="warn" size={11} /> Review{#if reviewCount} · {reviewCount}{/if}</Button>
    <Button size="sm" variant="ghost" title="Send an agent to check something on the web and add what it finds" onclick={() => research()}>Research…</Button>
    <Button size="sm" variant="ghost" title="Teach memory from a document: a chat export (Telegram, WhatsApp), notes, an article, a PDF…" onclick={() => { learnOpen = true; loadLearn(); }}>Learn from document</Button>
    <Button size="sm" variant="ghost" title="Standing questions whose answers memory keeps up to date; agents read them first" onclick={() => { modelsOpen = true; loadModels(); }}>Models</Button>
    <Button size="sm" variant="ghost" title="Download {bank ? 'this bank' : 'all memory'} as JSON" onclick={doExport}><Icon name="dl" size={11} /> Export</Button>
    <Button size="sm" variant="ghost" title="Add a memory export (JSON) to this memory" onclick={() => importer.click()}>Import</Button>
    <Button size="sm" onclick={() => (addOpen = true)}><Icon name="plus" size={11} /> Fact</Button>
  </div>
  <div class="body">
    <Panel title="Banks" flush>
      <div class="banks scroll">
        <button class="bk" class:on={bank === 0} onclick={() => (bank = 0)}><span>all banks</span><span class="n">{banks.reduce((a, b) => a + b.facts, 0)}</span></button>
        {#each grouped as g}
          <div class="gk">{g.kind}</div>
          {#each g.items as b (b.id)}
            <button class="bk" class:on={bank === b.id} onclick={() => (bank = b.id)} title={b.description}>
              <span class="ellipsis">{b.kind === 'user' ? 'user' : b.name}</span><span class="n">{b.facts}</span>
            </button>
          {/each}
        {/each}
      </div>
      <div class="bfoot">
        <Button size="sm" block onclick={() => (bankOpen = true)}><Icon name="plus" size={11} /> Bank</Button>
        {#if ops.length}<Button size="sm" variant="ghost" block title="{ops[0].summary}" onclick={undoOp}>↶ Undo {ops[0].kind}</Button>{/if}
        {#if curHealth}<div class="sm mute" title="Reflection and analysis only use trusted facts (confidence 50%+). Facts learned from the web stay unverified until a second site confirms them.">{curHealth.usable} trusted{curHealth.unverified ? ` · ${curHealth.unverified} unverified` : ''} · {curHealth.conclusions} conclusions · {curHealth.insights} insights · {curHealth.fresh_reflect} new since reflect · {curHealth.fresh_analyze} since analysis</div>{#if curHealth.card}<div class="sm pre" title="profile card kept by deep analysis">{curHealth.card}</div>{/if}{/if}
        {#if cur}<Button size="sm" block title="Have an agent check what this bank holds on the web and add what it finds" onclick={() => research({ bank_id: cur.id, label: labelOf(cur.id) })}>Research this bank</Button>{/if}
        {#if movable}<div class="two"><Button size="sm" block title="Move every fact of this bank into another one" onclick={openMerge}><Icon name="merge" size={11} /> Merge…</Button><Button size="sm" block title="Move some facts into a new bank" onclick={openSplit}><Icon name="split" size={11} /> Split</Button></div>{/if}
        {#if cur && cur.kind !== 'user'}<Button size="sm" variant="danger" block onclick={() => delBank(cur)}>Delete bank</Button>{/if}
      </div>
    </Panel>
    <Panel title={searching ? `Results for “${q}”` : bank ? labelOf(bank) : 'All facts'} flush grow>
      {#snippet right()}
        <Button size="sm" variant="ghost" loading={busy === 'reflect'} title="Draw conclusions from facts that agree with each other{bank ? ' in this bank' : ''}" onclick={reflect}>Reflect</Button>
        <Button size="sm" variant="ghost" loading={busy === 'analyze'} title="Deep analysis: patterns, hypotheses, trends, contradictions, duplicates and a profile card{bank ? ' for this bank' : ''}" onclick={analyze}>Analyze</Button>
        <Button size="sm" variant="ghost" loading={busy === 'entities'} title="Pull named entities (people, products, places…) and their relations out of facts{bank ? ' in this bank' : ''}" onclick={extractEntities}>Extract entities</Button>
        <Button size="sm" variant="ghost" loading={busy === 'process'} onclick={() => run('process', 'memory.process', {}, (n) => `${n} facts distilled from raw`)}>Digest raw</Button>
        <Button size="sm" variant="ghost" loading={busy === 'reindex'} onclick={() => run('reindex', 'memory.reindex', {}, (n) => `${n} facts re-embedded`)}>Re-embed</Button>
        <Button size="sm" variant="ghost" loading={busy === 'prune'} onclick={() => run('prune', 'memory.prune', {}, (r) => `archived ${r.archived}, purged ${r.purged}`)}>Prune</Button>
      {/snippet}
      {#if view === 'graph'}
        <div class="gbody"><MemoryGraph {bank} {history} onopen={openId} onresearch={research} /></div>
      {:else}
      <div class="scroll" use:nearEnd={loadMore}>
        <table class="t">
          <thead><tr><th style="width:52px">Rank</th><th>Fact</th>{#if !bank || searching}<th>Bank</th>{/if}<th>Tags</th><th style="width:34px" title="linked facts">Links</th><th style="width:34px">Hits</th><th style="width:44px">Age</th><th style="width:52px"></th></tr></thead>
          <tbody>
            {#each facts as f (f.id)}
              <tr class="click" onclick={() => openFact(f)} class:old={f.valid_to} class:concl={f.kind === 'conclusion'}>
                <td data-sort={f.rank}><Bar value={Math.min(f.rank, 3)} max={3} color={rankColor(f)} height={4} label="rank {f.rank.toFixed(2)} — {rankName(f)}" />{#if f.score}<div class="sm mute">{f.score.toFixed(2)}</div>{/if}</td>
                <td class="pre">{#if f.pinned}<Icon name="pin" size={10} /> {/if}{f.text}{#if f.valid_to}<Badge tone="mute" title="superseded {stamp(f.valid_to)}">retired</Badge>{/if}{#if f.kind === 'conclusion'}<Badge tone="accent" title="a conclusion drawn from {f.proof} facts ({(f.confidence * 100).toFixed(0)}% sure)">{f.tags?.find((x) => ['pattern','deduction','hypothesis','trend','preference','risk','question'].includes(x)) || 'conclusion'} · {f.proof}</Badge>{#if f.stale}<Badge tone="warn" title="some of its evidence was retired; the next reflection revises it">review</Badge>{/if}{:else if f.confidence < 0.5}<Badge tone="attn" title="learned from untrusted content{f.origins?.length ? ' (' + f.origins.join(', ') + ')' : ''}">unverified</Badge>{:else if f.origins?.length > 1}<Badge tone="ok" title="the same fact was found on {f.origins.join(', ')}">{f.origins.length} sites</Badge>{/if}{#if f.via}<Badge tone="mute" title="not matched by the query itself: reached through a link from #{f.via}">via #{f.via}</Badge>{/if}</td>
                {#if !bank || searching}<td class="dim nowrap">{f.bank}</td>{/if}
                <td class="mute sm">{(f.tags || []).join(', ')}</td>
                <td class="mute sm">{f.links || ''}</td>
                <td class="mute">{f.hits}</td>
                <td class="mute sm">{ago(f.created_at)}</td>
                <td class="nowrap">
                  {#if f.kind !== 'conclusion'}<Button size="sm" variant={f.pinned ? 'accent' : 'ghost'} title={f.pinned ? 'unpin' : 'pin: never auto-archived or decayed'} onclick={(e) => togglePin(f, e)}><Icon name="pin" size={11} /></Button>{/if}
                  <Button size="sm" variant="ghost" title="forget" onclick={(e) => delFact(f, e)}><Icon name="trash" size={11} /></Button>
                </td>
              </tr>
            {:else}
              <tr><td colspan="8"><Empty>{searching ? 'nothing relevant found' : 'no facts yet — talk to Atlas, memory fills itself'}</Empty></td></tr>
            {/each}
          </tbody>
        </table>
      </div>
      {/if}
    </Panel>
  </div>
</div>

<Modal bind:open={editOpen} title="{edit?.kind === 'conclusion' ? 'Conclusion' : 'Fact'} #{edit?.id} · {edit?.bank}" width={720}>
  {#if edit}
    <Field label="Text" hint={edit.kind === 'conclusion' ? '' : 'changing this preserves the old wording in history and flags any conclusion built on it for review'}><Textarea bind:value={edit.text} rows={4} mono={false} /></Field>
    <Provenance id={edit.id} onopen={openId} />
    <div class="row wrap gap-12">
      <Field label="Tags"><Tags bind:value={edit.tags} /></Field>
      <Field label="Rank" hint="usage-weighted; decays when unused (unless pinned)"><NumberInput bind:value={edit.rank} min={0.05} max={5} step={0.25} /></Field>
      {#if edit.kind !== 'conclusion'}<div class="pinfield"><Checkbox checked={edit.pinned} onchange={async (v) => { if (await call('memory.fact_pin', { id: edit.id, pinned: v })) edit.pinned = v; }} label="pinned — never auto-archived or decayed" /></div>{/if}
    </div>
    <div class="sm mute">
      source {edit.source || '—'} · confidence {(edit.confidence * 100).toFixed(0)}% · hits {edit.hits} · created {stamp(edit.created_at)}
      {#if edit.task_id} · <button type="button" class="ltx inline" onclick={(e) => openFromTask(edit.task_id, e)}>from task #{edit.task_id}</button>{/if}
      {#if edit.supersedes} · supersedes #{edit.supersedes}{/if}
      {#if edit.origins?.length} · learned from {edit.origins.join(', ')}{/if}{#if edit.superseded_by} · superseded by #{edit.superseded_by}{/if}
      {#if !edit.embedded} · <span class="attn">not embedded</span>{/if}
    </div>
    <div class="lk">
      <h4>{edit.kind === 'conclusion' ? 'Evidence and links' : 'Linked facts'}</h4>
      {#each links as l (l.id)}
        <div class="lrow" class:old={l.valid_to}>
          <Badge tone={linkTone[l.link_kind] || 'mute'} w={11}>{l.link_kind}</Badge>
          <button type="button" class="ltx" title="open this fact" onclick={() => openFact(l)}>{l.text}</button>
          <span class="sm mute nowrap">{l.bank}{l.valid_to ? ' · retired' : ''}</span>
          <Bar value={l.link_weight} max={1} height={3} label="link strength {l.link_weight.toFixed(2)}" />
          <Button size="sm" variant="ghost" title="remove the link" onclick={() => unlink(l)}><Icon name="x" size={10} /></Button>
        </div>
      {:else}<div class="sm mute">not linked to anything yet — related facts are linked automatically when they are stored</div>{/each}
      <div class="row">
        <div class="grow"><Input size="sm" bind:value={linkQ} placeholder="link another fact: search…" onenter={findLink} /></div>
        <div class="kw"><Select size="sm" bind:value={linkKind} options={['related', 'supports', 'contradicts']} /></div>
        <Button size="sm" onclick={findLink}>Find</Button>
      </div>
      {#each linkHits as h (h.id)}
        <div class="lrow"><button type="button" class="ltx" onclick={() => addLink(h)} title="link this fact">{h.text}</button><span class="sm mute nowrap">{h.bank}</span><Button size="sm" onclick={() => addLink(h)}><Icon name="link" size={10} /></Button></div>
      {/each}
    </div>
  {/if}
  {#snippet footer()}
    {#if edit && edit.kind !== 'conclusion' && !edit.valid_to}<Button variant="ghost" title="retire with no replacement" onclick={markOutdated}>Mark outdated</Button>{/if}
    <span class="grow"></span>
    {#if edit && edit.kind !== 'conclusion' && !edit.valid_to}<Button variant="ghost" title="Have an agent check this on the web and add what it finds" onclick={() => research({ fact_id: edit.id, label: edit.text })}>Research</Button>{/if}
    <Button variant="ghost" onclick={() => (editOpen = false)}>Cancel</Button>
    <Button variant="primary" onclick={saveFact}>Save</Button>
  {/snippet}
</Modal>

<Modal bind:open={addOpen} title="Add fact" width={560}>
  <Field label="Bank" hint="pick one or type project:<name> / domain:<name>"><Select bind:value={add.bank} options={bankOpts} searchable /></Field>
  <Field label="Fact" hint="one self-contained sentence"><Textarea bind:value={add.text} rows={3} mono={false} /></Field>
  <Field label="Tags"><Tags bind:value={add.tags} /></Field>
  {#snippet footer()}<Button variant="ghost" onclick={() => (addOpen = false)}>Cancel</Button><Button variant="primary" disabled={!add.text.trim()} onclick={addFact}>Store</Button>{/snippet}
</Modal>

<Modal bind:open={bankOpen} title="New memory bank" width={480}>
  <Field label="Kind"><Select bind:value={nb.kind} options={['project', 'domain']} /></Field>
  <Field label="Name"><Input bind:value={nb.name} placeholder={nb.kind === 'project' ? 'Price check November' : 'Cooking'} /></Field>
  <Field label="Description"><Input bind:value={nb.description} /></Field>
  {#snippet footer()}<Button variant="ghost" onclick={() => (bankOpen = false)}>Cancel</Button><Button variant="primary" disabled={!nb.name.trim()} onclick={createBank}>Create</Button>{/snippet}
</Modal>

<Modal bind:open={mergeOpen} title="Merge {cur?.kind} banks" width={560}>
  <div class="sm mute">Tick every bank that is really the same topic. Their facts move into the target; identical facts collapse into one, and each fact keeps its history, links and rank. Only the emptied banks are removed.</div>
  <div class="mgl">
    {#each mergeCands as b (b.id)}
      <label class="mgrow"><Checkbox checked={!!mg.sel[b.id]} onchange={(v) => (mg.sel[b.id] = v)} /> <span class="grow">{b.name}</span><span class="sm mute">{b.facts} facts</span>{#if b.id === mg.into}<Badge tone="accent">target</Badge>{/if}</label>
    {/each}
  </div>
  <Field label="Merge into" hint="one of the banks above (its facts stay), another one, or a new bank"><Select bind:value={mg.into} options={mergeTargets} searchable /></Field>
  {#if !mg.into}<Field label="New bank name"><Input bind:value={mg.name} /></Field>{/if}
  <div class="mghint">
    {#if mergeSources.length}
      <b>{mergeMoves}</b> fact{mergeMoves === 1 ? '' : 's'} from {mergeSources.map((b) => b.name).join(', ')} will move into <b>{mergeTarget ? mergeTarget.name + ' (' + mergeTarget.facts + ' facts)' : mg.name.trim() || 'the new bank'}</b>. It runs in a moment and can be undone right after (↶ Undo merge in the bank panel).
    {:else}<span class="mute">tick at least one bank to move</span>{/if}
  </div>
  {#snippet footer()}<Button variant="ghost" onclick={() => (mergeOpen = false)}>Cancel</Button><Button variant="primary" loading={merging} disabled={!mergeSources.length || (!mg.into && !mg.name.trim())} onclick={doMerge}>Merge {mergeSources.length || ''} bank{mergeSources.length === 1 ? '' : 's'}</Button>{/snippet}
</Modal>

<Modal bind:open={splitOpen} title="Split {cur?.name}" width={680}>
  <div class="sm mute">Tick the facts that belong in a new {cur?.kind} bank. They keep their history and links. Or let the model propose sub-topics.</div>
  <div class="row"><Button size="sm" loading={sp.busy} onclick={suggestSplit}>Suggest sub-topics</Button>
    {#each sp.groups as g}<Button size="sm" variant="accent" title={g.description} onclick={() => pickGroup(g)}>{g.name} · {g.fact_ids.length}</Button>{/each}</div>
  <div class="two"><Field label="New bank name"><Input bind:value={sp.name} /></Field><Field label="Description"><Input bind:value={sp.description} /></Field></div>
  <div class="splist scroll">
    {#each sp.facts as f (f.id)}<div class="sprow"><Checkbox checked={sp.ids.includes(f.id)} onchange={(v) => toggleFact(f.id, v)} label={f.text} /></div>{/each}
  </div>
  {#snippet footer()}<span class="sm mute grow">{sp.ids.length} selected</span><Button variant="ghost" onclick={() => (splitOpen = false)}>Cancel</Button><Button variant="primary" disabled={!sp.name.trim() || !sp.ids.length} onclick={doSplit}>Move to new bank</Button>{/snippet}
</Modal>

<Modal bind:open={rsOpen} title="Research" width={600}>
  <div class="sm mute">An agent looks into it: reads what memory already holds, searches the web, and stores each new verified detail as a fact with its source (a second independent site raises a fact's trust). You follow it in Tasks and get a short report.</div>
  {#if rs.label}<div class="pre">{rs.label}</div>
  {:else}<Field label="What should it look into?"><Textarea bind:value={rs.topic} rows={2} mono={false} placeholder="e.g. current state of the AMD AI 395 release and its price" /></Field>{/if}
  <Field label="Anything to focus on? (optional)"><Input bind:value={rs.note} placeholder="prices in euro, official sources only…" /></Field>
  <Field label="Agent"><Select bind:value={rs.agent} options={rsAgents.length ? rsAgents : [{ value: 'Atlas', label: 'Atlas' }]} /></Field>
  {#snippet footer()}<Button variant="ghost" onclick={() => (rsOpen = false)}>Cancel</Button><Button variant="accent" loading={rsBusy} disabled={!rs.label && !rs.topic.trim()} onclick={startResearch}>Start research</Button>{/snippet}
</Modal>

<Modal bind:open={learnOpen} title="Learn from a document" width={700}>
  <div class="sm mute">PRISM works out what the file is — a Telegram or WhatsApp export, a chat log, notes, an article, a PDF — reads it the way that kind needs and files what is worth remembering into memory as facts (about you, and about the people and projects in it). It runs in the background; the facts are marked as coming from the document.</div>
  <h4>Choose a file</h4>
  <div class="row"><input bind:this={picker} type="file" hidden onchange={uploadForLearn} /><Button size="sm" onclick={() => picker.click()}>Upload a file…</Button><span class="sm mute">or pick one already in the inbox</span></div>
  {#snippet fileRow(f)}
    <div class="frow2">
      <button type="button" class="ltx" class:on={pick === f.name} onclick={() => inspect(f.name)}>{f.name} <span class="mute sm">{(f.size / 1024).toFixed(0)} KB · {ago(f.modified)}</span></button>
      {#if f.status === 'needs_you'}<Badge tone="attn" title="a chat: tell PRISM which participant you are">needs you</Badge>{:else if f.status === 'unreadable'}<Badge tone="err" title={f.note}>can't read</Badge>{:else if f.status === 'learning'}<Badge tone="accent">learning</Badge>{:else if f.status === 'partial'}<Badge tone="warn" title="a job stopped before the end — start it again or resume it below">partly learned</Badge>{:else if f.status === 'learned'}<Badge tone="ok">{f.facts} facts</Badge>{/if}
      <Button size="sm" variant="ghost" title="Remove this file from the inbox (facts already learned stay in memory)" onclick={() => removeFile(f)}>Delete</Button>
    </div>
  {/snippet}
  {@const fresh = files.filter((f) => f.status !== 'learned')}
  {@const learned = files.filter((f) => f.status === 'learned')}
  <div class="sm mute">Not learned yet <span class="mute">{fresh.length}</span></div>
  {#each fresh as f (f.name)}{@render fileRow(f)}{:else}<div class="sm mute">nothing waiting</div>{/each}
  {#if learned.length}
    <div class="sm mute">Already learned <span class="mute">{learned.length}</span></div>
    {#each learned as f (f.name)}{@render fileRow(f)}{/each}
  {/if}
  {#if inspecting}<div class="sm mute">reading…</div>{/if}
  {#if det}
    <h4>{det.info.kind}</h4>
    <div class="sm">{det.info.messages ? det.info.messages.toLocaleString() + ' messages · ' : ''}{det.info.chars.toLocaleString()} characters · about {det.info.windows} model passes{#if det.info.from} · {stamp(det.info.from)} → {stamp(det.info.to)}{/if}</div>
    <Field label="Name for what it produces" hint="facts about people and the subject go to a bank with this name"><Input bind:value={lr.title} /></Field>
    {#if det.info.participants?.length}
      <Field label="Which one are you?" hint="facts about you go to your own bank; everything else is filed under the chat"><Select bind:value={lr.me} options={[{ value: '', label: '(none of them / not sure)' }, ...det.info.participants.map((p) => ({ value: p.name, label: p.name + ' — ' + p.messages + ' messages' }))]} /></Field>
      <Field label="Only from" hint="skip older messages (optional)"><input type="date" bind:value={lr.since} class="ans" /></Field>
    {/if}
    <div class="row end"><Button variant="accent" loading={starting} disabled={!lr.title.trim()} onclick={startLearn}>Start learning</Button></div>
  {/if}
  <h4>Jobs</h4>
  {#each jobs as j (j.id)}
    <div class="rvrow">
      <div class="row"><b>{j.name}</b><Badge tone={j.status === 'done' ? 'ok' : j.status === 'running' ? 'accent' : j.status === 'failed' ? 'err' : 'mute'}>{j.status}</Badge><span class="sm mute grow">{j.kind} → {j.title}</span><span class="sm">{j.facts} facts</span></div>
      <Bar value={j.done} max={Math.max(j.total, 1)} tone={j.status === 'failed' ? 'err' : 'accent'} height={4} label="{j.done} of {j.total} passes" />
      {#if j.error}<div class="sm mute">{j.error}</div>{/if}
      <div class="row end">
        {#if j.status === 'running'}<Button size="sm" variant="ghost" onclick={() => jobAction(j, 'ingest.cancel')}>Stop</Button>
        {:else if j.status !== 'done'}<Button size="sm" variant="ghost" onclick={() => jobAction(j, 'ingest.resume')}>Resume</Button>{/if}
        {#if j.status !== 'running'}<Button size="sm" variant="ghost" title="Remove this entry from the list" onclick={() => jobAction(j, 'ingest.forget_job')}>Clear</Button>{/if}
      </div>
    </div>
  {:else}<div class="sm mute">nothing learned from documents yet</div>{/each}
  {#snippet footer()}<Button variant="ghost" onclick={() => (learnOpen = false)}>Close</Button>{/snippet}
</Modal>

<Modal bind:open={timeOpen} title="Memory over time{bank ? ' · ' + labelOf(bank) : ''}" width={900}>
  <div class="row"><Segmented size="sm" bind:value={tv.mode} options={[{ value: 'asof', label: 'as of a date' }, { value: 'timeline', label: 'what changed' }]} />
    {#if tv.mode === 'asof'}<input type="date" class="ans" style="width:auto" bind:value={tv.at} max={new Date().toISOString().slice(0, 10)} /><Input size="sm" bind:value={tv.q} placeholder="filter…" onenter={loadTime} />
    {:else}<span class="sm mute">last</span><NumberInput bind:value={tv.days} min={1} max={730} step={7} unit="days" />{/if}
    {#if tvBusy}<span class="sm mute">loading…</span>{/if}</div>
  {#if tv.mode === 'asof'}
    <div class="sm mute">What memory held on {tv.at}: {tvFacts.length} fact{tvFacts.length === 1 ? '' : 's'}{tvFacts.length >= 300 ? ' (first 300)' : ''}. Facts marked “since retired” were dropped or replaced later.</div>
    <table class="t"><thead><tr><th>Fact</th><th>Bank</th><th>Since</th><th>Later</th></tr></thead><tbody>
      {#each tvFacts as f (f.id)}
        <tr class="click" onclick={() => { timeOpen = false; openFact(f); }}><td class="pre">{f.text}{#if f.kind === 'conclusion'} <Badge tone="accent">conclusion</Badge>{/if}</td><td class="sm">{f.bank}</td><td class="sm" data-sort={f.valid_from}>{stamp(f.valid_from)}</td>
          <td class="sm">{#if f.valid_to}<Badge tone="mute" title={stamp(f.valid_to)}>since retired</Badge>{:else}<span class="mute">still true</span>{/if}</td></tr>
      {:else}<tr><td colspan="4" class="mute">memory held nothing on that date</td></tr>{/each}
    </tbody></table>
  {:else}
    {#each tvDays as d (d.day)}
      <div class="rvrow">
        <div class="row"><b>{d.day}</b>{#if d.added}<Badge tone="ok">+{d.added} learned</Badge>{/if}{#if d.corrected}<Badge tone="accent">{d.corrected} corrected</Badge>{/if}{#if d.retired}<Badge tone="mute">{d.retired} retired</Badge>{/if}</div>
        {#each d.samples as it (it.kind + it.id)}
          <button type="button" class="ltx" onclick={() => { timeOpen = false; openId(it.id); }}><span class="mute sm">{it.kind === 'added' ? '+' : it.kind === 'corrected' ? '~' : '−'}</span> {it.text} <span class="mute sm">{it.bank}</span></button>
        {/each}
        {#if d.added + d.corrected + d.retired > d.samples.length}<div class="sm mute">…and {d.added + d.corrected + d.retired - d.samples.length} more</div>{/if}
      </div>
    {:else}<div class="sm mute">nothing changed in that period</div>{/each}
  {/if}
  {#snippet footer()}<Button variant="ghost" onclick={() => (timeOpen = false)}>Close</Button>{/snippet}
</Modal>

<Modal bind:open={healthOpen} title="Memory health" width={900}>
  <h4>Jobs</h4>
  <div class="sm mute">Every memory job's last run. A job that runs but never changes anything may just have nothing to do — or a threshold too high (see the counters below and Settings → Memory).</div>
  <table class="t">
    <thead><tr><th>Job</th><th>Last run</th><th>Result</th><th>Last acted</th><th>Runs</th></tr></thead>
    <tbody>
      {#each act.status.jobs || [] as j (j.name)}
        <tr><td>{j.name}</td><td data-sort={j.last_at}>{j.last_at ? ago(new Date(j.last_at * 1000).toISOString()) : '—'}</td>
          <td>{#if j.error}<Badge tone="err" title={j.error}>error</Badge> <span class="sm">{j.error.slice(0, 80)}</span>{:else}{j.result}{/if}</td>
          <td data-sort={j.acted_at}>{j.acted_at ? ago(new Date(j.acted_at * 1000).toISOString()) : 'never'}</td><td>{j.runs}</td></tr>
      {:else}<tr><td colspan="5" class="mute">no pass has finished yet since PRISM started</td></tr>{/each}
    </tbody>
  </table>
  <h4>Banks</h4>
  <table class="t">
    <thead><tr><th>Bank</th><th>Facts</th><th title="facts with confidence 50%+">Trusted</th><th>Unverified</th><th>New since reflect</th><th>New since analysis</th><th>Conclusions</th><th>Insights</th><th title="conclusions whose evidence changed">Stale</th><th title="open contradicting pairs">Conflicts</th><th title="facts that at least one entity mentions">Entity coverage</th><th title="facts learned from documents">From docs</th></tr></thead>
    <tbody>
      {#each health as h (h.id)}
        <tr><td>{h.bank}</td><td>{h.facts}</td><td>{h.usable}</td><td class:warn={h.unverified > 0 && h.unverified >= h.facts / 2}>{h.unverified}</td><td>{h.fresh_reflect}</td><td>{h.fresh_analyze}</td><td>{h.conclusions}</td><td>{h.insights}</td>
          <td class:warn={h.stale > 0}>{h.stale}</td><td class:warn={h.contradictions > 0}>{h.contradictions}</td>
          <td data-sort={h.facts ? h.mentioned / h.facts : 0}>{h.facts ? Math.round((100 * h.mentioned) / h.facts) + '%' : '—'} <span class="mute sm">({h.entities} entities)</span></td><td>{h.docs}</td></tr>
      {/each}
    </tbody>
  </table>
  {#snippet footer()}<Button variant="ghost" onclick={() => (healthOpen = false)}>Close</Button>{/snippet}
</Modal>

<Modal bind:open={activityOpen} title="Memory activity" width={680}>
  <div class="sm mute">{act.status.step ? 'Working on: ' + act.status.step : 'Idle.'}{act.status.next_at ? ' Next scheduled pass in ' + inSec(act.status.next_at) + ' (new facts or messages wake it sooner).' : ''} Digesting, reflection, analysis, entity extraction, mental models and bank merges all run in this loop; passes that find nothing to do leave no entry.</div>
  <div class="row end"><Button size="sm" variant="ghost" title="Write the digest briefing now (it appears under Autonomy → Briefings)" onclick={async () => { if (await call('memory.digest_now', {})) toast('Digest briefing written'); }}>Digest now</Button></div>
  {#each act.events as e (e.id)}
    <div class="rvrow"><div class="row"><span class="sm mute nowrap">{stamp(e.ts)}</span>{#if e.level !== 'info'}<Badge tone={e.level === 'error' ? 'err' : 'warn'}>{e.level}</Badge>{/if}</div><div class="pre">{e.message}</div></div>
  {:else}<div class="sm mute">nothing yet</div>{/each}
  {#snippet footer()}<Button variant="ghost" onclick={() => (activityOpen = false)}>Close</Button>{/snippet}
</Modal>

<Modal bind:open={modelsOpen} title="Mental models" width={680}>
  <div class="sm mute">A model is a standing question about your world. Memory answers it from the facts, conclusions and insights it holds and rewrites the answer as new facts arrive. Agents read models before searching raw facts.</div>
  {#each models as m (m.id)}
    <div class="rvrow">
      <div class="row"><b>{m.name}</b><span class="mute sm grow">{m.query}</span>{#if m.fresh}<Badge tone="attn" title="trusted facts added since the last refresh">{m.fresh} new</Badge>{/if}</div>
      <div class="pre">{m.body || 'not written yet — refresh once memory holds something about it'}</div>
      <div class="row end"><span class="sm mute grow">{m.refreshed_at ? 'refreshed ' + ago(m.refreshed_at) + ' · from ' + m.sources.length + ' facts' : ''}</span>
        <Button size="sm" variant="ghost" loading={modelBusy === m.id} onclick={() => refreshModel(m)}>Refresh</Button>
        <Button size="sm" variant="ghost" onclick={() => deleteModel(m)}>Delete</Button></div>
    </div>
  {:else}<div class="sm mute">no models yet</div>{/each}
  <h4>New model</h4>
  <Field label="Name"><Input bind:value={nm.name} placeholder="Media setup" /></Field>
  <Field label="Question"><Textarea bind:value={nm.query} rows={2} mono={false} placeholder="What does the user want from their media setup and what is in the way?" /></Field>
  <Field label="Scope"><Select bind:value={nm.bank_id} options={[{ value: 0, label: 'every bank' }, ...banks.map((b) => ({ value: b.id, label: labelOf(b.id) }))]} /></Field>
  {#snippet footer()}<Button variant="ghost" onclick={() => (modelsOpen = false)}>Close</Button><Button loading={modelBusy === -1} disabled={!nm.name.trim() || !nm.query.trim()} onclick={addModel}>Add model</Button>{/snippet}
</Modal>

<Modal bind:open={reviewOpen} title="Review" width={640}>
  <div class="sm mute">Things memory can't resolve on its own — a human call, not a maintenance job.</div>
  {#if review}
    <h4>Contradictions <span class="mute sm">{review.contradictions.length}</span></h4>
    {#each review.contradictions as c (c.a.id + '-' + c.b.id)}
      <div class="rvrow">
        <div class="rvpair">
          <button type="button" class="ltx" onclick={() => resolveContradiction(c, c.a.id)} title="keep this one, retire the other">{c.a.text}</button>
          <span class="sm mute">vs.</span>
          <button type="button" class="ltx" onclick={() => resolveContradiction(c, c.b.id)} title="keep this one, retire the other">{c.b.text}</button>
        </div>
        {#if c.note}<div class="sm mute">{c.note}</div>{/if}
        <div class="row end"><Button size="sm" variant="ghost" onclick={() => dismissContradiction(c)}>Not actually a contradiction</Button></div>
      </div>
    {:else}<div class="sm mute">none</div>{/each}
    <h4>Open questions and hypotheses <span class="mute sm">{review.open.length}</span></h4>
    {#each review.open as f (f.id)}
      {@const q = f.tags?.includes('question')}
      <div class="rvrow">
        <button type="button" class="ltx" onclick={() => { reviewOpen = false; openFact(f); }}><Badge tone={q ? 'accent' : 'attn'}>{q ? 'question' : 'hypothesis'}</Badge> {f.text}</button>
        {#if q}<input class="ans" placeholder="your answer" bind:value={answers[f.id]} />{/if}
        <div class="row end">
          {#if q}<Button size="sm" variant="ghost" disabled={!(answers[f.id] || '').trim()} onclick={() => resolveOpen(f, 'answer')}>Answer</Button>
          {:else}<Button size="sm" variant="ghost" title="Store it as a trusted fact" onclick={() => resolveOpen(f, 'confirm')}>True</Button>{/if}
          <Button size="sm" variant="ghost" title="Retire it" onclick={() => resolveOpen(f, 'reject')}>{q ? 'Skip' : 'False'}</Button>
        </div>
      </div>
    {:else}<div class="sm mute">none</div>{/each}
    <h4>Unverified facts <span class="mute sm">{review.unverified.length}</span></h4>
    {#if review.unverified.length + review.open.length}<div class="row end"><Button size="sm" variant="accent" title="An agent checks up to 6 of these against the web and confirms, retires or leaves them" onclick={async () => { const n = await call('memory.verify_now', { ids: [] }); if (n !== undefined && n !== null) toast(n ? `${n} claims sent to an agent to verify` : 'Nothing to verify right now'); }}>Verify with an agent</Button></div>{/if}
    {#each review.unverified as f (f.id)}
      <div class="rvrow">
        <button type="button" class="ltx" onclick={() => { reviewOpen = false; openFact(f); }}>{f.text}</button>
        <div class="sm mute">learned from {f.origins?.length ? f.origins.join(', ') : 'untrusted content'}; not used for conclusions until confirmed</div>
        <div class="row end">
          <Button size="sm" variant="ghost" title="Send an agent to check this one" onclick={async () => { if (await call('memory.verify_now', { ids: [f.id] })) toast('Sent to an agent'); }}>Verify</Button>
          <Button size="sm" variant="ghost" onclick={() => confirmFromReview(f)}>Confirm</Button>
          <Button size="sm" variant="ghost" onclick={() => outdateFromReview(f)}>Retire</Button>
        </div>
      </div>
    {:else}<div class="sm mute">none</div>{/each}
    <h4>Stale conclusions <span class="mute sm">{review.stale_conclusions.length}</span></h4>
    {#each review.stale_conclusions as f (f.id)}
      <div class="rvrow">
        <button type="button" class="ltx" onclick={() => { reviewOpen = false; openFact(f); }}>{f.text}</button>
        <div class="sm mute">some of its evidence was retired — Reflect will revise or retire it</div>
      </div>
    {:else}<div class="sm mute">none</div>{/each}
    <h4>About to be auto-archived <span class="mute sm">{review.prune_candidates.length}</span></h4>
    {#each review.prune_candidates as f (f.id)}
      <div class="rvrow">
        <button type="button" class="ltx" onclick={() => { reviewOpen = false; openFact(f); }}>{f.text}</button>
        <div class="row end">
          <Button size="sm" variant="ghost" onclick={() => pinFromReview(f)}>Pin</Button>
          <Button size="sm" variant="ghost" onclick={() => outdateFromReview(f)}>Retire now</Button>
        </div>
      </div>
    {:else}<div class="sm mute">none</div>{/each}
  {/if}
  {#snippet footer()}<Button variant="ghost" onclick={() => (reviewOpen = false)}>Close</Button>{/snippet}
</Modal>

<style>
  .pg { display: flex; flex-direction: column; gap: 6px; height: 100%; min-height: 0; }
  .bar { display: flex; align-items: center; gap: 8px; flex: none; }
  .srch { width: 320px; }
  @media (max-width: 820px) { .srch { width: 100%; } .body { grid-template-columns: minmax(0, 1fr); grid-template-rows: auto minmax(0, 1fr); } .banks { max-height: 120px; } }
  .body { flex: 1; display: grid; grid-template-columns: 190px minmax(0, 1fr); gap: 6px; min-height: 0; }
  .banks { flex: 1; padding: 4px 0; }
  .gk { padding: 6px 8px 2px; font-size: 10px; text-transform: uppercase; letter-spacing: 0.12em; color: var(--fg-mute); }
  .bk { display: flex; justify-content: space-between; gap: 6px; width: 100%; padding: 2px 8px 2px 14px; background: none; border: 0; border-left: 2px solid transparent; color: var(--fg-dim); text-align: left; }
  .bk:hover { background: var(--bg-2); color: var(--fg); }
  .bk.on { border-left-color: var(--fg); background: var(--bg-3); color: var(--fg-hi); }
  .n { color: var(--fg-mute); font-size: var(--fs-sm); }
  .bfoot { padding: 6px; border-top: 1px solid var(--line); display: flex; flex-direction: column; gap: 4px; }
  tr.old td { opacity: 0.55; }
  tr.concl td:nth-child(2) { border-left: 2px solid var(--accent-dim); background: color-mix(in srgb, var(--accent-bg) 55%, transparent); }
  .gbody { flex: 1; min-height: 0; display: flex; padding: 6px; }
  .two { display: grid; grid-template-columns: 1fr 1fr; gap: 4px; }
  .lk { display: flex; flex-direction: column; gap: 4px; border-top: 1px solid var(--line); padding-top: 6px; }
  .kw { width: 118px; flex: none; }
  .lrow { display: flex; align-items: center; gap: 6px; }
  .lrow.old { opacity: 0.55; }
  .lrow :global(.bar), .lrow > :global(div) { flex: none; width: 44px; }
  .ltx { flex: 1; min-width: 0; text-align: left; background: none; border: 0; padding: 0; color: var(--fg); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .ltx:hover { color: var(--fg-hi); text-decoration: underline; }
  .mgl { display: flex; flex-direction: column; gap: 2px; margin: 6px 0; max-height: 240px; overflow: auto; border: 1px solid var(--line); padding: 4px 6px; }
  .mgrow { display: flex; align-items: center; gap: 8px; cursor: pointer; }
  .mghint { margin-top: 8px; padding: 6px 8px; border: 1px solid var(--line); background: var(--bg-1); font-size: 12px; }
  .frow2 { display: flex; align-items: center; gap: 8px; }
  .frow2 .ltx { flex: 1; min-width: 0; }
  .ltx.on { color: var(--accent, var(--fg)); font-weight: 700; }
  .ltx.inline { flex: none; display: inline; color: var(--accent, var(--fg)); }
  .pinfield { display: flex; align-items: flex-end; padding-bottom: 6px; }
  .splist { max-height: 300px; border: 1px solid var(--line); padding: 4px 6px; display: flex; flex-direction: column; gap: 2px; }
  .ans { width: 100%; margin: 4px 0; padding: 4px 6px; border: 1px solid var(--line); border-radius: 4px; background: var(--bg2); color: var(--fg); font: inherit; }
  td.warn { color: var(--attn); font-weight: 700; }
  .act { background: none; border: 0; color: var(--fg-dim); font: inherit; font-size: 11px; cursor: pointer; display: inline-flex; align-items: center; gap: 5px; }
  .act:hover { color: var(--fg); }
  .rvrow { display: flex; flex-direction: column; gap: 2px; padding: 6px 0; border-top: 1px solid var(--line-2); }
  .rvpair { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .rvpair .ltx { flex: 1 1 auto; min-width: 160px; white-space: normal; }
</style>
