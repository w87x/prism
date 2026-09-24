<script>
  let { title = '', tone = '', collapsible = false, open = $bindable(true), flush = false, right, children, grow = false } = $props();
</script>

<section class="p {tone}" class:grow class:flush>
  {#if title || right}
    <header>
      <button type="button" class="ttl" class:static={!collapsible} onclick={() => collapsible && (open = !open)}>
        {#if collapsible}<span class="car">{open ? '▾' : '▸'}</span>{/if}{title}
      </button>
      <span class="grow"></span>
      {@render right?.()}
    </header>
  {/if}
  {#if open}<div class="body" class:flush>{@render children?.()}</div>{/if}
</section>

<style>
  .p { --c: var(--line-2); --a: var(--fg-dim); --ah: var(--fg-hi); position: relative; background: var(--panel-bg); border: 1px solid var(--c); display: flex; flex-direction: column; min-height: 0; min-width: 0; transition: border-color 0.15s, box-shadow 0.15s; }
  /* corner accent: a short glowing bar top-left that wakes up on hover */
  .p::before { content: ''; position: absolute; left: -1px; top: -1px; width: 28px; height: 2px; background: var(--a); box-shadow: 0 0 6px var(--a); transition: background 150ms ease, box-shadow 150ms ease; z-index: 1; pointer-events: none; }
  .p:hover { border-color: var(--line-3); }
  .p:hover::before { background: var(--ah); box-shadow: 0 0 8px var(--ah), 0 0 16px color-mix(in srgb, var(--ah) 55%, transparent); }
  .grow { flex: 1 1 auto; }
  .accent { --c: var(--accent-dim); --a: var(--accent); --ah: var(--accent-hi); }
  .attn { --c: var(--attn-dim); box-shadow: var(--glow-attn); --a: var(--attn); --ah: var(--attn-hi); }
  .err { --c: var(--err-dim); --a: var(--err); --ah: var(--err-hi); }
  header { display: flex; align-items: center; gap: 7px; padding: 2px 8px; border-bottom: 1px solid var(--c); background: color-mix(in srgb, var(--bg-2) 85%, transparent); min-height: 22px; flex: none; }
  .ttl { background: none; border: 0; padding: 0; text-transform: uppercase; letter-spacing: 0.1em; font-size: var(--fs-sm); font-weight: 600; color: var(--fg-dim); display: flex; align-items: center; gap: 5px; }
  .ttl.static { cursor: default; }
  .car { color: var(--fg-mute); }
  .body { padding: 8px; min-height: 0; flex: 1 1 auto; display: flex; flex-direction: column; gap: 8px; overflow: auto; }
  .body.flush { padding: 0; gap: 0; }
</style>
