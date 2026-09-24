<script>
  // Flat status dot: glow only, no border. Standby is dim and steady. `pulse` breathes slowly;
  // `live` swells faster (tokens are arriving): the glow grows and fades and the brightness shifts.
  // `liveMs`, when given, sets the live swell's cycle length (see store.svelte.js's markLive) so it
  // actually tracks how fast tokens are arriving rather than blinking at one fixed rate.
  let { state = 'ok', size = 9, pulse = false, live = false, liveMs = 750, title = '' } = $props();
</script>

<span class="led {state}" class:pulse class:live style="--s:{size}px; --live-ms:{liveMs}ms" {title}></span>

<style>
  .led { display: inline-block; width: var(--s); height: var(--s); border-radius: 50%; flex: none; vertical-align: middle; background: currentColor;
    box-shadow: 0 0 4px currentColor, 0 0 11px color-mix(in srgb, currentColor 55%, transparent); }
  .ok { color: var(--fg); }
  .standby { color: var(--accent); }
  .attention { color: var(--attn); }
  .warn { color: var(--warn); }
  .error { color: var(--err); }
  .off { color: #21352d; box-shadow: none; }
  .pulse { animation: ledswell 1.8s ease-in-out infinite; }
  .error.pulse { animation-duration: 1s; }
  /* dimmer peak than .pulse's ledswell — a fast live blink at full brightness reads as flickery/harsh, so
     the live state trades brightness range for speed rather than stacking both. */
  .live { animation: ledlive var(--live-ms, 750ms) ease-in-out infinite; }
  .off.pulse, .off.live { animation: none; }
  @keyframes ledswell {
    0%, 100% { filter: brightness(0.78); box-shadow: 0 0 3px currentColor, 0 0 6px color-mix(in srgb, currentColor 35%, transparent); }
    50% { filter: brightness(1.3); box-shadow: 0 0 6px currentColor, 0 0 17px color-mix(in srgb, currentColor 75%, transparent); }
  }
  @keyframes ledlive {
    0%, 100% { filter: brightness(0.85); box-shadow: 0 0 3px currentColor, 0 0 6px color-mix(in srgb, currentColor 35%, transparent); }
    50% { filter: brightness(1.12); box-shadow: 0 0 5px currentColor, 0 0 12px color-mix(in srgb, currentColor 55%, transparent); }
  }
</style>
