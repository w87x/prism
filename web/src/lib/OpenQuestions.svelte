<script>
  // Open questions and hypotheses from memory analysis, reachable from the chat without blocking it: a small button with the
  // counts that opens the list, each with its answer controls. Nothing interrupts; the list is simply always one click away.
  import { call, listen, go, S } from './store.svelte.js';
  import Button from './ui/Button.svelte';
  import Modal from './ui/Modal.svelte';
  import Badge from './ui/Badge.svelte';
  import OpenInsight from './OpenInsight.svelte';

  let items = $state([]);
  let open = $state(false);
  async function load() { items = (await call('memory.open', {}, { quiet: true })) || []; }
  $effect(() => { load(); return listen('memory.update', load); });
  const nq = $derived(items.filter((f) => f.tags?.includes('question')).length);
  const nh = $derived(items.length - nq);
  function inMemory(f) { open = false; S.selectedFact = f.id; go('memory'); }
</script>

{#if items.length}
  <Button size="sm" variant={nq ? 'accent' : 'ghost'} title="Questions memory would like you to answer, and guesses it wants you to confirm — answer whenever you like, nothing waits on it" onclick={() => (open = true)}>
    ? {nq}{#if nh} · ✦ {nh}{/if}
  </Button>
{/if}

<Modal bind:open title="Memory asks" width={680}>
  <div class="sm mute">Questions come from deep analysis of what memory knows. Answer the ones you can — the answer becomes a trusted fact. Skip the rest; nothing is waiting on you.</div>
  {#each items as f (f.id)}
    {@const q = f.tags?.includes('question')}
    <div class="qrow">
      <div><Badge tone={q ? 'accent' : 'attn'}>{q ? 'question' : 'hypothesis'}</Badge> <span class="mute sm">{f.bank}</span></div>
      <div class="qtx">{f.text}</div>
      <OpenInsight {f} ondone={load} />
      <button type="button" class="lk" onclick={() => inMemory(f)}>see the facts behind it</button>
    </div>
  {:else}<div class="sm mute">nothing open right now</div>{/each}
  {#snippet footer()}<Button variant="primary" onclick={() => (open = false)}>Close</Button>{/snippet}
</Modal>

<style>
  .qrow { display: flex; flex-direction: column; gap: 6px; padding: 8px 0; border-top: 1px solid var(--line-2); }
  .qtx { color: var(--fg-hi); }
  .lk { align-self: flex-start; background: none; border: 0; padding: 0; color: var(--fg-mute); font-size: var(--fs-sm); text-decoration: underline; cursor: pointer; }
</style>
