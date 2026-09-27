<script>
  // Single-chat mode: there is one chat (the main thread). This bar only ever shows its own settings —
  // project focus and whether it feeds memory — never a switcher, rename or delete (nothing else to switch to).
  import { S, call, toast } from './store.svelte.js';
  import Badge from './ui/Badge.svelte';
  import Button from './ui/Button.svelte';
  import Select from './ui/Select.svelte';
  import Modal from './ui/Modal.svelte';
  import Input from './ui/Input.svelte';

  const chat = $derived(S.chats.find((c) => c.topic === S.chatTopic));
  let banks = $state([]);
  $effect(() => { S.chats; call('memory.banks', {}, { quiet: true }).then((b) => (banks = (b || []).filter((x) => x.kind === 'project' && x.status === 'active'))); });
  const options = $derived([{ value: 0, label: 'no project' }, ...banks.map((b) => ({ value: b.id, label: b.name })), { value: -1, label: '+ New project…' }]);
  let npOpen = $state(false);
  let npName = $state('');
  async function createProject() { const n = npName.trim(); if (!n) return; npOpen = false; if (await call('chats.update', { id: chat.id, new_project: n })) toast(`Project “${n}” created; this chat is focused on it`); }
  const bind = (id) => { if (Number(id) === -1) { npName = ''; npOpen = true; return; } return call('chats.update', { id: chat.id, project_bank_id: Number(id) || 0 }); };
  const answer = (accept) => call('chats.update', { id: chat.id, accept_suggestion: accept, dismiss_suggestion: !accept });
  const remember = () => call('chats.update', { id: chat.id, remember: !chat.remember }).then(() => toast(chat.remember ? 'This chat is no longer kept for memory' : 'This chat is remembered again'));
</script>

{#if chat}
  <div class="hd">
    <span class="ttl">Main</span>
    <span class="grow"></span>
    <span class="proj" title="This chat's project: its bank is searched first and receives facts about the work. Everything else in memory stays reachable.">
      <span class="sm mute">project</span>
      <Select size="sm" value={chat.project_bank_id || 0} options={options} onchange={bind} />
    </span>
    <span class:nrbtn={!chat.remember}><Button size="sm" variant="ghost" title={chat.remember ? 'Messages of this chat are kept for memory — click to stop' : 'This chat is not kept for memory — click to remember it'} onclick={remember}>{chat.remember ? 'remembered' : 'not remembered'}</Button></span>
  </div>
  {#if chat.suggested_project}
    <div class="sg">
      <Badge tone="attn">project?</Badge>
      <span class="sm">This looks like {chat.suggested_new ? 'a new project' : 'the project'} <b>{chat.suggested_project.replace(/^project:/, '')}</b>. Focus this chat on it? Its bank would be searched first and receive facts about the work.</span>
      <span class="grow"></span>
      <Button size="sm" variant="accent" onclick={() => answer(true)}>Yes</Button>
      <Button size="sm" variant="ghost" onclick={() => answer(false)}>No</Button>
    </div>
  {/if}
{/if}

<Modal bind:open={npOpen} title="New project" width={460}>
  <div class="sm mute">A project is a memory bank for one piece of ongoing work. This chat will search it first and file facts about the work there; the rest of memory stays reachable.</div>
  <Input bind:value={npName} placeholder="e.g. Greenhouse build" onenter={createProject} />
  {#snippet footer()}<Button variant="ghost" onclick={() => (npOpen = false)}>Cancel</Button><Button variant="primary" disabled={!npName.trim()} onclick={createProject}>Create and focus this chat</Button>{/snippet}
</Modal>

<style>
  .hd { display: flex; align-items: center; gap: 8px; padding: 3px 6px; border: 1px solid var(--line); background: var(--bg-1); flex: none; }
  .ttl { color: var(--fg-hi); font-weight: 700; }
  .proj { display: flex; align-items: center; gap: 6px; }
  .nrbtn :global(button) { color: var(--attn); border-color: var(--attn-dim); }
  .sg { display: flex; align-items: center; gap: 8px; padding: 4px 8px; border: 1px solid var(--attn-dim); background: var(--bg-1); flex: none; }
  @media (max-width: 820px) { .proj .mute { display: none; } }
</style>
