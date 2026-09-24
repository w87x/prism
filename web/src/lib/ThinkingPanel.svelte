<script>
  // Old-CRT green-on-black view of what the agents are thinking. Three lines high, newest at the
  // bottom with the cursor: older lines scroll off the top. Several agents split the panel
  // vertically (max 4). A pending question takes the panel over in the attention colour.
  import { S, activeRuns, fmtTokens, iconOf } from './store.svelte.js';
  import AskCard from './AskCard.svelte';
  import Led from './ui/Led.svelte';
  import Glyph from './ui/Glyph.svelte';
  import RunText from './RunText.svelte';

  const runs = $derived(activeRuns());
  const shown = $derived(runs.slice(-4));
  const hidden = $derived(Math.max(0, runs.length - shown.length));
  const ask = $derived(S.asks[0]);
</script>

<div class="crt" class:attn={!!ask}>
  {#if ask}
    <div class="askwrap"><AskCard {ask} />{#if S.asks.length > 1}<div class="more">+{S.asks.length - 1} more waiting</div>{/if}</div>
  {:else if runs.length === 0}
    <div class="idle"><Led state="standby" pulse size={8} /> STANDBY</div>
  {:else}
    <div class="cols" style="grid-template-columns: repeat({shown.length}, minmax(0, 1fr))">
      {#each shown as r (r.id)}
        <div class="col" role="button" tabindex="0" title="open the live view of this agent" onclick={() => (S.peekRun = r)} onkeydown={(e) => e.key === 'Enter' && (S.peekRun = r)}>
          <div class="hd">
            <span class="nm" style="padding-left:{r.depth * 8}px"><Led state={r.done ? 'off' : r.phase === 'thinking' ? 'standby' : 'ok'} live={r.live} liveMs={r.liveMs} size={6} /> {r.depth ? '↳ ' : ''}<Glyph name={r.agent} /> {r.agent}</span>
            <span class="tk">{fmtTokens(r.tokens_in)}↑ {fmtTokens(r.tokens_out)}↓</span>
          </div>
          <div class="body"><div class="txt"><RunText text={r.buf} /></div></div>
        </div>
      {/each}
    </div>
    {#if hidden}<div class="more">+{hidden} more agents working</div>{/if}
  {/if}
</div>

<style>
  .crt {
    position: relative; flex: none; background: radial-gradient(ellipse at center, #04120c 0%, #010503 100%);
    border: 1px solid var(--line-3); box-shadow: inset 0 0 22px rgba(62, 232, 166, 0.1), var(--glow-sm);
    padding: 4px 8px 5px; overflow: hidden;
  }
  .crt::after { content: ''; position: absolute; inset: 0; pointer-events: none; background: repeating-linear-gradient(to bottom, rgba(0, 0, 0, 0) 0, rgba(0, 0, 0, 0) 2px, rgba(0, 0, 0, 0.22) 3px); mix-blend-mode: multiply; }
  .crt.attn { border-color: var(--attn); box-shadow: inset 0 0 22px rgba(255, 153, 0, 0.12), var(--glow-attn); background: radial-gradient(ellipse at center, #140c00 0%, #050300 100%); }
  .idle { color: var(--accent); text-shadow: var(--glow-accent); letter-spacing: 0.2em; font-size: 10.5px; line-height: 1.45; height: calc(3 * 1.45 * 10.5px + 16px); display: flex; align-items: flex-start; gap: 8px; padding-top: 1px; }
  .idle :global(.led) { margin-top: 3px; }
  .cols { display: grid; gap: 10px; }
  .col { cursor: pointer; min-width: 0; border-left: 1px solid var(--line-2); padding-left: 8px; }
  .col:first-child { border-left: 0; padding-left: 0; }
  .hd { display: flex; justify-content: space-between; gap: 6px; font-size: 10.5px; color: var(--fg-mute); letter-spacing: 0.06em; text-transform: uppercase; white-space: nowrap; }
  .nm { color: var(--fg-dim); font-weight: 700; overflow: hidden; text-overflow: ellipsis; }
  .body { height: calc(3 * 1.45 * 10.5px); overflow: hidden; display: flex; flex-direction: column; justify-content: flex-end; }
  .txt { color: var(--fg-dim); font-size: 10.5px; text-shadow: 0 0 4px rgba(62, 232, 166, 0.28); white-space: pre-wrap; word-break: break-word; line-height: 1.45; }
  .more { color: var(--fg-mute); font-size: 10.5px; text-align: right; }
  .askwrap { min-height: calc(3 * 1.45em + 12px); display: flex; flex-direction: column; justify-content: center; gap: 4px; }
</style>
