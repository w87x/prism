<script>
  // Knowledge base: pages written by the model from memory, in folders. Smart folders propose their
  // own pages; pages regenerate on their own when enough relevant memory changed.
  import { untrack } from 'svelte';
  import { S, call, listen, toast, confirmBox, ago, stamp, loadSetting, saveSetting, activeRuns, fmtTokens, modelOptions } from '../lib/store.svelte.js';
  import RichMessage from '../lib/rich/RichMessage.svelte';
  import Panel from '../lib/ui/Panel.svelte';
  import Button from '../lib/ui/Button.svelte';
  import Input from '../lib/ui/Input.svelte';
  import Select from '../lib/ui/Select.svelte';
  import Switch from '../lib/ui/Switch.svelte';
  import Badge from '../lib/ui/Badge.svelte';
  import Led from '../lib/ui/Led.svelte';
  import Modal from '../lib/ui/Modal.svelte';
  import Field from '../lib/ui/Field.svelte';
  import Textarea from '../lib/ui/Textarea.svelte';
  import NumberInput from '../lib/ui/NumberInput.svelte';
  import Empty from '../lib/ui/Empty.svelte';
  import Icon from '../lib/ui/Icon.svelte';

  let folders = $state([]);
  let pages = $state([]);
  let sel = $state(0);
  let page = $state(null);
  let open = $state({ 0: true });
  let cfg = $state({ enabled: true, regen_every_h: 24, min_changes: 3, smart_expand: true, model: '' });
  let cfgOpen = $state(false);
  // the writer's live run for the open page (its tokens stream in as the page is built)
  const writer = $derived(page ? activeRuns().find((r) => r.kb === page.id && !r.done) : null);
  let stream = $state();
  $effect(() => { writer?.buf; queueMicrotask(() => stream && (stream.scrollTop = stream.scrollHeight)); });

  async function loadTree() {
    const t = await call('kb.tree', {}, { quiet: true });
    if (t) { folders = t.folders || []; pages = t.pages || []; }
  }
  async function loadPage() {
    if (!sel) { page = null; return; }
    page = (await call('kb.page_get', { id: sel }, { quiet: true })) || null;
  }
  $effect(() => { loadTree(); untrack(() => loadSetting('kb', cfg).then((c) => (cfg = c))); });
  $effect(() => { sel; untrack(loadPage); });
  // a search-palette link sets S.selectedKbPage before navigating here
  $effect(() => { if (S.selectedKbPage != null) { sel = S.selectedKbPage; S.selectedKbPage = null; } });
  $effect(() => listen('kb.update', () => { loadTree(); untrack(loadPage); }));

  const kids = (pid) => folders.filter((f) => (f.parent_id ?? 0) === pid);
  const pagesIn = (fid) => pages.filter((p) => (p.folder_id ?? 0) === fid);
  const tone = (s) => ({ ready: 'ok', generating: 'warn', error: 'error', empty: 'off' })[s] || 'off';
  const flat = $derived.by(() => {
    const out = [{ value: 0, label: '(top level)' }];
    const walk = (pid, d) => kids(pid).forEach((f) => { out.push({ value: f.id, label: '  '.repeat(d) + f.name }); walk(f.id, d + 1); });
    walk(0, 0);
    return out;
  });
  const agentOpts = $derived([{ value: '', label: 'Atlas (default)' }, ...S.agents.filter((a) => a.role !== 'entry' && a.enabled).map((a) => ({ value: a.name, label: a.name, hint: a.group }))]);

  // ── page / folder editing ──
  let pOpen = $state(false);
  let ep = $state(null);
  let fOpen = $state(false);
  let ef = $state(null);
  function newPage(fid = 0) { ep = { id: 0, folder_id: fid, title: '', query: '', enrich: false, auto: true, agent: '' }; pOpen = true; }
  function editPage() { ep = { ...$state.snapshot(page), folder_id: page.folder_id ?? 0 }; pOpen = true; }
  async function savePage() {
    const id = await call('kb.page_save', { ...$state.snapshot(ep), folder_id: ep.folder_id || null, body: undefined });
    if (!id) return;
    pOpen = false;
    const isNew = !ep.id;
    sel = id;
    if (isNew) { await call('kb.page_generate', { id }); toast('Generating…'); }
    loadTree();
  }
  async function delPage() {
    if (await confirmBox({ title: 'Delete page', text: page.title, ok: 'Delete', danger: true })) { await call('kb.page_delete', { id: page.id }); sel = 0; loadTree(); }
  }
  function newFolder(pid = 0) { ef = { id: 0, parent_id: pid, name: '', query: '', smart: false, max_pages: 6 }; fOpen = true; }
  async function saveFolder() {
    const id = await call('kb.folder_save', { ...$state.snapshot(ef), parent_id: ef.parent_id || null });
    if (!id) return;
    fOpen = false;
    open[id] = true;
    loadTree();
    if (ef.smart && !ef.id && ef.query.trim()) { await call('kb.folder_expand', { id, generate: true }); toast('The smart folder is choosing its pages…'); }
  }
  async function delFolder(f) {
    if (await confirmBox({ title: 'Delete folder', text: `Delete “${f.name}” with all pages and subfolders inside?`, ok: 'Delete', danger: true })) { await call('kb.folder_delete', { id: f.id }); loadTree(); }
  }
  async function regenerate() {
    if (await call('kb.page_generate', { id: page.id })) { page.status = 'generating'; toast('Regenerating…'); }
  }
  async function toggle(field) { await call('kb.page_save', { ...$state.snapshot(page), body: undefined, folder_id: page.folder_id || null, [field]: !page[field] }); loadPage(); }
  async function saveCfg() { await saveSetting('kb', cfg, 'Knowledge base settings saved'); }
