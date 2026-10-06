<script>
  import { untrack } from 'svelte';
  import { FA } from '../lib/icons.js';
  import { S, call, listen, toast, confirmBox, refreshAll, skipOnboarding, loadModels, modelOptions, loadTools, go, activeRuns } from '../lib/store.svelte.js';
  import RunText from '../lib/RunText.svelte';
  import Logo from '../lib/ui/Logo.svelte';
  import Button from '../lib/ui/Button.svelte';
  import Input from '../lib/ui/Input.svelte';
  import Select from '../lib/ui/Select.svelte';
  import Field from '../lib/ui/Field.svelte';
  import Textarea from '../lib/ui/Textarea.svelte';
  import NumberInput from '../lib/ui/NumberInput.svelte';
  import Checkbox from '../lib/ui/Checkbox.svelte';
  import Tags from '../lib/ui/Tags.svelte';
  import MultiSelect from '../lib/ui/MultiSelect.svelte';
  import Badge from '../lib/ui/Badge.svelte';
  import Led from '../lib/ui/Led.svelte';
  import Empty from '../lib/ui/Empty.svelte';
  import Icon from '../lib/ui/Icon.svelte';
  import Segmented from '../lib/ui/Segmented.svelte';

  const steps = ['Database', 'Models', 'About you', 'Agents', 'Finish'];
  const setup = $derived(!!S.status?.setup);
  const regen = $derived(!!S.status?.onboarded);
  let step = $state(0);
  let started = false;
  $effect(() => {
    if (!S.status || started) return;
    started = true;
    step = S.status.setup ? 0 : S.status.onboarded ? 3 : 1;
  });

  // ── step 0: database ──
  let db = $state({ host: '127.0.0.1', port: 5432, user: 'postgres', password: '', database: 'prism' });
  let dbTest = $state(null);
  let dbBusy = $state('');
  $effect(() => { call('setup.info', {}, { quiet: true }).then((i) => i?.has_dsn && (dbTest = { note: `A saved connection exists (${i.dsn}) but the database is not reachable.` })); });
  async function testDb() {
    dbBusy = 'test'; dbTest = null;
    const r = await call('setup.test', db, { quiet: true, throw: true }).catch((e) => ({ error: e.message }));
    dbBusy = ''; dbTest = r;
  }
  async function connectDb() {
    dbBusy = 'connect';
    const r = await call('setup.connect', { ...db, create: true }, { quiet: true, throw: true }).catch((e) => ({ error: e.message }));
    dbBusy = '';
    if (r.error) { dbTest = { error: r.error }; return; }
    toast(r.created ? 'Database created and initialised' : 'Connected');
    await refreshAll();
    step = 1;
  }

  // ── start over ──
  let wipeArmed = $state(false);
  let wipeBusy = $state('');
  async function recreate(scope) {
    const all = scope === 'all';
    if (!(await confirmBox({ title: all ? 'Recreate everything' : 'Recreate database', ok: 'Delete', danger: true,
      text: all ? 'Everything is deleted: agents, tasks, memory, briefings, artifacts — and all API keys, providers, mail accounts and other integrations. You will set PRISM up again from the start.'
        : 'Agents, tasks, chats, memory, briefings, knowledge pages, trackers, artifacts and history are deleted. API keys and integrations are kept.' }))) return;
    wipeBusy = scope;
    const r = await call('onboarding.recreate', { scope });
    wipeBusy = '';
    if (!r) return;
    wipeArmed = false;
    toast(all ? 'Everything was reset' : 'Database recreated — API keys and integrations kept');
    await refreshAll();
    step = all ? 1 : 2;
  }

  // ── step 1: models ──
  let mk = $state({ kind: 'lmstudio', name: 'LM Studio', base_url: 'http://localhost:1234/v1', key: '' });
  const presets = { lmstudio: ['LM Studio', 'http://localhost:1234/v1'], openai: ['OpenAI', 'https://api.openai.com/v1'], gemini: ['Gemini', 'https://generativelanguage.googleapis.com/v1beta/openai'], grok: ['Grok', 'https://api.x.ai/v1'], openrouter: ['OpenRouter', 'https://openrouter.ai/api/v1'], custom: ['Custom', ''] };
  let found = $state([]);
  let sel = $state({});
  let provId = $state(0);
  let mBusy = $state('');
  let roles = $state({ chat: '', fast: '', embedding: '' });
  let rtest = $state({});
  let hasModels = $state(false);
  async function loadRoles() {
    await loadModels();
    roles = (await call('roles.get', {}, { quiet: true })) || roles;
    hasModels = !!S.models?.models?.length;
  }
  $effect(() => { if (!setup && step === 1) loadRoles(); });
  function pickKind(k) { mk.kind = k; mk.name = presets[k][0]; mk.base_url = presets[k][1]; }
  async function connectProvider() {
    mBusy = 'connect'; found = [];
    const pid = await call('providers.save', { id: 0, name: mk.name, kind: mk.kind, base_url: mk.base_url, enabled: true });
    if (!pid) { mBusy = ''; return; }
    provId = pid;
    if (mk.key.trim()) await call('keys.save', { provider_id: pid, label: 'main', api_key: mk.key.trim(), enabled: true, priority: 0 });
    const items = await call('providers.discover', { provider_id: pid });
    mBusy = '';
    if (!items) return;
    found = items;
    sel = {};
    items.filter((i) => i.loaded).forEach((i) => (sel[i.id] = true));
    if (!Object.values(sel).some(Boolean)) items.slice(0, 3).forEach((i) => (sel[i.id] = true));
  }
  async function importFound() {
    const items = found.filter((i) => sel[i.id]).map(({ id, kind, context, tools }) => ({ id, kind, context, tools }));
    const n = await call('models.import', { provider_id: provId, items });
    if (n === undefined) return;
    toast(`${n} models imported`);
    await loadRoles();
    // sensible defaults: first chat model for chat+fast, first embedding model for memory
    const chats = S.models.models.filter((m) => m.kind === 'chat'), embs = S.models.models.filter((m) => m.kind === 'embedding');
    const r = { chat: roles.chat || chats[0]?.name || '', fast: roles.fast || chats[chats.length > 1 ? 1 : 0]?.name || '', embedding: roles.embedding || embs[0]?.name || '' };
    roles = r; await call('roles.set', r);
    found = [];
  }
  async function setRole(k, v) { roles[k] = v || ''; await call('roles.set', $state.snapshot(roles)); }
  async function testRole(k) {
    rtest[k] = { busy: true };
    const res = await call('llm.test', { ref: 'role:' + k, kind: k === 'embedding' ? 'embedding' : 'chat' }, { quiet: true, throw: true }).catch((e) => ({ error: e.message }));
    rtest[k] = res.error ? { err: res.error } : { ok: k === 'embedding' ? `dim ${res.dim}` : `“${res.reply}” in ${(res.ms / 1000).toFixed(1)}s` };
  }
  const chatOpts = $derived([{ value: '', label: '(none)' }, ...modelOptions('chat')]);
  const embOpts = $derived([{ value: '', label: '(none — no semantic memory)' }, ...modelOptions('embedding')]);

  // ── step 2: about you ──
  let me = $state({ user_name: '', language: '', timezone: '', locale: '' });
  let hints = $state('');
  const zones = (() => { try { return Intl.supportedValuesOf('timeZone'); } catch { return []; } })();
  $effect(() => {
    if (step !== 2) return;
    untrack(() => {
      call('onboarding.state', {}, { quiet: true }).then((s) => {
        if (!s) return;
        me = { ...s.general, ...Object.fromEntries(Object.entries(me).filter(([, v]) => v)) };
        if (!me.timezone) { try { me.timezone = Intl.DateTimeFormat().resolvedOptions().timeZone; } catch {} }
        if (!hints) hints = s.hints || '';
      });
    });
  });
  async function saveMe() {
    await call('settings.set', { key: 'general', value: $state.snapshot(me) });
    step = 3;
  }

  // ── step 3: agents ──
  // The limits the generated team must respect. They go into the planner's prompt and are also enforced afterwards,
  // so a small model that ignores "at most 5 tools" is still held to it. Team size 0 = as many as the needs call for; with Max agents 0 too it is full auto and the model decides.
  const LIMITS_KEY = 'prism.onboardingLimits';
  const saved = (() => { try { return JSON.parse(localStorage.getItem(LIMITS_KEY)) || {}; } catch { return {}; } })();
  let count = $state(saved.count ?? 0);
  let maxAgents = $state(saved.max_agents ?? 0);
  let maxTools = $state(saved.max_tools ?? 8);
  let style = $state(saved.style ?? 'job');
  let allowDelegate = $state(saved.allow_delegate ?? false);
  let coverAll = $state(saved.cover_all ?? false);
  let genModel = $state('');
  // Optional: let an outside, stronger model (Codex CLI / a chat website on the user's own subscription, see Settings → Consult)
  // PLAN the team; every agent is still written on this machine.
  let planWith = $state('');
  let planOpts = $state([{ value: '', label: 'this machine (default)' }]);
  $effect(() => {
    call('consult.status', {}, { quiet: true }).then((c) => {
      if (!c) return;
      const o = [{ value: '', label: 'this machine (default)' }];
      if (c.codex?.found) o.push({ value: 'codex', label: 'Codex (your ChatGPT subscription)' });
      for (const st of c.sites || []) if (!st.disabled) o.push({ value: st.name, label: `${st.name} (website, in PRISM's browser)` });
      planOpts = o;
    });
  });
  let drafts = $state([]);
  let pick = $state({});
  let note = $state('');
  let gBusy = $state(false);
  let replace = $state(false);
  let open = $state(-1);
  let job = 0;
  let stageNote = $state('');
  // what the model is writing right now: the live runs of this generation (Forge, one per planning / soul call) —
  // or, in a window opened mid-way, the tail the server kept
  let thinkTail = $state('');
  const liveThinking = $derived(activeRuns().filter((r) => r.agent === 'Forge' && (r.title || '').startsWith('Onboarding')));
  $effect(() => { if (step === 3) loadTools(); });
  const toolOpts = $derived((S.tools || []).map((t) => ({ value: t.name, label: t.name, hint: t.category, badge: t.risk === 'exec' ? 'exec' : t.risk === 'write' ? 'write' : t.category?.startsWith('mcp:') ? 'mcp' : '' })));
  // connected MCP servers, shown so it's clear what "built-in templates" can fold tools from before generating
  let mcpServers = $state([]);
  $effect(() => { if (step === 3) call('mcp.list', {}, { quiet: true }).then((r) => (mcpServers = r || [])); });
  const mcpConnected = $derived(mcpServers.filter((s) => s.status?.state === 'connected'));
  function removeDraft(i) {
    drafts = drafts.filter((_, j) => j !== i);
    const np = {};
    Object.entries(pick).forEach(([k, v]) => { const ki = Number(k); if (ki < i) np[ki] = v; else if (ki > i) np[ki - 1] = v; });
    pick = np;
    if (open === i) open = -1; else if (open > i) open--;
  }
  $effect(() => listen('onboarding.progress', (e) => {
    if (!gBusy) return;
    if (!job) job = e.job; // the first event can outrun the RPC reply
    if (e.job !== job) return;
    if (e.note && e.stage !== 'finished') stageNote = e.note;
    if (e.draft) {
      const i = drafts.findIndex((d) => d.name === e.draft.name);
      if (i >= 0) drafts[i] = e.draft; else drafts.push(e.draft);
      pick[drafts.findIndex((d) => d.name === e.draft.name)] = !e.draft.exists;
    }
    if (e.stage === 'finished') {
      gBusy = false; stageNote = ''; job = 0;
      drafts = e.drafts || [];
      note = e.note || '';
      pick = {};
      drafts.forEach((d, i) => { if (!d.exists) pick[i] = true; });
    }
  }));
  // The generation runs in the server, so this window can be closed while a slow local model writes the team. A
  // reopened window picks the job up: still running (progress continues), or finished and waiting to be reviewed.
  let resumed = false;
  $effect(() => {
    if (resumed) return;
    resumed = true;
    call('onboarding.job', {}, { quiet: true }).then((j) => {
      if (!j) return;
      step = 3;
      drafts = j.drafts || [];
      pick = {};
      drafts.forEach((d, i) => { if (!d.exists) pick[i] = true; });
      if (j.finished) { note = j.error || (j.generated ? '' : 'Built-in templates.'); }
      else { gBusy = true; job = j.job; stageNote = j.note || 'Working…'; thinkTail = j.think || ''; }
    });
  });
  function inBackground() { toast('Generating in the background — you will be notified when the team draft is ready'); close(); }
  async function generate() {
    gBusy = true; note = ''; drafts = []; pick = {}; job = 0; stageNote = 'Starting…';
    const limits = { count, max_agents: maxAgents, max_tools: maxTools, style, allow_delegate: allowDelegate, cover_all: coverAll };
    try { localStorage.setItem(LIMITS_KEY, JSON.stringify(limits)); } catch {}
    const r = await call('onboarding.propose', { hints, ...limits, model: genModel, plan_with: planWith });
    if (!r) { gBusy = false; return; }
    if (!job) job = r.job;
  }
  async function templates() {
    const t = await call('onboarding.templates');
    if (t) { drafts = t; note = 'Built-in templates.'; pick = {}; t.forEach((d, i) => { if (!d.exists) pick[i] = true; }); }
  }
  async function apply() {
    const chosen = drafts.filter((_, i) => pick[i]);
    if (replace && !chosen.length) return toast('Nothing selected', 'warn');
    const n = await call('onboarding.apply', { drafts: chosen, replace });
    if (n === undefined) return;
    toast(`${n} agents created`);
    step = 4;
  }

  // ── step 4: finish ──
  async function finish() {
    await call('onboarding.finish', { hints });
    S.onboardingOpen = false;
    await refreshAll();
    go('chat');
    toast('Welcome to PRISM');
  }
  function close() { S.onboardingOpen = false; skipOnboarding(); }
