<script>
  // The memory graph in 3D: facts (circles), conclusions (diamonds) and entities (rings) as points in space, tied by their
  // links. Placement is spatial — every bank gets its own region on a sphere (a Fibonacci lattice, so regions are evenly
  // apart), the facts of a bank gather round their region's centre, entities float in the middle between the banks that
  // mention them, and a 3D force simulation settles the rest (linked things attract, everything repels). Drag to orbit,
  // wheel / pinch to zoom, right button or two fingers to pan, click to select (its links light up), double-click to open a fact.
  import { onMount, untrack } from 'svelte';
  import { call } from './store.svelte.js';
  import Button from './ui/Button.svelte';
  let { bank = 0, history = false, onopen = () => {} } = $props();

  let host = $state();
  let tip = $state(null);
  let sel = $state(null); // the selected node (plain data)
  let spin = $state(false);
  let busy = $state(false);
  let more = $state(0);
  let api = null;

  onMount(() => {
    let dead = false, cleanup = () => {};
    (async () => {
      const THREE = await import('three');
      const { OrbitControls } = await import('three/examples/jsm/controls/OrbitControls.js');
      if (dead || !host) return;
      cleanup = start(THREE, OrbitControls);
    })();
    return () => { dead = true; cleanup(); api = null; };
  });
  $effect(() => { bank; history; untrack(() => api?.load()); });

  function start(THREE, OrbitControls) {
    const cs = getComputedStyle(host);
    const col = (v, d) => { try { return new THREE.Color(cs.getPropertyValue(v).trim() || d); } catch { return new THREE.Color(d); } };
    const C = { fg: col('--fg', '#ccc'), hi: col('--fg-hi', '#fff'), dim: col('--fg-dim', '#999'), accent: col('--accent', '#6c6'), accentHi: col('--accent-hi', '#9f9'),
      attn: col('--attn', '#fb3'), err: col('--err', '#f55'), mute: col('--fg-mute', '#777'), line: col('--line-3', '#444') };
    const renderer = new THREE.WebGLRenderer({ antialias: true, alpha: true });
    renderer.setPixelRatio(Math.min(window.devicePixelRatio || 1, 2));
    renderer.domElement.style.cssText = 'position:absolute;inset:0;width:100%;height:100%;display:block;touch-action:none';
    host.appendChild(renderer.domElement);
    const scene = new THREE.Scene();
    const camera = new THREE.PerspectiveCamera(50, 1, 1, 6000);
    const controls = new OrbitControls(camera, renderer.domElement);
    controls.enableDamping = true; controls.dampingFactor = 0.08; controls.rotateSpeed = 0.7; controls.screenSpacePanning = true; controls.autoRotateSpeed = 0.6;
    const disposables = [];
    const own = (o) => { disposables.push(o); return o; };
    const shapeTex = (paint) => {
      const c = document.createElement('canvas'); c.width = c.height = 64; paint(c.getContext('2d'));
      const t = own(new THREE.CanvasTexture(c)); t.colorSpace = THREE.SRGBColorSpace; return t;
    };
    const T = {
      circle: shapeTex((g) => { g.fillStyle = '#fff'; g.beginPath(); g.arc(32, 32, 26, 0, 6.283); g.fill(); }),
      diamond: shapeTex((g) => { g.fillStyle = '#fff'; g.beginPath(); g.moveTo(32, 4); g.lineTo(60, 32); g.lineTo(32, 60); g.lineTo(4, 32); g.closePath(); g.fill(); }),
      ring: shapeTex((g) => { g.strokeStyle = '#fff'; g.lineWidth = 9; g.beginPath(); g.arc(32, 32, 24, 0, 6.283); g.stroke(); }),
    };
    const kindHue = { person: 0.08, organization: 0.58, product: 0.75, place: 0.33, event: 0.95, concept: 0.5 };

    let group = new THREE.Group(); scene.add(group);
    let nodes = [], edges = [], idx = new Map(), lineAll, lineSel, labels = new Map(), hoverI = -1, selI = -1, radius = 300, built = false;

    const fib = (i, n) => { const y = n === 1 ? 0 : 1 - (2 * (i + 0.5)) / n, r = Math.sqrt(Math.max(0, 1 - y * y)), th = i * 2.399963229728653; return new THREE.Vector3(Math.cos(th) * r, y, Math.sin(th) * r); };
    const rnd = (i) => { const s = Math.sin(i * 12.9898) * 43758.5453; return s - Math.floor(s); };
    function clear() {
      scene.remove(group);
      group.traverse((o) => { o.geometry?.dispose?.(); const m = o.material; if (m) (Array.isArray(m) ? m : [m]).forEach((x) => { x.map?.dispose?.(); x.dispose?.(); }); });
      group = new THREE.Group(); scene.add(group); nodes = []; edges = []; labels = new Map();
    }
    const colorOf = (n) => n.type === 'entity' ? new THREE.Color().setHSL(kindHue[n.kind] ?? 0.6, 0.55, 0.62)
      : n.kind === 'conclusion' ? C.accentHi : n.bank_kind === 'user' ? C.hi : n.bank_kind === 'project' ? C.accent : n.bank_kind === 'domain' ? C.attn : C.dim;

    async function load() {
      busy = true;
      const r = await call('memory.full_graph', { bank_id: bank, history, limit: 220 }, { quiet: true });
      busy = false;
      if (!r) return;
      more = r.more || 0; sel = null; selI = -1; hoverI = -1;
      clear();
      const banks = [...new Set(r.nodes.filter((n) => n.type === 'fact').map((n) => n.bank))].sort();
      radius = banks.length > 1 ? 240 + banks.length * 16 : 140;
      const anchors = Object.fromEntries(banks.map((b, i) => [b, banks.length === 1 ? new THREE.Vector3() : fib(i, banks.length).multiplyScalar(radius)]));
      nodes = r.nodes.map((n, i) => {
        const home = n.type === 'fact' ? anchors[n.bank] : new THREE.Vector3();
        const p = home.clone().add(new THREE.Vector3(rnd(i) - 0.5, rnd(i + 999) - 0.5, rnd(i + 1999) - 0.5).multiplyScalar(n.type === 'fact' ? 110 : 160));
        return { d: n, p, v: new THREE.Vector3(), home, i, deg: 0 };
      });
      idx = new Map(nodes.map((n, i) => [n.d.id, i]));
      edges = [];
      for (const e of r.edges) { const a = idx.get(e.a), b = idx.get(e.b); if (a !== undefined && b !== undefined) { edges.push({ a, b, kind: e.kind, w: e.weight || 0.5, retired: !!e.retired }); nodes[a].deg++; nodes[b].deg++; } }
      settle();
      build();
      if (!built) { reset(); built = true; }
    }

    // a 3D force layout: pairwise repulsion, springs along links, a pull towards each node's bank region, a little gravity
    function settle() {
      const n = nodes.length, f = nodes.map(() => new THREE.Vector3()), tmp = new THREE.Vector3();
      for (let it = 0; it < 260; it++) {
        const cool = 1 - it / 260;
        for (const x of f) x.set(0, 0, 0);
        for (let i = 0; i < n; i++) for (let j = i + 1; j < n; j++) {
          tmp.subVectors(nodes[i].p, nodes[j].p); let d2 = tmp.lengthSq() + 0.5;
          if (d2 > 90000) continue;
          const k = 1800 / d2; tmp.multiplyScalar(k / Math.sqrt(d2)); f[i].add(tmp); f[j].sub(tmp);
        }
        for (const e of edges) {
          tmp.subVectors(nodes[e.b].p, nodes[e.a].p); const d = tmp.length() + 0.01, k = (d - 55) * 0.04 * (0.5 + e.w);
          tmp.multiplyScalar(k / d); f[e.a].add(tmp); f[e.b].sub(tmp);
        }
        for (let i = 0; i < n; i++) {
          tmp.subVectors(nodes[i].home, nodes[i].p).multiplyScalar(nodes[i].d.type === 'fact' ? 0.02 : 0.004); f[i].add(tmp);
          tmp.copy(nodes[i].p).multiplyScalar(-0.0015); f[i].add(tmp);
          nodes[i].v.add(f[i]).multiplyScalar(0.82);
          const sp = nodes[i].v.length(), cap = 14 * cool + 1; if (sp > cap) nodes[i].v.multiplyScalar(cap / sp);
          nodes[i].p.add(nodes[i].v);
        }
      }
    }

    function labelSprite(text, color) {
      const c = document.createElement('canvas'); c.width = 384; c.height = 48; const g = c.getContext('2d');
      const t = text.length > 44 ? text.slice(0, 43) + '…' : text;
      g.font = '600 22px monospace'; g.textAlign = 'center'; g.textBaseline = 'middle'; g.shadowColor = 'rgba(0,0,0,0.85)'; g.shadowBlur = 5;
      g.fillStyle = '#' + color.getHexString(); g.fillText(t, 192, 24);
      const tex = own(new THREE.CanvasTexture(c)); tex.colorSpace = THREE.SRGBColorSpace;
      const s = new THREE.Sprite(new THREE.SpriteMaterial({ map: tex, transparent: true, depthWrite: false })); s.scale.set(96, 12, 1); s.renderOrder = 6; return s;
    }

    function build() {
      const score = (n) => (n.d.kind === 'conclusion' ? 100 : 0) + (n.d.type === 'entity' ? (n.d.mentions || 0) + 2 : n.d.rank || 0) + n.deg;
      const top = new Set([...nodes].sort((a, b) => score(b) - score(a)).slice(0, 12).map((n) => n.i));
      for (const n of nodes) {
        const d = n.d, base = d.type === 'entity' ? 8 : d.kind === 'conclusion' ? 9 : 5 + Math.min(4, (d.rank || 0) * 1.2) + Math.min(3, n.deg * 0.4);
        n.size = base; n.color = colorOf(n);
        n.sprite = new THREE.Sprite(new THREE.SpriteMaterial({ map: d.type === 'entity' ? T.ring : d.kind === 'conclusion' ? T.diamond : T.circle, color: n.color, transparent: true, depthWrite: false,
          opacity: d.retired ? 0.28 : d.type === 'fact' && d.confidence < 0.5 ? 0.55 : 1 }));
        n.sprite.scale.set(base, base, 1); n.sprite.position.copy(n.p); n.sprite.renderOrder = 3; n.sprite.userData.i = n.i; group.add(n.sprite);
        n.top = top.has(n.i);
      }
      const mk = (opacity) => { const g = new THREE.BufferGeometry(); g.setAttribute('position', new THREE.BufferAttribute(new Float32Array(Math.max(1, edges.length) * 6), 3)); g.setAttribute('color', new THREE.BufferAttribute(new Float32Array(Math.max(1, edges.length) * 6), 3));
        const l = new THREE.LineSegments(g, new THREE.LineBasicMaterial({ vertexColors: true, transparent: true, opacity, depthWrite: false })); l.frustumCulled = false; group.add(l); return l; };
      lineAll = mk(0.22); lineSel = mk(0.95);
      const kc = (k) => (k === 'contradicts' ? C.err : k === 'evidence' || k === 'supports' ? C.accent : k === 'mentions' ? C.mute : C.dim);
      const fill = (l, pick) => {
        const pa = l.geometry.attributes.position.array, ca = l.geometry.attributes.color.array; let m = 0;
        for (const e of edges) { if (!pick(e)) continue; const a = nodes[e.a].p, b = nodes[e.b].p, c = kc(e.kind);
          pa.set([a.x, a.y, a.z, b.x, b.y, b.z], m * 6); ca.set([c.r, c.g, c.b, c.r, c.g, c.b], m * 6); m++; }
        l.geometry.setDrawRange(0, m * 2); l.geometry.attributes.position.needsUpdate = true; l.geometry.attributes.color.needsUpdate = true;
      };
      api.fill = fill; fill(lineAll, () => true); fill(lineSel, () => false);
    }

    function reset() {
      const d = Math.max(300, radius * 2.6); camera.position.set(d * 0.3, d * 0.35, d * 0.9); controls.target.set(0, 0, 0);
      controls.minDistance = 40; controls.maxDistance = d * 4; controls.update();
    }
    function select(i) {
      selI = i;
      if (i < 0) { sel = null; api.fill(lineSel, () => false); return; }
      const d = nodes[i].d; sel = { id: d.id, type: d.type, kind: d.kind, text: d.text, bank: d.bank, conf: d.confidence, mentions: d.mentions };
      api.fill(lineSel, (e) => e.a === i || e.b === i);
    }
    api = { load, reset, fill: () => {} };

    function resize() { const w = host.clientWidth || 300, h = host.clientHeight || 300; renderer.setSize(w, h, false); camera.aspect = w / h; camera.updateProjectionMatrix(); }
    const ro = new ResizeObserver(resize); ro.observe(host); resize();

    const ray = new THREE.Raycaster(), ndc = new THREE.Vector2(); let down = null, lastClick = 0;
    const pick = (e) => {
      const b = renderer.domElement.getBoundingClientRect(); ndc.set(((e.clientX - b.left) / b.width) * 2 - 1, -((e.clientY - b.top) / b.height) * 2 + 1); ray.setFromCamera(ndc, camera);
      const h = ray.intersectObjects(nodes.map((n) => n.sprite), false)[0]; return h ? h.object.userData.i : -1;
    };
    const el = renderer.domElement;
    const onDown = (e) => { down = { x: e.clientX, y: e.clientY }; };
    const onUp = (e) => {
      if (down && Math.hypot(e.clientX - down.x, e.clientY - down.y) < 5) {
        const i = pick(e), now = Date.now();
        if (i >= 0 && i === selI && now - lastClick < 400 && nodes[i].d.type === 'fact') onopen(Number(nodes[i].d.id.slice(1)));
        select(i); lastClick = now;
      }
      down = null;
    };
    const onMove = (e) => {
      if (down && e.buttons) { tip = null; return; }
      const i = pick(e); hoverI = i; el.style.cursor = i >= 0 ? 'pointer' : 'grab';
      const b = el.getBoundingClientRect();
      tip = i >= 0 ? { text: nodes[i].d.text, sub: nodes[i].d.type === 'entity' ? nodes[i].d.kind : nodes[i].d.bank, x: e.clientX - b.left, y: e.clientY - b.top } : null;
    };
    el.addEventListener('pointerdown', onDown); window.addEventListener('pointerup', onUp); el.addEventListener('pointermove', onMove); el.addEventListener('pointerleave', () => { hoverI = -1; tip = null; });

    let raf = 0;
    function frame() {
      raf = requestAnimationFrame(frame);
      if (document.hidden) return;
      const near = new Set(); if (selI >= 0) { near.add(selI); for (const e of edges) { if (e.a === selI) near.add(e.b); if (e.b === selI) near.add(e.a); } }
      for (const n of nodes) {
        const hov = hoverI === n.i, s = n.size * (hov ? 1.6 : n.i === selI ? 1.4 : 1);
        n.sprite.scale.set(s, s, 1);
        const dimmed = selI >= 0 && !near.has(n.i);
        n.sprite.material.opacity = (n.d.retired ? 0.28 : n.d.type === 'fact' && n.d.confidence < 0.5 ? 0.55 : 1) * (dimmed ? 0.25 : 1);
        const wantLabel = hov || n.i === selI || (selI < 0 && n.top) || (selI >= 0 && near.has(n.i) && near.size < 14);
        if (wantLabel && !labels.has(n.i)) { const sp = labelSprite(n.d.text, C.hi); group.add(sp); labels.set(n.i, sp); }
        const sp = labels.get(n.i);
        if (sp) { sp.position.set(n.p.x, n.p.y - s * 0.5 - 8, n.p.z); sp.material.opacity += ((wantLabel ? 1 : 0) - sp.material.opacity) * 0.2; sp.visible = sp.material.opacity > 0.03; }
      }
      controls.autoRotate = spin; controls.update(); renderer.render(scene, camera);
    }
    raf = requestAnimationFrame(frame);
    api.load();

    return () => {
      cancelAnimationFrame(raf); ro.disconnect(); window.removeEventListener('pointerup', onUp); el.removeEventListener('pointerdown', onDown); el.removeEventListener('pointermove', onMove);
      clear(); disposables.forEach((d) => d.dispose?.()); controls.dispose(); renderer.dispose(); renderer.domElement.remove();
    };
  }
