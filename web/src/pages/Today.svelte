<script>
  import { S, call, listen, go, ago, until, openProposal } from '../lib/store.svelte.js';
  import Panel from '../lib/ui/Panel.svelte';
  import Button from '../lib/ui/Button.svelte';
  import Icon from '../lib/ui/Icon.svelte';
  import Empty from '../lib/ui/Empty.svelte';
  import Glyph from '../lib/ui/Glyph.svelte';
  import Segmented from '../lib/ui/Segmented.svelte';
  import TaskReview from '../lib/TaskReview.svelte';

  let d = $state(null);
  let busy = $state(false);

  async function load() {
    busy = true;
    const r = await call('today.get', {}, { quiet: true });
    busy = false;
    if (r) d = r;
  }
  $effect(() => {
    load();
    // any of these changing likely means this page is stale — cheap to just refetch rather than patch in place
    return listen('task.update', load), listen('briefing.new', load), listen('agents.update', load);
  });
  $effect(() => {
    const i = setInterval(load, 20000);
    return () => clearInterval(i);
  });

  // whole-page layout: the panels ("cards") or one list with a header per section — remembered per browser
  let layout = $state((() => { try { return localStorage.getItem('prism.todayLayout') === 'list' ? 'list' : 'cards'; } catch { return 'cards'; } })());
  $effect(() => { try { localStorage.setItem('prism.todayLayout', layout); } catch {} });
  const kindLabel = { waiting_input: 'Waiting for your answer', partial: 'Stopped early', failed: 'Failed', briefing: 'Briefings', hire: 'Hiring to confirm', proposal: 'Proposed changes', plugin: 'New tools to review', ingest: 'Documents to learn' };
  const attnGroups = $derived.by(() => {
    const m = new Map();
    for (const it of d?.needs_attention || []) { if (!m.has(it.kind)) m.set(it.kind, []); m.get(it.kind).push(it); }
    return [...m].map(([kind, items]) => ({ kind, label: kindLabel[kind] || kind, items }));
  });
  let reviewId = $state(0);
  async function dismissTask(it, e) { e.stopPropagation(); if (await call('tasks.ack', { id: Number(it.ref.slice(5)) })) load(); }
  const kindIcon = { partial: 'warn', failed: 'warn', waiting_input: 'warn', briefing: 'bell', hire: 'agents', proposal: 'edit', plugin: 'tools', task: 'check', ingest: 'doc' };

  function openRef(ref) {
    if (!ref) return;
    if (ref.startsWith('task:')) { go('tasks'); S.selectedTask = Number(ref.slice(5)); }
    else if (ref.startsWith('briefing:')) { go('autonomy'); }
    else if (ref.startsWith('ingest:')) { S.learnFile = ref.slice(7); go('memory'); }
    else if (ref.startsWith('hire:')) { go('agents'); S.selectedAgent = Number(ref.slice(5)); }
    else if (ref.startsWith('proposal:')) { go('agents'); openProposal(Number(ref.slice(9))); }
    else go(ref);
  }
  function openProject(p) { S.memoryBank = p.bank_id; go('memory'); }

  const empty = $derived(d && !d.needs_attention?.length && !d.working_on?.length && !d.produced?.length && !d.commitments?.length && !d.projects?.length);

  // live run per task id, so a "working on now" row can show its context-compaction count as it happens
  const runByTask = $derived.by(() => {
    const m = {};
    for (const r of Object.values(S.runs)) if (r.task) m[r.task] = r;
    return m;
  });
</script>

