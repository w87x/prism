// Procedural mineral textures. Nothing is pre-drawn: each is an SVG <feTurbulence> (Perlin-style noise) that the browser
// renders itself, shaped into a mask by a transfer curve. The mask is painted with the theme's own colours (see
// body::after in themes.css). Tiles stitch seamlessly. To change a texture, edit its numbers here:
//   freq   noise scale — one number, or "x y" to stretch the grain (a long x ⇒ horizontal strata, a long y ⇒ streaks)
//   oct    octaves: more = finer detail layered over the broad shape
//   kind   'fractalNoise' (soft clouds) or 'turbulence' (creased, vein-like ridges)
//   gain/bias  stretch the noise before the curve (alpha = gain·noise + bias), so the curve sees a full range
//   curve  alpha as a function of noise value, low → high: a single hump = a soft glow, spikes = contour lines
//          (agate bands, strata), a ramp up = a threshold (crystal speckle)
import { currentTheme } from './themes.js';

const MINERALS = {
  emerald:    { freq: '0.011',        oct: 3, kind: 'fractalNoise', seed: 3,  gain: 2.6, bias: -0.8, curve: [0, 0, 0.85, 0, 0, 0.55, 0, 0, 0.85, 0, 0, 0.4, 0, 0, 0.85, 0, 0, 0.55, 0, 0] },   // zoned banding, like growth rings in beryl
  tanzanite:  { freq: '0.007',        oct: 2, kind: 'fractalNoise', seed: 11, gain: 2.4, bias: -0.7, curve: [0, 0.05, 0.4, 0.9, 1, 0.9, 0.4, 0.05, 0] },                                                         // broad flashes of light inside the stone
  cobaltite:  { freq: '0.009',        oct: 4, kind: 'turbulence',   seed: 5,  gain: 1,   bias: 0,    curve: [1, 0.7, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0] },                                                          // creased veins through ore
  amber:      { freq: '0.003 0.024',  oct: 2, kind: 'fractalNoise', seed: 8,  gain: 2.6, bias: -0.8, curve: [0, 0, 0.75, 0, 0, 0.45, 0, 0, 0.75, 0, 0, 0.45, 0, 0, 0.75, 0, 0] },                              // resin flowing in slow strata
  amethyst:   { freq: '0.05',         oct: 2, kind: 'fractalNoise', seed: 21, gain: 3.2, bias: -1.2, curve: [0, 0, 0, 0, 0, 0.15, 0.7, 1, 1, 1, 1] },                                                          // druzy: a crust of tiny crystals
  opal:       { freq: '0.017',        oct: 3, kind: 'fractalNoise', seed: 14, gain: 2.5, bias: -0.75, curve: [0, 0.15, 0.7, 0.2, 0, 0.1, 0.8, 0.15, 0, 0, 0.6, 0.1, 0, 0] },                                     // soft, shifting bands of colour
  aquamarine: { freq: '0.045 0.004',  oct: 2, kind: 'fractalNoise', seed: 6,  gain: 2.6, bias: -0.8, curve: [0, 0, 0.6, 0, 0.3, 0, 0, 0.7, 0, 0.3, 0, 0, 0.6, 0, 0.3, 0, 0, 0.7, 0] },                        // growth striations along the crystal
  moonstone:  { freq: '0.004 0.014',  oct: 2, kind: 'fractalNoise', seed: 17, gain: 2.4, bias: -0.7, curve: [0, 0.2, 0.7, 0.2, 0, 0, 0.2, 0.7, 0.2, 0, 0, 0.2, 0.7, 0.2, 0] },                                    // a floating, milky sheen
  obsidian:   { freq: '0.014',        oct: 4, kind: 'fractalNoise', seed: 9,  gain: 2.8, bias: -0.9, curve: [0, 0, 0, 0.95, 0, 0, 0, 0, 0.8, 0, 0, 0, 0, 0.95, 0, 0, 0, 0, 0.7, 0, 0] },                      // conchoidal fracture contours
};
const SIZE = 480;

function svgUrl(svg) {
  return `url("data:image/svg+xml;utf8,${svg.replace(/#/g, '%23').replace(/</g, '%3C').replace(/>/g, '%3E')}")`;
}

function noise(m) {
  const svg = `<svg xmlns='http://www.w3.org/2000/svg' width='${SIZE}' height='${SIZE}'>` +
    `<filter id='n' x='0' y='0' width='100%' height='100%' color-interpolation-filters='sRGB'>` +
    `<feTurbulence type='${m.kind}' baseFrequency='${m.freq}' numOctaves='${m.oct}' seed='${m.seed}' stitchTiles='stitch'/>` +
    `<feColorMatrix type='matrix' values='0 0 0 0 1  0 0 0 0 1  0 0 0 0 1  ${m.gain} 0 0 0 ${m.bias}'/>` +
    `<feComponentTransfer><feFuncA type='table' tableValues='${m.curve.join(' ')}'/></feComponentTransfer>` +
    `</filter><rect width='100%' height='100%' filter='url(#n)'/></svg>`;
  return svgUrl(svg);
}

// the plain grid, for anyone who prefers it
function grid() {
  const s = 19, n = 12, W = s * n;
  let d = '';
  for (let i = 0; i < n; i++) d += `M${i * s},0V${W}M0,${i * s}H${W}`;
  return { url: svgUrl(`<svg xmlns='http://www.w3.org/2000/svg' width='${W}' height='${W}'><path d='${d}' stroke='white' stroke-width='0.5' fill='none'/></svg>`), size: W };
}

// Point --tex / --tex-size at the right pattern for the current theme and texture choice. Called by applyTheme/applyTexture.
export function paintTexture(theme = currentTheme(), mode = document.documentElement.dataset.texture || 'mineral') {
  const root = document.documentElement;
  if (mode === 'none') return;
  const t = mode === 'grid' ? grid() : { url: noise(MINERALS[theme] || MINERALS.emerald), size: SIZE };
  root.style.setProperty('--tex', t.url);
  root.style.setProperty('--tex-size', `${t.size}px ${t.size}px`);
}
