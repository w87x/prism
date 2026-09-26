<script>
  import { S, call, listen, toast, confirmBox, loadTools, loadSkills, loadSetting, saveSetting } from '../lib/store.svelte.js';
  import Panel from '../lib/ui/Panel.svelte';
  import Tabs from '../lib/ui/Tabs.svelte';
  import Button from '../lib/ui/Button.svelte';
  import Input from '../lib/ui/Input.svelte';
  import Switch from '../lib/ui/Switch.svelte';
  import Segmented from '../lib/ui/Segmented.svelte';
  import Badge from '../lib/ui/Badge.svelte';
  import Led from '../lib/ui/Led.svelte';
  import Modal from '../lib/ui/Modal.svelte';
  import Field from '../lib/ui/Field.svelte';
  import Textarea from '../lib/ui/Textarea.svelte';
  import Tags from '../lib/ui/Tags.svelte';
  import RadioGroup from '../lib/ui/RadioGroup.svelte';
  import Checkbox from '../lib/ui/Checkbox.svelte';
  import Empty from '../lib/ui/Empty.svelte';
  import Icon from '../lib/ui/Icon.svelte';
  import { MCP_GALLERY } from '../lib/mcpGallery.js';

  let tab = $state('tools');
  let legendOpen = $state((() => { try { return localStorage.getItem('prism.toolsLegend') !== '0'; } catch { return true; } })());
  const toggleLegend = () => { legendOpen = !legendOpen; try { localStorage.setItem('prism.toolsLegend', legendOpen ? '1' : '0'); } catch {} };

  // ── tools ──
  let tools = $state([]);
  let filter = $state('');
  let master = $state(false);
  let ignoreTaint = $state(false);
  async function loadT() {
    tools = (await loadTools(true)) || [];
    const m = await call('tools.master', {}, { quiet: true });
    master = !!m?.armed;
    ignoreTaint = !!m?.ignore_taint;
  }
  async function setIgnoreTaint(on) {
    if (on && !(await confirmBox({ title: 'Stop asking after web content', text: 'With the master arm on, tools will ALSO run without asking after a web page, mail, calendar entry or MCP result has been read in the same task.\n\nThat is exactly how prompt injection works: a page can tell an agent to run a command. Approvals for sending mail, deleting calendar items and long speech stay.\n\nTurn this on only if you accept that risk.', ok: 'Stop asking', danger: true }))) return false;
    ignoreTaint = on;
    await call('tools.master', { set: true, armed: master, ignore_taint: on });
    loadT();
  }
  async function setMaster(on) {
    if (on && !(await confirmBox({ title: 'Master arm', text: 'Arm EVERY enabled tool?\nThey will run without asking, including shell, file writes and browser actions.\n\nOnly the untrusted-content rule still applies: after web/MCP content enters a turn, risky tools still ask.', ok: 'Arm all', danger: true }))) return false;
    master = on;
    await call('tools.master', { set: true, armed: on });
    if (!on) ignoreTaint = false;
    loadT();
  }
  $effect(() => { loadT(); });
  const filtered = $derived(tools.filter((t) => !filter || `${t.name} ${t.description} ${t.category}`.toLowerCase().includes(filter.toLowerCase())));
  const groups = $derived.by(() => {
    const m = {};
    filtered.forEach((t) => (m[t.category || 'other'] ||= []).push(t));
    return Object.entries(m).sort(([a], [b]) => a.localeCompare(b));
  });
  async function setTool(t, patch) {
    Object.assign(t, patch);
    await call('tools.set', { name: t.name, ...patch });
  }
  async function setGroup(items, patch) {
    items.forEach((t) => Object.assign(t, patch));
    await call('tools.set_many', { names: items.map((t) => t.name), ...patch });
  }
  const riskTone = { read: 'ok', write: 'attn', exec: 'err' };

  // ── MCP ──
  let servers = $state([]);
  let srvOpen = $state(false);
  let srv = $state(null);
  async function loadM() { servers = (await call('mcp.list', {}, { quiet: true })) || []; }
  $effect(() => { loadM(); return listen('mcp.update', () => { loadM(); loadT(); }); });
  function newServer() {
    srv = { id: 0, name: '', transport: 'stdio', command: '', args: [], envText: '', url: '', headersText: '', enabled: true, armed: false };
    srvOpen = true;
  }
  function editServer(s) {
    srv = { ...structuredClone($state.snapshot(s)), envText: Object.entries(s.env || {}).map(([k, v]) => `${k}=${v}`).join('\n'), headersText: Object.entries(s.headers || {}).map(([k, v]) => `${k}: ${v}`).join('\n') };
    srvOpen = true;
  }
  const parseKV = (t, sep) => Object.fromEntries(t.split('\n').map((l) => l.trim()).filter(Boolean).map((l) => { const i = l.indexOf(sep); return i < 0 ? [l, ''] : [l.slice(0, i).trim(), l.slice(i + sep.length).trim()]; }));
  async function saveServer() {
    const payload = { id: srv.id, name: srv.name.trim(), transport: srv.transport, command: srv.command.trim(), args: srv.args, env: parseKV(srv.envText, '='), url: srv.url.trim(), headers: parseKV(srv.headersText, ':'), enabled: srv.enabled, armed: srv.armed };
    if (await call('mcp.save', payload)) { srvOpen = false; toast('Server saved — connecting…'); setTimeout(loadM, 600); }
  }
  async function delServer(s) {
    if (await confirmBox({ title: 'Remove MCP server', text: `Remove ${s.name} and its ${s.status.tools?.length || 0} tools?`, ok: 'Remove', danger: true })) { await call('mcp.delete', { id: s.id }); loadM(); loadT(); }
  }
  const mcpLed = (st) => ({ connected: 'ok', connecting: 'warn', error: 'error', needs_auth: 'attention', disabled: 'off' })[st] || 'off';
  // ── sign-in (OAuth) and the starter gallery ──
  let galleryOpen = $state(false);
  let signLink = $state('');
  async function signIn(s) {
    const url = await call('mcp.oauth_start', { id: s.id, origin: location.origin });
    if (!url) return;
    signLink = url;
    const w = window.open(url, '_blank', 'noopener');
    if (w) { toast(`Finish signing in to ${s.name} in the new tab`); signLink = ''; }
  }
  async function signOut(s) {
    if (await confirmBox({ title: 'Sign out', text: `Forget the sign-in for ${s.name}?`, ok: 'Sign out', danger: true })) { await call('mcp.oauth_signout', { id: s.id }); loadM(); loadT(); }
  }
  function fromGallery(g) {
    srv = { id: 0, name: g.name, transport: g.transport, command: g.command || '', args: [...(g.args || [])], envText: '', url: g.url || '', headersText: g.headersText || '', enabled: true, armed: false };
    galleryOpen = false;
    srvOpen = true;
  }

  // ── skills ──
  let skills = $state([]);
  // ── runtime plugins: Python tools agents wrote; nothing runs until you have read and approved the code ──
  let plugins = $state([]);
  let plInfo = $state({ sandboxed: true });
  let plOpen = $state(false);
  let plView = $state(null);
  async function loadPl() { plugins = (await call('plugins.list', {}, { quiet: true })) || []; plInfo = (await call('plugins.info', {}, { quiet: true })) || plInfo; }
  $effect(() => { loadPl(); return listen('plugin.pending', loadPl); });
  async function viewPl(p) { plView = await call('plugins.get', { id: p.id }); plOpen = !!plView; }
  async function setPl(p, status) { if (await call('plugins.status', { id: p.id, status })) { toast(status === 'approved' ? `plugin_${p.name} approved` : status === 'disabled' ? `plugin_${p.name} switched off` : 'Updated'); plOpen = false; loadPl(); S.tools = null; } }
  async function delPl(p) { if (await confirmBox({ title: 'Delete plugin', text: `plugin_${p.name}`, ok: 'Delete', danger: true })) { await call('plugins.delete', { id: p.id }); plOpen = false; loadPl(); S.tools = null; } }
  const plTone = { approved: 'ok', pending: 'attn', disabled: 'mute' };
  let hubs = $state([]);
  let skOpen = $state(false);
  let sk = $state(null);
  let hubOpen = $state(false);
  let hub = $state({ id: 0, name: '', repo: '', path: '', ref: '', trusted: false });
  let searchOpen = $state(false);
  let sq = $state('');
  let shits = $state([]);
  let sbusy = $state(false);
  let ssel = $state({});
  let importingMany = $state(false);
  async function searchHubs() {
    sbusy = true; ssel = {};
    shits = (await call('skills.hub_search', { query: sq })) || [];
    sbusy = false;
  }
  async function importMany() {
    const picks = shits.filter((h) => ssel[h.hub_id + ':' + h.path]);
    importingMany = true;
    let n = 0;
    for (const h of picks) {
      const k = await call('skills.hub_import', { hub_id: h.hub_id, path: h.path, adapt: adaptOnImport });
      if (k) n++;
    }
    importingMany = false;
    toast(`${n} of ${picks.length} skills imported`);
    ssel = {};
    loadS();
  }
  let browseOpen = $state(false);
  let browsing = $state(null);
  let remote = $state([]);
  let remoteBusy = $state(false);
  let adaptOnImport = $state(true);
  let importing = $state('');
  let ghToken = $state('');
  async function loadS() { skills = (await loadSkills(true)) || []; hubs = (await call('skills.hubs', {}, { quiet: true })) || []; }
  $effect(() => { loadS(); loadSetting('skills', { github_token: '' }).then((c) => (ghToken = c.github_token)); });
  async function saveSkill() {
    if (await call('skills.save', $state.snapshot(sk))) { skOpen = false; toast('Skill saved'); loadS(); }
  }
  async function toggleSkill(k) { await call('skills.save', { ...$state.snapshot(k) }); }
  async function adapt(k) {
    toast(`Adapting ${k.name}…`);
    const r = await call('skills.adapt', { id: k.id });
    if (r) { toast(`${k.name} adapted for PRISM`); loadS(); if (skOpen) sk = r; }
  }
  async function revert(k) { const r = await call('skills.revert', { id: k.id }); if (r) { toast('Original restored'); loadS(); if (skOpen) sk = r; } }
  async function delSkill(k) {
    if (await confirmBox({ title: 'Delete skill', text: k.name, ok: 'Delete', danger: true })) { await call('skills.delete', { id: k.id }); loadS(); }
  }
  async function saveHub() {
    if (await call('skills.hub_save', hub)) { hubOpen = false; loadS(); }
  }
  async function browse(h) {
    browsing = h; remote = []; browseOpen = true; remoteBusy = true;
    remote = (await call('skills.hub_browse', { id: h.id })) || [];
    remoteBusy = false;
  }
  async function importSkill(r) {
    importing = r.path;
    const k = await call('skills.hub_import', { hub_id: browsing.id, path: r.path, adapt: adaptOnImport });
    importing = '';
    if (k) { toast(`Imported ${k.name}${k.adapted ? ' (adapted)' : ''}`); loadS(); }
  }
  async function saveToken() { await saveSetting('skills', { github_token: ghToken }, 'GitHub token saved'); }
  const installed = $derived(new Set(skills.map((k) => k.name)));
