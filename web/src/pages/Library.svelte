<script>
  import { call, listen, toast, confirmBox, ago, until, stamp, fmtTokens } from '../lib/store.svelte.js';
  import { artifactUrl } from '../lib/ws.js';
  import Panel from '../lib/ui/Panel.svelte';
  import Tabs from '../lib/ui/Tabs.svelte';
  import Button from '../lib/ui/Button.svelte';
  import Switch from '../lib/ui/Switch.svelte';
  import Input from '../lib/ui/Input.svelte';
  import Bar from '../lib/ui/Bar.svelte';
  import Badge from '../lib/ui/Badge.svelte';
  import Led from '../lib/ui/Led.svelte';
  import Modal from '../lib/ui/Modal.svelte';
  import Field from '../lib/ui/Field.svelte';
  import Tags from '../lib/ui/Tags.svelte';
  import Empty from '../lib/ui/Empty.svelte';
  import Icon from '../lib/ui/Icon.svelte';

  let tab = $state('bookmarks');
  // ── code workspaces: isolated git worktrees where agents did coding work ──
  let wss = $state([]);
  let wsDiff = $state(null); // { ws, text }
  let wsBusy = $state(0);
  const loadWs = async () => (wss = (await call('workspaces.list', {}, { quiet: true })) || []);
  $effect(() => { if (tab === 'code') loadWs(); return listen('workspaces.update', loadWs); });
  let cmds = $state(null); // { repo, build, test, lint, detected_* } being edited
  async function openCmds(w) { cmds = await call('workspaces.commands', { repo: w.repo }); }
  async function saveCmds() { if (await call('workspaces.set_commands', { repo: cmds.repo, build: cmds.build, test: cmds.test, lint: cmds.lint })) { toast('Saved'); cmds = null; } }
  async function runChecks(w) {
    wsBusy = w.id;
    const r = await call('workspaces.verify', { id: w.id });
    wsBusy = 0;
    if (r !== undefined) { toast(String(r).split('\n')[0] === 'pass' ? 'Checks passed' : String(r).split('\n')[0] === 'fail' ? 'Checks failed' : 'No checks configured', String(r).startsWith('pass') ? 'ok' : 'err'); loadWs(); if (wsDiff?.ws.id === w.id) viewDiff(w); }
  }
  const checkTone = (w) => (w.verify_status === 'pass' ? (w.stale ? 'warn' : 'ok') : w.verify_status === 'fail' ? 'err' : 'mute');
  const verdictTone = (v) => (v === 'approve' ? 'ok' : v === 'reject' ? 'err' : 'warn');
  // applying something unchecked, failing or rejected asks first
  async function applyChecked(w) {
    const why = w.verdict === 'reject' ? 'The reviewer rejected it.' : w.verify_status === 'fail' ? 'Its checks fail.' : w.stale ? 'It changed since its checks ran.' : !w.verify_status ? 'Its checks were never run.' : '';
    if (why && !(await confirmBox({ title: 'Apply anyway?', text: why + ' Apply it to your checkout anyway?', ok: 'Apply anyway', danger: true }))) return;
    wsAct(w, 'workspaces.apply', 'Applied');
  }
  async function viewDiff(w) { const text = await call('workspaces.diff', { id: w.id }); if (text !== undefined) wsDiff = { ws: w, text }; }
  async function wsAct(w, m, msg) {
    if (m === 'workspaces.discard' && !(await confirmBox({ title: 'Discard workspace', text: `Throw away ${w.branch} and everything the agent changed in it?`, ok: 'Discard', danger: true }))) return;
    wsBusy = w.id;
    const r = await call(m, { id: w.id });
    wsBusy = 0;
    if (r !== undefined) { toast(typeof r === 'string' ? r : msg, 'ok'); wsDiff = null; loadWs(); }
  }
  const diffClass = (l) => (l.startsWith('+++') || l.startsWith('---') || l.startsWith('diff ') || l.startsWith('index ') ? 'dm' : l.startsWith('+') ? 'da' : l.startsWith('-') ? 'dd' : l.startsWith('@@') ? 'dh' : '');
  const human = (n) => (n >= 1e9 ? (n / 1e9).toFixed(1) + ' GB' : n >= 1e6 ? (n / 1e6).toFixed(1) + ' MB' : n >= 1e3 ? (n / 1e3).toFixed(0) + ' KB' : n + ' B');

  // bookmarks
  let bms = $state([]);
  let bq = $state('');
  let bOpen = $state(false);
  let bm = $state(null);
  let harvesting = $state(false);
  async function harvest() {
    harvesting = true;
    const n = await call('bookmarks.harvest', {});
    harvesting = false;
    if (n !== undefined && n !== null) { toast(n ? `${n} bookmark${n === 1 ? '' : 's'} added from pages the agents visited` : 'Nothing new worth bookmarking'); loadBm(); }
  }
  const loadBm = async () => (bms = (await call('bookmarks.list', {}, { quiet: true })) || []);
  $effect(() => { loadBm(); });
  // rank-first: bookmarks that have actually proven useful (or were saved on purpose by the user) lead
  const bshown = $derived(bms.filter((b) => !bq || `${b.title} ${b.url} ${b.description} ${(b.keywords || []).join(' ')}`.toLowerCase().includes(bq.toLowerCase())).sort((a, b) => b.rank - a.rank));
  async function saveBm() { if (await call('bookmarks.save', $state.snapshot(bm))) { bOpen = false; loadBm(); } }
  async function delBm(b) { if (await confirmBox({ title: 'Delete bookmark', text: b.title || b.url, ok: 'Delete', danger: true })) { await call('bookmarks.delete', { id: b.id }); loadBm(); } }
  let enriching = $state(false);
  async function suggestBm() {
    if (!bm?.url?.trim()) return;
    enriching = true;
    const s = await call('bookmarks.enrich', { url: bm.url.trim() });
    enriching = false;
    if (!s) return;
    if (s.title && !bm.title) bm.title = s.title;
    if (s.description && !bm.description) bm.description = s.description;
    if (s.keywords?.length && !bm.keywords?.length) bm.keywords = s.keywords;
  }

  // downloads
  let dls = $state([]);
  let durl = $state('');
  const loadDl = async () => (dls = (await call('downloads.list', {}, { quiet: true })) || []);
  $effect(() => { loadDl(); return listen('download.progress', (e) => { const d = dls.find((x) => x.id === e.id); if (d) Object.assign(d, e); else loadDl(); }); });
  async function startDl() { if (await call('downloads.start', { url: durl })) { durl = ''; loadDl(); } }
  const dled = (s) => ({ running: 'ok', done: 'ok', queued: 'standby', failed: 'error', cancelled: 'off' })[s];

  // background processes
  let procs = $state([]);
  let logView = $state(null); // the process whose log is open
  let logText = $state('');
  const loadProcs = async () => (procs = (await call('processes.list', {}, { quiet: true })) || []);
  $effect(() => { loadProcs(); return listen('process.update', loadProcs); });
  const procLed = (s) => ({ running: 'ok', exited: 'ok', killed: 'off', failed: 'error', unknown: 'attention' })[s] || 'off';
  async function stopProc(p) {
    await call('processes.cancel', { id: p.id });
    await loadProcs();
    if (logView?.id === p.id) logView = procs.find((x) => x.id === p.id) || logView;
  }
  async function viewLog(p) { logView = p; logText = (await call('processes.log', { id: p.id })) || '(no output yet)'; }
  $effect(() => {
    if (!logView) return;
    const i = setInterval(async () => { logText = (await call('processes.log', { id: logView.id }, { quiet: true })) || logText; }, 1500);
    return () => clearInterval(i);
  });

  // artifacts
  let arts = $state([]);
  const loadA = async () => (arts = (await call('artifacts.list', {}, { quiet: true })) || []);
  $effect(() => { loadA(); return listen('artifact.new', loadA); });
  // folder maps: one-line summaries of every file under a folder the user attached
  let maps = $state([]);
  let mapView = $state(null);
  let mapRows = $state([]);
  const loadMaps = async () => (maps = (await call('foldermap.list', {}, { quiet: true })) || []);
  $effect(() => listen('foldermap.progress', (m) => { const i = maps.findIndex((x) => x.id === m.id); if (i >= 0) maps[i] = m; else loadMaps(); }));
  $effect(() => listen('foldermap.done', loadMaps));
  $effect(() => { loadMaps(); });
  async function viewMap(m) { mapView = m; mapRows = (await call('foldermap.entries', { id: m.id })) || []; }
  async function rebuildMap(m) { if (await call('foldermap.build', { path: m.root })) loadMaps(); }
  async function delMap(m) { if (await confirmBox({ title: 'Delete folder map', text: m.root, ok: 'Delete', danger: true })) { await call('foldermap.delete', { id: m.id }); loadMaps(); } }
  const mapTone = { done: 'ok', running: 'accent', queued: 'accent', failed: 'err' };
  async function keepA(a) { if (await call('artifacts.keep', { id: a.id })) loadA(); }
  async function delA(a) { if (await confirmBox({ title: 'Delete artifact', text: a.name, ok: 'Delete', danger: true })) { await call('artifacts.delete', { id: a.id }); loadA(); } }

  // documents
  let srcs = $state([]);
  let sOpen = $state(false);
  let src = $state(null);
  let q = $state('');
  let hits = $state(null);
  let prog = $state({});
  // drop zone: documents go to the Inbox, are read (PDF, Word, EPUB, HTML, pictures via OCR…) and indexed at once
  let up = $state([]); // {name, state: 'reading'|'ok'|'err', note}
  let over = $state(false);
  let filePick = $state();
  const MAX_UPLOAD = 28 * 1024 * 1024; // the socket carries base64: stay under its limit
  const b64 = (f) => new Promise((res, rej) => { const r = new FileReader(); r.onload = () => res(String(r.result).split(',')[1] || ''); r.onerror = () => rej(r.error); r.readAsDataURL(f); });
  async function ingest(files) {
    for (const f of files) {
      const row = { name: f.name, state: 'reading', note: '' };
      up.unshift(row);
      const r0 = up[0];
      if (f.size > MAX_UPLOAD) { r0.state = 'err'; r0.note = `larger than ${MAX_UPLOAD >> 20} MB`; continue; }
      try {
        const r = await call('docs.upload', { name: f.name, data: await b64(f) }, { throw: true });
        r0.state = 'ok'; r0.note = `${r.chunks} searchable passages`;
      } catch (e) { r0.state = 'err'; r0.note = e.message; }
    }
    loadS();
  }
  function ondrop(e) { over = false; if (e.dataTransfer?.files?.length) { e.preventDefault(); ingest([...e.dataTransfer.files]); } }
  const loadS = async () => (srcs = (await call('docs.sources', {}, { quiet: true })) || []);
  $effect(() => { loadS(); const a = listen('docs.progress', (e) => (prog[e.id] = e)); const b = listen('docs.indexed', (e) => { delete prog[e.id]; loadS(); toast(e.error ? `Index failed: ${e.error}` : `Indexed ${e.files} files (${e.chunks} chunks)`, e.error ? 'err' : 'ok'); }); return () => { a(); b(); }; });
  async function saveSrc() { if (await call('docs.save', $state.snapshot(src))) { sOpen = false; loadS(); } }
  async function search() { hits = q.trim() ? (await call('docs.search', { query: q.trim() })) || [] : null; }
