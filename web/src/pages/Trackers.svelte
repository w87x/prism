<script>
  import { untrack } from 'svelte';
  import { S, call, listen, toast, confirmBox, ago } from '../lib/store.svelte.js';
  import Panel from '../lib/ui/Panel.svelte';
  import Button from '../lib/ui/Button.svelte';
  import Modal from '../lib/ui/Modal.svelte';
  import Field from '../lib/ui/Field.svelte';
  import Input from '../lib/ui/Input.svelte';
  import Textarea from '../lib/ui/Textarea.svelte';
  import Badge from '../lib/ui/Badge.svelte';
  import Empty from '../lib/ui/Empty.svelte';
  import Icon from '../lib/ui/Icon.svelte';

  let trackers = $state([]);
  let selected = $state(null); // { tracker, rows, changes } — shown in-page instead of a modal (tables get big)
  let createOpen = $state(false);
  let form = $state({ name: '', description: '', columns: '' });
  let descExpanded = $state(false);
  $effect(() => { selected; descExpanded = false; }); // collapse again whenever a different tracker is opened

  async function load() {
    const r = await call('trackers.list', {}, { quiet: true });
    if (r) trackers = r;
  }
  load();
  $effect(() => listen('trackers.update', async () => {
    load();
    if (selected) selected = (await call('trackers.get', { name: selected.tracker.name }, { quiet: true })) || selected;
  }));

  async function open(t) {
    const r = await call('trackers.get', { name: t.name });
    if (r) selected = r;
  }
  // a search-palette link sets S.selectedTrackerName before navigating here
  $effect(() => { if (S.selectedTrackerName) { const name = S.selectedTrackerName; S.selectedTrackerName = null; untrack(() => open({ name })); } });

  async function create() {
    const cols = form.columns.split(/[\n,]/).map((s) => s.trim()).filter(Boolean).map((name) => ({ name }));
    if (!form.name.trim() || !cols.length) { toast('Name and at least one column are required'); return; }
    const t = await call('trackers.create', { name: form.name.trim(), description: form.description.trim(), columns: cols });
    if (t) { createOpen = false; form = { name: '', description: '', columns: '' }; toast(`Tracker "${t.name}" created`); load(); }
  }

  async function deleteTracker(t) {
    if (await confirmBox({ title: 'Delete tracker', text: `Delete "${t.name}" and all its rows and history?`, ok: 'Delete', danger: true })) {
      await call('trackers.delete', { name: t.name });
      selected = null;
      toast(`Tracker "${t.name}" deleted`);
      load();
    }
  }

  async function deleteRow(r) {
    if (await confirmBox({ title: 'Delete row', text: r.key, ok: 'Delete', danger: true })) {
      await call('trackers.row_delete', { id: r.id });
      selected = await call('trackers.get', { name: selected.tracker.name });
    }
  }

  const cellText = (r, col) => {
    const v = r.data?.[col];
    return v === undefined || v === null ? '' : typeof v === 'object' ? JSON.stringify(v) : String(v);
  };
</script>

