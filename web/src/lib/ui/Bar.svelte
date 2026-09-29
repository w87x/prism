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
  .sweep { position: absolute; inset: 0; width: 40%; transform-origin: left center; opacity: 0.85;
    animation: idle 1.8s ease-in-out infinite; }
  .sweep.live { animation-name: sweep; animation-duration: var(--ms); animation-timing-function: ease-in-out; }
  @keyframes sweep {
    0%   { transform: translateX(-40%) scaleX(0.5); }
    50%  { transform: translateX(80%) scaleX(1.4); }
    100% { transform: translateX(220%) scaleX(0.5); }
  }
  @keyframes idle {
    0%, 100% { transform: translateX(-10%) scaleX(0.6); }
    50%      { transform: translateX(60%) scaleX(0.9); }
  }
  @media (prefers-reduced-motion: reduce) { .sweep { animation: none; transform: translateX(0) scaleX(1); } }
</style>
