<script>
  let { tabs = [], active = $bindable(), onchange } = $props();
</script>

<div class="tabs" role="tablist">
  {#each tabs as t}
    <button type="button" role="tab" aria-selected={active === t.id} class:on={active === t.id} onclick={() => { active = t.id; onchange?.(t.id); }}>
      {t.label}{#if t.badge !== undefined && t.badge !== null && t.badge !== ''}<span class="bd">{t.badge}</span>{/if}
    </button>
  {/each}
</div>

<style>
  .tabs { display: flex; gap: 2px; border-bottom: 1px solid var(--line-2); flex: none; overflow-x: auto; }
  button { background: transparent; border: 0; border-bottom: 2px solid transparent; padding: 4px 11px 3px; text-transform: uppercase; letter-spacing: 0.08em; font-size: var(--fs-sm); color: var(--fg-mute); white-space: nowrap; margin-bottom: -1px; }
  button:hover { color: var(--fg); }
  button.on { color: var(--fg-hi); border-bottom-color: var(--fg); text-shadow: var(--glow-sm); }
  .bd { margin-left: 5px; font-size: 10px; color: var(--accent); }
</style>
