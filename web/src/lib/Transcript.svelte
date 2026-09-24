<script>
  // A task's message log: who said what, the tool calls, and which turns followed untrusted input.
  import Badge from './ui/Badge.svelte';
  let { messages = [], max = 420 } = $props();
</script>

<div class="tr" style="max-height:{max}px">
  {#each messages as m}
    <div class="tm {m.Role}">
      <span class="r">{m.Role}{#if m.Name} · {m.Name}{/if}{#if m.Tainted}<Badge tone="attn" title="Written after untrusted content (a web page, an unknown file, an MCP result) entered this conversation. While that is in scope, risky tools ask for confirmation.">untrusted input</Badge>{/if}</span>
      {#if m.ToolCalls}<pre class="tc">{JSON.stringify(m.ToolCalls.map((c) => ({ [c.name]: c.arguments })), null, 1)}</pre>{/if}
      {#if m.Content}<div class="pre c">{m.Content}</div>{/if}
    </div>
  {/each}
</div>

<style>
  .tr { display: flex; flex-direction: column; gap: 4px; overflow: auto; border: 1px solid var(--line); padding: 4px; background: var(--bg); }
  .tm { border-left: 2px solid var(--line-2); padding-left: 6px; }
  .tm.user { border-color: var(--accent); } .tm.tool { border-color: var(--fg-mute); } .tm.assistant { border-color: var(--fg); }
  .r { font-size: 10px; text-transform: uppercase; letter-spacing: 0.08em; color: var(--fg-mute); display: flex; gap: 6px; align-items: center; }
  .tc { margin: 2px 0; max-height: 120px; color: var(--accent-hi); font-size: var(--fs-sm); }
  .c { color: var(--fg-dim); max-height: 160px; overflow: auto; font-size: var(--fs-sm); }
</style>
