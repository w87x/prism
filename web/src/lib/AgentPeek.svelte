<script>
  // Live view of one running agent: its task, the stream it is producing right now, and the
  // conversation it has had so far (the sub-chat between it and whoever delegated to it).
  import { untrack } from 'svelte';
  import RunText from './RunText.svelte';
  import { S, call, fmtTokens } from './store.svelte.js';
  import Modal from './ui/Modal.svelte';
  import Led from './ui/Led.svelte';
  import Glyph from './ui/Glyph.svelte';
  import Bar from './ui/Bar.svelte';
  import Field from './ui/Field.svelte';
  import Transcript from './Transcript.svelte';

  let open = $state(false);
  let detail = $state(null);
  let live = $state();
  const r = $derived(S.peekRun);
  $effect(() => { open = !!r; });

  async function load(id) {
    const d = await call('tasks.get', { id }, { quiet: true });
    if (d && untrack(() => S.peekRun?.task) === id) detail = d;
  }
  $effect(() => {
    detail = null;
    const id = r?.task;
    if (!id) return;
    load(id);
    const i = setInterval(() => untrack(() => S.peekRun && !S.peekRun.done) && load(id), 2500);
    return () => clearInterval(i);
  });
  $effect(() => { r?.buf; queueMicrotask(() => live && (live.scrollTop = live.scrollHeight)); });
  // where this run's prompt tokens go — the colours match the legend below the bar
  const PARTS = [['system', 'system prompt', 'var(--fg-mute)'], ['tools', 'tool definitions', 'var(--ico)'], ['user', 'user / notes', 'var(--accent)'], ['assistant', 'agent', 'var(--fg)'], ['tool', 'tool results', 'var(--attn)']];
  const parts = $derived.by(() => {
    const b = r?.breakdown;
    if (!b) return [];
    const total = PARTS.reduce((a, [k]) => a + (b[k] || 0), 0) || 1;
    return PARTS.map(([k, label, color]) => ({ k, label, color, n: b[k] || 0, pct: ((b[k] || 0) / total) * 100 })).filter((p) => p.n > 0);
  });
  const led = $derived(r ? (r.done ? 'off' : r.phase === 'thinking' ? 'standby' : 'ok') : 'off');
</script>

<Modal bind:open title={r ? `${r.agent} · live` : ''} width={860} onclose={() => (S.peekRun = null)}>
  {#if r}
    <div class="row wrap gap-12 sm">
      <span><Led state={led} live={r.live} liveMs={r.liveMs} size={8} /> {r.done ? r.phase : r.phase === 'thinking' ? 'thinking' : 'acting'}</span>
      <span class="hi"><Glyph name={r.agent} /> {r.agent}</span>
      <span class="dim">{fmtTokens(r.tokens_in)}↑ {fmtTokens(r.tokens_out)}↓ · {r.calls}⚙</span>
      {#if r.task}<span class="dim">task #{r.task}</span>{/if}
      {#if r.window}<span class="grow ctx" title="context {r.context}/{r.window} tokens"><Bar value={r.context} max={r.window} height={3} equalize={r.phase === 'thinking'} tone={r.phase === 'thinking' ? 'accent' : ''} live={r.live} liveMs={r.liveMs} /></span>{/if}
    </div>
    {#if parts.length}
      <div class="bk" title="estimated prompt tokens by source">
        <div class="bkbar">{#each parts as p (p.k)}<i style="width:{p.pct}%;background:{p.color}" title="{p.label}: {fmtTokens(p.n)}"></i>{/each}</div>
        <div class="bkleg sm">{#each parts as p (p.k)}<span><b style="background:{p.color}"></b>{p.label} {fmtTokens(p.n)}</span>{/each}</div>
      </div>
    {/if}
    {#if detail?.task?.input}<Field label="Task"><div class="pre task">{detail.task.input}</div></Field>{:else if r.title}<Field label="Task"><div class="pre task">{r.title}</div></Field>{/if}
    <Field label="Right now">
      <div class="live" bind:this={live}>{#if r.buf.trim()}<RunText text={r.buf} />{:else}…{/if}</div>
    </Field>
    {#if detail?.transcript?.length}
      <Field label="Conversation ({detail.transcript.length} messages)"><Transcript messages={detail.transcript} max={300} /></Field>
    {:else if !r.task}
      <div class="sm mute">This is the conversation with you; it is in the chat.</div>
    {/if}
  {/if}
</Modal>

<style>
  .live { background: radial-gradient(ellipse at center, #04120c 0%, #010503 100%); border: 1px solid var(--line-2); padding: 6px 10px; font-size: var(--fs-sm); color: var(--fg-dim); white-space: pre-wrap; word-break: break-word; max-height: 220px; min-height: 60px; overflow: auto; line-height: 1.45; }
  .task { color: var(--fg-dim); max-height: 110px; overflow: auto; font-size: var(--fs-sm); }
  .bk { display: flex; flex-direction: column; gap: 3px; }
  .bkbar { display: flex; height: 6px; border: 1px solid var(--line); background: var(--bg); }
  .bkbar i { display: block; height: 100%; }
  .bkleg { display: flex; flex-wrap: wrap; gap: 4px 12px; color: var(--fg-dim); }
  .bkleg span { display: inline-flex; align-items: center; gap: 4px; }
  .bkleg b { width: 7px; height: 7px; display: inline-block; }
  .ctx { min-width: 80px; max-width: 220px; margin-left: auto; }
</style>
