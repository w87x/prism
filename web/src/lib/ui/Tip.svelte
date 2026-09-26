<script>
  // Text cut to one line that shows its full content in a readable popover on hover (native title= is slow and tiny).
  let { text = '', max = 280 } = $props();
  let box = $state(null); // { x, y }
  function show(e) {
    if (!text) return;
    const r = e.currentTarget.getBoundingClientRect();
    box = { x: Math.min(r.left, window.innerWidth - 500), y: r.bottom + 4 };
  }
</script>

<span class="tip" style="max-width:{max}px" onmouseenter={show} onmouseleave={() => (box = null)} role="note">{text}</span>
{#if box}<div class="pop" style="left:{Math.max(8, box.x)}px; top:{box.y}px">{text}</div>{/if}

<style>
  .tip { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .pop { position: fixed; z-index: 60; width: min(480px, 92vw); max-height: 50vh; overflow: auto; background: var(--bg-2); border: 1px solid var(--line-3); padding: 7px 9px; color: var(--fg); font-size: var(--fs-sm); line-height: 1.45; white-space: pre-wrap; word-break: break-word; box-shadow: 0 6px 24px rgba(0, 0, 0, 0.45); pointer-events: none; }
</style>
