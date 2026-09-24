<script>
  // Review of a change an agent (Metis) proposes for another agent: what changes, why, and whether the
  // agent moved on since — then apply it, apply your own wording of it, or reject it.
  import { S, call, toast, stamp } from './store.svelte.js';
  import { diffText, diffStats } from './diff.js';
  import Modal from './ui/Modal.svelte';
  import Button from './ui/Button.svelte';
  import Badge from './ui/Badge.svelte';
  import Diff from './ui/Diff.svelte';
  import Textarea from './ui/Textarea.svelte';
  import Segmented from './ui/Segmented.svelte';

  let open = $state(false);
  let mode = $state('diff'); // diff | edit
  let split = $state(false);
  let text = $state('');
  let busy = $state(false);

  const p = $derived(S.review);
  $effect(() => {
    open = !!p;
    if (p) { mode = 'diff'; text = p.proposal; }
  });
  function onclose() { S.review = null; }

  const label = { soul: 'soul', tools: 'toolset', traits: 'traits' };
  const pending = $derived(p?.status === 'pending');
  const stats = $derived(p ? diffStats(diffText(p.base, p.proposal)) : { add: 0, del: 0 });
  const stale = $derived(p && p.kind === 'soul' && p.base_version > 0 && p.current_version !== p.base_version);
  const edited = $derived(p && text.trim() !== p.proposal.trim());
  const growth = $derived(p && p.kind === 'soul' && p.base.length ? Math.round(((p.proposal.length - p.base.length) / p.base.length) * 100) : null);

  async function decide(apply) {
    busy = true;
    const ok = await call('agents.decide', { id: p.id, apply, edited: apply && edited ? text : '' });
    busy = false;
    if (ok) {
      toast(apply ? `${p.agent}: ${label[p.kind] || p.kind} updated` : 'Proposal rejected');
      S.refresh++;
      S.review = null;
    }
  }
</script>

<Modal bind:open title={p ? `${p.agent} — proposed ${label[p.kind] || p.kind} change` : ''} width={980} {onclose}>
  {#if p}
    <div class="meta">
      <Badge tone={pending ? 'attn' : p.status === 'applied' ? 'ok' : 'mute'}>{p.status}</Badge>
      <span class="sm mute">{stamp(p.created_at)}</span>
      <span class="sm add">+{stats.add}</span><span class="sm del">−{stats.del}</span>
      {#if growth !== null}<span class="sm mute" title="Metis is told to keep growth under ~15%">size {growth > 0 ? '+' : ''}{growth}%</span>{/if}
    </div>
    <div class="why"><span class="k">Why</span> {p.rationale || '—'}</div>
    {#if stale}
      <div class="warn">The agent has changed since this was written (v{p.base_version} → v{p.current_version}). The diff shows what the proposal was written against; applying it replaces the current {label[p.kind]} entirely.</div>
    {/if}
    {#if pending}
      <div class="bar">
        <Segmented size="sm" value={mode} options={[{ value: 'diff', label: 'changes' }, { value: 'edit', label: 'edit before applying' }]} onchange={(v) => (mode = v)} />
        {#if mode === 'diff'}<Segmented size="sm" value={split ? 'split' : 'unified'} options={[{ value: 'unified', label: 'unified' }, { value: 'split', label: 'side by side' }]} onchange={(v) => (split = v === 'split')} />{/if}
        {#if edited}<span class="sm warnc">edited — your version will be applied</span>{/if}
      </div>
    {:else}
      <div class="bar"><Segmented size="sm" value={split ? 'split' : 'unified'} options={[{ value: 'unified', label: 'unified' }, { value: 'split', label: 'side by side' }]} onchange={(v) => (split = v === 'split')} /></div>
    {/if}
    {#if mode === 'edit' && pending}
      <div class="ed"><Textarea bind:value={text} rows={12} autosize maxRows={22} /></div>
      <div class="sm mute lbl">Your version against the current one</div>
      <Diff before={p.base} after={text} {split} />
    {:else}
      <Diff before={p.base} after={p.proposal} {split} />
    {/if}
    {#if p.kind !== 'soul'}<div class="sm mute hint">One {p.kind === 'tools' ? 'tool' : 'trait'} per line.</div>{/if}
  {/if}
  {#snippet footer()}
    {#if pending}
      <Button variant="ghost" disabled={busy} onclick={() => decide(false)}>Reject</Button>
      <Button variant="primary" loading={busy} onclick={() => decide(true)}>{edited ? 'Apply my version' : 'Apply'}</Button>
    {:else}<Button onclick={onclose}>Close</Button>{/if}
  {/snippet}
</Modal>

<style>
  .meta { display: flex; align-items: center; gap: 10px; margin-bottom: 6px; }
  .add { color: var(--fg-hi); } .del { color: var(--err-hi); } .warnc { color: var(--warn); }
  .why { color: var(--fg-dim); line-height: 1.5; margin-bottom: 8px; }
  .why .k { color: var(--fg-mute); text-transform: uppercase; letter-spacing: 0.08em; font-size: 10px; margin-right: 6px; }
  .warn { border: 1px solid var(--attn-dim); background: var(--attn-bg); color: var(--attn-hi); padding: 5px 9px; margin-bottom: 8px; line-height: 1.45; font-size: var(--fs-sm); }
  .bar { display: flex; align-items: center; gap: 12px; margin-bottom: 8px; }
  .ed { margin-bottom: 8px; }
  .lbl, .hint { margin: 6px 0; }
</style>
