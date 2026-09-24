<script>
  // A run's live text with the tool calls picked out: "▸ tool args" lines get the tool name in bold, underlined
  // amber and their arguments dimmed, so what the agent DID stands apart from what it said. Context-compaction
  // notes are dimmed italics.
  let { text = '' } = $props();
  const lines = $derived(
    text.replace(/^\n+/, '').split('\n').map((l) => {
      const m = /^▸ ([^\s{[(]+)\s*(.*)$/.exec(l);
      if (m) return { k: 'tool', name: m[1], args: m[2] || '' };
      if (l.startsWith('⟲')) return { k: 'note', t: l };
      return { k: 'txt', t: l };
    })
  );
</script>{#each lines as l}{#if l.k === 'tool'}<span class="ln tool"><span class="mk">▸</span> <b class="nm">{l.name}</b>{#if l.args}<span class="ar"> {l.args}</span>{/if}</span>{:else if l.k === 'note'}<span class="ln note">{l.t}</span>{:else}<span class="ln">{l.t}</span>{/if}{/each}

<style>
  .ln { display: block; min-height: 1em; }
  .tool { color: var(--fg-dim); }
  .mk { color: var(--attn); }
  .nm { color: var(--attn-hi); font-weight: 800; text-decoration: underline; text-decoration-thickness: 1px; text-underline-offset: 2px; text-shadow: 0 0 6px color-mix(in srgb, var(--attn) 55%, transparent); }
  .ar { color: var(--fg-mute); }
  .note { color: var(--fg-mute); font-style: italic; }
</style>
