<script>
  // A tiny bouncing-bars indicator for an active run when there's nothing real to show a progress bar
  // against yet (a reasoning phase with no visible output tokens, or the gap before the first one arrives).
  // `live`/`liveMs` are the same real inter-token-arrival signal that already drives the agent LED's pulse
  // (see store.svelte.js markLive) — when tokens are actually streaming, the bars bounce at that real rate;
  // with nothing streaming yet it falls back to a slow ambient pulse, so it never reads as frozen/dead but
  // never fakes a rate it doesn't have either.
  let { live = false, liveMs = 750, tone = '' } = $props();
</script>

<div class="eq {tone}" class:live style="--ms:{liveMs}ms" aria-hidden="true">
  <i></i><i></i><i></i><i></i>
</div>

<style>
  .eq { display: inline-flex; align-items: flex-end; gap: 1.5px; height: 9px; width: 13px; flex: none; }
  .eq i { flex: 1; background: var(--c, var(--fg-mute)); border-radius: 1px; height: 25%; opacity: 0.5; animation: idle 1.8s ease-in-out infinite; }
  .eq.live i { opacity: 0.95; animation-name: bounce; animation-duration: var(--ms); }
  .eq i:nth-child(1) { animation-delay: 0ms; }
  .eq i:nth-child(2) { animation-delay: 180ms; }
  .eq i:nth-child(3) { animation-delay: 90ms; }
  .eq i:nth-child(4) { animation-delay: 260ms; }
  .eq.live i:nth-child(1) { animation-delay: 0ms; }
  .eq.live i:nth-child(2) { animation-delay: calc(var(--ms) * 0.15); }
  .eq.live i:nth-child(3) { animation-delay: calc(var(--ms) * 0.3); }
  .eq.live i:nth-child(4) { animation-delay: calc(var(--ms) * 0.45); }
  .accent i { --c: var(--accent); }
  @keyframes bounce { 0%, 100% { height: 25%; } 50% { height: 100%; } }
  @keyframes idle { 0%, 100% { height: 20%; } 50% { height: 45%; } }
  @media (prefers-reduced-motion: reduce) { .eq i { animation: none; height: 40%; } }
</style>
