<script>
  // `equalize` swaps the normal proportional fill for a VU-meter level animation — same bar, same
  // border/height/colors, just no real value to show yet (a model reasoning with no visible output tokens):
  // the fill jumps between levels (100%, 20%, 40%, …) like an audio meter instead of sitting static.
  // `live`/`liveMs` are the same real inter-token-arrival signal that drives the agent LED's pulse (see
  // store.svelte.js's markLive): live jumps at that real rate, and with nothing streaming yet it falls back
  // to a slower ambient cycle instead of sitting frozen.
  let { value = 0, max = 100, tone = '', height = 5, label = '', color = '', equalize = false, live = false, liveMs = 750 } = $props();
  const pct = $derived(Math.max(0, Math.min(100, max ? (value / max) * 100 : 0)));
  const t = $derived(tone || (pct > 90 ? 'err' : pct > 75 ? 'attn' : 'ok'));
  // a random negative delay phase-shifts this instance into the middle of the cycle rather than starting
  // at 0 — several agents thinking at once (often at the same liveMs, e.g. all idle-fallback) would
  // otherwise all jump levels in perfect lockstep, which reads as one fake bar copy-pasted N times.
  const jitter = (-Math.random() * 3).toFixed(2);
</script>

<div class="bar {t}" class:equalize style="height:{height}px;{color ? `--c:${color}` : ''}{equalize ? `--ms:${liveMs}ms;--delay:${jitter}s` : ''}" title={label}>
  {#if equalize}
    <i class="level" class:live></i>
  {:else}
    <i style="width:{pct}%"></i>
  {/if}
</div>

<style>
  .bar { --c: var(--fg); background: var(--bg); border: 1px solid var(--line); position: relative; width: 100%; overflow: hidden; }
  .accent { --c: var(--accent); } .attn { --c: var(--attn); } .warn { --c: var(--warn); } .err { --c: var(--err); }
  .bar i { display: block; height: 100%; background: var(--c); box-shadow: 0 0 6px var(--c); }
  .bar > i:not(.level) { transition: width 0.25s; }
  .level { animation: level 2.4s ease-in-out infinite; animation-delay: var(--delay, 0s); }
  .level.live { animation-duration: var(--ms); }
  @keyframes level {
    0%   { width: 15%; }
    12%  { width: 70%; }
    24%  { width: 30%; }
    36%  { width: 100%; }
    50%  { width: 20%; }
    64%  { width: 55%; }
    78%  { width: 40%; }
    90%  { width: 80%; }
    100% { width: 15%; }
  }
  @media (prefers-reduced-motion: reduce) { .level { animation: none; width: 40%; } }
</style>