</script>

<div class="ov">
  <div class="dlg" role="dialog" aria-label="PRISM setup">
    <header>
      <Logo size={30} /><span class="wm">PRISM</span><span class="sub">{regen ? 'agent profiles' : 'first-run setup'}</span>
      <span class="grow"></span>
      {#if !setup}<Button size="sm" variant="ghost" onclick={close}>{regen ? 'Close' : 'Skip for now'}</Button>{/if}
    </header>
    <div class="steps">
      {#each steps as s, i}
        <button class="st" class:on={step === i} class:done={step > i} disabled={setup && i > 0} onclick={() => (step = i)}><span class="n">{step > i ? '✓' : i + 1}</span>{s}</button>
      {/each}
    </div>

    <div class="body">
      {#if step === 0}
        <h2>PostgreSQL</h2>
        <p class="dim">PRISM keeps memory, tasks, agents and settings in PostgreSQL. Point it at a server; the <b class="hi">prism</b> database is created for you if it does not exist. pgvector is used automatically when the server has it.</p>
        <div class="grid">
          <Field label="Host"><Input bind:value={db.host} placeholder="127.0.0.1" /></Field>
          <Field label="Port"><NumberInput bind:value={db.port} min={1} max={65535} /></Field>
          <Field label="User"><Input bind:value={db.user} /></Field>
          <Field label="Password"><Input type="password" bind:value={db.password} /></Field>
          <Field label="Database"><Input bind:value={db.database} /></Field>
        </div>
        {#if dbTest?.error}<div class="err pre">✗ {dbTest.error}</div>{/if}
        {#if dbTest?.note}<div class="attn">{dbTest.note}</div>{/if}
        {#if dbTest?.server}
          <div class="ok-box">
            <div><Led state="ok" size={8} /> server {dbTest.server}</div>
            <div><Led state={dbTest.database_exists ? 'ok' : 'standby'} size={8} /> database “{dbTest.database}” {dbTest.database_exists ? 'exists' : 'will be created'}</div>
            <div><Led state={dbTest.pgvector_available ? 'ok' : 'off'} size={8} /> pgvector {dbTest.pgvector_available ? 'available' : 'not available (in-process similarity will be used)'}</div>
          </div>
        {/if}
        {#if !setup}
          <div class="danger">
            <Checkbox bind:checked={wipeArmed} label="Recreate database — start over from scratch" />
            {#if wipeArmed}
              <div class="sm dim">Drops everything PRISM produced — agents, tasks, chats, memory, briefings, knowledge pages, trackers, artifacts, logs and usage history — then runs setup again. Cancels anything that is running. <b class="hi">This cannot be undone.</b></div>
              <div class="row">
                <Button variant="danger" loading={wipeBusy === 'data'} disabled={!!wipeBusy} onclick={() => recreate('data')}>Recreate — keep API keys &amp; integrations</Button>
                <Button variant="danger" loading={wipeBusy === 'all'} disabled={!!wipeBusy} onclick={() => recreate('all')}>Recreate — drop API keys &amp; integrations too</Button>
              </div>
            {/if}
          </div>
        {/if}
        <div class="row end"><Button loading={dbBusy === 'test'} onclick={testDb}>Test connection</Button><Button variant="primary" loading={dbBusy === 'connect'} disabled={!db.host || !db.user} onclick={connectDb}>Create & connect</Button></div>

      {:else if step === 1}
        <h2>Models</h2>
        <p class="dim">Connect any OpenAI-compatible provider. Local models (LM Studio) need no key. You can add more providers, several keys per provider and fallback lists later in Settings.</p>
        {#if hasModels}
          <div class="roles">
            {#each [['chat', 'Chat model', chatOpts], ['fast', 'Fast model (summaries, extraction)', chatOpts], ['embedding', 'Embedding model', embOpts]] as [k, label, opts]}
              <Field {label}><Select value={roles[k]} options={opts} onchange={(v) => setRole(k, v)} searchable />
                <div class="row tst"><Button size="sm" loading={rtest[k]?.busy} disabled={!roles[k]} onclick={() => testRole(k)}>Test</Button>{#if rtest[k]?.ok}<span class="sm hi">✔ {rtest[k].ok}</span>{/if}{#if rtest[k]?.err}<span class="sm err">✗ {rtest[k].err}</span>{/if}</div></Field>
            {/each}
          </div>
          <hr />
          <div class="sm mute">Add another provider:</div>
        {/if}
        <div class="row wrap">
          {#each Object.keys(presets) as k}<Button size="sm" variant={mk.kind === k ? 'primary' : 'ghost'} onclick={() => pickKind(k)}>{presets[k][0]}</Button>{/each}
        </div>
        <div class="grid">
          <Field label="Name"><Input bind:value={mk.name} /></Field>
          <Field label="Base URL"><Input bind:value={mk.base_url} mono /></Field>
          {#if mk.kind !== 'lmstudio'}<Field label="API key"><Input type="password" bind:value={mk.key} /></Field>{/if}
        </div>
        <div class="row end"><Button variant="primary" loading={mBusy === 'connect'} disabled={!mk.base_url} onclick={connectProvider}>Connect & discover models</Button></div>
        {#if found.length}
          <div class="scroll list"><table class="t"><tbody>
            {#each found as i (i.id)}<tr><td><Checkbox bind:checked={sel[i.id]} /></td><td class="hi">{i.id}</td><td><Badge tone={i.kind === 'chat' ? 'ok' : 'accent'} w={10}>{i.kind}</Badge></td><td class="dim">{(i.context / 1024).toFixed(0)}k</td><td><Led state={i.loaded ? 'ok' : 'off'} size={8} title={i.loaded ? 'loaded' : 'not loaded'} /></td><td class="mute">{i.tools ? 'tools' : ''}</td></tr>{/each}
          </tbody></table></div>
          <div class="row end"><Button variant="primary" onclick={importFound} disabled={!Object.values(sel).some(Boolean)}>Import selected</Button></div>
        {/if}
        <div class="row end"><Button variant="primary" disabled={!hasModels} onclick={() => (step = 2)}>Next →</Button></div>

      {:else if step === 2}
        <h2>About you</h2>
        <p class="dim">Agents use this to address you and to understand what to build for you. The free text is distilled into memory facts.</p>
        <div class="grid">
          <Field label="Your name"><Input bind:value={me.user_name} /></Field>
          <Field label="Reply language" hint="e.g. Russian, English"><Input bind:value={me.language} /></Field>
          <Field label="Timezone"><Select bind:value={me.timezone} options={zones.map((z) => ({ value: z, label: z }))} searchable /></Field>
        </div>
        <Field label="Tell PRISM about yourself and what you want help with" hint="work, hobbies, tools you use, recurring tasks, things you want automated…">
          <Textarea bind:value={hints} rows={7} mono={false} placeholder="I run a small online shop and track competitor prices. I keep notes in Obsidian. I write Go and Python…" />
        </Field>
        <div class="row end"><Button variant="ghost" onclick={() => (step = 1)}>← Back</Button><Button variant="primary" onclick={saveMe}>Next →</Button></div>

      {:else if step === 3}
        <h2>Agents</h2>
        <p class="dim">Atlas (your point of contact) and the maintenance staff — Forge, Metis, Mnemosyne, Oneiros, Daedalus, Sentinel — already exist. Now create your specialists from what you told me.</p>
        {#if mcpConnected.length}
          <div class="mcpbox sm">
            <b class="hi">{mcpConnected.length} MCP server{mcpConnected.length === 1 ? '' : 's'} connected</b> — "Generate with the model" sees all of it directly; "Use built-in templates" folds each server's tools into whichever template fits, or drafts a new one when nothing does:
            <div class="mcprow">{#each mcpConnected as s}<Badge tone="accent">{s.name} · {s.status.tools?.length || 0} tools</Badge>{/each}</div>
          </div>
        {/if}
        {#if regen}<Field label="Hints"><Textarea bind:value={hints} rows={3} mono={false} /></Field>{/if}
        <div class="row wrap genrow">
          <Field label="Team size" hint="0 = auto"><NumberInput bind:value={count} min={0} max={100} /></Field>
          {#if count === 0}<Field label="Max agents" hint="0 = no cap: full auto"><NumberInput bind:value={maxAgents} min={0} max={30} /></Field>{/if}
          <Field label="Max tools per agent" hint="0 = no limit"><NumberInput bind:value={maxTools} min={0} max={40} /></Field>
          <Field label="Team style" hint={style === 'domain' ? 'one agent per area of your life or work, owning it end to end' : 'one agent per kind of work'}><Segmented bind:value={style} options={[{ value: 'job', label: 'By job' }, { value: 'domain', label: 'By domain' }]} /></Field>
          <Checkbox bind:checked={allowDelegate} label="allow agents to delegate" />
          <Checkbox bind:checked={coverAll} label="use every available tool — spread across agents, within the limit above (the common tools every agent gets are not counted)" />
          <div class="sm mute" style="flex-basis:100%">Limits are written into the planner's prompt and enforced afterwards: a plan that breaks one is sent back once with the exact violations, and whatever still breaks is trimmed. Agents cannot load tools later, so keep the limit generous enough for the job.</div>
          <div class="gm"><Field label="Model (fast = finishes sooner)"><Select bind:value={genModel} options={[{ value: '', label: 'chat model (default)' }, { value: 'role:fast', label: 'fast model' }, ...modelOptions('chat')]} /></Field></div>
          {#if planOpts.length > 1}<div class="gm"><Field label="Plan the team with" hint="an outside model plans who is on the team (slow — minutes; your hints and the tool list leave this machine, no secrets); each agent is still written here"><Select bind:value={planWith} options={planOpts} /></Field></div>{/if}
          <Button variant="primary" loading={gBusy} disabled={gBusy} onclick={generate}>Generate with the model</Button>
          <Button onclick={templates}>Use built-in templates</Button>
        </div>
        {#if regen}<Checkbox bind:checked={replace} label="overwrite existing — replace my current specialist agents with the selected ones (well-known agents stay)" />{/if}
        {#if gBusy && (liveThinking.length || thinkTail)}
          <div class="think scroll" title="what the model is writing right now">
            {#each liveThinking as r (r.id)}<div class="tk"><b class="hi">▸ {r.title.replace('Onboarding: ', '')}</b></div><div class="tx"><RunText text={r.buf} /></div>{:else}<div class="tx pre">{thinkTail}</div>{/each}
          </div>
        {/if}
        {#if gBusy}<div class="acc sm row wrap"><Led state="ok" pulse size={8} /> {stageNote} <span class="mute">— local models can take a few minutes; drafts appear as they are written</span><Button size="sm" variant="ghost" title="close this window; generation keeps running and you get a notification when the draft is ready" onclick={inBackground}>Run in background</Button></div>{/if}
        {#if note}<div class="attn sm">{note}</div>{/if}
        {#if drafts.length}
          <div class="drafts scroll">
            {#each drafts as d, i (i)}
              <div class="draft" class:exists={d.exists}>
                <div class="row">
                  {#if d.exists}<Badge tone="mute">exists</Badge>{:else}<Checkbox bind:checked={pick[i]} />{/if}
                  <span class="hi"><i class="fa">{FA[d.icon] || FA[d.icon_hint] || FA.robot}</i> {d.name}</span><Badge tone="accent">{d.group}</Badge><span class="dim sm grow ellipsis">{d.description}</span>
                  <Button size="sm" variant="ghost" onclick={() => (open = open === i ? -1 : i)}>{open === i ? 'hide' : 'edit'}</Button>
                  <Button size="sm" variant="ghost" title="remove from the list" onclick={() => removeDraft(i)}><Icon name="x" size={11} /></Button>
                </div>
                {#if open === i}
                  <div class="edrow">
                    <div class="two"><Field label="Name"><Input bind:value={d.name} /></Field><Field label="Group"><Input bind:value={d.group} /></Field></div>
                    <Field label="Description"><Input bind:value={d.description} /></Field>
                    <Field label="Traits"><Tags bind:value={d.traits} placeholder="add trait…" /></Field>
                    <Field label="Toolset"><MultiSelect bind:value={d.tools} options={toolOpts} placeholder="base tools only" /></Field>
                    <Field label="Soul"><Textarea bind:value={d.soul} rows={10} /></Field>
                  </div>
                {/if}
              </div>
            {/each}
          </div>
        {:else}
          <Empty>generate a team or start from templates</Empty>
        {/if}
        <div class="row end"><Button variant="ghost" onclick={() => (step = 2)}>← Back</Button><Button variant="ghost" onclick={() => (step = 4)}>Skip</Button><Button variant="primary" disabled={!Object.values(pick).some(Boolean)} onclick={apply}>Create selected</Button></div>

      {:else}
        <h2>Ready</h2>
        <p class="dim">PRISM is configured. A few things worth doing next (all under Settings → Integrations):</p>
        <ul class="next">
          <li><b class="hi">Telegram</b> — talk to Atlas from your phone; agents can drop messages into forum topics.</li>
          <li><b class="hi">Obsidian</b> — connect your vault (iCloud is detected automatically) for note search and writing.</li>
          <li><b class="hi">Web search keys</b> — DuckDuckGo works out of the box; Yandex, AnySearch and Tavily improve results.</li>
          <li><b class="hi">Tools</b> — tools start in SAFE mode (they ask before acting); arm the ones you trust.</li>
        </ul>
        <div class="row end"><Button variant="ghost" onclick={() => (step = 3)}>← Back</Button><Button variant="primary" onclick={finish}>Enter PRISM</Button></div>
      {/if}
    </div>
  </div>
</div>

<style>
  .ov { position: fixed; inset: 0; z-index: 2000; background: radial-gradient(ellipse at 50% 30%, color-mix(in srgb, rgb(var(--rgb-fg)) 10%, #000) 0%, color-mix(in srgb, rgb(var(--rgb-fg)) 2%, #000) 75%); display: flex; align-items: flex-start; justify-content: center; padding: 4vh 12px 12px; overflow: auto; }
  .dlg { width: min(880px, 100%); background: var(--bg-1); border: 1px solid var(--line-3); box-shadow: var(--glow), 0 24px 80px rgba(0, 0, 0, 0.8); display: flex; flex-direction: column; max-height: 92vh; }
  header { display: flex; align-items: center; gap: 12px; padding: 8px 12px; border-bottom: 1px solid var(--line-2); background: var(--bg-2); }
  .wm { font-weight: 700; letter-spacing: 0.34em; font-size: 17px; background: linear-gradient(90deg, var(--fg), var(--accent)); -webkit-background-clip: text; background-clip: text; color: transparent; }
  .sub { color: var(--fg-mute); text-transform: uppercase; letter-spacing: 0.14em; font-size: var(--fs-sm); }
  .steps { display: flex; border-bottom: 1px solid var(--line-2); }
  .st { flex: 1; background: none; border: 0; border-bottom: 2px solid transparent; padding: 6px 4px; color: var(--fg-mute); text-transform: uppercase; letter-spacing: 0.08em; font-size: var(--fs-sm); display: flex; align-items: center; justify-content: center; gap: 6px; }
  .st:disabled { opacity: 0.4; cursor: not-allowed; }
  .st .n { width: 17px; height: 17px; border: 1px solid currentColor; display: inline-flex; align-items: center; justify-content: center; font-size: 10px; border-radius: 50%; }
  .st.on { color: var(--fg-hi); border-bottom-color: var(--fg); text-shadow: var(--glow-sm); }
  .st.done { color: var(--fg-dim); }
  .body { padding: 14px 16px; display: flex; flex-direction: column; gap: 10px; overflow: auto; min-height: 0; }
  .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(190px, 100%), 1fr)); gap: 10px; }
  .ok-box { border: 1px solid var(--line-2); background: var(--bg); padding: 6px 10px; display: flex; flex-direction: column; gap: 3px; }
  .mcpbox { border: 1px solid var(--accent-dim); background: color-mix(in srgb, var(--accent) 6%, transparent); padding: 6px 10px; display: flex; flex-direction: column; gap: 5px; }
  .mcprow { display: flex; flex-wrap: wrap; gap: 6px; }
  .roles { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(240px, 100%), 1fr)); gap: 10px; }
  .tst { margin-top: 4px; }
  .list { max-height: 220px; border: 1px solid var(--line); }
  .drafts { display: flex; flex-direction: column; gap: 4px; max-height: 330px; }
  .draft { border: 1px solid var(--line); background: var(--bg); padding: 5px 8px; display: flex; flex-direction: column; gap: 4px; }
  .draft.exists { opacity: 0.55; }
  .draft pre { max-height: 220px; }
  .edrow { display: flex; flex-direction: column; gap: 8px; padding-top: 4px; border-top: 1px solid var(--line); }
  .edrow .two { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
  .acc { color: var(--accent-hi); }
  .gm { width: 260px; }
  .danger { border: 1px solid var(--err-dim); background: color-mix(in srgb, var(--err) 6%, transparent); padding: 8px 10px; display: flex; flex-direction: column; gap: 8px; }
  .genrow { align-items: flex-end; }
  .next { margin: 0; padding-left: 18px; display: flex; flex-direction: column; gap: 5px; color: var(--fg-dim); }
  .think { max-height: 190px; overflow: auto; margin: 6px 0; padding: 6px 8px; border: 1px solid var(--line-2); background: var(--bg); font-size: var(--fs-sm); color: var(--fg-dim); display: flex; flex-direction: column; gap: 2px; }
  .think .tx { white-space: pre-wrap; word-break: break-word; }
</style>
