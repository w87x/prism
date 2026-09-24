<script>
  let { checked = $bindable(), label = '', disabled = false, onchange, tone = 'ok', title = '' } = $props();
  // onchange may return false (or a promise of false) to veto the change, e.g. a declined confirmation
  async function change(e) {
    const el = e.currentTarget, v = el.checked;
    checked = v;
    if ((await onchange?.(v)) === false) { checked = !v; el.checked = !v; }
  }
</script>

<label class="sw {tone}" class:disabled {title}>
  <input type="checkbox" checked={!!checked} {disabled} onchange={change} />
  <span class="track"><span class="knob"></span></span>
  {#if label}<span class="lbl">{label}</span>{/if}
</label>

<style>
  .sw { --c: var(--fg); display: inline-flex; align-items: center; gap: 7px; cursor: pointer; user-select: none; }
  .accent { --c: var(--accent); } .attn { --c: var(--attn); }
  .disabled { opacity: 0.4; cursor: not-allowed; }
  input { position: absolute; opacity: 0; width: 0; height: 0; }
  .track { width: 26px; height: 13px; border: 1px solid var(--fg-mute); border-radius: 0; background: var(--bg); position: relative; flex: none; transition: all 0.12s; }
  .knob { position: absolute; top: 1px; left: 1px; width: 9px; height: 9px; border-radius: 0; background: var(--fg-faint); transition: all 0.12s; }
  input:checked + .track { border-color: var(--c); background: color-mix(in srgb, var(--c) 16%, var(--bg)); box-shadow: 0 0 6px color-mix(in srgb, var(--c) 40%, transparent); }
  input:checked + .track .knob { left: 14px; background: var(--c); box-shadow: 0 0 6px var(--c); }
  input:focus-visible + .track { outline: 1px solid var(--c); outline-offset: 2px; }
  .lbl { font-size: var(--fs); }
</style>
