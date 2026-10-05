// Procedural mineral textures. Nothing is pre-drawn: each texture is an SVG filter graph the browser evaluates itself —
// Perlin-style noise (feTurbulence) shaped into a mask, optionally layered, domain-warped, grown, or lit like a surface
// in relief — and the mask is then painted with the theme's own colours (body::after in themes.css).
//
// Each mineral is 2–4 styles (layers) blended into one; their alphas are added together:
//   type 'noise' (default)  alpha = curve(gain·noise + bias)
//   type 'light'            the noise is read as a height map and lit from a direction — embossed facets, glassy sheen
//   kind   'fractalNoise' (soft clouds) | 'turbulence' (creased ridges, veins)
//   freq   noise scale — one number, or "x y" to stretch the grain (long x ⇒ strata, long y ⇒ streaks); oct = octaves
//   gain/bias  stretch the noise before the curve; curve = alpha as a function of the value, low → high: one hump is a
//          soft glow, spikes are contour lines (agate bands), a ramp up is a threshold (speckle, stars); amp scales it
//   warp   {freq, oct, scale, seed}: bends the layer by a second noise (swirls, marbling) — needs cover:true, see below
//   grow   n: swell bright spots by n px (small dots → stars, bubbles)
//   light  {az, el, scale}: direction and relief of a 'light' layer
// A texture tiles seamlessly (default) or, with cover:true, is one image stretched over the whole screen — the only
// mode where warping is allowed, since a warp would break a tile's seams.
import { currentTheme } from './themes.js';

const hump = [0, 0.05, 0.3, 0.7, 1, 0.7, 0.3, 0.05, 0];
const bands = (a, b) => [0, 0, a, 0, 0, b, 0, 0, a, 0, 0, b * 0.8, 0, 0, a, 0, 0, b, 0, 0];

