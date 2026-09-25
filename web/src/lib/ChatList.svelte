<script>
  import { S, call, selectChat, newChat, chatHasNew, confirmBox } from './store.svelte.js';
  import Led from './ui/Led.svelte';
  import Icon from './ui/Icon.svelte';
  import Badge from './ui/Badge.svelte';
  import ChatMerge from './ChatMerge.svelte';

  let { onpick } = $props();
  let showArchived = $state(false);
  let selecting = $state(false); // merge mode: rows get checkboxes
  let ticked = $state({});
  let mergeOpen = $state(false);
  const tickedIds = $derived(Object.entries(ticked).filter(([, v]) => v).map(([k]) => Number(k)));
  function toggleSelect() { selecting = !selecting; ticked = {}; }
  let archived = $state([]);
  $effect(() => { if (showArchived) call('chats.list', { archived: true }, { quiet: true }).then((r) => (archived = (r || []).filter((c) => c.archived))); });
  const label = (c) => c.title || (c.topic ? 'New chat' : 'Main');
  const rows = $derived(showArchived ? archived : S.chats);
  async function open(c) { if (selecting) { ticked[c.id] = !ticked[c.id]; return; } await selectChat(c.topic); onpick?.(); }
  async function undo(c, e) { e.stopPropagation(); if (await call('chats.unmerge', { id: c.id })) { showArchived = false; } }
  async function create() { await newChat(''); onpick?.(); }
  async function archiveRow(c, e) { e.stopPropagation(); await call('chats.update', { id: c.id, archived: true }); if (S.chatTopic === c.topic) selectChat(''); }
  async function deleteRow(c, e) {
    e.stopPropagation();
    if (await confirmBox({ title: 'Delete chat', text: `Delete “${c.title || 'New chat'}” with all its messages? Facts already learned from it stay in memory.`, ok: 'Delete', danger: true })) await call('chats.delete', { id: c.id });
  }
  async function restore(c) { await call('chats.update', { id: c.id, archived: false }); showArchived = false; }
</script>