</script>

<div class="pg">
  <Tabs tabs={[{ id: 'bookmarks', label: 'Bookmarks', badge: bms.length }, { id: 'downloads', label: 'Downloads', badge: dls.filter((d) => d.status === 'running').length || '' }, { id: 'processes', label: 'Processes', badge: procs.filter((p) => p.status === 'running').length || '' }, { id: 'artifacts', label: 'Artifacts', badge: arts.length }, { id: 'folders', label: 'Folder maps', badge: maps.length || '' }, { id: 'code', label: 'Code', badge: wss.filter((w) => w.status === 'open').length || '' }, { id: 'docs', label: 'Documents' }]} bind:active={tab} />

  {#if tab === 'bookmarks'}
    <div class="bar"><div class="f"><Input bind:value={bq} size="sm" placeholder="filter…" /></div><span class="grow"></span>
      <Button size="sm" variant="ghost" loading={harvesting} title="Look through the pages the agents fetched and keep the ones worth returning to (this also runs with the memory jobs)" onclick={harvest}>Find from browsing</Button>
      <Button size="sm" onclick={() => { bm = { id: 0, url: '', title: '', description: '', keywords: [] }; bOpen = true; }}><Icon name="plus" size={11} /> Bookmark</Button></div>
    <Panel flush grow>
      <div class="scroll"><table class="t">
        <thead><tr><th style="width:40px" title="usage rank: rises when an agent's search actually returns it, decays daily otherwise">Rank</th><th>Title</th><th>URL</th><th>Description</th><th>Keywords</th><th style="width:96px"></th></tr></thead>
        <tbody>
          {#each bshown as b (b.id)}
            <tr><td class="mute sm" title="last used {b.last_used ? ago(b.last_used) : 'never'}">{b.rank.toFixed(1)}</td><td class="hi">{b.title}</td><td class="ellipsis" style="max-width:260px"><a href={b.url} target="_blank" rel="noopener noreferrer">{b.url}</a></td><td class="dim">{b.description}</td><td class="mute sm">{(b.keywords || []).join(', ')}</td>
              <td class="end nowrap"><Button size="sm" variant="ghost" onclick={() => { bm = structuredClone($state.snapshot(b)); bOpen = true; }}>Edit</Button><Button size="sm" variant="ghost" onclick={() => delBm(b)}><Icon name="trash" size={11} /></Button></td></tr>
          {:else}<tr><td colspan="6"><Empty>no bookmarks — agents can add them for fast-dial lookups</Empty></td></tr>{/each}
        </tbody></table></div>
    </Panel>
  {:else if tab === 'code'}
    <div class="bar"><span class="sm mute">When an agent works on a repository it does so in an isolated copy (git worktree) on its own branch — your checkout is never touched. Review what it changed here, then apply it as staged changes, keep the branch, or discard it.</span><span class="grow"></span><Button size="sm" variant="ghost" onclick={loadWs}><Icon name="refresh" size={11} /> Refresh</Button></div>
    <Panel flush grow>
      <div class="scroll"><table class="t">
        <thead><tr><th>Workspace</th><th>Repository</th><th>Agent</th><th>Status</th><th>Checks</th><th>Review</th><th>Changes</th><th style="width:300px"></th></tr></thead>
        <tbody>
          {#each wss as w (w.id)}
            <tr><td><span class="mono">{w.branch}</span><div class="sm mute">{ago(w.created_at)}</div></td><td class="sm">{w.repo}</td><td>{w.agent}{#if w.task_id} <span class="sm mute">task #{w.task_id}</span>{/if}</td>
              <td><Badge tone={w.status === 'open' ? 'accent' : w.status === 'applied' ? 'ok' : w.status === 'kept' ? 'warn' : 'mute'}>{w.status}</Badge></td>
              <td>{#if w.verify_status}<Badge tone={checkTone(w)} title={w.verify_output}>{w.verify_status}{w.stale ? ' · stale' : ''}</Badge>{:else if w.status === 'open'}<span class="mute sm">not run</span>{/if}</td>
              <td>{#if w.verdict}<Badge tone={verdictTone(w.verdict)} title={w.review}>{w.verdict}</Badge>{:else if w.review_task_id}<span class="mute sm">reviewing…</span>{/if}</td>
              <td class="sm pre">{(w.stat || '').split('\n').slice(-1)[0]}</td>
              <td class="end">{#if w.status === 'open' || w.status === 'kept'}
                <Button size="sm" variant="ghost" onclick={() => viewDiff(w)}>Diff</Button>
                {#if w.status === 'open'}<Button size="sm" variant="ghost" loading={wsBusy === w.id} title="Run the project's build, lint and test commands in this workspace" onclick={() => runChecks(w)}>Run checks</Button>{/if}
                <Button size="sm" variant="ghost" title="Which commands the checks run for this repository" onclick={() => openCmds(w)}>Commands…</Button>
                <Button size="sm" variant="accent" loading={wsBusy === w.id} title="Bring the changes into your checkout as staged changes (needs a clean checkout)" onclick={() => applyChecked(w)}>Apply</Button>
                {#if w.status === 'open'}<Button size="sm" variant="ghost" title="Commit and keep the branch in the repository; merge it yourself" onclick={() => wsAct(w, 'workspaces.keep', 'Kept')}>Keep branch</Button>{/if}
                <Button size="sm" variant="ghost" onclick={() => wsAct(w, 'workspaces.discard', 'Discarded')}>Discard</Button>
              {/if}</td></tr>
          {:else}<tr><td colspan="8" class="mute">no coding workspaces yet — ask an agent to work on a repository (it opens one with workspace_open)</td></tr>{/each}
        </tbody>
      </table></div>
    </Panel>
  {:else if tab === 'downloads'}
    <div class="bar"><div class="grow"><Input bind:value={durl} size="sm" placeholder="http(s) URL — magnet/torrent/ftp need aria2c" onenter={startDl} /></div><Button size="sm" variant="primary" disabled={!durl.trim()} onclick={startDl}><Icon name="dl" size={11} /> Download</Button></div>
    <Panel flush grow>
      <div class="scroll"><table class="t">
        <thead><tr><th></th><th>File</th><th style="width:220px">Progress</th><th>Owner</th><th style="width:44px">Age</th><th style="width:60px"></th></tr></thead>
        <tbody>
          {#each dls as d (d.id)}
            <tr>
              <td><Led state={dled(d.status)} pulse={d.status === 'running'} size={8} /></td>
              <td class="ellipsis" style="max-width:340px"><span class="hi">{d.dest ? d.dest.split('/').pop() : d.url}</span><div class="sm mute ellipsis">{d.url}</div>{#if d.error}<div class="err sm">{d.error}</div>{/if}</td>
              <td>{#if d.total > 0}<Bar value={d.bytes} max={d.total} tone={d.status === 'failed' ? 'err' : ''} height={6} /><div class="sm mute">{d.total > 100 && d.total !== 100 ? `${human(d.bytes)} / ${human(d.total)}` : d.bytes + '%'}</div>{:else}<span class="sm mute">{d.status}{d.bytes ? ' · ' + human(d.bytes) : ''}</span>{/if}</td>
              <td class="dim">{d.owner}</td><td class="mute sm">{ago(d.created_at)}</td>
              <td>{#if d.status === 'running' || d.status === 'queued'}<Button size="sm" variant="danger" onclick={() => call('downloads.cancel', { id: d.id })}>Stop</Button>{/if}</td>
            </tr>
          {:else}<tr><td colspan="6"><Empty>no downloads</Empty></td></tr>{/each}
        </tbody></table></div>
    </Panel>
  {:else if tab === 'processes'}
    <Panel flush grow>
      <div class="scroll"><table class="t">
        <thead><tr><th></th><th>Command</th><th>Agent</th><th style="width:44px">Age</th><th style="width:130px"></th></tr></thead>
        <tbody>
          {#each procs as p (p.id)}
            <tr>
              <td><Led state={procLed(p.status)} pulse={p.status === 'running'} size={8} /></td>
              <td class="ellipsis" style="max-width:380px"><span class="hi">{p.command}</span><div class="sm mute">{p.status}{p.exit_code !== undefined && p.exit_code !== null ? ` · exit ${p.exit_code}` : ''}{p.status === 'unknown' ? ' — PRISM restarted while this was running; its real status is unknown' : ''}</div></td>
              <td class="dim">{p.agent || '—'}</td><td class="mute sm">{ago(p.created_at)}</td>
              <td class="end nowrap"><Button size="sm" variant="ghost" onclick={() => viewLog(p)}>Log</Button>{#if p.status === 'running'}<Button size="sm" variant="danger" onclick={() => stopProc(p)}>Stop</Button>{/if}</td>
            </tr>
          {:else}<tr><td colspan="5"><Empty>no background processes — agents start one with process_start for anything that runs too long for a normal shell call</Empty></td></tr>{/each}
        </tbody></table></div>
    </Panel>
  {:else if tab === 'artifacts'}
    <Panel flush grow>
      <div class="scroll"><table class="t">
        <thead><tr><th>Name</th><th>Type</th><th>Size</th><th>By</th><th>Created</th><th style="width:96px"></th></tr></thead>
        <tbody>
          {#each arts as a (a.id)}
            <tr><td class="hi"><a href={artifactUrl(a.id)} target="_blank" rel="noopener noreferrer">{a.name}</a>{#if a.expires_at} <Badge tone="attn" title="a hand-off between agents: deleted {stamp(a.expires_at)} unless you keep it">temp · {until(a.expires_at)}</Badge>{/if}{#if a.tainted} <Badge tone="mute" title="written while untrusted content was in scope">untrusted</Badge>{/if} <span class="sm mute">#{a.id}</span></td><td class="mute">{a.mime}</td><td class="mute">{human(a.size)}</td><td class="dim">{a.created_by}</td><td class="mute sm">{stamp(a.created_at)}</td>
              <td class="end">{#if a.expires_at}<Button size="sm" variant="ghost" title="keep it permanently" onclick={() => keepA(a)}>Keep</Button>{/if}<Button size="sm" variant="ghost" onclick={() => delA(a)}><Icon name="trash" size={11} /></Button></td></tr>
          {:else}<tr><td colspan="6"><Empty>no artifacts yet — deliverables saved by agents appear here</Empty></td></tr>{/each}
        </tbody></table></div>
    </Panel>
  {:else if tab === 'folders'}
    <Panel flush grow>
      <div class="scroll"><table class="t">
        <thead><tr><th>Folder</th><th style="width:90px">State</th><th style="width:200px">Progress</th><th style="width:60px">Entries</th><th style="width:44px">Age</th><th style="width:150px"></th></tr></thead>
        <tbody>
          {#each maps as m (m.id)}
            <tr>
              <td class="ellipsis" style="max-width:380px" title={m.root}><span class="hi">{m.root.split('/').pop() || m.root}</span><div class="sm mute ellipsis">{m.root}</div>{#if m.error}<div class="err sm">{m.error}</div>{/if}</td>
              <td><Badge tone={mapTone[m.status] || 'mute'} w={8}>{m.status}</Badge></td>
              <td>{#if m.total > 0}<Bar value={m.done} max={m.total} tone={m.status === 'failed' ? 'err' : 'accent'} height={6} /><div class="sm mute">{m.done} / {m.total}{m.truncated ? ' · large folder, first entries only' : ''}</div>{/if}</td>
              <td class="mute">{m.entries}</td><td class="mute sm">{ago(m.updated_at)}</td>
              <td class="end nowrap"><Button size="sm" variant="ghost" onclick={() => viewMap(m)}>View</Button><Button size="sm" variant="ghost" title="summarise changed files again" disabled={m.status === 'running'} onclick={() => rebuildMap(m)}>Rebuild</Button><Button size="sm" variant="ghost" onclick={() => delMap(m)}><Icon name="trash" size={11} /></Button></td>
            </tr>
          {:else}<tr><td colspan="6"><Empty>no folder maps — attach a folder in chat (the folder button) and PRISM summarises every file in it, so agents can find things by content</Empty></td></tr>{/each}
        </tbody></table></div>
    </Panel>
  {:else}
    <div class="drop" class:over role="presentation" ondragover={(e) => { if ([...(e.dataTransfer?.types || [])].includes('Files')) { e.preventDefault(); over = true; } }} ondragleave={() => (over = false)} {ondrop}>
      <input bind:this={filePick} type="file" multiple hidden accept=".pdf,.docx,.epub,.odt,.rtf,.html,.htm,.md,.txt,.csv,.png,.jpg,.jpeg,.heic,.tif,.tiff" onchange={(e) => { ingest([...e.target.files]); e.target.value = ''; }} />
      <span>Drop documents here — PDF (scans are read with OCR), Word, EPUB, ODT, RTF, HTML, text, pictures</span>
      <Button size="sm" onclick={() => filePick.click()}>Choose files…</Button>
      <span class="sm mute">They are kept in the Inbox, indexed at once, and searchable by agents. Treated as outside material.</span>
    </div>
    {#if up.length}
      <div class="ups">{#each up.slice(0, 6) as u}<div class="sm"><span class={u.state === 'err' ? 'err' : u.state === 'ok' ? 'hi' : 'mute'}>{u.state === 'reading' ? '…' : u.state === 'ok' ? '✔' : '✘'}</span> {u.name} <span class="mute">{u.state === 'reading' ? 'reading and indexing…' : u.note}</span></div>{/each}</div>
    {/if}
    <div class="two">
      <Panel title="Indexed sources" flush grow>
        {#snippet right()}<Button size="sm" variant="ghost" onclick={() => { src = { id: 0, name: '', path: '', globs: ['*.md', '*.txt'], watch: false, untrusted: false }; sOpen = true; }}><Icon name="plus" size={11} /> Source</Button>{/snippet}
        <div class="scroll"><table class="t">
          <thead><tr><th>Source</th><th>Path</th><th>Files</th><th>Chunks</th><th>Indexed</th><th></th></tr></thead>
          <tbody>
            {#each srcs as s (s.id)}
              <tr><td class="hi">{s.name}{#if s.watch} <Badge tone="mute" title="re-indexed automatically when files change">watching</Badge>{/if}{#if s.untrusted} <Badge tone="attn" title="material from outside: a turn that searches it is treated as untrusted">outside</Badge>{/if}</td><td class="mute ellipsis" style="max-width:220px">{s.path}</td><td class="dim">{s.files}</td><td class="dim">{s.chunks}</td>
                <td class="mute sm">{#if prog[s.id]}{prog[s.id].done}/{prog[s.id].total}…{:else}{s.indexed_at ? ago(s.indexed_at) : 'never'}{/if}</td>
                <td class="end nowrap"><Button size="sm" variant="primary" loading={!!prog[s.id]} onclick={() => call('docs.index', { id: s.id })}>Index</Button><Button size="sm" variant="ghost" onclick={() => { src = structuredClone($state.snapshot(s)); sOpen = true; }}>Edit</Button>
                  <Button size="sm" variant="ghost" onclick={async () => { if (await confirmBox({ title: 'Remove source', text: s.name, ok: 'Remove', danger: true })) { await call('docs.delete', { id: s.id }); loadS(); } }}><Icon name="trash" size={11} /></Button></td></tr>
            {:else}<tr><td colspan="6"><Empty>add a folder (notes, docs) to search it by meaning</Empty></td></tr>{/each}
          </tbody></table></div>
      </Panel>
      <Panel title="Semantic search" grow>
        <div class="row"><div class="grow"><Input bind:value={q} placeholder="search by meaning…" onenter={search} /></div><Button variant="primary" onclick={search}>Search</Button></div>
        <div class="scroll res">
          {#if hits}{#each hits as h}<div class="hit"><div class="row"><span class="hi">{h.file}</span><Badge tone="mute">{h.source}</Badge><span class="grow"></span><span class="sm mute">{h.score.toFixed(2)}</span></div><div class="pre dim sm">{h.text.slice(0, 400)}{h.text.length > 400 ? '…' : ''}</div></div>{:else}<Empty>no matches</Empty>{/each}{/if}
        </div>
      </Panel>
    </div>
  {/if}
</div>

<Modal open={!!logView} title="Process #{logView?.id} log" width={760} onclose={() => (logView = null)}>
  <pre class="pre proclog">{logText}</pre>
  {#snippet footer()}
    {#if logView?.status === 'running'}<Button variant="danger" onclick={() => { stopProc(logView); }}>Stop</Button>{/if}
    <Button variant="ghost" onclick={() => (logView = null)}>Close</Button>
  {/snippet}
</Modal>

<Modal bind:open={bOpen} title={bm?.id ? 'Edit bookmark' : 'New bookmark'} width={560}>
  {#if bm}
    <Field label="URL"><div class="row"><div class="grow"><Input bind:value={bm.url} placeholder="https://…" /></div><Button size="sm" variant="ghost" loading={enriching} disabled={!bm.url?.trim()} onclick={suggestBm}>Suggest</Button></div></Field>
    <Field label="Title"><Input bind:value={bm.title} /></Field>
    <Field label="Description"><Input bind:value={bm.description} /></Field>
    <Field label="Keywords"><Tags bind:value={bm.keywords} /></Field>
  {/if}
  {#snippet footer()}<Button variant="ghost" onclick={() => (bOpen = false)}>Cancel</Button><Button variant="primary" disabled={!bm?.url?.trim()} onclick={saveBm}>Save</Button>{/snippet}
</Modal>

<Modal bind:open={sOpen} title={src?.id ? 'Edit source' : 'New source'} width={560}>
  {#if src}<Field label="Name"><Input bind:value={src.name} /></Field><Field label="Folder"><Input bind:value={src.path} mono placeholder="~/Documents/Notes" /></Field><Field label="File patterns" hint="add *.pdf, *.docx, *.epub, *.png … to read those too"><Tags bind:value={src.globs} placeholder="*.md" /></Field>
    <Switch bind:checked={src.watch} label="watch: re-index when files change" />
    <Switch bind:checked={src.untrusted} label="material from outside (downloads, mail attachments): treat what agents read from it as untrusted" />{/if}
  {#snippet footer()}<Button variant="ghost" onclick={() => (sOpen = false)}>Cancel</Button><Button variant="primary" disabled={!src?.name?.trim() || !src?.path?.trim()} onclick={saveSrc}>Save</Button>{/snippet}
</Modal>

<Modal open={!!mapView} title={mapView ? mapView.root : ''} width={860} onclose={() => (mapView = null)}>
  <div class="scroll" style="max-height:60vh"><table class="t">
    <tbody>
      {#each mapRows as e (e.path)}
        <tr><td class="nowrap" class:hi={e.kind === 'dir'} style="padding-left:{8 + (e.path ? e.path.split('/').length - 1 : 0) * 12}px">{e.path ? e.path.split('/').pop() + (e.kind === 'dir' ? '/' : '') : '(this folder)'}</td><td class="dim">{e.summary}</td></tr>
      {/each}
    </tbody></table></div>
  {#snippet footer()}<Button variant="ghost" onclick={() => (mapView = null)}>Close</Button>{/snippet}
</Modal>

<Modal open={!!cmds} title="Checks for {cmds?.repo?.split('/').pop()}" width={560} onclose={() => (cmds = null)}>
  {#if cmds}
    <div class="sm mute">These commands run inside a workspace (build, then lint, then test; the first failure stops it). Leave a field empty to use what PRISM found in the repository (shown as the hint).</div>
    <Field label="Build"><Input bind:value={cmds.build} mono placeholder={cmds.detected_build || 'none found'} /></Field>
    <Field label="Lint"><Input bind:value={cmds.lint} mono placeholder={cmds.detected_lint || 'none found'} /></Field>
    <Field label="Test"><Input bind:value={cmds.test} mono placeholder={cmds.detected_test || 'none found'} /></Field>
  {/if}
  {#snippet footer()}<Button variant="ghost" onclick={() => (cmds = null)}>Cancel</Button><Button variant="primary" onclick={saveCmds}>Save</Button>{/snippet}
</Modal>

<Modal open={!!wsDiff} title="Changes in {wsDiff?.ws?.branch}" width={980} onclose={() => (wsDiff = null)}>
  {#if wsDiff}
    {#if wsDiff.ws.verify_status}<div class="chk"><Badge tone={checkTone(wsDiff.ws)}>checks: {wsDiff.ws.verify_status}{wsDiff.ws.stale ? ' · stale' : ''}</Badge><pre class="patch small">{wsDiff.ws.verify_output}</pre></div>{/if}
    {#if wsDiff.ws.review}<div class="chk"><Badge tone={verdictTone(wsDiff.ws.verdict)}>review: {wsDiff.ws.verdict || 'in progress'}</Badge><pre class="patch small wrap">{wsDiff.ws.review}</pre></div>{/if}
    <pre class="patch">{#each wsDiff.text.split('\n') as l}<span class={diffClass(l)}>{l}
</span>{/each}</pre>
  {/if}
  {#snippet footer()}
    {#if wsDiff}<Button variant="ghost" onclick={() => wsAct(wsDiff.ws, 'workspaces.discard', 'Discarded')}>Discard</Button>
    {#if wsDiff.ws.status === 'open'}<Button variant="ghost" onclick={() => wsAct(wsDiff.ws, 'workspaces.keep', 'Kept')}>Keep branch</Button>{/if}
    {#if wsDiff.ws.status === 'open'}<Button variant="ghost" onclick={() => runChecks(wsDiff.ws)}>Run checks</Button>{/if}
    <Button variant="accent" onclick={() => applyChecked(wsDiff.ws)}>Apply to my checkout</Button>{/if}
    <Button variant="primary" onclick={() => (wsDiff = null)}>Close</Button>
  {/snippet}
</Modal>

<style>
  .patch { margin: 0; font-size: 12px; line-height: 1.45; overflow: auto; max-height: 62vh; white-space: pre; }
  .chk { margin-bottom: 8px; }
  .patch.small { max-height: 160px; margin-top: 4px; }
  .patch.wrap { white-space: pre-wrap; }
  .patch .da { color: var(--ok); } .patch .dd { color: var(--err); } .patch .dh { color: var(--accent); } .patch .dm { color: var(--fg-mute); }
  .drop { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; border: 1px dashed var(--line-3); background: var(--bg-1); padding: 8px 12px; color: var(--fg-dim); flex: none; }
  .drop.over { border-color: var(--accent); background: var(--accent-bg); color: var(--accent-hi); }
  .ups { flex: none; padding: 2px 4px; display: flex; flex-direction: column; gap: 1px; }
  .pg { display: flex; flex-direction: column; gap: 6px; height: 100%; min-height: 0; }
  .bar { display: flex; align-items: center; gap: 8px; flex: none; }
  .f { width: 260px; }
  @media (max-width: 820px) { .f { width: 100%; } }
  .two { flex: 1; display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 6px; min-height: 0; }
  .res { flex: 1; display: flex; flex-direction: column; gap: 6px; }
  .hit { border-left: 2px solid var(--line-3); padding-left: 8px; }
  .proclog { max-height: 60vh; overflow: auto; white-space: pre-wrap; word-break: break-word; }
</style>
