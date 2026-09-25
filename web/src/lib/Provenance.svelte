<script>
  import { call, go, S, stamp } from './store.svelte.js';
  import Badge from './ui/Badge.svelte';

  let { id, onopen } = $props();
  let p = $state(null);
  let loaded = 0;
  $effect(() => { if (id && id !== loaded) { loaded = id; p = null; call('memory.provenance', { id }, { quiet: true }).then((r) => (p = r)); } });
  const tone = { you: 'ok', confirmed: 'ok', document: 'accent', web: 'attn', agent: 'accent', conversation: 'mute', reflection: 'accent', analysis: 'accent' };
</script>

{#if p}
  <div class="pv">
    <div class="row"><b>Where this came from</b>{#if p.channel}<Badge tone={tone[p.channel] || 'mute'}>{p.channel}</Badge>{/if}</div>
    <div class="sm">{p.origin}</div>
    {#if p.task}<div class="sm">Task: <button type="button" class="lk" onclick={() => { S.selectedTask = p.task.id; go('tasks'); }}>#{p.task.id} {p.task.title}</button> <span class="mute">({p.task.agent}, {p.task.status})</span></div>{/if}
    {#if p.verified}<div class="sm mute">{p.verified}</div>{/if}
    {#if p.history.length > 1}
      <div class="sm mute">History of the wording</div>
      {#each p.history as v (v.id)}
        <div class="hv" class:cur={v.current}>
          <button type="button" class="lk" onclick={() => onopen?.(v.id)}>{v.text}</button>
          <span class="sm mute nowrap">{stamp(v.valid_from)}{v.valid_to ? ' → ' + stamp(v.valid_to) : ' → now'}{v.current ? ' · this one' : ''}</span>
        </div>
      {/each}
    {/if}
    {#if p.evidence.length}
      <div class="sm mute">Rests on</div>
      {#each p.evidence as f (f.id)}<div class="hv"><button type="button" class="lk" onclick={() => onopen?.(f.id)}>{f.text}</button><span class="sm mute nowrap">{f.bank}</span></div>{/each}
    {/if}
    {#if p.used_by.length}
      <div class="sm mute">Supports these conclusions</div>
      {#each p.used_by as f (f.id)}<div class="hv"><button type="button" class="lk" onclick={() => onopen?.(f.id)}>{f.text}</button></div>{/each}
    {/if}
    {#if p.contradicts.length}
      <div class="sm mute">In conflict with</div>
      {#each p.contradicts as f (f.id)}<div class="hv"><button type="button" class="lk" onclick={() => onopen?.(f.id)}>{f.text}</button></div>{/each}
    {/if}
    <div class="sm mute">Timeline</div>
    {#each p.events as e}<div class="sm ev">{e}</div>{/each}
  </div>
{/if}

<style>
  .pv { display: flex; flex-direction: column; gap: 3px; margin-top: 10px; padding: 8px; border: 1px solid var(--line); background: var(--bg-1); }
  .row { display: flex; align-items: center; gap: 8px; }
  .hv { display: flex; align-items: baseline; gap: 8px; padding: 1px 0 1px 8px; border-left: 2px solid var(--line-2); }
  .hv.cur { border-left-color: var(--accent); }
  .lk { background: none; border: 0; padding: 0; color: var(--fg); font: inherit; text-align: left; cursor: pointer; flex: 1; min-width: 0; }
  .lk:hover { text-decoration: underline; color: var(--accent); }
  .ev { font-family: var(--mono, monospace); }
</style>
