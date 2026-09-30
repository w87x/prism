<script>
  // Global keyboard shortcuts that aren't already owned by a specific component (Cmd+K lives in
  // CommandPalette; this one holds the rest, and doubles as the cheatsheet — see shortcuts.js).
  import { S, go } from './store.svelte.js';
  import Modal from './ui/Modal.svelte';
  import { SHORTCUTS } from './shortcuts.js';

  function onKey(e) {
    const mod = e.metaKey || e.ctrlKey;
    if (!mod) return;
    const k = e.key.toLowerCase();
    if (k === 'j') { e.preventDefault(); S.quickChatOpen = !S.quickChatOpen; }
    else if (e.key === '/') { e.preventDefault(); S.shortcutsOpen = !S.shortcutsOpen; }
    else if (k === 'i') { e.preventDefault(); S.wallOpen = !S.wallOpen; } // thinking wall
    else if (k === 'b') { e.preventDefault(); S.quickChatOpen = false; go('today'); } // today's briefings
  }
  $effect(() => {
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  });
</script>

{#if S.shortcutsOpen}
  <Modal open={S.shortcutsOpen} title="Keyboard shortcuts" width={420} onclose={() => (S.shortcutsOpen = false)}>
    <div class="list">
      {#each SHORTCUTS as s}
        <div class="row">
          <kbd>{s.keys}</kbd>
          <div class="lbl"><span>{s.label}</span>{#if s.hint}<span class="hint">{s.hint}</span>{/if}</div>
        </div>
      {/each}
    </div>
  </Modal>
{/if}

<style>
  .list { display: flex; flex-direction: column; gap: 8px; }
  .row { display: flex; align-items: center; gap: 12px; }
  kbd { flex: none; min-width: 42px; text-align: center; background: var(--bg-2); border: 1px solid var(--line-2); border-radius: 4px; padding: 3px 7px; font-size: 12px; font-weight: 700; color: var(--fg-hi); }
  .lbl { display: flex; flex-direction: column; gap: 1px; }
  .lbl span:first-child { color: var(--fg); font-size: var(--fs-sm); }
  .hint { color: var(--fg-mute); font-size: 11px; }
</style>
