<script>
  // How PRISM works like a team with a lead: runbooks juniors follow, the decision log, the reviewer's second pair of eyes and the weekly standup.
  import { S, call, toast, confirmBox, loadSetting, saveSetting, ago, stamp } from './store.svelte.js';
  import Segmented from './ui/Segmented.svelte';
  import Button from './ui/Button.svelte';
  import Badge from './ui/Badge.svelte';
  import Switch from './ui/Switch.svelte';
  import Textarea from './ui/Textarea.svelte';
  import Input from './ui/Input.svelte';
  import Modal from './ui/Modal.svelte';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import Empty from './ui/Empty.svelte';

  let view = $state('standup');

  // ── runbooks ──
  let books = $state([]);
  let edit = $state(null);
  let auto = $state({ runbooks_off: false, audit_one_in: 0 });
  const loadBooks = async () => { books = (await call('runbooks.list', {}, { quiet: true })) || []; };
  const tone = (s) => ({ active: 'ok', learning: 'attn', stale: 'err', off: 'mute' })[s] || 'mute';
  const hint = (s) => ({ active: 'the small model follows it', learning: 'the main model still does this job, to learn how', stale: 'it failed twice: the main model has the job back and will rewrite it', off: 'switched off' })[s] || '';
  async function saveBook() { if (await call('runbooks.save', { id: edit.id, steps: edit.steps, status: edit.status })) { edit = null; loadBooks(); } }
  async function delBook(b) { if (await confirmBox(`Forget the runbook for “${b.title}”? The job goes back to the main model and learns again.`)) { await call('runbooks.delete', { id: b.id }); loadBooks(); } }
  async function saveAuto() { const cur = await loadSetting('autonomy', {}); await saveSetting('autonomy', { ...cur, runbooks_off: auto.runbooks_off, audit_one_in: Number(auto.audit_one_in) }, 'Saved'); }

  // ── decisions ──
  let decisions = $state([]);
  let dfilter = $state('active');
  let nd = $state(null);
  const loadDec = async () => { decisions = (await call('decisions.list', { status: dfilter === 'all' ? '' : dfilter }, { quiet: true })) || []; };
  async function addDec() { if (await call('decisions.add', { title: nd.title, decision: nd.decision, why: nd.why, review_in_days: Number(nd.days) })) { nd = null; loadDec(); } }
  async function decSet(d, status) { await call('decisions.status', { id: d.id, status }); loadDec(); }
  async function decExtend(d) { await call('decisions.extend', { id: d.id, days: 90 }); toast('Still stands — looking again in 90 days'); loadDec(); }
  async function decDel(d) { if (await confirmBox(`Delete the decision “${d.title}”?`)) { await call('decisions.delete', { id: d.id }); loadDec(); } }

  // ── reviewer ──
  let rv = $state({ enabled: false, always_ask: [] });
  let rlog = $state([]);
  const loadRv = async () => { rlog = (await call('review.log', {}, { quiet: true })) || []; };
  const saveRv = () => saveSetting('review', { enabled: rv.enabled, always_ask: rv.always_ask }, 'Saved');
  const vtone = (v) => ({ approve: 'ok', deny: 'err', ask: 'attn' })[v] || 'mute';

  // ── standup ──
  let ups = $state([]);
  let su = $state({ off: false, weekday: 1, hour: 9 });
  let running = $state(false);
  const loadUps = async () => { ups = (await call('standup.list', {}, { quiet: true })) || []; };
  const saveSu = () => saveSetting('standup', { off: su.off, weekday: Number(su.weekday), hour: Number(su.hour) }, 'Saved');
  async function runUp() { running = true; const r = await call('standup.run', {}); running = false; if (r) { toast('Standup written'); loadUps(); } }
  const days = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'].map((l, i) => ({ value: String(i), label: l }));

  $effect(() => {
    loadBooks(); loadRv(); loadUps();
    loadSetting('autonomy', { runbooks_off: false, audit_one_in: 0 }).then((c) => (auto = { runbooks_off: !!c.runbooks_off, audit_one_in: c.audit_one_in || 0 }));
    loadSetting('review', { enabled: false, always_ask: [] }).then((c) => (rv = { enabled: !!c.enabled, always_ask: c.always_ask || [] }));
    loadSetting('standup', { off: false, weekday: 1, hour: 9 }).then((c) => (su = { off: !!c.off, weekday: c.weekday ?? 1, hour: c.hour ?? 9 }));
  });
  $effect(() => { dfilter; loadDec(); });
</script>

