<script>
  let { value = $bindable(), options = [], name = 'r' + Math.random().toString(36).slice(2, 7), inline = false, onchange, disabled = false } = $props();
  const items = $derived(options.map((o) => (typeof o === 'object' ? o : { value: o, label: String(o) })));
</script>

<div class="rg" class:inline role="radiogroup">
  {#each items as it}
    <label class="r" class:disabled={disabled || it.disabled}>
      <input type="radio" {name} value={it.value} checked={value === it.value} disabled={disabled || it.disabled}
        onchange={() => { value = it.value; onchange?.(it.value); }} />
      <span class="dot"><span></span></span>
      <span class="lbl">{it.label}{#if it.hint}<span class="hint"> — {it.hint}</span>{/if}</span>
    </label>
  {/each}
</div>

<style>
  .rg { display: flex; flex-direction: column; gap: 5px; }
  .rg.inline { flex-direction: row; flex-wrap: wrap; gap: 14px; }
  .r { display: inline-flex; align-items: center; gap: 7px; cursor: pointer; user-select: none; }
  .disabled { opacity: 0.4; cursor: not-allowed; }
  input { position: absolute; opacity: 0; width: 0; height: 0; }
  .dot { width: 13px; height: 13px; border: 1px solid var(--fg-mute); border-radius: 50%; background: var(--bg); display: inline-flex; align-items: center; justify-content: center; flex: none; }
  .dot span { width: 5px; height: 5px; border-radius: 50%; background: transparent; }
  input:checked + .dot { border-color: var(--fg); box-shadow: var(--glow-sm); }
  input:checked + .dot span { background: var(--fg); box-shadow: 0 0 5px var(--fg); }
  input:focus-visible + .dot { outline: 1px solid var(--fg); outline-offset: 2px; }
  .hint { color: var(--fg-mute); }
</style>