const MINERALS = {
  // zoned growth bands like agate / beryl, bent a little, with the tiny dark inclusions emeralds are known for
  emerald: { cover: true, layers: [
    { freq: 0.0085, oct: 3, seed: 3, gain: 2.6, bias: -0.8, curve: bands(0.9, 0.55), warp: { freq: 0.005, oct: 2, scale: 140, seed: 7 } },
    { freq: 0.35, oct: 1, seed: 12, gain: 3, bias: -1.35, curve: [0, 0, 0, 0, 0, 0, 0, 0, 0, 0.3, 1], grow: 1, amp: 0.8 },
    { kind: 'turbulence', freq: 0.018, oct: 3, seed: 27, gain: 1, bias: 0, curve: [1, 0.45, 0, 0, 0, 0, 0, 0, 0, 0], amp: 0.4, warp: { freq: 0.006, oct: 2, scale: 90, seed: 4 } },   // the fine cracks (jardin) of a real emerald
  ] },
  // a galaxy in a pocket: swirling nebula, thin spiral dust lanes, a few glowing cores, and a field of stars
  tanzanite: { cover: true, layers: [
    { freq: 0.0042, oct: 5, seed: 4, gain: 2.7, bias: -0.85, curve: hump, amp: 0.8, warp: { freq: 0.0032, oct: 2, scale: 280, seed: 9 } },
    { kind: 'turbulence', freq: 0.0075, oct: 4, seed: 2, gain: 1, bias: 0, curve: [1, 0.55, 0, 0, 0, 0, 0, 0, 0, 0], amp: 0.5, warp: { freq: 0.0032, oct: 2, scale: 280, seed: 9 } },
    { freq: 0.0016, oct: 1, seed: 21, gain: 5, bias: -2.1, curve: hump, amp: 0.9 },
    { freq: 0.6, oct: 1, seed: 5, gain: 3, bias: -1.35, curve: [0, 0, 0, 0, 0, 0, 0, 0, 0, 0.3, 1], grow: 1, amp: 1 },
  ] },
  // creased veins running through ore, with the odd metallic glint
  cobaltite: { layers: [
    { kind: 'turbulence', freq: 0.009, oct: 4, seed: 5, gain: 1, bias: 0, curve: [1, 0.7, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0] },
    { freq: 0.3, oct: 1, seed: 40, gain: 3, bias: -1.35, curve: [0, 0, 0, 0, 0, 0, 0, 0, 0, 0.3, 1], amp: 0.9 },
    { freq: 0.004, oct: 2, seed: 33, gain: 2.4, bias: -0.7, curve: hump, amp: 0.35 },   // a broad metallic sheen over the ore
  ] },
  // resin flowing in slow strata, with bubbles caught in it
  amber: { cover: true, layers: [
    { freq: '0.0025 0.02', oct: 2, seed: 8, gain: 2.6, bias: -0.8, curve: bands(0.75, 0.45), warp: { freq: '0.003 0.006', oct: 2, scale: 90, seed: 3 } },
    { freq: 0.06, oct: 1, seed: 15, gain: 3, bias: -1.35, curve: [0, 0, 0, 0, 0, 0, 0, 0, 0, 0.3, 1], grow: 2, amp: 0.8 },
    { type: 'light', freq: 0.007, oct: 2, seed: 4, gain: 3, bias: -1.2, curve: [0, 0, 0, 0.1, 0.5, 1], amp: 0.4, light: { az: 70, el: 40, scale: 9 } },   // the glossy surface of polished resin
  ] },
  // a druzy crust of tiny crystals, lit so each one has a bright facet
  amethyst: { layers: [
    { type: 'light', kind: 'turbulence', freq: 0.035, oct: 2, seed: 21, gain: 2.2, bias: -0.2, curve: [0, 0, 0.1, 0.5, 0.9, 1], amp: 0.8, light: { az: 55, el: 38, scale: 6 } },
    { freq: 0.006, oct: 3, seed: 8, gain: 2.5, bias: -0.75, curve: hump, amp: 0.4 },   // the deeper glow of the geode
    { freq: 0.5, oct: 1, seed: 19, gain: 3, bias: -1.35, curve: [0, 0, 0, 0, 0, 0, 0, 0, 0, 0.3, 1], grow: 1, amp: 0.9 },   // sparks off single crystals
  ] },
  // soft shifting bands of colour with fleeting bright flecks
  opal: { cover: true, layers: [
    { freq: 0.014, oct: 3, seed: 14, gain: 2.5, bias: -0.75, curve: [0, 0.15, 0.7, 0.2, 0, 0.1, 0.8, 0.15, 0, 0, 0.6, 0.1, 0, 0], warp: { freq: 0.006, oct: 2, scale: 120, seed: 5 } },
    { freq: 0.18, oct: 1, seed: 30, gain: 3, bias: -1.35, curve: [0, 0, 0, 0, 0, 0, 0, 0, 0, 0.3, 1], grow: 1, amp: 0.8 },
    { type: 'light', freq: 0.01, oct: 2, seed: 2, gain: 3, bias: -1.2, curve: [0, 0, 0, 0.1, 0.5, 1], amp: 0.35, light: { az: 100, el: 45, scale: 7 } },   // the smooth, rounded surface of the stone
  ] },
  // growth striations along the crystal
  aquamarine: { layers: [
    { freq: '0.045 0.004', oct: 2, seed: 6, gain: 2.6, bias: -0.8, curve: bands(0.6, 0.7) },
    { freq: '0.02 0.0035', oct: 2, seed: 19, gain: 2.4, bias: -0.7, curve: hump, amp: 0.3 },
    { type: 'light', freq: '0.03 0.008', oct: 2, seed: 12, gain: 3, bias: -1.2, curve: [0, 0, 0, 0.1, 0.5, 1], amp: 0.3, light: { az: 90, el: 35, scale: 6 } },   // light caught on the facets of the prism
  ] },
  // adularescence: a milky sheen floating over a smooth, glassy relief
  moonstone: { cover: true, layers: [
    { freq: '0.004 0.012', oct: 2, seed: 17, gain: 2.4, bias: -0.7, curve: hump, amp: 0.7, warp: { freq: 0.004, oct: 2, scale: 160, seed: 2 } },
    { type: 'light', freq: 0.006, oct: 3, seed: 6, gain: 3, bias: -1.2, curve: [0, 0, 0, 0.1, 0.5, 1], amp: 0.6, light: { az: 120, el: 30, scale: 12 } },
    { freq: 0.25, oct: 1, seed: 44, gain: 3, bias: -1.35, curve: [0, 0, 0, 0, 0, 0, 0, 0, 0, 0.3, 1], grow: 1, amp: 0.7 },   // pin-points of light in the sheen
  ] },
  // volcanic glass: smooth conchoidal relief with fine fracture contours
  obsidian: { layers: [
    { type: 'light', freq: 0.011, oct: 4, seed: 9, gain: 3, bias: -1.3, curve: [0, 0, 0, 0.1, 0.5, 1], amp: 0.7, light: { az: 40, el: 32, scale: 10 } },
    { freq: 0.014, oct: 4, seed: 9, gain: 2.8, bias: -0.9, curve: [0, 0, 0, 0.95, 0, 0, 0, 0, 0.8, 0, 0, 0, 0, 0.95, 0, 0, 0, 0, 0.7, 0, 0], amp: 0.8 },
    { freq: 0.4, oct: 1, seed: 55, gain: 3, bias: -1.35, curve: [0, 0, 0, 0, 0, 0, 0, 0, 0, 0.3, 1], amp: 0.8 },   // tiny glints on the glass
  ] },
};
const TILE = 480;            // a seamless tile, px
const COVER = [1440, 900];   // a screen-filling image, px (stretched with mask-size: cover)

