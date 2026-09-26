<script>
  // Phone layout of Today: one column of full-width cards, big tap targets, what needs you first.
  import { S, call, listen, ago, until, openRef } from '../lib/store.svelte.js';
  import Icon from '../lib/ui/Icon.svelte';
  import Glyph from '../lib/ui/Glyph.svelte';
  import Button from '../lib/ui/Button.svelte';
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
    const a = listen('task.update', load), b = listen('briefing.new', load), c = listen('agents.update', load);
    const i = setInterval(load, 20000);
    return () => { a(); b(); c(); clearInterval(i); };
  });

  const kindIcon = { partial: 'warn', failed: 'warn', waiting_input: 'warn', briefing: 'bell', hire: 'agents', proposal: 'edit', plugin: 'tools', task: 'check', ingest: 'doc' };
  let reviewId = $state(0);
  async function dismissTask(it, e) { e.stopPropagation(); if (await call('tasks.ack', { id: Number(it.ref.slice(5)) })) load(); }

  const originLabel = { auto: 'Autonomous work', briefing: 'Briefings', request: 'Your requests' };
  const prod = $derived.by(() => {
    const m = { auto: [], briefing: [], request: [] };
    for (const it of d?.produced || []) (m[it.origin] || m.auto).push(it);
    return ['auto', 'briefing', 'request'].filter((k) => m[k].length).map((k) => ({ key: k, label: originLabel[k], items: m[k] }));
  });
  let open = $state({});
  const SHOW = 3;
</script>

