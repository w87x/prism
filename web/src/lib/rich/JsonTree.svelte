<script>
  // Collapsible tree for JSON/YAML values (native <details>, no HTML injection).
  import JsonTree from './JsonTree.svelte';
  let { data, name = null, depth = 0, last = true } = $props();
  const isObj = $derived(data !== null && typeof data === 'object');
  const isArr = $derived(Array.isArray(data));
  const entries = $derived(isObj ? Object.entries(data) : []);
  const summary = $derived(isArr ? `[${data.length}]` : isObj ? `{${entries.length}}` : '');
</script>

{#if isObj}
  <details class="n" open={depth < 2}>
    <summary>{#if name !== null}<span class="k">{name}</span><span class="p">:</span> {/if}<span class="b">{isArr ? '[' : '{'}</span><span class="cnt">{summary}</span></summary>
    <div class="kids">
      {#each entries as [k, v], i (k)}<JsonTree data={v} name={isArr ? null : k} depth={depth + 1} last={i === entries.length - 1} />{/each}
    </div>
    <span class="b close">{isArr ? ']' : '}'}</span>
  </details>
{:else}
  <div class="leaf">{#if name !== null}<span class="k">{name}</span><span class="p">:</span> {/if}<span class="v {data === null ? 'null' : typeof data}">{typeof data === 'string' ? JSON.stringify(data) : String(data)}</span></div>
{/if}

<style>
  .n, .leaf { font-family: var(--font); font-size: 12px; line-height: 1.55; }
  summary { cursor: pointer; list-style: none; user-select: none; }
  summary::-webkit-details-marker { display: none; }
  summary::before { content: '▸'; display: inline-block; width: 1.1em; color: var(--fg-mute); }
  details[open] > summary::before { content: '▾'; }
  .kids { padding-left: 1.1em; border-left: 1px dotted var(--line-2); margin-left: 0.35em; }
  .leaf { padding-left: 1.1em; }
  .close { padding-left: 0.1em; }
  .k { color: var(--accent-hi); } .p { color: var(--fg-mute); } .b { color: var(--fg-dim); }
  .cnt { color: var(--fg-faint); margin-left: 0.4em; font-size: 10.5px; }
  .v.string { color: var(--fg-hi); } .v.number { color: var(--attn-hi); } .v.boolean { color: var(--warn); } .v.null { color: var(--fg-mute); font-style: italic; }
</style>
