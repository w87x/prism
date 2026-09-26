<script>
  import { S, call, listen, toast, confirmBox, loadSetting, saveSetting, ago, until, stamp, openProposal, go } from '../lib/store.svelte.js';
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
  import RadioGroup from '../lib/ui/RadioGroup.svelte';
  import Checkbox from '../lib/ui/Checkbox.svelte';
  import Segmented from '../lib/ui/Segmented.svelte';
  import Empty from '../lib/ui/Empty.svelte';
  import Icon from '../lib/ui/Icon.svelte';
  import Hint from '../lib/ui/Hint.svelte';
  import Tip from '../lib/ui/Tip.svelte';
  import { AUTONOMY_HELP } from '../lib/help.js';

  let tab = $state('intents');
  let cfg = $state({ enabled: true, dream_enabled: true, auto_evolve: false, hire_limit: 3 });
  $effect(() => { loadSetting('autonomy', { enabled: true, dream_enabled: true, auto_evolve: false }).then((c) => (cfg = c)); });
  const saveCfg = () => saveSetting('autonomy', cfg, 'Autonomy settings saved');

  // ── intents ──
  let intents = $state([]);
  let iOpen = $state(false);
  let ni = $state(null);
  const kinds = [
    { value: 'time', label: 'at a time', hint: 'reminder' }, { value: 'http', label: 'web page condition' }, { value: 'llm', label: 'judged by the model', hint: 'free-form question' },
    { value: 'rss', label: 'new feed item' }, { value: 'file', label: 'file exists / finished copying' }, { value: 'process', label: 'process exits' }, { value: 'download', label: 'download finishes' },
  ];
  async function loadI() { intents = (await call('intents.list', {}, { quiet: true })) || []; }
  $effect(() => { loadI(); return listen('intent.update', loadI); });
  function newIntent() {
    ni = { description: '', type: 'intent', owner: 'Atlas', kind: 'llm', cadence_s: 300, repeat: false, p: { at: '', url: '', contains: '', not_contains: '', changed: false, status: 200, question: '', query: '', path: '', stable_s: 5, pid: 0, pattern: '', download_id: 0 } };
    iOpen = true;
  }
  async function saveIntent() {
    const p = { kind: ni.kind };
    const src = ni.p;
    const pick = { time: ['at'], http: ['url', 'contains', 'not_contains', 'changed', 'status'], llm: ['question', 'url', 'query'], rss: ['url', 'contains'], file: ['path', 'stable_s'], process: ['pid', 'pattern'], download: ['download_id'] }[ni.kind];
    pick.forEach((k) => { if (src[k] !== '' && src[k] !== 0 && src[k] !== false) p[k] = src[k]; });
    if (ni.kind === 'time' && p.at && !p.at.includes('T')) p.at = new Date(p.at).toISOString();
    else if (ni.kind === 'time' && p.at) p.at = new Date(p.at).toISOString();
    const r = await call('intents.create', { owner: ni.owner, description: ni.description, type: ni.type, cadence_s: ni.cadence_s, repeat: ni.repeat, predicate: p });
    if (r) { iOpen = false; toast('Registered'); loadI(); }
  }
  async function setIntent(i, status) { await call('intents.update', { id: i.id, status }); loadI(); }
  async function delIntent(i) { if (await confirmBox({ title: 'Delete', text: i.description, ok: 'Delete', danger: true })) { await call('intents.delete', { id: i.id }); loadI(); } }
  const iLed = (s) => ({ active: 'standby', fired: 'ok', cancelled: 'off', error: 'error', expired: 'warn' })[s];
  // monitors (watches with a time budget): tune them without opening a dialog
  const dur = (sec) => (sec < 90 ? `${Math.round(sec)}s` : sec < 5400 ? `${Math.round(sec / 60)}m` : `${Math.floor(sec / 3600)}h ${Math.round((sec % 3600) / 60)}m`);
  const monNote = (i) => [i.eta_seconds != null ? `finishes in ~${dur(i.eta_seconds)}` : '', i.expires_at && i.status === 'active' ? `${dur(Math.max(0, (new Date(i.expires_at) - Date.now()) / 1000))} left` : i.status === 'expired' ? 'time is up' : ''].filter(Boolean).join(' · ');
  async function faster(i) { await call('intents.update', { id: i.id, cadence_s: Math.max(15, Math.round(i.cadence_s / 2)) }); loadI(); }
  async function slower(i) { await call('intents.update', { id: i.id, cadence_s: Math.min(86400, i.cadence_s * 2) }); loadI(); }
  async function extend(i, m) { await call('intents.extend', { id: i.id, minutes: m }); loadI(); }
  const pred = (i) => { const p = i.predicate || {}; return p.kind + (p.url ? ' ' + p.url : p.path ? ' ' + p.path : p.question ? ' “' + p.question + '”' : p.command ? ' ' + p.command : p.at ? ' ' + stamp(p.at) : ''); };

  // ── crons ──
  let crons = $state([]);
  let cOpen = $state(false);
  let nc = $state(null);
  const presets = [
    { value: '*/30 * * * *', label: 'every 30 minutes' }, { value: '0 * * * *', label: 'hourly' }, { value: '0 9 * * *', label: 'daily 09:00' }, { value: '0 8 * * 1-5', label: 'weekdays 08:00' },
    { value: '0 20 * * *', label: 'daily 20:00' }, { value: '0 10 * * 6', label: 'Saturday 10:00' }, { value: '0 9 1 * *', label: 'monthly, the 1st' }, { value: '@every 2h', label: 'every 2 hours' },
  ];
  async function loadC() { crons = (await call('crons.list', {}, { quiet: true })) || []; }
  $effect(() => { loadC(); return listen('cron.fired', loadC); });
  async function saveCron() { if (await call('crons.save', $state.snapshot(nc))) { cOpen = false; loadC(); } }
  async function toggleCron(c) { await call('crons.save', $state.snapshot(c)); loadC(); }
  async function runCron(c) { if (await call('crons.run', { id: c.id })) toast(`${c.name} queued`); }
  async function delCron(c) { if (await confirmBox({ title: 'Delete schedule', text: c.name, ok: 'Delete', danger: true })) { await call('crons.delete', { id: c.id }); loadC(); } }

  // ── briefings ──
  let briefs = $state([]);
  let showDismissed = $state(false);
  let readB = $state(null); // the briefing open in the "Read" modal
  // every briefing, dismissed ones included, with the unread ones called out — a bare unread count read "0" once everything was opened
  const briefBadge = $derived.by(() => { const n = briefs.filter((b) => b.status === 'new').length; return n ? `${n} new · ${briefs.length}` : briefs.length; });
  async function loadB() { briefs = (await call('briefings.list', {}, { quiet: true })) || []; arrive(); }
  // arriving from Today: jump to the right tab and open the briefing that was clicked
  function arrive() {
    if (S.autonomyTab) { tab = S.autonomyTab; S.autonomyTab = null; }
    if (S.openBriefing) {
      const b = briefs.find((x) => x.id === S.openBriefing);
      S.openBriefing = null;
      if (b) { tab = 'brief'; openRead(b); }
    }
  }
  arrive();
  $effect(() => { loadB(); return listen('briefing.new', loadB); });
  $effect(() => { S.briefRev; if (S.briefRev) loadB(); }); // the full-screen reader changed one
  const shown = $derived(briefs.filter((b) => showDismissed || b.status !== 'dismissed'));
  // cards (as before) or a compact grouped list — by day or by agent — remembered per browser
  const lsg = (k, d) => { try { return localStorage.getItem(k) || d; } catch { return d; } };
  let bView = $state(lsg('prism.briefView', 'cards'));
  let bGroup = $state(lsg('prism.briefGroup', 'day'));
  $effect(() => { try { localStorage.setItem('prism.briefView', bView); localStorage.setItem('prism.briefGroup', bGroup); } catch {} });
  const dayKey = (t) => { const d = new Date(t), n = new Date(); const diff = Math.floor((new Date(n.getFullYear(), n.getMonth(), n.getDate()) - new Date(d.getFullYear(), d.getMonth(), d.getDate())) / 864e5); return diff <= 0 ? 'Today' : diff === 1 ? 'Yesterday' : diff < 7 ? 'This week' : 'Earlier'; };
  const bGroups = $derived.by(() => {
    const m = new Map();
    for (const b of shown) { const k = bGroup === 'agent' ? b.agent : dayKey(b.created_at); if (!m.has(k)) m.set(k, []); m.get(k).push(b); }
    return [...m].map(([label, items]) => ({ label, items }));
  });
  async function dismiss(b, status) {
    // optimistic: the card should visibly react right away, not just after the next load
    b.status = status;
    await call('briefings.status', { id: b.id, status });
    loadB();
    // close the Read modal only on an actual dismiss — openRead marks it delivered too and must not self-close
    if (status === 'dismissed' && readB?.id === b.id) readB = null;
  }
  let replyText = $state('');
  let replying = $state(false);
  const asks = (b) => !b.reply && /\?/.test(b.body);
  async function sendReply() {
    replying = true;
    const ok = await call('briefings.reply', { id: readB.id, text: replyText });
    replying = false;
    if (ok) { readB.reply = replyText; readB.replied_at = new Date().toISOString(); replyText = ''; toast(`${readB.agent} got your answer`); loadB(); }
  }
  function openRead(b) { replyText = ''; readB = b; if (b.status === 'new') dismiss(b, 'delivered'); }
  async function saveToObsidian(b) {
    const rel = await call('briefings.save_obsidian', { id: b.id });
    if (rel) toast(`Saved to Obsidian: ${rel}`);
  }
  async function dream() { if (await call('dream.now')) toast('Oneiros is dreaming…'); }

  // ── evolution ──
  let props = $state([]);
  async function loadP() { props = (await call('agents.proposals', { status: '' }, { quiet: true })) || []; }
  $effect(() => { loadP(); const a = listen('evolution.proposal', loadP), b = listen('evolution.decided', loadP); return () => { a(); b(); }; });
  async function reviewAll() {
    const t = await call('tasks.create', { agent: 'Metis', input: 'Review all non-system agents with recent activity (agent_performance) and propose soul improvements only where evidence is clear.' });
    if (t) toast(`Metis started (task #${t.id})`);
  }
  const agentOpts = $derived(S.agents.filter((a) => a.enabled && a.role !== 'entry' || a.role === 'entry').map((a) => ({ value: a.name, label: a.name, hint: a.group })));

  // ── audit: what autonomy actually did, unattended — a timeline over crons/intents/briefings, not a new log ──
  let audit = $state([]);
  async function loadAudit() { audit = (await call('autonomy.audit', { limit: 150 }, { quiet: true })) || []; }
  $effect(() => { if (tab === 'audit') loadAudit(); });
  const auditTone = { cron: 'accent', intent: 'ok', watch: 'mute', dream: 'attn' };
  const auditStatusTone = (s) => ({ done: 'ok', failed: 'err', fired: 'ok', running: 'accent' })[s] || 'mute';
  let auditSel = $state(null); // the row open in the detail modal — the table truncates the summary
  function openAuditTask(e) {
    if (!e.task_id) return;
    S.selectedTask = e.task_id;
    go('tasks');
  }
