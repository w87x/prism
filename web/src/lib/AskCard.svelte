<script>
  // A pending question (clarify) or tool confirmation (confirm) from an agent. Questions are a small form (see
  // QuestionForm); confirmations are allow / deny buttons.
  import { answerAsk } from './store.svelte.js';
  import Button from './ui/Button.svelte';
  import QuestionForm from './QuestionForm.svelte';
  let { ask, compact = false } = $props();

  const confirm = $derived(ask.kind === 'confirm');
  const tone = (o) => (o === 'deny' ? 'danger' : o === 'allow for this task' ? 'accent' : 'primary');
  // older asks (and other channels) carry only text + options: treat them as one item
  const items = $derived(
    ask.items?.length
      ? ask.items
      : [{ text: ask.text, kind: ask.options?.length ? 'single' : 'text', options: ask.options || [] }]
  );
</script>

<div class="ask" class:compact>
  <div class="q">
    <span class="tag">{confirm ? 'CONFIRM' : 'QUESTION'}</span>
    <span class="who">{ask.agent}</span>
    {#if confirm || items.length === 1}<span class="txt">{ask.text}</span>{/if}
  </div>
  {#if ask.args}<pre class="args">{ask.tool} {ask.args}</pre>{/if}

  {#if confirm}
    <div class="act">
      {#each ask.options?.length ? ask.options : ['allow', 'deny'] as o}
        <Button size="sm" variant={tone(o)} onclick={() => answerAsk(ask.id, o)}>{o}</Button>
      {/each}
    </div>
  {:else}
    <QuestionForm {items} {compact} onsubmit={(t) => answerAsk(ask.id, t)} />
  {/if}
</div>

<style>
  .ask { display: flex; flex-direction: column; gap: 6px; color: var(--attn-hi); }
  .q { display: flex; gap: 8px; align-items: baseline; flex-wrap: wrap; }
  .tag { color: var(--attn); font-weight: 700; letter-spacing: 0.1em; font-size: var(--fs-sm); text-shadow: var(--glow-attn); }
  .who { color: var(--attn); font-weight: 700; }
  .txt { color: var(--fg-hi); }
  .args { margin: 0; background: var(--attn-bg); border-color: var(--attn-dim); color: var(--attn-hi); max-height: 70px; font-size: var(--fs-sm); }
  .act { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; }
</style>
