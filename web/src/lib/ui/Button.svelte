<script>
  // `class` is destructured out (not left in ...rest) so a caller's extra class ADDS to "btn ghost md" instead
  // of silently replacing it — a spread's class does not merge with an earlier static class attribute.
  let { variant = 'default', size = 'md', disabled = false, loading = false, type = 'button', title = '', block = false, onclick, children, class: cls = '', ...rest } = $props();
</script>

<button class="btn {variant} {size} {cls}" class:block {type} {title} disabled={disabled || loading} {onclick} {...rest}>
  {#if loading}<span class="spin">◐</span>{/if}{@render children?.()}
</button>

<style>
  .btn {
    --c: var(--fg); --cbg: var(--bg-3); --cg: var(--glow-sm);
    display: inline-flex; align-items: center; justify-content: center; gap: 6px; white-space: nowrap;
    background: transparent; color: var(--c); border: 1px solid color-mix(in srgb, var(--c) 55%, transparent);
    border-radius: var(--r); padding: 3px 11px; line-height: 1.3;
    text-transform: uppercase; letter-spacing: 0.07em; font-size: var(--fs-sm); font-weight: 500;
    transition: background 0.1s, box-shadow 0.1s, border-color 0.1s;
  }
  .btn.sm { padding: 1px 7px; font-size: 10.5px; }
  .btn.block { display: flex; width: 100%; }
  .btn:hover:not(:disabled) { background: color-mix(in srgb, var(--c) 14%, transparent); border-color: var(--c); box-shadow: var(--cg); }
  .btn:active:not(:disabled) { background: color-mix(in srgb, var(--c) 26%, transparent); }
  .btn:focus-visible { outline: 1px solid var(--c); outline-offset: 2px; }
  .btn:disabled { opacity: 0.38; cursor: not-allowed; }
  .primary { background: color-mix(in srgb, var(--c) 18%, transparent); border-color: var(--c); }
  .accent { --c: var(--accent); --cg: var(--glow-accent); }
  .attn { --c: var(--attn); --cg: var(--glow-attn); }
  .warn { --c: var(--warn); --cg: var(--glow-warn); }
  .danger { --c: var(--err); --cg: var(--glow-err); }
  .ghost { border-color: transparent; color: var(--fg-dim); }
  .ghost:hover:not(:disabled) { color: var(--fg); }
  .spin { display: inline-block; animation: spin 0.8s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
</style>
