<script>
  // Markdown → Svelte nodes (never innerHTML): headings, paragraphs, nested lists, tables,
  // blockquotes, rules, links, inline code, and code blocks with copy. Fenced json/yaml (or a
  // message that is entirely JSON) becomes a collapsible tree.
  import JsonTree from './JsonTree.svelte';
  import MediaKeep from './MediaKeep.svelte';
  import { artifactUrl } from '../ws.js';
  import { parseStructured } from './yaml.js';

  let { text = '' } = $props();
  let copied = $state(-1);
  const blocks = $derived(parse(text));

  const fence = /^\s*```\s*([^\s`]*)\s*$/;
  const heading = /^(#{1,6})\s+(.+?)\s*#*\s*$/;
  const item = /^(\s*)([-*+]|\d+[.)])\s+(.*)$/;
  const quote = /^\s*>\s?(.*)$/;
  const rule = /^\s*([-*_])(?:\s*\1){2,}\s*$/;
  const tdiv = /^\s*\|?\s*:?-{3,}:?\s*(?:\|\s*:?-{3,}:?\s*)+\|?\s*$/;
  const inlineRe = /(\[([^\]\n]+)\]\((https?:\/\/[^\s<>"'`)]+)\)|`([^`\n]+)`|\*\*([^*\n]+)\*\*|__([^_\n]+)__|\*([^*\n]+)\*|(?<![\w])_([^_\n]+)_(?![\w])|(https?:\/\/[^\s<>"'`]+)|~~([^~\n]+)~~|\[(audio|image):(\d+)\])/g;

  function row(line) { return line.trim().replace(/^\|/, '').replace(/\|$/, '').split('|').map((c) => c.trim()); }

  function buildList(lines) {
    // lines: [{ind, ordered, text}] → nested [{text, children, ordered}]
    const root = { children: [], ind: -1 };
    const stack = [root];
    for (const l of lines) {
      while (stack.length > 1 && stack.at(-1).ind >= l.ind) stack.pop();
      const tk = l.text.match(/^\[([ xX])\]\s+(.*)$/);
      const node = { text: tk ? tk[2] : l.text, task: tk ? (tk[1] === ' ' ? 'todo' : 'done') : '', ordered: l.ordered, ind: l.ind, children: [] };
      stack.at(-1).children.push(node);
      stack.push(node);
    }
    return root.children;
  }

  function parse(value) {
    const src = String(value ?? '').replace(/\r\n?/g, '\n');
    const whole = src.trim();
    if (whole.startsWith('{') || whole.startsWith('[')) {
      const s = parseStructured(whole, 'json');
      if (s) return [{ type: 'tree', data: s.data, kind: 'json' }];
    }
    const lines = src.split('\n');
    const out = [];
    let i = 0;
    while (i < lines.length) {
      const line = lines[i];
      const f = line.match(fence);
      if (f) {
        const code = [];
        i++;
        while (i < lines.length && !fence.test(lines[i])) code.push(lines[i++]);
        i++;
        const lang = (f[1] || 'text').toLowerCase();
        const body = code.join('\n');
        const s = ['json', 'yaml', 'yml'].includes(lang) ? parseStructured(body, lang) : null;
        out.push(s ? { type: 'tree', data: s.data, kind: s.kind, raw: body } : { type: 'code', lang, value: body });
        continue;
      }
      if (!line.trim()) { i++; continue; }
      const h = line.match(heading);
      if (h) { out.push({ type: 'heading', level: h[1].length, value: h[2] }); i++; continue; }
      if (rule.test(line)) { out.push({ type: 'rule' }); i++; continue; }
      if (i + 1 < lines.length && line.includes('|') && tdiv.test(lines[i + 1])) {
        const headers = row(line);
        const al = row(lines[i + 1]).map((c) => (c.startsWith(':') && c.endsWith(':') ? 'center' : c.endsWith(':') ? 'right' : 'left'));
        const rows = [];
        i += 2;
        while (i < lines.length && lines[i].trim() && lines[i].includes('|')) rows.push(row(lines[i++]));
        out.push({ type: 'table', headers, al, rows });
        continue;
      }
      if (quote.test(line)) {
        const q = [];
        while (i < lines.length && quote.test(lines[i])) q.push(lines[i++].match(quote)[1]);
        const co = q[0].match(/^\[!(NOTE|TIP|INFO|IMPORTANT|WARNING|CAUTION)\]\s*(.*)$/i);
        if (co) out.push({ type: 'callout', kind: co[1].toLowerCase(), lines: [co[2], ...q.slice(1)].filter((x, i) => i || x) });
        else out.push({ type: 'quote', lines: q });
        continue;
      }
      if (item.test(line)) {
        const ls = [];
        while (i < lines.length) {
          const m = lines[i].match(item);
          if (m) ls.push({ ind: m[1].length, ordered: /\d/.test(m[2]), text: m[3] });
          else if (lines[i].trim() && /^\s{2,}\S/.test(lines[i]) && ls.length) ls.at(-1).text += ' ' + lines[i].trim(); // wrapped item
          else break;
          i++;
        }
        out.push({ type: 'list', items: buildList(ls) });
        continue;
      }
      const para = [line];
      i++;
      while (i < lines.length && lines[i].trim() && !fence.test(lines[i]) && !heading.test(lines[i]) && !rule.test(lines[i]) && !quote.test(lines[i]) && !item.test(lines[i]) && !(lines[i].includes('|') && i + 1 < lines.length && tdiv.test(lines[i + 1]))) para.push(lines[i++]);
      out.push({ type: 'p', lines: para });
    }
    return out;
  }

  function trim(u) { let s = ''; while (/[.,;:!?)]$/.test(u)) { s = u.slice(-1) + s; u = u.slice(0, -1); } return [u, s]; }

  function inline(v) {
    const toks = [];
    let last = 0;
    for (const m of String(v).matchAll(inlineRe)) {
      if (m.index > last) toks.push({ t: 'text', v: v.slice(last, m.index) });
      if (m[2] && m[3]) toks.push({ t: 'link', label: m[2], href: m[3] });
      else if (m[4]) toks.push({ t: 'code', v: m[4] });
      else if (m[5] || m[6]) toks.push({ t: 'strong', v: m[5] || m[6] });
      else if (m[7] || m[8]) toks.push({ t: 'em', v: m[7] || m[8] });
      else if (m[10]) toks.push({ t: 'del', v: m[10] });
      else if (m[11]) toks.push({ t: 'media', kind: m[11], id: Number(m[12]) });
      else if (m[9]) { const [u, s] = trim(m[9]); toks.push({ t: 'link', label: u, href: u }); if (s) toks.push({ t: 'text', v: s }); }
      last = m.index + m[0].length;
    }
    if (last < v.length) toks.push({ t: 'text', v: v.slice(last) });
    return toks;
  }

  async function copy(i, v) {
    try { await navigator.clipboard.writeText(v); copied = i; setTimeout(() => copied === i && (copied = -1), 1500); } catch {}
  }
</script>

{#snippet inl(v)}{#each inline(v) as tk}{#if tk.t === 'text'}{tk.v}{:else if tk.t === 'link'}<a href={tk.href} target="_blank" rel="noopener noreferrer">{tk.label}</a>{:else if tk.t === 'code'}<code>{tk.v}</code>{:else if tk.t === 'strong'}<strong>{tk.v}</strong>{:else if tk.t === 'del'}<del>{tk.v}</del>{:else if tk.t === 'media'}{#if tk.kind === 'audio'}<audio class="media" controls preload="none" src={artifactUrl(tk.id)}></audio>{:else}<a href={artifactUrl(tk.id)} target="_blank" rel="noopener noreferrer"><img class="media" src={artifactUrl(tk.id)} alt="generated" loading="lazy" /></a><MediaKeep id={tk.id} />{/if}{:else}<em>{tk.v}</em>{/if}{/each}{/snippet}

{#snippet lst(items)}
  {#if items.length}
    {#if items[0].ordered}<ol>{#each items as it}<li class:task={it.task}>{#if it.task}<span class="cb" class:done={it.task === 'done'}></span>{/if}{@render inl(it.text)}{#if it.children.length}{@render lst(it.children)}{/if}</li>{/each}</ol>
    {:else}<ul>{#each items as it}<li class:task={it.task}>{#if it.task}<span class="cb" class:done={it.task === 'done'}></span>{/if}{@render inl(it.text)}{#if it.children.length}{@render lst(it.children)}{/if}</li>{/each}</ul>{/if}
  {/if}
{/snippet}

<div class="rm">
  {#each blocks as b, bi}
    {#if b.type === 'heading'}
      <div class="h h{b.level}">{@render inl(b.value)}</div>
    {:else if b.type === 'rule'}<hr />
    {:else if b.type === 'tree'}
      <div class="tree"><div class="bar"><span>{b.kind}</span>{#if b.raw}<button type="button" onclick={() => copy(bi, b.raw)}>{copied === bi ? 'copied' : 'copy'}</button>{/if}</div><div class="tb"><JsonTree data={b.data} /></div></div>
    {:else if b.type === 'code'}
      <div class="code"><div class="bar"><span>{b.lang}</span><button type="button" onclick={() => copy(bi, b.value)}>{copied === bi ? 'copied' : 'copy'}</button></div><pre><code>{b.value}</code></pre></div>
    {:else if b.type === 'table'}
      <div class="tw"><table>
        <thead><tr>{#each b.headers as hd, c}<th style="text-align:{b.al[c] || 'left'}">{@render inl(hd)}</th>{/each}</tr></thead>
        <tbody>{#each b.rows as r}<tr>{#each b.headers as _, c}<td style="text-align:{b.al[c] || 'left'}">{@render inl(r[c] ?? '')}</td>{/each}</tr>{/each}</tbody>
      </table></div>
    {:else if b.type === 'callout'}
      <div class="co {b.kind}"><div class="ct">{b.kind}</div>{#each b.lines as l, li}{@render inl(l)}{#if li < b.lines.length - 1}<br />{/if}{/each}</div>
    {:else if b.type === 'quote'}
      <blockquote>{#each b.lines as l, li}{@render inl(l)}{#if li < b.lines.length - 1}<br />{/if}{/each}</blockquote>
    {:else if b.type === 'list'}{@render lst(b.items)}
    {:else}<p>{#each b.lines as l, li}{@render inl(l)}{#if li < b.lines.length - 1}<br />{/if}{/each}</p>{/if}
  {/each}
</div>

<style>
  audio.media { display: block; margin: 4px 0; height: 32px; max-width: 100%; width: 320px; }
  img.media { display: block; margin: 4px 0; max-width: min(100%, 420px); max-height: 320px; border: 1px solid var(--line-2); }
  .rm { line-height: 1.6; overflow-wrap: anywhere; min-width: 0; }
  p { margin: 0 0 0.6em; } p:last-child { margin-bottom: 0; }
  .h { font-weight: 700; color: var(--fg-hi); margin: 1em 0 0.4em; line-height: 1.25; } .h:first-child { margin-top: 0; }
  .h1 { font-size: 1.6em; letter-spacing: 0.02em; padding-bottom: 0.2em; border-bottom: 1px solid var(--line-3); text-shadow: var(--glow-sm); }
  .h2 { font-size: 1.32em; padding-bottom: 0.15em; border-bottom: 1px solid var(--line-2); }
  .h3 { font-size: 1.14em; color: var(--accent-hi); }
  .h4 { font-size: 1em; color: var(--accent-hi); text-transform: uppercase; letter-spacing: 0.08em; }
  .h5, .h6 { font-size: 0.92em; color: var(--fg-dim); text-transform: uppercase; letter-spacing: 0.1em; }
  a { color: var(--accent-hi); text-decoration: underline; text-decoration-color: color-mix(in srgb, var(--accent) 50%, transparent); text-underline-offset: 2px; }
  strong { color: var(--fg-hi); font-weight: 700; } em { color: var(--fg-dim); } del { color: var(--fg-mute); text-decoration-color: var(--fg-faint); }
  code { background: var(--bg-3); padding: 1px 5px; color: var(--accent-hi); font-size: 0.93em; border: 1px solid var(--line); }
  ul, ol { margin: 0.4em 0 0.55em; padding-left: 1.5em; } li { margin: 0.16em 0; } li::marker { color: var(--accent-dim); }
  li > ul, li > ol { margin: 0.1em 0; }
  li.task { list-style: none; margin-left: -1.2em; }
  .cb { display: inline-block; width: 0.8em; height: 0.8em; border: 1px solid var(--fg-mute); margin-right: 0.55em; vertical-align: -0.06em; position: relative; }
  .cb.done { background: color-mix(in srgb, var(--fg) 25%, transparent); border-color: var(--fg); } .cb.done::after { content: ''; position: absolute; left: 0.2em; top: 0.02em; width: 0.22em; height: 0.44em; border: solid var(--fg-hi); border-width: 0 1.5px 1.5px 0; transform: rotate(40deg); }
  hr { border: 0; border-top: 1px solid var(--line-2); margin: 1em 0; }
  blockquote { margin: 0.6em 0; padding: 0.2em 0 0.2em 0.9em; color: var(--fg-dim); border-left: 2px solid var(--line-3); font-style: italic; }
  .co { margin: 0.6em 0; padding: 0.4em 0.8em 0.5em; border: 1px solid var(--k); border-left-width: 3px; background: color-mix(in srgb, var(--k) 8%, var(--bg)); --k: var(--accent); }
  .co .ct { text-transform: uppercase; letter-spacing: 0.12em; font-size: 0.8em; font-weight: 700; color: var(--k); margin-bottom: 0.15em; }
  .co.tip { --k: var(--fg); } .co.warning, .co.important { --k: var(--attn); } .co.caution { --k: var(--err); }
  .code, .tree { margin: 0.6em 0; border: 1px solid var(--line-2); background: var(--bg); }
  .bar { display: flex; justify-content: space-between; align-items: center; padding: 1px 8px; background: var(--bg-2); border-bottom: 1px solid var(--line); font-size: 10px; letter-spacing: 0.1em; text-transform: uppercase; color: var(--fg-mute); }
  .bar button { background: none; border: 0; color: var(--accent); font-size: 10px; letter-spacing: 0.1em; text-transform: uppercase; padding: 0 2px; } .bar button:hover { color: var(--accent-hi); }
  pre { margin: 0; border: 0; background: transparent; padding: 7px 10px; } pre code { background: none; padding: 0; border: 0; color: var(--fg); font-size: 12px; }
  .tb { padding: 6px 10px; overflow: auto; max-height: 360px; }
  .tw { margin: 0.7em 0; overflow-x: auto; border: 1px solid var(--line-2); background: var(--bg); box-shadow: 0 0 10px rgba(62, 232, 166, 0.04); }
  table { width: 100%; border-collapse: collapse; font-size: 0.95em; min-width: max-content; }
  th, td { padding: 4px 11px; vertical-align: top; border-bottom: 1px solid var(--line); }
  th { color: var(--accent-hi); background: var(--bg-2); font-weight: 700; text-transform: uppercase; letter-spacing: 0.06em; font-size: 0.84em; white-space: nowrap; border-bottom: 1px solid var(--accent-dim); }
  td:first-child { color: var(--fg-hi); }
  tbody tr:last-child td { border-bottom: 0; } tbody tr:nth-child(even) td { background: color-mix(in srgb, var(--bg-2) 60%, transparent); }
  tbody tr:hover td { background: var(--bg-3); }
</style>
