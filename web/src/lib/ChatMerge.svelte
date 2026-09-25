<script>
  import { S, call, toast, selectChat } from './store.svelte.js';
  import Modal from './ui/Modal.svelte';
  import Button from './ui/Button.svelte';
  import Select from './ui/Select.svelte';
  import Field from './ui/Field.svelte';

  let { open = $bindable(false), ids = [] } = $props(); // ids: the chats ticked in the list
  const picked = $derived(S.chats.filter((c) => ids.includes(c.id)));
  let target = $state(0);
  let project = $state(-1); // -1: undecided
  let busy = $state(false);
  $effect(() => {
    if (!open) return;
    const main = picked.find((c) => !c.topic);
    target = (main || [...picked].sort((a, b) => new Date(b.last_at) - new Date(a.last_at))[0])?.id || 0;
    project = -1;
  });
  const tgt = $derived(picked.find((c) => c.id === target));
  const sources = $derived(picked.filter((c) => c.id !== target));
  const label = (c) => c.title || (c.topic ? 'New chat' : 'Main');
  // projects among the sources; a choice is needed only when the target has none and the sources disagree
  const srcProjects = $derived([...new Map(sources.filter((c) => c.project_bank_id).map((c) => [c.project_bank_id, c.project])).entries()]);
  const needsChoice = $derived(tgt && !tgt.project_bank_id && srcProjects.length > 1);
  const effective = $derived(tgt?.project || (srcProjects.length === 1 ? srcProjects[0][1] : ''));
  async function go() {
    busy = true;
    const r = await call('chats.merge', { sources: sources.map((c) => c.id), target, project_bank_id: needsChoice ? Number(project) : null });
    busy = false;
    if (r) {
      toast(`${r.moved} message${r.moved === 1 ? '' : 's'} moved into “${label(r.target)}”. Undo from “archived chats”.`);
      open = false;
      await selectChat(r.target.topic);
    }
  }
</script>

<Modal bind:open title="Merge chats" width={560}>
  <div class="sm mute">Everything said in the other chats moves into the target, in time order, marked with where it came from. The assistant continues from a short summary of each merged chat. The merged chats are archived, and can be restored for a month.</div>
  <Field label="Merge into">
    <Select bind:value={target} options={picked.map((c) => ({ value: c.id, label: label(c) + (c.project ? '  ·  ' + c.project.replace(/^project:/, '') : '') }))} />
  </Field>
  {#if needsChoice}
    <Field label="These chats are focused on different projects — which one should the merged chat keep?">
      <Select bind:value={project} options={[{ value: -1, label: 'choose…' }, { value: 0, label: 'no project' }, ...srcProjects.map(([id, name]) => ({ value: id, label: name.replace(/^project:/, '') }))]} />
    </Field>
  {/if}
  <div class="hint">
    {#if sources.length}
      <b>{sources.map(label).join(', ')}</b> will be merged into <b>{tgt ? label(tgt) : '…'}</b>{effective ? ', focused on ' + effective.replace(/^project:/, '') : ''}.
      {#if picked.some((c) => S.chatBusy[c.topic])}<div class="warn">A chat that is working cannot be merged yet — wait for it to finish or stop it.</div>{/if}
    {:else}<span class="mute">tick at least two chats</span>{/if}
  </div>
  {#snippet footer()}
    <Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
    <Button variant="primary" loading={busy} disabled={!sources.length || (needsChoice && project === -1) || picked.some((c) => S.chatBusy[c.topic])} onclick={go}>Merge {sources.length + 1} chats</Button>
  {/snippet}
</Modal>

<style>
  .hint { margin-top: 8px; padding: 6px 8px; border: 1px solid var(--line); background: var(--bg-1); font-size: 12px; }
  .warn { color: var(--attn); margin-top: 4px; }
</style>
