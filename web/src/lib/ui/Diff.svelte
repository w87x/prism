<script>
  // Unified or side-by-side line diff of two texts, with word-level highlights and folded context.
  import { diffText, diffStats, fold } from '../diff.js';
  let { before = '', after = '', split = false, context = 3, empty = 'no changes' } = $props();

  const rows = $derived(diffText(before, after));
  const stats = $derived(diffStats(rows));
  let open = $state({}); // fold index → expanded
  const view = $derived(fold(rows, context));

  // side-by-side: pair each deletion run with the additions that follow it
  const pairs = $derived.by(() => {
    const out = [];
    let dels = [], adds = [];
    const flush = () => {
      for (let k = 0; k < Math.max(dels.length, adds.length); k++) out.push({ l: dels[k] || null, r: adds[k] || null });
      dels = []; adds = [];
    };
    for (const r of view.flatMap((x, i) => (x.t === 'fold' && open[i] ? x.rows : [x]))) {
      if (r.t === 'del') dels.push(r);
      else if (r.t === 'add') adds.push(r);
      else { flush(); out.push(r.t === 'fold' ? { fold: r } : { l: r, r }); }
    }
    flush();
    return out;
  });
</script>

{#snippet line(r)}
  {#if r.segs}{#each r.segs as s}{#if s.hl}<mark>{s.v}</mark>{:else}{s.v}{/if}{/each}{:else}{r.text || ' '}{/if}
{/snippet}

{#if stats.changed === 0}
  <div class="none">{empty}</div>
{:else if !split}
  <div class="df" role="table" aria-label="changes">
    {#each view as r, i}
      {#if r.t === 'fold'}
        {#if open[i]}
          {#each r.rows as x}<div class="ln eq"><span class="no">{x.a}</span><span class="no">{x.b}</span><span class="sg"> </span><span class="tx">{x.text || ' '}</span></div>{/each}
        {:else}
          <button type="button" class="fold" onclick={() => (open[i] = true)}>⋯ {r.n} unchanged lines</button>
        {/if}
      {:else}
        <div class="ln {r.t}"><span class="no">{r.a ?? ''}</span><span class="no">{r.b ?? ''}</span><span class="sg">{r.t === 'add' ? '+' : r.t === 'del' ? '−' : ' '}</span><span class="tx">{@render line(r)}</span></div>
      {/if}
    {/each}
  </div>
{:else}
  <div class="df sp" role="table" aria-label="changes side by side">
    {#each pairs as p, i}
      {#if p.fold}
        <button type="button" class="fold wide" onclick={() => (open[view.indexOf(p.fold)] = true)}>⋯ {p.fold.n} unchanged lines</button>
      {:else}
        <div class="ln half {p.l ? p.l.t : 'ph'}"><span class="no">{p.l?.a ?? ''}</span><span class="tx">{#if p.l}{@render line(p.l)}{/if}</span></div>
        <div class="ln half {p.r ? p.r.t : 'ph'}"><span class="no">{p.r?.b ?? ''}</span><span class="tx">{#if p.r}{@render line(p.r)}{/if}</span></div>
      {/if}
    {/each}
  </div>
{/if}

<style>
  .df { font-size: 12px; line-height: 1.5; border: 1px solid var(--line); background: var(--bg); overflow: auto; max-height: 52vh; }
  .df.sp { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); }
  .ln { display: flex; min-width: 0; }
  .ln.half { border-left: 1px solid transparent; }
  .no { flex: none; width: 3.2em; text-align: right; padding-right: 8px; color: var(--fg-faint); user-select: none; }
  .sg { flex: none; width: 1.6em; text-align: center; color: var(--fg-mute); user-select: none; }
  .tx { flex: 1; min-width: 0; white-space: pre-wrap; overflow-wrap: anywhere; color: var(--fg-dim); padding-right: 8px; }
  .add { background: color-mix(in srgb, var(--fg) 11%, transparent); }
  .add .tx, .add .sg { color: var(--fg-hi); }
  .del { background: color-mix(in srgb, var(--err) 13%, transparent); }
  .del .tx, .del .sg { color: var(--err-hi); }
  .ph { background: repeating-linear-gradient(135deg, transparent 0 5px, color-mix(in srgb, var(--line) 60%, transparent) 5px 6px); }
  mark { color: inherit; border-radius: 1px; }
  .add mark { background: color-mix(in srgb, var(--fg) 34%, transparent); }
  .del mark { background: color-mix(in srgb, var(--err) 38%, transparent); }
  .fold { display: block; width: 100%; background: var(--bg-2); border: 0; border-top: 1px solid var(--line); border-bottom: 1px solid var(--line); color: var(--fg-mute); padding: 1px 8px; text-align: left; font-size: 11px; }
  .fold:hover { color: var(--fg); background: var(--bg-3); }
  .fold.wide { grid-column: 1 / -1; }
  .none { padding: 14px; text-align: center; color: var(--fg-mute); border: 1px dashed var(--line-2); }
</style>
