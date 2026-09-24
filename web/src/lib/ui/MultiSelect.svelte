<script>
  // Themed multi-select with checkmarks and search. value: string[]
  let { value = $bindable(), options = [], placeholder = 'none', disabled = false, onchange, chips = true } = $props();
  let open = $state(false);
  let q = $state('');
  let trigger = $state();
  let pop = $state();
  let rect = $state({ left: 0, top: 0, width: 0, up: false, bottom: 0, maxH: 280 });

  const items = $derived(options.map((o) => (typeof o === 'object' && o !== null ? o : { value: o, label: String(o) })));
  const filtered = $derived(q ? items.filter((i) => `${i.label} ${i.hint || ''} ${i.group || ''}`.toLowerCase().includes(q.toLowerCase())) : items);
  const set = $derived(new Set(value || []));

  function place() {
    const r = trigger.getBoundingClientRect();
    const below = window.innerHeight - r.bottom - 8, above = r.top - 8;
    const up = below < 200 && above > below;
    rect = { left: r.left, top: r.bottom + 2, width: Math.max(r.width, 260), up, bottom: window.innerHeight - r.top + 2, maxH: Math.min(340, up ? above : below) };
  }
  function toggleOpen() { if (disabled) return; open = !open; if (open) { q = ''; place(); } }
  function flip(v) {
    const s = new Set(value || []);
    s.has(v) ? s.delete(v) : s.add(v);
    value = [...s]; onchange?.(value);
  }
  function outside(e) { if (open && !trigger?.contains(e.target) && !pop?.contains(e.target)) open = false; }
</script>

<svelte:window onpointerdown={outside} onresize={() => open && place()} />

<button type="button" bind:this={trigger} class="tr" class:open {disabled} onclick={toggleOpen} onkeydown={(e) => e.key === 'Escape' && (open = false)}>
  <span class="chips">
    {#if !value?.length}<span class="ph">{placeholder}</span>
    {:else if chips}{#each value.slice(0, 40) as v}<span class="chip">{v}<span class="x" role="button" tabindex="-1" onclick={(e) => { e.stopPropagation(); flip(v); }} onkeydown={(e) => e.key === 'Enter' && flip(v)}>×</span></span>{/each}
    {:else}<span>{value.length} selected</span>{/if}
  </span>
  <span class="car">▾</span>
</button>

{#if open}
  <div class="pop" bind:this={pop} style="left:{rect.left}px; width:{rect.width}px; max-height:{rect.maxH}px; {rect.up ? `bottom:${rect.bottom}px` : `top:${rect.top}px`}">
    <!-- svelte-ignore a11y_autofocus -->
    <input class="q" placeholder="filter…" bind:value={q} autofocus onkeydown={(e) => e.key === 'Escape' && (open = false)} />
    <div class="list">
      {#each filtered as it}
        <div class="it" class:sel={set.has(it.value)} role="option" aria-selected={set.has(it.value)} tabindex="0" onclick={() => flip(it.value)} onkeydown={(e) => (e.key === 'Enter' || e.key === ' ') && (e.preventDefault(), flip(it.value))}>
          <span class="box">{#if set.has(it.value)}<svg viewBox="0 0 10 10" width="9" height="9"><path d="M1.5 5.2 4 7.7 8.6 2.3" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="square" /></svg>{/if}</span>
          <span class="ellipsis">{it.label}</span>
          {#if it.badge}<span class="bdg">{it.badge}</span>{/if}
          {#if it.hint}<span class="hint ellipsis">{it.hint}</span>{/if}
        </div>
      {:else}
        <div class="it dis">no matches</div>
      {/each}
    </div>
  </div>
{/if}

<style>
  .tr { width: 100%; display: flex; align-items: flex-start; gap: 6px; text-align: left; background: var(--bg); border: 1px solid var(--line-2); border-radius: var(--r); padding: 3px 6px 3px 6px; color: var(--fg-hi); min-height: 24px; }
  .tr:hover:not(:disabled) { border-color: var(--fg-dim); }
  .tr.open { border-color: var(--fg); box-shadow: var(--glow-sm); }
  .tr:disabled { opacity: 0.5; }
  .chips { flex: 1; display: flex; flex-wrap: wrap; gap: 3px; min-width: 0; }
  .ph { color: var(--fg-faint); padding-left: 2px; }
  .chip { display: inline-flex; align-items: center; gap: 4px; padding: 0 4px; background: var(--bg-3); border: 1px solid var(--line-2); font-size: var(--fs-sm); color: var(--fg); }
  .x { color: var(--fg-mute); cursor: pointer; } .x:hover { color: var(--err); }
  .car { color: var(--fg-mute); font-size: 10px; align-self: center; }
  .pop { position: fixed; z-index: 3000; background: var(--bg-1); border: 1px solid var(--fg-dim); box-shadow: 0 6px 24px rgba(0, 0, 0, 0.7), var(--glow-sm); display: flex; flex-direction: column; }
  .q { background: var(--bg); border: 0; border-bottom: 1px solid var(--line-2); padding: 4px 8px; outline: 0; color: var(--fg-hi); }
  .list { overflow: auto; }
  .it { display: flex; align-items: center; gap: 7px; padding: 3px 8px; cursor: pointer; }
  .it:hover { background: var(--bg-4); }
  .it.sel { color: var(--fg-hi); }
  .it.dis { color: var(--fg-faint); cursor: default; }
  .box { width: 12px; height: 12px; border: 1px solid var(--fg-mute); display: inline-flex; align-items: center; justify-content: center; flex: none; color: var(--fg); }
  .sel .box { border-color: var(--fg); background: var(--bg-3); }
  .hint { margin-left: auto; color: var(--fg-mute); font-size: var(--fs-sm); max-width: 50%; }
  .bdg { font-size: 10px; color: var(--attn); border: 1px solid var(--attn-dim); padding: 0 3px; }
</style>
