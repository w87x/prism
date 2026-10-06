<script>
  // Answer a task that stopped asking the user for input (waiting_input): it carries on with the answer.
  import { call, toast } from './store.svelte.js';
  import Modal from './ui/Modal.svelte';
  import Button from './ui/Button.svelte';
  import Textarea from './ui/Textarea.svelte';

  let { taskId = 0, onclose, ondone } = $props();
  let task = $state(null);
  let answer = $state('');
  let busy = $state(false);

  $effect(() => {
    if (!taskId) { task = null; return; }
    answer = '';
    call('tasks.get', { id: taskId }, { quiet: true }).then((r) => { task = r?.task || null; });
  });
  async function send() {
    if (!answer.trim() || busy) return;
    busy = true;
    const r = await call('tasks.answer', { id: taskId, answer: answer.trim() });
    busy = false;
    if (r) { toast('Answer sent — the task is running again'); onclose?.(); ondone?.(); }
  }
</script>

<Modal open={!!taskId} onclose={() => onclose?.()} title="Task #{taskId} — needs your answer" width={560}>
  {#if task}
    <div class="sm dim">{task.to_agent} asks:</div>
    <div class="q">{task.question || '(no question recorded)'}</div>
    <Textarea bind:value={answer} rows={3} placeholder="your answer…" mono={false} />
    <div class="act"><Button size="sm" variant="primary" disabled={!answer.trim()} loading={busy} onclick={send}>Send answer</Button></div>
  {/if}
</Modal>

<style>
  .q { margin: 6px 0 10px; padding: 8px 10px; border: 1px solid var(--attn-dim); background: var(--attn-bg); color: var(--fg-hi); white-space: pre-wrap; }
  .act { display: flex; justify-content: flex-end; margin-top: 8px; }
</style>
