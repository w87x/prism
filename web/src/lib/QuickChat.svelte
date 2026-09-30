<script>
  // Send a message into the main conversation from anywhere in the app (Cmd+J): you're looking at a
  // page — a briefing missing a column, a tracker with a bad value — and you tell Atlas right there
  // instead of navigating to Chat first. Unlike Chat's own "Quick Ask" this is the real conversation
  // (full memory/context), just opened from wherever you are; the reply shows up in Chat as normal.
  import { S, call, go, toast } from './store.svelte.js';
  import Modal from './ui/Modal.svelte';
  import Textarea from './ui/Textarea.svelte';
  import Button from './ui/Button.svelte';
  import Icon from './ui/Icon.svelte';

  let text = $state('');
  let sending = $state(false);
  let ta;

  $effect(() => { if (S.quickChatOpen) queueMicrotask(() => ta?.focus()); });

  async function send() {
    const t = text.trim();
    if (!t || sending) return;
    sending = true;
    const r = await call('chat.send', { topic: S.chatTopic, text: t });
    sending = false;
    if (r === undefined) return; // keep the draft if it failed
    text = '';
    S.quickChatOpen = false;
    toast('Sent to Atlas');
  }
  function openChat() { S.quickChatOpen = false; go('chat'); }
</script>

{#if S.quickChatOpen}
  <Modal open={S.quickChatOpen} title="Quick chat" width={560} onclose={() => (S.quickChatOpen = false)}>
    <Textarea bind:this={ta} bind:value={text} rows={3} maxRows={8} autosize mono={false}
      placeholder="Tell Atlas something, from wherever you are…" onenter={send} disabled={sending} />
    {#snippet footer()}
      <button type="button" class="lnk" onclick={openChat}><Icon name="chat" size={12} /> open full chat</button>
      <span class="grow"></span>
      <Button variant="primary" disabled={!text.trim() || sending} loading={sending} onclick={send}><Icon name="send" size={12} /> Send</Button>
    {/snippet}
  </Modal>
{/if}

<style>
  .lnk { background: none; border: 0; color: var(--fg-dim); font-size: var(--fs-sm); display: inline-flex; align-items: center; gap: 5px; padding: 4px 6px; } .lnk:hover { color: var(--accent); }
  .grow { flex: 1; }
</style>
