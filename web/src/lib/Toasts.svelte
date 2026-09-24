<script>
  import { S, dismissToast } from './store.svelte.js';
</script>

<div class="wrap" aria-live="polite">
  {#each S.toasts as t (t.id)}
    <button type="button" class="t {t.tone}" title="dismiss" onclick={() => dismissToast(t.id)}>
      <span class="ic">{t.tone === 'err' ? '✗' : t.tone === 'attn' ? '!' : t.tone === 'warn' ? '!' : '›'}</span>
      <span class="tx">{t.text}</span>
    </button>
  {/each}
</div>

<style>
  .wrap { position: fixed; right: 12px; bottom: 34px; z-index: 5000; display: flex; flex-direction: column; gap: 6px; max-width: min(460px, 92vw); }
  .t { --c: var(--fg); display: flex; gap: 8px; padding: 5px 10px; text-align: left; font: inherit; background: var(--bg-1); border: 1px solid var(--c); color: var(--c); box-shadow: 0 4px 18px rgba(0, 0, 0, 0.7); cursor: pointer; animation: in 0.15s ease-out; }
  .attn { --c: var(--attn); box-shadow: 0 4px 18px rgba(0, 0, 0, 0.7), var(--glow-attn); }
  .warn { --c: var(--warn); }
  .err { --c: var(--err); box-shadow: 0 4px 18px rgba(0, 0, 0, 0.7), var(--glow-err); }
  .ic { font-weight: 700; }
  .tx { color: var(--fg-hi); word-break: break-word; }
  @keyframes in { from { opacity: 0; transform: translateY(6px); } }
</style>