function svgUrl(svg) {
  return `url("data:image/svg+xml;utf8,${svg.replace(/#/g, '%23').replace(/</g, '%3C').replace(/>/g, '%3E')}")`;
}

const noiseEl = (L, id, o = {}) =>
  `<feTurbulence type='${o.kind || L.kind || 'fractalNoise'}' baseFrequency='${o.freq ?? L.freq}' numOctaves='${o.oct ?? L.oct ?? 1}' seed='${o.seed ?? L.seed ?? 1}' stitchTiles='stitch' result='${id}'/>`;

// one layer → filter primitives ending in an alpha-only image called L{i}
function layer(L, i, cover) {
  const amp = L.amp ?? 1;
  const curve = L.curve.map((v) => +(v * amp).toFixed(3)).join(' ');
  let s = noiseEl(L, `n${i}`);
  if (L.type === 'light') {
    const li = L.light || {};
    // lit height map → brightness as alpha (flat areas sit at mid-grey; relief catches or loses the light)
    s += `<feDiffuseLighting in='n${i}' surfaceScale='${li.scale ?? 8}' diffuseConstant='1' lighting-color='white' result='s${i}'><feDistantLight azimuth='${li.az ?? 45}' elevation='${li.el ?? 35}'/></feDiffuseLighting>`;
    s += `<feColorMatrix in='s${i}' type='matrix' values='0 0 0 0 1  0 0 0 0 1  0 0 0 0 1  ${L.gain} 0 0 0 ${L.bias}' result='m${i}'/>`;
    s += `<feComponentTransfer in='m${i}' result='c${i}'><feFuncA type='table' tableValues='${curve}'/></feComponentTransfer>`;
  } else {
    s += `<feColorMatrix in='n${i}' type='matrix' values='0 0 0 0 1  0 0 0 0 1  0 0 0 0 1  ${L.gain} 0 0 0 ${L.bias}' result='m${i}'/>`;
    s += `<feComponentTransfer in='m${i}' result='c${i}'><feFuncA type='table' tableValues='${curve}'/></feComponentTransfer>`;
  }
  let out = `c${i}`;
  if (L.warp && cover) {
    const w = L.warp;
    s += noiseEl(L, `w${i}`, { kind: 'fractalNoise', freq: w.freq, oct: w.oct, seed: w.seed });
    s += `<feDisplacementMap in='${out}' in2='w${i}' scale='${w.scale}' xChannelSelector='R' yChannelSelector='G' result='d${i}'/>`;
    out = `d${i}`;
  }
  if (L.grow) { s += `<feMorphology in='${out}' operator='dilate' radius='${L.grow}' result='g${i}'/>`; out = `g${i}`; }
  return s + `<feOffset in='${out}' dx='0' dy='0' result='L${i}'/>`;
}

function noise(m) {
  const [w, h] = m.cover ? COVER : [TILE, TILE];
  let f = '';
  m.layers.forEach((L, i) => { f += layer(L, i, m.cover); });
  // layers add up (alpha is clamped at 1)
  let acc = 'L0';
  for (let i = 1; i < m.layers.length; i++) {
    f += `<feComposite in='${acc}' in2='L${i}' operator='arithmetic' k1='0' k2='1' k3='1' k4='0' result='a${i}'/>`;
    acc = `a${i}`;
  }
  return { url: svgUrl(`<svg xmlns='http://www.w3.org/2000/svg' width='${w}' height='${h}' viewBox='0 0 ${w} ${h}' preserveAspectRatio='none'>` +
    `<filter id='f' x='0' y='0' width='100%' height='100%' color-interpolation-filters='sRGB'>${f}</filter><rect width='100%' height='100%' filter='url(#f)'/></svg>`),
    size: m.cover ? 'cover' : `${TILE}px ${TILE}px` };
}

// the plain grid, for anyone who prefers it
function grid() {
  const s = 19, n = 12, W = s * n;
  let d = '';
  for (let i = 0; i < n; i++) d += `M${i * s},0V${W}M0,${i * s}H${W}`;
  return { url: svgUrl(`<svg xmlns='http://www.w3.org/2000/svg' width='${W}' height='${W}'><path d='${d}' stroke='white' stroke-width='0.5' fill='none'/></svg>`), size: `${W}px ${W}px` };
}

// Point --tex / --tex-size at the right pattern for the current theme and texture choice. Called by applyTheme/applyTexture.
export function paintTexture(theme = currentTheme(), mode = document.documentElement.dataset.texture || 'mineral') {
  const root = document.documentElement;
  if (mode === 'none') return;
  const t = mode === 'grid' ? grid() : noise(MINERALS[theme] || MINERALS.emerald);
  root.style.setProperty('--tex', t.url);
  root.style.setProperty('--tex-size', t.size);
  root.style.setProperty('--tex-repeat', t.size === 'cover' ? 'no-repeat' : 'repeat');
}