<div class="pg">
  <div class="bar">
    <Segmented size="sm" bind:value={layout} options={[{ value: 'cards', label: 'cards' }, { value: 'list', label: 'list' }]} />
    <span class="grow"></span>
    <Button size="sm" variant="ghost" onclick={load} loading={busy} title="Refresh"><Icon name="refresh" size={11} /></Button>
  </div>

  <div class="body scroll">
{#snippet attn(it)}
      <li class="row click" onclick={() => openRef(it.ref)}>
        <span class="ic attn"><Icon name={kindIcon[it.kind] || 'warn'} size={13} /></span>
        <div class="txt"><span class="hi">{it.title}</span>{#if it.sub}<span class="sub">{it.sub}</span>{/if}</div>
        {#if it.kind === 'partial' || it.kind === 'failed'}
          <Button size="sm" variant="accent" onclick={(e) => { e.stopPropagation(); reviewId = Number(it.ref.slice(5)); }}>Review</Button>
          <Button size="sm" variant="ghost" title="I've seen it — remove from this list" onclick={(e) => dismissTask(it, e)}>Dismiss</Button>
        {/if}
        <span class="when mute sm">{ago(it.at)}</span>
      </li>
    {/snippet}
    {#snippet working(w)}
      {@const compactions = runByTask[w.task_id]?.compactions}
      <li class="row click" onclick={() => { go('tasks'); S.selectedTask = w.task_id; }}>
        <span class="ic ok"><Glyph name={w.agent} size={14} /></span>
        <div class="txt"><span class="hi">{w.title || 'working…'}</span><span class="sub">{w.agent}</span></div>
        {#if compactions}<span class="compact-badge" title="{compactions} context compaction{compactions === 1 ? '' : 's'} — this task's context was getting long and was condensed">⟲ {compactions}</span>{/if}
        <span class="when mute sm">{ago(w.started)}</span>
      </li>
    {/snippet}
    {#snippet produced(it)}
      <li class="row click" onclick={() => openRef(it.ref)}>
        <span class="ic"><Icon name={kindIcon[it.kind] || 'check'} size={13} /></span>
        <div class="txt"><span class="hi">{it.title}</span>{#if it.sub}<span class="sub">{it.sub}</span>{/if}</div>
        <span class="when mute sm">{ago(it.at)}</span>
      </li>
    {/snippet}
    {#snippet coming(c)}
      <li class="row click" onclick={() => go('autonomy')}>
        <span class="ic"><Icon name={c.kind === 'cron' ? 'compact' : 'auto'} size={13} /></span>
        <div class="txt"><span class="hi">{c.title}</span></div>
        <span class="when mute sm">{until(c.due)}</span>
      </li>
    {/snippet}
    {#snippet project(p)}
      <li class="row click" onclick={() => openProject(p)}>
        <span class="ic"><Icon name="folder" size={13} /></span>
        <div class="txt"><span class="hi">{p.bank}</span><span class="sub">{p.facts} fact{p.facts === 1 ? '' : 's'}</span></div>
        <span class="when mute sm">idle {p.idle_days}d</span>
      </li>
    {/snippet}

    {#if !d}
      <Empty>loading…</Empty>
    {:else if empty}
      <Empty>nothing needs you right now — nothing is running, and nothing new was produced in the last day</Empty>
    {:else if layout === 'list'}
      <Panel flush>
        <ul class="list">
          {#if d.needs_attention.length}
            <li class="shead attn">Needs your attention <span class="mute">{d.needs_attention.length}</span></li>
            {#each attnGroups as g (g.kind)}
              {#if attnGroups.length > 1}<li class="ghead">{g.label} <span class="mute">{g.items.length}</span></li>{/if}
              {#each g.items as it}{@render attn(it)}{/each}
            {/each}
          {/if}
          {#if d.working_on.length}
            <li class="shead">Working on now <span class="mute">{d.working_on.length}</span></li>
            {#each d.working_on as w}{@render working(w)}{/each}
          {/if}
          {#if d.produced.length}
            <li class="shead">Produced today <span class="mute">{d.produced.length}</span></li>
            {#each d.produced as it}{@render produced(it)}{/each}
          {/if}
          {#if d.commitments.length}
            <li class="shead">Coming up <span class="mute">{d.commitments.length}</span></li>
            {#each d.commitments as c}{@render coming(c)}{/each}
          {/if}
          {#if d.projects.length}
            <li class="shead">Projects awaiting a next step <span class="mute">{d.projects.length}</span></li>
            {#each d.projects as p}{@render project(p)}{/each}
          {/if}
        </ul>
      </Panel>
    {:else}
      <div class="cols">
        <Panel title="Needs your attention{d.needs_attention.length ? ` (${d.needs_attention.length})` : ''}" flush>
          {#if !d.needs_attention.length}<Empty>all clear</Empty>
          {:else}<ul class="list">{#each d.needs_attention as it}{@render attn(it)}{/each}</ul>{/if}
        </Panel>
        <Panel title="Working on now{d.working_on.length ? ` (${d.working_on.length})` : ''}" flush>
          {#if !d.working_on.length}<Empty>nothing running</Empty>
          {:else}<ul class="list">{#each d.working_on as w}{@render working(w)}{/each}</ul>{/if}
        </Panel>
        <Panel title="Produced today{d.produced.length ? ` (${d.produced.length})` : ''}" flush>
          {#if !d.produced.length}<Empty>nothing finished in the last day</Empty>
          {:else}<ul class="list">{#each d.produced as it}{@render produced(it)}{/each}</ul>{/if}
        </Panel>
        <Panel title="Coming up{d.commitments.length ? ` (${d.commitments.length})` : ''}" flush>
          {#if !d.commitments.length}<Empty>nothing scheduled in the next 2 days</Empty>
          {:else}<ul class="list">{#each d.commitments as c}{@render coming(c)}{/each}</ul>{/if}
        </Panel>
        <Panel title="Projects awaiting a next step{d.projects.length ? ` (${d.projects.length})` : ''}" flush>
          {#if !d.projects.length}<Empty>no active project has gone quiet</Empty>
          {:else}<ul class="list">{#each d.projects as p}{@render project(p)}{/each}</ul>{/if}
        </Panel>
      </div>
    {/if}
  </div>
</div>

<TaskReview taskId={reviewId} onclose={() => (reviewId = 0)} ondone={load} />

<style>
  .ghead { list-style: none; padding: 6px 10px 2px; font-size: 10px; text-transform: uppercase; letter-spacing: 0.12em; color: var(--fg-mute); border-top: 1px solid var(--line-2); }
  .ghead:first-child { border-top: 0; }
  .shead { list-style: none; padding: 9px 10px 4px; font-size: 11px; font-weight: 700; text-transform: uppercase; letter-spacing: 0.14em; color: var(--fg-dim); background: var(--bg-1); border-top: 1px solid var(--line-2); border-bottom: 1px solid var(--line-2); }
  .shead:first-child { border-top: 0; }
  .shead.attn { color: var(--attn-hi); }
  .pg { display: flex; flex-direction: column; gap: 6px; height: 100%; min-height: 0; }
  .bar { display: flex; align-items: center; gap: 8px; flex: none; }
  .body { flex: 1; min-height: 0; }
  .cols { display: grid; grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); gap: 8px; align-items: start; }
  .list { list-style: none; margin: 0; padding: 0; }
  .row { display: flex; align-items: center; gap: 8px; padding: 7px 10px; border-bottom: 1px solid var(--line); }
  .row:last-child { border-bottom: none; }
  .row.click { cursor: pointer; }
  .row.click:hover { background: var(--bg-1); }
  .ic { flex: none; display: flex; color: var(--fg-mute); }
  .ic.attn { color: var(--attn-hi); }
  .ic.ok { color: var(--ok); }
  .txt { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 1px; }
  .txt .hi { font-size: var(--fs-sm); color: var(--fg-hi); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .txt .sub { font-size: 11px; color: var(--fg-dim); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .when { flex: none; white-space: nowrap; }
  .compact-badge { flex: none; font-size: 10px; color: var(--fg-mute); border: 1px solid var(--line-2); border-radius: var(--r); padding: 0 4px; }
</style>
