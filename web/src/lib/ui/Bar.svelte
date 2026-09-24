<script>
  let { value = 0, max = 100, tone = '', height = 5, label = '', color = '' } = $props();
  const pct = $derived(Math.max(0, Math.min(100, max ? (value / max) * 100 : 0)));
  const t = $derived(tone || (pct > 90 ? 'err' : pct > 75 ? 'attn' : 'ok'));
</script>

<div class="bar {t}" style="height:{height}px;{color ? `--c:${color}` : ''}" title={label}><i style="width:{pct}%"></i></div>

<style>
  .bar { --c: var(--fg); background: var(--bg); border: 1px solid var(--line); position: relative; width: 100%; }
  .accent { --c: var(--accent); } .attn { --c: var(--attn); } .warn { --c: var(--warn); } .err { --c: var(--err); }
  i { display: block; height: 100%; background: var(--c); box-shadow: 0 0 6px var(--c); transition: width 0.25s; }
</style>
