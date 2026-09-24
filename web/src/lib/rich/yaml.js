// A small YAML subset parser for rendering: mappings, sequences, nested by indentation,
// scalars, quoted strings, inline [a, b] / {a: 1}, block scalars (| and >), comments.
// It throws on anything it does not understand so the caller can fall back to plain text.

function scalar(v) {
  v = v.trim();
  if (v === '' || v === '~' || v === 'null') return null;
  if (v === 'true') return true;
  if (v === 'false') return false;
  if (/^-?\d+(\.\d+)?$/.test(v)) return Number(v);
  if ((v.startsWith('"') && v.endsWith('"')) || (v.startsWith("'") && v.endsWith("'"))) return v.slice(1, -1);
  if ((v.startsWith('[') && v.endsWith(']')) || (v.startsWith('{') && v.endsWith('}'))) {
    try { return JSON.parse(v); } catch { return flow(v); }
  }
  return v.replace(/\s+#.*$/, '');
}

function flow(v) {
  if (v.startsWith('[')) {
    const inner = v.slice(1, -1).trim();
    return inner ? inner.split(',').map((x) => scalar(x)) : [];
  }
  const o = {};
  const inner = v.slice(1, -1).trim();
  if (inner) for (const part of inner.split(',')) { const i = part.indexOf(':'); if (i < 0) throw new Error('flow map'); o[part.slice(0, i).trim()] = scalar(part.slice(i + 1)); }
  return o;
}

export function parseYaml(src) {
  const lines = src.replace(/\t/g, '  ').split('\n').map((raw) => ({ raw, ind: raw.match(/^ */)[0].length, t: raw.trim() })).filter((l) => l.t && !l.t.startsWith('#') && l.t !== '---' && l.t !== '...');
  if (!lines.length) throw new Error('empty');
  let pos = 0;

  function block(ind) {
    const first = lines[pos];
    if (first.t.startsWith('- ') || first.t === '-') return seq(first.ind);
    if (/^[^\s:][^:]*:(\s|$)/.test(first.t)) return map(first.ind);
    pos++;
    return scalar(first.t);
  }
  function value(rest, ind) {
    if (rest === '|' || rest === '>' || rest === '|-' || rest === '>-') {
      const buf = [];
      while (pos < lines.length && lines[pos].ind > ind) buf.push(lines[pos++].t);
      return buf.join(rest.startsWith('|') ? '\n' : ' ');
    }
    if (rest === '') {
      if (pos < lines.length && (lines[pos].ind > ind || (lines[pos].ind === ind && lines[pos].t.startsWith('- ')))) return block(lines[pos].ind);
      return null;
    }
    return scalar(rest);
  }
  function map(ind) {
    const o = {};
    while (pos < lines.length && lines[pos].ind === ind && !lines[pos].t.startsWith('- ')) {
      const m = lines[pos].t.match(/^("[^"]*"|'[^']*'|[^:]+?):(?:\s+(.*))?$/);
      if (!m) throw new Error('not a mapping line: ' + lines[pos].t);
      pos++;
      o[m[1].replace(/^["']|["']$/g, '')] = value((m[2] ?? '').trim(), ind);
    }
    return o;
  }
  function seq(ind) {
    const a = [];
    while (pos < lines.length && lines[pos].ind === ind && (lines[pos].t.startsWith('- ') || lines[pos].t === '-')) {
      const rest = lines[pos].t.slice(1).trim();
      if (/^[^\s:][^:]*:(\s|$)/.test(rest) && !rest.startsWith('"') && !rest.startsWith('[')) {
        // "- key: value" starts an inline mapping whose further keys are indented past the dash
        lines[pos] = { raw: '', ind: ind + 2, t: rest };
        a.push(map(ind + 2));
      } else { pos++; a.push(value(rest, ind)); }
    }
    return a;
  }
  const out = block(lines[0].ind);
  if (pos < lines.length) throw new Error('trailing content');
  return out;
}

/** Try to interpret text as JSON or YAML; returns {kind, data} or null. */
export function parseStructured(text, hint) {
  const t = text.trim();
  if (!t) return null;
  if (hint === 'json' || t.startsWith('{') || t.startsWith('[')) {
    try { const d = JSON.parse(t); if (d !== null && typeof d === 'object') return { kind: 'json', data: d }; } catch { if (hint === 'json') return null; }
  }
  if (hint === 'yaml' || hint === 'yml') {
    try { const d = parseYaml(t); if (d !== null && typeof d === 'object') return { kind: 'yaml', data: d }; } catch {}
  }
  return null;
}
