// Small line + word diff (LCS), no dependencies. Used to review proposed changes to an agent's soul,
// toolset and traits.

/** Longest-common-subsequence diff of two arrays → [{t:'eq'|'del'|'add', v}] */
function lcs(a, b) {
  // trim the common head/tail first: most edits touch a small part of a long text
  let s = 0;
  while (s < a.length && s < b.length && a[s] === b[s]) s++;
  let ea = a.length, eb = b.length;
  while (ea > s && eb > s && a[ea - 1] === b[eb - 1]) { ea--; eb--; }
  const A = a.slice(s, ea), B = b.slice(s, eb);
  const n = A.length, m = B.length;
  const out = a.slice(0, s).map((v) => ({ t: 'eq', v }));
  if (n * m > 4_000_000) { // pathological size: show it as one replaced block instead of freezing the tab
    A.forEach((v) => out.push({ t: 'del', v }));
    B.forEach((v) => out.push({ t: 'add', v }));
  } else {
    const w = m + 1;
    const dp = new Uint32Array((n + 1) * w);
    for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--)
      dp[i * w + j] = A[i] === B[j] ? dp[(i + 1) * w + j + 1] + 1 : Math.max(dp[(i + 1) * w + j], dp[i * w + j + 1]);
    let i = 0, j = 0;
    while (i < n && j < m) {
      if (A[i] === B[j]) { out.push({ t: 'eq', v: A[i] }); i++; j++; }
      else if (dp[(i + 1) * w + j] >= dp[i * w + j + 1]) out.push({ t: 'del', v: A[i++] });
      else out.push({ t: 'add', v: B[j++] });
    }
    while (i < n) out.push({ t: 'del', v: A[i++] });
    while (j < m) out.push({ t: 'add', v: B[j++] });
  }
  for (let k = ea; k < a.length; k++) out.push({ t: 'eq', v: a[k] });
  return out;
}

/** Word-level segments for a changed line pair → [oldSegs, newSegs], each [{v, hl}] */
function wordDiff(oldLine, newLine) {
  const tok = (s) => s.split(/(\s+)/).filter((x) => x !== '');
  const ops = lcs(tok(oldLine), tok(newLine));
  const o = [], n = [];
  const push = (arr, v, hl) => { const l = arr[arr.length - 1]; if (l && l.hl === hl) l.v += v; else arr.push({ v, hl }); };
  for (const op of ops) {
    if (op.t === 'eq') { push(o, op.v, false); push(n, op.v, false); }
    else if (op.t === 'del') push(o, op.v, true);
    else push(n, op.v, true);
  }
  return [o, n];
}

/**
 * Diff two texts line by line. Returns rows {t, a, b, ta, tb, segs}:
 * a/b are the 1-based line numbers on each side (null when absent), segs the word-level pieces of changed lines.
 */
export function diffText(oldText = '', newText = '') {
  const A = String(oldText).split('\n'), B = String(newText).split('\n');
  const ops = lcs(A, B);
  const rows = [];
  let a = 0, b = 0;
  for (let i = 0; i < ops.length;) {
    if (ops[i].t === 'eq') { rows.push({ t: 'eq', a: ++a, b: ++b, text: ops[i].v }); i++; continue; }
    // a run of deletions followed by additions is one edited block: pair the lines for word highlights
    const dels = [], adds = [];
    while (i < ops.length && ops[i].t === 'del') dels.push(ops[i++].v);
    while (i < ops.length && ops[i].t === 'add') adds.push(ops[i++].v);
    const pairs = Math.min(dels.length, adds.length);
    const dSegs = [], aSegs = [];
    for (let k = 0; k < pairs; k++) {
      const [o, n] = wordDiff(dels[k], adds[k]);
      // if the lines share almost nothing, highlighting every word is noise: mark the line as a whole
      const same = o.filter((s) => !s.hl).reduce((x, s) => x + s.v.length, 0);
      const ok = same >= 0.35 * Math.max(dels[k].length, adds[k].length);
      dSegs.push(ok ? o : null); aSegs.push(ok ? n : null);
    }
    dels.forEach((text, k) => rows.push({ t: 'del', a: ++a, b: null, text, segs: dSegs[k] || null }));
    adds.forEach((text, k) => rows.push({ t: 'add', a: null, b: ++b, text, segs: aSegs[k] || null }));
  }
  return rows;
}

export function diffStats(rows) {
  let add = 0, del = 0;
  for (const r of rows) { if (r.t === 'add') add++; else if (r.t === 'del') del++; }
  return { add, del, changed: add + del };
}

/** Collapse long unchanged stretches, keeping `ctx` lines around every change → rows or {t:'fold', n, rows} */
export function fold(rows, ctx = 3) {
  const keep = new Array(rows.length).fill(false);
  rows.forEach((r, i) => { if (r.t !== 'eq') for (let k = Math.max(0, i - ctx); k <= Math.min(rows.length - 1, i + ctx); k++) keep[k] = true; });
  const out = [];
  for (let i = 0; i < rows.length;) {
    if (keep[i] || !rows.some((r) => r.t !== 'eq')) { out.push(rows[i++]); continue; }
    let j = i;
    while (j < rows.length && !keep[j]) j++;
    if (j - i <= 2) { for (; i < j; i++) out.push(rows[i]); continue; } // not worth a fold
    out.push({ t: 'fold', n: j - i, rows: rows.slice(i, j) });
    i = j;
  }
  return out;
}
