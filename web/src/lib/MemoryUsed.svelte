<script>
  import { call, toast, go, S } from './store.svelte.js';
  import Badge from './ui/Badge.svelte';
  import Button from './ui/Button.svelte';

  let { taskId } = $props();
  let facts = $state([]);
  let open = $state(false);
  let loaded = $state(0);

  async function load() { facts = (await call('memory.used', { task_id: taskId }, { quiet: true })) || []; loaded = taskId; }
  $effect(() => { if (taskId && taskId !== loaded) load(); });

  async function vote(f, useful) {
    await call('memory.feedback', { id: f.id, useful });
    toast(useful ? 'Ranked up' : 'Ranked down');
  }
  async function wrong(f) {
    if (await call('memory.fact_outdate', { id: f.id })) { facts = facts.filter((x) => x.id !== f.id); toast('Retired — it will not be used again'); }
  }
  const open_ = (f) => { S.selectedFact = f.id; go('memory'); };
</script>

{#if facts.length}
  <div class="mu">
    <button type="button" class="head" onclick={() => (open = !open)}>{open ? '▾' : '▸'} memory used: {facts.length} fact{facts.length === 1 ? '' : 's'}</button>
    {#if open}
      {#each facts as f (f.id)}
        <div class="row">
          <button type="button" class="tx" onclick={() => open_(f)} title="open in Memory">{f.text}</button>
          {#if f.kind === 'conclusion'}<Badge tone="accent">conclusion</Badge>{:else if f.confidence < 0.5}<Badge tone="attn">unverified</Badge>{/if}
          {#if f.valid_to}<Badge tone="mute">outdated</Badge>{/if}
          <span class="sm mute nowrap">{f.bank}</span>
          <Button size="sm" variant="ghost" title="This helped: rank it up" onclick={() => vote(f, true)}>useful</Button>
          <Button size="sm" variant="ghost" title="Not relevant: rank it down" onclick={() => vote(f, false)}>not relevant</Button>
          {#if !f.valid_to}<Button size="sm" variant="ghost" title="This is wrong or outdated: retire it" onclick={() => wrong(f)}>wrong</Button>{/if}
        </div>
      {/each}
    {/if}
  </div>
{/if}

<style>
  .mu { margin: 6px 0; border: 1px solid var(--line); padding: 4px 8px; background: var(--bg-1); }
  .head { background: none; border: 0; color: var(--fg-dim); font: inherit; font-size: 11px; cursor: pointer; padding: 0; }
  .head:hover { color: var(--fg); }
  .row { display: flex; align-items: center; gap: 8px; padding: 3px 0; border-top: 1px solid var(--line-2); }
  .tx { flex: 1; min-width: 0; text-align: left; background: none; border: 0; color: var(--fg); font: inherit; cursor: pointer; }
  .tx:hover { text-decoration: underline; }
</style>
