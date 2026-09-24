<script>
  // Right panel for the chat page: active agents with token counts and a brief current task.
  import { S, activeRuns, fmtTokens, go, lastLine } from '../lib/store.svelte.js';
  import Panel from '../lib/ui/Panel.svelte';
  import Glyph from '../lib/ui/Glyph.svelte';
  import Led from '../lib/ui/Led.svelte';
  import Bar from '../lib/ui/Bar.svelte';
  import Empty from '../lib/ui/Empty.svelte';

  const runs = $derived(activeRuns());
  const st = $derived(S.status);
  const total = $derived(runs.reduce((a, r) => ({ i: a.i + r.tokens_in, o: a.o + r.tokens_out, c: a.c + r.calls }), { i: 0, o: 0, c: 0 }));
</script>

<Panel title="Active agents" grow>
  {#snippet right()}<span class="sm dim" title="tokens in / out · tool calls">{fmtTokens(total.i)}↑ {fmtTokens(total.o)}↓ · {total.c}⚙</span>{/snippet}
  {#if runs.length === 0}
    <Empty>no agent running</Empty>
  {/if}
  {#each runs as r (r.id)}
    <div class="ag" style="margin-left:{r.depth * 10}px" role="button" tabindex="0" title="open the live view of this agent"
      onclick={() => (S.peekRun = r)} onkeydown={(e) => (e.key === 'Enter' || e.key === ' ') && (S.peekRun = r)}>
      <div class="row">
        <Led state={r.done ? 'off' : r.phase === 'thinking' ? 'standby' : 'ok'} live={r.live} liveMs={r.liveMs} size={8} />
        <span class="nm grow ellipsis"><Glyph name={r.agent} /> {r.agent}</span>
        <span class="tk sm dim" title="tokens in / out · tool calls">{fmtTokens(r.tokens_in)}↑ {fmtTokens(r.tokens_out)}↓ · {r.calls}⚙</span>
      </div>
      {#if !r.done && lastLine(r.buf)}<div class="now" title={lastLine(r.buf)}>{lastLine(r.buf).slice(0, 90)}</div>{:else if r.task_text}<div class="now">{r.task_text}</div>{/if}
      {#if r.window}<div class="ctx" title="context {r.context}/{r.window} tokens"><Bar value={r.context} max={r.window} height={3} /></div>{/if}
    </div>
  {/each}
</Panel>

<Panel title="Queue">
  <div class="kv" title="tasks waiting for a free worker"><span>queued</span><b class="hi">{st?.queue ?? 0}</b></div>
  <div class="kv" title="tasks an agent is working on right now"><span>running</span><b class="hi">{st?.running ?? 0}</b></div>
  <div class="kv" title="tasks paused until you answer a question"><span>waiting for input</span><b class={st?.waiting ? 'attn' : 'hi'}>{st?.waiting ?? 0}</b></div>
  <div class="kv"><span>raw messages to digest</span><b class="hi">{st?.raw_pending ?? 0}</b></div>
  <button class="lnk" onclick={() => go('tasks')}>open task queue ›</button>
</Panel>

<style>
  .ag { display: flex; flex-direction: column; gap: 2px; padding: 3px 4px; border-bottom: 1px dotted var(--line); cursor: pointer; } .ag:hover { background: var(--bg-2); }
  .nm { color: var(--fg-hi); font-weight: 700; }
  .now { font-size: 10.5px; color: var(--fg-faint); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .kv { display: flex; justify-content: space-between; font-size: var(--fs-sm); color: var(--fg-dim); }
  .lnk { background: none; border: 0; color: var(--accent); text-align: left; padding: 0; font-size: var(--fs-sm); } .lnk:hover { color: var(--accent-hi); }
</style>
