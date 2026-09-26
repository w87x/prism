<script>
  // Phone replacement for the bottom status bar: one dot in the top bar showing the worst state, and a sheet that
  // spells every LED out (hover tooltips do not exist on a touch screen).
  import { S, activeRuns, go } from './store.svelte.js';
  import Led from './ui/Led.svelte';

  let open = $state(false);
  const st = $derived(S.status);
  const leds = $derived(st?.leds || []);
  const rank = { error: 4, attention: 3, warn: 2, standby: 1, ok: 1, off: 0 };
  const worst = $derived.by(() => {
    let w = S.conn === 'open' ? 'ok' : S.conn === 'connecting' ? 'warn' : 'error';
    for (const l of leds) if ((rank[l.state] || 0) > (rank[w] || 0) && l.state !== 'standby') w = l.state;
    return w;
  });
  const busy = $derived(activeRuns().filter((r) => !r.done).length);
  const where = { ask: 'chat', telegram: 'settings', browser: 'settings', obsidian: 'settings', mcp: 'tools', auto: 'autonomy' };
  const say = { ok: 'fine', standby: 'ready', attention: 'needs you', warn: 'warning', error: 'problem', off: 'off' };
  function pick(l) { if (where[l.id]) { go(where[l.id]); open = false; } }
  const tasks = $derived(st ? [st.queue && `${st.queue} queued`, st.running && `${st.running} running`, st.waiting && `${st.waiting} waiting`].filter(Boolean).join(' · ') || 'idle' : '');
</script>

<button type="button" class="dotbtn" aria-label="System status" title="System status" onclick={() => (open = !open)}>
  <Led state={worst} pulse={worst === 'attention' || worst === 'error'} live={busy > 0} size={9} />
</button>

{#if open}
  <button type="button" class="veil" aria-label="close" onclick={() => (open = false)}></button>
  <div class="sheet" role="dialog" aria-label="System status">
    <div class="grip"></div>
    <div class="row"><span class="k">Connection</span><span class="v"><Led state={S.conn === 'open' ? 'ok' : S.conn === 'connecting' ? 'warn' : 'error'} size={8} /> {S.conn === 'open' ? 'online' : S.conn}</span></div>
    <div class="row"><span class="k">Tasks</span><span class="v">{tasks}</span></div>
    <div class="row"><span class="k">Agents thinking</span><span class="v">{busy || 'none'}</span></div>
    {#each leds as l (l.id)}
      <button type="button" class="row btn" class:link={where[l.id]} onclick={() => pick(l)}>
        <span class="k"><Led state={l.state} size={8} /> {l.label}</span><span class="v">{l.detail || say[l.state] || l.state}</span>
      </button>
    {/each}
    {#if st && !st.pgvector}<div class="row"><span class="k">Vectors</span><span class="v warn">fallback (pgvector missing)</span></div>{/if}
    <button type="button" class="row btn link" onclick={() => { S.wallOpen = true; open = false; }}><span class="k">Thinking wall</span><span class="v">open ›</span></button>
  </div>
{/if}

<style>
  .dotbtn { display: inline-flex; align-items: center; justify-content: center; width: 32px; height: 32px; padding: 0; min-height: 0; background: none; border: 0; }
  .veil { position: fixed; inset: 0; z-index: 130; background: rgba(0, 0, 0, 0.5); border: 0; padding: 0; }
  .sheet { position: fixed; z-index: 131; left: 0; right: 0; bottom: 0; max-height: 75dvh; overflow-y: auto; background: var(--bg); border-top: 1px solid var(--line-3); box-shadow: 0 -10px 30px rgba(0, 0, 0, 0.6); padding: 0 0 calc(8px + env(safe-area-inset-bottom)); }
  .grip { width: 40px; height: 4px; background: var(--line-3); margin: 8px auto; border-radius: 2px; }
  .row { display: flex; align-items: center; justify-content: space-between; gap: 12px; width: 100%; min-height: 44px; padding: 8px 16px; border: 0; border-bottom: 1px solid var(--line); background: none; color: var(--fg); text-align: left; font-size: 12px; text-transform: uppercase; letter-spacing: 0.07em; }
  .k { display: inline-flex; align-items: center; gap: 8px; color: var(--fg-dim); }
  .v { color: var(--fg-hi); text-align: right; text-transform: none; letter-spacing: 0; display: inline-flex; align-items: center; gap: 6px; }
  .v.warn { color: var(--warn); }
  .link .v { color: var(--accent-hi); }
</style>
