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
  // real morse is irregular — short dots, longer dashes, uneven gaps — not one shape repeating on a fixed
  // beat. Built once per instance (this component stays mounted for an agent's lifetime, keyed by run id),
  // so it doesn't reshuffle mid-animation; also gives each agent's tape its own texture instead of all of
  // them being the same strip copy-pasted.
  function morse() {
    const stops = [];
    let x = 0;
    while (x < 64) {
      const dash = Math.random() < 0.35;
      const w = dash ? 7 + Math.random() * 7 : 2 + Math.random() * 2.5;
      const gap = 3 + Math.random() * 5;
      stops.push(`var(--c) ${x.toFixed(1)}px ${(x + w).toFixed(1)}px`);
      x += w;
      stops.push(`transparent ${x.toFixed(1)}px ${(x + gap).toFixed(1)}px`);
      x += gap;
    }
    // scale the base (non-live) duration to this instance's length so every tape scrolls at the same
    // px/s regardless of how long its random pattern turned out — otherwise a longer tape would look
    // like it's crawling and a short one like it's sprinting, purely by luck of the draw.
    return { image: `repeating-linear-gradient(90deg, ${stops.join(', ')})`, len: x.toFixed(1), dur: (x / 46).toFixed(2) };
  }
  const tape = equalize ? morse() : null;
</script>

<div class="bar {t}" class:equalize style="height:{height}px;{color ? `--c:${color}` : ''}{equalize ? `--ms:${liveMs}ms;--delay:${jitter}s;--tape:${tape.image};--tlen:${tape.len}px;--dur:${tape.dur}s` : ''}" title={label}>
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
  /* a telegraph tape: dots and dashes scrolling left-to-right like a wire ticking — one CSS
     background-position animation, no JS, no extra nodes; costs nothing whether one agent is thinking
     or a dozen are. The pattern itself (--tape/--tlen) is generated once per instance in the script, so
     it reads as irregular morse rather than one shape on a metronome.
     (`.bar i.level` — not just `.level` — to out-specificity `.bar i`'s `background` shorthand above,
     which would otherwise blank out the gradient.) */
  .bar i.level { width: 100%; background-color: transparent; background-image: var(--tape); background-size: var(--tlen) 100%; background-position: 0 0; animation: telegraph var(--dur, 1.4s) linear infinite; animation-delay: var(--delay, 0s); opacity: 0.85; }
  .bar i.level.live { animation-duration: calc(var(--ms) * 0.85); }
  @keyframes telegraph { from { background-position: 0 0; } to { background-position: var(--tlen) 0; } }
  @media (prefers-reduced-motion: reduce) { .bar i.level { animation: none; width: 40%; background-image: none; background-color: var(--c); } }
</style>
