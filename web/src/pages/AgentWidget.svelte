<script>
  // Right panel of the agents page: settings of the selected agent.
  import { untrack } from 'svelte';
  import { S, call, toast, confirmBox, loadTools, loadSkills, modelOptions, stamp, go } from '../lib/store.svelte.js';
  import Panel from '../lib/ui/Panel.svelte';
  import Field from '../lib/ui/Field.svelte';
  import Input from '../lib/ui/Input.svelte';
  import Textarea from '../lib/ui/Textarea.svelte';
  import Switch from '../lib/ui/Switch.svelte';
  import Select from '../lib/ui/Select.svelte';
  import NumberInput from '../lib/ui/NumberInput.svelte';
  import Tags from '../lib/ui/Tags.svelte';
  import MultiSelect from '../lib/ui/MultiSelect.svelte';
  import Button from '../lib/ui/Button.svelte';
  import Badge from '../lib/ui/Badge.svelte';
  import Modal from '../lib/ui/Modal.svelte';
  import Tabs from '../lib/ui/Tabs.svelte';
  import Empty from '../lib/ui/Empty.svelte';

  const teamOpts = $derived(S.agents.filter((a) => a.role === 'worker' && a.name !== d?.name).map((a) => ({ value: a.name, label: a.name, hint: a.group })));
  const blank = () => ({ id: 0, name: '', icon: '', group: 'General', description: '', soul: '', traits: [], tools: [], skills: [], banks: [], model: '', role: 'worker', system: false, can_delegate: false, team: [], auto_tools: true, max_iterations: 24, enabled: true, soul_version: 1 });

  let d = $state(null);
  let orig = '';
  let saving = $state(false);
  let soulOpen = $state(false);
  let soulTab = $state('edit');
  let history = $state([]);
  let proposals = $state([]);

  // what this agent has learned: the facts of its own profile bank (lessons about how to do its work)
  let lessons = $state({ bank: null, facts: [] });
  async function loadLessons(id, name) {
    lessons = { bank: null, facts: [] };
    if (!id || !name) return;
    const bs = (await call('memory.banks', {}, { quiet: true })) || [];
    const b = bs.find((x) => x.kind === 'profile' && x.owner === name);
    if (!b) return;
    lessons = { bank: b, facts: (await call('memory.facts', { bank_id: b.id, limit: 6 }, { quiet: true })) || [] };
  }
  function openMemory() { S.memoryBank = lessons.bank.id; go('memory'); }

  const sel = $derived(S.selectedAgent);
  const dirty = $derived(d && JSON.stringify(d) !== orig);

  // Depends only on the selection and the agent list; everything else runs untracked so
  // editing the draft can never retrigger it.
  $effect(() => {
    const id = sel;
    S.refresh; S.agents;
    untrack(() => sync(id));
  });
  $effect(() => { loadTools(); loadSkills(); });
  $effect(() => { const id = d?.id; S.refresh; untrack(() => loadLessons(id, d?.name)); });

  function sync(id) {
    if (id === 'new') { if (!d || d.id !== 0) { d = blank(); orig = JSON.stringify(d); history = []; proposals = []; } return; }
    if (!id) { d = null; return; }
    const a = S.agents.find((x) => x.id === id);
    if (!a) return;
    if (d && d.id === a.id && dirty) return; // keep unsaved edits when the list refreshes
    d = structuredClone($state.snapshot(a));
    orig = JSON.stringify(d);
    loadMeta(a.id);
  }

  async function loadMeta(id) {
    history = (await call('agents.history', { id }, { quiet: true })) || [];
    proposals = ((await call('agents.proposals', { status: 'pending' }, { quiet: true })) || []).filter((p) => p.profile_id === id);
  }

  const toolOpts = $derived((S.tools || []).map((t) => ({ value: t.name, label: t.name, hint: t.category, badge: t.risk === 'exec' ? 'exec' : t.risk === 'write' ? 'write' : t.category?.startsWith('mcp:') ? 'mcp' : '' })));
  const skillOpts = $derived((S.skills || []).map((s) => ({ value: s.name, label: s.name, hint: s.description })));
  const modelOpts = $derived([{ value: '', label: '(default chat model)' }, { value: 'role:fast', label: '(fast model)' }, ...modelOptions('chat')]);

  async function save() {
    saving = true;
    const r = await call('agents.save', { profile: $state.snapshot(d), reason: 'edited in UI' });
    saving = false;
    if (r) { toast(`${r.name} saved`); S.selectedAgent = r.id; d = structuredClone(r); orig = JSON.stringify(d); loadMeta(r.id); }
  }
  async function del() {
    if (!(await confirmBox({ title: 'Delete agent', text: `Delete ${d.name}? Its task history stays, the profile and soul history are removed.`, ok: 'Delete', danger: true }))) return;
    if (await call('agents.delete', { id: d.id })) { S.selectedAgent = null; d = null; }
  }
  async function review() {
    const t = await call('agents.review', { name: d.name });
    if (t) toast(`Metis is reviewing ${d.name} (task #${t.id})`);
  }
  function restore(v) { d.soul = v.soul; soulTab = 'edit'; }
