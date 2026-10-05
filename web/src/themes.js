// Colour themes. The tokens live in app.css (default "terminal") and themes.css; this picks one, remembers it
// on this device, and applies it before the app mounts so there is no flash of the default colours.
export const THEMES = [
  { id: 'terminal', name: 'Terminal', blurb: 'mint on black — the original', swatch: ['#030806', '#3ee8a6', '#4499ee', '#ff9900'] },
  { id: 'tanzanite', name: 'Tanzanite', blurb: 'blue-violet gemstone, a galaxy in a pocket', swatch: ['#04021a', '#a99bff', '#4fb4ff', '#ff48c8'] },
  { id: 'cobalt', name: 'Cobalt', blurb: 'blue and silver, orange accent', swatch: ['#030914', '#c4d2e6', '#26518f', '#ff8a1f'] },
];
const KEY = 'prism.theme';

export function currentTheme() {
  try { const t = JSON.parse(localStorage.getItem(KEY)); if (THEMES.some((x) => x.id === t)) return t; } catch {}
  return 'terminal';
}

export function applyTheme(id, save = true) {
  if (!THEMES.some((x) => x.id === id)) id = 'terminal';
  const root = document.documentElement;
  if (id === 'terminal') delete root.dataset.theme; else root.dataset.theme = id;
  // the browser chrome (iOS status bar, tab strip) follows the page background
  const bg = getComputedStyle(root).getPropertyValue('--bg').trim();
  if (bg) document.querySelector('meta[name="theme-color"]')?.setAttribute('content', bg);
  if (save) { try { localStorage.setItem(KEY, JSON.stringify(id)); } catch {} }
  window.dispatchEvent(new CustomEvent('prism-theme', { detail: id }));
  return id;
}
