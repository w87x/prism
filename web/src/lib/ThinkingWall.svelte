<script>
  // Full-screen "thinking wall": every active agent run as a live tile (2×2, 3×3 or 4×4). Same old-CRT
  // look as the chat's thinking panel, but big enough to actually read several agents at once.
  import { S, activeRuns, fmtTokens } from './store.svelte.js';
  import Segmented from './ui/Segmented.svelte';
  import Led from './ui/Led.svelte';
  import Glyph from './ui/Glyph.svelte';
  import RunText from './RunText.svelte';
  import EditorBadge from './EditorBadge.svelte';
  const standalone = new URLSearchParams(location.search).get('view') === 'wall';
  function popOut() { window.open(location.origin + '/?view=wall', 'prism-wall', 'popup,width=1400,height=900'); S.wallOpen = false; }

  const saved = (() => { try { return localStorage.getItem('prism.wallSize') || '2'; } catch { return '2'; } })();
  let size = $state(saved);
  $effect(() => { try { localStorage.setItem('prism.wallSize', size); } catch {} });

  const runs = $derived(activeRuns());
  const n = $derived(Number(size));
  const cells = $derived(n * n);
  // running first, then the most recently finished; keep the oldest active ones if there are too many
  const shown = $derived([...runs].sort((a, b) => Number(!!a.done) - Number(!!b.done) || b.id - a.id).slice(0, cells));
  const hidden = $derived(Math.max(0, runs.length - shown.length));
  const blanks = $derived(Math.max(0, cells - shown.length));

  function onKey(e) { if (S.wallOpen && !standalone && e.key === 'Escape') { e.preventDefault(); S.wallOpen = false; } }
  $effect(() => { window.addEventListener('keydown', onKey); return () => window.removeEventListener('keydown', onKey); });
  const stick = (node) => { const f = () => (node.scrollTop = node.scrollHeight); f(); return { update: f }; };
  function open(r) { S.wallOpen = false; S.peekRun = r; }
</script>

{#if S.wallOpen}
  <div class="wall">
    <div class="top">
      <span class="ttl"><Led state={runs.some((r) => !r.done) ? 'ok' : 'standby'} pulse={runs.some((r) => !r.done)} size={8} /> THINKING</span>
      <span class="sm mute">{runs.filter((r) => !r.done).length} working{hidden ? ` · +${hidden} not shown` : ''}</span>
      <span class="grow"></span>
      <Segmented size="sm" bind:value={size} options={[{ value: '2', label: '2×2' }, { value: '3', label: '3×3' }, { value: '4', label: '4×4' }]} />
      <EditorBadge />
      {#if !standalone}<button type="button" class="x" title="open the wall in its own window (for a second display)" onclick={popOut}>⧉ new window</button>
      <button type="button" class="x" title="close (Esc)" onclick={() => (S.wallOpen = false)}>✕</button>{/if}
    </div>
    <div class="grid" style="grid-template-columns: repeat({n}, minmax(0, 1fr)); grid-template-rows: repeat({n}, minmax(0, 1fr));">
      {#each shown as r (r.id)}
        <div class="tile" class:done={r.done} role="button" tabindex="0" title="open this agent's live view" onclick={() => open(r)} onkeydown={(e) => e.key === 'Enter' && open(r)}>
          <div class="hd">
            <span class="nm" style="padding-left:{r.depth * 8}px"><Led state={r.done ? 'off' : r.phase === 'thinking' ? 'standby' : 'ok'} live={r.live} liveMs={r.liveMs} size={7} /> {r.depth ? '↳ ' : ''}<Glyph name={r.agent} /> {r.agent}</span>
            <span class="tk">{r.task || ''}{r.task ? ' · ' : ''}{fmtTokens(r.tokens_in)}↑ {fmtTokens(r.tokens_out)}↓</span>
          </div>
          <div class="body" use:stick={r.buf}><div class="txt">{#if r.buf.trim()}<RunText text={r.buf} />{:else}{r.done ? '(finished)' : '…'}{/if}<span class="cur">▌</span></div></div>
        </div>
      {/each}
      {#each Array(blanks) as _, i (i)}<div class="tile empty"><span class="mute sm">idle</span></div>{/each}
    </div>
  </div>
{/if}

<style>
  .wall { position: fixed; inset: 0; z-index: 150; display: flex; flex-direction: column; gap: 8px; padding: 10px; background: radial-gradient(ellipse at center, #04120c 0%, #010503 100%); }
  .top { display: flex; align-items: center; gap: 12px; flex: none; }
  .ttl { color: var(--accent); letter-spacing: 0.25em; font-size: 12px; text-shadow: var(--glow-accent); display: inline-flex; gap: 8px; align-items: center; }
  .x { background: none; border: 1px solid var(--line-2); color: var(--fg-dim); padding: 2px 9px; }
  .x:hover { color: var(--fg-hi); border-color: var(--fg-dim); }
  .grid { flex: 1; min-height: 0; display: grid; gap: 8px; }
  .tile { position: relative; min-width: 0; min-height: 0; display: flex; flex-direction: column; border: 1px solid var(--line-3); background: rgba(2, 10, 6, 0.85); box-shadow: inset 0 0 18px rgba(62, 232, 166, 0.08); padding: 6px 9px; cursor: pointer; overflow: hidden; }
  .tile:hover { border-color: var(--fg-dim); }
  .tile.done { opacity: 0.55; }
  .tile.empty { align-items: center; justify-content: center; border-style: dashed; border-color: var(--line-2); cursor: default; box-shadow: none; background: transparent; }
  .tile::after { content: ''; position: absolute; inset: 0; pointer-events: none; background: repeating-linear-gradient(to bottom, rgba(0,0,0,0) 0, rgba(0,0,0,0) 2px, rgba(0,0,0,0.2) 3px); mix-blend-mode: multiply; }
  .hd { display: flex; justify-content: space-between; gap: 8px; flex: none; font-size: 11px; color: var(--fg-mute); letter-spacing: 0.06em; text-transform: uppercase; white-space: nowrap; padding-bottom: 4px; border-bottom: 1px solid var(--line-2); }
  .nm { color: var(--fg-hi); font-weight: 700; overflow: hidden; text-overflow: ellipsis; }
  .tk { overflow: hidden; text-overflow: ellipsis; }
  .body { flex: 1; min-height: 0; overflow: auto; padding-top: 5px; scrollbar-width: none; }
  .body::-webkit-scrollbar { display: none; }
  .txt { color: var(--fg); font-size: 12px; line-height: 1.5; white-space: pre-wrap; word-break: break-word; text-shadow: 0 0 4px rgba(62, 232, 166, 0.28); }
  .cur { color: var(--accent); animation: blink 1s step-start infinite; }
  .tile.done .cur { display: none; }
  @keyframes blink { 50% { opacity: 0; } }
</style>
