<script>
  // Universal search (Cmd+K / Ctrl+K): one box across memory, trackers, knowledge and tasks instead of a
  // separate search box per page. Read-only fan-out (see internal/search) — this component just renders
  // and navigates.
  import { S, call, go } from './store.svelte.js';
  import Icon from './ui/Icon.svelte';

  let q = $state('');
  let results = $state([]);
  let idx = $state(0);
  let inputEl;
  let timer;

  function onKey(e) {
    const mod = e.metaKey || e.ctrlKey;
    if (mod && e.key.toLowerCase() === 'k') {
      e.preventDefault();
      S.searchOpen ? close() : show();
      return;
    }
    if (!S.searchOpen) return;
    if (e.key === 'Escape') { e.preventDefault(); close(); }
    else if (e.key === 'ArrowDown') { e.preventDefault(); idx = Math.min(idx + 1, results.length - 1); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); idx = Math.max(idx - 1, 0); }
    else if (e.key === 'Enter') { e.preventDefault(); pick(results[idx]); }
  }
  $effect(() => {
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  });
  // opened from elsewhere (e.g. the top-bar button) by setting S.searchOpen directly
  $effect(() => { if (S.searchOpen) queueMicrotask(() => inputEl?.focus()); });

  function show() { S.searchOpen = true; q = ''; results = []; idx = 0; queueMicrotask(() => inputEl?.focus()); }
  function close() { S.searchOpen = false; }

  function search() {
    clearTimeout(timer);
    const query = q.trim();
    if (!query) { results = []; return; }
    timer = setTimeout(async () => {
      const r = await call('search.all', { q: query }, { quiet: true });
      results = r || [];
      idx = 0;
    }, 150);
  }

  const kindLabel = { memory: 'Memory', tracker: 'Tracker', knowledge: 'Knowledge', task: 'Task' };
  const kindIcon = { memory: 'memory', tracker: 'table', knowledge: 'kb', task: 'tasks' };

  function pick(r) {
    if (!r) return;
    if (r.kind === 'memory') { S.selectedFact = r.id; go('memory'); }
    else if (r.kind === 'tracker') { S.selectedTrackerName = r.title; go('trackers'); }
    else if (r.kind === 'knowledge') { S.selectedKbPage = r.id; go('kb'); }
    else if (r.kind === 'task') { S.selectedTask = r.id; go('tasks'); }
    close();
  }
</script>

{#if S.searchOpen}
  <div class="scrim" onclick={close} role="presentation">
    <!-- svelte-ignore a11y_no_static_element_interactions a11y_click_events_have_key_events -->
    <div class="pal" onclick={(e) => e.stopPropagation()}>
      <div class="qrow">
        <Icon name="search" size={13} />
        <input bind:this={inputEl} bind:value={q} oninput={search} placeholder="Search memory, trackers, knowledge, tasks…" />
        <span class="esc">esc</span>
      </div>
      <div class="results">
        {#each results as r, i (r.kind + r.id)}
          <button type="button" class="row" class:on={i === idx} onmouseenter={() => (idx = i)} onclick={() => pick(r)}>
            <span class="kico"><Icon name={kindIcon[r.kind]} size={12} /></span>
            <span class="kk">{kindLabel[r.kind]}</span>
            <span class="tt ellipsis">{r.title}</span>
            {#if r.extra}<span class="ex mute sm">{r.extra}</span>{/if}
          </button>
        {:else}
          {#if q.trim()}<div class="empty sm mute">nothing found</div>
          {:else}<div class="empty sm mute">type to search across memory, trackers, knowledge and tasks</div>{/if}
        {/each}
      </div>
    </div>
  </div>
{/if}

<style>
  .scrim { position: fixed; inset: 0; background: rgba(0, 0, 0, 0.45); z-index: 200; display: flex; align-items: flex-start; justify-content: center; padding-top: 12vh; }
  .pal { width: min(640px, 92vw); max-height: 60vh; display: flex; flex-direction: column; background: var(--panel-bg); border: 1px solid var(--line-2); border-radius: var(--r); box-shadow: 0 12px 40px rgba(0, 0, 0, 0.5); overflow: hidden; }
  .qrow { display: flex; align-items: center; gap: 8px; padding: 10px 12px; border-bottom: 1px solid var(--line-2); color: var(--ico); }
  .qrow input { flex: 1; background: none; border: 0; color: var(--fg-hi); font: inherit; font-size: 13px; }
  .qrow input:focus { outline: none; }
  .esc { font-size: 10px; color: var(--fg-mute); border: 1px solid var(--line-2); border-radius: 3px; padding: 1px 5px; }
  .results { overflow: auto; padding: 4px; }
  .row { display: flex; align-items: center; gap: 8px; width: 100%; padding: 6px 8px; background: none; border: 0; border-radius: 4px; color: var(--fg-dim); text-align: left; }
  .row.on, .row:hover { background: var(--bg-3); color: var(--fg-hi); }
  .kico { color: var(--ico); flex: none; }
  .kk { flex: none; width: 64px; font-size: 10px; text-transform: uppercase; letter-spacing: 0.08em; color: var(--fg-mute); }
  .tt { flex: 1; min-width: 0; color: var(--fg-hi); }
  .ex { flex: none; max-width: 140px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .empty { padding: 16px 10px; text-align: center; }
</style>
