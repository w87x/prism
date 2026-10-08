<script>
  // The agent graph in real 3D space. Placement is spatial, not a flat picture lifted into depth: Atlas sits in the middle,
  // the groups are spread EVENLY over a sphere round it (a Fibonacci lattice — every group the same angular distance from
  // its neighbours, in all three dimensions), and each group's members are spread evenly over a small cap round their hub,
  // facing away from Atlas so no member sits on the trunk line. Drag to orbit, wheel / pinch to zoom, two fingers or right
  // button to pan; click an agent to open it. three.js is loaded only when this layout is chosen.
  import { onMount } from 'svelte';
  let { agents = [], activeNames = new Set(), liveNames = new Set(), blockedNames = new Set(), heatOf = () => 0, selectedId = null,
        onselect = () => {}, match = () => true, iconOf = () => '', drift = true, nonce = 0 } = $props();

  let host = $state();
  let tip = $state(null);
  let spin = $state(true);
  try { spin = localStorage.getItem('prism.graph3dSpin') !== '0'; } catch {}
  const toggleSpin = () => { spin = !spin; try { localStorage.setItem('prism.graph3dSpin', spin ? '1' : '0'); } catch {} };
  const reduced = typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches;
  let api = null; // set once three is loaded: { rebuild(), reset(), shuffle() }

  onMount(() => {
    let dead = false, cleanup = () => {};
    (async () => {
      const THREE = await import('three');
      const { OrbitControls } = await import('three/examples/jsm/controls/OrbitControls.js');
      try { await document.fonts.load('900 24px FA'); } catch {}
      if (dead || !host) return;
      cleanup = start(THREE, OrbitControls);
    })();
    return () => { dead = true; cleanup(); api = null; };
  });

  // re-lay-out when the roster changes, and when Shuffle is pressed
  $effect(() => { agents; api?.rebuild(); });
  $effect(() => { nonce; api?.shuffle(); });

  function start(THREE, OrbitControls) {
    const cs = getComputedStyle(host);
    const col = (v, d) => { try { return new THREE.Color(cs.getPropertyValue(v).trim() || d); } catch { return new THREE.Color(d); } };
    const C = { bg: col('--bg', '#000'), bg2: col('--bg-2', '#111'), fg: col('--fg', '#ccc'), hi: col('--fg-hi', '#fff'), dim: col('--fg-dim', '#999'),
      accent: col('--accent', '#6c6'), accentHi: col('--accent-hi', '#9f9'), attn: col('--attn', '#fb3'), err: col('--err', '#f55'), line: col('--line-3', '#444'),
      off: col('--fg-faint', '#555'), maint: col('--accent-dim', '#486') };
    const css = (c) => '#' + c.getHexString();

    const renderer = new THREE.WebGLRenderer({ antialias: true, alpha: true });
    renderer.setPixelRatio(Math.min(window.devicePixelRatio || 1, 2));
    renderer.setClearColor(0x000000, 0);
    renderer.domElement.style.cssText = 'position:absolute;inset:0;width:100%;height:100%;display:block;touch-action:none';
    host.appendChild(renderer.domElement);
    const scene = new THREE.Scene();
    const camera = new THREE.PerspectiveCamera(50, 1, 1, 4000);
    const controls = new OrbitControls(camera, renderer.domElement);
    controls.enableDamping = true; controls.dampingFactor = 0.08; controls.rotateSpeed = 0.7; controls.zoomSpeed = 0.9; controls.screenSpacePanning = true;
    controls.autoRotateSpeed = 0.5;

    const disposables = [];
    const own = (o) => { disposables.push(o); return o; };
    const canvasTex = (w, h, paint) => {
      const c = document.createElement('canvas'); c.width = w; c.height = h;
      const g = c.getContext('2d'); paint(g, w, h);
      const t = own(new THREE.CanvasTexture(c)); t.colorSpace = THREE.SRGBColorSpace; t.anisotropy = 4; return t;
    };
    const haloTex = canvasTex(128, 128, (g) => { g.strokeStyle = '#fff'; g.lineWidth = 5; g.beginPath(); g.arc(64, 64, 58, 0, Math.PI * 2); g.stroke(); });
    const glowTex = canvasTex(128, 128, (g) => { const gr = g.createRadialGradient(64, 64, 8, 64, 64, 64); gr.addColorStop(0, 'rgba(255,255,255,0.55)'); gr.addColorStop(1, 'rgba(255,255,255,0)'); g.fillStyle = gr; g.fillRect(0, 0, 128, 128); });

    let group = new THREE.Group(); scene.add(group);
    let items = [];   // {a, sprite, halo, glow, label, base: Vector3, ph, size, isHub?}
    let hubs = [];    // {key, label, base, sprite(label), mesh, members}
    let lineBase, lineLive, lineBlocked, posAttr = {}; let segs = [];
    let order = new Map(), radius = 200, built = false;

    const fib = (i, n, yMin = -1) => { // i-th of n points evenly spread over a sphere (or a cap, down to yMin)
      const y = n === 1 ? 1 : 1 - ((1 - yMin) * (i + 0.5)) / n, r = Math.sqrt(Math.max(0, 1 - y * y)), th = i * 2.399963229728653;
      return new THREE.Vector3(Math.cos(th) * r, y, Math.sin(th) * r);
    };

    function clear() {
      scene.remove(group);
      group.traverse((o) => { o.geometry?.dispose?.(); const m = o.material; if (m) { (Array.isArray(m) ? m : [m]).forEach((x) => { x.map?.dispose?.(); x.dispose?.(); }); } });
      group = new THREE.Group(); scene.add(group); items = []; hubs = [];
    }

    function nodeTexture(a, col) {
      return canvasTex(128, 128, (g) => {
        g.beginPath(); g.arc(64, 64, 56, 0, Math.PI * 2); g.fillStyle = css(C.bg2); g.fill();
        g.lineWidth = 7; g.strokeStyle = css(col); g.stroke();
        g.fillStyle = css(col); g.font = '900 52px FA'; g.textAlign = 'center'; g.textBaseline = 'middle'; g.fillText(iconOf(a.name), 64, 68);
      });
    }
    function textSprite(lines, opts = {}) {
      const w = 256, h = lines.length > 1 ? 72 : 44;
      const tex = canvasTex(w, h, (g) => {
        g.textAlign = 'center'; g.textBaseline = 'middle';
        g.font = `700 ${opts.size || 24}px monospace`; g.fillStyle = css(opts.color || C.hi); g.shadowColor = 'rgba(0,0,0,0.8)'; g.shadowBlur = 5;
        g.fillText(lines[0], w / 2, lines.length > 1 ? 22 : h / 2);
        if (lines[1]) { g.font = '16px monospace'; g.fillStyle = css(C.dim); g.fillText(lines[1].toUpperCase(), w / 2, 52); }
      });
      const m = new THREE.SpriteMaterial({ map: tex, transparent: true, opacity: 0, depthWrite: false });
      const s = new THREE.Sprite(m); s.scale.set(w / 256 * 92, h / 256 * 92, 1); s.renderOrder = 5;
      return s;
    }

    function rebuild() {
      clear();
      const list = agents;
      const atlas = list.find((a) => a.role === 'entry');
      const keyOf = (a) => (a.role === 'entry' ? null : a.role === 'maint' ? 'staff' : 'g:' + a.group);
      const keys = [...new Set(list.map(keyOf).filter(Boolean))].sort((x, y) => (x === 'staff') - (y === 'staff') || x.localeCompare(y));
      const members = Object.fromEntries(keys.map((k) => [k, []]));
      for (const a of list) { const k = keyOf(a); if (k) members[k].push(a); }
      for (const k of keys) members[k].sort((x, y) => (order.get(x.id) ?? x.id) - (order.get(y.id) ?? y.id));
      const rc = (m) => 30 + 10 * Math.sqrt(m);
      const maxRc = Math.max(40, ...keys.map((k) => rc(members[k].length)));
      radius = keys.length ? Math.max(150, Math.sqrt(keys.length) * 62 + maxRc * 1.7) : 0;
      const dirs = keys.length === 2 ? [new THREE.Vector3(1, 0.2, 0).normalize(), new THREE.Vector3(-1, -0.2, 0).normalize()] : keys.map((_, i) => fib(i, keys.length));
      const up = new THREE.Vector3(0, 1, 0), spinQ = new THREE.Quaternion().setFromAxisAngle(up, order.get('rot') ?? 0);
      const compact = host.clientWidth < 560, nodeSize = compact ? 12 : 15;

      const mk = (a, base, size) => {
        const col = !a.enabled ? C.off : a.role === 'entry' ? C.hi : a.role === 'maint' ? C.accent : C.fg;
        const sp = new THREE.Sprite(new THREE.SpriteMaterial({ map: nodeTexture(a, col), transparent: true, depthWrite: false }));
        sp.scale.set(size, size, 1); sp.userData.agent = a; sp.renderOrder = 3;
        const halo = new THREE.Sprite(new THREE.SpriteMaterial({ map: haloTex, transparent: true, opacity: 0, depthWrite: false, color: C.fg })); halo.scale.set(size * 1.45, size * 1.45, 1); halo.renderOrder = 2;
        const glow = new THREE.Sprite(new THREE.SpriteMaterial({ map: glowTex, transparent: true, opacity: 0, depthWrite: false, color: C.accent, blending: THREE.AdditiveBlending })); glow.scale.set(size * 3, size * 3, 1); glow.renderOrder = 1;
        const label = textSprite([a.name, a.probation ? 'on probation' : a.role === 'entry' ? 'entry' : a.role === 'maint' ? 'staff' : a.group], { color: a.role === 'maint' ? C.accentHi : C.hi });
        group.add(sp, halo, glow, label);
        const it = { a, sprite: sp, halo, glow, label, base: base.clone(), size, ph: Array.from({ length: 6 }, () => Math.random() * 6.283), lab: 0, k: 1, col };
        items.push(it); return it;
      };
      const atlasItem = atlas ? mk(atlas, new THREE.Vector3(0, 0, 0), nodeSize * 1.55) : null;
      segs = []; // [fromObj, toObj, memberItem|null, hubKey]
      keys.forEach((k, gi) => {
        const m = members[k], dir = dirs[gi].clone().applyQuaternion(spinQ), R = radius * (keys.length > 5 && gi % 2 ? 0.8 : 1);
        const hubPos = dir.clone().multiplyScalar(R);
        const mesh = new THREE.Mesh(new THREE.OctahedronGeometry(3.4), new THREE.MeshBasicMaterial({ color: k === 'staff' ? C.maint : C.dim, wireframe: false }));
        mesh.position.copy(hubPos); group.add(mesh);
        const lbl = textSprite([k === 'staff' ? 'staff' : k.slice(2)], { size: 22, color: C.dim });
        group.add(lbl);
        const hub = { key: k, label: lbl, base: hubPos, mesh, ph: Array.from({ length: 6 }, () => Math.random() * 6.283), lab: 0, pos: hubPos.clone() };
        hubs.push(hub);
        // members: evenly spread over a cap facing away from Atlas
        const q = new THREE.Quaternion().setFromUnitVectors(up, dir);
        const r = rc(m.length);
        m.forEach((a, i) => {
          const off = m.length === 1 ? up.clone() : fib(i, m.length, -0.15);
          const it = mk(a, hubPos.clone().add(off.applyQuaternion(q).multiplyScalar(r)), nodeSize);
          it.hub = hub; it.rc = r; segs.push({ hub, it });
        });
      });
      if (atlasItem) hubs.forEach((h) => segs.push({ hub: h, it: null }));
      const mkLines = (color, opacity, dashed) => {
        const g = new THREE.BufferGeometry(); g.setAttribute('position', new THREE.BufferAttribute(new Float32Array(600 * 6), 3)); g.setDrawRange(0, 0);
        const l = new THREE.LineSegments(g, new THREE.LineBasicMaterial({ color, transparent: true, opacity, depthWrite: false }));
        l.frustumCulled = false; group.add(l); return l;
      };
      lineBase = mkLines(C.line, 0.75); lineLive = mkLines(C.hi, 1); lineBlocked = mkLines(C.err, 0.9);
      api.atlasItem = atlasItem;
      if (!built) { resetView(false); built = true; } // the camera is only reset on the first build, not on every roster change
    }

    function resetView(animate = true) {
      const d = Math.max(260, radius * 2.35);
      camera.position.set(d * 0.28, d * 0.34, d * 0.9); controls.target.set(0, 0, 0);
      controls.minDistance = 50; controls.maxDistance = Math.max(900, d * 3); controls.update();
    }
    function shuffle() {
      const ids = agents.map((a) => a.id);
      for (const id of ids) order.set(id, Math.random());
      order.set('rot', Math.random() * Math.PI * 2);
      rebuild();
    }
    api = { rebuild, shuffle, reset: resetView };
    api.rebuild();
    if (import.meta.env.DEV) window.__g3 = { scene, camera, controls, renderer, get items() { return items; }, get hubs() { return hubs; } }; // for debugging in the dev server only

    function resize() {
      const w = host.clientWidth || 300, h = host.clientHeight || 300;
      renderer.setSize(w, h, false); camera.aspect = w / h; camera.updateProjectionMatrix();
    }
    const ro = new ResizeObserver(resize); ro.observe(host); resize();

    // ── pointer: hover tooltip, click to open ──
    const ray = new THREE.Raycaster(), ndc = new THREE.Vector2();
    let down = null, hoverIt = null;
    const pick = (e) => {
      const b = renderer.domElement.getBoundingClientRect();
      ndc.set(((e.clientX - b.left) / b.width) * 2 - 1, -((e.clientY - b.top) / b.height) * 2 + 1);
      ray.setFromCamera(ndc, camera);
      const hit = ray.intersectObjects(items.map((i) => i.sprite), false)[0];
      return hit ? items.find((i) => i.sprite === hit.object) : null;
    };
    const onDown = (e) => { down = { x: e.clientX, y: e.clientY }; };
    const onUp = (e) => {
      if (down && Math.hypot(e.clientX - down.x, e.clientY - down.y) < 5) { const it = pick(e); if (it) onselect(it.a.id); }
      down = null;
    };
    const onMove = (e) => {
      if (down && e.buttons) { tip = null; return; }
      const it = pick(e); hoverIt = it;
      renderer.domElement.style.cursor = it ? 'pointer' : 'grab';
      const b = renderer.domElement.getBoundingClientRect();
      tip = it ? { text: `${it.a.name} — ${it.a.description} (${it.a.tools.length} tools)`, x: e.clientX - b.left, y: e.clientY - b.top } : null;
    };
    const onLeave = () => { hoverIt = null; tip = null; };
    const el = renderer.domElement;
    el.addEventListener('pointerdown', onDown); window.addEventListener('pointerup', onUp); el.addEventListener('pointermove', onMove); el.addEventListener('pointerleave', onLeave);

    // ── frame loop ──
    const wob = (ph, t, ch) => { const s = t / 1000, k = ch * 2; return (Math.sin(s * (0.55 + 0.07 * ch) + ph[k]) + 0.6 * Math.sin(s * 0.21 + ph[k + 1])) / 1.6; };
    const tmp = new THREE.Vector3();
    let raf = 0, last = 0;
    function frame(t) {
      raf = requestAnimationFrame(frame);
      if (document.hidden) return;
      const dt = Math.min(0.1, (t - last) / 1000); last = t;
      const live = drift && !reduced;
      // positions: base + a small wobble (capped in world units, so far nodes do not swing wide)
      for (const h of hubs) {
        h.pos.copy(h.base);
        if (live) h.pos.add(tmp.set(wob(h.ph, t, 0), wob(h.ph, t, 1), wob(h.ph, t, 2)).multiplyScalar(5));
        h.mesh.position.copy(h.pos); h.mesh.rotation.y += dt * 0.6;
      }
      for (const it of items) {
        const p = it.sprite.position;
        const hubShift = it.hub ? tmp.copy(it.hub.pos).sub(it.hub.base) : tmp.set(0, 0, 0);
        p.copy(it.base).add(hubShift);
        if (live && it.a.role !== 'entry') p.add(tmp.set(wob(it.ph, t, 0), wob(it.ph, t, 1), wob(it.ph, t, 2)).multiplyScalar(Math.min(0.08 * (it.rc || 40), 4)));
      }
      // node state: size ease, halos, glow, labels, filter
      for (const it of items) {
        const a = it.a, active = activeNames.has(a.name), blocked = blockedNames.has(a.name), selected = selectedId === a.id, hov = hoverIt === it;
        const goal = hov ? 1.5 : selected ? 1.2 : 1; it.k += (goal - it.k) * 0.2;
        const pulse = 0.5 + 0.5 * Math.sin(t / 220), s = it.size * it.k * (active ? 1 + 0.06 * pulse : 1);
        it.sprite.scale.set(s, s, 1); it.sprite.material.opacity = match(a) ? 1 : 0.2;
        it.halo.position.copy(it.sprite.position); it.glow.position.copy(it.sprite.position);
        it.halo.scale.set(s * 1.45, s * 1.45, 1);
        it.halo.material.color.copy(blocked ? C.err : selected || hov ? C.hi : C.accent);
        it.halo.material.opacity = blocked ? 0.5 + 0.5 * pulse : selected || hov ? 0.8 : active ? 0.6 : 0;
        const heat = a.role === 'entry' ? 0 : heatOf(a.name);
        it.glow.material.opacity = active ? 0.55 + 0.25 * pulse : heat > 0.15 ? heat * 0.4 : 0;
        it.glow.scale.set(s * 3, s * 3, 1);
        if (active || blocked) it.busyAt = Date.now();
        const want = hov || selected || active || blocked || a.role === 'entry' || Date.now() - (it.busyAt || 0) < 3500;
        it.lab += ((want ? 1 : 0) - it.lab) * 0.14;
        it.label.material.opacity = it.lab < 0.03 ? 0 : it.lab * (match(a) ? 1 : 0.25);
        it.label.position.copy(it.sprite.position).add(tmp.set(0, -(s * 0.5 + 12), 0));
      }
      for (const h of hubs) {
        const shown = items.some((i) => i.hub === h && (i.lab > 0.5 || activeNames.has(i.a.name)));
        h.lab += ((shown ? 1 : 0) - h.lab) * 0.14;
        h.label.material.opacity = h.lab < 0.03 ? 0 : h.lab; h.label.position.copy(h.pos).add(tmp.set(0, 12, 0));
      }
      // lines: Atlas → hub → member; the ones carrying a live delegation (or a blocked agent) light up
      let nb = 0, nl = 0, nr = 0;
      const put = (l, n, a, b) => { const arr = l.geometry.attributes.position.array; if (n * 6 + 5 >= arr.length) return n; arr[n * 6] = a.x; arr[n * 6 + 1] = a.y; arr[n * 6 + 2] = a.z; arr[n * 6 + 3] = b.x; arr[n * 6 + 4] = b.y; arr[n * 6 + 5] = b.z; return n + 1; };
      const atlas = api.atlasItem, ap = atlas ? atlas.sprite.position : null;
      for (const sg of segs) {
        if (!sg.it) { if (!ap) continue; const liveHub = items.some((i) => i.hub === sg.hub && liveNames.has(i.a.name)); if (liveHub) nl = put(lineLive, nl, ap, sg.hub.pos); else nb = put(lineBase, nb, ap, sg.hub.pos); continue; }
        if (liveNames.has(sg.it.a.name)) nl = put(lineLive, nl, sg.hub.pos, sg.it.sprite.position); else nb = put(lineBase, nb, sg.hub.pos, sg.it.sprite.position);
        if (ap && blockedNames.has(sg.it.a.name)) nr = put(lineBlocked, nr, sg.it.sprite.position, ap);
      }
      for (const [l, n] of [[lineBase, nb], [lineLive, nl], [lineBlocked, nr]]) { l.geometry.setDrawRange(0, n * 2); l.geometry.attributes.position.needsUpdate = true; }
      lineLive.material.opacity = 0.65 + 0.35 * Math.sin(t / 160);
      controls.autoRotate = spin && !reduced && !down; controls.update();
      renderer.render(scene, camera);
    }
    raf = requestAnimationFrame(frame);

    return () => {
      cancelAnimationFrame(raf); ro.disconnect();
      el.removeEventListener('pointerdown', onDown); window.removeEventListener('pointerup', onUp); el.removeEventListener('pointermove', onMove); el.removeEventListener('pointerleave', onLeave);
      clear(); disposables.forEach((d) => d.dispose?.()); controls.dispose(); renderer.dispose(); renderer.domElement.remove();
    };
  }