</script>

{#snippet branch(pid, depth)}
  {#each kids(pid) as f (f.id)}
    <div class="fr" style="padding-left:{6 + depth * 14}px">
      <button type="button" class="fh" onclick={() => (open[f.id] = !open[f.id])}><span class="car">{open[f.id] ? '▾' : '▸'}</span><span class="fn ellipsis">{f.name}</span>{#if f.smart}<Badge tone="accent" title="smart folder: it proposes its own pages from memory">smart</Badge>{/if}</button>
      <span class="acts">
        <button type="button" title="new page here" onclick={() => newPage(f.id)}><Icon name="plus" size={11} /></button>
        <button type="button" title="new subfolder" onclick={() => newFolder(f.id)}>▸+</button>
        {#if f.smart}<button type="button" title="let the folder propose pages now" onclick={() => call('kb.folder_expand', { id: f.id, generate: true }).then(() => toast('Smart folder is working…'))}><Icon name="refresh" size={11} /></button>{/if}
        <button type="button" title="edit folder" onclick={() => { ef = { ...f, parent_id: f.parent_id ?? 0 }; fOpen = true; }}><Icon name="edit" size={11} /></button>
        <button type="button" title="delete folder" onclick={() => delFolder(f)}><Icon name="x" size={11} /></button>
      </span>
    </div>
    {#if open[f.id]}
      {@render branch(f.id, depth + 1)}
      {#each pagesIn(f.id) as p (p.id)}{@render prow(p, depth + 1)}{/each}
    {/if}
  {/each}
{/snippet}

{#snippet prow(p, depth)}
  <button type="button" class="pr" class:on={sel === p.id} style="padding-left:{22 + depth * 14}px" onclick={() => (sel = p.id)}>
    <Led state={p.status === 'generating' ? 'standby' : tone(p.status)} pulse={p.status === 'generating'} size={7} /><span class="ellipsis">{p.title}</span>
  </button>
{/snippet}

<div class="pg">
  <div class="bar">
    <span class="sm mute">Pages are written by the model from your memory banks; ask for anything with a query. Enrich lets an agent research the gaps first.</span>
    <span class="grow"></span>
    <Button size="sm" variant="ghost" onclick={() => (cfgOpen = true)}>Auto-regeneration</Button>
    <Button size="sm" onclick={() => newFolder(0)}><Icon name="plus" size={11} /> Folder</Button>
    <Button size="sm" variant="primary" onclick={() => newPage(0)}><Icon name="plus" size={11} /> Page</Button>
  </div>

  <div class="body">
    <Panel title="Pages" flush>
      <div class="tree scroll">
        {@render branch(0, 0)}
        {#each pagesIn(0) as p (p.id)}{@render prow(p, 0)}{/each}
        {#if !folders.length && !pages.length}<Empty>no pages yet</Empty>{/if}
      </div>
    </Panel>

    <Panel title={page ? page.title : 'Page'} grow>
      {#snippet right()}
        {#if page}
          <span class="sm mute">{page.generated_at ? 'built ' + ago(page.generated_at) + ' ago · ' + page.sources.length + ' facts' : 'not generated'}</span>
          <Button size="sm" variant="primary" loading={page.status === 'generating'} onclick={regenerate}><Icon name="refresh" size={11} /> {page.generated_at ? 'Regenerate' : 'Generate'}</Button>
          <Button size="sm" variant="ghost" onclick={editPage}>Edit</Button>
          <Button size="sm" variant="ghost" onclick={delPage}><Icon name="trash" size={11} /></Button>
        {/if}
      {/snippet}
      {#if !page}
        <Empty>select a page or create one</Empty>
      {:else}
        <div class="meta">
          <span class="q"><span class="mute">query</span> {page.query}</span>
          <Switch checked={page.enrich} label="enrich" onchange={() => toggle('enrich')} title="Let an agent research gaps before writing" />
          <Switch checked={page.auto} label="auto" onchange={() => toggle('auto')} title="Regenerate automatically when memory changed" />
        </div>
        {#if page.status === 'error'}<div class="err sm pre">{page.error}</div>{/if}
        {#if page.status === 'generating'}
          <div class="gen">
            <div class="sm dim row gap-6"><Led state="standby" live={writer?.live} liveMs={writer?.liveMs} pulse={!writer} size={7} />
              {#if writer}<span>Scribe is writing this page · {fmtTokens(writer.tokens_out)}↓ so far</span><button type="button" class="lnk" onclick={() => (S.peekRun = writer)}>open live view</button>
              {:else if page.enrich}<span>an agent is researching the gaps first — follow it in the task queue</span>
              {:else}<span>reading memory and waiting for the model…</span>{/if}
            </div>
            {#if writer}<div class="stream" bind:this={stream}>{writer.buf.replace(/^\n+/, '')}</div>{/if}
          </div>
        {/if}
        {#if page.body}<div class="doc"><RichMessage text={page.body} /></div>{:else if page.status !== 'generating'}<Empty>this page has not been generated yet</Empty>{/if}
      {/if}
    </Panel>
  </div>
</div>

<Modal bind:open={pOpen} title={ep?.id ? 'Edit page' : 'New page'} width={620}>
  {#if ep}
    <Field label="Title"><Input bind:value={ep.title} placeholder="Cooking preferences" /></Field>
    <Field label="Query" hint="what the page should cover — the model reads matching memory facts and writes it up"><Textarea bind:value={ep.query} rows={3} mono={false} autosize /></Field>
    <div class="row wrap gap-12">
      <div class="grow"><Field label="Folder"><Select bind:value={ep.folder_id} options={flat} /></Field></div>
      <div class="grow"><Field label="Enrichment agent"><Select bind:value={ep.agent} options={agentOpts} searchable /></Field></div>
    </div>
    <div class="row gap-12"><Switch bind:checked={ep.enrich} label="enrich — an agent researches gaps first" /><Switch bind:checked={ep.auto} label="regenerate automatically" /></div>
  {/if}
  {#snippet footer()}<Button variant="ghost" onclick={() => (pOpen = false)}>Cancel</Button><Button variant="primary" disabled={!ep?.title?.trim() || !ep?.query?.trim()} onclick={savePage}>{ep?.id ? 'Save' : 'Create & generate'}</Button>{/snippet}
</Modal>

<Modal bind:open={fOpen} title={ef?.id ? 'Edit folder' : 'New folder'} width={560}>
  {#if ef}
    <Field label="Name"><Input bind:value={ef.name} /></Field>
    <Field label="Inside"><Select bind:value={ef.parent_id} options={flat.filter((o) => o.value !== ef.id)} /></Field>
    <Switch bind:checked={ef.smart} label="smart folder — it proposes and creates its own pages" />
    {#if ef.smart}
      <Field label="What belongs here" hint="the folder reads memory for this and creates pages for the topics it finds"><Textarea bind:value={ef.query} rows={3} mono={false} autosize /></Field>
      <Field label="Max pages"><NumberInput bind:value={ef.max_pages} min={1} max={30} /></Field>
    {/if}
  {/if}
  {#snippet footer()}<Button variant="ghost" onclick={() => (fOpen = false)}>Cancel</Button><Button variant="primary" disabled={!ef?.name?.trim()} onclick={saveFolder}>Save</Button>{/snippet}
</Modal>

<Modal bind:open={cfgOpen} title="Knowledge base settings" width={520}>
  <Field label="Writing model" hint="The model that writes pages and lets smart folders choose them. Default: the fast (auxiliary) model from Settings → Models, so a slow reasoning model is not tied up. Pick a bigger one for better prose.">
    <Select bind:value={cfg.model} options={[{ value: '', label: 'fast model (default)' }, ...modelOptions('chat')]} searchable />
  </Field>
  <div class="sm mute">Pages with “auto” are rebuilt when they are older than the interval <b class="hi">and</b> enough relevant facts were added or retired since they were written. Smart folders are re-examined on the same interval.</div>
  <Switch bind:checked={cfg.enabled} label="enabled" />
  <div class="row wrap gap-12">
    <Field label="At most every"><NumberInput bind:value={cfg.regen_every_h} min={1} max={720} unit="h" /></Field>
    <Field label="…and at least this many changes"><NumberInput bind:value={cfg.min_changes} min={1} max={200} /></Field>
  </div>
  <Switch bind:checked={cfg.smart_expand} label="smart folders may create new pages" />
  {#snippet footer()}<Button variant="ghost" onclick={() => (cfgOpen = false)}>Close</Button><Button variant="primary" onclick={() => saveCfg().then(() => (cfgOpen = false))}>Save</Button>{/snippet}
</Modal>

<style>
  .pg { display: flex; flex-direction: column; gap: 6px; height: 100%; min-height: 0; }
  .bar { display: flex; align-items: center; gap: 8px; flex: none; }
  .body { flex: 1; display: grid; grid-template-columns: 270px minmax(0, 1fr); gap: 6px; min-height: 0; }
  @media (max-width: 820px) { .body { grid-template-columns: minmax(0, 1fr); grid-template-rows: minmax(0, 40%) minmax(0, 1fr); } }
  .tree { flex: 1; padding: 4px 0; }
  .fr { display: flex; align-items: center; gap: 4px; padding-right: 6px; }
  .fh { flex: 1; min-width: 0; display: flex; align-items: center; gap: 5px; background: none; border: 0; padding: 2px 0; color: var(--fg-hi); text-align: left; text-transform: uppercase; letter-spacing: 0.06em; font-size: var(--fs-sm); }
  .car { color: var(--fg-mute); width: 10px; }
  .acts { display: none; gap: 2px; } .fr:hover .acts { display: flex; }
  .acts button { background: none; border: 0; color: var(--fg-mute); padding: 1px 3px; font-size: 11px; } .acts button:hover { color: var(--fg); }
  .pr { display: flex; align-items: center; gap: 7px; width: 100%; background: none; border: 0; border-left: 2px solid transparent; padding: 2px 8px; color: var(--fg-dim); text-align: left; }
  .pr:hover { background: var(--bg-2); color: var(--fg); }
  .pr.on { border-left-color: var(--fg); background: var(--bg-3); color: var(--fg-hi); }
  .meta { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; padding-bottom: 6px; border-bottom: 1px dotted var(--line-2); }
  .q { flex: 1; min-width: 200px; color: var(--fg-dim); font-size: var(--fs-sm); }
  .doc { max-width: 900px; }
  .gen { display: flex; flex-direction: column; gap: 4px; }
  .stream { background: radial-gradient(ellipse at center, #04120c 0%, #010503 100%); border: 1px solid var(--line-2); padding: 6px 10px; max-height: 180px; overflow: auto; white-space: pre-wrap; word-break: break-word; font-size: var(--fs-sm); color: var(--fg-dim); line-height: 1.45; }
  .lnk { background: none; border: 0; padding: 0; color: var(--accent); font-size: var(--fs-sm); } .lnk:hover { color: var(--accent-hi); }
</style>
