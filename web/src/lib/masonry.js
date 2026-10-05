// masonry — Svelte action: lays the children of a grid container out so each lands in the shortest column
// (CSS grid with 1px rows + dense packing; every child spans as many rows as it is tall). Re-measures when a
// child resizes, collapses or its content changes. Container CSS: see .masonry in app.css.
export function masonry(node, gap = 8) {
  const ro = new ResizeObserver(() => fit());
  let seen = new Set();
  function fit() {
    for (const el of node.children) {
      if (!seen.has(el)) { ro.observe(el); seen.add(el); }
      const h = el.getBoundingClientRect().height;
      el.style.gridRowEnd = `span ${Math.max(1, Math.ceil(h + gap))}`;
    }
  }
  const mo = new MutationObserver(fit);
  mo.observe(node, { childList: true });
  ro.observe(node);
  fit();
  return { destroy() { ro.disconnect(); mo.disconnect(); } };
}
