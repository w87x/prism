<script>
  // Free-form list of strings. Enter / comma adds, Backspace removes the last.
  let { value = $bindable(), placeholder = 'add…', disabled = false, onchange } = $props();
  let draft = $state('');
  function add() {
    const v = draft.trim().replace(/,$/, '');
    if (v && !(value || []).includes(v)) { value = [...(value || []), v]; onchange?.(value); }
    draft = '';
  }
  function remove(v) { value = (value || []).filter((x) => x !== v); onchange?.(value); }
</script>

<div class="tg" class:disabled>
  {#each value || [] as v}<span class="chip">{v}<button type="button" tabindex="-1" onclick={() => remove(v)} {disabled}>×</button></span>{/each}
  <input bind:value={draft} {placeholder} {disabled} spellcheck="false"
    onkeydown={(e) => {
      if (e.key === 'Enter' || e.key === ',') { e.preventDefault(); add(); }
      else if (e.key === 'Backspace' && !draft && value?.length) remove(value[value.length - 1]);
    }}
    onblur={add} />
</div>

<style>
  .tg { display: flex; flex-wrap: wrap; gap: 3px; background: var(--bg); border: 1px solid var(--line-2); border-radius: var(--r); padding: 3px 5px; min-height: 24px; }
  .tg:focus-within { border-color: var(--fg); box-shadow: var(--glow-sm); }
  .disabled { opacity: 0.5; }
  .chip { display: inline-flex; align-items: center; gap: 4px; padding: 0 4px; background: var(--bg-3); border: 1px solid var(--line-2); font-size: var(--fs-sm); color: var(--fg); }
  .chip button { background: none; border: 0; color: var(--fg-mute); padding: 0; line-height: 1; } .chip button:hover { color: var(--err); }
  input { flex: 1; min-width: 70px; background: transparent; border: 0; outline: 0; color: var(--fg-hi); caret-color: var(--fg); padding: 0 2px; }
  input::placeholder { color: var(--fg-faint); }
</style>