<div class="mt">
  {#if !d}
    <div class="empty">loading…</div>
  {:else}
    {#if d.needs_attention.length}
      <section class="card attn">
        <h3>Needs you <b>{d.needs_attention.length}</b></h3>
        {#each d.needs_attention as it}
          <div class="it" role="button" tabindex="0" onclick={() => openRef(it.ref)} onkeydown={(e) => e.key === 'Enter' && openRef(it.ref)}>
            <span class="ic"><Icon name={kindIcon[it.kind] || 'warn'} size={16} /></span>
            <div class="tx"><div class="t">{it.title}</div>{#if it.sub}<div class="s">{it.sub}</div>{/if}<div class="w">{ago(it.at)}</div></div>
            {#if it.kind === 'partial' || it.kind === 'failed'}
              <div class="btns"><Button size="sm" variant="accent" onclick={(e) => { e.stopPropagation(); reviewId = Number(it.ref.slice(5)); }}>Review</Button><Button size="sm" variant="ghost" onclick={(e) => dismissTask(it, e)}>Dismiss</Button></div>
            {/if}
          </div>
        {/each}
      </section>
    {:else}
      <section class="card ok"><h3>All clear</h3><div class="s pad">Nothing needs you right now.</div></section>
    {/if}

    {#if d.working_on.length}
      <section class="card">
        <h3>Working now <b>{d.working_on.length}</b></h3>
        {#each d.working_on as w}
          <div class="it" role="button" tabindex="0" onclick={() => { S.selectedTask = w.task_id; S.page = 'tasks'; }} onkeydown={(e) => e.key === 'Enter' && (S.page = 'tasks')}>
            <span class="ic ok"><Glyph name={w.agent} size={16} /></span>
            <div class="tx"><div class="t">{w.title || 'working…'}</div><div class="w">{w.agent} · {ago(w.started)}</div></div>
          </div>
        {/each}
      </section>
    {/if}

    {#if d.commitments.length}
      <section class="card">
        <h3>Coming up <b>{d.commitments.length}</b></h3>
        {#each d.commitments as c}
          <div class="it" role="button" tabindex="0" onclick={() => { S.autonomyTab = c.kind === 'cron' ? 'cron' : 'intents'; S.page = 'autonomy'; }} onkeydown={() => {}}>
            <span class="ic"><Icon name={c.kind === 'cron' ? 'compact' : 'auto'} size={16} /></span>
            <div class="tx"><div class="t">{c.title}</div></div><span class="w">{until(c.due)}</span>
          </div>
        {/each}
      </section>
    {/if}

    {#each prod as g}
      <section class="card">
        <h3>{g.label} <b>{g.items.length}</b></h3>
        {#each open[g.key] ? g.items : g.items.slice(0, SHOW) as it}
          <div class="it" role="button" tabindex="0" onclick={() => openRef(it.ref)} onkeydown={(e) => e.key === 'Enter' && openRef(it.ref)}>
            <span class="ic"><Icon name={kindIcon[it.kind] || 'check'} size={16} /></span>
            <div class="tx"><div class="t">{it.title}</div>{#if it.sub}<div class="s">{it.origin === 'request' ? '↳ ' : ''}{it.sub}</div>{/if}</div><span class="w">{ago(it.at)}</span>
          </div>
        {/each}
        {#if g.items.length > SHOW}<button type="button" class="more" onclick={() => (open[g.key] = !open[g.key])}>{open[g.key] ? 'Show fewer' : `Show ${g.items.length - SHOW} more`}</button>{/if}
      </section>
    {/each}

    {#if d.projects.length}
      <section class="card">
        <h3>Projects waiting <b>{d.projects.length}</b></h3>
        {#each d.projects as p}
          <div class="it" role="button" tabindex="0" onclick={() => { S.memoryBank = p.bank_id; S.page = 'memory'; }} onkeydown={() => {}}>
            <span class="ic"><Icon name="folder" size={16} /></span>
            <div class="tx"><div class="t">{p.bank}</div><div class="s">{p.facts} fact{p.facts === 1 ? '' : 's'}</div></div><span class="w">idle {p.idle_days}d</span>
          </div>
        {/each}
      </section>
    {/if}
  {/if}
</div>

<TaskReview taskId={reviewId} onclose={() => (reviewId = 0)} ondone={load} />

<style>
  .mt { height: 100%; overflow-y: auto; display: flex; flex-direction: column; gap: 10px; padding: 4px 2px 12px; -webkit-overflow-scrolling: touch; }
  .top { display: flex; align-items: center; justify-content: space-between; padding: 2px 6px; }
  .rf { background: none; border: 0; color: var(--fg-mute); min-width: 36px; min-height: 36px; display: flex; align-items: center; justify-content: center; }
  .card { background: var(--panel-bg); border: 1px solid var(--line-2); flex: none; }
  .card.attn { border-color: var(--attn-dim); box-shadow: var(--glow-attn); }
  .card.ok { border-color: var(--line-3); }
  h3 { margin: 0; padding: 9px 12px 7px; font-size: 11px; text-transform: uppercase; letter-spacing: 0.14em; color: var(--fg-dim); border-bottom: 1px solid var(--line); display: flex; gap: 8px; align-items: baseline; }
  .attn h3 { color: var(--attn-hi); }
  h3 b { color: var(--fg-mute); font-weight: 500; }
  .it { display: flex; align-items: flex-start; gap: 10px; padding: 11px 12px; min-height: 48px; border-bottom: 1px solid var(--line); cursor: pointer; }
  .it:last-of-type { border-bottom: 0; }
  .it:active { background: var(--bg-2); }
  .ic { flex: none; color: var(--fg-mute); padding-top: 1px; display: flex; }
  .attn .ic { color: var(--attn); } .ic.ok { color: var(--accent); }
  .tx { flex: 1; min-width: 0; }
  .t { color: var(--fg-hi); line-height: 1.35; overflow-wrap: anywhere; }
  .s { color: var(--fg-dim); font-size: var(--fs-sm); line-height: 1.4; margin-top: 2px; display: -webkit-box; -webkit-line-clamp: 2; line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
  .s.pad { padding: 12px; -webkit-line-clamp: unset; line-clamp: unset; }
  .w { color: var(--fg-mute); font-size: var(--fs-sm); flex: none; white-space: nowrap; }
  .tx .w { display: block; margin-top: 3px; }
  .btns { display: flex; flex-direction: column; gap: 6px; flex: none; }
  .more { width: 100%; min-height: 44px; background: none; border: 0; border-top: 1px solid var(--line); color: var(--accent-hi); font-size: var(--fs-sm); }
  .empty { padding: 40px; text-align: center; color: var(--fg-mute); text-transform: uppercase; letter-spacing: 0.12em; }
</style>
