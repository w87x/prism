<script>
  let { children } = $props();
</script>

<div class="e">{@render children?.()}</div>

<style>
  .e { color: var(--fg-mute); padding: 14px 8px; text-align: center; font-size: var(--fs-sm); text-transform: uppercase; letter-spacing: 0.08em; }
</style>
