<script>
  // "keep" for a picture in the chat: pictures an agent fetched are temporary (they delete themselves after a week);
  // one click makes this one permanent.
  import { call, toast } from '../store.svelte.js';

  let { id } = $props();
  let temp = $state(false);
  let kept = $state(false);
  let known = false;
  let listing;
  $effect(() => {
    if (known) return;
    known = true;
    listing ??= call('artifacts.list', {}, { quiet: true });
    listing.then((r) => { const a = (r || []).find((x) => x.id === id); temp = !!a?.expires_at; });
  });
  async function keep() {
    if (await call('artifacts.keep', { id })) { kept = true; temp = false; toast('Picture kept for good'); }
  }
</script>

{#if temp}
  <button type="button" class="keep" onclick={keep} title="This picture deletes itself after a few days: keep it permanently">keep</button>
{:else if kept}
  <span class="kept">✓ kept</span>
{/if}

<style>
  .keep, .kept { font-size: 10px; text-transform: uppercase; letter-spacing: 0.08em; margin-left: 6px; vertical-align: bottom; }
  .keep { background: var(--bg-2); border: 1px solid var(--line-3); color: var(--fg-mute); padding: 0 6px; cursor: pointer; }
  .keep:hover { color: var(--accent-hi); border-color: var(--accent); }
  .kept { color: var(--ok); }
</style>
