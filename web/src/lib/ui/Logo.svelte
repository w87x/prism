<script>
  // A hexagon ring built from six trapezoids, green → blue neon.
  let { size = 26 } = $props();
  const R = 46, r = 22, cx = 50, cy = 50;
  const pt = (rad, k) => { const a = ((-90 + 60 * k) * Math.PI) / 180; return [cx + rad * Math.cos(a), cy + rad * Math.sin(a)]; };
  const mix = (t) => { // green #3ee8a6 → blue #4499ee
    const a = [0x3e, 0xe8, 0xa6], b = [0x44, 0x99, 0xee];
    return '#' + a.map((v, i) => Math.round(v + (b[i] - v) * t).toString(16).padStart(2, '0')).join('');
  };
  const segs = Array.from({ length: 6 }, (_, k) => {
    const p = [pt(R, k), pt(R, k + 1), pt(r, k + 1), pt(r, k)];
    const mx = p.reduce((s, q) => s + q[0], 0) / 4, my = p.reduce((s, q) => s + q[1], 0) / 4;
    const g = 0.9; // seam between segments
    return { d: p.map(([x, y]) => `${(mx + (x - mx) * g).toFixed(2)},${(my + (y - my) * g).toFixed(2)}`).join(' '), c: mix(k / 5) };
  });
</script>

<svg viewBox="0 0 100 100" width={size} height={size} aria-label="PRISM" class="logo">
  {#each segs as s}<polygon points={s.d} fill={s.c} fill-opacity="0.85" stroke={s.c} stroke-width="1.5" />{/each}
</svg>

<style>
  .logo { filter: drop-shadow(0 0 2.5px #3ee8a6) drop-shadow(0 0 7px rgba(68, 153, 238, 0.55)); flex: none; }
</style>
