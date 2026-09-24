<script>
  // Fully themed dropdown (no native <select>): keyboard, search, fixed-position popup.
  let { value = $bindable(), options = [], placeholder = 'select…', disabled = false, searchable = false, onchange, size = 'md', clearable = false } = $props();
  let open = $state(false);
  let q = $state('');
  let idx = $state(0);
  let trigger = $state();
  let pop = $state();
  let rect = $state({ left: 0, top: 0, width: 0, up: false, bottom: 0, maxH: 260 });

  const items = $derived(options.map((o) => (typeof o === 'object' && o !== null ? o : { value: o, label: String(o) })));
  const filtered = $derived(q ? items.filter((i) => `${i.label} ${i.hint || ''}`.toLowerCase().includes(q.toLowerCase())) : items);
  const current = $derived(items.find((i) => i.value === value));

  function place() {
    const r = trigger.getBoundingClientRect();
    const below = window.innerHeight - r.bottom - 8;
    const above = r.top - 8;
    const up = below < 180 && above > below;
    rect = { left: r.left, top: r.bottom + 2, width: Math.max(r.width, 160, items.some((i) => i.hint && String(i.hint).length > 12) ? 380 : 0), up, bottom: window.innerHeight - r.top + 2, maxH: Math.min(300, up ? above : below) };
  }
  function toggle() {
    if (disabled) return;
    open = !open;
    if (open) { q = ''; place(); idx = Math.max(0, items.findIndex((i) => i.value === value)); queueMicrotask(scrollToIdx); }
  }
  function pick(it) {
    if (it.disabled) return;
    value = it.value; open = false; onchange?.(it.value); trigger?.focus();
  }
  function scrollToIdx() { pop?.querySelector('.it.hl')?.scrollIntoView({ block: 'nearest' }); }
  function key(e) {
    if (!open) { if (['ArrowDown', 'ArrowUp', 'Enter', ' '].includes(e.key)) { e.preventDefault(); toggle(); } return; }
    if (e.key === 'Escape') { e.preventDefault(); open = false; trigger?.focus(); }
    else if (e.key === 'ArrowDown') { e.preventDefault(); idx = Math.min(filtered.length - 1, idx + 1); scrollToIdx(); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); idx = Math.max(0, idx - 1); scrollToIdx(); }
    else if (e.key === 'Enter') { e.preventDefault(); if (filtered[idx]) pick(filtered[idx]); }
    else if (e.key === 'Tab') open = false;
  }
  function outside(e) { if (open && !trigger?.contains(e.target) && !pop?.contains(e.target)) open = false; }
</script>

<svelte:window onpointerdown={outside} onresize={() => open && place()} />

<div class="sel {size}">
  <button type="button" bind:this={trigger} class="tr" class:open class:ph={!current} {disabled} onclick={toggle} onkeydown={key} aria-haspopup="listbox" aria-expanded={open}>
    <span class="v ellipsis">{current ? current.label : placeholder}</span>
    {#if clearable && current}<span class="x" role="button" tabindex="-1" onclick={(e) => { e.stopPropagation(); value = undefined; onchange?.(undefined); }} onkeydown={(e) => e.key === 'Enter' && (value = undefined)}>×</span>{/if}
    <span class="car">▾</span>
  </button>
</div>

{#if open}
  <div class="pop" bind:this={pop} role="listbox"
    style="left:{rect.left}px; width:{rect.width}px; max-height:{rect.maxH}px; {rect.up ? `bottom:${rect.bottom}px` : `top:${rect.top}px`}">
    {#if searchable || items.length > 9}
      <!-- svelte-ignore a11y_autofocus -->
      <input class="q" placeholder="filter…" bind:value={q} autofocus onkeydown={key} oninput={() => (idx = 0)} />
    {/if}
    <div class="list">
      {#each filtered as it, i}
        <div class="it" class:hl={i === idx} class:sel={it.value === value} class:dis={it.disabled} role="option" aria-selected={it.value === value} tabindex="-1"
          onpointerenter={() => (idx = i)} onclick={() => pick(it)} onkeydown={(e) => e.key === 'Enter' && pick(it)}>
          <span class="ck">{it.value === value ? '▸' : ''}</span>
          <span class="ellipsis">{it.label}</span>
          {#if it.hint}<span class="hint ellipsis">{it.hint}</span>{/if}
        </div>
      {:else}
        <div class="it dis">no matches</div>
      {/each}
    </div>
  </div>
{/if}

<style>
  .sel { position: relative; width: 100%; }
  .tr { width: 100%; display: flex; align-items: center; gap: 6px; text-align: left; background: var(--bg); border: 1px solid var(--line-2); border-radius: var(--r); padding: 3px 6px 3px 8px; color: var(--fg-hi); min-height: 24px; }
  .sm .tr { padding: 1px 5px 1px 7px; font-size: var(--fs-sm); min-height: 20px; }
  .tr:hover:not(:disabled) { border-color: var(--fg-dim); }
  .tr.open, .tr:focus-visible { border-color: var(--fg); box-shadow: var(--glow-sm); outline: 0; }
  .tr:disabled { opacity: 0.5; cursor: not-allowed; }
  .ph .v { color: var(--fg-faint); }
  .v { flex: 1; }
  .car { color: var(--fg-mute); font-size: 10px; }
  .x { color: var(--fg-mute); padding: 0 3px; } .x:hover { color: var(--err); }
  .pop { position: fixed; z-index: 3000; background: var(--bg-1); border: 1px solid var(--fg-dim); box-shadow: 0 6px 24px rgba(0, 0, 0, 0.7), var(--glow-sm); display: flex; flex-direction: column; }
  .q { background: var(--bg); border: 0; border-bottom: 1px solid var(--line-2); padding: 4px 8px; outline: 0; color: var(--fg-hi); }
  .list { overflow: auto; }
  .it { display: flex; align-items: center; gap: 6px; padding: 3px 8px 3px 4px; cursor: pointer; }
  .it.hl { background: var(--bg-4); }
  .it.sel { color: var(--fg-hi); }
  .it.dis { color: var(--fg-faint); cursor: default; }
  .ck { width: 10px; color: var(--fg); flex: none; }
  .hint { margin-left: auto; color: var(--fg-faint); font-size: 10.5px; max-width: 62%; }
</style>
