<script>
  let { checked = $bindable(), label = '', disabled = false, onchange, children } = $props();
  async function change(e) {
    const el = e.currentTarget, v = el.checked;
    checked = v;
    if ((await onchange?.(v)) === false) { checked = !v; el.checked = !v; }
  }
</script>

<label class="cb" class:disabled>
  <input type="checkbox" checked={!!checked} {disabled} onchange={change} />
  <span class="box">{#if checked}<svg viewBox="0 0 10 10" width="9" height="9"><path d="M1.5 5.2 4 7.7 8.6 2.3" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="square" /></svg>{/if}</span>
  <span class="lbl">{label}{@render children?.()}</span>
</label>

<style>
  .cb { display: inline-flex; align-items: center; gap: 7px; cursor: pointer; user-select: none; }
  .disabled { opacity: 0.4; cursor: not-allowed; }
  input { position: absolute; opacity: 0; width: 0; height: 0; }
  .box { width: 13px; height: 13px; border: 1px solid var(--fg-mute); background: var(--bg); display: inline-flex; align-items: center; justify-content: center; color: var(--fg); flex: none; }
  input:checked + .box { border-color: var(--fg); background: var(--bg-3); box-shadow: var(--glow-sm); }
  input:focus-visible + .box { outline: 1px solid var(--fg); outline-offset: 2px; }
  .cb:hover:not(.disabled) .box { border-color: var(--fg-dim); }
</style>
