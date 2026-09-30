<script>
  // `equalize` swaps the normal proportional fill for a scrolling dash-dot "telegraph tape" — same bar,
  // same border/height/colors, just no real value to show yet (a model reasoning with no visible output
  // tokens): dots and dashes tick past like a wire transmitting, instead of sitting static.
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
  /* a telegraph tape: dots and dashes scrolling past, like a wire ticking — one CSS background-position
     animation, no JS, no extra nodes; costs nothing whether one agent is thinking or a dozen are.
     (`.bar i.level` — not just `.level` — to out-specificity `.bar i`'s `background` shorthand above,
     which would otherwise blank out the gradient.) */
  .bar i.level { width: 100%; background-color: transparent; background-image: repeating-linear-gradient(90deg, var(--c) 0 3px, transparent 3px 7px, var(--c) 7px 16px, transparent 16px 22px); background-size: 22px 100%; animation: telegraph 1s linear infinite; animation-delay: var(--delay, 0s); opacity: 0.85; }
  .bar i.level.live { animation-duration: calc(var(--ms) * 0.5); }
  @keyframes telegraph { from { background-position: 0 0; } to { background-position: -22px 0; } }
  @media (prefers-reduced-motion: reduce) { .bar i.level { animation: none; width: 40%; background-image: none; background-color: var(--c); } }
</style>
