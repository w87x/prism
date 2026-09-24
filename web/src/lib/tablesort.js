// Click a column header of any `table.t` to sort its rows by that column (again: reverse, third click: back to
// the original order). Sorting works on what is displayed — a cell may carry data-sort="…" to give a better
// key (a rank behind a bar, a timestamp behind "5 min ago"). A row made of one wide cell (a detail row) stays
// attached to the row above it. Tables set data-nosort to opt out. Rows that appear later (a live update)
// are slotted into the current order.
const state = new WeakMap(); // table -> { col, dir, orig: Map(row -> index), busy }

const keyOf = (cell) => {
  const raw = cell?.dataset?.sort ?? cell?.textContent ?? '';
  const txt = String(raw).trim();
  const num = /^[-+]?\d[\d\s.,]*/.exec(txt);
  if (num) { const n = parseFloat(num[0].replace(/[\s,]/g, '')); if (!Number.isNaN(n)) return n; }
  return txt.toLowerCase();
};
const cmp = (a, b) => (typeof a === 'number' && typeof b === 'number') ? a - b
  : typeof a === 'number' ? -1 : typeof b === 'number' ? 1 : String(a).localeCompare(String(b), undefined, { numeric: true });

function groups(tbody) {
  const out = [];
  for (const tr of tbody.rows) {
    const detail = tr.cells.length === 1 && tr.cells[0].colSpan > 1;
    if (detail && out.length) out[out.length - 1].push(tr); else out.push([tr]);
  }
  return out;
}

function apply(table) {
  const st = state.get(table);
  if (!st) return;
  st.busy = true;
  for (const tbody of table.tBodies) {
    const gs = groups(tbody);
    for (const g of gs) if (!st.orig.has(g[0])) st.orig.set(g[0], st.orig.size + 1e6); // rows added later go last in the original order
    const sorted = st.col < 0
      ? [...gs].sort((a, b) => (st.orig.get(a[0]) ?? 0) - (st.orig.get(b[0]) ?? 0))
      : [...gs].sort((a, b) => st.dir * cmp(keyOf(a[0].cells[st.col]), keyOf(b[0].cells[st.col])));
    let prev = null;
    for (const g of sorted) for (const tr of g) {
      const at = prev ? prev.nextSibling : tbody.firstChild;
      if (at !== tr) tbody.insertBefore(tr, at);
      prev = tr;
    }
  }
  for (const [i, th] of [...(table.tHead?.rows[0]?.cells || [])].entries()) {
    if (st.col === i) th.setAttribute('aria-sort', st.dir > 0 ? 'ascending' : 'descending'); else th.removeAttribute('aria-sort');
  }
  queueMicrotask(() => { st.busy = false; });
}

export function installTableSort() {
  document.addEventListener('click', (e) => {
    const th = e.target.closest?.('table.t thead th');
    if (!th) return;
    const table = th.closest('table');
    if (table.hasAttribute('data-nosort') || !th.textContent.trim() || table.tBodies.length === 0) return;
    let st = state.get(table);
    if (!st) {
      const orig = new Map();
      for (const tbody of table.tBodies) groups(tbody).forEach((g, i) => orig.set(g[0], i));
      st = { col: -1, dir: 1, orig, busy: false };
      state.set(table, st);
      new MutationObserver(() => { if (!st.busy && st.col >= 0) apply(table); }).observe(table, { childList: true, subtree: true, characterData: true });
    }
    const i = th.cellIndex;
    if (st.col !== i) { st.col = i; st.dir = 1; } else if (st.dir === 1) st.dir = -1; else { st.col = -1; st.dir = 1; }
    apply(table);
  });
}
