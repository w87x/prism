<script>
  import { S, activeRuns, fmtTokens, go } from './store.svelte.js';
  import Led from './ui/Led.svelte';

  const st = $derived(S.status);
  const runs = $derived(activeRuns());
  const total = $derived(runs.reduce((a, r) => a + r.tokens_in + r.tokens_out, 0));
  const tasks = $derived(st ? [st.queue && `${st.queue} queued`, st.running && `${st.running} running`, st.waiting && `${st.waiting} waiting`].filter(Boolean) : []);
  const connLed = $derived(S.conn === 'open' ? 'ok' : S.conn === 'connecting' ? 'warn' : 'error');

  let now = $state(new Date());
  $effect(() => {
    const i = setInterval(() => (now = new Date()), 1000);
    return () => clearInterval(i);
  });
  const pad = (n) => String(n).padStart(2, '0');
</script>

<footer class="sb">
  <div class="leds">
    {#each st?.leds || [] as l (l.id)}
      <button class="led" title="{l.label}{l.detail ? ' — ' + l.detail : ''}" onclick={() => (l.id === 'ask' ? go('chat') : l.id === 'telegram' || l.id === 'browser' || l.id === 'obsidian' ? go('settings') : l.id === 'mcp' ? go('tools') : l.id === 'auto' ? go('autonomy') : null)}>
        <Led state={l.id === 'llm' && S.llm.active > 0 ? 'ok' : l.state} pulse={l.state === 'attention'} live={l.id === 'llm' && (S.llm.active > 0 || l.detail === 'generating')} size={8} /><span>{l.label}</span>
      </button>
    {/each}
  </div>
  <div class="mid"></div>
  <div class="right">
    {#if st && !st.setup}
      {#if tasks.length}<button class="mute lk" title="tasks: waiting for a worker · being worked on · paused until you answer" onclick={() => go('tasks')}>{tasks.join(' · ')}</button>
      {:else}<span class="mute" title="no tasks queued, running or waiting for an answer">tasks idle</span>{/if}
    {/if}
    {#if st && !st.pgvector}<span class="warnc" title="The pgvector extension is missing: memory search works, but compares embeddings in the app, which gets slow as memory grows.">vectors: fallback</span>{/if}
    <button class="wallbtn" title="Full-screen thinking wall" onclick={() => (S.wallOpen = true)}><Led state={runs.some((r) => !r.done) ? 'ok' : 'off'} live={runs.some((r) => !r.done)} size={6} /> thinking{#if runs.filter((r) => !r.done).length} · {runs.filter((r) => !r.done).length}{/if}</button>
    <span class="conn"><Led state={connLed} size={7} /> {S.conn === 'open' ? 'online' : S.conn}</span>
    <span class="clock" title="local time">{pad(now.getHours())}<span class="blink">:</span>{pad(now.getMinutes())}<span class="blink">:</span>{pad(now.getSeconds())}</span>
  </div>
</footer>

<style>
  .sb { grid-area: sb; display: flex; align-items: center; gap: 14px; padding: 0 10px; background: var(--panel-bg); border-top: 1px solid var(--line-2); font-size: 10.5px; text-transform: uppercase; letter-spacing: 0.07em; min-width: 0; overflow: hidden; }
  .leds { display: flex; gap: 12px; flex: none; align-items: center; }
  .clock { color: var(--fg-dim); font-variant-numeric: tabular-nums; letter-spacing: 0.05em; }
  .clock .blink { animation: clockblink 1s step-start infinite; }
  @keyframes clockblink { 50% { opacity: 0; } }
  .led { display: inline-flex; align-items: center; gap: 5px; background: none; border: 0; padding: 0; color: var(--fg-dim); font-size: 10.5px; letter-spacing: 0.07em; text-transform: uppercase; }
  .led:hover { color: var(--fg-hi); }
  .mid { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; text-align: center; }
  .right { display: flex; gap: 12px; flex: none; align-items: center; }
  .lk { background: none; border: 0; padding: 0; font-size: 10.5px; letter-spacing: 0.07em; text-transform: uppercase; } .lk:hover { color: var(--fg); }
  .wallbtn { display: inline-flex; align-items: center; gap: 5px; background: none; border: 0; padding: 0; color: var(--fg-dim); font-size: 10.5px; letter-spacing: 0.07em; text-transform: uppercase; }
  .wallbtn:hover { color: var(--fg-hi); }
  .warnc { color: var(--warn); }
  .conn { display: inline-flex; align-items: center; gap: 5px; color: var(--fg-dim); }
  @media (max-width: 820px) {
    .sb { padding: 0 8px env(safe-area-inset-bottom); min-height: 26px; gap: 8px; }
    .led span, .right .mute, .right .lk, .right .warnc { display: none; }
    .leds { gap: 9px; }
  }
</style>
