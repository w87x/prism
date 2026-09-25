<script>
  // The unified memory graph on a <canvas> (SVG made big graphs crawl: one DOM node per dot/edge/label).
  // Facts (circles, diamonds for conclusions) and entities (people, orgs, products, places, events,
  // concepts) share one canvas, tied by fact links, entity relations and "mentions" edges.
  // Following Hindsight's constellation: edges are only drawn strongly for the hovered/selected node (the
  // rest is a faint haze), labels are placed on a coarse grid so they never pile up, details come from a
  // hover tooltip. Wheel zooms, dragging the background pans, dragging a node moves it, click selects,
  // double-click opens a fact.
  import { untrack } from 'svelte';
  import { call } from './store.svelte.js';
  import Button from './ui/Button.svelte';
  import Empty from './ui/Empty.svelte';

  let { bank = 0, history = false, onopen, onresearch } = $props();
  let W = $state(900), H = $state(600);
  let g = $state(null);
  let hl = $state(''); // legend filter: key of the legend entry whose nodes are highlighted
  const hlMatch = (n) => hl.startsWith('e:') ? n.type === 'entity' && n.kind === hl.slice(2)
    : hl === 'conclusion' ? n.type === 'fact' && n.kind === 'conclusion'
    : hl === 'unverified' ? n.type === 'fact' && n.confidence < 0.5
    : hl.startsWith('f:') ? n.type === 'fact' && n.bank_kind === hl.slice(2) : false;
  const toggleHl = (k) => { hl = hl === k ? '' : k; };
  let layout = $state('force'); // force | bank | type | rings
  let sel = $state(0); // 0 = none, else a prefixed id ("f12" fact, "e12" entity)
  let entFacts = $state([]);
  let busy = $state(false);
  let hoverNode = $state(null); // {id, text, x, y} for the tooltip
  let wrapEl = $state(), canvasEl = $state(), boxEl = $state();

  // non-reactive scene state, read by the draw loop every frame
  let pos = {}; // id → {x, y}
  let view = { x: 0, y: 0, k: 1 };
  let nodes = [], idx = new Map(), edges = [], adj = [], order = [];
  let hoverIdx = -1, gesture = null, labelMax = 99; // labelMax: label length cap of the current arrangement
  let colors = {};

  async function load() {
    busy = true;
    const r = await call('memory.full_graph', { bank_id: bank, history, limit: 140 }, { quiet: true });
    busy = false;
    if (!r) return;
    g = r;
    prepare();
    pos = settle(r.nodes, r.edges, layout);
    view = { x: 0, y: 0, k: 1 };
    sel = 0; hoverIdx = -1; hoverNode = null;
  }
  $effect(() => { bank; history; untrack(load); });
  $effect(() => { W; H; const m = layout; untrack(() => { if (g) { pos = settle(g.nodes, g.edges, m); view = { x: 0, y: 0, k: 1 }; } }); });
  $effect(() => {
    if (!sel || !String(sel).startsWith('e')) { entFacts = []; return; }
    const id = Number(sel.slice(1));
    untrack(() => call('memory.entity_facts', { id }, { quiet: true }).then((r) => (entFacts = r || [])));
  });

  function prepare() {
    nodes = g.nodes;
    idx = new Map(nodes.map((n, i) => [n.id, i]));
    adj = nodes.map(() => []);
    edges = [];
    for (const e of g.edges) {
      const a = idx.get(e.a), b = idx.get(e.b);
      if (a === undefined || b === undefined) continue;
      edges.push({ a, b, kind: e.kind, label: e.label, w: e.weight || 0.5, retired: !!e.retired });
      adj[a].push(edges.length - 1); adj[b].push(edges.length - 1);
    }
    // label priority: conclusions, then well-connected / frequently mentioned nodes
    const score = (n) => (n.type === 'fact' && n.kind === 'conclusion' ? 100 : 0) + (n.type === 'entity' ? n.mentions + 2 : n.rank);
    order = nodes.map((_, i) => i).sort((a, b) => score(nodes[b]) - score(nodes[a]));
  }

  const rnd = (i) => { const s = Math.sin(i * 12.9898) * 43758.5453; return s - Math.floor(s); };
  const groupOf = (n, mode) => mode === 'bank' ? (n.type === 'entity' ? 'entities' : n.bank || '?')
    : n.type === 'entity' ? 'e:' + n.kind : n.kind === 'conclusion' ? 'conclusion' : 'f:' + n.bank_kind;
  const nodeScore = (n) => (n.type === 'fact' && n.kind === 'conclusion' ? 100 : 0) + (n.type === 'entity' ? n.mentions + 2 : n.rank || 0);
  // deterministic arrangements: 'type' = one column per kind of node, most important first; 'rings' = the
  // most important nodes in the middle, the rest on ever wider rings
  function arrange(ns, mode) {
    const out = {};
    if (mode === 'type') {
      const groups = new Map();
      ns.forEach((n) => { const k = groupOf(n, 'type'); (groups.get(k) || groups.set(k, []).get(k)).push(n); });
      const cols = [...groups.entries()].sort((a, b) => b[1].length - a[1].length);
      const colW = (W - 80) / cols.length;
      labelMax = Math.max(6, Math.floor(colW / 6.4));
      cols.forEach(([, list], ci) => {
        list.sort((a, b) => nodeScore(b) - nodeScore(a));
        const sub = Math.max(1, Math.min(Math.ceil(list.length / Math.max(1, Math.floor((H - 80) / 20)) ), Math.floor(colW / 200))), per = Math.ceil(list.length / sub), rowH = Math.min(28, (H - 80) / per), sw = colW / sub;
        list.forEach((n, i) => { out[n.id] = { x: 40 + ci * colW + (Math.floor(i / per) + 0.5) * sw, y: 40 + (i % per) * rowH + rowH / 2 }; });
      });
      return out;
    }
    const sorted = [...ns].sort((a, b) => nodeScore(b) - nodeScore(a));
    let ring = 0, placed = 0;
    while (placed < sorted.length) {
      const cap = ring === 0 ? 1 : Math.round(ring * 7), r = ring * Math.min(W, H) * 0.09;
      sorted.slice(placed, placed + cap).forEach((n, i, arr) => {
        const a = (i / arr.length) * Math.PI * 2 + ring * 0.6;
        out[n.id] = { x: W / 2 + Math.cos(a) * r * 1.5, y: H / 2 + Math.sin(a) * r };
      });
      placed += cap; ring++;
    }
    return out;
  }
  function settle(ns, es, mode = 'force') {
    labelMax = 99;
    if (mode === 'type' || mode === 'rings') return arrange(ns, mode);
    const n = ns.length;
    const anchor = ns.map(() => null);
    if (mode === 'bank') {
      const keys = [...new Set(ns.map((v) => groupOf(v, 'bank')))];
      const R = keys.length > 1 ? Math.min(W, H) * 0.32 : 0;
      ns.forEach((v, i) => { const gi = keys.indexOf(groupOf(v, 'bank')), a = (gi / keys.length) * Math.PI * 2 - Math.PI / 2; anchor[i] = { x: W / 2 + Math.cos(a) * R * 1.4, y: H / 2 + Math.sin(a) * R }; });
    }
    const ix = new Map(ns.map((v, i) => [v.id, i]));
    const x = ns.map((_, i) => W / 2 + (rnd(i * 2 + 1) - 0.5) * W * 0.6);
    const y = ns.map((_, i) => H / 2 + (rnd(i * 2 + 2) - 0.5) * H * 0.6);
    const vx = new Array(n).fill(0), vy = new Array(n).fill(0);
    const ee = es.map((e) => [ix.get(e.a), ix.get(e.b), e.weight || 0.5]).filter(([a, b]) => a !== undefined && b !== undefined);
    const iters = n > 200 ? 160 : 280;
    for (let it = 0; it < iters; it++) {
      const alpha = 1 - it / iters;
      for (let i = 0; i < n; i++) for (let j = i + 1; j < n; j++) {
        let dx = x[i] - x[j], dy = y[i] - y[j];
        const d2 = dx * dx + dy * dy + 0.5, f = (9000 * alpha) / d2, d = Math.sqrt(d2);
        dx = (dx / d) * f; dy = (dy / d) * f;
        vx[i] += dx; vy[i] += dy; vx[j] -= dx; vy[j] -= dy;
      }
      for (const [a, b, w] of ee) {
        const dx = x[b] - x[a], dy = y[b] - y[a], d = Math.sqrt(dx * dx + dy * dy) + 0.01, f = (d - 110) * 0.02 * (0.5 + w);
        vx[a] += (dx / d) * f; vy[a] += (dy / d) * f; vx[b] -= (dx / d) * f; vy[b] -= (dy / d) * f;
      }
      for (let i = 0; i < n; i++) {
        const cx0 = anchor[i] ? anchor[i].x : W / 2, cy0 = anchor[i] ? anchor[i].y : H / 2, gk = anchor[i] ? 0.01 : 0.004;
        vx[i] += (cx0 - x[i]) * gk; vy[i] += (cy0 - y[i]) * gk;
        vx[i] *= 0.82; vy[i] *= 0.82;
        x[i] = Math.min(W - 20, Math.max(20, x[i] + vx[i]));
        y[i] = Math.min(H - 20, Math.max(20, y[i] + vy[i]));
      }
    }
    const minX = Math.min(...x), maxX = Math.max(...x), minY = Math.min(...y), maxY = Math.max(...y);
    const k = Math.min((W - 200) / Math.max(maxX - minX, 1), (H - 120) / Math.max(maxY - minY, 1), 3);
    const cx = (minX + maxX) / 2, cy = (minY + maxY) / 2;
    return Object.fromEntries(ns.map((v, i) => [v.id, { x: W / 2 + (x[i] - cx) * k, y: H / 2 + (y[i] - cy) * k }]));
  }

  const node = $derived(g?.nodes.find((n) => n.id === sel));
  const factColor = { user: 'var(--fg)', profile: 'var(--accent)', project: 'var(--attn)', domain: 'var(--ico)' };
  const entityColor = { person: 'var(--ok)', organization: 'var(--accent)', product: 'var(--attn)', place: 'var(--ico)', event: 'var(--err)', concept: 'var(--fg-dim)', entity: 'var(--fg-mute)' };
  const edgeStyle = {
    evidence: { c: 'var(--accent)', d: '' }, supports: { c: 'var(--fg)', d: '' }, contradicts: { c: 'var(--err)', d: '' },
    related: { c: 'var(--fg-mute)', d: '' }, supersedes: { c: 'var(--attn-dim)', d: '3 3' },
    temporal: { c: 'var(--attn)', d: '' }, semantic: { c: 'var(--accent)', d: '' }, mentions: { c: 'var(--fg-mute)', d: '1 3' },
  };
  const entityShapes = {
    organization: { form: 'square', outline: false }, product: { form: 'hexagon', outline: false },
    place: { form: 'tri-down', outline: false }, event: { form: 'tri-up', outline: false },
    concept: { form: 'circle', outline: true }, person: { form: 'circle', outline: false },
    entity: { form: 'square', outline: true },
  };
  const r = (n) => (n.type === 'entity' ? 3.5 + Math.min(n.mentions, 8) * 0.7 : 2.5 + Math.min(n.rank, 3) * 0.9);
  const clip = (t, n) => (t.length > n ? t.slice(0, n - 1) + '…' : t);

  // canvas can't read CSS variables, so resolve them once per load/theme
  function resolveColors() {
    const cs = getComputedStyle(wrapEl);
    const get = (v) => { const m = /var\((--[\w-]+)\)/.exec(v); return m ? cs.getPropertyValue(m[1]).trim() || '#888' : v; };
    colors = { bg: get('var(--bg)'), bg2: get('var(--bg-2)'), hi: get('var(--fg-hi)'), attn: get('var(--attn)'), mute: get('var(--fg-mute)'), font: cs.fontFamily };
    // normalise any CSS colour (#f90, rgb(), named…) through a canvas so a translucent variant can be derived
    const probe = document.createElement('canvas').getContext('2d');
    const soft = (c) => { probe.fillStyle = '#000'; probe.fillStyle = c; const h = probe.fillStyle; if (h[0] !== '#') return h; const n = parseInt(h.slice(1), 16); return `rgba(${n >> 16},${(n >> 8) & 255},${n & 255},0.35)`; };
    for (const m of [factColor, entityColor]) for (const [k, v] of Object.entries(m)) { colors[k] = get(v); colors[k + '_soft'] = soft(colors[k]); }
    for (const [k, v] of Object.entries(edgeStyle)) colors['e_' + k] = get(v.c);
  }
  const colorOf = (n) => colors[n.type === 'entity' ? n.kind : n.bank_kind] || '#888';

  function shape(ctx, form, rad) {
    ctx.beginPath();
    switch (form) {
      case 'diamond': { const s = rad * 1.5; ctx.moveTo(0, -s); ctx.lineTo(s, 0); ctx.lineTo(0, s); ctx.lineTo(-s, 0); break; }
      case 'square': { const s = rad * 1.15; ctx.rect(-s, -s, 2 * s, 2 * s); return; }
      case 'hexagon': for (let i = 0; i < 6; i++) { const a = (Math.PI / 180) * (60 * i - 30); const px = rad * 1.2 * Math.cos(a), py = rad * 1.2 * Math.sin(a); i ? ctx.lineTo(px, py) : ctx.moveTo(px, py); } break;
      case 'tri-down': ctx.moveTo(0, rad * 1.3); ctx.lineTo(rad * 1.15, -rad * 0.8); ctx.lineTo(-rad * 1.15, -rad * 0.8); break;
      case 'tri-up': ctx.moveTo(0, -rad * 1.3); ctx.lineTo(rad * 1.15, rad * 0.8); ctx.lineTo(-rad * 1.15, rad * 0.8); break;
      default: ctx.arc(0, 0, rad, 0, Math.PI * 2); return;
    }
    ctx.closePath();
  }
  function formOf(n) {
    if (n.type === 'fact') return { form: n.kind === 'conclusion' ? 'diamond' : 'circle', outline: n.kind === 'conclusion' };
    return entityShapes[n.kind] || entityShapes.entity;
  }

  let raf, t0 = 0;
  function draw(t) {
    raf = requestAnimationFrame(draw);
    if (!canvasEl || !g) return;
    const ctx = canvasEl.getContext('2d');
    const dpr = window.devicePixelRatio || 1;
    if (canvasEl.width !== Math.round(W * dpr) || canvasEl.height !== Math.round(H * dpr)) { canvasEl.width = Math.round(W * dpr); canvasEl.height = Math.round(H * dpr); }
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, W, H);
    const { k } = view;
    const selIdx = sel ? (idx.get(sel) ?? -1) : -1;
    const focus = hoverIdx >= 0 ? hoverIdx : selIdx;
    const near = new Set();
    if (focus >= 0) for (const ei of adj[focus]) { near.add(edges[ei].a); near.add(edges[ei].b); }
    const sx = (n) => view.x + pos[n.id].x * k, sy = (n) => view.y + pos[n.id].y * k;
    const dense = nodes.length > 40, busy2 = edges.length > 150;
    const hlOn = hl !== '', lit = hlOn ? nodes.map(hlMatch) : [];

    // edges: a faint haze normally; the focused node's links are drawn bright (with a bead of light travelling out)
    ctx.lineCap = 'round';
    for (let ei = 0; ei < edges.length; ei++) {
      const e = edges[ei], A = nodes[e.a], B = nodes[e.b];
      if (!pos[A.id] || !pos[B.id]) continue;
      const hot = focus >= 0 && (e.a === focus || e.b === focus);
      ctx.globalAlpha = (hot ? 0.95 : hlOn ? (lit[e.a] || lit[e.b] ? 0.5 : 0.03) : focus >= 0 ? 0.04 : busy2 ? 0.2 : dense ? 0.32 : 0.6) * (e.retired ? 0.5 : 1);
      ctx.strokeStyle = colors['e_' + e.kind] || colors.mute;
      ctx.lineWidth = (0.6 + e.w * 1.4) * (hot ? 1.2 : 1);
      ctx.setLineDash(e.retired ? [3, 3] : (edgeStyle[e.kind]?.d || '').split(' ').filter(Boolean).map(Number));
      ctx.beginPath(); ctx.moveTo(sx(A), sy(A)); ctx.lineTo(sx(B), sy(B)); ctx.stroke();
      if (hot) {
        const u = ((t / 1400) + ei * 0.13) % 1, from = e.a === focus ? A : B, to = e.a === focus ? B : A;
        ctx.setLineDash([]); ctx.globalAlpha = 1; ctx.fillStyle = colors.hi;
        ctx.beginPath(); ctx.arc(sx(from) + (sx(to) - sx(from)) * u, sy(from) + (sy(to) - sy(from)) * u, 1.8, 0, Math.PI * 2); ctx.fill();
      }
    }
    ctx.setLineDash([]);

    // nodes
    for (let i = 0; i < nodes.length; i++) {
      const n = nodes[i], p = pos[n.id]; if (!p) continue;
      const rad = r(n) * Math.max(0.8, Math.min(k, 1.6)), col = colorOf(n), { form, outline } = formOf(n);
      const isFocus = i === focus || i === selIdx;
      ctx.save();
      ctx.translate(sx(n), sy(n));
      ctx.globalAlpha = hlOn ? (lit[i] ? 1 : 0.1) : focus >= 0 && i !== focus && !near.has(i) ? 0.18 : n.retired ? 0.5 : (n.type === 'fact' && n.confidence < 0.5 ? 0.55 : 1);
      ctx.shadowColor = col;
      ctx.shadowBlur = (isFocus ? 12 : 4 + 2 * Math.sin(t / 1100 + i)) * (nodes.length <= 15 ? 0.3 : nodes.length <= 40 ? 0.6 : 1);
      shape(ctx, form, rad);
      if (outline) { ctx.fillStyle = colors.bg2; ctx.fill(); ctx.shadowBlur = 0; ctx.strokeStyle = col; ctx.lineWidth = 1.4; ctx.stroke(); }
      else {
        const gr = ctx.createRadialGradient(-rad * 0.3, -rad * 0.35, rad * 0.1, 0, 0, rad * 1.3);
        gr.addColorStop(0, col); gr.addColorStop(1, colors[(n.type === 'entity' ? n.kind : n.bank_kind) + '_soft'] || col);
        ctx.fillStyle = gr; ctx.fill();
        if (n.type === 'fact' && n.confidence < 0.5) { ctx.shadowBlur = 0; ctx.setLineDash([2, 2]); ctx.strokeStyle = colors.attn; ctx.lineWidth = 1; ctx.stroke(); }
      }
      if (i === selIdx) { ctx.shadowBlur = 0; ctx.setLineDash([]); ctx.strokeStyle = colors.hi; ctx.lineWidth = 2.2; ctx.stroke(); }
      ctx.restore();
    }

    // labels: forced for the focused node and its neighbours, otherwise one per coarse grid cell (highest
    // priority first) so they never pile up; small graphs simply show everything
    ctx.font = `11px ${colors.font}`; ctx.textAlign = 'center'; ctx.textBaseline = 'alphabetic';
    ctx.lineJoin = 'round'; ctx.lineWidth = 2; ctx.globalAlpha = 1; ctx.shadowBlur = 0;
    const cell = 72 / Math.min(Math.max(k, 0.6), 2), used = new Set();
    const say = (i, force) => {
      const n = nodes[i], p = pos[n.id]; if (!p) return;
      const x = sx(n), y = sy(n); if (x < -50 || y < -20 || x > W + 50 || y > H + 20) return;
      if (!force && dense) { const key = Math.floor(x / cell) + ',' + Math.floor(y / (cell / 2)); if (used.has(key)) return; used.add(key); }
      const txt = clip(n.text, Math.min(labelMax, n.type === 'entity' ? 24 : 34)), yy = y - r(n) * Math.max(0.8, Math.min(k, 1.6)) - 5;
      ctx.globalAlpha = 0.6; ctx.strokeStyle = colors.bg; ctx.strokeText(txt, x, yy); ctx.globalAlpha = 1; ctx.fillStyle = colors.hi; ctx.fillText(txt, x, yy);
    };
    const forced = new Set(focus >= 0 ? [focus, ...near] : []);
    if (hlOn) for (let i = 0; i < nodes.length; i++) if (lit[i]) forced.add(i);
    for (const i of forced) say(i, true);
    for (const i of order) if (!forced.has(i)) { if (focus >= 0 && dense) continue; say(i, false); }
    // relation labels of the focused node's edges
    ctx.font = `9px ${colors.font}`; ctx.fillStyle = colors.mute;
    if (focus >= 0) for (const ei of adj[focus]) {
      const e = edges[ei]; if (!e.label) continue;
      const A = nodes[e.a], B = nodes[e.b], mx = (sx(A) + sx(B)) / 2, my = (sy(A) + sy(B)) / 2 - 4;
      ctx.strokeStyle = colors.bg; ctx.strokeText(e.label, mx, my); ctx.fillText(e.label, mx, my);
    }
  }

  // ── interaction (screen-space hit testing; a hit target is always larger than the mark) ──
  const local = (e) => { const b = canvasEl.getBoundingClientRect(); return { x: e.clientX - b.left, y: e.clientY - b.top }; };
  function hit(x, y) {
    let best = -1, bd = Infinity;
    for (let i = 0; i < nodes.length; i++) {
      const p = pos[nodes[i].id]; if (!p) continue;
      const d = Math.hypot(view.x + p.x * view.k - x, view.y + p.y * view.k - y);
      const lim = Math.max(r(nodes[i]) * Math.max(0.8, Math.min(view.k, 1.6)) + 7, 11);
      if (d < lim && d < bd) { bd = d; best = i; }
    }
    return best;
  }
  function onDown(e) {
    const { x, y } = local(e), i = hit(x, y);
    gesture = { sx: x, sy: y, node: i, vx: view.x, vy: view.y, ox: i >= 0 ? pos[nodes[i].id].x : 0, oy: i >= 0 ? pos[nodes[i].id].y : 0, moved: false };
    canvasEl.setPointerCapture?.(e.pointerId);
  }
  function onMove(e) {
    const { x, y } = local(e);
    if (gesture) {
      const dx = x - gesture.sx, dy = y - gesture.sy;
      if (Math.abs(dx) + Math.abs(dy) > 3) gesture.moved = true;
      if (gesture.moved) {
        if (gesture.node >= 0) pos[nodes[gesture.node].id] = { x: gesture.ox + dx / view.k, y: gesture.oy + dy / view.k };
        else view = { ...view, x: gesture.vx + dx, y: gesture.vy + dy };
      }
    }
    const i = gesture?.moved && gesture.node < 0 ? -1 : hit(x, y);
    hoverIdx = i;
    canvasEl.style.cursor = gesture?.moved ? 'grabbing' : i >= 0 ? 'pointer' : 'grab';
    if (i >= 0) { const n = nodes[i]; hoverNode = { id: n.id, text: n.type === 'entity' ? `${n.text} (${n.kind})` : n.text, x, y }; } else hoverNode = null;
  }
  function onUp() {
    if (gesture && !gesture.moved) sel = gesture.node >= 0 ? nodes[gesture.node].id : 0; // a click, not a drag
    gesture = null;
  }
  function onLeave() { if (!gesture) { hoverIdx = -1; hoverNode = null; } }
  function onDbl(e) {
    const { x, y } = local(e), i = hit(x, y);
    if (i >= 0 && nodes[i].type === 'fact') onopen?.(Number(nodes[i].id.slice(1)));
  }
  function onWheel(e) {
    e.preventDefault();
    const { x, y } = local(e), k = Math.min(4, Math.max(0.3, view.k * (e.deltaY < 0 ? 1.12 : 1 / 1.12)));
    view = { k, x: x - ((x - view.x) / view.k) * k, y: y - ((y - view.y) / view.k) * k };
  }

  $effect(() => {
    if (!canvasEl) return;
    resolveColors();
    const ro = new ResizeObserver(() => { W = boxEl.clientWidth; H = boxEl.clientHeight; });
    ro.observe(boxEl);
    canvasEl.addEventListener('wheel', onWheel, { passive: false });
    raf = requestAnimationFrame(draw);
    return () => { cancelAnimationFrame(raf); ro.disconnect(); canvasEl.removeEventListener('wheel', onWheel); };
  });
