<script>
  // The controls of one open question or hypothesis from memory analysis, wherever it is shown (the review list, the fact
  // editor, the chat's question list): a question takes an answer (or is skipped), a hypothesis is confirmed as true or
  // rejected as false. Resolving retires the insight so analysis does not raise it again unchanged.
  import { call, toast } from './store.svelte.js';
  import Button from './ui/Button.svelte';
  let { f, ondone = () => {}, compact = false } = $props();
  let answer = $state('');
  let busy = $state('');
  const isQuestion = $derived(f.tags?.includes('question'));
  async function resolve(verdict) {
    busy = verdict;
    const ok = await call('memory.resolve_insight', { id: f.id, verdict, answer: answer.trim() });
    busy = '';
    if (ok) { answer = ''; toast(verdict === 'answer' ? 'Answer remembered' : verdict === 'confirm' ? 'Stored as a trusted fact' : 'Dropped'); ondone(verdict); }
  }
</script>

<div class="oi" class:compact>
  {#if isQuestion}
    <input class="ans" placeholder="your answer…" bind:value={answer} onkeydown={(e) => e.key === 'Enter' && answer.trim() && resolve('answer')} />
    <div class="row end">
      <Button size="sm" variant="primary" loading={busy === 'answer'} disabled={!answer.trim()} onclick={() => resolve('answer')}>Answer</Button>
      <Button size="sm" variant="ghost" loading={busy === 'reject'} title="Not a question worth answering — drop it" onclick={() => resolve('reject')}>Skip</Button>
    </div>
  {:else}
    <div class="row end">
      <Button size="sm" variant="primary" loading={busy === 'confirm'} title="Store it as a trusted fact" onclick={() => resolve('confirm')}>True</Button>
      <Button size="sm" variant="ghost" loading={busy === 'reject'} title="Drop it" onclick={() => resolve('reject')}>False</Button>
    </div>
  {/if}
</div>

<style>
  .oi { display: flex; flex-direction: column; gap: 6px; }
  .ans { width: 100%; box-sizing: border-box; background: var(--bg-1); color: var(--fg-hi); border: 1px solid var(--line-2); padding: 5px 8px; font: inherit; }
  .row { display: flex; gap: 6px; justify-content: flex-end; }
</style>
