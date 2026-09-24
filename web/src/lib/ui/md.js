// Small, safe markdown → HTML: everything is escaped first, then a subset is re-introduced.
const esc = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');

export function md(src = '') {
  const stash = [];
  const put = (h) => `@@PRISM${stash.push(h) - 1}@@`;
  let s = String(src).replace(/@@PRISM(\d+)@@/g, '@ @PRISM$1@ @'); // defuse look-alike sentinels in the input
  s = s.replace(/```[a-zA-Z0-9_+-]*\n?([\s\S]*?)```/g, (_, c) => put(`<pre><code>${esc(c.replace(/\n$/, ''))}</code></pre>`));
  s = s.replace(/`([^`\n]+)`/g, (_, c) => put(`<code>${esc(c)}</code>`));
  s = esc(s);
  s = s.replace(/^#{1,6}\s+(.+)$/gm, '<strong class="mdh">$1</strong>');
  s = s.replace(/\*\*([^*\n]+)\*\*/g, '<strong>$1</strong>');
  s = s.replace(/(^|[\s(])\*([^*\s][^*\n]*?)\*(?=$|[\s).,!?:;])/g, '$1<em>$2</em>');
  s = s.replace(/\[([^\]\n]+)\]\((https?:\/\/[^)\s]+)\)/g, '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>');
  s = s.replace(/(^|[\s(])(https?:\/\/[^\s<)]+)/g, '$1<a href="$2" target="_blank" rel="noopener noreferrer">$2</a>');
  s = s.replace(/^(\s*)[-*]\s+/gm, '$1• ');
  s = s.replace(/@@PRISM(\d+)@@/g, (_, i) => stash[+i] ?? '');
  return s;
}
