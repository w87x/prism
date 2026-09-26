<script>
  // A small "?" that opens a popover explaining what something is for. Click again or press Esc to close.
  let { text = '', title = '', children } = $props();
  let open = $state(false);
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && (open = false)} />
<span class="hint">
  <button type="button" class="q" class:on={open} aria-label="What is this?" title="What is this?" onclick={() => (open = !open)}>?</button>
  {#if open}
    <button type="button" class="veil" aria-label="close" onclick={() => (open = false)}></button>
    <div class="pop" role="note">
      {#if title}<b>{title}</b>{/if}
      {#if text}<p>{text}</p>{/if}
      {@render children?.()}
    </div>
  {/if}
</span>

<style>
  .hint { position: relative; display: inline-flex; align-items: center; flex: none; }
  .q { flex: none; box-sizing: border-box; width: 18px; min-width: 18px; height: 18px; min-height: 18px; aspect-ratio: 1; display: inline-flex; align-items: center; justify-content: center; padding: 0; border-radius: 50%; border: 1px solid var(--line-3); background: none; color: var(--fg-mute); font-size: 10px; line-height: 1; cursor: pointer; }
  /* the app gives every button a 30px minimum height on phones, which turned the circle into an ellipse: size both sides alike */
  @media (max-width: 820px), (pointer: coarse) { .q { width: 26px; min-width: 26px; height: 26px; min-height: 26px; font-size: 13px; } }
  .q:hover, .q.on { color: var(--accent-hi); border-color: var(--accent); }
  .veil { position: fixed; inset: 0; background: none; border: 0; padding: 0; cursor: default; z-index: 40; }
  .pop { position: absolute; top: 22px; left: 0; z-index: 41; width: min(420px, 86vw); background: var(--bg-2); border: 1px solid var(--line-3); padding: 8px 10px; color: var(--fg-dim); font-size: var(--fs-sm); line-height: 1.45; box-shadow: 0 6px 24px rgba(0, 0, 0, 0.4); text-transform: none; letter-spacing: 0; font-weight: 400; }
  .pop b { color: var(--fg-hi); display: block; margin-bottom: 3px; }
  .pop p { margin: 0; }
</style>
