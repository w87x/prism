<script>
  // Which window may change things. Only one PRISM window is the master; every other one is view-only —
  // it can look at everything (and be a full-screen thinking wall on a second display) but its writing
  // calls are refused (see the guard in store.svelte.js). Click a view-only badge to take over.
  import { S, takeOver, toast } from './store.svelte.js';
  const master = $derived(S.editor.editor);
  async function click() {
    if (master) toast('This window is the master. Open PRISM in another window to watch — it starts view-only.', 'info');
    else { await takeOver(); toast('This window is now the master'); }
  }
</script>

<button type="button" class="eb" class:master class:dotonly={S.narrow && master} title={master ? 'This window is the master (can change things)' : 'View-only window — click to make it the master'} onclick={click}>
  <span class="dot"></span>{#if !(S.narrow && master)}{master ? 'master' : S.narrow ? 'view only · tap to control' : 'view only'}{/if}
</button>

<style>
  .eb { display: inline-flex; align-items: center; gap: 5px; background: none; border: 1px solid var(--attn-dim); color: var(--attn-hi); padding: 1px 8px; font-size: 9.5px; letter-spacing: 0.1em; text-transform: uppercase; border-radius: 10px; }
  .eb .dot { width: 6px; height: 6px; border-radius: 50%; background: currentColor; box-shadow: 0 0 5px currentColor; }
  .eb.master { border-color: var(--line-2); color: var(--fg-dim); }
  .eb.master .dot { color: var(--fg); }
  .eb { min-height: 0; height: 22px; }
  .eb.dotonly { width: 22px; padding: 0; justify-content: center; border-color: transparent; }
  .eb:hover { border-color: currentColor; }
</style>
