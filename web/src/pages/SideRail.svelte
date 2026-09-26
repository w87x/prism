<script>
  // Right rail for every page that has no dedicated widget: what the agents are doing right now, what needs
  // you, and what is coming up — so the column earns its space instead of only repeating system counters.
  import { S, call, listen, go, activeRuns, fmtTokens, lastLine, ago, until, openProposal, openRef } from '../lib/store.svelte.js';
  import Panel from '../lib/ui/Panel.svelte';
  import Glyph from '../lib/ui/Glyph.svelte';
  import Led from '../lib/ui/Led.svelte';
  import Icon from '../lib/ui/Icon.svelte';
  import Empty from '../lib/ui/Empty.svelte';
  import Button from '../lib/ui/Button.svelte';
  import TaskReview from '../lib/TaskReview.svelte';
  import PageInfo from './PageInfo.svelte';

  const runs = $derived(activeRuns().filter((r) => !r.done));
  const onToday = $derived(S.page === 'today'); // Today already shows all of this in its own body
  let d = $state(null);
  let reviewId = $state(0);
  async function load() { const r = await call('today.get', {}, { quiet: true }); if (r) d = r; }
  $effect(() => {
    if (onToday) return;
    load();
    const off = [listen('task.update', load), listen('briefing.new', load)];
    const i = setInterval(load, 30000);
    return () => { off.forEach((f) => f()); clearInterval(i); };
  });

  const kindIcon = { partial: 'warn', failed: 'warn', waiting_input: 'warn', briefing: 'bell', hire: 'agents', proposal: 'edit', plugin: 'tools' };
  function open(it) { openRef(it.ref || 'today'); }
  const attn = $derived((d?.needs_attention || []).slice(0, 6));
  const soon = $derived((d?.commitments || []).slice(0, 4));
</script>

<Panel title="Agents now">
  {#snippet right()}<button type="button" class="lnk" title="full-screen thinking wall" onclick={() => (S.wallOpen = true)}>wall ›</button>{/snippet}
  {#if runs.length === 0}
    <Empty>no agent running</Empty>
  {/if}
  {#each runs as r (r.id)}
    <div class="ag" style="margin-left:{r.depth * 8}px" role="button" tabindex="0" title="open the live view of this agent"
      onclick={() => (S.peekRun = r)} onkeydown={(e) => (e.key === 'Enter' || e.key === ' ') && (S.peekRun = r)}>
      <div class="rw">
        <Led state={r.phase === 'thinking' ? 'standby' : 'ok'} live={r.live} liveMs={r.liveMs} size={7} />
        <span class="nm grow ellipsis"><Glyph name={r.agent} /> {r.agent}</span>
        <span class="sm dim">{fmtTokens(r.tokens_in)}↑ {fmtTokens(r.tokens_out)}↓</span>
      </div>
      {#if lastLine(r.buf)}<div class="now" title={lastLine(r.buf)}>{lastLine(r.buf).slice(0, 90)}</div>{/if}
    </div>
  {/each}
</Panel>

{#if !onToday}
  <Panel title="Needs you{attn.length ? ` (${d.needs_attention.length})` : ''}">
    {#if !attn.length}
      <Empty>all clear</Empty>
    {/if}
    {#each attn as it}
      <div class="it" role="button" tabindex="0" onclick={() => open(it)} onkeydown={(e) => e.key === 'Enter' && open(it)}>
        <span class="ic"><Icon name={kindIcon[it.kind] || 'warn'} size={12} /></span>
        <div class="tx"><span class="hi ellipsis">{it.title}</span>{#if it.sub}<span class="sub ellipsis">{it.sub}</span>{/if}</div>
        {#if it.kind === 'partial' || it.kind === 'failed'}<Button size="sm" variant="accent" onclick={(e) => { e.stopPropagation(); reviewId = Number(it.ref.slice(5)); }}>Review</Button>{:else}<span class="sm mute nowrap">{ago(it.at)}</span>{/if}
      </div>
    {/each}
  </Panel>
  {#if soon.length}
    <Panel title="Coming up">
      {#each soon as c}
        <div class="it plain"><span class="ic"><Icon name={c.kind === 'cron' ? 'compact' : 'auto'} size={12} /></span><div class="tx"><span class="hi ellipsis">{c.title}</span></div><span class="sm mute nowrap">{until(c.due)}</span></div>
      {/each}
    </Panel>
  {/if}
{/if}

<PageInfo widget />

<TaskReview taskId={reviewId} onclose={() => (reviewId = 0)} ondone={load} />

<style>
  .lnk { background: none; border: 0; color: var(--accent); padding: 0; font-size: var(--fs-sm); } .lnk:hover { color: var(--accent-hi); }
  .ag { display: flex; flex-direction: column; gap: 2px; padding: 3px 4px; border-bottom: 1px dotted var(--line); cursor: pointer; } .ag:hover { background: var(--bg-2); }
  .rw { display: flex; align-items: center; gap: 6px; }
  .nm { color: var(--fg-hi); font-weight: 700; }
  .now { font-size: 10.5px; color: var(--fg-faint); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .it { display: flex; align-items: center; gap: 6px; padding: 4px 2px; border-bottom: 1px dotted var(--line); cursor: pointer; } .it.plain { cursor: default; } .it:not(.plain):hover { background: var(--bg-2); }
  .ic { flex: none; color: var(--attn-hi); display: flex; }
  .tx { flex: 1; min-width: 0; display: flex; flex-direction: column; }
  .sub { font-size: 10.5px; color: var(--fg-mute); }
</style>
