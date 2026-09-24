<script>
  let { open = $bindable(false), title = '', width = 560, onclose, children, footer, tone = '' } = $props();
  function close() { open = false; onclose?.(); }
</script>

<svelte:window onkeydown={(e) => open && e.key === 'Escape' && close()} />

{#if open}
  <div class="ov" role="presentation" onpointerdown={(e) => e.target === e.currentTarget && close()}>
    <div class="dlg {tone}" style="width:min({width}px,96vw)" role="dialog" aria-modal="true" aria-label={title}>
      <header>
        <span class="t">{title}</span>
        <button type="button" class="x" onclick={close} aria-label="close">×</button>
      </header>
      <div class="bd">{@render children?.()}</div>
      {#if footer}<footer>{@render footer()}</footer>{/if}
    </div>
  </div>
{/if}

<style>
  .ov { position: fixed; inset: 0; z-index: 2100; background: rgba(0, 4, 0, 0.72); display: flex; align-items: flex-start; justify-content: center; padding: 6vh 12px 12px; overflow: auto; }
  .dlg { --c: var(--line-3); background: var(--bg-1); border: 1px solid var(--c); box-shadow: 0 12px 48px rgba(0, 0, 0, 0.8), var(--glow); display: flex; flex-direction: column; max-height: 88vh; }
  .dlg.attn { --c: var(--attn); box-shadow: 0 12px 48px rgba(0, 0, 0, 0.8), var(--glow-attn); }
  .dlg.err { --c: var(--err); background: color-mix(in srgb, var(--err-bg) 92%, var(--bg-1)); box-shadow: 0 12px 48px rgba(0, 0, 0, 0.8), var(--glow-err); }
  .dlg.err header, .dlg.err footer { background: color-mix(in srgb, var(--err) 12%, var(--bg-1)); }
  .dlg.err .t { color: var(--err-hi); }
  /* neutral chrome on the red dialog: the green line, close and cancel controls would clash with it */
  .dlg.err header { border-bottom-color: color-mix(in srgb, var(--err) 45%, transparent); }
  .dlg.err footer { border-top-color: #4a4a4a; }
  .dlg.err .x { color: #8c8c8c; } .dlg.err .x:hover { color: #d0d0d0; }
  .dlg.err footer :global(.btn.ghost) { color: #9a9a9a; }
  .dlg.err footer :global(.btn.ghost:hover:not(:disabled)) { color: #d4d4d4; background: rgba(255, 255, 255, 0.07); border-color: #6a6a6a; box-shadow: none; }
  header { display: flex; align-items: center; justify-content: space-between; padding: 4px 10px; border-bottom: 1px solid var(--c); background: var(--bg-2); flex: none; }
  .t { text-transform: uppercase; letter-spacing: 0.1em; font-size: var(--fs-sm); font-weight: 700; color: var(--fg-hi); }
  .x { background: none; border: 0; font-size: 18px; line-height: 1; color: var(--fg-mute); padding: 0 2px; } .x:hover { color: var(--err); }
  .bd { padding: 10px; overflow: auto; display: flex; flex-direction: column; gap: 10px; min-height: 0; }
  footer { padding: 7px 10px; border-top: 1px solid var(--line-2); display: flex; justify-content: flex-end; gap: 8px; background: var(--bg-2); flex: none; }
</style>
