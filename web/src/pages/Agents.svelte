<script>
  // Agents on a <canvas>, laid out by a small force simulation: Atlas fixed at the centre, everyone else
  // repelled apart (so neighbours never overlap), gently pulled toward their own group's centroid (so a
  // group still reads as a cluster) and toward a target distance from Atlas by role (staff inner, each
  // specialist group a little further out — no fixed rings or angles, so nothing has to "cross" anything
  // else). Dragging pins a node under the pointer; on release the simulation takes it from wherever it was
  // dropped. Active agents glow and their delegation lines light up; requests between colleagues arc with a
  // travelling dot.
  import { S, activeRuns, reopenOnboarding, iconOf, call, listen, toast } from '../lib/store.svelte.js';
  import Button from '../lib/ui/Button.svelte';
  import Glyph from '../lib/ui/Glyph.svelte';
  import Icon from '../lib/ui/Icon.svelte';
  import Input from '../lib/ui/Input.svelte';
  import Segmented from '../lib/ui/Segmented.svelte';
  import Badge from '../lib/ui/Badge.svelte';
  import Led from '../lib/ui/Led.svelte';
  import Empty from '../lib/ui/Empty.svelte';

  let view = $state('graph'); // the force graph is unreadable on a phone
  let filter = $state('');
  let w = $state(700), h = $state(480);
  let canvas = $state();
  let tip = $state(null); // {text, x, y}

  const agents = $derived(S.agents);
  const runs = $derived(activeRuns());
  const activeNames = $derived(new Set(runs.map((r) => r.agent)));
  const match = (a) => !filter || `${a.name} ${a.group} ${a.description} ${(a.traits || []).join(' ')}`.toLowerCase().includes(filter.toLowerCase());
  function newAgent() { S.selectedAgent = 'new'; }

  // activity heat: call volume over the last 24h, so a heavily-used agent still reads as such at rest —
  // distinct from the live glow, which only says "is it running this instant".
  let activity = $state({});
  async function loadActivity() { activity = (await call('agents.activity', {}, { quiet: true })) || {}; }
  $effect(() => { loadActivity(); const i = setInterval(loadActivity, 60000); return () => clearInterval(i); });
  const maxActivity = $derived(Math.max(1, ...Object.values(activity)));
  const heatOf = (name) => Math.min(1, (activity[name] || 0) / maxActivity);

  // agents proposed for a newly connected MCP server, waiting on the user's review
  let suggestions = $state([]);
  async function loadSuggestions() { suggestions = (await call('mcp.suggestions', {}, { quiet: true })) || []; }
  $effect(() => { loadSuggestions(); });
  $effect(() => listen('mcp.suggestion', loadSuggestions));
  async function acceptSuggestion(s) {
    const id = await call('mcp.suggestion_apply', { id: s.id });
    if (id) { toast(`${s.draft.name} created`); suggestions = suggestions.filter((x) => x.id !== s.id); S.refresh++; }
  }
  async function dismissSuggestion(s) {
    if (await call('mcp.suggestion_dismiss', { id: s.id })) suggestions = suggestions.filter((x) => x.id !== s.id);
  }

  // ── scene (plain state, read by the draw loop) ──
  let nodes = []; // {id, a, x, y, vx, vy, orbit, pinned}
  let orbits = []; // {key, label, k (0..1 of the available radius) — the group's target distance from Atlas}
  let hoverId = null, gesture = null, colors = {};
  const reduced = typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches;

  // rebuild group assignment whenever the roster changes; positions are kept for agents already placed
  $effect(() => {
    const list = S.agents;
    const old = new Map(nodes.map((n) => [n.id, n]));
    const groups = [...new Set(list.filter((a) => a.role === 'worker').map((a) => a.group))].sort();
    orbits = [];
    const hasStaff = list.some((a) => a.role === 'maint');
    if (hasStaff) orbits.push({ key: 'staff', label: 'staff', k: groups.length ? 0.36 : 0.62 });
    groups.forEach((g, i) => orbits.push({ key: 'g:' + g, label: g, k: groups.length === 1 ? 0.78 : 0.58 + (0.36 * i) / (groups.length - 1) }));
    const byOrbit = {};
    const { cx, cy, ex, ey } = spec();
    nodes = list.map((a) => {
      const key = a.role === 'entry' ? null : a.role === 'maint' ? 'staff' : 'g:' + a.group;
      const n = old.get(a.id) || { id: a.id, x: cx, y: cy, vx: 0, vy: 0, pinned: false, isNew: true };
      n.a = a; n.orbit = key;
      if (key) (byOrbit[key] ||= []).push(n);
      return n;
    });
    // seed a spread-out starting spot for brand-new nodes only — the simulation takes it from there, this
    // just avoids everyone spawning stacked on top of Atlas and fighting their way apart on frame one
    for (const [key, ns] of Object.entries(byOrbit)) {
      const o = orbits.find((x) => x.key === key);
      ns.forEach((n, i) => {
        if (!n.isNew) return;
        n.isNew = false;
        const ang = (i / ns.length) * Math.PI * 2 + key.length;
        n.x = cx + Math.cos(ang) * (o?.k ?? 0.6) * ex;
        n.y = cy + Math.sin(ang) * (o?.k ?? 0.6) * ey;
      });
    }
  });

  const spec = () => { const cx = w / 2, cy = h / 2, mx = w < 560 ? 26 : 70, my = w < 560 ? 30 : 92; return { cx, cy, ex: Math.max(80, w / 2 - mx), ey: Math.max(80, h / 2 - my) }; };
  const orbitOf = (key) => orbits.find((o) => o.key === key);
  // small by default so the orbits stay readable; the hovered agent swells (eased per node in draw())
  // phone-sized canvas: smaller nodes, and names only for the agent you touch (see draw)
  const compact = () => w < 560;
  const BASE = (a) => (compact() ? (a.role === 'entry' ? 13 : 8) : a.role === 'entry' ? 19 : 12);
  const R = (n) => BASE(n.a) * (n.k || 1);
  const nodeByName = (name) => nodes.find((n) => n.a.name === name);

  function resolveColors() {
    const cs = getComputedStyle(canvas);
    const g = (v) => cs.getPropertyValue(v).trim() || '#888';
    colors = { bg: g('--bg'), bg2: g('--bg-2'), bg4: g('--bg-4'), fg: g('--fg'), hi: g('--fg-hi'), dim: g('--fg-dim'), mute: g('--fg-mute'), line: g('--line-2'), accent: g('--accent'), accentHi: g('--accent-hi'), attn: g('--attn'), err: g('--err'), off: g('--fg-faint'), maint: g('--accent-dim'), disabled: g('--fg-mute') };
  }
  const nodeColor = (a) => (!a.enabled ? colors.off : a.role === 'entry' ? colors.hi : a.role === 'maint' ? colors.accent : colors.fg);

  // force constants: REPEL keeps neighbours from overlapping — nodes only ever push each other apart, never
  // pull together — RADIAL is a soft spring toward the group's target distance from Atlas (this is what keeps
  // a group loosely in the same band, without any node attracting another), DAMP settles the motion instead
  // of letting it oscillate, MAXV caps how fast a node can catch up (so a roster change eases in rather than
  // snapping).
  const REPEL = 2.6, RADIAL = 0.02, DAMP = 0.82, MAXV = 9;
  function place() {
    const { cx, cy, ex, ey } = spec();
    const atlas = nodes.find((n) => n.a.role === 'entry');
    if (atlas && !atlas.pinned) { atlas.x = cx; atlas.y = cy; atlas.vx = 0; atlas.vy = 0; }
    if (reduced) return; // prefers-reduced-motion: settle once via CSS-free snap, no continuous simulation
    for (const n of nodes) {
      if (n.a.role === 'entry' || n.pinned) { n.vx = 0; n.vy = 0; continue; }
      let fx = 0, fy = 0;
      for (const m of nodes) {
        if (m === n) continue;
        let dx = n.x - m.x, dy = n.y - m.y, d = Math.hypot(dx, dy);
        const minD = R(n) + R(m) + 22;
        if (d < 0.01) { dx = Math.random() - 0.5; dy = Math.random() - 0.5; d = Math.hypot(dx, dy); }
        if (d < minD) { const f = ((minD - d) / minD) * REPEL; fx += (dx / d) * f; fy += (dy / d) * f; }
      }
      const o = orbitOf(n.orbit), k = o ? o.k : 0.6;
      const sx = (n.x - cx) / ex, sy = (n.y - cy) / ey, sd = Math.hypot(sx, sy) || 0.0001, err = k - sd;
      fx += (sx / sd) * err * ex * RADIAL; fy += (sy / sd) * err * ey * RADIAL;
      n.vx = (n.vx + fx) * DAMP; n.vy = (n.vy + fy) * DAMP;
      const sp = Math.hypot(n.vx, n.vy);
      if (sp > MAXV) { n.vx = (n.vx / sp) * MAXV; n.vy = (n.vy / sp) * MAXV; }
      n.x += n.vx; n.y += n.vy;
    }
  }
  const arcPath = (ctx, a, b) => {
    const mx = (a.x + b.x) / 2, my = (a.y + b.y) / 2, dx = b.x - a.x, dy = b.y - a.y, len = Math.hypot(dx, dy) || 1, k = Math.min(60, len * 0.28);
    const qx = mx - (dy / len) * k, qy = my + (dx / len) * k;
    ctx.beginPath(); ctx.moveTo(a.x, a.y); ctx.quadraticCurveTo(qx, qy, b.x, b.y);
    return { qx, qy };
  };
  const bez = (a, q, b, u) => ({ x: (1 - u) * (1 - u) * a.x + 2 * (1 - u) * u * q.qx + u * u * b.x, y: (1 - u) * (1 - u) * a.y + 2 * (1 - u) * u * q.qy + u * u * b.y });

  let raf;
  function draw(t) {
    raf = requestAnimationFrame(draw);
    if (!canvas || view !== 'graph') return;
    const ctx = canvas.getContext('2d'), dpr = window.devicePixelRatio || 1;
    if (canvas.width !== Math.round(w * dpr) || canvas.height !== Math.round(h * dpr)) { canvas.width = Math.round(w * dpr); canvas.height = Math.round(h * dpr); }
    place();
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, w, h);
    const atlas = nodes.find((n) => n.a.role === 'entry');

    const byRun = Object.fromEntries(runs.map((r) => [r.id, r.agent]));
    // spokes from Atlas; live ones (delegation in progress) run bright and dashed
    if (atlas) for (const n of nodes) {
      if (n === atlas) continue;
      const live = runs.some((r) => r.agent === n.a.name && ((r.parent_run && byRun[r.parent_run] === 'Atlas') || (r.depth === 1 && !r.parent_run)));
      // a spoke this agent is actually delegated to a lot reads thicker and more solid even when quiet —
      // "how much does Atlas actually route here" instead of every spoke looking equally important.
      const spokeHeat = n.a.role === 'maint' ? 0 : heatOf(n.a.name);
      ctx.beginPath(); ctx.moveTo(atlas.x, atlas.y); ctx.lineTo(n.x, n.y);
      ctx.lineWidth = live ? 2 : 1 + spokeHeat * 0.8; ctx.strokeStyle = live ? colors.fg : n.a.role === 'maint' ? colors.maint : colors.line;
      ctx.globalAlpha = live ? 1 : 0.4 + spokeHeat * 0.4; ctx.setLineDash(live ? [6, 4] : []); ctx.lineDashOffset = live ? -t / 40 : 0;
      if (live) { ctx.shadowColor = colors.fg; ctx.shadowBlur = 6; }
      ctx.stroke(); ctx.shadowBlur = 0;
    }
    ctx.setLineDash([]); ctx.globalAlpha = 1;

    // agents blocked on the user: steady red arc to Atlas
    for (const x of S.asks) { const n = nodeByName(x.agent); if (!n || !atlas || n === atlas) continue;
      arcPath(ctx, n, atlas); ctx.strokeStyle = colors.err; ctx.lineWidth = 1.8; ctx.globalAlpha = 0.55 + 0.45 * Math.sin(t / 220); ctx.shadowColor = colors.err; ctx.shadowBlur = 5; ctx.stroke(); ctx.shadowBlur = 0; ctx.globalAlpha = 1; }

    // requests between colleagues: amber arcs, dot travelling toward the one asked; lingering ones fade
    const asks = [];
    for (const r of runs) { if (r.kind !== 'ask' || r.done || !byRun[r.parent_run]) continue; const a = nodeByName(byRun[r.parent_run]), b = nodeByName(r.agent); if (a && b) asks.push({ a, b, live: true, ok: true }); }
    for (const tr of S.askTrail) { const a = nodeByName(tr.from), b = nodeByName(tr.to); if (a && b) asks.push({ a, b, live: false, ok: tr.ok }); }
    for (const k of asks) {
      const q = arcPath(ctx, k.a, k.b);
      ctx.strokeStyle = k.live || k.ok ? colors.attn : colors.err; ctx.lineWidth = 1.6; ctx.setLineDash(k.live ? [2, 5] : []); ctx.lineDashOffset = -t / 50; ctx.globalAlpha = k.live ? 0.95 : 0.6;
      ctx.shadowColor = ctx.strokeStyle; ctx.shadowBlur = 4; ctx.stroke(); ctx.shadowBlur = 0; ctx.setLineDash([]); ctx.globalAlpha = 1;
      if (k.live) { const p = bez(k.a, q, k.b, (t / 1500) % 1); ctx.fillStyle = colors.attn; ctx.beginPath(); ctx.arc(p.x, p.y, 3.4, 0, Math.PI * 2); ctx.fill(); }
    }

    // agents
    for (const n of nodes) {
      const a = n.a, col = nodeColor(a), active = activeNames.has(a.name), blocked = S.asks.some((x) => x.agent === a.name);
      const selected = S.selectedAgent === a.id, hovered = hoverId === n.id;
      const heat = a.role === 'entry' ? 0 : heatOf(a.name); // Atlas is always busy by construction; heat would say nothing
      const goal = hovered ? (a.role === 'entry' ? 1.35 : 1.7) : selected ? 1.25 : 1;
      n.k = (n.k || 1) + (goal - (n.k || 1)) * 0.2;
      const r = R(n);
      ctx.save(); ctx.translate(n.x, n.y); ctx.globalAlpha = match(a) ? 1 : 0.2;
      if (selected || hovered) { ctx.beginPath(); ctx.arc(0, 0, r + 5, 0, Math.PI * 2); ctx.strokeStyle = col; ctx.lineWidth = 0.6; ctx.globalAlpha *= 0.55; ctx.stroke(); ctx.globalAlpha = match(a) ? 1 : 0.2; }
      ctx.beginPath(); ctx.arc(0, 0, r, 0, Math.PI * 2);
      ctx.fillStyle = active ? `color-mix(in srgb, ${col} ${20 + 10 * Math.sin(t / 160)}%, ${colors.bg})`
        : selected || hovered ? colors.bg4
        : heat > 0.08 ? `color-mix(in srgb, ${col} ${Math.round(heat * 16)}%, ${colors.bg2})`
        : colors.bg2;
      if (active || selected) { ctx.shadowColor = col; ctx.shadowBlur = 12; }
      else if (heat > 0.15) { ctx.shadowColor = col; ctx.shadowBlur = heat * 7; } // a quiet, steady glow — not pulsing like "active" — for an agent that's been busy lately but isn't this instant
      ctx.fill(); ctx.shadowBlur = 0;
      ctx.strokeStyle = blocked ? colors.err : col; ctx.lineWidth = blocked ? 1.6 + 1.4 * (0.5 + 0.5 * Math.sin(t / 220)) : selected ? 2.6 : 1.6; ctx.stroke();
      ctx.fillStyle = col; ctx.font = `900 ${r * 0.9}px FA`; ctx.textAlign = 'center'; ctx.textBaseline = 'middle'; ctx.fillText(iconOf(a.name), 0, 1);
      if (blocked) { ctx.fillStyle = colors.err; ctx.font = '800 13px monospace'; ctx.fillText('!', 0, -r - 8); }
      ctx.textBaseline = 'alphabetic';
      if (compact() && !(hovered || selected || active || blocked || a.role === 'entry')) { ctx.restore(); continue; }
      ctx.font = '700 11px monospace'; ctx.fillStyle = !a.enabled ? colors.disabled : a.role === 'maint' ? colors.accentHi : colors.hi; ctx.fillText(a.name, 0, r + 14);
      ctx.font = '9px monospace'; ctx.fillStyle = colors.mute; ctx.fillText((a.probation ? 'on probation' : a.role === 'entry' ? 'entry' : a.role === 'maint' ? 'staff' : a.group).toUpperCase(), 0, r + 25);
      ctx.restore();
    }
  }

  // ── interaction ──
  const local = (e) => { const b = canvas.getBoundingClientRect(); return { x: e.clientX - b.left, y: e.clientY - b.top }; };
  const hit = (x, y) => { for (let i = nodes.length - 1; i >= 0; i--) { const n = nodes[i]; if (Math.hypot(n.x - x, n.y - y) <= Math.max(R(n), BASE(n.a) * 1.4) + 4) return n; } return null; };
  function onDown(e) { const p = local(e), n = hit(p.x, p.y); if (!n) { S.selectedAgent = null; return; } hoverId = n.id; gesture = { n, sx: p.x, sy: p.y, moved: false }; canvas.setPointerCapture?.(e.pointerId); }
  function onMove(e) {
    const p = local(e);
    if (gesture) {
      if (!gesture.moved && Math.hypot(p.x - gesture.sx, p.y - gesture.sy) < 5) return;
      gesture.moved = true; gesture.n.pinned = true; gesture.n.x = p.x; gesture.n.y = p.y; tip = null; return;
    }
    const n = hit(p.x, p.y); hoverId = n?.id ?? null; canvas.style.cursor = n ? 'grab' : 'default';
    tip = n ? { text: `${n.a.name} — ${n.a.description} (${n.a.tools.length} tools)`, x: p.x, y: p.y } : null;
  }
  function onUp() {
    if (!gesture) return;
    const { n, moved } = gesture; gesture = null; hoverId = null;
    if (!moved) { S.selectedAgent = n.a.id; return; }
    // released: the simulation takes over from wherever it was dropped, gently repelling neighbours apart
    // if it landed close to any of them rather than a hard reset back onto a ring
    n.pinned = false;
  }

  $effect(() => {
    if (!canvas) return;
    resolveColors();
    raf = requestAnimationFrame(draw);
    return () => cancelAnimationFrame(raf);
  });
