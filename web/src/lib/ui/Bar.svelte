<script>
  // `equalize` swaps the normal proportional fill for a sweeping VU-meter-style animation — same bar, same
  // border/height/colors, just no real value to show yet (a model reasoning with no visible output tokens).
  // `live`/`liveMs` are the same real inter-token-arrival signal that drives the agent LED's pulse (see
  // store.svelte.js's markLive): live sweeps at that real rate, and with nothing streaming yet it falls back
  // to a slow ambient sweep instead of sitting frozen.
  let { value = 0, max = 100, tone = '', height = 5, label = '', color = '', equalize = false, live = false, liveMs = 750 } = $props();
  const pct = $derived(Math.max(0, Math.min(100, max ? (value / max) * 100 : 0)));
  const t = $derived(tone || (pct > 90 ? 'err' : pct > 75 ? 'attn' : 'ok'));
</script>

<div class="bar {t}" class:equalize style="height:{height}px;{color ? `--c:${color}` : ''}{equalize ? `--ms:${liveMs}ms` : ''}" title={label}>
  {#if equalize}
    <i class="sweep" class:live></i>
  {:else}
    <i style="width:{pct}%"></i>
  {/if}
</div>

<style>
  .bar { --c: var(--fg); background: var(--bg); border: 1px solid var(--line); position: relative; width: 100%; overflow: hidden; }
  .accent { --c: var(--accent); } .attn { --c: var(--attn); } .warn { --c: var(--warn); } .err { --c: var(--err); }
  .bar i { display: block; height: 100%; background: var(--c); box-shadow: 0 0 6px var(--c); }
  .bar > i:not(.sweep) { transition: width 0.25s; }
  .sweep { position: absolute; top: 0; bottom: 0; left: 0; width: 30%; opacity: 0.85;
    animation: sweep 1.6s linear infinite; }
  .sweep.live { animation-duration: var(--ms); }
  @keyframes sweep {
    from { transform: translateX(-100%); }
    to   { transform: translateX(430%); }
  }
  @media (prefers-reduced-motion: reduce) { .sweep { animation: none; transform: translateX(0); } }
</style>
