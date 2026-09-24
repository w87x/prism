<script>
  // Fallback content: setup hint in the main area, or a quiet system panel in the widget column.
  import { S } from '../lib/store.svelte.js';
  import Panel from '../lib/ui/Panel.svelte';
  import Button from '../lib/ui/Button.svelte';
  import { reopenOnboarding } from '../lib/store.svelte.js';
  let { widget = false } = $props();
  const st = $derived(S.status);
</script>

{#if widget}
  <Panel title="System">
    <div class="kv" title="tasks waiting for a worker"><span>queued</span><b>{st?.queue ?? 0}</b></div>
    <div class="kv" title="tasks an agent is working on right now"><span>running</span><b>{st?.running ?? 0}</b></div>
    <div class="kv" title="tasks paused until you answer a question"><span>waiting for you</span><b class={st?.waiting ? 'attn' : ''}>{st?.waiting ?? 0}</b></div>
    <div class="kv"><span>agents thinking</span><b>{st?.thinking ?? 0}</b></div>
    <div class="kv" title="vector index for memory search"><span>vector search</span><b>{st?.pgvector ? 'pgvector' : 'fallback'}</b></div>
    <div class="kv"><span>raw to digest</span><b>{st?.raw_pending ?? 0}</b></div>
  </Panel>
{:else}
  <Panel title="Setup required">
    <p>PRISM needs a PostgreSQL database before anything else works.</p>
    <div><Button variant="primary" onclick={reopenOnboarding}>Open setup</Button></div>
  </Panel>
{/if}

<style>
  .kv { display: flex; justify-content: space-between; color: var(--fg-dim); font-size: var(--fs-sm); }
  .kv b { color: var(--fg-hi); }
</style>
