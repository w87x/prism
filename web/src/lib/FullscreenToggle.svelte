<script>
  // A button that puts an element on the whole screen: the browser's Fullscreen API where it exists, otherwise (iPhone
  // Safari has none for ordinary elements) a fixed full-viewport overlay. `target` is the element, or a function returning it.
  // Escape leaves the overlay mode; the browser handles it for real fullscreen.
  let { target, title = 'Full screen', class: cls = '' } = $props();
  let on = $state(false);
  const el = () => (typeof target === 'function' ? target() : target);
  const isReal = (e) => document.fullscreenElement === e || document.webkitFullscreenElement === e;
  function sync() {
    const e = el();
    on = !!e && (isReal(e) || e.classList.contains('fsx'));
    e?.classList.toggle('is-fs', on);
  }
  async function toggle() {
    const e = el();
    if (!e) return;
    if (on) {
      if (e.classList.contains('fsx')) e.classList.remove('fsx');
      else await (document.exitFullscreen || document.webkitExitFullscreen)?.call(document);
    } else {
      const req = e.requestFullscreen || e.webkitRequestFullscreen;
      if (req) { try { await req.call(e); } catch { e.classList.add('fsx'); } } else e.classList.add('fsx');
    }
    sync();
  }
  function key(ev) { const e = el(); if (ev.key === 'Escape' && e?.classList.contains('fsx')) { e.classList.remove('fsx'); sync(); } }
  $effect(() => {
    const f = () => sync();
    document.addEventListener('fullscreenchange', f);
    document.addEventListener('webkitfullscreenchange', f);
    return () => {
      document.removeEventListener('fullscreenchange', f);
      document.removeEventListener('webkitfullscreenchange', f);
      const e = el();
      if (e && isReal(e)) (document.exitFullscreen || document.webkitExitFullscreen)?.call(document);
      e?.classList.remove('fsx', 'is-fs');
    };
  });
</script>

<svelte:window onkeydown={key} />

<button type="button" class="fsb {cls}" onclick={toggle} aria-label={on ? 'Leave full screen' : title} title={on ? 'Leave full screen' : title} aria-pressed={on}>
  <svg width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
    {#if on}<path d="M5 1v4H1M9 1v4h4M5 13V9H1M9 13V9h4" />{:else}<path d="M1 5V1h4M13 5V1H9M1 9v4h4M13 9v4H9" />{/if}
  </svg>
</button>

<style>
  .fsb { display: inline-flex; align-items: center; justify-content: center; width: 32px; height: 32px; border: 1px solid var(--line-2); background: var(--bg-1); color: var(--fg-hi); border-radius: var(--r); cursor: pointer; padding: 0; }
  .fsb:hover { background: var(--bg-4); }
  .fsb.sm { width: 26px; height: 26px; }
</style>