<div class="pg">
  <div class="bar">
    {#if selected}
      <Button size="sm" variant="ghost" onclick={() => (selected = null)}>← Back</Button>
      <span class="hi">{selected.tracker.name}</span>
      <span class="grow"></span>
      <Button size="sm" variant="danger" onclick={() => deleteTracker(selected.tracker)}>Delete tracker</Button>
    {:else}
      <span class="grow"></span>
      <Button size="sm" variant="primary" onclick={() => (createOpen = true)}><Icon name="plus" size={11} /> New Tracker</Button>
    {/if}
  </div>
  <Panel title={selected ? selected.tracker.name : 'Trackers'} flush grow>
    {#if !selected}
      {#if !trackers.length}
        <Empty>no trackers yet — ask an agent to track something, or create one</Empty>
      {:else}
        <div class="grid">
          {#each trackers as t (t.id)}
            <button class="card" onclick={() => open(t)}>
              <div class="row1"><Icon name="table" size={13} /><span class="nm">{t.name}</span><span class="grow"></span><Badge tone="mute">{t.rows} row{t.rows === 1 ? '' : 's'}</Badge></div>
              {#if t.description}<div class="desc">{t.description}</div>{/if}
              <div class="cols">{t.columns.map((c) => c.name).join(' · ')}</div>
            </button>
          {/each}
        </div>
      {/if}
    {:else}
      {@const t = selected.tracker}
      <div class="detail">
        {#if t.description}
          <button type="button" class="desc-block" class:expanded={descExpanded} onclick={() => (descExpanded = !descExpanded)} title={descExpanded ? 'click to collapse' : 'click to show more'}>
            {t.description}
          </button>
        {/if}
        {#if !selected.rows.length}
          <Empty>no rows yet</Empty>
        {:else}
          <div class="tbl-wrap">
            <table class="t">
              <thead>
                <tr>
                  {#each t.columns as c}<th>{c.name}</th>{/each}
                  <th title="whether this row is still active or was retired">Tracking</th><th>Source</th><th style="width:90px">Updated</th><th style="width:32px"></th>
                </tr>
              </thead>
              <tbody>
                {#each selected.rows as r (r.id)}
                  <tr class:gone={r.status === 'gone'}>
                    {#each t.columns as c}{@const v = cellText(r, c.name)}<td class="ellipsis" style="max-width:320px" title={v}>{v}</td>{/each}
                    <td>{#if r.status === 'gone'}<Badge tone="mute">gone</Badge>{:else}<Badge tone="ok">active</Badge>{/if}</td>
                    <td class="ellipsis" style="max-width:220px" title={r.source_url}>{#if r.source_url}<a href={r.source_url} target="_blank" rel="noopener">{r.source_url}</a>{/if}</td>
                    <td class="mute sm">{ago(r.updated_at)}</td>
                    <td><Button size="sm" variant="ghost" onclick={() => deleteRow(r)}><Icon name="trash" size={11} /></Button></td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
        <Field label="Recent changes">
          {#if !selected.changes.length}
            <div class="mute sm">none yet</div>
          {:else}
            <div class="changes">
              {#each selected.changes as c (c.id)}
                <div class="chg"><span class="hi">{c.row_key}</span> <span class="dim">{c.field}</span>: <span class="mute">{c.old_value || '(none)'}</span> → <span>{c.new_value}</span> <span class="mute sm">{ago(c.created_at)}</span></div>
              {/each}
            </div>
          {/if}
        </Field>
      </div>
    {/if}
  </Panel>
</div>

<Modal bind:open={createOpen} title="New Tracker" width={480}>
  <Field label="Name"><Input bind:value={form.name} placeholder="Apartments" /></Field>
  <Field label="Description" hint="what this tracks and why (optional)"><Textarea bind:value={form.description} rows={2} /></Field>
  <Field label="Columns" hint="one per line or comma-separated, e.g. Price, Bedrooms, Location, Status"><Textarea bind:value={form.columns} rows={3} /></Field>
  {#snippet footer()}
    <Button variant="ghost" onclick={() => (createOpen = false)}>Cancel</Button>
    <Button variant="primary" onclick={create}>Create</Button>
  {/snippet}
</Modal>

<style>
  .pg { display: flex; flex-direction: column; gap: 6px; height: 100%; min-height: 0; }
  .bar { display: flex; align-items: center; gap: 8px; flex: none; }
  .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 8px; padding: 8px; overflow: auto; }
  .card { display: flex; flex-direction: column; gap: 4px; text-align: left; background: var(--bg-2); border: 1px solid var(--line-2); border-radius: var(--r); padding: 8px 10px; cursor: pointer; }
  .card:hover { border-color: var(--fg-dim); }
  .row1 { display: flex; align-items: center; gap: 6px; }
  .nm { font-weight: 600; color: var(--fg-hi); }
  .desc { font-size: var(--fs-sm); color: var(--fg-dim); display: -webkit-box; -webkit-line-clamp: 4; -webkit-box-orient: vertical; overflow: hidden; }
  .cols { font-size: var(--fs-sm); color: var(--fg-mute); }
  .detail { flex: 1; min-height: 0; display: flex; flex-direction: column; gap: 8px; padding: 8px; overflow: hidden; }
  .desc-block { flex: none; text-align: left; background: none; border: 0; padding: 0; margin: 0; color: var(--fg-dim); font-size: var(--fs-sm); line-height: 1.45; cursor: pointer;
    display: -webkit-box; -webkit-line-clamp: 4; -webkit-box-orient: vertical; overflow: hidden; }
  .desc-block.expanded { -webkit-line-clamp: unset; }
  .desc-block:hover { color: var(--fg); }
  .tbl-wrap { flex: 1; min-height: 0; overflow: auto; border: 1px solid var(--line-2); }
  .t { width: 100%; border-collapse: collapse; font-size: var(--fs-sm); }
  .t th { position: sticky; top: 0; background: var(--bg-1); }
  .t th, .t td { padding: 4px 8px; border-bottom: 1px solid var(--line-2); text-align: left; white-space: nowrap; }
  .t tr.gone { opacity: 0.5; }
  .ellipsis { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .changes { display: flex; flex-direction: column; gap: 4px; max-height: 24vh; overflow: auto; font-size: var(--fs-sm); flex: none; }
  .chg { border-bottom: 1px solid var(--line-2); padding-bottom: 3px; }
</style>
