// Colour themes. The tokens live in app.css (default "emerald") and themes.css; this picks one, remembers it
// on this device, and applies it before the app mounts so there is no flash of the default colours.
export const THEMES = [
  { id: 'emerald', name: 'Emerald', blurb: 'mint on black — the original', swatch: ['#030806', '#3ee8a6', '#4499ee', '#ff9900'] },
  { id: 'tanzanite', name: 'Tanzanite', blurb: 'blue-violet gemstone, a galaxy in a pocket', swatch: ['#04021a', '#a99bff', '#4fb4ff', '#ff48c8'] },
  { id: 'cobaltite', name: 'Cobaltite', blurb: 'blue and silver, orange accent', swatch: ['#030914', '#c4d2e6', '#26518f', '#ff8a1f'] },
  { id: 'amber', name: 'Amber', blurb: 'honey resin with a warm glow', swatch: ['#0d0702', '#ffb347', '#a8661f', '#5eb8ff'] },
  { id: 'amethyst', name: 'Amethyst', blurb: 'lilac crystal, magenta depth', swatch: ['#0b0414', '#d3a6ff', '#7c3dd6', '#5ee0c8'] },
  { id: 'opal', name: 'Opal', blurb: 'pale stone, slowly moving flashes of colour', swatch: ['#060a11', '#bfe9ee', '#4a7d97', '#ff9ee0'] },
  { id: 'aquamarine', name: 'Aquamarine', blurb: 'clear sea-water, light drifting through it', swatch: ['#021416', '#7fe8f2', '#1c6b70', '#7aa8ff'] },
  { id: 'moonstone', name: 'Moonstone', blurb: 'milky blue-white with a slow floating sheen', swatch: ['#090c14', '#d6def5', '#5a6b96', '#8fa8ff'] },
  { id: 'obsidian', name: 'Obsidian', blurb: 'volcanic glass: neutral greys, cool cyan', swatch: ['#060606', '#d4d4d8', '#5b5e66', '#44d9e6'] },
];
// names used by earlier versions
const LEGACY = { terminal: 'emerald', cobalt: 'cobaltite' };
const KEY = 'prism.theme';

export function currentTheme() {
  try { let t = JSON.parse(localStorage.getItem(KEY)); t = LEGACY[t] || t; if (THEMES.some((x) => x.id === t)) return t; } catch {}
  return 'emerald';
}

export function applyTheme(id, save = true) {
  id = LEGACY[id] || id;
  if (!THEMES.some((x) => x.id === id)) id = 'emerald';
  const root = document.documentElement;
  if (id === 'emerald') delete root.dataset.theme; else root.dataset.theme = id; // Emerald is the base tokens in app.css
  // the browser chrome (iOS status bar, tab strip) follows the page background
  const bg = getComputedStyle(root).getPropertyValue('--bg').trim();
  if (bg) document.querySelector('meta[name="theme-color"]')?.setAttribute('content', bg);
  if (save) { try { localStorage.setItem(KEY, JSON.stringify(id)); } catch {} }
  window.dispatchEvent(new CustomEvent('prism-theme', { detail: id }));
  return id;
}

// Texture: "mineral" (each theme's own pattern, the default), the plain "grid", or "none". Remembered per device.
export const TEXTURES = [{ id: 'mineral', name: 'Mineral' }, { id: 'grid', name: 'Grid' }, { id: 'none', name: 'None' }];
const TKEY = 'prism.texture';

export function currentTexture() {
  try { const t = JSON.parse(localStorage.getItem(TKEY)); if (TEXTURES.some((x) => x.id === t)) return t; } catch {}
  return 'mineral';
}

export function applyTexture(id, save = true) {
  if (!TEXTURES.some((x) => x.id === id)) id = 'mineral';
  const root = document.documentElement;
  if (id === 'mineral') delete root.dataset.texture; else root.dataset.texture = id;
  if (save) { try { localStorage.setItem(TKEY, JSON.stringify(id)); } catch {} }
  return id;
}
