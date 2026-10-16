<script>
  import { S, call, selectChat, newChat, chatHasNew, confirmBox, ago } from './store.svelte.js';
  import Icon from './ui/Icon.svelte';
  import ChatMerge from './ChatMerge.svelte';

  let { onpick } = $props();
  let showArchived = $state(false);
  let selecting = $state(false); // merge mode: rows get checkboxes
  let ticked = $state({});
  let mergeOpen = $state(false);
  const tickedIds = $derived(Object.entries(ticked).filter(([, v]) => v).map(([k]) => Number(k)));
  function toggleSelect() { selecting = !selecting; ticked = {}; }
  // the hover card: where the row is, so the card sits beside it (fixed, so the scrolling list does not clip it)
  let tip = $state(null);
  function showTip(c, e) { const r = e.currentTarget.getBoundingClientRect(); tip = { c, x: Math.min(r.right + 6, window.innerWidth - 270), y: Math.max(6, Math.min(r.top, window.innerHeight - 150)) }; }
  // touch has no hover: a long press shows the card (and swallows the tap that ends it); the row actions are always visible (see the css)
  let pressTimer, longPressed = false;
  function pressStart(c, e) { if (e.pointerType !== 'touch') return; longPressed = false; const el = e.currentTarget; pressTimer = setTimeout(() => { longPressed = true; showTip(c, { currentTarget: el }); }, 450); }
  function pressEnd() { clearTimeout(pressTimer); }
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
  <div class="list scroll" onscroll={() => (tip = null)}>
    {#each rows as c (c.id)}
      <div class="ci" class:on={!showArchived && !selecting && c.topic === S.chatTopic} class:busy={!!S.chatBusy[c.topic]} class:hasnew={chatHasNew(c)} role="button" tabindex="0"
        onclick={() => { if (longPressed) { longPressed = false; return; } tip = null; showArchived ? restore(c) : open(c); }}
        onpointerdown={(e) => pressStart(c, e)} onpointerup={pressEnd} onpointercancel={pressEnd} onpointermove={pressEnd} oncontextmenu={(e) => { if (e.pointerType === 'touch' || longPressed) e.preventDefault(); }} onkeydown={(e) => (e.key === 'Enter' || e.key === ' ') && (showArchived ? restore(c) : open(c))}
        onmouseenter={(e) => showTip(c, e)} onmouseleave={() => (tip = null)} onfocus={(e) => showTip(c, e)} onblur={() => (tip = null)}>
        {#if selecting}<input type="checkbox" checked={!!ticked[c.id]} onclick={(e) => e.stopPropagation()} onchange={(e) => (ticked[c.id] = e.currentTarget.checked)} aria-label="select {label(c)}" />{/if}
        <i class="led"></i>
        <span class="t ellipsis" class:new={chatHasNew(c)}>{label(c)}</span>
        {#if showArchived && c.merged_into}<button type="button" class="undo" onclick={(e) => undo(c, e)}>undo</button>
        {:else if c.topic && !showArchived}<span class="acts"><button type="button" class="ibtn sm danger" title="Archive" aria-label="archive" onclick={(e) => archiveRow(c, e)}>▭</button><button type="button" class="ibtn sm danger" title="Delete" aria-label="delete" onclick={(e) => deleteRow(c, e)}>×</button></span>{/if}
      </div>
    {:else}<div class="sm mute pad">{showArchived ? 'no archived chats' : 'no chats'}</div>{/each}
  </div>
  {#if tip}
    <div class="tip" style="left:{tip.x}px; top:{tip.y}px">
      <div class="tt">{label(tip.c)}</div>
      <div class="tm">{#if tip.c.project}<span class="chip">{tip.c.project.replace(/^project:/, '')}</span>{/if}{#if !tip.c.remember}<span class="chip warn">no memory</span>{/if}{#if S.chatBusy[tip.c.topic]}<span class="chip ok">working</span>{:else if chatHasNew(tip.c)}<span class="chip">new messages</span>{/if}<span>{ago(tip.c.last_at)} ago</span></div>
      <div class="tb">{showArchived && tip.c.merged_into ? 'merged into ' + (tip.c.merged_title || 'another chat') : tip.c.last_text || 'empty'}</div>
    </div>
  {/if}
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
  .chats { width: 190px; flex: none; display: flex; flex-direction: column; min-height: 0; border: 1px solid var(--line); background: var(--panel-bg); }
  .head { padding: 6px; border-bottom: 1px solid var(--line); }
  .new { width: 100%; background: var(--bg-2); border: 1px solid var(--line-3); color: var(--fg-hi); padding: 4px 8px; cursor: pointer; display: flex; align-items: center; gap: 6px; justify-content: center; }
  .new:hover { border-color: var(--accent); color: var(--accent-hi); }
  .list { flex: 1; min-height: 0; }
  .ci { padding: 0 6px 0 0; height: 24px; border-bottom: 1px solid var(--line-2); cursor: pointer; display: flex; align-items: center; gap: 7px; }
  .ci:hover { background: var(--bg-2); }
  .ci.on { background: var(--bg-3); }
  /* the status is one small rectangular LED on the left edge: dark at rest, lit on hover or when this is the open chat, pulsing while the chat works, amber when it has news */
  .led { flex: none; width: 4px; align-self: stretch; margin: 4px 0 4px 0; background: var(--line-3); transition: background 0.12s, box-shadow 0.12s; }
  .ci:hover .led, .ci.on .led { background: var(--accent); box-shadow: 0 0 6px var(--accent); }
  .ci.hasnew .led { background: var(--attn); box-shadow: 0 0 5px var(--attn); }
  .ci.busy .led { background: var(--ok, var(--accent)); animation: pls 1.1s ease-in-out infinite; }
  @keyframes pls { 50% { opacity: 0.35; } }
  .t { flex: 1; min-width: 0; color: var(--fg-dim); font-size: var(--fs-sm); }
  .ci:hover .t, .ci.on .t { color: var(--fg); }
  .t.new { color: var(--fg-hi); font-weight: 700; }
  .acts { display: none; gap: 3px; flex: none; }
  .ci:hover .acts { display: inline-flex; }
  @media (hover: none) { .acts { display: inline-flex; } .ci { height: 34px; } }
  .ci input[type='checkbox'] { margin: 0 0 0 2px; }
  .tip { position: fixed; z-index: 60; width: 256px; pointer-events: none; background: var(--bg-1); border: 1px solid var(--line-3); box-shadow: 0 6px 20px rgba(0, 0, 0, 0.45); padding: 7px 9px; display: flex; flex-direction: column; gap: 4px; }
  .tt { color: var(--fg-hi); font-weight: 700; overflow-wrap: anywhere; }
  .tm { display: flex; flex-wrap: wrap; align-items: center; gap: 5px; font-size: 10.5px; color: var(--fg-mute); }
  .chip { border: 1px solid var(--line-3); padding: 0 4px; text-transform: uppercase; letter-spacing: 0.05em; font-size: 9.5px; color: var(--accent-hi); }
  .chip.warn { color: var(--attn); } .chip.ok { color: var(--ok, var(--accent)); }
  .tb { color: var(--fg-dim); font-size: var(--fs-sm); line-height: 1.4; display: -webkit-box; -webkit-line-clamp: 5; -webkit-box-orient: vertical; overflow: hidden; overflow-wrap: anywhere; }
  .arch { background: none; border: 0; border-top: 1px solid var(--line); color: var(--fg-mute); padding: 4px; font-size: var(--fs-sm); cursor: pointer; }
  .arch:hover { color: var(--fg); }
  .pad { padding: 8px; }
</style>
