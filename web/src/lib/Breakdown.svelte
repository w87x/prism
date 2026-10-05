<script>
  // Where an agent's prompt tokens go (system prompt / tool definitions / user / agent / tool results) as a
  // stacked bar with a legend. `b` is the map the runner reports (run.usage → breakdown).
  import { fmtTokens } from './store.svelte.js';
  let { b } = $props();
  const PARTS = [['system', 'system prompt', 'var(--fg-mute)'], ['tools', 'tool definitions', 'var(--ico)'], ['user', 'user / notes', 'var(--accent)'], ['assistant', 'agent', 'var(--fg)'], ['tool', 'tool results', 'var(--attn)']];
  const parts = $derived.by(() => {
    if (!b) return [];
    const total = PARTS.reduce((a, [k]) => a + (b[k] || 0), 0) || 1;
    return PARTS.map(([k, label, color]) => ({ k, label, color, n: b[k] || 0, pct: ((b[k] || 0) / total) * 100 })).filter((p) => p.n > 0);
  });
</script>

{#if parts.length}
  <div class="bk" title="estimated prompt tokens by source (latest turn)">
    <div class="bkbar">{#each parts as p (p.k)}<i style="width:{p.pct}%;background:{p.color}" title="{p.label}: {fmtTokens(p.n)}"></i>{/each}</div>
    <div class="bkleg sm">{#each parts as p (p.k)}<span><b style="background:{p.color}"></b>{p.label} {fmtTokens(p.n)}</span>{/each}</div>
  </div>
{/if}

<style>
  .bk { display: flex; flex-direction: column; gap: 3px; margin: 6px 0; }
  .bkbar { display: flex; height: 6px; border: 1px solid var(--line); background: var(--bg); }
  .bkbar i { display: block; height: 100%; }
  .bkleg { display: flex; flex-wrap: wrap; gap: 4px 12px; color: var(--fg-dim); }
  .bkleg span { display: inline-flex; align-items: center; gap: 4px; }
  .bkleg b { width: 7px; height: 7px; display: inline-block; }
</style>