</script>

<svelte:window onpointermove={(e) => gesture && onMove(e)} onpointerup={onUp} />

<div class="pg">
  <div class="bar">
    <Segmented bind:value={view} options={[{ value: 'graph', label: 'Graph' }, { value: 'list', label: 'List' }]} />
    <div class="f"><Input bind:value={filter} size="sm" placeholder="filter agents / traits…" /></div>
    <span class="grow"></span>
    <span class="sm dim">{agents.length} agents · {activeNames.size} active</span>
    <Button size="sm" onclick={newAgent}><Icon name="plus" size={11} /> New agent</Button>
    <Button size="sm" variant="accent" onclick={reopenOnboarding}><Icon name="refresh" size={11} /> Regenerate profiles</Button>
  </div>

  {#if suggestions.length}
    <div class="sugg">
      {#each suggestions as s (s.id)}
        <div class="srow">
          <Icon name="plus" size={12} />
          <span class="sm">New MCP server <b class="hi">{s.server}</b> connected — suggested agent <b class="hi">{s.draft.name}</b>: {s.draft.description}</span>
          <span class="grow"></span>
          <Button size="sm" variant="primary" onclick={() => acceptSuggestion(s)}>Create</Button>
          <Button size="sm" variant="ghost" onclick={() => dismissSuggestion(s)}>Dismiss</Button>
        </div>
      {/each}
    </div>
  {/if}

  {#if view === 'graph'}
    <div class="graph" bind:clientWidth={w} bind:clientHeight={h}>
      <canvas bind:this={canvas} style="width:{w}px;height:{h}px" onpointerdown={onDown} onpointermove={onMove} onpointerup={onUp} onpointerleave={() => { hoverId = null; tip = null; }} aria-label="agent orbits"></canvas>
      {#if tip}<div class="tip" style="left:{tip.x + 14}px; top:{tip.y + 14}px">{tip.text}</div>{/if}
      <div class="legend">
        <span><Led state="ok" size={7} /> specialist</span><span><Led state="standby" size={7} /> staff</span><span><Led state="off" size={7} /> disabled</span>
        <span><svg width="26" height="8" class="lg"><path d="M1,4 H25" stroke="var(--attn)" stroke-width="1.6" stroke-dasharray="2 5" fill="none" /></svg> asking a colleague</span>
        <span><svg width="26" height="8" class="lg"><path d="M1,4 H25" stroke="var(--err)" stroke-width="1.8" fill="none" /></svg> needs you</span>
        <span class="mute">agents settle apart to avoid overlap · drag to move · click to edit · a steady glow at rest = used a lot in the last 24h</span>
      </div>
      {#if !agents.length}<div class="abs"><Empty>no agents yet</Empty></div>{/if}
    </div>
  {:else}
    <div class="list scroll">
      <table class="t">
        <thead><tr><th></th><th>Agent</th><th>Group</th><th>Description</th><th>Model</th><th>Tools</th><th>Soul</th></tr></thead>
        <tbody>
          {#each agents.filter(match) as a (a.id)}
            <tr class="click" class:sel={S.selectedAgent === a.id} onclick={() => (S.selectedAgent = a.id)}>
              <td><Led state={!a.enabled ? 'off' : activeNames.has(a.name) ? 'ok' : a.role === 'maint' ? 'standby' : 'ok'} pulse={activeNames.has(a.name)} size={8} /></td>
              <td class="hi"><span class="gl"><Glyph name={a.name} /></span> {a.name} {#if a.role === 'entry'}<Badge tone="ok">entry</Badge>{:else if a.system}<Badge tone="accent">staff</Badge>{/if}</td>
              <td class="dim">{a.group}</td>
              <td class="dim">{a.description}</td>
              <td class="mute">{a.model || 'default'}</td>
              <td class="mute">{a.tools.length}</td>
              <td class="mute">v{a.soul_version}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>

<style>
  .pg { display: flex; flex-direction: column; gap: 6px; height: 100%; min-height: 0; }
  .bar { display: flex; align-items: center; gap: 8px; flex: none; }
  .f { width: 230px; }
  @media (max-width: 820px) { .f { width: 100%; flex: 1; } }
  .sugg { display: flex; flex-direction: column; gap: 4px; flex: none; }
  .srow { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; padding: 5px 8px; border: 1px solid var(--accent-dim); background: color-mix(in srgb, var(--accent) 8%, transparent); }
  .graph { position: relative; flex: 1; min-height: 0; border: 1px solid var(--line-2); overflow: hidden; background: radial-gradient(circle at 50% 50%, var(--bg-2) 0%, var(--bg) 100%); }
  canvas { position: absolute; inset: 0; display: block; touch-action: none; }
  .tip { position: absolute; z-index: 5; max-width: 300px; padding: 5px 8px; background: var(--bg-1); border: 1px solid var(--line-2); border-radius: var(--r); color: var(--fg-hi); font-size: var(--fs-sm); pointer-events: none; box-shadow: 0 4px 14px rgba(0,0,0,0.35); }
  .lg { vertical-align: middle; }
  .legend { position: absolute; left: 8px; bottom: 6px; display: flex; gap: 12px; font-size: 10px; color: var(--fg-mute); text-transform: uppercase; letter-spacing: 0.07em; }
  .legend span { display: inline-flex; gap: 5px; align-items: center; }
  .abs { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; }
  .list { flex: 1; border: 1px solid var(--line-2); background: var(--bg-1); }
  .gl { display: inline-block; width: 1.5em; text-align: center; }
</style>