<aside class="chats">
  <div class="head">
    <button type="button" class="new" onclick={create} title="Start a new chat: its own conversation, same memory"><Icon name="plus" size={11} /> New chat</button>
    {#if S.chats.length >= 2 && !showArchived}
      {#if selecting}
        <div class="msel"><button type="button" class="mgo" disabled={tickedIds.length < 2} onclick={() => (mergeOpen = true)}>Merge {tickedIds.length || ''} chats…</button><button type="button" class="mx" onclick={toggleSelect}>cancel</button></div>
      {:else}<button type="button" class="mlink" onclick={toggleSelect} title="Combine several chats into one">merge chats…</button>{/if}
    {/if}
  </div>
  <div class="list scroll">
    {#each rows as c (c.id)}
      <div class="ci" class:on={!showArchived && !selecting && c.topic === S.chatTopic} role="button" tabindex="0" onclick={() => (showArchived ? restore(c) : open(c))} onkeydown={(e) => (e.key === 'Enter' || e.key === ' ') && (showArchived ? restore(c) : open(c))} title={showArchived ? 'Click to restore' : label(c)}>
        <div class="r1">
          {#if selecting}<input type="checkbox" checked={!!ticked[c.id]} onclick={(e) => e.stopPropagation()} onchange={(e) => (ticked[c.id] = e.currentTarget.checked)} aria-label="select {label(c)}" />{/if}
          <Led state={S.chatBusy[c.topic] ? 'ok' : 'off'} live={!!S.chatBusy[c.topic]} size={7} />
          <span class="t ellipsis" class:new={chatHasNew(c)}>{label(c)}</span>
          {#if chatHasNew(c)}<span class="dot" title="new messages"></span>{/if}
          {#if c.topic && !showArchived}<span class="acts"><button type="button" title="Archive" aria-label="archive" onclick={(e) => archiveRow(c, e)}>archive</button><button type="button" title="Delete" aria-label="delete" onclick={(e) => deleteRow(c, e)}>×</button></span>{/if}
        </div>
        <div class="r2 ellipsis">
          {#if c.project}<Badge tone="accent" title="focused on {c.project}">{c.project.replace(/^project:/, '')}</Badge>{/if}
          {#if !c.remember}<span class="nr" title="this chat is not kept for memory">no memory</span>{/if}
          {#if showArchived && c.merged_into}<span class="pv">merged into {c.merged_title || 'another chat'}</span><button type="button" class="undo" onclick={(e) => undo(c, e)}>undo</button>{:else}<span class="pv">{c.last_text || 'empty'}</span>{/if}
        </div>
      </div>
    {:else}<div class="sm mute pad">{showArchived ? 'no archived chats' : 'no chats'}</div>{/each}
  </div>
  <button type="button" class="arch" onclick={() => (showArchived = !showArchived)}>{showArchived ? '‹ back to chats' : 'archived chats'}</button>
</aside>

<ChatMerge bind:open={mergeOpen} ids={tickedIds} />

<style>
  .msel { display: flex; gap: 4px; margin-top: 4px; }
  .mgo { flex: 1; background: var(--accent-dim, var(--bg-3)); border: 1px solid var(--accent); color: var(--fg-hi); padding: 3px 6px; cursor: pointer; }
  .mgo:disabled { opacity: 0.5; cursor: default; }
  .mx, .mlink, .undo { background: none; border: 0; color: var(--fg-mute); font-size: var(--fs-sm); cursor: pointer; }
  .mlink { margin-top: 4px; padding: 0; } .mlink:hover, .mx:hover, .undo:hover { color: var(--fg); }
  .undo { border: 1px solid var(--line-2); padding: 0 4px; font-size: 10px; margin-left: auto; }
  .chats { width: 214px; flex: none; display: flex; flex-direction: column; min-height: 0; border: 1px solid var(--line); background: var(--panel-bg); }
  .head { padding: 6px; border-bottom: 1px solid var(--line); }
  .new { width: 100%; background: var(--bg-2); border: 1px solid var(--line-3); color: var(--fg-hi); padding: 4px 8px; cursor: pointer; display: flex; align-items: center; gap: 6px; justify-content: center; }
  .new:hover { border-color: var(--accent); color: var(--accent-hi); }
  .list { flex: 1; min-height: 0; }
  .ci { padding: 5px 8px; border-bottom: 1px solid var(--line-2); cursor: pointer; display: flex; flex-direction: column; gap: 2px; }
  .ci:hover { background: var(--bg-2); }
  .ci.on { background: var(--bg-3); box-shadow: inset 2px 0 0 var(--accent); }
  .r1 { display: flex; align-items: center; gap: 6px; }
  .t { flex: 1; min-width: 0; color: var(--fg); }
  .t.new { color: var(--fg-hi); font-weight: 700; }
  .acts { display: none; gap: 4px; flex: none; }
  .ci:hover .acts { display: inline-flex; }
  .acts button { background: none; border: 1px solid var(--line-2); color: var(--fg-mute); font-size: 10px; padding: 0 4px; cursor: pointer; }
  .acts button:hover { color: var(--err); border-color: var(--err); }
  .dot { width: 7px; height: 7px; border-radius: 50%; background: var(--accent); flex: none; }
  .r2 { display: flex; align-items: center; gap: 5px; font-size: 10.5px; color: var(--fg-faint); }
  .pv { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
  .nr { font-size: 9.5px; text-transform: uppercase; letter-spacing: 0.06em; color: var(--attn); }
  .arch { background: none; border: 0; border-top: 1px solid var(--line); color: var(--fg-mute); padding: 4px; font-size: var(--fs-sm); cursor: pointer; }
  .arch:hover { color: var(--fg); }
  .pad { padding: 8px; }
</style>
