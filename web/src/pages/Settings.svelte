<script>
  import { untrack } from 'svelte';
  import { S, call, listen, toast, confirmBox, loadSetting, saveSetting, loadModels, modelOptions, reopenOnboarding, ago, stamp } from '../lib/store.svelte.js';
  import Panel from '../lib/ui/Panel.svelte';
  import Tabs from '../lib/ui/Tabs.svelte';
  import Button from '../lib/ui/Button.svelte';
  import Input from '../lib/ui/Input.svelte';
  import Select from '../lib/ui/Select.svelte';
  import Switch from '../lib/ui/Switch.svelte';
  import Badge from '../lib/ui/Badge.svelte';
  import Led from '../lib/ui/Led.svelte';
  import Modal from '../lib/ui/Modal.svelte';
  import Field from '../lib/ui/Field.svelte';
  import Textarea from '../lib/ui/Textarea.svelte';
  import NumberInput from '../lib/ui/NumberInput.svelte';
  import Checkbox from '../lib/ui/Checkbox.svelte';
  import Tags from '../lib/ui/Tags.svelte';
  import OrderedList from '../lib/ui/OrderedList.svelte';
  import Empty from '../lib/ui/Empty.svelte';
  import Icon from '../lib/ui/Icon.svelte';
  import Bar from '../lib/ui/Bar.svelte';

  let tab = $state('general');

  // ── general ──
  let gen = $state({ user_name: '', timezone: '', language: '', locale: '' });
  let ctx = $state({ compact_at: 0.8, target: 0.2 });
  let mem = $state({ process_every_s: 300, raw_batch: 40, reflect_off: false, reflect_after: 0, auto_merge_off: false, hints: '', process_min: 0, entities_off: false, entities_after: 0, analyze_off: false, analyze_after: 0, synth_off: false, synth_after: 0, bookmarks_off: false, digest_off: false, digest_days: 0, verify_off: false, auto_ingest_off: false });
  let rt = $state({ llm_concurrency: 4 });
  let el = $state({ api_key: '', voice_id: '', model_id: 'eleven_flash_v2_5', monthly_cap: 8000, confirm_over: 600, image_model: 'gemini-2.5-flash-image' });
  let els = $state(null); // plan and credits
  let voices = $state([]);
  let cal = $state(null); // result of the Calendar & Reminders access check
  let calBusy = $state(false);
  let shortcuts = $state(null);
  // ── mail ──
  let mails = $state([]);
  let mailForm = $state(null); // account being edited
  let mailOpen = $state(false);
  let mailTest = $state(null);
  let mailBusy = $state(false);
  let him = $state({ installed: false, accounts: [] });
  const secOpts = [{ value: 'tls', label: 'TLS (implicit)' }, { value: 'starttls', label: 'STARTTLS' }, { value: 'none', label: 'none (local only)' }];
  const blankMail = () => ({ id: 0, tag: '', backend: him.installed ? 'himalaya' : 'imap', enabled: true, himalaya: '', imap_host: '', imap_port: 0, imap_security: 'tls', user: '', password: '', has_password: false,
    smtp_host: '', smtp_port: 0, smtp_security: 'tls', smtp_user: '', smtp_password: '', from: '', inbox: '', drafts: '', sent: '' });
  async function loadMail() {
    mails = (await call('mail.accounts', {}, { quiet: true })) || [];
    him = (await call('mail.himalaya', {}, { quiet: true })) || him;
  }
  function editMail(a) { mailForm = a ? structuredClone($state.snapshot(a)) : blankMail(); mailTest = null; mailOpen = true; }
  async function saveMail() { if (await call('mail.save', $state.snapshot(mailForm))) { mailOpen = false; toast('Mail account saved'); loadMail(); } }
  async function testMail(a) {
    mailBusy = true; mailTest = null;
    mailTest = await call('mail.test', $state.snapshot(a), { quiet: true });
    mailBusy = false;
  }
  async function delMail(a) { if (await confirmBox({ title: 'Remove mail account', text: `Remove “${a.tag}”? Mail on the server is not touched.`, ok: 'Remove', danger: true })) { await call('mail.delete', { id: a.id }); loadMail(); } }
  // typing a server name in the IMAP box fills sensible defaults for the rest
  function guessSmtp() { if (mailForm && !mailForm.smtp_host && mailForm.imap_host) mailForm.smtp_host = mailForm.imap_host.replace(/^imap\./, 'smtp.'); }
  let tools = $state({ enabled: true, use_llm: false, max: 4 });
  let ret = $state({ logs_days: 14, tasks_days: 30, task_statuses: ['done', 'failed', 'cancelled'] });
  const retStatuses = ['done', 'failed', 'cancelled', 'partial'];
  const toggleRetStatus = (s, on) => { const cur = new Set(ret.task_statuses || []); on ? cur.add(s) : cur.delete(s); ret.task_statuses = [...cur]; };
  let gr = $state({ code_review_off: false, tool_repeat_warn: 3, tool_repeat_abort: 5, text_repeat_abort: 2, autonomous_boost_pct: 50, autonomous_max_iterations: 60 });
  let nf = $state({ cron: { show: true, external: false }, intent: { show: true, external: true }, ask: { show: true, external: false }, error: { show: true, external: true }, proposal: { show: true, external: true } });
  let cleaning = $state(false);
  async function cleanNow() {
    cleaning = true;
    const r = await call('maintenance.cleanup');
    cleaning = false;
    if (r) toast(`Removed ${r.logs} log entries, ${r.tasks} tasks, ${r.sessions} sessions`);
  }
  const zones = (() => { try { return Intl.supportedValuesOf('timeZone'); } catch { return ['UTC']; } })();
  const zoneOpts = [{ value: '', label: '(system)' }, ...zones.map((z) => ({ value: z, label: z }))];
  // Effects must not read the state they later assign, or they re-run forever: untrack the loads.
  $effect(() => {
    untrack(() => {
      loadSetting('general', gen).then((v) => (gen = v));
      loadSetting('context', ctx).then((v) => (ctx = v));
      loadSetting('memory', mem).then((v) => (mem = v));
      loadSetting('runtime', rt).then((v) => (rt = v));
      loadSetting('elevenlabs', el).then((v) => (el = v));
      loadSetting('tool_selector', tools).then((v) => (tools = v));
      loadSetting('retention', ret).then((v) => (ret = v));
      loadSetting('guardrails', gr).then((v) => (gr = v));
      loadSetting('notifications', nf).then((v) => (nf = v));
    });
  });

  // ── models ──
  let provs = $state([]);
  let presets = $state({});
  let models = $state([]);
  let lists = $state([]);
  let roles = $state({ chat: '', fast: '', embedding: '' });
  let testing = $state({});
  const kinds = ['openai', 'lmstudio', 'gemini', 'grok', 'openrouter', 'custom'];
  async function loadLLM() {
    const p = await call('providers.list', {}, { quiet: true });
    if (p) { provs = p.providers || []; presets = p.presets || {}; provs.forEach((x) => { newKey[x.id] ||= { label: '', api_key: '' }; }); }
    models = (await call('models.list', {}, { quiet: true })) || [];
    lists = (await call('lists.list', {}, { quiet: true })) || [];
    roles = (await call('roles.get', {}, { quiet: true })) || roles;
    loadModels();
  }
  $effect(() => { loadLLM(); });
  const provName = (id) => provs.find((p) => p.id === id)?.name || '?';
  const chatOpts = $derived([{ value: '', label: '(first available)' }, ...modelOptions('chat')]);
  const embOpts = $derived([{ value: '', label: '(none — lexical search only)' }, ...modelOptions('embedding')]);

  async function setRole(r, v) { roles[r] = v || ''; await call('roles.set', $state.snapshot(roles)); loadModels(); }
  async function testRole(r) {
    testing[r] = { busy: true };
    const res = await call('llm.test', { ref: 'role:' + r, kind: r === 'embedding' ? 'embedding' : 'chat' }, { quiet: true, throw: true }).catch((e) => ({ error: e.message }));
    testing[r] = res.error ? { err: res.error } : { ok: r === 'embedding' ? `dim ${res.dim} · ${res.ms} ms` : `${res.model} → “${res.reply}” · ${res.ms} ms` };
  }

  let pOpen = $state(false);
  let prov = $state(null);
  let newKey = $state({});
  async function saveProv() { if (await call('providers.save', $state.snapshot(prov))) { pOpen = false; loadLLM(); } }
  async function delProv(p) { if (await confirmBox({ title: 'Delete provider', text: `${p.name} with its keys and models?`, ok: 'Delete', danger: true })) { await call('providers.delete', { id: p.id }); loadLLM(); } }
  async function addKey(p) {
    const k = newKey[p.id] || {};
    if (!k.api_key && p.kind !== 'lmstudio' && p.kind !== 'custom') return toast('Enter a key', 'warn');
    if (await call('keys.save', { provider_id: p.id, label: k.label || '', api_key: k.api_key || '', enabled: true, priority: p.keys?.length || 0 })) { newKey[p.id] = { label: '', api_key: '' }; loadLLM(); }
  }
  async function saveKey(k) { await call('keys.save', { id: k.id, provider_id: k.provider_id, label: k.label, api_key: '', enabled: k.enabled, priority: k.priority }); loadLLM(); }
  async function delKey(k) { await call('keys.delete', { id: k.id }); loadLLM(); }

  // discover
  let dOpen = $state(false);
  let disc = $state({ provider: null, items: [], busy: false, sel: {} });
  async function discover(p) {
    disc = { provider: p, items: [], busy: true, sel: {} };
    dOpen = true;
    const items = await call('providers.discover', { provider_id: p.id });
    disc.busy = false;
    if (!items) { dOpen = false; return; }
    const have = new Set(models.map((m) => m.name));
    disc.items = items.map((i) => ({ ...i, have: have.has(i.id) }));
    disc.items.filter((i) => !i.have && i.loaded).forEach((i) => (disc.sel[i.id] = true)); // preselect what is already loaded
  }
  async function importSel() {
    const items = disc.items.filter((i) => disc.sel[i.id] && !i.have).map(({ id, kind, context, tools }) => ({ id, kind, context, tools }));
    const n = await call('models.import', { provider_id: disc.provider.id, items });
    if (n !== undefined) { toast(`${n} models imported`); dOpen = false; loadLLM(); }
  }

  // models
  let mOpen = $state(false);
  let mdl = $state(null);
  async function saveModel() { if (await call('models.save', $state.snapshot(mdl))) { mOpen = false; loadLLM(); } }
  async function delModel(m) { if (await confirmBox({ title: 'Delete model', text: m.name, ok: 'Delete', danger: true })) { await call('models.delete', { id: m.id }); loadLLM(); } }

  // lists
  let lOpen = $state(false);
  let lst = $state(null);
  async function saveList() { if (await call('lists.save', $state.snapshot(lst))) { lOpen = false; loadLLM(); } }
  async function delList(l) { if (await confirmBox({ title: 'Delete list', text: l.name, ok: 'Delete', danger: true })) { await call('lists.delete', { id: l.id }); loadLLM(); } }
  const listModelOpts = $derived(models.filter((m) => m.kind === (lst?.kind || 'chat')).map((m) => ({ value: m.name, label: m.name })));

  // ── web ──
  let web = $state({ search_order: [], tavily_key: '', yandex_key: '', yandex_folder: '', yandex_region: '', anysearch_key: '', anysearch_url: '', flaresolverr_url: '', translate_key: '', user_agent: '', allow_private: false });
  let wprov = $state([]);
  let wq = $state('');
  let wres = $state(null);
  async function loadWeb() {
    const r = await call('web.providers', {}, { quiet: true });
    if (r) { web = { ...web, ...r.config }; wprov = r.providers; }
  }
  $effect(() => { loadWeb(); });
  const wlabels = $derived(Object.fromEntries(wprov.map((p) => [p.id, p.label + (p.available ? '' : ' (no key)')])));
  async function saveWeb() { if (await saveSetting('web', web, 'Web settings saved')) loadWeb(); }
  async function testSearch() { wres = null; wres = (await call('web.search', { query: wq || 'prism personal assistant' })) || { error: true }; }

  // ── integrations ──
  let tg = $state({ enabled: false, token: '', owner_id: 0, group_id: 0, pair_code: '', mirror_notices: true });
  let tgs = $state(null);
  let obs = $state({ vault_path: '' });
  let vaults = $state([]);
  let mac = $state({ notifications: true, sound: 'Glass' });
  let br = $state({ headless: true, remote_url: '', chrome_bin: '' });
  let brs = $state(null);
  let fs = $state({ write_roots: [], read_deny: [] });
  let btest = $state(null);
  const sounds = ['Glass', 'Ping', 'Pop', 'Tink', 'Submarine', 'Hero', 'Funk', 'Basso', ''].map((s) => ({ value: s, label: s || '(silent)' }));
  async function loadInt() {
    tg = await loadSetting('telegram', { ...tg, mirror_notices: true });
    tgs = await call('telegram.status', {}, { quiet: true });
    obs = await loadSetting('obsidian', obs);
    mac = await loadSetting('macos', mac);
    br = await loadSetting('browser', br);
    brs = await call('browser.status', {}, { quiet: true });
    fs = await loadSetting('fs', fs);
  }
  $effect(() => { if (tab === 'integrations') untrack(() => { loadInt(); loadEl(); loadMail(); }); });
  $effect(() => listen('status', () => { if (tab === 'integrations') call('telegram.status', {}, { quiet: true }).then((r) => r && (tgs = r)); }));
  async function saveTg() { await saveSetting('telegram', tg, 'Telegram settings saved'); setTimeout(async () => (tgs = await call('telegram.status', {}, { quiet: true })), 1500); }
  async function testTg() { const u = await call('telegram.test', { token: tg.token }); if (u) toast(`Bot OK: @${u}`); }
  async function pair() { const c = await call('telegram.pair'); if (c) { tg.pair_code = c; tgs = await call('telegram.status', {}, { quiet: true }); } }
  async function unpair() { if (await call('telegram.unpair')) { tgs = await call('telegram.status', {}, { quiet: true }); toast('Unpaired'); } }
  async function detectVaults() { vaults = (await call('obsidian.detect', {})) || []; if (!vaults.length) toast('No vaults found — enter the path manually', 'warn'); }
  async function setVault(p) { const r = await call('obsidian.set', { path: p }); if (r) { obs.vault_path = p; toast(p ? 'Vault connected and registered for semantic search' : 'Vault disconnected'); } }
  async function testMac() { if (await call('macos.test')) toast('Notification sent'); }
  async function loadEl(key = '') {
    els = await call('elevenlabs.status', { key }, { quiet: true });
    if (els?.ok) voices = (await call('elevenlabs.voices', { key }, { quiet: true })) || [];
  }
  async function checkCal() {
    calBusy = true;
    cal = await call('calendar.check', {}, { quiet: true, timeout: 120000 });
    calBusy = false;
  }
  async function listShortcuts() { shortcuts = (await call('shortcuts.list', {}, { quiet: true })) || []; }
  async function saveEl() { if (await saveSetting('elevenlabs', el, 'ElevenLabs settings saved')) loadEl(); }
  const creditLeft = $derived(els?.ok ? Math.max(0, els.limit - els.used) : 0);
  const voiceOpts = $derived([{ value: '', label: '(first available voice)' }, ...voices.map((v) => ({ value: v.id, label: v.name, hint: [v.labels?.gender, v.labels?.accent, v.labels?.use_case].filter(Boolean).join(' · ') }))]);
  const elModels = [{ value: 'eleven_flash_v2_5', label: 'Flash v2.5 — 0.5 credit/char, fast, 32 languages' }, { value: 'eleven_multilingual_v2', label: 'Multilingual v2 — 1 credit/char, richer' }, { value: 'eleven_turbo_v2_5', label: 'Turbo v2.5 — 0.5 credit/char' }];
  async function saveBrowser() { await saveSetting('browser', br, 'Browser settings saved'); brs = await call('browser.status', {}, { quiet: true }); }
  async function testBrowser() { btest = { busy: true }; const r = await call('browser.test', { url: 'https://example.com' }, { quiet: true, throw: true }).catch((e) => ({ error: e.message })); btest = r.error ? { err: r.error } : { ok: `“${r.title}” · ${r.bytes} bytes · ${r.ms} ms` }; brs = await call('browser.status', {}, { quiet: true }); }
  const stLed = (s) => ({ ok: 'ok', error: 'error', standby: 'standby', warn: 'warn', off: 'off' })[s] || 'off';

  // ── logs ──
  let logs = $state([]);
  let level = $state('');
  async function loadLogs() { logs = (await call('logs.list', { limit: 300, level }, { quiet: true })) || []; }
  $effect(() => { if (tab === 'logs') { level; untrack(loadLogs); } });
  const lvl = { info: 'dim', warn: 'warn', error: 'err' };