</script>

<div class="pg">
  <div class="ctl">
    <Switch bind:checked={cfg.enabled} label="autonomy" onchange={saveCfg} />
    <Switch bind:checked={cfg.dream_enabled} label="dreams" onchange={saveCfg} title="Oneiros reflects daily and prepares briefings" />
    <Switch bind:checked={cfg.auto_evolve} label="auto-apply soul evolution" tone="attn" onchange={saveCfg} title="Otherwise proposals wait for your review" />
    <Hint title="Schedule, intent, watch, tracker — what's the difference?">
      <table class="cmp"><tbody>{#each AUTONOMY_HELP.compare as [n, k, t]}<tr><td><b>{n}</b><br /><span class="mute">{k}</span></td><td>{t}</td></tr>{/each}</tbody></table>
    </Hint>
    <span class="sm mute" title="How many agents other agents (Forge) may hire per week. Each starts on probation without exec tools until you confirm it. 0 forbids hiring.">auto-hires per week</span>
    <div style="width:78px"><NumberInput bind:value={cfg.hire_limit} min={0} max={20} onchange={saveCfg} /></div>
  </div>
  <Tabs tabs={[{ id: 'intents', label: 'Intents & watches', badge: intents.filter((i) => i.status === 'active').length }, { id: 'cron', label: 'Schedules', badge: crons.length },
    { id: 'brief', label: 'Briefings', badge: briefBadge }, { id: 'evo', label: 'Evolution', badge: props.filter((p) => p.status === 'pending').length },
    { id: 'audit', label: 'Audit' }]} bind:active={tab} />

  {#if tab === 'intents'}
    <div class="bar"><span class="sm mute">“Tell me when X ships.” Checked on a cadence; when it holds, the owner agent is woken with the evidence. Watches just track progress and notify.</span><span class="grow"></span><Button size="sm" onclick={newIntent}><Icon name="plus" size={11} /> New</Button></div>
    <Panel flush grow>
      <div class="scroll">
        <table class="t">
          <thead><tr><th></th><th>What</th><th>Check</th><th>Owner</th><th>Progress</th><th style="width:60px">Every</th><th style="width:52px">Next</th><th style="width:150px"></th></tr></thead>
          <tbody>
            {#each intents as i (i.id)}
              <tr class:off={i.status === 'cancelled'}>
                <td><Led state={iLed(i.status)} pulse={i.status === 'active'} size={8} title={i.status} /></td>
                <td class="hi">{i.description} <Badge tone={i.type === 'watch' ? 'accent' : 'ok'}>{i.type}</Badge>{#if i.repeat}<Badge tone="mute">repeat</Badge>{/if}</td>
                <td class="dim sm"><Tip text={pred(i)} max={220} /></td>
                <td class="dim">{i.owner}</td>
                <td class="sm {i.last_error ? 'err' : 'mute'}" style="max-width:220px">{i.last_error || i.progress}{#if i.fraction != null}<div class="mbar" title="{Math.round(i.fraction * 100)}%"><i style="width:{Math.round(i.fraction * 100)}%"></i></div>{/if}{#if i.expires_at}<div class="mon">{monNote(i)}</div>{/if}</td>
                <td class="mute sm">{i.cadence_s}s</td>
                <td class="mute sm">{i.status === 'active' ? until(i.next_due) : i.status}</td>
                <td class="end nowrap">
                  {#if i.expires_at}
                    <Button size="sm" variant="ghost" title="check twice as often" onclick={() => faster(i)}>⏩</Button>
                    <Button size="sm" variant="ghost" title="check half as often" onclick={() => slower(i)}>⏪</Button>
                    <Button size="sm" variant="ghost" title="give it another hour" onclick={() => extend(i, 60)}>+1h</Button>
                  {/if}
                  {#if i.status === 'expired'}<Button size="sm" variant="ghost" onclick={() => extend(i, 60)}>Resume</Button>
                  {:else if i.status === 'active'}<Button size="sm" variant="ghost" onclick={() => setIntent(i, 'cancelled')}>Pause</Button>{:else if i.status !== 'fired'}<Button size="sm" variant="ghost" onclick={() => setIntent(i, 'active')}>Resume</Button>{/if}
                  <Button size="sm" variant="ghost" onclick={() => delIntent(i)}><Icon name="trash" size={11} /></Button>
                </td>
              </tr>
            {:else}
              <tr><td colspan="8"><Empty>nothing is being watched</Empty></td></tr>
            {/each}
          </tbody>
        </table>
      </div>
    </Panel>
  {:else if tab === 'cron'}
    <div class="bar"><span class="sm mute">Each firing starts a fresh session of the agent with the standing prompt. Times use your timezone.</span><span class="grow"></span>
      <Button size="sm" onclick={() => { nc = { id: 0, name: '', agent: 'Atlas', expr: '0 9 * * *', prompt: '', enabled: true, system: false }; cOpen = true; }}><Icon name="plus" size={11} /> New schedule</Button></div>
    <Panel flush grow>
      <div class="scroll">
        <table class="t">
          <thead><tr><th>Name</th><th>Agent</th><th>Cron</th><th>Prompt</th><th style="width:50px">Next</th><th style="width:44px">Last</th><th style="width:54px">On</th><th style="width:150px"></th></tr></thead>
          <tbody>
            {#each crons as c (c.id)}
              <tr>
                <td class="hi">{#if c.system}<Badge tone="accent">built-in</Badge> {/if}{c.name}</td>
                <td class="dim">{c.agent}</td><td class="mute nowrap">{c.expr}</td>
                <td class="dim sm"><Tip text={c.prompt} max={280} /></td>
                <td class="mute sm">{c.enabled ? until(c.next_run) : '—'}</td><td class="mute sm">{ago(c.last_run)}</td>
                <td><Switch bind:checked={c.enabled} onchange={() => toggleCron(c)} /></td>
                <td class="end nowrap">
                  <Button size="sm" variant="ghost" onclick={() => runCron(c)}><Icon name="play" size={10} /> Run</Button>
                  <Button size="sm" variant="ghost" onclick={() => { nc = structuredClone($state.snapshot(c)); cOpen = true; }}>Edit</Button>
                  {#if !c.system}<Button size="sm" variant="ghost" onclick={() => delCron(c)}><Icon name="trash" size={11} /></Button>{/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </Panel>
  {:else if tab === 'brief'}
    <div class="bar"><span class="sm mute">Oneiros dreams over everything known about you and drops briefings here; importance 4+ is pushed to you immediately.</span><span class="grow"></span>
      {#if briefs.some((b) => b.status === 'dismissed')}<Checkbox bind:checked={showDismissed} label="show dismissed ({briefs.filter((b) => b.status === 'dismissed').length})" />{/if}
      <Segmented size="sm" bind:value={bView} options={[{ value: 'cards', label: 'cards' }, { value: 'list', label: 'list' }]} />
      {#if bView === 'list'}<Segmented size="sm" bind:value={bGroup} options={[{ value: 'day', label: 'by day' }, { value: 'agent', label: 'by agent' }]} />{/if}
      <Button size="sm" onclick={() => { S.reader.id = null; S.reader.open = true; }} title="Read them one at a time, full screen"><Icon name="edit" size={11} /> Reader</Button>
      <Button size="sm" variant="accent" onclick={dream}>Dream now</Button></div>
    {#if bView === 'list'}
      <Panel flush grow>
        <div class="scroll">
          {#each bGroups as g (g.label)}
            <div class="bgh">{g.label} <span class="mute">{g.items.length}</span></div>
            {#each g.items as b (b.id)}
              <button type="button" class="brow" class:unread={b.status === 'new'} class:gone={b.status === 'dismissed'} onclick={() => openRead(b)}>
                <Badge tone={b.importance >= 4 ? 'attn' : 'mute'}>P{b.importance}</Badge>
                {#if asks(b)}<Badge tone="attn" title="this briefing asks you something">?</Badge>{/if}
                <span class="hi grow ellipsis">{b.title}</span>
                <span class="sm dim ellipsis" style="max-width:38%">{b.body}</span>
                <span class="sm mute nowrap">{bGroup === 'agent' ? '' : b.agent + ' · '}{ago(b.created_at)}</span>
              </button>
            {/each}
          {:else}<Empty>no briefings yet</Empty>{/each}
        </div>
      </Panel>
    {:else}
    <div class="cards scroll">
      {#each shown as b (b.id)}
        <div class="card" class:unread={b.status === 'new'} class:gone={b.status === 'dismissed'}>
          <Panel title={b.title} tone={b.importance >= 4 ? 'attn' : ''}>
            {#snippet right()}
              {#if asks(b)}<Badge tone="attn" title="this briefing asks you something">question</Badge>{:else if b.reply}<Badge tone="ok">answered</Badge>{/if}
              {#if b.status === 'new'}<Badge tone="accent">unread</Badge>{/if}
              <span class="sm mute">{b.agent} · {ago(b.created_at)}</span><Badge tone={b.importance >= 4 ? 'attn' : 'mute'}>P{b.importance}</Badge>
            {/snippet}
            <button type="button" class="preview" onclick={() => openRead(b)}>{b.body}</button>
            <div class="row end">
              <Button size="sm" variant="ghost" onclick={() => openRead(b)}><Icon name="edit" size={11} /> Read</Button>
              <Button size="sm" variant="ghost" onclick={() => saveToObsidian(b)}>Save to Obsidian</Button>
              {#if b.status !== 'dismissed'}<Button size="sm" variant="ghost" onclick={() => dismiss(b, 'dismissed')}>Dismiss</Button>
              {:else}<Button size="sm" variant="ghost" onclick={() => dismiss(b, 'delivered')}>Restore</Button>{/if}
            </div>
          </Panel>
        </div>
      {:else}
        <Empty>no briefings yet</Empty>
      {/each}
    </div>
    {/if}
  {:else if tab === 'audit'}
    <div class="bar"><span class="sm mute">Everything autonomy did on its own — schedules, standing intents, watches and dreams — newest first. Nothing here is a separate log; it is read straight from the tasks, intents and briefings it already produced.</span><span class="grow"></span><Button size="sm" variant="ghost" onclick={loadAudit}><Icon name="refresh" size={11} /> Refresh</Button></div>
    <Panel flush grow>
      <div class="scroll">
        <table class="t">
          <thead><tr><th style="width:70px">Kind</th><th style="width:80px">When</th><th>Title</th><th style="width:90px">Agent</th><th style="width:70px">Status</th><th>Summary</th></tr></thead>
          <tbody>
            {#each audit as e, i (i)}
              <tr class="click" onclick={() => (auditSel = e)}>
                <td><Badge tone={auditTone[e.kind] || 'mute'}>{e.kind}</Badge></td>
                <td class="mute sm">{ago(e.time)}</td>
                <td class="hi">{e.title}</td>
                <td class="dim sm">{e.agent}</td>
                <td><Badge tone={auditStatusTone(e.status)}>{e.status}</Badge></td>
                <td class="dim sm ellipsis" style="max-width:360px" title={e.summary}>{e.summary}</td>
              </tr>
            {:else}
              <tr><td colspan="6"><Empty>nothing autonomous has run yet</Empty></td></tr>
            {/each}
          </tbody>
        </table>
      </div>
    </Panel>
  {:else}
    <div class="bar"><span class="sm mute">Metis proposes revised souls from facts and performance. Nothing changes until you apply it (unless auto-apply is on); every version stays in history.</span><span class="grow"></span><Button size="sm" variant="accent" onclick={reviewAll}>Review all agents</Button></div>
    <Panel flush grow>
      <div class="scroll">
        <table class="t">
          <thead><tr><th>Agent</th><th>Change</th><th>Rationale</th><th>Status</th><th style="width:44px">Age</th><th style="width:110px"></th></tr></thead>
          <tbody>
            {#each props as p (p.id)}
              <tr class="click" onclick={() => openProposal(p.id)}>
                <td class="hi">{p.agent}</td><td><Badge tone="accent">{p.kind}</Badge></td><td class="dim sm">{p.rationale}</td>
                <td><Badge tone={p.status === 'pending' ? 'attn' : p.status === 'applied' ? 'ok' : 'mute'}>{p.status}</Badge></td><td class="mute sm">{ago(p.created_at)}</td>
                <td class="end"><Button size="sm" variant="ghost">Review</Button></td>
              </tr>
            {:else}
              <tr><td colspan="6"><Empty>no proposals</Empty></td></tr>
            {/each}
          </tbody>
        </table>
      </div>
    </Panel>
  {/if}
</div>

<Modal open={!!auditSel} onclose={() => (auditSel = null)} title={auditSel ? `${auditSel.kind}: ${auditSel.title}` : ''} width={720}>
  {#if auditSel}
    <div class="row wrap gap-12 sm mute"><Badge tone={auditTone[auditSel.kind] || 'mute'}>{auditSel.kind}</Badge><span>{auditSel.agent}</span><Badge tone={auditStatusTone(auditSel.status)}>{auditSel.status}</Badge><span>{stamp(auditSel.time)}</span></div>
    <pre class="pre-full">{auditSel.summary || '(no summary recorded)'}</pre>
  {/if}
  {#snippet footer()}
    {#if auditSel?.task_id}<Button variant="accent" onclick={() => { const e = auditSel; auditSel = null; openAuditTask(e); }}>Open task #{auditSel.task_id}</Button>{/if}
    <span class="grow"></span><Button variant="ghost" onclick={() => (auditSel = null)}>Close</Button>
  {/snippet}
</Modal>

<Modal bind:open={iOpen} title="New intent / watch" width={640}>
  {#if ni}
    <Field label="What are you waiting for?"><Input bind:value={ni.description} placeholder="Tell me when the new Framework laptop ships" /></Field>
    <div class="row wrap gap-12"><Field label="Type"><RadioGroup inline bind:value={ni.type} options={[{ value: 'intent', label: 'intent', hint: 'wake the agent' }, { value: 'watch', label: 'watch', hint: 'just notify' }]} /></Field>
      <div class="grow"><Field label="Owner"><Select bind:value={ni.owner} options={agentOpts} searchable /></Field></div></div>
    <Field label="Check"><Select bind:value={ni.kind} options={kinds} /></Field>
    {#if ni.kind === 'time'}<Field label="When" hint="local date & time"><Input bind:value={ni.p.at} type="text" placeholder="2026-10-01 17:00" /></Field>{/if}
    {#if ni.kind === 'http' || ni.kind === 'rss' || ni.kind === 'llm'}<Field label={ni.kind === 'llm' ? 'Page URL (or use a search query below)' : 'URL'}><Input bind:value={ni.p.url} placeholder="https://…" /></Field>{/if}
    {#if ni.kind === 'llm'}
      <Field label="Search query" hint="used as evidence when there is no URL"><Input bind:value={ni.p.query} /></Field>
      <Field label="Condition"><Input bind:value={ni.p.question} placeholder="Is version 2.0 released?" /></Field>
    {/if}
    {#if ni.kind === 'http'}
      <div class="row wrap gap-12"><div class="grow"><Field label="Contains"><Input bind:value={ni.p.contains} /></Field></div><div class="grow"><Field label="Must be gone"><Input bind:value={ni.p.not_contains} /></Field></div></div>
      <Checkbox bind:checked={ni.p.changed} label="fire when the content changes" />
    {/if}
    {#if ni.kind === 'rss'}<Field label="Title contains (optional)"><Input bind:value={ni.p.contains} /></Field>{/if}
    {#if ni.kind === 'file'}<Field label="Path"><Input bind:value={ni.p.path} mono placeholder="~/Downloads/big.iso" /></Field><Field label="Stable for (seconds)" hint="0 = fire when it exists"><NumberInput bind:value={ni.p.stable_s} min={0} max={3600} /></Field>{/if}
    {#if ni.kind === 'process'}<div class="row gap-12"><Field label="PID"><NumberInput bind:value={ni.p.pid} min={0} max={9999999} /></Field><div class="grow"><Field label="or command pattern"><Input bind:value={ni.p.pattern} mono placeholder="rsync" /></Field></div></div>{/if}
    {#if ni.kind === 'download'}<Field label="Download id"><NumberInput bind:value={ni.p.download_id} min={0} max={99999999} /></Field>{/if}
    <div class="row wrap gap-12"><Field label="Check every (s)"><NumberInput bind:value={ni.cadence_s} min={30} max={86400} step={30} unit="s" /></Field><Checkbox bind:checked={ni.repeat} label="keep watching after it fires" /></div>
  {/if}
  {#snippet footer()}<Button variant="ghost" onclick={() => (iOpen = false)}>Cancel</Button><Button variant="primary" disabled={!ni?.description?.trim()} onclick={saveIntent}>Register</Button>{/snippet}
</Modal>

<Modal bind:open={cOpen} title={nc?.id ? 'Edit schedule' : 'New schedule'} width={620}>
  {#if nc}
    <Field label="Name"><Input bind:value={nc.name} /></Field>
    <div class="row wrap gap-12">
      <div class="grow"><Field label="Agent"><Select bind:value={nc.agent} options={agentOpts} searchable /></Field></div>
      <div class="grow"><Field label="Preset"><Select value="" options={presets} placeholder="pick a preset…" onchange={(v) => v && (nc.expr = v)} /></Field></div>
    </div>
    <Field label="Cron expression" hint="min hour day month weekday · also @daily, @hourly, @every 2h"><Input bind:value={nc.expr} mono /></Field>
    <Field label="Standing prompt" hint="Tell the agent to reply NO_REPLY when there is nothing worth telling you."><Textarea bind:value={nc.prompt} rows={6} mono={false} /></Field>
    <Switch bind:checked={nc.enabled} label="enabled" />
  {/if}
  {#snippet footer()}<Button variant="ghost" onclick={() => (cOpen = false)}>Cancel</Button><Button variant="primary" disabled={!nc?.name?.trim() || !nc?.prompt?.trim()} onclick={saveCron}>Save</Button>{/snippet}
</Modal>

<Modal open={!!readB} title={readB?.title || ''} width={640} onclose={() => (readB = null)}>
  {#if readB}
    <div class="sm mute">{readB.agent} · {stamp(readB.created_at)} · P{readB.importance}</div>
    <div class="pre">{readB.body}</div>
    {#if readB.reply}
      <h4>Your answer <span class="mute sm">{ago(readB.replied_at)}</span></h4>
      <div class="pre">{readB.reply}</div>
    {/if}
    <h4>{readB.reply ? 'Add to your answer' : /\?/.test(readB.body) ? 'Answer' : 'Reply'}</h4>
    <Textarea bind:value={replyText} rows={3} mono={false} placeholder="{readB.agent} reads this, remembers what matters and acts on it" />
  {/if}
  {#snippet footer()}
    <Button variant="ghost" onclick={() => saveToObsidian(readB)}>Save to Obsidian</Button>
    {#if readB?.status !== 'dismissed'}<Button variant="danger" onclick={() => dismiss(readB, 'dismissed')}>Dismiss</Button>{/if}
    <Button variant="accent" loading={replying} disabled={!replyText.trim()} onclick={sendReply}>Send</Button>
    <Button variant="primary" onclick={() => (readB = null)}>Close</Button>
  {/snippet}
</Modal>

<style>
  .cmp { border-collapse: collapse; }
  .cmp td { vertical-align: top; padding: 4px 8px 4px 0; border-bottom: 1px solid var(--line); }
  .cmp tr:last-child td { border-bottom: 0; }
  .pg { display: flex; flex-direction: column; gap: 6px; height: 100%; min-height: 0; }
  .ctl { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 16px; flex: none;
    /* phone: one scrolling line instead of a tall wrapped block */ padding: 4px 8px; border: 1px solid var(--line); background: var(--bg-1); }
  .bar { display: flex; align-items: center; gap: 8px; flex: none; }
  tr.off td { opacity: 0.45; }
  .cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(min(360px, 100%), 1fr)); gap: 8px; align-content: start; flex: 1; }
  /* unread gets its own colored cue independent of the importance badge; dismissed fades until "show dismissed" reveals it */
  .card.unread :global(.p) { border-left: 3px solid var(--accent); background: color-mix(in srgb, var(--accent-bg) 55%, var(--panel-bg)); }
  .card.gone { opacity: 0.5; }
  .preview { display: -webkit-box; -webkit-line-clamp: 4; -webkit-box-orient: vertical; overflow: hidden; width: 100%; text-align: left; background: none; border: 0; padding: 0; margin: 0; color: var(--fg-dim); white-space: pre-wrap; line-height: 1.45; cursor: pointer; }
  .preview:hover { color: var(--fg); }
  .mbar { height: 3px; margin-top: 3px; background: var(--bg-3); } .mbar i { display: block; height: 100%; background: var(--accent); }
  .mon { color: var(--attn); font-size: 10.5px; margin-top: 2px; }
  .bgh { padding: 6px 10px 2px; font-size: 10px; text-transform: uppercase; letter-spacing: 0.12em; color: var(--fg-mute); border-top: 1px solid var(--line-2); }
  .bgh:first-child { border-top: 0; }
  .brow { display: flex; align-items: center; gap: 10px; width: 100%; padding: 5px 10px; background: none; border: 0; border-bottom: 1px solid var(--line-2); text-align: left; color: var(--fg-dim); }
  .brow:hover { background: var(--bg-2); }
  .brow.unread .hi { color: var(--accent); }
  .brow.gone { opacity: 0.5; }
  .pre-full { white-space: pre-wrap; overflow-wrap: anywhere; max-height: 55vh; overflow: auto; margin: 0; padding: 8px; background: var(--bg-2); border: 1px solid var(--line-2); color: var(--fg); font: inherit; line-height: 1.45; }
  .soul { max-height: 52vh; }
  @media (max-width: 820px) { .ctl { flex-wrap: nowrap; overflow-x: auto; scrollbar-width: none; white-space: nowrap; } .ctl::-webkit-scrollbar { display: none; } }
</style>
