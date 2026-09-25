<script>
  import { S, call, toast, confirmBox } from './store.svelte.js';
  import Badge from './ui/Badge.svelte';
  import Button from './ui/Button.svelte';
  import Select from './ui/Select.svelte';
  import Icon from './ui/Icon.svelte';
  import Modal from './ui/Modal.svelte';
  import Input from './ui/Input.svelte';

  let { onlist } = $props(); // opens the chat list on narrow screens
  const chat = $derived(S.chats.find((c) => c.topic === S.chatTopic));
  let banks = $state([]);
  $effect(() => { S.chats; call('memory.banks', {}, { quiet: true }).then((b) => (banks = (b || []).filter((x) => x.kind === 'project' && x.status === 'active'))); });
  const options = $derived([{ value: 0, label: 'no project' }, ...banks.map((b) => ({ value: b.id, label: b.name })), { value: -1, label: '+ New project…' }]);
  let npOpen = $state(false);
  let npName = $state('');
  async function createProject() { const n = npName.trim(); if (!n) return; npOpen = false; if (await call('chats.update', { id: chat.id, new_project: n })) toast(`Project “${n}” created; this chat is focused on it`); }
  let editing = $state(false);
  let draft = $state('');
  function edit() { if (!chat) return; draft = chat.title || ''; editing = true; }
  async function saveTitle() { editing = false; if (chat && draft.trim() && draft.trim() !== chat.title) await call('chats.update', { id: chat.id, title: draft.trim() }); }
  const bind = (id) => { if (Number(id) === -1) { npName = ''; npOpen = true; return; } return call('chats.update', { id: chat.id, project_bank_id: Number(id) || 0 }); };
  const answer = (accept) => call('chats.update', { id: chat.id, accept_suggestion: accept, dismiss_suggestion: !accept });
  const remember = () => call('chats.update', { id: chat.id, remember: !chat.remember }).then(() => toast(chat.remember ? 'This chat is no longer kept for memory' : 'This chat is remembered again'));
  const archive = () => call('chats.update', { id: chat.id, archived: true }).then(() => { S.chatTopic = ''; });
  async function remove() {
    if (await confirmBox({ title: 'Delete chat', text: `Delete “${chat.title || 'New chat'}” with all its messages? Facts already learned from it stay in memory.`, ok: 'Delete', danger: true })) await call('chats.delete', { id: chat.id });
  }
</script>

{#if chat}
  <div class="hd">
    <button type="button" class="menu" onclick={onlist} title="Chats" aria-label="Chats"><Icon name="menu" size={13} /></button>
    {#if editing}
      <input class="title" bind:value={draft} onblur={saveTitle} onkeydown={(e) => { if (e.key === 'Enter') saveTitle(); if (e.key === 'Escape') editing = false; }} />
    {:else}
      <button type="button" class="ttl" onclick={edit} disabled={!chat.topic} title={chat.topic ? 'Rename' : 'The main chat'}>{chat.title || (chat.topic ? 'New chat' : 'Main')}</button>
    {/if}
    <span class="grow"></span>
    <span class="proj" title="This chat's project: its bank is searched first and receives facts about the work. Everything else in memory stays reachable.">
      <span class="sm mute">project</span>
      <Select size="sm" value={chat.project_bank_id || 0} options={options} onchange={bind} />
    </span>
    <span class:nrbtn={!chat.remember}><Button size="sm" variant="ghost" title={chat.remember ? 'Messages of this chat are kept for memory — click to stop' : 'This chat is not kept for memory — click to remember it'} onclick={remember}>{chat.remember ? 'remembered' : 'not remembered'}</Button></span>
    {#if chat.topic}
      <Button size="sm" variant="ghost" onclick={archive} title="Hide this chat from the list (restore it from “archived chats” at the bottom of the list)">Archive</Button>
      <Button size="sm" variant="ghost" onclick={remove} title="Delete this chat and its messages"><Icon name="trash" size={12} /> Delete</Button>
    {/if}
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
  .ttl { background: none; border: 0; color: var(--fg-hi); font: inherit; font-weight: 700; cursor: text; padding: 0; text-align: left; }
  .ttl:disabled { cursor: default; }
  .ttl:not(:disabled):hover { text-decoration: underline dotted; }
  .title { background: var(--bg); border: 1px solid var(--accent); color: var(--fg-hi); font: inherit; padding: 1px 4px; min-width: 220px; }
  .proj { display: flex; align-items: center; gap: 6px; }
  .nrbtn :global(button) { color: var(--attn); border-color: var(--attn-dim); }
  .menu { display: none; background: none; border: 0; color: var(--fg-dim); padding: 0 4px; cursor: pointer; }
  .sg { display: flex; align-items: center; gap: 8px; padding: 4px 8px; border: 1px solid var(--attn-dim); background: var(--bg-1); flex: none; }
  @media (max-width: 820px) { .menu { display: inline-flex; } .proj .mute { display: none; } }
</style>
