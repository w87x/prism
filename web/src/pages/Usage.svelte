<script>
  import { untrack } from 'svelte';
  import { call, fmtTokens, ago } from '../lib/store.svelte.js';
  import Panel from '../lib/ui/Panel.svelte';
  import BarChart from '../lib/ui/BarChart.svelte';
  import Segmented from '../lib/ui/Segmented.svelte';
  import Switch from '../lib/ui/Switch.svelte';
  import Button from '../lib/ui/Button.svelte';
  import Icon from '../lib/ui/Icon.svelte';
  import Empty from '../lib/ui/Empty.svelte';

  const lsGet = (k, d) => { try { return JSON.parse(localStorage.getItem(k)) ?? d; } catch { return d; } };
  let days = $state(lsGet('prism.usageDays', 7));
  let tables = $state(lsGet('prism.usageTables', false));
  let d = $state(null);
  let deleg = $state(null);
  let busy = $state(false);

  async function load() {
    busy = true;
    const [r, dg] = await Promise.all([call('ops.dashboard', { days }, { quiet: true }), call('ops.delegation', { days }, { quiet: true })]);
    busy = false;
    if (r) d = r;
    if (dg) deleg = dg;
  }
  // effects must not read what they write: load untracked, refresh on a timer
  $effect(() => {
    days;
    try { localStorage.setItem('prism.usageDays', JSON.stringify(days)); } catch {}
    untrack(load);
    const t = setInterval(() => untrack(load), 30000);
    return () => clearInterval(t);
  });
  $effect(() => { try { localStorage.setItem('prism.usageTables', JSON.stringify(tables)); } catch {} });

  const n = (v) => Math.round(v || 0).toLocaleString();
  const secs = (ms) => (ms >= 10000 ? `${(ms / 1000).toFixed(0)} s` : ms >= 1000 ? `${(ms / 1000).toFixed(1)} s` : `${Math.round(ms || 0)} ms`);
  const pct = (a, b) => (b ? `${((100 * a) / b).toFixed(a && a / b < 0.1 ? 1 : 0)}%` : '0%');
  const dayLabel = (l) => (l.includes('T') ? l.slice(11) : new Date(l + 'T12:00:00').toLocaleDateString([], { month: 'short', day: 'numeric' }));

  const labels = $derived((d?.buckets || []).map((b) => b.label));
  const col = (f) => (d?.buckets || []).map(f);
  const empty = $derived(d && !d.totals.calls && !d.totals.tasks_done && !d.totals.tasks_failed);
  const t = $derived(d?.totals);
  const hint = $derived.by(() => {
    if (!t?.calls) return '';
    if (t.wait_p95_ms > 8000) return `Calls waited a long time for a model slot (p95 ${secs(t.wait_p95_ms)}). Agents are queuing behind each other: fewer parallel agents, a faster model, or a different concurrency (Settings → Context) may help.`;
    if (t.errors / t.calls > 0.1) return `${pct(t.errors, t.calls)} of model calls failed — see the recent errors below.`;
    return '';
  });
</script>

