<script>
  import { untrack } from 'svelte';
  import { nearEnd } from '../lib/nearend.js';
  import MemoryUsed from '../lib/MemoryUsed.svelte';
  import { S, call, listen, toast, ago, fmtTokens, confirmBox, iconOf } from '../lib/store.svelte.js';
  import Panel from '../lib/ui/Panel.svelte';
  import Glyph from '../lib/ui/Glyph.svelte';
  import Button from '../lib/ui/Button.svelte';
  import Select from '../lib/ui/Select.svelte';
  import Led from '../lib/ui/Led.svelte';
  import Transcript from '../lib/Transcript.svelte';
  import Modal from '../lib/ui/Modal.svelte';
  import TaskReview from '../lib/TaskReview.svelte';
  import Badge from '../lib/ui/Badge.svelte';
  import Field from '../lib/ui/Field.svelte';
  import Empty from '../lib/ui/Empty.svelte';
  import Icon from '../lib/ui/Icon.svelte';

  let tasks = $state([]);
  let status = $state('');
  let agent = $state('');
  let collapsed = $state({});
  let detail = $state(null);
  let detailOpen = $state(false);
  let tick = $state(0);

  let limit = $state(200);
  let exhausted = $state(false);
  async function load() {
    const r = await call('tasks.list', { status, agent, limit }, { quiet: true });
    if (r) { tasks = r; exhausted = r.length < limit; }
  }
  // older tasks load as you scroll to the end of the list
  async function loadMore() { if (exhausted || limit >= 2000) return; limit += 200; await load(); }
  $effect(() => { status; agent; untrack(() => { limit = 200; load(); }); });
  $effect(() => listen('task.update', (t) => {
    if (status && t.status !== status) { tasks = tasks.filter((x) => x.id !== t.id); return; }
    if (agent && t.to_agent !== agent) return;
    const i = tasks.findIndex((x) => x.id === t.id);
    if (i >= 0) tasks[i] = t; else tasks.unshift(t);
    if (detail?.task?.id === t.id) detail.task = t;
  }));
  $effect(() => { const i = setInterval(() => tick++, 5000); return () => clearInterval(i); });

  const roots = $derived(tasks.filter((t) => t.depth === 0 || !tasks.some((x) => x.id === t.root_id && x.id !== t.id)).sort((a, b) => b.id - a.id));
  const kids = (r) => tasks.filter((t) => t.root_id === r.id && t.id !== r.id).sort((a, b) => a.id - b.id);
  const rows = $derived(roots.flatMap((r) => [{ t: r, lvl: 0 }, ...(collapsed[r.id] ? [] : kids(r).map((k) => ({ t: k, lvl: k.depth || 1 })))]));

  const led = (s) => ({ queued: 'standby', running: 'ok', waiting_input: 'attention', partial: 'attention', done: 'ok', failed: 'error', cancelled: 'off' })[s] || 'off';
  const brief = (a) => (a.description || a.group || '').replace(/\s+/g, ' ').split(/(?<=[.!?])\s/)[0].slice(0, 70);
  const agentOpts = $derived([{ value: '', label: 'all agents' }, ...S.agents.map((a) => ({ value: a.name, label: a.name, hint: brief(a) }))]);
  const statusOpts = ['', 'queued', 'running', 'waiting_input', 'partial', 'done', 'failed', 'cancelled'].map((s) => ({ value: s, label: s || 'all statuses' }));

  async function open(t) {
    detail = await call('tasks.get', { id: t.id });
    if (detail) detailOpen = true;
  }
  // a Today-page link sets S.selectedTask before navigating here; open it directly rather than making the
  // person find it in the list. Left set afterwards (like S.selectedAgent elsewhere) — harmless, and means
  // returning to Tasks later still shows what was last linked to.
  $effect(() => { if (S.selectedTask != null) untrack(() => open({ id: S.selectedTask })); });
  async function cancel(t, e) {
    e?.stopPropagation();
    if (await call('tasks.cancel', { id: t.id })) toast(`Task #${t.id} cancelled`);
  }
  let reviewId = $state(0);
  const rerunnable = (t) => t.status === 'cancelled' && t.from_kind === 'user' && t.to_agent === 'Atlas';
  async function rerun(t, e) {
    e?.stopPropagation();
    const r = await call('tasks.rerun', { id: t.id });
    if (r) toast(`Rerunning as task #${r.id}`);
  }
  async function saveRoutine(t, e) {
    e?.stopPropagation();
    const r = await call('tasks.save_as_routine', { id: t.id });
    if (r) toast(`Asked Daedalus to turn this into a skill (task #${r.id})`);
  }
  async function prune() {
    if (await confirmBox({ title: 'Prune tasks', text: 'Delete finished tasks older than 7 days?', ok: 'Prune' })) {
      const n = await call('tasks.prune', { days: 7 });
      toast(`${n} tasks removed`); load();
    }
  }