</script>

<div class="pg">
  <Tabs tabs={[{ id: 'tools', label: 'Tools', badge: tools.length }, { id: 'mcp', label: 'MCP servers', badge: servers.length }, { id: 'skills', label: 'Skills', badge: skills.length }, { id: 'plugins', label: 'Plugins', badge: plugins.filter((p) => p.status === 'pending').length || plugins.length || '' }]} bind:active={tab} />

  {#if tab === 'tools'}
    <div class="bar">
      <div class="f"><Input bind:value={filter} size="sm" placeholder="filter tools…" /></div>
      <Switch checked={master} tone="attn" label="MASTER ARM" onchange={setMaster} title="Arm every tool at once (auto-mode)" />
      {#if master}<Switch checked={ignoreTaint} tone="attn" label="…and don't ask after web content" onchange={setIgnoreTaint} title="Sub-agents inherit 'untrusted content' from the conversation, so tools still ask after any web search. Turn on to stop that (risky)." />{/if}
      <span class="grow"></span>
      <button type="button" class="lg" onclick={toggleLegend}>{legendOpen ? '▾' : '▸'} legend</button>
    </div>
    {#if legendOpen}
      <div class="legend sm mute"><b class="nmb">name</b> base tool · <b class="nme">name</b> returns external content · <i>name</i> deferred &nbsp;|&nbsp; <b class="hi">SAFE</b> asks before every run · <b class="hi">ARMED</b> runs on its own (still asks when untrusted web/MCP content is in the turn) · click a name for details</div>
    {/if}
    <Panel flush grow>
      <div class="scroll">
        <table class="t">
          <thead><tr><th>Tool</th><th>Risk</th><th>Description</th><th style="width:60px">On</th><th style="width:118px">Mode</th></tr></thead>
          <tbody>
            {#each groups as [cat, items]}
              <tr class="grp"><td colspan="3">{cat}</td>
                <td colspan="2" class="end"><Button size="sm" variant="ghost" onclick={() => setGroup(items, { enabled: true })}>all on</Button><Button size="sm" variant="ghost" onclick={() => setGroup(items, { enabled: false })}>all off</Button></td></tr>
              {#each items as t (t.name)}
                <tr class:off={!t.enabled} class:sel={S.selectedTool === t.name}>
                  <td class="nm nowrap" class:base={t.base} class:ext={t.untrusted} class:defer={t.deferred} onclick={() => (S.selectedTool = t.name)}
                    title={[t.base && 'Base tool: part of every agent\'s toolset.', t.untrusted && 'Returns content from outside (web, files of unknown origin, MCP). Using it taints the turn: risky tools then ask for confirmation. This is about the data it returns, not about the tool itself.', t.deferred && 'Deferred: its schema is only loaded on demand via tool_search.'].filter(Boolean).join('\n')}>{t.name}</td>
                  <td><Badge tone={riskTone[t.risk]} em={5}>{t.risk}</Badge></td>
                  <td class="dim desc" title={t.description}>{#if t.only?.length}<Badge tone="accent" title="only {t.only.join(', ')} can use this tool">only {t.only.join(', ')}</Badge> {/if}{t.description}</td>
                  <td><Switch checked={t.enabled} onchange={(v) => setTool(t, { enabled: v })} /></td>
                  <td>{#if t.risk === 'read' && false}{:else}<Segmented size="sm" value={t.armed ? 'armed' : 'safe'} disabled={!t.enabled || master}
                    options={[{ value: 'safe', label: 'safe', tone: 'accent' }, { value: 'armed', label: 'armed', tone: 'attn' }]} onchange={(v) => setTool(t, { armed: v === 'armed' })} />{/if}</td>
                </tr>
              {/each}
            {:else}
              <tr><td colspan="5"><Empty>no tools match</Empty></td></tr>
            {/each}
          </tbody>
        </table>
      </div>
    </Panel>
  {:else if tab === 'mcp'}
    <div class="bar"><span class="grow"></span><Button size="sm" variant="ghost" onclick={() => (galleryOpen = true)}>Starter gallery…</Button><Button size="sm" onclick={newServer}><Icon name="plus" size={11} /> Add MCP server</Button></div>
    {#if signLink}<div class="sm attn">Your browser blocked the sign-in tab: <a href={signLink} target="_blank" rel="noopener noreferrer">open the sign-in page</a></div>{/if}
    <div class="cards scroll">
      {#each servers as s (s.id)}
        <Panel title={s.name}>
          {#snippet right()}<Led state={mcpLed(s.status.state)} pulse={s.status.state === 'connecting'} size={8} /><span class="sm dim">{s.status.state === 'needs_auth' ? 'sign-in needed' : s.status.state}</span>{/snippet}
          <div class="sm mute ellipsis">{s.transport === 'http' ? s.url : `${s.command} ${(s.args || []).join(' ')}`}</div>
          {#if s.status.error}<div class="err sm pre">{s.status.error}</div>{/if}
          {#if s.status.tools?.length}<div class="sm dim pre">{s.status.tools.length} tools: {s.status.tools.map((n) => n.replace(/^mcp__[^_]+(?:_[^_]+)*__/, '')).join(', ')}</div>{/if}
          <div class="row wrap">
            <Switch bind:checked={s.enabled} label="enabled" onchange={() => call('mcp.save', $state.snapshot(s)).then(() => setTimeout(loadM, 500))} />
            <Switch bind:checked={s.armed} label="armed" tone="attn" title="tools of this server run without asking (still gated by untrusted-content rules)" onchange={() => call('mcp.save', $state.snapshot(s))} />
            <span class="grow"></span>
            {#if s.transport === 'http' && (s.status.state === 'needs_auth' || !s.signed_in && s.status.state !== 'connected' && s.status.state !== 'disabled')}<Button size="sm" variant="primary" onclick={() => signIn(s)}>Sign in</Button>{/if}
            {#if s.signed_in}<Button size="sm" variant="ghost" onclick={() => signOut(s)} title="Forget the stored sign-in">Sign out</Button>{/if}
            <Button size="sm" variant="ghost" onclick={() => call('mcp.reload', { id: s.id })}><Icon name="refresh" size={11} /> Reload</Button>
            <Button size="sm" variant="ghost" onclick={() => editServer(s)}>Edit</Button>
            <Button size="sm" variant="danger" onclick={() => delServer(s)}>Remove</Button>
          </div>
        </Panel>
      {:else}
        <Empty>no MCP servers — tools from MCP servers appear as deferred tools and load on demand</Empty>
      {/each}
    </div>
  {:else}
    <div class="two">
      <Panel title="Installed skills" grow flush>
        {#snippet right()}<Button size="sm" variant="ghost" onclick={() => { sk = { id: 0, name: '', description: '', body: '---\nname: my-skill\ndescription: what it does and when to use it\n---\n\n# Steps\n1. ', source: 'local', enabled: true, adapted: false }; skOpen = true; }}><Icon name="plus" size={11} /> New</Button>{/snippet}
        <div class="scroll">
          <table class="t">
            <thead><tr><th>Skill</th><th>Description</th><th style="width:50px">On</th><th style="width:168px"></th></tr></thead>
            <tbody>
              {#each skills as k (k.id)}
                <tr>
                  <td class="hi nowrap">{k.name} <Badge tone="mute">{k.source}</Badge>{#if k.adapted}<Badge tone="accent">adapted</Badge>{/if}</td>
                  <td class="dim desc">{k.description}</td>
                  <td><Switch bind:checked={k.enabled} onchange={() => toggleSkill(k)} /></td>
                  <td class="end nowrap">
                    <Button size="sm" variant="ghost" onclick={async () => { sk = await call('skills.get', { id: k.id }); skOpen = true; }}>Edit</Button>
                    {#if k.adapted}<Button size="sm" variant="ghost" onclick={() => revert(k)}>Revert</Button>{:else}<Button size="sm" variant="ghost" onclick={() => adapt(k)}>Adapt</Button>{/if}
                    <Button size="sm" variant="ghost" onclick={() => delSkill(k)}><Icon name="trash" size={11} /></Button>
                  </td>
                </tr>
              {:else}
                <tr><td colspan="4"><Empty>no skills — import from a hub or write your own</Empty></td></tr>
              {/each}
            </tbody>
          </table>
        </div>
      </Panel>
      <Panel title="Skill hubs" grow>
        {#snippet right()}<Button size="sm" variant="ghost" onclick={() => { searchOpen = true; if (!shits.length) searchHubs(); }}><Icon name="link" size={11} /> Search all hubs</Button><Button size="sm" variant="ghost" onclick={() => { hub = { id: 0, name: '', repo: '', path: '', ref: '', trusted: false }; hubOpen = true; }}><Icon name="plus" size={11} /> Hub</Button>{/snippet}
        <div class="sm mute">A hub is a GitHub repository with skills (folders containing SKILL.md). Import copies a skill locally; <b class="hi">adapt</b> rewrites it for PRISM (no mention of other agent platforms, tool names mapped).</div>
        {#each hubs as h (h.id)}
          <div class="hub">
            <div class="grow"><div class="hi">{h.name}{#if h.trusted}<Badge tone="ok" title="agents may search and install from this hub on their own">trusted</Badge>{/if}</div><div class="sm mute">{h.repo}{h.path ? ' / ' + h.path : ''}{h.ref ? ' @ ' + h.ref : ''}</div></div>
            <Button size="sm" variant="primary" onclick={() => browse(h)}>Browse</Button>
            <Button size="sm" variant="ghost" onclick={() => { hub = { ...h }; hubOpen = true; }}>Edit</Button>
            <Button size="sm" variant="ghost" onclick={async () => { if (await confirmBox({ title: 'Remove hub', text: h.name, ok: 'Remove', danger: true })) { await call('skills.hub_delete', { id: h.id }); loadS(); } }}><Icon name="trash" size={11} /></Button>
          </div>
        {:else}
          <Empty>no hubs configured</Empty>
        {/each}
        <Field label="GitHub token (optional)" hint="raises the API rate limit from 60 to 5000 requests/hour">
          <div class="row"><div class="grow"><Input type="password" bind:value={ghToken} placeholder="ghp_…" /></div><Button size="sm" onclick={saveToken}>Save</Button></div>
        </Field>
      </Panel>
    </div>
  {:else if tab === 'plugins'}
    <div class="bar"><span class="sm mute">Agents can write small Python tools when nothing else fits. Each one waits here until you read its code and approve it; it then runs offline in a sandbox (no network unless it says so, no writes outside its own folder, no access to your secrets).</span></div>
    {#if !plInfo.sandboxed}<div class="sm err">The macOS sandbox is not available on this machine, so plugins are switched off.</div>{/if}
    <Panel flush grow>
      <div class="scroll"><table class="t">
        <thead><tr><th>Plugin</th><th>What it does</th><th>Access</th><th>By</th><th>Status</th><th></th></tr></thead>
        <tbody>
          {#each plugins as p (p.id)}
            <tr class="click" onclick={() => viewPl(p)}>
              <td class="hi">plugin_{p.name}</td><td class="dim">{p.description}</td>
              <td>{#if p.network}<Badge tone="attn">network</Badge>{:else}<Badge tone="mute">offline</Badge>{/if}</td>
              <td class="dim">{p.created_by || '—'}</td><td><Badge tone={plTone[p.status]}>{p.status}</Badge></td>
              <td class="end nowrap"><Button size="sm" variant={p.status === 'pending' ? 'primary' : 'ghost'}>{p.status === 'pending' ? 'Review' : 'View'}</Button></td>
            </tr>
          {:else}<tr><td colspan="6"><Empty>no plugins yet — an agent proposes one when it needs a tool that does not exist</Empty></td></tr>{/each}
        </tbody>
      </table></div>
    </Panel>
  {/if}
</div>

<Modal bind:open={plOpen} title={plView ? `plugin_${plView.name}` : ''} width={860}>
  {#if plView}
    <div class="sm dim">{plView.description}</div>
    <div class="row"><Badge tone={plTone[plView.status]}>{plView.status}</Badge>{#if plView.network}<Badge tone="attn">needs network access</Badge>{:else}<Badge tone="mute">offline</Badge>{/if}<span class="sm mute">written by {plView.created_by || 'unknown'} · {plView.timeout_s}s limit · sha256 {plView.hash.slice(0, 12)}…</span></div>
    <Field label="Arguments (JSON schema)"><pre class="code">{JSON.stringify(plView.params, null, 2)}</pre></Field>
    <Field label="Code — this exact text is what runs"><pre class="code">{plView.code}</pre></Field>
  {/if}
  {#snippet footer()}
    {#if plView}
      <Button variant="danger" onclick={() => delPl(plView)}>Delete</Button>
      {#if plView.status === 'approved'}<Button variant="ghost" onclick={() => setPl(plView, 'disabled')}>Switch off</Button>
      {:else}<Button variant="primary" onclick={() => setPl(plView, 'approved')}>{plView.status === 'pending' ? 'I have read it — approve' : 'Switch on'}</Button>{/if}
    {/if}
  {/snippet}
</Modal>

<Modal bind:open={galleryOpen} title="MCP starter gallery" width={780}>
  <div class="sm mute">Picking one only fills in the form — nothing runs until you save. Addresses follow each project's docs and may change.</div>
  <div class="gal">
    {#each MCP_GALLERY as g}
      <button type="button" class="gcard" onclick={() => fromGallery(g)}>
        <span class="hi">{g.title}</span> <Badge tone={g.tag === 'sign-in' ? 'accent' : g.tag === 'token' ? 'attn' : 'mute'}>{g.tag}</Badge>
        <span class="dim sm">{g.text}</span>
        <span class="mute sm">needs: {g.needs}</span>
      </button>
    {/each}
  </div>
</Modal>

<Modal bind:open={srvOpen} title={srv?.id ? 'Edit MCP server' : 'Add MCP server'} width={640}>
  {#if srv}
    <Field label="Name" hint="tools become mcp__<name>__<tool>"><Input bind:value={srv.name} placeholder="filesystem" /></Field>
    <Field label="Transport"><RadioGroup inline bind:value={srv.transport} options={[{ value: 'stdio', label: 'stdio (local command)' }, { value: 'http', label: 'streamable HTTP' }]} /></Field>
    {#if srv.transport === 'stdio'}
      <Field label="Command"><Input bind:value={srv.command} mono placeholder="npx" /></Field>
      <Field label="Arguments" hint="one per tag, e.g. -y, @modelcontextprotocol/server-filesystem, ~/Documents"><Tags bind:value={srv.args} placeholder="add argument…" /></Field>
      <Field label="Environment" hint="KEY=value per line"><Textarea bind:value={srv.envText} rows={3} /></Field>
    {:else}
      <Field label="URL"><Input bind:value={srv.url} placeholder="https://host/mcp" /></Field>
      <Field label="Headers" hint="Name: value per line (e.g. Authorization: Bearer …)"><Textarea bind:value={srv.headersText} rows={3} /></Field>
    {/if}
    <div class="row gap-12"><Switch bind:checked={srv.enabled} label="enabled" /><Switch bind:checked={srv.armed} label="armed" tone="attn" /></div>
  {/if}
  {#snippet footer()}<Button variant="ghost" onclick={() => (srvOpen = false)}>Cancel</Button><Button variant="primary" disabled={!srv?.name?.trim()} onclick={saveServer}>Save & connect</Button>{/snippet}
</Modal>

<Modal bind:open={skOpen} title={sk?.id ? `Skill · ${sk.name}` : 'New skill'} width={820}>
  {#if sk}
    <div class="row gap-12"><div class="grow"><Field label="Name"><Input bind:value={sk.name} disabled={!!sk.id} /></Field></div><Switch bind:checked={sk.enabled} label="enabled" /></div>
    <Field label="Description" hint="the only part agents see until they load the skill"><Input bind:value={sk.description} /></Field>
    <Field label="SKILL.md"><Textarea bind:value={sk.body} rows={20} /></Field>
    {#if sk.files?.length}<div class="sm mute">bundled files: {sk.files.join(', ')}</div>{/if}
    {#if sk.original}<div class="sm mute">An original (pre-adaptation) copy is stored; use Revert to restore it.</div>{/if}
  {/if}
  {#snippet footer()}
    {#if sk?.id}<Button variant="accent" onclick={() => adapt(sk)}>Adapt for PRISM</Button>{/if}
    <Button variant="ghost" onclick={() => (skOpen = false)}>Cancel</Button><Button variant="primary" onclick={saveSkill}>Save</Button>
  {/snippet}
</Modal>

<Modal bind:open={hubOpen} title={hub.id ? 'Edit hub' : 'Add skill hub'} width={520}>
  <Field label="Repository" hint="owner/repo or a github.com URL"><Input bind:value={hub.repo} placeholder="owner/skills-repo" /></Field>
  <Field label="Name"><Input bind:value={hub.name} placeholder="(defaults to the repo)" /></Field>
  <div class="row"><div class="grow"><Field label="Sub-folder"><Input bind:value={hub.path} placeholder="skills" /></Field></div><div class="grow"><Field label="Branch / tag"><Input bind:value={hub.ref} placeholder="default" /></Field></div></div>
  <Switch bind:checked={hub.trusted} label="trusted hub — agents may search it and install (adapted) skills on their own" tone="attn" />
  {#snippet footer()}<Button variant="ghost" onclick={() => (hubOpen = false)}>Cancel</Button><Button variant="primary" disabled={!hub.repo.trim()} onclick={saveHub}>Save</Button>{/snippet}
</Modal>

<Modal bind:open={searchOpen} title="Search all skill hubs" width={900}>
  <div class="row"><div class="grow"><Input bind:value={sq} placeholder="what should the skill do? (empty = list everything)" onenter={searchHubs} /></div><Button variant="primary" loading={sbusy} onclick={searchHubs}>Search</Button></div>
  <Checkbox bind:checked={adaptOnImport} label="adapt for PRISM while importing (removes other platforms' names, maps tools)" />
  <div class="scroll rl">
    <table class="t"><tbody>
      {#each shits as h (h.hub_id + ':' + h.path)}
        <tr>
          <td>{#if installed.has(h.name.toLowerCase().replace(/[^a-z0-9._-]+/g, '-'))}<Badge tone="ok" w={9}>installed</Badge>{:else}<Checkbox bind:checked={ssel[h.hub_id + ':' + h.path]} />{/if}</td>
          <td class="hi nowrap">{h.name}</td><td class="dim">{h.description}</td><td class="mute nowrap">{h.hub}{#if h.trusted} ✓{/if}</td>
        </tr>
      {:else}<tr><td><Empty>{sbusy ? 'reading hubs…' : 'nothing found — add a hub first'}</Empty></td></tr>{/each}
    </tbody></table>
  </div>
  {#snippet footer()}<Button variant="ghost" onclick={() => (searchOpen = false)}>Close</Button><Button variant="primary" loading={importingMany} disabled={!Object.values(ssel).some(Boolean)} onclick={importMany}>Import selected ({Object.values(ssel).filter(Boolean).length})</Button>{/snippet}
</Modal>

<Modal bind:open={browseOpen} title="Browse · {browsing?.name || ''}" width={820}>
  <Checkbox bind:checked={adaptOnImport} label="adapt for PRISM while importing (uses the chat model, ~a minute per skill)" />
  {#if remoteBusy}<Empty>reading repository…</Empty>{/if}
  <div class="scroll rl">
    <table class="t">
      <tbody>
        {#each remote as r (r.path)}
          <tr>
            <td class="hi nowrap">{r.name}</td><td class="dim">{r.description}</td>
            <td class="end">{#if installed.has(r.name.toLowerCase().replace(/[^a-z0-9._-]+/g, '-'))}<Badge tone="ok">installed</Badge>{:else}<Button size="sm" variant="primary" loading={importing === r.path} disabled={!!importing} onclick={() => importSkill(r)}>Import</Button>{/if}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
</Modal>

<style>
  .gal { display: grid; grid-template-columns: repeat(auto-fill, minmax(min(240px, 100%), 1fr)); gap: 8px; margin-top: 8px; }
  .gcard { display: flex; flex-direction: column; gap: 3px; text-align: left; background: var(--bg-1); border: 1px solid var(--line-2); padding: 8px 10px; color: inherit; }
  .gcard:hover { border-color: var(--fg); background: var(--bg-2); }
  .code { margin: 0; padding: 8px 10px; background: var(--bg); border: 1px solid var(--line); max-height: 44vh; overflow: auto; white-space: pre; font-size: 12px; line-height: 1.5; color: var(--fg-hi); }
  .pg { display: flex; flex-direction: column; gap: 6px; height: 100%; min-height: 0; }
  .bar { display: flex; align-items: center; gap: 8px; flex: none; }
  .f { width: 260px; }
  @media (max-width: 820px) { .f { width: 100%; } }
  .desc { max-width: 0; width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  tr.off td { opacity: 0.45; }
  td.nm { padding-left: 22px; color: var(--fg-hi); cursor: pointer; }
  td.nm:hover { text-decoration: underline; text-underline-offset: 2px; }
  tr.sel td { background: var(--bg-3); }
  .lg { background: none; border: 0; color: var(--fg-mute); font-size: var(--fs-sm); text-transform: uppercase; letter-spacing: 0.08em; padding: 0 4px; } .lg:hover { color: var(--fg); }
  .legend { padding: 3px 8px; border: 1px dashed var(--line); flex: none; }
  td.nm.base { color: var(--accent-hi); }
  td.nm.ext { color: var(--attn-hi); }
  td.nm.defer { font-style: italic; }
  .legend .nmb { color: var(--accent-hi); font-weight: 500; } .legend .nme { color: var(--attn-hi); font-weight: 500; } .legend i { color: var(--fg-dim); }
  tr.grp td { background: var(--bg-2); color: var(--fg-hi); text-transform: uppercase; letter-spacing: 0.1em; font-size: var(--fs-sm); padding-top: 4px; }
  .cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(min(340px, 100%), 1fr)); gap: 8px; align-content: start; flex: 1; }
  .two { flex: 1; display: grid; grid-template-columns: minmax(0, 1.5fr) minmax(0, 1fr); gap: 6px; min-height: 0; }
  .hub { display: flex; align-items: center; gap: 6px; padding: 4px 0; border-bottom: 1px dotted var(--line); }
  .rl { max-height: 55vh; border: 1px solid var(--line); }
</style>
