<script>
  // Phone layout of Memory: search, bank chips, fact cards, and a bottom sheet for the one you open.
  import { S, call, listen, ago } from '../lib/store.svelte.js';
  import Icon from '../lib/ui/Icon.svelte';
  import Badge from '../lib/ui/Badge.svelte';
  import Provenance from '../lib/Provenance.svelte';

  let banks = $state([]);
  let bank = $state(0);
  let q = $state('');
  let facts = $state([]);
  let kind = $state('');
  let busy = $state(false);
  let sel = $state(null);
  const PAGE = 60;

  const label = (b) => (b.kind === 'user' ? 'user' : `${b.kind}:${b.name}`);
  async function loadBanks() { banks = (await call('memory.banks', {}, { quiet: true })) || []; }
  async function loadFacts() {
    busy = true;
    let r;
    if (q.trim()) {
      const b = banks.find((x) => x.id === bank);
      r = ((await call('memory.find', { query: q.trim(), banks: b ? [label(b)] : [], k: 30 })) || []).filter((f) => !kind || f.kind === kind);
    } else {
      r = (await call('memory.facts', { bank_id: bank, kind, limit: PAGE }, { quiet: true })) || [];
    }
    facts = r;
    busy = false;
  }
  $effect(() => { loadBanks(); return listen('memory.update', () => { loadBanks(); loadFacts(); }); });
  $effect(() => { bank; kind; loadFacts(); });
  $effect(() => { if (S.memoryBank) { bank = S.memoryBank; S.memoryBank = 0; } });
  $effect(() => { if (S.selectedFact != null) { const id = S.selectedFact; S.selectedFact = null; call('memory.fact', { id }).then((f) => f && (sel = f)); } });
  let timer;
  function typing() { clearTimeout(timer); timer = setTimeout(loadFacts, 350); }
  const conf = (f) => `${Math.round((f.confidence ?? 1) * 100)}%`;
</script>

<div class="mm">
  <div class="search">
    <Icon name="search" size={15} />
    <input type="search" placeholder="Search what PRISM knows…" bind:value={q} oninput={typing} enterkeyhint="search" />
  </div>
  <div class="chips" role="tablist">
    {#each [['', 'Everything'], ['fact', 'Facts'], ['conclusion', 'Conclusions']] as [v, l]}<button type="button" class="chip kind" class:on={kind === v} onclick={() => (kind = v)}>{l}</button>{/each}
    <span class="sep"></span>
    <button type="button" class="chip" class:on={bank === 0} onclick={() => (bank = 0)}>All banks</button>
    {#each banks as b (b.id)}<button type="button" class="chip" class:on={bank === b.id} onclick={() => (bank = b.id)}>{label(b)}<i>{b.facts ?? ''}</i></button>{/each}
  </div>

  <div class="list">
    {#each facts as f (f.id)}
      <button type="button" class="fc" onclick={() => (sel = f)}>
        <div class="tx">{f.text}</div>
        <div class="meta">
          {#if f.kind === 'conclusion'}<Badge tone="accent">{f.source === 'synthesis' ? 'L2' : f.source === 'principle' ? 'L3' : 'conclusion'}</Badge>{/if}
          <span>{f.bank}</span><span>{conf(f)}</span><span>{ago(f.created_at)}</span>
        </div>
      </button>
    {:else}
      <div class="empty">{busy ? 'loading…' : q ? 'nothing matches' : 'no facts here yet'}</div>
    {/each}
  </div>
</div>

{#if sel}
  <button type="button" class="veil" aria-label="close" onclick={() => (sel = null)}></button>
  <div class="sheet">
    <div class="grip"></div>
    <div class="head"><span class="mute sm">{sel.bank} · {sel.kind} · {conf(sel)}</span><button type="button" class="x" onclick={() => (sel = null)}>✕</button></div>
    <div class="body">
      <p class="full">{sel.text}</p>
      {#if sel.tags?.length}<div class="tags">{#each sel.tags as t}<Badge tone="mute">{t}</Badge>{/each}</div>{/if}
      <Provenance id={sel.id} onopen={(id) => call('memory.fact', { id }).then((f) => f && (sel = f))} />
    </div>
  </div>
{/if}

<style>
  .mm { height: 100%; display: flex; flex-direction: column; gap: 8px; min-height: 0; padding: 2px; }
  .search { display: flex; align-items: center; gap: 8px; padding: 0 12px; height: 46px; flex: none; background: var(--bg-1); border: 1px solid var(--line-3); color: var(--fg-mute); }
  .search input { flex: 1; min-width: 0; height: 100%; background: none; border: 0; outline: 0; color: var(--fg-hi); font: inherit; font-size: 16px; }
  .chips { display: flex; gap: 6px; overflow-x: auto; flex: none; padding-bottom: 2px; scrollbar-width: none; }
  .chips::-webkit-scrollbar { display: none; }
  .chip { flex: none; min-height: 36px; padding: 0 12px; background: var(--bg-1); border: 1px solid var(--line-2); color: var(--fg-dim); font-size: var(--fs-sm); white-space: nowrap; display: inline-flex; align-items: center; gap: 6px; }
  .chip i { font-style: normal; color: var(--fg-mute); }
  .sep { flex: none; width: 1px; background: var(--line-3); margin: 4px 2px; }
  .chip.kind { border-style: dashed; }
  .chip.sm { min-height: 30px; font-size: 11px; }
  .chip.on { color: var(--fg-hi); border-color: var(--accent); background: linear-gradient(180deg, rgba(62, 232, 166, 0.14), transparent); }
  .list { flex: 1; min-height: 0; overflow-y: auto; display: flex; flex-direction: column; gap: 6px; padding-bottom: 8px; -webkit-overflow-scrolling: touch; }
  .fc { flex: none; text-align: left; display: block; width: 100%; padding: 11px 12px; background: var(--panel-bg); border: 1px solid var(--line-2); color: var(--fg); }
  .fc:active { background: var(--bg-2); }
  .tx { line-height: 1.45; overflow-wrap: anywhere; display: -webkit-box; -webkit-line-clamp: 4; line-clamp: 4; -webkit-box-orient: vertical; overflow: hidden; }
  .meta { display: flex; flex-wrap: wrap; gap: 4px 10px; align-items: center; margin-top: 6px; color: var(--fg-mute); font-size: 11px; }
  .empty { padding: 40px; text-align: center; color: var(--fg-mute); text-transform: uppercase; letter-spacing: 0.12em; }
  .veil { position: fixed; inset: 0; z-index: 90; background: rgba(0, 0, 0, 0.55); border: 0; padding: 0; }
  .sheet { position: fixed; z-index: 91; left: 0; right: 0; bottom: 0; max-height: 82dvh; display: flex; flex-direction: column; background: var(--bg); border-top: 1px solid var(--line-3); box-shadow: 0 -10px 30px rgba(0, 0, 0, 0.6); padding-bottom: env(safe-area-inset-bottom); }
  .grip { width: 40px; height: 4px; background: var(--line-3); margin: 8px auto 2px; border-radius: 2px; }
  .head { display: flex; align-items: center; justify-content: space-between; padding: 4px 14px; }
  .x { background: none; border: 0; color: var(--fg-dim); min-width: 40px; min-height: 40px; font-size: 16px; }
  .body { overflow-y: auto; padding: 4px 14px 16px; display: flex; flex-direction: column; gap: 10px; }
  .full { margin: 0; line-height: 1.5; color: var(--fg-hi); overflow-wrap: anywhere; white-space: pre-wrap; }
  .tags { display: flex; flex-wrap: wrap; gap: 4px; }
</style>