</script>

<div class="g3" bind:this={host}>
  <div class="bt">
    <button type="button" class:on={spin} onclick={toggleSpin} title="slowly turn the whole graph (drag to take over)" aria-pressed={spin}>⟳</button>
    <button type="button" onclick={() => api?.reset()} title="reset the view">⌖</button>
  </div>
  {#if tip}<div class="tip" style="left:{tip.x + 14}px; top:{tip.y + 14}px">{tip.text}</div>{/if}
</div>

<style>
  .g3 { position: absolute; inset: 0; overflow: hidden; }
  .bt { position: absolute; left: 8px; top: 8px; z-index: 4; display: flex; flex-direction: column; gap: 4px; }
  .bt button { width: 32px; height: 32px; border: 1px solid var(--line-2); background: var(--bg-1); color: var(--fg-hi); border-radius: var(--r); font-size: 16px; line-height: 1; cursor: pointer; }
  .bt button:hover { background: var(--bg-4); }
  .bt button.on { border-color: var(--accent); color: var(--accent-hi); }
  .tip { position: absolute; z-index: 5; max-width: 300px; padding: 5px 8px; background: var(--bg-1); border: 1px solid var(--line-2); border-radius: var(--r); color: var(--fg-hi); font-size: var(--fs-sm); pointer-events: none; box-shadow: 0 4px 14px rgba(0, 0, 0, 0.35); }
</style>