<div class="pg usage">
  <div class="filters">
    <Segmented size="sm" value={days} options={[{ value: 1, label: 'last 24 h' }, { value: 7, label: '7 days' }, { value: 30, label: '30 days' }]} onchange={(v) => (days = v)} />
    <Switch bind:checked={tables} label="tables instead of charts" />
    <span class="grow"></span>
    {#if t?.recording_since}<span class="sm mute">recording since {new Date(t.recording_since).toLocaleDateString([], { month: 'short', day: 'numeric' })}</span>{/if}
    <Button size="sm" variant="ghost" onclick={load} loading={busy} title="Refresh"><Icon name="refresh" size={11} /></Button>
  </div>

  <div class="body scroll">
    {#if !d}
      <Empty>loading…</Empty>
    {:else if empty}
      <Empty>nothing recorded in this period yet — model and tool calls are counted from now on</Empty>
    {:else}
      {#if hint}<div class="hint">{hint}</div>{/if}
      <div class="tiles">
        <div class="tile"><span class="k">Model calls</span><b>{n(t.calls)}</b><span class="s">{t.errors ? `${n(t.errors)} failed (${pct(t.errors, t.calls)})` : 'none failed'}</span></div>
        <div class="tile"><span class="k">Tokens</span><b>{fmtTokens(t.tokens_in + t.tokens_out)}</b><span class="s">{fmtTokens(t.tokens_in)} in · {fmtTokens(t.tokens_out)} out</span></div>
        <div class="tile"><span class="k">Speed</span><b>{t.tok_per_s ? t.tok_per_s.toFixed(1) : '—'}<small> tok/s</small></b><span class="s">generation, successful calls</span></div>
        <div class="tile"><span class="k">Latency</span><b>{secs(t.p50_ms)}</b><span class="s">median · p95 {secs(t.p95_ms)}</span></div>
        <div class="tile"><span class="k">Queue wait</span><b>{secs(t.wait_avg_ms)}</b><span class="s">avg for a model slot · p95 {secs(t.wait_p95_ms)}</span></div>
        <div class="tile"><span class="k">Tasks</span><b>{n(t.tasks_done)}<small> done</small></b><span class="s">{t.tasks_failed ? `${n(t.tasks_failed)} failed` : 'none failed'}</span></div>
      </div>

      <div class="charts">
        <Panel title="Tokens per {d.hourly ? 'hour' : 'day'}">
          <BarChart {labels} label={dayLabel} table={tables} ariaLabel="Tokens per {d.hourly ? 'hour' : 'day'}, input and output" format={fmtTokens}
            series={[{ name: 'input', color: 'var(--c-in)', values: col((b) => b.tokens_in) }, { name: 'output', color: 'var(--c-out)', values: col((b) => b.tokens_out) }]} />
        </Panel>
        <Panel title="Model calls per {d.hourly ? 'hour' : 'day'}">
          <BarChart {labels} label={dayLabel} table={tables} ariaLabel="Model calls per {d.hourly ? 'hour' : 'day'}, successful and failed"
            series={[{ name: 'succeeded', color: 'var(--c-in)', values: col((b) => b.calls - b.errors) }, { name: 'failed', color: 'var(--c-bad)', values: col((b) => b.errors) }]} />
        </Panel>
        <Panel title="Tasks finished per {d.hourly ? 'hour' : 'day'}">
          <BarChart {labels} label={dayLabel} table={tables} ariaLabel="Tasks finished per {d.hourly ? 'hour' : 'day'}, done and failed"
            series={[{ name: 'done', color: 'var(--c-ok)', values: col((b) => b.tasks_done) }, { name: 'failed', color: 'var(--c-bad)', values: col((b) => b.tasks_failed) }]} />
        </Panel>
      </div>

      <div class="tables">
        <Panel title="By model" flush>
          <table class="t">
            <thead><tr><th>Model</th><th class="r">Calls</th><th class="r">Tokens</th><th class="r">Avg</th><th class="r">p95</th><th class="r">tok/s</th><th class="r">Failed</th></tr></thead>
            <tbody>{#each d.models as m}<tr><td class="hi">{m.model}</td><td class="r">{n(m.calls)}</td><td class="r">{fmtTokens(m.tokens_in + m.tokens_out)}</td><td class="r">{secs(m.avg_ms)}</td><td class="r">{secs(m.p95_ms)}</td><td class="r">{m.tok_per_s ? m.tok_per_s.toFixed(1) : '—'}</td><td class="r" class:err={m.errors}>{m.errors ? n(m.errors) : '—'}</td></tr>
              {:else}<tr><td colspan="7"><Empty>no model calls</Empty></td></tr>{/each}</tbody>
          </table>
        </Panel>
        <Panel title="By agent" flush>
          <table class="t">
            <thead><tr><th>Agent</th><th class="r">Calls</th><th class="r">Tokens</th><th class="r">Tasks done</th><th class="r">Tasks failed</th><th class="r">Call errors</th></tr></thead>
            <tbody>{#each d.agents as a}<tr><td class="hi">{a.agent}</td><td class="r">{n(a.calls)}</td><td class="r">{fmtTokens(a.tokens_in + a.tokens_out)}</td><td class="r">{a.tasks_done || '—'}</td><td class="r" class:err={a.tasks_failed}>{a.tasks_failed || '—'}</td><td class="r" class:err={a.errors}>{a.errors || '—'}</td></tr>
              {:else}<tr><td colspan="6"><Empty>no agent activity</Empty></td></tr>{/each}</tbody>
          </table>
        </Panel>
        <Panel title="Tools" flush>
          <table class="t">
            <thead><tr><th>Tool</th><th class="r">Calls</th><th class="r">Failed</th><th class="r">Avg</th><th class="r">p95</th></tr></thead>
            <tbody>{#each d.tools as x}<tr><td class="hi">{x.tool}</td><td class="r">{n(x.calls)}</td><td class="r" class:err={x.errors}>{x.errors ? `${n(x.errors)} (${pct(x.errors, x.calls)})` : '—'}</td><td class="r">{secs(x.avg_ms)}</td><td class="r">{secs(x.p95_ms)}</td></tr>
              {:else}<tr><td colspan="5"><Empty>no tool calls</Empty></td></tr>{/each}</tbody>
          </table>
        </Panel>
      </div>

      <Panel title="Delegation overhead" flush>
        {#if !deleg || !deleg.roots}
          <Empty>not enough delegated requests recorded yet in this period</Empty>
        {:else}
          <div class="delegsum">
            {#if deleg.delegated_runs}
              Across {n(deleg.delegated_runs)} delegated request{deleg.delegated_runs === 1 ? '' : 's'} (of {n(deleg.roots)} total), the entry agent's own routing/synthesis calls
              took <b class:err={deleg.child_ms ? deleg.root_ms / (deleg.root_ms + deleg.child_ms) > 0.4 : false}>{pct(deleg.root_ms, deleg.root_ms + deleg.child_ms)}</b>
              of the combined LLM time ({secs(deleg.root_ms)} of {secs(deleg.root_ms + deleg.child_ms)}); the specialist doing the actual work took the rest.
            {:else}
              No request in this period was delegated to a specialist — everything was answered directly.
            {/if}
          </div>
          {#if deleg.chains?.some((c) => c.delegated)}
            <table class="t">
              <thead><tr><th>Entry agent</th><th class="r">Its own time</th><th class="r">Specialist time</th><th class="r">Overhead</th><th class="r">Specialist calls</th></tr></thead>
              <tbody>{#each deleg.chains.filter((c) => c.delegated) as c}<tr><td class="hi">{c.root_agent}</td><td class="r">{secs(c.root_ms)}</td><td class="r">{secs(c.child_ms)}</td><td class="r" class:err={c.root_ms / (c.root_ms + c.child_ms) > 0.4}>{pct(c.root_ms, c.root_ms + c.child_ms)}</td><td class="r">{c.child_calls}</td></tr>
                {/each}</tbody>
            </table>
          {/if}
        {/if}
      </Panel>

      {#if d.recent_errors.length}
        <Panel title="Recent errors" flush>
          <table class="t">
            <thead><tr><th style="width:56px">When</th><th style="width:48px">Kind</th><th>What</th><th>Agent</th><th>Error</th></tr></thead>
            <tbody>{#each d.recent_errors as e}<tr><td class="mute sm nowrap">{ago(e.ts)}</td><td class="dim">{e.kind}</td><td class="hi">{e.what}</td><td class="dim">{e.agent || '—'}</td><td class="err sm">{e.error}</td></tr>{/each}</tbody>
          </table>
        </Panel>
      {/if}
    {/if}
  </div>
</div>

<style>
  /* categorical slots 1–3, dark-mode steps (validated ΔE ≥ 8 for colour-vision deficiency); errors reuse the status red */
  .usage { --c-in: #3987e5; --c-out: #d95926; --c-ok: #199e70; --c-bad: var(--err); }
  .pg { display: flex; flex-direction: column; gap: 8px; height: 100%; min-height: 0; }
  .filters { display: flex; align-items: center; gap: 16px; flex: none; padding: 4px 8px; border: 1px solid var(--line); background: var(--bg-1); }
  .body { flex: 1; min-height: 0; display: flex; flex-direction: column; gap: 8px; }
  .hint { border: 1px solid var(--attn-dim); background: var(--attn-bg); color: var(--attn-hi); padding: 6px 10px; line-height: 1.45; font-size: var(--fs-sm); }
  .tiles { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 8px; }
  .tile { border: 1px solid var(--line); background: var(--bg-1); padding: 7px 10px; display: flex; flex-direction: column; gap: 1px; }
  .tile .k { font-size: 10px; text-transform: uppercase; letter-spacing: 0.1em; color: var(--fg-mute); }
  .tile b { font-size: 20px; font-weight: 500; color: var(--fg-hi); }
  .tile b small { font-size: 11px; color: var(--fg-dim); font-weight: 400; }
  .tile .s { font-size: var(--fs-sm); color: var(--fg-dim); }
  .charts { display: grid; grid-template-columns: repeat(auto-fit, minmax(340px, 1fr)); gap: 8px; align-items: start; }
  .tables { display: grid; grid-template-columns: repeat(auto-fit, minmax(520px, 1fr)); gap: 8px; align-items: start; }
  .r { text-align: right; }
  .err { color: var(--err); }
  .delegsum { padding: 8px 10px; font-size: var(--fs-sm); color: var(--fg-dim); line-height: 1.5; }
  .delegsum b { color: var(--fg-hi); }
</style>