</script>

<div class="pg">
{#if S.narrow}
  <div class="mchips">
    {#each [['', 'All'], ['running', 'Running'], ['waiting_input', 'Waiting'], ['partial', 'Stopped'], ['done', 'Done'], ['failed', 'Failed']] as [v, l]}
      <button type="button" class="mchip" class:on={status === v} onclick={() => (status = v)}>{l}</button>
    {/each}
  </div>
  <div class="mlist scroll" use:nearEnd={loadMore}>
    {#each roots as t (t.id)}
      {@const n = kids(t).length}
      <div class="mcard" role="button" tabindex="0" onclick={() => open(t)} onkeydown={(e) => e.key === 'Enter' && open(t)}>
        <div class="mtop">
          <Led state={led(t.status)} pulse={t.status === 'running' || t.status === 'waiting_input'} size={9} />
          <span class="hi"><Glyph name={t.to_agent} /> {t.to_agent}</span>
          <span class="dim sm">{t.from_kind === 'agent' ? t.from_name : t.from_kind}</span>
          {#if n}<span class="mute sm">+{n}</span>{/if}
          <span class="grow"></span><span class="mute sm">#{t.id} · {ago(t.created_at)}</span>
        </div>
        <div class="mtitle">{t.title}</div>
        {#if t.question}<div class="attn sm mline">? {t.question}</div>
        {:else if t.status === 'partial'}<div class="attn sm mline">⚠ {t.error}</div>
        {:else if t.status === 'failed'}<div class="err sm mline">{t.error}</div>{/if}
        <div class="mbot">
          <span class={t.status === 'failed' ? 'err' : t.status === 'waiting_input' || t.status === 'partial' ? 'attn' : 'dim'} style="font-size:var(--fs-sm)">{t.status.replace('_', ' ')}</span>
          <span class="mute sm">{fmtTokens(t.tokens_in)}↑ {fmtTokens(t.tokens_out)}↓</span><span class="grow"></span>
          {#if t.status === 'queued' || t.status === 'running' || t.status === 'waiting_input'}<Button size="sm" variant="danger" onclick={(e) => cancel(t, e)}>Stop</Button>
          {:else if rerunnable(t)}<Button size="sm" variant="ghost" onclick={(e) => rerun(t, e)}>Rerun</Button>{/if}
        </div>
      </div>
    {:else}<Empty>the queue is empty</Empty>{/each}
  </div>
{:else}
  <div class="bar">
    <div class="w"><Select bind:value={status} options={statusOpts} size="sm" /></div>
    <div class="w"><Select bind:value={agent} options={agentOpts} size="sm" searchable /></div>
    <span class="grow"></span>
    <span class="sm dim">{tasks.length} tasks</span>
    <Button size="sm" variant="ghost" onclick={load}><Icon name="refresh" size={11} /></Button>
    <Button size="sm" variant="ghost" onclick={prune}>Prune</Button>
  </div>
  <Panel title="Task queue" flush grow>
    <div class="scroll" use:nearEnd={loadMore}>
      <table class="t">
        <thead><tr><th style="width:56px">#</th><th>Flow</th><th>Task</th><th>Status</th><th style="width:90px">Tokens</th><th style="width:44px">Age</th><th style="width:60px"></th></tr></thead>
        <tbody>
          {#each rows as { t, lvl } (t.id)}
            {@const n = lvl === 0 ? kids(t).length : 0}
            <tr class="click" class:sel={detail?.task?.id === t.id && detailOpen} onclick={() => open(t)}>
              <td class="mute">
                {#if n}<button class="car" onclick={(e) => { e.stopPropagation(); collapsed[t.id] = !collapsed[t.id]; }}>{collapsed[t.id] ? '▸' : '▾'}</button>{/if}#{t.id}
              </td>
              <td class="nowrap" style="padding-left:{8 + lvl * 14}px">
                <span class="dim">{t.from_kind === 'agent' ? t.from_name : t.from_kind}</span> <span class="mute">→</span> <span class="hi"><Glyph name={t.to_agent} /> {t.to_agent}</span>{#if n}<span class="mute sm"> +{n}</span>{/if}
              </td>
              <td class="ellipsis" style="max-width:340px">{t.title}{#if t.question}<div class="attn sm ellipsis">? {t.question}</div>{:else if t.status === 'partial'}<div class="attn sm ellipsis">⚠ {t.error}</div>{/if}</td>
              <td class="nowrap"><Led state={led(t.status)} pulse={t.status === 'running' || t.status === 'waiting_input'} size={8} /> <span class={t.status === 'failed' ? 'err' : t.status === 'waiting_input' || t.status === 'partial' ? 'attn' : 'dim'}>{t.status.replace('_', ' ')}</span></td>
              <td class="mute sm">{fmtTokens(t.tokens_in)}↑ {fmtTokens(t.tokens_out)}↓</td>
              <td class="mute sm">{ago(t.created_at)}</td>
              <td>
                {#if t.status === 'queued' || t.status === 'running' || t.status === 'waiting_input'}<Button size="sm" variant="danger" onclick={(e) => cancel(t, e)}>Stop</Button>
                {:else if rerunnable(t)}<Button size="sm" variant="ghost" onclick={(e) => rerun(t, e)}>Rerun</Button>
                {:else if t.status === 'done' && t.to_agent !== 'Daedalus'}<Button size="sm" variant="ghost" onclick={(e) => saveRoutine(t, e)}>Save as routine</Button>{/if}
              </td>
            </tr>
          {:else}
            <tr><td colspan="7"><Empty>the queue is empty</Empty></td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  </Panel>
{/if}
</div>

<Modal bind:open={detailOpen} title="Task #{detail?.task?.id} · {detail?.task?.to_agent}" width={860}>
  {#if detail}
    {@const t = detail.task}
    <MemoryUsed taskId={t.id} />
    <div class="row wrap gap-12 sm">
      <span><Led state={led(t.status)} size={8} /> {t.status}</span><span class="dim">from {t.from_kind}{t.from_name && t.from_kind !== 'user' ? ' · ' + t.from_name : ''}</span>
      <span class="dim">depth {t.depth}</span><span class="dim">{fmtTokens(t.tokens_in)}↑ {fmtTokens(t.tokens_out)}↓</span>
      {#if t.status === 'partial' || t.status === 'failed'}<span class="grow"></span>{#if t.acknowledged_at}<Badge tone="mute">acknowledged</Badge>{/if}{#if !t.acknowledged_at}<Button size="sm" variant="ghost" title="Nothing more to do here — remove it from Needs attention" onclick={(e) => { e.stopPropagation(); call('tasks.ack', { id: t.id }).then(() => load()); }}>Dismiss</Button>{/if}<Button size="sm" variant="accent" onclick={() => (reviewId = t.id)}>Review…</Button>
      {:else if rerunnable(t)}<span class="grow"></span><Button size="sm" variant="primary" onclick={(e) => rerun(t, e)}>Rerun</Button>
      {:else if t.status === 'done' && t.to_agent !== 'Daedalus'}<span class="grow"></span><Button size="sm" variant="ghost" onclick={(e) => saveRoutine(t, e)}>Save as routine</Button>{/if}
    </div>
    <Field label="Input"><pre>{t.input}</pre></Field>
    {#if t.question}<Field label="Waiting for"><pre class="attn">{t.question}</pre></Field>{/if}
    {#if t.result}<details class="fold" open><summary>Result</summary><pre>{t.result}</pre></details>{/if}
    {#if t.error}<Field label="Error"><pre class="err">{t.error}</pre></Field>{/if}
    {#if detail.summary}
      {@const sm = detail.summary}
      <Field label="Summary">
        <div class="summary sm">
          <div><b>Goal:</b> {sm.goal}</div>
          {#if sm.decisions}<div><b>Decisions:</b> {sm.decisions}</div>{/if}
          {#if sm.attempts}<div><b>Attempts:</b> {sm.attempts}</div>{/if}
          <div><b>Outcome:</b> {sm.outcome}</div>
          {#if sm.unfinished}<div><b>Unfinished:</b> {sm.unfinished}</div>{/if}
        </div>
      </Field>
    {/if}
    {#if detail.transcript?.length}
      <details class="fold"><summary>Transcript ({detail.transcript.length} messages)</summary>
        <Transcript messages={detail.transcript} />
      </details>
    {/if}
  {/if}
</Modal>

<TaskReview taskId={reviewId} onclose={() => (reviewId = 0)} ondone={() => { detailOpen = false; load(); }} />

<style>
  .mchips { display: flex; gap: 6px; overflow-x: auto; flex: none; scrollbar-width: none; }
  .mchips::-webkit-scrollbar { display: none; }
  .mchip { flex: none; min-height: 36px; padding: 0 14px; background: var(--bg-1); border: 1px solid var(--line-2); color: var(--fg-dim); font-size: var(--fs-sm); white-space: nowrap; }
  .mchip.on { color: var(--fg-hi); border-color: var(--accent); background: linear-gradient(180deg, rgba(62, 232, 166, 0.14), transparent); }
  .mlist { flex: 1; min-height: 0; overflow-y: auto; display: flex; flex-direction: column; gap: 8px; padding-bottom: 8px; }
  .mcard { flex: none; padding: 10px 12px; background: var(--panel-bg); border: 1px solid var(--line-2); display: flex; flex-direction: column; gap: 6px; cursor: pointer; }
  .mcard:active { background: var(--bg-2); }
  .mtop, .mbot { display: flex; align-items: center; gap: 8px; }
  .mtitle { color: var(--fg-hi); line-height: 1.4; overflow-wrap: anywhere; display: -webkit-box; -webkit-line-clamp: 3; line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden; }
  .mline { overflow-wrap: anywhere; display: -webkit-box; -webkit-line-clamp: 2; line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
  .pg { display: flex; flex-direction: column; gap: 6px; height: 100%; min-height: 0; }
  .bar { display: flex; align-items: center; gap: 8px; flex: none; }
  .w { width: 190px; }
  .car { background: none; border: 0; color: var(--fg-mute); padding: 0 3px 0 0; width: 16px; }
  .summary div + div { margin-top: 6px; }
  .fold { border: 1px solid var(--line-2); padding: 4px 8px; }
  .fold > summary { cursor: pointer; color: var(--fg-dim); font-size: var(--fs-sm); text-transform: uppercase; letter-spacing: 0.08em; }
  .fold[open] > summary { margin-bottom: 6px; }
</style>
