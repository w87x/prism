<script>
  let { value = $bindable(), min = -Infinity, max = Infinity, step = 1, disabled = false, onchange, unit = '' } = $props();
  function set(v) {
    v = Math.min(max, Math.max(min, Number.isFinite(v) ? v : 0));
    value = Math.round(v * 1e6) / 1e6;
    onchange?.(value);
  }
</script>

<span class="num" class:disabled>
  <button type="button" tabindex="-1" onclick={() => set((value ?? 0) - step)} {disabled}>−</button>
  <input type="text" inputmode="decimal" {disabled} value={value ?? 0}
    onchange={(e) => set(parseFloat(e.currentTarget.value))}
    onkeydown={(e) => { if (e.key === 'ArrowUp') { e.preventDefault(); set((value ?? 0) + step); } else if (e.key === 'ArrowDown') { e.preventDefault(); set((value ?? 0) - step); } }} />
  {#if unit}<span class="u">{unit}</span>{/if}
  <button type="button" tabindex="-1" onclick={() => set((value ?? 0) + step)} {disabled}>+</button>
</span>

<style>
  .num { display: inline-flex; align-items: stretch; background: var(--bg); border: 1px solid var(--line-2); border-radius: var(--r); }
  .num:focus-within { border-color: var(--fg); box-shadow: var(--glow-sm); }
  .disabled { opacity: 0.5; }
  input { width: 64px; text-align: center; background: transparent; border: 0; outline: 0; color: var(--fg-hi); padding: 3px 2px; }
  button { background: transparent; border: 0; color: var(--fg-dim); width: 20px; }
  button:hover:not(:disabled) { color: var(--fg); background: var(--bg-3); }
  .u { align-self: center; color: var(--fg-mute); font-size: var(--fs-sm); padding-right: 3px; }
</style>
