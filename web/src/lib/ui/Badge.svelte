<script>
  let { tone = 'ok', title = '', w = 0, em = 0, children } = $props();
</script>

<span class="b {tone}" class:fx={w > 0 || em > 0} style={em > 0 ? `width:${em}em` : w > 0 ? `min-width:${w}ch` : null} {title}>{@render children?.()}</span>

<style>
  .b { display: inline-block; padding: 0 5px; font-size: 10px; line-height: 1.5; text-transform: uppercase; letter-spacing: 0.07em;
    border: 1px solid color-mix(in srgb, var(--c) 55%, transparent); color: var(--c); border-radius: var(--r); white-space: nowrap; }
  .fx { text-align: center; box-sizing: border-box; }
  .ok { --c: var(--fg); }
  .mute { --c: var(--fg-mute); }
  .accent { --c: var(--accent); }
  .attn { --c: var(--attn); }
  .warn { --c: var(--warn); }
  .err { --c: var(--err); }
</style>
