<script>
  // Review of a task that stopped early (iteration budget / loop guard / failure): what happened, why, and
  // a handful of concrete ways forward — continue, split, ask Metis to improve the agent, hire a better
  // one, or just dismiss. Shared by Today ("needs your attention") and the task detail.
  import { call, toast } from './store.svelte.js';
  import Modal from './ui/Modal.svelte';
  import Button from './ui/Button.svelte';
  import Badge from './ui/Badge.svelte';
  import Textarea from './ui/Textarea.svelte';

  let { taskId = 0, onclose, ondone } = $props();
  let review = $state(null);
  let loading = $state(false);
  let note = $state('');
  let busy = $state('');
  const open = $derived(!!taskId);

  $effect(() => {
    if (!taskId) { review = null; return; }
    loading = true; note = '';
    call('tasks.review', { id: taskId }, { quiet: true }).then((r) => { review = r; loading = false; });
  });

  const tone = { continue: 'accent', split: 'ok', evolve: 'attn', hire: 'attn', dismiss: 'mute' };
  async function choose(o) {
    busy = o.action;
    const id = taskId;
    const r = o.action === 'dismiss' ? await call('tasks.ack', { id }) : await call('tasks.resolve', { id, action: o.action, note });
    busy = '';
    if (r) { toast(o.action === 'dismiss' ? 'Dismissed' : `Started task #${r.id}`); ondone?.(); onclose?.(); }
  }
</script>

<Modal {open} onclose={() => onclose?.()} title="Task #{taskId} — what happened" width={640}>
  {#if loading}<div class="sm mute">reading the transcript…</div>
  {:else if review}
    <div class="row sm"><Badge tone={review.cause === 'loop' ? 'warn' : review.cause === 'budget' ? 'attn' : 'err'}>{review.cause}</Badge></div>
    <p class="sum">{review.summary}</p>
    <div class="opts">
      {#each review.options as o (o.action)}
        <button type="button" class="opt" disabled={!!busy} onclick={() => choose(o)}>
          <span class="hd"><Badge tone={tone[o.action] || 'mute'}>{o.action}</Badge> {o.label}</span>
          {#if o.detail}<span class="sm mute">{o.detail}</span>{/if}
        </button>
      {/each}
    </div>
    <Textarea bind:value={note} rows={2} placeholder="optional: your own guidance for whoever picks this up…" mono={false} />
  {/if}
  {#snippet footer()}<span class="grow"></span><Button variant="ghost" onclick={() => onclose?.()}>Close</Button>{/snippet}
</Modal>

<style>
  .sum { margin: 4px 0; line-height: 1.5; color: var(--fg); }
  .opts { display: flex; flex-direction: column; gap: 6px; }
  .opt { display: flex; flex-direction: column; gap: 2px; text-align: left; padding: 7px 9px; background: var(--bg-2); border: 1px solid var(--line-2); border-radius: var(--r); color: var(--fg-hi); }
  .opt:hover:not(:disabled) { border-color: var(--fg-dim); background: var(--bg-3); }
  .hd { display: flex; align-items: center; gap: 8px; }
</style>