</script>

<div class="pg">
  <Tabs tabs={[{ id: 'general', label: 'General' }, { id: 'models', label: 'Models' }, { id: 'web', label: 'Web' }, { id: 'integrations', label: 'Integrations' }, { id: 'context', label: 'Advanced' }, { id: 'notify', label: 'Notifications' }, { id: 'logs', label: 'Logs' }]} bind:active={tab} />
  <div class="body scroll">
    {#if tab === 'general'}
      <div class="cols">
        <Panel title="You">
          <Field label="Name"><Input bind:value={gen.user_name} placeholder="how agents address you" /></Field>
          <Field label="Reply language" hint="preferred language for replies, free text (e.g. Russian, English)"><Input bind:value={gen.language} /></Field>
          <Field label="Timezone" hint="cron schedules, reminders and agents' clock"><Select bind:value={gen.timezone} options={zoneOpts} searchable /></Field>
          <div class="row"><Button variant="primary" onclick={() => saveSetting('general', gen, 'Saved')}>Save</Button></div>
        </Panel>
        <Panel title="System">
          <div class="kv"><span>Database</span><b>{S.status?.leds?.find((l) => l.id === 'db')?.detail || '—'}</b></div>
          <div class="kv"><span>Version</span><b>{S.status?.version}</b></div>
          <div class="kv"><span>Onboarding</span><b>{S.status?.onboarded ? 'completed' : 'pending'}</b></div>
          <div class="row"><Button variant="accent" onclick={reopenOnboarding}>Run onboarding again</Button></div>
          <div class="sm mute">Everything here is stored in PostgreSQL; only the connection string lives in <code>~/.prism/config.json</code>.</div>
        </Panel>
      </div>
    {:else if tab === 'models'}
      <Panel title="Model roles">
        <div class="roles">
          {#each [['chat', 'Chat model', 'default for every agent (each agent can override)', chatOpts], ['fast', 'Fast model', 'summaries, extraction, tool selection, judging — small & cheap', chatOpts], ['embedding', 'Embedding model', 'memory & semantic search', embOpts]] as [r, label, hint, opts]}
            <div class="role">
              <Field {label} {hint}><Select value={roles[r]} options={opts} onchange={(v) => setRole(r, v)} searchable /></Field>
              <div class="tst"><Button size="sm" loading={testing[r]?.busy} onclick={() => testRole(r)}>Test</Button>
                {#if testing[r]?.ok}<span class="sm hi">✔ {testing[r].ok}</span>{/if}{#if testing[r]?.err}<span class="sm err">✗ {testing[r].err}</span>{/if}</div>
            </div>
          {/each}
        </div>
      </Panel>

      <Panel title="Providers & keys">
        {#snippet right()}<Button size="sm" variant="ghost" onclick={() => { prov = { id: 0, name: '', kind: 'openai', base_url: presets.openai || '', enabled: true }; pOpen = true; }}><Icon name="plus" size={11} /> Provider</Button>{/snippet}
        {#each provs as p (p.id)}
          <div class="prov">
            <div class="row"><Led state={p.enabled ? 'ok' : 'off'} size={8} /><span class="hi">{p.name}</span><Badge tone="accent">{p.kind}</Badge><span class="sm mute ellipsis grow">{p.base_url}</span>
              <Button size="sm" variant="primary" onclick={() => discover(p)}>Discover models</Button><Button size="sm" variant="ghost" onclick={() => { prov = { ...p }; pOpen = true; }}>Edit</Button><Button size="sm" variant="ghost" onclick={() => delProv(p)}><Icon name="trash" size={11} /></Button></div>
            {#if p.keys?.length}
              <table class="t keys"><tbody>
                {#each p.keys as k (k.id)}
                  <tr><td class="mute">{k.label || 'key'}</td><td class="dim">{k.masked || '(keyless)'}</td><td><Switch bind:checked={k.enabled} onchange={() => saveKey(k)} /></td>
                    <td class="mute sm">prio {k.priority}</td><td class="sm {k.last_error ? 'err' : 'mute'}" title={k.last_error}>{k.cooldown_until && new Date(k.cooldown_until) > new Date() ? '⏸ cooling down' : k.last_error ? 'last call failed' : `${k.uses} calls`}</td>
                    <td class="end"><Button size="sm" variant="ghost" onclick={() => delKey(k)}><Icon name="x" size={10} /></Button></td></tr>
                {/each}
              </tbody></table>
            {/if}
            {#if newKey[p.id]}
              <div class="row addkey">
                <div class="k1"><Input size="sm" bind:value={newKey[p.id].label} placeholder="label" /></div>
                <div class="grow"><Input size="sm" type="password" bind:value={newKey[p.id].api_key} placeholder={p.kind === 'lmstudio' || p.kind === 'custom' ? 'API key (optional)' : 'API key — several keys give automatic fallback'} /></div>
                <Button size="sm" onclick={() => addKey(p)}>Add key</Button>
              </div>
            {/if}
          </div>
        {:else}<Empty>no providers — add OpenAI, Gemini, Grok, OpenRouter, LM Studio or any OpenAI-compatible endpoint</Empty>{/each}
      </Panel>

      <Panel title="Models" flush>
        {#snippet right()}<Button size="sm" variant="ghost" onclick={() => { mdl = { id: 0, name: '', provider_id: provs[0]?.id || 0, model_id: '', kind: 'chat', context_window: 32768, supports_tools: true, vision: true, max_output: 0 }; mOpen = true; }}><Icon name="plus" size={11} /> Model</Button>{/snippet}
        <div class="scroll" style="max-height:300px"><table class="t">
          <thead><tr><th>Name</th><th>Provider</th><th>Model id</th><th>Kind</th><th>Context</th><th>Tools</th><th>Sees images</th><th></th></tr></thead>
          <tbody>
            {#each models as m (m.id)}
              <tr><td class="hi">{m.name}</td><td class="dim">{provName(m.provider_id)}</td><td class="mute ellipsis" style="max-width:220px">{m.model_id}</td><td><Badge tone={m.kind === 'chat' ? 'ok' : 'accent'} w={10}>{m.kind}</Badge></td>
                <td class="dim">{(m.context_window / 1024).toFixed(0)}k</td><td>{m.supports_tools ? '✔' : '—'}</td><td>{m.kind === 'chat' ? (m.vision ? '✔' : '—') : ''}</td>
                <td class="end nowrap"><Button size="sm" variant="ghost" onclick={() => { mdl = { ...m }; mOpen = true; }}>Edit</Button><Button size="sm" variant="ghost" onclick={() => delModel(m)}><Icon name="trash" size={11} /></Button></td></tr>
            {:else}<tr><td colspan="8"><Empty>no models — use “Discover models” on a provider</Empty></td></tr>{/each}
          </tbody></table></div>
      </Panel>

      <Panel title="Fallback lists">
        {#snippet right()}<Button size="sm" variant="ghost" onclick={() => { lst = { id: 0, name: '', kind: 'chat', models: [] }; lOpen = true; }}><Icon name="plus" size={11} /> List</Button>{/snippet}
        <div class="sm mute">A list is an ordered chain: if the first model or all its keys fail, the next is tried. Use a list name anywhere a model is chosen (roles, agents).</div>
        {#each lists as l (l.id)}
          <div class="row"><span class="hi">⛓ {l.name}</span><Badge tone="mute">{l.kind}</Badge><span class="sm dim grow ellipsis">{l.models.join(' → ')}</span><Button size="sm" variant="ghost" onclick={() => { lst = structuredClone($state.snapshot(l)); lOpen = true; }}>Edit</Button><Button size="sm" variant="ghost" onclick={() => delList(l)}><Icon name="trash" size={11} /></Button></div>
        {/each}
      </Panel>
    {:else if tab === 'web'}
      <div class="cols">
        <Panel title="Search providers (fallback order)">
          <OrderedList bind:value={web.search_order} options={wprov.map((p) => p.id)} labels={wlabels} addLabel="add provider…" />
          <div class="sm mute">The first provider that answers wins. DuckDuckGo needs no key.</div>
          <h4>Yandex Search API</h4>
          <div class="two"><Field label="API key"><Input type="password" bind:value={web.yandex_key} /></Field><Field label="Folder id"><Input bind:value={web.yandex_folder} /></Field></div>
          <Field label="Region id" hint="e.g. 225 = Russia (optional)"><Input bind:value={web.yandex_region} /></Field>
          <h4>AnySearch</h4>
          <div class="two"><Field label="API key"><Input type="password" bind:value={web.anysearch_key} /></Field><Field label="Base URL" hint="optional"><Input bind:value={web.anysearch_url} placeholder="https://api.anysearch.com" /></Field></div>
          <h4>Tavily</h4>
          <Field label="API key"><Input type="password" bind:value={web.tavily_key} /></Field>
        </Panel>
        <div class="col">
          <Panel title="Fetching & extraction">
            <Field label="FlareSolverr URL" hint="solves Cloudflare challenges for web_fetch (optional)"><Input bind:value={web.flaresolverr_url} placeholder="http://localhost:8191" /></Field>
            <Field label="User agent" hint="empty = a current Safari UA"><Input bind:value={web.user_agent} /></Field>
            <Switch bind:checked={web.allow_private} label="allow fetching localhost / LAN addresses" tone="attn" />
            <div class="sm mute">Blocked by default so a web page cannot steer an agent at your local network.</div>
          </Panel>
          <Panel title="Language tools">
            <Field label="Google Cloud Translation API key" hint="for the lexicon tool; without it the fast model translates"><Input type="password" bind:value={web.translate_key} /></Field>
          </Panel>
          <div class="row"><Button variant="primary" onclick={saveWeb}>Save web settings</Button></div>
          <Panel title="Test search">
            <div class="row"><div class="grow"><Input bind:value={wq} placeholder="query" onenter={testSearch} /></div><Button onclick={testSearch}>Search</Button></div>
            {#if wres?.results}<div class="sm hi">via {wres.provider}</div>{#each wres.results as r}<div class="sm"><a href={r.url} target="_blank" rel="noopener noreferrer">{r.title}</a><div class="mute ellipsis">{r.snippet}</div></div>{/each}{/if}
          </Panel>
        </div>
      </div>
    {:else if tab === 'integrations'}
      <div class="mas">
        <Panel title="Telegram">
          {#snippet right()}<Led state={stLed(tgs?.state || 'off')} size={8} /><span class="sm dim">{tgs?.detail || tgs?.state || 'disabled'}</span>{/snippet}
          <Switch bind:checked={tg.enabled} label="enabled" />
          <Field label="Bot token" hint="from @BotFather"><div class="row"><div class="grow"><Input type="password" bind:value={tg.token} /></div><Button size="sm" onclick={testTg} disabled={!tg.token}>Test</Button></div></Field>
          <Switch bind:checked={tg.mirror_notices} label="also send agent notices to my DM" />
          <div class="row"><Button variant="primary" onclick={saveTg}>Save</Button></div>
          <hr />
          <div class="kv"><span>Owner</span><b>{tgs?.paired ? `paired (id ${tgs.owner_id})` : 'not paired'}</b></div>
          {#if tgs?.paired}<div><Button size="sm" variant="danger" onclick={unpair}>Unpair</Button></div>
          {:else}
            <div class="row"><Button size="sm" variant="accent" onclick={pair}>Generate pairing code</Button>{#if tgs?.pair_code}<code class="code">/pair {tgs.pair_code}</code>{/if}</div>
            <div class="sm mute">Send that command to your bot in Telegram. Only the paired account can talk to PRISM.</div>
          {/if}
          <div class="kv"><span>Topics group</span><b>{tgs?.group_id ? tgs.group_id : 'not set'}</b></div>
          <div class="sm mute">Create a supergroup with Topics enabled, add the bot as admin (Manage topics), then send <code>/setgroup</code> there. Agents drop messages into topics by name; your replies in a topic carry its context.</div>
          {#if tgs?.topics?.length}<div class="topics">{#each tgs.topics as t}<div class="sm"><span class="hi">{t.name}</span> <span class="mute">by {t.created_by} — {t.purpose.slice(0, 60)}</span></div>{/each}</div>{/if}
        </Panel>
          <Panel title="Obsidian">
            <Field label="Vault path" hint="an iCloud vault lives under ~/Library/Mobile Documents/iCloud~md~obsidian/Documents"><Input bind:value={obs.vault_path} mono placeholder="/path/to/vault" /></Field>
            <div class="row"><Button variant="primary" onclick={() => setVault(obs.vault_path)}>Connect</Button><Button onclick={detectVaults}>Detect vaults</Button>{#if obs.vault_path}<Button variant="ghost" onclick={() => setVault('')}>Disconnect</Button>{/if}</div>
            {#each vaults as v}<div class="row"><span class="sm mute ellipsis grow">{v}</span><Button size="sm" onclick={() => setVault(v)}>Use</Button></div>{/each}
            <div class="sm mute">A connected vault is registered as a semantic-search source; index it under Library → Documents.</div>
          </Panel>
          <Panel title="ElevenLabs — voice, sound effects, images">
            {#snippet right()}{#if els?.ok}<Led state="ok" size={8} /><span class="sm dim">{els.tier} plan</span>{:else if els?.configured}<Led state="error" size={8} /><span class="sm dim">check key</span>{:else}<Led state="off" size={8} /><span class="sm dim">not set up</span>{/if}{/snippet}
            <Field label="API key" hint="elevenlabs.io → Developers → API keys. Needs Text to Speech and Sound Effects access."><div class="row"><div class="grow"><Input type="password" bind:value={el.api_key} /></div><Button size="sm" onclick={() => loadEl(el.api_key)} disabled={!el.api_key}>Test</Button></div></Field>
            {#if els?.ok}
              <div class="kv"><span>Credits this cycle</span><b>{creditLeft.toLocaleString()} of {els.limit.toLocaleString()} left</b></div>
              <Bar value={els.used} max={els.limit || 1} label="{els.used} used" />
            {:else if els?.error}<div class="sm err">{els.error}</div>{/if}
            <Field label="Voice"><Select bind:value={el.voice_id} options={voiceOpts} searchable /></Field>
            <Field label="Speech model"><Select bind:value={el.model_id} options={elModels} /></Field>
            <div class="two">
              <Field label="Monthly cap (credits)" hint="PRISM stops speaking past this, leaving the rest for you"><NumberInput bind:value={el.monthly_cap} min={0} max={10000000} step={500} /></Field>
              <Field label="Ask before speaking over (chars)" hint="0 = never ask"><NumberInput bind:value={el.confirm_over} min={0} max={5000} step={100} /></Field>
            </div>
            <Field label="Image model" hint="image generation needs a Pro plan on ElevenLabs' side"><Input bind:value={el.image_model} mono /></Field>
            <div class="row"><Button variant="primary" onclick={saveEl}>Save</Button></div>
            <div class="sm mute">Agents get <code>tts_speak</code>, <code>sound_effect</code>, <code>image_generate</code> and <code>voice_list</code>. Results appear in chat as a player or picture, and are sent as attachments on Telegram.</div>
          </Panel>
          <Panel title="macOS">
            <Switch bind:checked={mac.notifications} label="native notifications when the UI is not in use" onchange={() => saveSetting('macos', mac, 'Saved')} />
            <Field label="Sound"><Select bind:value={mac.sound} options={sounds} onchange={() => saveSetting('macos', mac, 'Saved')} /></Field>
            <div class="row"><Button size="sm" onclick={testMac}>Send test notification</Button></div>
          </Panel>
          <Panel title="Mail">
            {#snippet right()}<Button size="sm" variant="ghost" onclick={() => editMail(null)}><Icon name="plus" size={11} /> Account</Button>{/snippet}
            <div class="sm mute">Connect several mailboxes and tag them (work, personal…): agents refer to an account by its tag. Use PRISM's own IMAP/SMTP client, or a <code>himalaya</code> account (OAuth, Gmail, Outlook). Agents can search and read (always treated as untrusted), save drafts, and send only after you approve each message.</div>
            {#each mails as m (m.id)}
              <div class="row">
                <Led state={m.enabled ? 'ok' : 'off'} size={7} /><b class="hi">{m.tag}</b>
                <span class="sm mute grow ellipsis">{m.backend === 'himalaya' ? `himalaya: ${m.himalaya}` : `${m.user} @ ${m.imap_host}`}</span>
                <Button size="sm" variant="ghost" onclick={() => editMail(m)}>Edit</Button>
                <Button size="sm" variant="ghost" onclick={() => delMail(m)}><Icon name="trash" size={11} /></Button>
              </div>
            {:else}<div class="sm mute">no accounts yet</div>{/each}
            {#if !him.installed}<div class="sm mute">himalaya is not installed (<code>brew install himalaya</code>); PRISM's own client works without it.</div>{/if}
          </Panel>
          <Panel title="Calendar, Reminders & Shortcuts">
            <div class="sm mute">Agents can read and change your calendar and reminders, and run (or, experimentally, create) Apple Shortcuts. macOS asks for permission the first time; the first check also builds a small helper (about 10 seconds).</div>
            <div class="row"><Button size="sm" variant="accent" loading={calBusy} onclick={checkCal}>Check calendar access</Button><Button size="sm" onclick={listShortcuts}>List my shortcuts</Button></div>
            {#if cal?.ok}<div class="sm"><Led state="ok" size={7} /> access granted — {cal.calendars.filter((c) => c.kind === 'events').length} calendars, {cal.calendars.filter((c) => c.kind === 'reminders').length} reminder lists</div>
              <div class="sm mute">{cal.calendars.map((c) => c.title).join(' · ')}</div>
            {:else if cal}<div class="sm err">{cal.error}</div>{/if}
            {#if shortcuts}<div class="sm mute">{shortcuts.length ? `${shortcuts.length} shortcuts: ${shortcuts.join(' · ')}` : 'no shortcuts found'}</div>{/if}
          </Panel>
          <Panel title="Browser automation">
            {#snippet right()}<Led state={stLed(brs?.state || 'off')} size={8} /><span class="sm dim">{brs?.detail || ''}</span>{/snippet}
            <Switch bind:checked={br.headless} label="headless" />
            <Field label="Remote DevTools URL" hint="attach to a Chrome started with --remote-debugging-port (keeps your logins)"><Input bind:value={br.remote_url} placeholder="http://127.0.0.1:9222" /></Field>
            <Field label="Chrome binary" hint="empty = auto-detect"><Input bind:value={br.chrome_bin} mono /></Field>
            <div class="row"><Button variant="primary" onclick={saveBrowser}>Save</Button><Button onclick={testBrowser} loading={btest?.busy}>Test render</Button><Button variant="ghost" onclick={() => call('browser.stop').then(() => toast('Browser closed'))}>Stop</Button></div>
            {#if btest?.ok}<div class="sm hi">✔ {btest.ok}</div>{/if}{#if btest?.err}<div class="sm err">✗ {btest.err}</div>{/if}
          </Panel>
          <Panel title="File access">
            <Field label="Extra writable folders" hint="agents may write only in the PRISM data dir plus these"><Tags bind:value={fs.write_roots} placeholder="~/Documents/agents" /></Field>
            <Field label="Extra denied folders" hint="~/.ssh, ~/.aws, keychains… are always denied"><Tags bind:value={fs.read_deny} /></Field>
            <div class="row"><Button variant="primary" onclick={() => saveSetting('fs', fs, 'Saved')}>Save</Button></div>
          </Panel>
      </div>
    {:else if tab === 'context'}
      <div class="cols dense">
        <Panel title="Auto-compaction" id="set.auto-compaction" collapsible resizable>
          <div class="sm mute">When an agent's context reaches the trigger, stale results are dropped and old turns are summarized down to the target (a fixed template; memory hits are dropped since they can be queried again).</div>
          <div class="two"><Field label="Trigger (% of model window)"><NumberInput value={Math.round(ctx.compact_at * 100)} min={40} max={95} step={5} unit="%" onchange={(v) => (ctx.compact_at = v / 100)} /></Field>
            <Field label="Target (%)"><NumberInput value={Math.round(ctx.target * 100)} min={5} max={60} step={5} unit="%" onchange={(v) => (ctx.target = v / 100)} /></Field></div>
          <div class="row"><Button variant="primary" onclick={() => saveSetting('context', ctx, 'Saved')}>Save</Button></div>
        </Panel>
        <Panel title="Model concurrency" id="set.model-concurrency" collapsible resizable>
          <div class="sm mute">How many model calls run at the same time across all agents. Local models on one GPU usually do best with 1–2: extra parallel calls only queue up inside the server. Applies immediately.</div>
          <div class="two"><Field label="Parallel model calls"><NumberInput bind:value={rt.llm_concurrency} min={1} max={16} step={1} /></Field></div>
          <div class="row"><Button variant="primary" onclick={() => saveSetting('runtime', rt, 'Saved')}>Save</Button></div>
        </Panel>
        <Panel title="Guardrails · loops" id="set.guardrails-loops" collapsible resizable>
          <div class="sm mute">Loop detection: when an agent keeps making the identical tool call, or gets stuck regenerating the same text, it is warned once and then the run is stopped rather than left to burn tokens forever.</div>
          <div class="two"><Field label="Warn after (identical tool call)"><NumberInput bind:value={gr.tool_repeat_warn} min={2} max={9} step={1} /></Field>
            <Field label="Stop after"><NumberInput bind:value={gr.tool_repeat_abort} min={2} max={10} step={1} /></Field></div>
          <Field label="Stop after (stuck repeating text)" hint="lower than the tool threshold — regenerating a whole reply is far more expensive than retrying one call"><NumberInput bind:value={gr.text_repeat_abort} min={1} max={5} step={1} /></Field>
          <div class="row"><Button variant="primary" onclick={() => saveSetting('guardrails', gr, 'Saved')}>Save</Button></div>
        </Panel>
        <Panel title="Guardrails · long runs" id="set.guardrails-long-runs" collapsible resizable>
          <div class="sm mute">Autonomous runs (a schedule firing, a standing intent waking its owner) get extra iteration budget over the agent's own limit, since nobody is there to ask for more time.</div>
          <div class="two"><Field label="Extra budget" hint="% added to the agent's own limit"><NumberInput bind:value={gr.autonomous_boost_pct} min={0} max={200} step={10} unit="%" /></Field>
            <Field label="Hard cap (iterations)"><NumberInput bind:value={gr.autonomous_max_iterations} min={10} max={200} step={10} /></Field></div>
          <div class="sm mute">When a run uses its whole budget or is stopped as a loop, PRISM analyses it (what was achieved, why it stalled), rewrites the instruction and runs it once more in a fresh session. If that fails too, a delegated task ends as failed with the analysis so the parent agent tells you; a top-level task stays "partial" for your review.</div>
          <Switch checked={!gr.auto_retry_off} label="analyse and retry stalled tasks once" onchange={(v) => (gr.auto_retry_off = !v)} />
          <Switch checked={!gr.code_review_off} label="when a coding task finishes: run the project's checks and have Reviewer judge the diff (shown in Library → Code)" onchange={(v) => (gr.code_review_off = !v)} />
          <div class="row"><Button variant="primary" onclick={() => saveSetting('guardrails', gr, 'Saved')}>Save</Button></div>
        </Panel>
        <Panel title="Memory · digesting" id="set.memory-digesting" collapsible resizable>
          <div class="sm mute">Raw conversation is distilled into facts on this cadence; new messages also wake the pipeline sooner.</div>
          <div class="two"><Field label="Digest raw messages every (s)"><NumberInput bind:value={mem.process_every_s} min={30} max={86400} step={30} unit="s" /></Field><Field label="Batch size"><NumberInput bind:value={mem.raw_batch} min={5} max={200} step={5} /></Field></div>
          <Field label="…but only once this many raw messages are waiting" hint="0 = default (6). A smaller backlog is still digested when its oldest message is 20 minutes old. New messages and facts also wake the pipeline within ~20 s instead of waiting for the timer."><NumberInput bind:value={mem.process_min} min={0} max={200} step={1} /></Field>
          <div class="sm mute">Auto-merge finds project/domain banks that turned out to be the same topic (one per model version instead of one for the family) and merges them — undoable from the Memory page.</div>
          <Switch checked={!mem.auto_merge_off} label="merge near-duplicate banks automatically" onchange={(v) => (mem.auto_merge_off = !v)} />
          <div class="row"><Button variant="primary" onclick={() => saveSetting('memory', mem, 'Saved')}>Save</Button><Button onclick={() => call('memory.process').then((n) => n !== undefined && toast(`${n} facts distilled`))}>Digest now</Button><Button onclick={() => call('memory.reindex').then((n) => n !== undefined && toast(`${n} facts re-embedded`))}>Re-embed all facts</Button><Button onclick={() => call('memory.auto_merge').then((rs) => rs && toast(rs.length ? `${rs.length} bank group(s) merged` : 'No near-duplicate banks found'))}>Merge duplicate banks now</Button></div>
        </Panel>
        <Panel title="Memory · thinking" id="set.memory-thinking" collapsible resizable>
          <div class="sm mute">What memory works out from its facts, per bank, once enough new ones have arrived.</div>
          <div class="sm mute">Reflection draws conclusions from facts that agree with each other (each keeps its evidence and is revised when the evidence changes).</div>
          <div class="two"><Switch checked={!mem.reflect_off} label="reflect automatically" onchange={(v) => (mem.reflect_off = !v)} /><Field label="…after new facts in a bank" hint="0 = default (8)"><NumberInput bind:value={mem.reflect_after} min={0} max={100} step={1} /></Field></div>
          <div class="sm mute">Deep analysis reads a whole bank and derives patterns, deductions, hypotheses, trends, risks and open questions, flags contradicting facts, retires duplicates and keeps a profile card. It uses the chat model.</div>
          <div class="two"><Switch checked={!mem.analyze_off} label="analyse automatically" onchange={(v) => (mem.analyze_off = !v)} /><Field label="…after new facts in a bank" hint="0 = default (12)"><NumberInput bind:value={mem.analyze_after} min={0} max={200} step={1} /></Field></div>
          <div class="sm mute">Synthesis thinks in levels: level 2 reads the analysis results of all banks together (themes, causes, implications, tensions); level 3 distils standing principles, open tensions and gaps from level 2. Each cites the level below, so every chain ends in real facts, and confidence shrinks per level.</div>
          <div class="two"><Switch checked={!mem.synth_off} label="synthesise automatically" onchange={(v) => (mem.synth_off = !v)} /><Field label="…after new conclusions" hint="0 = default (6)"><NumberInput bind:value={mem.synth_after} min={0} max={200} step={1} /></Field></div>
          <div class="sm mute">Entity extraction pulls people, products, places and their relations out of facts into the entity graph (Memory → graph).</div>
          <div class="two"><Switch checked={!mem.entities_off} label="extract entities automatically" onchange={(v) => (mem.entities_off = !v)} /><Field label="…after new facts in a bank" hint="0 = default (8)"><NumberInput bind:value={mem.entities_after} min={0} max={100} step={1} /></Field></div>
          <div class="row"><Button variant="primary" onclick={() => saveSetting('memory', mem, 'Saved')}>Save</Button></div>
        </Panel>
        <Panel title="Memory · upkeep" id="set.memory-upkeep" collapsible resizable>
          <div class="sm mute">Background housekeeping around memory: what to keep, check and summarise.</div>
          <Switch checked={!mem.bookmarks_off} label="bookmark pages the agents visited (the model keeps only reusable references)" onchange={(v) => (mem.bookmarks_off = !v)} />
          <Switch checked={!mem.verify_off} label="have an agent verify unverified web facts and hypotheses (at most every 3 days)" onchange={(v) => (mem.verify_off = !v)} />
          <Switch checked={!mem.auto_ingest_off} label="learn new inbox documents automatically (chats wait until you say which participant you are)" onchange={(v) => (mem.auto_ingest_off = !v)} />
          <div class="two"><Switch checked={!mem.digest_off} label="write a memory digest briefing" onchange={(v) => (mem.digest_off = !v)} /><Field label="…every (days)" hint="0 = default (7). Sums up what memory learned, worked out and doubts."><NumberInput bind:value={mem.digest_days} min={0} max={60} step={1} /></Field></div>
          <div class="row"><Button variant="primary" onclick={() => saveSetting('memory', mem, 'Saved')}>Save</Button></div>
        </Panel>
        <Panel title="Memory · hints" id="set.memory-hints" collapsible resizable>
          <Field label="Hints for memory" hint="Your own instructions, shown to the model whenever memory distils conversations, draws conclusions or extracts entities. PRISM already tells it your name (General) and that agent names are software, not people. Examples: “Treat the Berlin trip as a project, not a person.” · “Never store prices as facts about me.”"><Textarea bind:value={mem.hints} rows={4} mono={false} placeholder="e.g. I am Danil. Facts about my hardware belong in the user bank." /></Field>
          <div class="row"><Button variant="primary" onclick={() => saveSetting('memory', mem, 'Saved')}>Save</Button></div>
        </Panel>
        <Panel title="Retention" id="set.retention" collapsible resizable>
          <div class="sm mute">How long finished tasks (with their agent sessions) and log entries are kept. 0 keeps them forever. Cleanup runs hourly.</div>
          <div class="two"><Field label="Finished tasks"><NumberInput bind:value={ret.tasks_days} min={0} max={3650} step={5} unit="days" /></Field><Field label="Logs"><NumberInput bind:value={ret.logs_days} min={0} max={3650} step={1} unit="days" /></Field></div>
          <Field label="Remove tasks that ended as" hint="none ticked = every finished task. “partial” runs are resumable, so they are kept unless ticked."><div class="row wrap gap-12">{#each retStatuses as st}<Checkbox checked={(ret.task_statuses || []).includes(st)} label={st} onchange={(v) => toggleRetStatus(st, v)} />{/each}</div></Field>
          <div class="row"><Button variant="primary" onclick={() => saveSetting('retention', ret, 'Saved')}>Save</Button><Button loading={cleaning} onclick={cleanNow}>Clean now</Button></div>
        </Panel>
        <Panel title="Tool selector (Sherpa)" id="set.tool-selector-sherpa-" collapsible resizable>
          <div class="sm mute">Agents start with a small toolset; Sherpa adds the few extra tools each task needs from the whole repository instead of loading every schema.</div>
          <Switch bind:checked={tools.enabled} label="select tools per task" />
          <Switch bind:checked={tools.use_llm} label="let Sherpa's model refine the candidates (slower, more precise)" />
          <Field label="Max extra tools"><NumberInput bind:value={tools.max} min={1} max={10} /></Field>
          <div class="row"><Button variant="primary" onclick={() => saveSetting('tool_selector', tools, 'Saved')}>Save</Button></div>
        </Panel>
      </div>
    {:else if tab === 'notify'}
      <div class="cols">
        <Panel title="What notifies you" id="set.what-notifies-you" collapsible resizable>
          <div class="sm mute">Notifications appear under the bell in the top bar and as a toast. “Also push” sends them to macOS and Telegram (when those are configured).</div>
          <table class="t nt"><thead><tr><th>Event</th><th>Show</th><th>Also push</th></tr></thead><tbody>
            {#each [['cron', 'A schedule fires', true], ['intent', 'A standing intent / watch triggers', true], ['ask', 'An agent needs you (question or approval)', false], ['error', 'A task fails for good, or a watch gives up', true], ['proposal', 'An agent proposes a change to another agent (needs your review)', true]] as [k, label, ext]}
              <tr><td>{label}</td><td><Switch bind:checked={nf[k].show} /></td><td>{#if ext}<Switch bind:checked={nf[k].external} disabled={!nf[k].show} />{:else}<span class="mute sm" title="Approvals and questions already reach Telegram and macOS on their own">built in</span>{/if}</td></tr>
            {/each}
          </tbody></table>
          <div class="row"><Button variant="primary" onclick={() => saveSetting('notifications', nf, 'Saved')}>Save</Button></div>
        </Panel>
      </div>
    {:else}
      <div class="row"><div class="w"><Select bind:value={level} options={[{ value: '', label: 'all levels' }, 'info', 'warn', 'error']} size="sm" /></div><Button size="sm" onclick={loadLogs}><Icon name="refresh" size={11} /></Button></div>
      <Panel flush>
        <div class="scroll" style="max-height:70vh"><table class="t"><tbody>
          {#each logs as l (l.id)}<tr><td class="mute nowrap sm">{stamp(l.ts)}</td><td class={lvl[l.level] || 'dim'}>{l.level}</td><td class="dim">{l.source}</td><td class="pre">{l.message}</td></tr>
          {:else}<tr><td><Empty>no log entries</Empty></td></tr>{/each}
        </tbody></table></div>
      </Panel>
    {/if}
  </div>
</div>

<Modal bind:open={pOpen} title={prov?.id ? 'Edit provider' : 'New provider'} width={520}>
  {#if prov}
    <Field label="Name"><Input bind:value={prov.name} placeholder="OpenAI" /></Field>
    <Field label="Kind"><Select bind:value={prov.kind} options={kinds} onchange={(k) => { if (!prov.id || !prov.base_url) prov.base_url = presets[k] || ''; }} /></Field>
    <Field label="Base URL" hint="OpenAI-compatible, including /v1"><Input bind:value={prov.base_url} mono /></Field>
    <Switch bind:checked={prov.enabled} label="enabled" />
  {/if}
  {#snippet footer()}<Button variant="ghost" onclick={() => (pOpen = false)}>Cancel</Button><Button variant="primary" disabled={!prov?.name?.trim()} onclick={saveProv}>Save</Button>{/snippet}
</Modal>

<Modal bind:open={dOpen} title="Models on {disc.provider?.name || ''}" width={900}>
  {#if disc.busy}<Empty>asking the provider…</Empty>{/if}
  <div class="scroll" style="max-height:56vh"><table class="t">
    <thead><tr><th></th><th>Model</th><th>Kind</th><th>Context</th><th>Loaded</th><th>Tools</th></tr></thead>
    <tbody>
      {#each disc.items as i (i.id)}
        <tr class:off={i.have}>
          <td>{#if i.have}<Badge tone="mute">have</Badge>{:else}<Checkbox bind:checked={disc.sel[i.id]} />{/if}</td>
          <td class="hi">{i.id}{#if i.vision}<Badge tone="accent">vision</Badge>{/if}</td>
          <td><Badge tone={i.kind === 'chat' ? 'ok' : 'accent'} w={10}>{i.kind}</Badge></td>
          <td class="dim">{(i.context / 1024).toFixed(0)}k{#if i.max_context && i.max_context !== i.context}<span class="mute sm"> / max {(i.max_context / 1024).toFixed(0)}k</span>{/if}</td>
          <td><Led state={i.loaded ? 'ok' : 'off'} size={8} /></td><td>{i.kind === 'chat' ? (i.tools ? '✔' : '—') : ''}</td>
        </tr>
      {/each}
    </tbody></table></div>
  <div class="sm mute">Context shows the loaded window (or 32k for models not loaded). It drives auto-compaction — edit it per model afterwards if needed.</div>
  {#snippet footer()}<Button variant="ghost" onclick={() => (dOpen = false)}>Close</Button><Button variant="primary" onclick={importSel} disabled={!Object.values(disc.sel).some(Boolean)}>Import selected</Button>{/snippet}
</Modal>

<Modal bind:open={mailOpen} title={mailForm?.id ? `Mail account “${mailForm.tag}”` : 'New mail account'} width={640}>
  {#if mailForm}
    <div class="two">
      <Field label="Tag" hint="what agents call it: work, personal, personal2"><Input bind:value={mailForm.tag} placeholder="work" /></Field>
      <Field label="Connect with"><Select bind:value={mailForm.backend} options={[{ value: 'imap', label: "PRISM's IMAP/SMTP client" }, { value: 'himalaya', label: 'himalaya (CLI)' }]} /></Field>
    </div>
    {#if mailForm.backend === 'himalaya'}
      <Field label="himalaya account" hint="configured in ~/.config/himalaya/config.toml (run `himalaya configure`)">
        {#if him.accounts.length}<Select bind:value={mailForm.himalaya} options={him.accounts.map((n) => ({ value: n, label: n }))} />{:else}<Input bind:value={mailForm.himalaya} placeholder="account name" />{/if}
      </Field>
      {#if !him.installed}<div class="sm err">himalaya is not installed.</div>{/if}
    {:else}
      <div class="two">
        <Field label="IMAP server"><Input bind:value={mailForm.imap_host} placeholder="imap.example.com" onchange={guessSmtp} /></Field>
        <Field label="Port" hint="0 = default for the security mode"><NumberInput bind:value={mailForm.imap_port} min={0} max={65535} /></Field>
      </div>
      <div class="two">
        <Field label="Security"><Select bind:value={mailForm.imap_security} options={secOpts} /></Field>
        <Field label="User name"><Input bind:value={mailForm.user} placeholder="you@example.com" /></Field>
      </div>
      <Field label="Password" hint={mailForm.has_password ? 'leave empty to keep the saved one' : 'an app password if the provider requires one'}><Input type="password" bind:value={mailForm.password} /></Field>
      <div class="two">
        <Field label="SMTP server (for sending)"><Input bind:value={mailForm.smtp_host} placeholder="smtp.example.com" /></Field>
        <Field label="SMTP port"><NumberInput bind:value={mailForm.smtp_port} min={0} max={65535} /></Field>
      </div>
      <div class="two">
        <Field label="SMTP security"><Select bind:value={mailForm.smtp_security} options={secOpts} /></Field>
        <Field label="Send as (From)"><Input bind:value={mailForm.from} placeholder="You <you@example.com>" /></Field>
      </div>
      <details><summary class="sm mute">Folders and separate SMTP login</summary>
        <div class="two"><Field label="Inbox" hint="empty = INBOX"><Input bind:value={mailForm.inbox} /></Field><Field label="Drafts folder" hint="empty = detected"><Input bind:value={mailForm.drafts} /></Field></div>
        <div class="two"><Field label="Sent folder" hint="empty = detected"><Input bind:value={mailForm.sent} /></Field><Field label="SMTP user" hint="empty = same as IMAP"><Input bind:value={mailForm.smtp_user} /></Field></div>
        <Field label="SMTP password" hint="empty = same as IMAP"><Input type="password" bind:value={mailForm.smtp_password} /></Field>
      </details>
    {/if}
    <Switch bind:checked={mailForm.enabled} label="available to agents" />
    {#if mailTest}<div class="sm {mailTest.ok ? '' : 'err'}">{#if mailTest.ok}<Led state="ok" size={7} /> connected — {mailTest.folders.length} folders, {mailTest.unread} unread in the inbox{:else}{mailTest.error}{/if}</div>{/if}
  {/if}
  {#snippet footer()}<Button variant="ghost" onclick={() => (mailOpen = false)}>Cancel</Button><Button loading={mailBusy} onclick={() => testMail(mailForm)}>Test connection</Button><Button variant="primary" disabled={!mailForm?.tag?.trim()} onclick={saveMail}>Save</Button>{/snippet}
</Modal>

<Modal bind:open={mOpen} title={mdl?.id ? 'Edit model' : 'New model'} width={560}>
  {#if mdl}
    <Field label="Name" hint="how agents and lists refer to it"><Input bind:value={mdl.name} /></Field>
    <div class="two"><Field label="Provider"><Select bind:value={mdl.provider_id} options={provs.map((p) => ({ value: p.id, label: p.name }))} /></Field><Field label="Kind"><Select bind:value={mdl.kind} options={['chat', 'embedding']} /></Field></div>
    <Field label="Model id" hint="sent to the API"><Input bind:value={mdl.model_id} mono /></Field>
    <div class="two"><Field label="Context window (tokens)"><NumberInput bind:value={mdl.context_window} min={512} max={4000000} step={1024} /></Field><Field label="Max output (0 = default)"><NumberInput bind:value={mdl.max_output} min={0} max={200000} step={256} /></Field></div>
    {#if mdl.kind === 'chat'}<Switch bind:checked={mdl.supports_tools} label="supports tool calling" />
      <Switch bind:checked={mdl.vision} label="can see images" />{/if}
  {/if}
  {#snippet footer()}<Button variant="ghost" onclick={() => (mOpen = false)}>Cancel</Button><Button variant="primary" disabled={!mdl?.name?.trim() || !mdl?.model_id?.trim()} onclick={saveModel}>Save</Button>{/snippet}
</Modal>

<Modal bind:open={lOpen} title={lst?.id ? 'Edit fallback list' : 'New fallback list'} width={520}>
  {#if lst}
    <Field label="Name"><Input bind:value={lst.name} placeholder="robust-chat" /></Field>
    <Field label="Kind"><Select bind:value={lst.kind} options={['chat', 'embedding']} onchange={() => (lst.models = [])} /></Field>
    <Field label="Chain (first is tried first)"><OrderedList bind:value={lst.models} options={listModelOpts} addLabel="add model…" /></Field>
  {/if}
  {#snippet footer()}<Button variant="ghost" onclick={() => (lOpen = false)}>Cancel</Button><Button variant="primary" disabled={!lst?.name?.trim() || !lst?.models?.length} onclick={saveList}>Save</Button>{/snippet}
</Modal>

<style>
  .pg { display: flex; flex-direction: column; gap: 6px; height: 100%; min-height: 0; }
  .body { flex: 1; display: flex; flex-direction: column; gap: 8px; padding-right: 2px; }
  .cols { display: grid; grid-template-columns: repeat(auto-fit, minmax(380px, 1fr)); gap: 8px; align-items: start; }
  .cols.dense { grid-auto-flow: dense; }
  .col { display: flex; flex-direction: column; gap: 8px; }
  /* integrations: panels flow into as many columns as fit and balance their heights. overflow:hidden here
     (not on Panel itself, which relies on its corner-accent glow bleeding slightly elsewhere) because a
     status LED's box-shadow glow otherwise paints straight across the narrow column-gap into the next
     panel — CSS multi-column layout doesn't clip overflowing paint at column boundaries on its own. */
  .mas { columns: 380px; column-gap: 8px; }
  .mas > :global(.p) { break-inside: avoid; margin-bottom: 8px; overflow: hidden; }
  .two { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
  .row { flex-wrap: wrap; } /* button rows wrap instead of overflowing a narrow panel */
  .kv { display: flex; justify-content: space-between; color: var(--fg-dim); }
  .kv b { color: var(--fg-hi); font-weight: 500; }
  .roles { display: grid; grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); gap: 12px; }
  .tst { display: flex; gap: 8px; align-items: center; margin-top: 4px; flex-wrap: wrap; }
  .prov { border: 1px solid var(--line); padding: 6px 8px; display: flex; flex-direction: column; gap: 5px; background: var(--bg); }
  .keys td { padding: 1px 6px; }
  .addkey .k1 { width: 130px; }
  .code { color: var(--attn); font-size: var(--fs-lg); letter-spacing: 0.08em; background: var(--attn-bg); border: 1px solid var(--attn-dim); padding: 1px 8px; }
  .topics { max-height: 90px; overflow: auto; border: 1px solid var(--line); padding: 3px 6px; }
  .w { width: 160px; }
  tr.off td { opacity: 0.5; }
</style>