</script>

{#if !d}
  <Panel title="Agent" grow>
    <Empty>select an agent in the graph</Empty>
    <div class="sm mute">Click a node to inspect and edit its soul, tools, skills, model and memory banks. Well-known agents (Atlas and the maintenance staff) keep their names and roles.</div>
  </Panel>
{:else}
  <Panel title={d.id ? d.name : 'New agent'} grow>
    {#snippet right()}{#if d.system}<Badge tone="accent">{d.role === 'entry' ? 'entry' : 'staff'}</Badge>{/if}<span class="sm mute">v{d.soul_version}</span>{/snippet}
    <div class="form">
      <Field label="Name"><Input bind:value={d.name} disabled={d.system} placeholder="e.g. Scout" /></Field>
      <div class="two">
        <Field label="Group"><Input bind:value={d.group} placeholder="Web, Coding…" /></Field>
        <Field label="Max steps"><NumberInput bind:value={d.max_iterations} min={2} max={80} /></Field>
      </div>
      <Field label="Description" hint="shown in the agent catalog"><Textarea bind:value={d.description} rows={3} mono={false} autosize maxRows={8} /></Field>
      <Field label="Model"><Select bind:value={d.model} options={modelOpts} /></Field>
      <div class="sw">
        <Switch bind:checked={d.enabled} label="enabled" disabled={d.role === 'entry'} />
        <Switch bind:checked={d.can_delegate} label="can delegate" disabled={d.role === 'entry'} />
        <Switch bind:checked={d.auto_tools} label="auto-select tools" disabled={d.role === 'entry'} title="Sherpa adds the few tools each task needs" />
      </div>
      {#if d.role !== 'entry'}<Field label="Team" hint="specialists {d.name || 'this agent'} leads: it delegates only to them, waits for their results and consolidates them (implies can-delegate)"><MultiSelect bind:value={d.team} options={teamOpts} placeholder="no team" /></Field>{/if}
      <Field label="Traits" hint="searchable keywords (agent_find)"><Tags bind:value={d.traits} placeholder="add trait…" /></Field>
      <Field label="Toolset" hint="everything else loads on demand"><MultiSelect bind:value={d.tools} options={toolOpts} placeholder="base tools only" /></Field>
      <Field label="Skills"><MultiSelect bind:value={d.skills} options={skillOpts} placeholder="none pinned" /></Field>
      <Field label="Extra memory banks" hint="user:, project:<name>, domain:<name>"><Tags bind:value={d.banks} placeholder="domain:Cooking" /></Field>
      <Field label="What {d.name || 'it'} has learned" hint="the lessons in its own memory bank (profile:{d.name})">
        {#if lessons.bank}
          <div class="lessons">
            {#each lessons.facts as f (f.id)}<div class="ls"><span class="mute sm">#{f.id}</span> {f.text}</div>{/each}
            <div class="row between"><span class="sm mute">{lessons.bank.facts} facts in the bank</span><Button size="sm" variant="ghost" onclick={openMemory}>Open in Memory</Button></div>
          </div>
        {:else}<div class="sm mute">nothing yet — lessons appear here once {d.name || 'the agent'} has worked and memory has digested it</div>{/if}
      </Field>
      <Field label="Soul">
        {#snippet right()}<Button size="sm" variant="ghost" onclick={() => (soulOpen = true)}>Expand</Button>{/snippet}
        <Textarea bind:value={d.soul} rows={7} />
      </Field>

      {#if d.probation}
        <div class="props">
          <h4>On probation</h4>
          <div class="sm dim">Another agent hired {d.name}. It can work, but has no shell or other exec tools and cannot delegate until you confirm the hire.</div>
          <div class="row"><Button size="sm" variant="primary" onclick={async () => { if (await call('agents.probation', { id: d.id, keep: true })) { toast(`${d.name} confirmed`); S.refresh++; } }}>Confirm the hire</Button>
            <Button size="sm" variant="ghost" onclick={async () => { if (await call('agents.probation', { id: d.id, keep: false })) { toast(`${d.name} switched off`); S.refresh++; } }}>Let go</Button></div>
        </div>
      {/if}
      {#if proposals.length}
        <div class="props">
          <h4>Pending evolution proposals</h4>
          {#each proposals as p}
            <div class="prop">
              <div class="sm dim">{stamp(p.created_at)} · <Badge tone="accent">{p.kind}</Badge> {p.rationale}</div>
              <div class="row"><Button size="sm" variant="primary" onclick={() => (S.review = p)}>Review changes…</Button></div>
            </div>
          {/each}
        </div>
      {/if}
    </div>
    <div class="actions">
      <Button variant="primary" disabled={!dirty || !d.name.trim()} loading={saving} onclick={save}>Save</Button>
      <Button variant="ghost" disabled={!dirty} onclick={() => { d = JSON.parse(orig); }}>Revert</Button>
      <span class="grow"></span>
      {#if d.id && d.role !== 'entry'}<Button size="sm" variant="accent" onclick={review} title="Ask Metis to review this agent">Evolve…</Button>{/if}
      {#if d.id && !d.system}<Button size="sm" variant="danger" onclick={del}>Delete</Button>{/if}
    </div>
  </Panel>
{/if}

<Modal bind:open={soulOpen} title="Soul — {d?.name || ''}" width={820}>
  <Tabs tabs={[{ id: 'edit', label: 'Edit' }, { id: 'hist', label: 'History', badge: history.length }]} bind:active={soulTab} />
  {#if soulTab === 'edit'}
    <Textarea bind:value={d.soul} rows={26} />
    <div class="sm mute">{d?.soul?.length || 0} chars · ≈{Math.round((d?.soul?.length || 0) / 4)} tokens in every prompt of this agent</div>
  {:else}
    {#each history as v}
      <div class="hv">
        <div class="row between"><span class="hi">v{v.version}</span><span class="sm mute">{stamp(v.created_at)} · {v.reason}</span><Button size="sm" onclick={() => restore(v)}>Load into editor</Button></div>
        <pre>{v.soul}</pre>
      </div>
    {/each}
  {/if}
  {#snippet footer()}<Button variant="primary" onclick={() => (soulOpen = false)}>Done</Button>{/snippet}
</Modal>

<style>
  .form { display: flex; flex-direction: column; gap: 9px; }
  .two { display: grid; grid-template-columns: 1fr auto; gap: 8px; align-items: end; }
  .sw { display: flex; flex-wrap: wrap; gap: 6px 14px; }
  .actions { display: flex; gap: 6px; align-items: center; border-top: 1px solid var(--line); padding-top: 8px; margin-top: auto; }
  .props { border: 1px solid var(--attn-dim); background: var(--attn-bg); padding: 6px; display: flex; flex-direction: column; gap: 6px; }
  .props h4 { color: var(--attn); }
  .hv pre { max-height: 200px; }
  .lessons { display: flex; flex-direction: column; gap: 3px; border: 1px solid var(--line); background: var(--bg); padding: 4px 6px; max-height: 190px; overflow: auto; }
  .ls { color: var(--fg-dim); }
</style>
