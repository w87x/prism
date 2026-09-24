<script>
  // Right panel of the tools page: full description and parameters of the selected tool.
  import { S, loadTools } from '../lib/store.svelte.js';
  import Panel from '../lib/ui/Panel.svelte';
  import Badge from '../lib/ui/Badge.svelte';
  import Empty from '../lib/ui/Empty.svelte';

  $effect(() => { loadTools(); });
  const t = $derived((S.tools || []).find((x) => x.name === S.selectedTool));
  const params = $derived.by(() => {
    const sch = t?.params || {};
    const req = new Set(sch.required || []);
    return Object.entries(sch.properties || {}).map(([k, v]) => ({ k, type: v.type || (v.enum ? 'enum' : 'any'), desc: v.description || '', enum: v.enum, req: req.has(k) }));
  });
  const tone = { read: 'ok', write: 'attn', exec: 'err' };
</script>

<Panel title={t ? t.name : 'Tool'} grow>
  {#if !t}
    <Empty>click a tool name</Empty>
  {:else}
    <div class="row wrap gap-4"><Badge tone={tone[t.risk]} em={5}>{t.risk}</Badge><Badge tone="mute">{t.category}</Badge>
      {#if t.base}<Badge tone="accent">base</Badge>{/if}{#if t.deferred}<Badge tone="mute">deferred</Badge>{/if}{#if t.untrusted}<Badge tone="attn">external content</Badge>{/if}</div>
    <div class="pre desc">{t.description}</div>
    <div class="sm mute">source: {t.source} · {t.enabled ? 'enabled' : 'disabled'} · {t.armed ? 'armed' : 'safe'}</div>
    {#if params.length}
      <h4>Parameters</h4>
      {#each params as p}
        <div class="pm"><div><span class="hi">{p.k}</span> <span class="mute sm">{p.type}{p.req ? ' · required' : ''}</span></div>
          {#if p.desc}<div class="sm dim">{p.desc}</div>{/if}{#if p.enum}<div class="sm mute">one of: {p.enum.join(', ')}</div>{/if}</div>
      {/each}
    {:else}<div class="sm mute">takes no parameters</div>{/if}
  {/if}
</Panel>

<style>
  .desc { color: var(--fg-dim); line-height: 1.5; }
  .pm { border-left: 2px solid var(--line-2); padding-left: 7px; margin-bottom: 4px; }
</style>
