<script>
  // A pending question (clarify) or tool confirmation (confirm) from an agent.
  import { answerAsk } from './store.svelte.js';
  import Button from './ui/Button.svelte';
  import Input from './ui/Input.svelte';
  let { ask, compact = false } = $props();
  let text = $state('');
  const opts = $derived(ask.options?.length ? ask.options : ask.kind === 'confirm' ? ['allow', 'deny'] : []);
  const tone = (o) => (o === 'deny' ? 'danger' : o === 'allow for this task' ? 'accent' : 'primary');
</script>

<div class="ask" class:compact>
  <div class="q">
    <span class="tag">{ask.kind === 'confirm' ? 'CONFIRM' : 'QUESTION'}</span>
    <span class="who">{ask.agent}</span>
    <span class="txt">{ask.text}</span>
  </div>
  {#if ask.args}<pre class="args">{ask.tool} {ask.args}</pre>{/if}
  <div class="act">
    {#each opts as o}
      <Button size="sm" variant={ask.kind === 'confirm' ? tone(o) : 'primary'} onclick={() => answerAsk(ask.id, o)}>{o}</Button>
    {/each}
    {#if ask.kind !== 'confirm'}
      <div class="grow"><Input bind:value={text} size="sm" placeholder="type your answer…" onenter={() => text.trim() && answerAsk(ask.id, text.trim())} /></div>
      <Button size="sm" variant="primary" disabled={!text.trim()} onclick={() => answerAsk(ask.id, text.trim())}>Answer</Button>
    {/if}
  </div>
</div>

<style>
  .ask { display: flex; flex-direction: column; gap: 5px; color: var(--attn-hi); }
  .q { display: flex; gap: 8px; align-items: baseline; flex-wrap: wrap; }
  .tag { color: var(--attn); font-weight: 700; letter-spacing: 0.1em; font-size: var(--fs-sm); text-shadow: var(--glow-attn); }
  .who { color: var(--attn); font-weight: 700; }
  .txt { color: var(--fg-hi); }
  .args { margin: 0; background: var(--attn-bg); border-color: var(--attn-dim); color: var(--attn-hi); max-height: 70px; font-size: var(--fs-sm); }
  .act { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; }
</style>