</script>

<div class="wrap" bind:this={wrapEl}>
  {#if !g || !g.nodes.length}
    <Empty>{busy ? 'laying out…' : 'nothing to draw yet — facts appear here once memory has some'}</Empty>
  {:else}
    <div class="cv" bind:this={boxEl}>
      <canvas bind:this={canvasEl} style="width:{W}px;height:{H}px" onpointerdown={onDown} onpointermove={onMove} onpointerup={onUp} onpointerleave={onLeave} ondblclick={onDbl} aria-label="memory graph"></canvas>
      {#if hoverNode}
        {@const flip = hoverNode.x > W - 280}
        <div class="tip" style="left:{flip ? hoverNode.x - 14 : hoverNode.x + 14}px; top:{hoverNode.y + 14}px; {flip ? 'transform:translateX(-100%);' : ''}">{hoverNode.text}</div>
      {/if}
    </div>
<div class="side">
      <div class="lg">
        <span class="lgh">facts</span>
        {#each Object.entries(factColor) as [k, c]}<button type="button" class="li" class:on={hl === 'f:' + k} onclick={() => toggleHl('f:' + k)}><i style="background:{c}"></i>{k}</button>{/each}
        <button type="button" class="li" class:on={hl === 'conclusion'} onclick={() => toggleHl('conclusion')}><b class="dia" style="border-color:var(--fg)"></b>conclusion</button>
        <button type="button" class="li" class:on={hl === 'unverified'} onclick={() => toggleHl('unverified')}><i class="ring"></i>unverified</button>
      </div>
      <div class="lg">
        <span class="lgh">entities</span>
        {#each Object.entries(entityColor) as [k, c]}
          {@const sh = entityShapes[k] || entityShapes.entity}
          <button type="button" class="li" class:on={hl === 'e:' + k} onclick={() => toggleHl('e:' + k)}>
            {#if sh.form === 'circle'}<i class="ent" class:outline={sh.outline} style="background:{sh.outline ? 'transparent' : c}; border-color:{c}"></i>
            {:else if sh.form === 'square'}<i class="entsq" style="border-color:{c}"></i>
            {:else if sh.form === 'hexagon'}<i class="enthex" style="border-color:{c}"></i>
            {:else if sh.form === 'tri-down'}<i class="enttrid" style="border-top-color:{c}"></i>
            {:else}<i class="enttriu" style="border-bottom-color:{c}"></i>{/if}
            {k}
          </button>
        {/each}
      </div>
      <div class="lg">
        {#each Object.entries(edgeStyle) as [k, s]}<span><u style="border-color:{s.c};border-top-style:{s.d ? 'dotted' : 'solid'}"></u>{k}</span>{/each}
      </div>
      <div class="lg"><span class="lgh">arrange</span>
        {#each [['force', 'organic'], ['bank', 'by bank'], ['type', 'by type'], ['rings', 'rings']] as [k, label]}<button type="button" class="li" class:on={layout === k} onclick={() => (layout = k)}>{label}</button>{/each}
      </div>
      <div class="sm mute">{g.nodes.length} node{g.nodes.length === 1 ? '' : 's'}{g.more ? ` (+${g.more} not shown — pick a bank)` : ''} · {g.edges.length} links · hover for detail · click selects · double-click opens a fact · drag to move · wheel zooms, drag the background pans</div>
      {#if node}
        <div class="card">
          {#if node.type === 'fact'}
            <div class="sm mute">#{node.id.slice(1)} · {node.bank}{node.retired ? ' · retired' : ''} · {node.links} links</div>
            <div class="tx">{node.text}</div>
            <div class="row"><Button size="sm" onclick={() => onopen?.(Number(node.id.slice(1)))}>Open</Button>
              <Button size="sm" variant="ghost" title="Have an agent check this on the web and add what it finds" onclick={() => onresearch?.({ fact_id: Number(node.id.slice(1)), label: node.text })}>Research</Button></div>
          {:else}
            <div class="sm mute">{node.kind} · {node.mentions} mention{node.mentions === 1 ? '' : 's'}</div>
            <div class="tx hi">{node.text}</div>
            <Button size="sm" variant="ghost" title="Have an agent research this on the web and add what it finds" onclick={() => onresearch?.({ entity_id: Number(node.id.slice(1)), label: node.text })}>Research</Button>
            {#each entFacts as f (f.id)}
              <button type="button" class="frow" onclick={() => onopen?.(f.id)}>{f.text}</button>
            {:else}
              <div class="sm mute">no facts loaded yet</div>
            {/each}
          {/if}
        </div>
      {/if}
    </div>
  {/if}
</div>


<style>
  .wrap { position: relative; flex: 1; min-width: 0; min-height: 0; display: flex; gap: 8px; }
  .cv { position: relative; flex: 1; min-width: 0; overflow: hidden; background: color-mix(in srgb, var(--bg) 70%, transparent); border: 1px solid var(--line); }
  canvas { position: absolute; inset: 0; display: block; cursor: grab; touch-action: none; user-select: none; -webkit-user-select: none; }
  .tip { position: absolute; z-index: 5; max-width: 280px; padding: 6px 9px; background: var(--bg-1); border: 1px solid var(--line-2); border-radius: var(--r); color: var(--fg-hi); font-size: var(--fs-sm); line-height: 1.4; pointer-events: none; box-shadow: 0 4px 14px rgba(0,0,0,0.35); }
  .side { width: 250px; flex: none; display: flex; flex-direction: column; gap: 8px; overflow: auto; }
  .lg { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 10px; font-size: 10.5px; color: var(--fg-dim); }
  .lgh { flex-basis: 100%; text-transform: uppercase; letter-spacing: 0.1em; font-size: 9.5px; color: var(--fg-mute); }
  .lg span { display: inline-flex; align-items: center; gap: 4px; }
  .lg i { width: 8px; height: 8px; border-radius: 50%; display: inline-block; }
  .lg i.ring { background: transparent; border: 1px dashed var(--attn); }
  .lg i.ent { border: 1.5px solid transparent; }
  .lg i.entsq { width: 7px; height: 7px; border-radius: 0; border: 1.5px solid; background: transparent; }
  .lg i.enthex { width: 8px; height: 8px; border: 1.5px solid; background: transparent; clip-path: polygon(25% 0%, 75% 0%, 100% 50%, 75% 100%, 25% 100%, 0% 50%); border-radius: 0; }
  .lg i.enttriu { width: 0; height: 0; background: none; border-radius: 0; border-left: 4px solid transparent; border-right: 4px solid transparent; border-bottom: 7px solid; }
  .lg i.enttrid { width: 0; height: 0; background: none; border-radius: 0; border-left: 4px solid transparent; border-right: 4px solid transparent; border-top: 7px solid; }
  .lg .li { background: none; border: 0; padding: 1px 4px; margin: -1px -4px; color: inherit; font: inherit; cursor: pointer; display: inline-flex; align-items: center; gap: 5px; border-radius: 4px; }
  .lg .li:hover { background: var(--bg-hover, rgba(128,128,128,.15)); }
  .lg .li.on { outline: 1px solid var(--accent); color: var(--fg); }
  .lg u { width: 14px; border-top: 2px solid; display: inline-block; text-decoration: none; }
  .lg .dia { width: 7px; height: 7px; border: 1.5px solid; transform: rotate(45deg); display: inline-block; }
  .card { border: 1px solid var(--line-2); background: var(--bg-1); padding: 6px 8px; display: flex; flex-direction: column; gap: 6px; flex: none; max-height: 45%; overflow: auto; }
  .tx { color: var(--fg-hi); overflow-wrap: anywhere; }
  .frow { text-align: left; background: none; border: 0; padding: 4px 0 0; margin: 0; font-size: var(--fs-sm); color: var(--fg-dim); border-top: 1px solid var(--line-2); }
  .frow:hover { color: var(--fg-hi); }
</style>
