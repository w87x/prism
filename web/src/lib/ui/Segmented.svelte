<script>
  // Compact exclusive choice (e.g. SAFE | ARMED). Options: [{value,label,tone}]
  let { value = $bindable(), options = [], onchange, disabled = false, size = 'md' } = $props();
</script>

<div class="seg {size}" class:disabled role="group">
  {#each options as o}
    <button type="button" class="{o.tone || 'ok'}" class:on={value === o.value} {disabled} onclick={() => { value = o.value; onchange?.(o.value); }}>{o.label}</button>
  {/each}
</div>

<style>
  .seg { display: inline-flex; border: 1px solid var(--line-2); border-radius: var(--r); overflow: hidden; }
  .disabled { opacity: 0.4; pointer-events: none; }
  button { background: var(--bg); border: 0; border-right: 1px solid var(--line-2); padding: 1px 8px; font-size: 10.5px; letter-spacing: 0.07em; text-transform: uppercase; color: var(--fg-mute); }
  button:last-child { border-right: 0; }
  button:hover { color: var(--fg); background: var(--bg-3); }
  button.on { --c: var(--fg); color: var(--c); background: color-mix(in srgb, var(--c) 16%, var(--bg)); text-shadow: 0 0 6px color-mix(in srgb, var(--c) 60%, transparent); }
  button.on.accent { --c: var(--accent); } button.on.attn { --c: var(--attn); } button.on.warn { --c: var(--warn); } button.on.err { --c: var(--err); } button.on.mute { --c: var(--fg-dim); }
  .sm button { padding: 0 6px; font-size: 10px; }
</style>
