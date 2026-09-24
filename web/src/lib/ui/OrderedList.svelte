<script>
  // Ordered list editor for fallback chains: reorder with ↑ ↓, remove with ×, add from a Select.
  import Select from './Select.svelte';
  let { value = $bindable(), options = [], labels = {}, addLabel = 'add…', onchange } = $props();
  let pick = $state();
  const avail = $derived(options.filter((o) => !(value || []).includes(typeof o === 'object' ? o.value : o)));
  function move(i, d) { const a = [...value]; const j = i + d; if (j < 0 || j >= a.length) return; [a[i], a[j]] = [a[j], a[i]]; value = a; onchange?.(value); }
  function remove(i) { value = value.filter((_, k) => k !== i); onchange?.(value); }
  function add(v) { if (v === undefined || v === null || v === '') return; value = [...(value || []), v]; pick = undefined; onchange?.(value); }
</script>

<div class="ol">
  {#each value || [] as v, i (v)}
    <div class="it">
      <span class="n">{i + 1}</span>
      <span class="grow ellipsis">{labels[v] || v}</span>
      <button type="button" onclick={() => move(i, -1)} disabled={i === 0} aria-label="up">↑</button>
      <button type="button" onclick={() => move(i, 1)} disabled={i === value.length - 1} aria-label="down">↓</button>
      <button type="button" class="x" onclick={() => remove(i)} aria-label="remove">×</button>
    </div>
  {/each}
  {#if avail.length}<Select bind:value={pick} options={avail} placeholder={addLabel} size="sm" onchange={add} />{/if}
</div>

<style>
  .ol { display: flex; flex-direction: column; gap: 3px; }
  .it { display: flex; align-items: center; gap: 6px; padding: 2px 6px; background: var(--bg); border: 1px solid var(--line-2); }
  .n { color: var(--fg-mute); width: 14px; font-size: var(--fs-sm); }
  button { background: none; border: 0; color: var(--fg-dim); padding: 0 4px; } button:hover:not(:disabled) { color: var(--fg); } button:disabled { opacity: 0.25; cursor: default; }
  .x:hover { color: var(--err) !important; }
</style>