</script>

<div class="g3" bind:this={host}>
  <div class="bt">
    <button type="button" class:on={spin} onclick={() => (spin = !spin)} title="slowly turn the graph" aria-pressed={spin}>⟳</button>
    <button type="button" onclick={() => api?.reset()} title="reset the view">⌖</button>
  </div>
  {#if busy}<div class="st">loading…</div>{:else if more}<div class="st">{more} more facts not shown</div>{/if}
  {#if tip}<div class="tip" style="left:{tip.x + 14}px; top:{tip.y + 14}px">{tip.text}<div class="mute sm">{tip.sub}</div></div>{/if}
  {#if sel}
    <div class="sel">
      <div class="sm mute">{sel.type === 'entity' ? `${sel.kind} · mentioned ${sel.mentions || 0}×` : `${sel.kind} · ${sel.bank} · ${Math.round((sel.conf || 0) * 100)}%`}</div>
      <div class="stx">{sel.text}</div>
      {#if sel.type === 'fact'}<Button size="sm" variant="primary" onclick={() => onopen(Number(sel.id.slice(1)))}>Open</Button>{/if}
    </div>
  {/if}
</div>

<style>
  .g3 { position: relative; flex: 1; min-height: 0; overflow: hidden; border: 1px solid var(--line-2); background: radial-gradient(circle at 50% 50%, var(--bg-2) 0%, var(--bg) 100%); }
  .bt { position: absolute; left: 8px; top: 8px; z-index: 4; display: flex; flex-direction: column; gap: 4px; }
  .bt button { width: 32px; height: 32px; border: 1px solid var(--line-2); background: var(--bg-1); color: var(--fg-hi); border-radius: var(--r); font-size: 16px; line-height: 1; cursor: pointer; }
  .bt button.on { border-color: var(--accent); color: var(--accent-hi); }
  .st { position: absolute; right: 10px; top: 8px; z-index: 4; font-size: var(--fs-sm); color: var(--fg-mute); }
  .tip { position: absolute; z-index: 5; max-width: 320px; padding: 5px 8px; background: var(--bg-1); border: 1px solid var(--line-2); border-radius: var(--r); color: var(--fg-hi); font-size: var(--fs-sm); pointer-events: none; }
  .sel { position: absolute; left: 8px; right: 8px; bottom: 8px; z-index: 4; max-width: 520px; display: flex; flex-direction: column; gap: 4px; align-items: flex-start; padding: 8px 10px; background: var(--bg-1); border: 1px solid var(--line-3); }
  .stx { color: var(--fg-hi); max-height: 110px; overflow: auto; }
</style>