<div class="body scroll">
  <Segmented size="sm" bind:value={view} options={[{ value: 'standup', label: 'Standup' }, { value: 'runbooks', label: 'Runbooks' }, { value: 'decisions', label: 'Decisions' }, { value: 'review', label: 'Reviewer' }]} />

  {#if view === 'standup'}
    <div class="sm mute" style="margin:8px 0">Once a week the team reports like a team: what each agent got done, what failed, what you rated down, which runbooks changed, how long the models were out, which decisions are due for another look. A pattern of thumbs-down on one agent's results (rate them in Tasks) puts a review on Metis's desk.</div>
    <div class="row wrap gap-8" style="margin-bottom:8px">
      <Switch checked={!su.off} label="weekly standup" onchange={(v) => { su.off = !v; saveSu(); }} />
      <Select size="sm" value={String(su.weekday)} options={days} onchange={(v) => { su.weekday = Number(v); saveSu(); }} />
      <span class="sm mute">at</span>
      <Select size="sm" value={String(su.hour)} options={Array.from({ length: 24 }, (_, h) => ({ value: String(h), label: String(h).padStart(2, '0') + ':00' }))} onchange={(v) => { su.hour = Number(v); saveSu(); }} />
      <span class="grow"></span>
      <Button variant="primary" loading={running} onclick={runUp}>Write one now</Button>
    </div>
    {#each ups as u (u.id)}
      <div class="card"><div class="sm mute">{stamp(u.created_at)} · {u.stats?.tasks ?? 0} tasks</div><div class="pre">{u.text}</div></div>
    {:else}<Empty>No standup yet — the first one is written on the chosen day, or write one now.</Empty>{/each}

  {:else if view === 'runbooks'}
    <div class="sm mute" style="margin:8px 0">Small models drift; a real team solves that with a procedure. A routine job (schedule, standing watch) runs on the main model until it has succeeded twice; then the main model writes down how, and the small model follows that from then on. Two failures in a row send the job back to the main model, which rewrites the runbook. Now and then the main model quietly re-checks a small-model run.</div>
    <div class="row wrap gap-8" style="margin-bottom:8px">
      <Switch checked={!auto.runbooks_off} label="learn runbooks" onchange={(v) => { auto.runbooks_off = !v; saveAuto(); }} />
      <span class="sm mute" title="The main model re-checks one small-model run in N (the first two after a runbook goes live are always checked). 0 here means the default of 8; -1 never.">re-check one in</span>
      <input class="num" type="number" min="-1" bind:value={auto.audit_one_in} onchange={saveAuto} />
      <span class="sm mute">runs (0 = 8, −1 = never)</span>
    </div>
    {#each books as b (b.id)}
      <div class="card">
        <div class="row wrap gap-8"><b>{b.title || b.key}</b><Badge tone={tone(b.status)} title={hint(b.status)}>{b.status}</Badge><span class="sm mute">{b.agent} · v{b.version} · {b.wins} good · {b.losses} bad · {ago(b.updated_at)}</span><span class="grow"></span>
          <Button size="sm" variant="ghost" onclick={() => (edit = { ...b })}>{b.steps ? 'Edit' : 'Write'}</Button><Button size="sm" variant="ghost" onclick={() => delBook(b)}>Forget</Button></div>
        {#if b.status === 'learning'}<div class="sm mute">{b.learn_wins} of 2 successful main-model runs seen</div>{/if}
        {#if b.note && b.status === 'stale'}<div class="sm err">stopped working: {b.note}</div>{/if}
        {#if b.steps}<div class="pre">{b.steps}</div>{/if}
      </div>
    {:else}<Empty>No routine job has been through a run yet. They appear here as schedules and watches run.</Empty>{/each}

  {:else if view === 'decisions'}
    <div class="sm mute" style="margin:8px 0">Choices that shape later work, with the reason and a date to look again — so a change of mind starts from why it was decided. Agents record them (and search them before changing a settled approach); you can add your own.</div>
    <div class="row wrap gap-8" style="margin-bottom:8px">
      <Segmented size="sm" bind:value={dfilter} options={[{ value: 'active', label: 'active' }, { value: 'due', label: 'review due' }, { value: 'closed', label: 'closed' }, { value: 'all', label: 'all' }]} />
      <span class="grow"></span><Button variant="primary" onclick={() => (nd = { title: '', decision: '', why: '', days: 90 })}>Add decision</Button>
    </div>
    {#each decisions as d (d.id)}
      <div class="card">
        <div class="row wrap gap-8"><b>{d.title}</b>{#if d.due}<Badge tone="attn">review due</Badge>{/if}{#if d.status !== 'active'}<Badge tone="mute">{d.status}</Badge>{/if}<span class="sm mute">{d.by} · {stamp(d.made_at)}{d.review_at ? ' · look again ' + stamp(d.review_at) : ''}</span><span class="grow"></span>
          {#if d.status === 'active'}{#if d.due}<Button size="sm" variant="ghost" onclick={() => decExtend(d)}>Still stands</Button>{/if}<Button size="sm" variant="ghost" onclick={() => decSet(d, 'closed')}>Close</Button>{:else}<Button size="sm" variant="ghost" onclick={() => decSet(d, 'active')}>Reopen</Button>{/if}<Button size="sm" variant="ghost" onclick={() => decDel(d)}>Delete</Button></div>
        <div>{d.decision}</div>
        {#if d.rationale}<div class="sm mute">because: {d.rationale}</div>{/if}
      </div>
    {:else}<Empty>Nothing here.</Empty>{/each}

  {:else}
    <div class="sm mute" style="margin:8px 0">Approval prompts for small actions train everyone to click “allow” without reading — which makes the prompts that matter worthless. With the reviewer on, a tool call that would ask you is first put to the main model, which knows your request: it approves what clearly follows from it, refuses what clearly does not, and passes the rest to you as before. Never reviewed (always asked): anything while web/mail content is in the turn, programs that run code, and tools that send, delete, pay, publish or install.</div>
    <div class="row wrap gap-8" style="margin-bottom:8px"><Switch bind:checked={rv.enabled} label="reviewer on" onchange={saveRv} /><span class="grow"></span><Button size="sm" variant="ghost" onclick={loadRv}>Refresh</Button></div>
    <Field label="Always ask me for these tools too" hint="tool names, comma separated"><Input value={rv.always_ask.join(', ')} onchange={(e) => { rv.always_ask = (e.target?.value ?? e).toString().split(',').map((x) => x.trim()).filter(Boolean); saveRv(); }} /></Field>
    <h4>What it decided</h4>
    {#each rlog as r (r.id)}
      <div class="row wrap gap-8 sm"><Badge tone={vtone(r.verdict)}>{r.verdict === 'ask' ? 'passed to you' : r.verdict === 'approve' ? 'approved' : 'refused'}</Badge><span class="mute">{ago(r.at)}</span><b>{r.agent}</b><code>{r.tool}</code><span class="dim grow ellipsis" title={r.args}>{r.reason}</span></div>
    {:else}<div class="sm mute">nothing reviewed yet</div>{/each}
  {/if}
</div>

<Modal open={!!edit} title={edit?.title || 'Runbook'} width={720} onclose={() => (edit = null)}>
  {#if edit}
    <div class="sm mute">{edit.key} · {edit.agent}</div>
    <Textarea bind:value={edit.steps} rows={14} mono={false} placeholder="Numbered steps the small model follows…" />
    <div class="row gap-8" style="margin-top:6px"><span class="sm mute">after saving</span><Segmented size="sm" bind:value={edit.status} options={[{ value: 'active', label: 'use it' }, { value: 'off', label: 'off' }, { value: 'learning', label: 'relearn' }]} /></div>
  {/if}
  {#snippet footer()}<Button variant="ghost" onclick={() => (edit = null)}>Cancel</Button><Button variant="primary" onclick={saveBook}>Save</Button>{/snippet}
</Modal>

<Modal open={!!nd} title="Add a decision" width={560} onclose={() => (nd = null)}>
  {#if nd}
    <Field label="Title"><Input bind:value={nd.title} /></Field>
    <Field label="What was decided"><Textarea bind:value={nd.decision} rows={3} mono={false} /></Field>
    <Field label="Why"><Textarea bind:value={nd.why} rows={3} mono={false} /></Field>
    <Field label="Look at it again in (days, 0 = never)"><Input type="number" bind:value={nd.days} /></Field>
  {/if}
  {#snippet footer()}<Button variant="ghost" onclick={() => (nd = null)}>Cancel</Button><Button variant="primary" disabled={!nd?.title?.trim() || !nd?.decision?.trim()} onclick={addDec}>Save</Button>{/snippet}
</Modal>

<style>
  .card { border: 1px solid var(--line); background: var(--bg-1); padding: 8px 10px; margin-bottom: 8px; }
  .pre { white-space: pre-wrap; overflow-wrap: anywhere; color: var(--fg); line-height: 1.45; margin-top: 4px; }
  .num { width: 56px; background: var(--bg); color: var(--fg); border: 1px solid var(--line-2); padding: 1px 4px; }
  h4 { margin: 12px 0 4px; }
  .err { color: var(--err); }
</style>
