<script>
  // A hexagon ring built from six trapezoids, text colour → accent neon (follows the theme).
  let { size = 26 } = $props();
  const R = 46, r = 22, cx = 50, cy = 50;
  const pt = (rad, k) => { const a = ((-90 + 60 * k) * Math.PI) / 180; return [cx + rad * Math.cos(a), cy + rad * Math.sin(a)]; };
  // t = 0 → the theme's text colour, t = 1 → its accent
  const mix = (t) => `color-mix(in srgb, var(--accent) ${Math.round(t * 100)}%, var(--fg))`;
  const segs = Array.from({ length: 6 }, (_, k) => {
    const p = [pt(R, k), pt(R, k + 1), pt(r, k + 1), pt(r, k)];
    const mx = p.reduce((s, q) => s + q[0], 0) / 4, my = p.reduce((s, q) => s + q[1], 0) / 4;
    const g = 0.9; // seam between segments
    return { d: p.map(([x, y]) => `${(mx + (x - mx) * g).toFixed(2)},${(my + (y - my) * g).toFixed(2)}`).join(' '), c: mix(k / 5) };
  });
</script>

<svg viewBox="0 0 100 100" width={size} height={size} aria-label="PRISM" class="logo">
  {#each segs as s}<polygon points={s.d} style="fill:{s.c};stroke:{s.c}" fill-opacity="0.85" stroke-width="1.5" />{/each}
</svg>

<style>
  .logo { filter: drop-shadow(0 0 2.5px var(--fg)) drop-shadow(0 0 7px rgb(var(--rgb-accent) / 0.55)); flex: none; }
</style>
