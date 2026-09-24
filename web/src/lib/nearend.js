// Svelte action: calls fn() whenever the element is scrolled to within 300px of its end, and once after
// mount/update in case the content does not fill the box yet. fn returns true while more may be loaded.
export function nearEnd(node, fn) {
  let busy = false;
  const check = async () => {
    if (busy || node.scrollTop + node.clientHeight < node.scrollHeight - 300) return;
    busy = true;
    const h = node.scrollHeight;
    try { await fn(); } finally { busy = false; }
    if (node.scrollHeight > h) setTimeout(check, 100); // the box may still not be full
  };
  node.addEventListener('scroll', check, { passive: true });
  const t = setTimeout(check, 400);
  return { update(f) { fn = f; }, destroy() { node.removeEventListener('scroll', check); clearTimeout(t); } };
}
