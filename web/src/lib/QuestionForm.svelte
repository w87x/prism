<script>
  // The answer form of a question: one or more items, each single-choice, multiple-choice or free text; choices may carry a
  // short explanation ("label — why"), and choice items always offer "Other…" so the user is never boxed in. A lone
  // single-choice question answers on click. Used by agent asks (AskCard) and by briefings that ask something.
  import Button from './ui/Button.svelte';
  import Input from './ui/Input.svelte';
  let { items, onsubmit, submitLabel = 'Answer', compact = false } = $props();
  const split = (o) => { const i = o.indexOf(' — '); return i < 0 ? [o, ''] : [o.slice(0, i), o.slice(i + 3)]; };
  const solo = $derived(items.length === 1 && items[0].kind === 'single'); // click answers at once

  // per-item answer state
  let st = $state([]);
  $effect(() => { if (st.length !== items.length) st = items.map(() => ({ sel: [], other: false, text: '' })); });
  const pick = (i, k, multi) => {
    const s = st[i];
    if (multi) s.sel = s.sel.includes(k) ? s.sel.filter((x) => x !== k) : [...s.sel, k];
    else { s.sel = [k]; s.other = false; }
  };
  const toggleOther = (i, multi) => { const s = st[i]; s.other = !s.other; if (!multi && s.other) s.sel = []; };
  const valueOf = (it, s) => {
    if (!s) return '';
    if (it.kind === 'text') return s.text.trim();
    const parts = s.sel.slice().sort((a, b) => a - b).map((k) => split(it.options[k])[0]);
    if (s.other && s.text.trim()) parts.push(s.text.trim());
    return parts.join(', ');
  };
  const ready = $derived(items.every((it, i) => valueOf(it, st[i])));
  function submit() {
    if (!ready) return;
    const vals = items.map((it, i) => valueOf(it, st[i]));
    onsubmit(items.length === 1 ? vals[0] : items.map((it, i) => `${it.header || it.text}: ${vals[i]}`).join('\n'));
  }
</script>

<div class="qf" class:compact>
    {#each items as it, i}
      {@const multi = it.kind === 'multi'}
      <div class="item">
        {#if items.length > 1}<div class="itq">{#if it.header}<span class="hd">{it.header}</span>{/if}<span class="txt">{it.text}</span></div>{/if}
        {#if it.kind === 'text'}
          <div class="act"><div class="grow"><Input bind:value={st[i].text} size="sm" placeholder="type your answer…" onenter={() => (items.length === 1 ? submit() : null)} /></div></div>
        {:else}
          <div class="opts">
            {#each it.options as o, k}
              {@const [label, why] = split(o)}
              {#if solo}
                <button type="button" class="opt" onclick={() => onsubmit(label)}><span class="l">{label}</span>{#if why}<span class="w">{why}</span>{/if}</button>
              {:else}
                <button type="button" class="opt" class:on={st[i]?.sel.includes(k)} onclick={() => pick(i, k, multi)} aria-pressed={st[i]?.sel.includes(k)}>
                  <span class="box" class:round={!multi}>{st[i]?.sel.includes(k) ? '✓' : ''}</span><span class="l">{label}</span>{#if why}<span class="w">{why}</span>{/if}
                </button>
              {/if}
            {/each}
            <button type="button" class="opt other" class:on={st[i]?.other} onclick={() => toggleOther(i, multi)} aria-pressed={!!st[i]?.other}>
              {#if !solo}<span class="box" class:round={!multi}>{st[i]?.other ? '✓' : ''}</span>{/if}<span class="l">Other…</span>
            </button>
          </div>
          {#if st[i]?.other}
            <div class="act"><div class="grow"><Input bind:value={st[i].text} size="sm" placeholder="type your own answer…" onenter={submit} /></div></div>
          {/if}
        {/if}
      </div>
    {/each}
    {#if !solo || st[0]?.other}
      <div class="act"><Button size="sm" variant="primary" disabled={!ready} onclick={submit}>{items.length > 1 ? 'Send answers' : submitLabel}</Button></div>
    {/if}
</div>

<style>
  .qf { display: flex; flex-direction: column; gap: 6px; }
  .txt { color: var(--fg-hi); }
  .grow { flex: 1; min-width: 160px; }
  .act { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; }
  .item { display: flex; flex-direction: column; gap: 4px; }
  .itq { display: flex; gap: 6px; align-items: baseline; flex-wrap: wrap; }
  .hd { font-size: 10px; letter-spacing: 0.08em; text-transform: uppercase; color: var(--attn); border: 1px solid var(--attn-dim); padding: 0 5px; }
  .opts { display: flex; flex-wrap: wrap; gap: 5px; }
  .opt { display: inline-flex; align-items: baseline; gap: 6px; padding: 4px 9px; border: 1px solid var(--attn-dim); background: var(--attn-bg); color: var(--fg-hi); border-radius: var(--r); font: inherit; font-size: var(--fs-sm); cursor: pointer; text-align: left; }
  .opt:hover { border-color: var(--attn); }
  .opt.on { border-color: var(--attn); background: color-mix(in srgb, var(--attn) 22%, transparent); }
  .opt.other .l { color: var(--fg-dim); font-style: italic; }
  .w { color: var(--fg-dim); font-size: 11px; }
  .box { display: inline-flex; align-items: center; justify-content: center; width: 13px; height: 13px; border: 1px solid var(--attn); font-size: 10px; line-height: 1; flex: none; align-self: center; }
  .box.round { border-radius: 50%; }
  .compact .opt { padding: 3px 7px; }
</style>
