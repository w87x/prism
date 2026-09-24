<script>
  let { label = '', hint = '', error = '', inline = false, children, right } = $props();
</script>

<div class="f" class:inline>
  {#if label}<div class="l"><span>{label}</span>{@render right?.()}</div>{/if}
  <div class="c">{@render children?.()}</div>
  {#if error}<div class="e">{error}</div>{:else if hint}<div class="h">{hint}</div>{/if}
</div>

<style>
  .f { display: flex; flex-direction: column; gap: 3px; min-width: 0; }
  .f.inline { flex-direction: row; align-items: center; gap: 10px; }
  .f.inline .l { min-width: 120px; }
  .l { display: flex; justify-content: space-between; align-items: center; font-size: var(--fs-sm); color: var(--fg-dim); text-transform: uppercase; letter-spacing: 0.07em; }
  .c { min-width: 0; }
  .f.inline .c { flex: 1; }
  .h { font-size: var(--fs-sm); color: var(--fg-mute); }
  .e { font-size: var(--fs-sm); color: var(--err); }
</style>
