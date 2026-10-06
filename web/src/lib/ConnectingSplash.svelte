<script>
  // Full-screen cover while the backend is unreachable, replacing the earlier "tint the whole UI"
  // approach: that quiet wash was too easy to miss entirely, and this is the app's own splash screen
  // (not a grayscale/error look — full color, the same brand it opens with) showing real reconnect
  // progress instead of nothing. It blocks interaction on purpose: every RPC would fail right now anyway.
  import { S, connStatusText } from './store.svelte.js';
  import Logo from './ui/Logo.svelte';
  import Bar from './ui/Bar.svelte';

  let now = $state(new Date());
  const starting = $derived(S.conn === 'open' && !!S.status?.starting);
  $effect(() => {
    if (S.conn === 'open' && !starting) return;
    const i = setInterval(() => (now = new Date()), 1000);
    return () => clearInterval(i);
  });
  const text = $derived(connStatusText(now));
  // while PRISM is up but still opening its database and starting services: what it is doing, how long it has taken
  const su = $derived(S.startup);
  const secs = (ms) => (ms < 1000 ? '<1s' : ms < 60000 ? `${Math.round(ms / 1000)}s` : `${Math.floor(ms / 60000)}m ${Math.round((ms % 60000) / 1000)}s`);
  const elapsed = $derived(su ? su.elapsed_ms + Math.max(0, now.getTime() - (su._at || now.getTime())) : 0);
  const current = $derived(su?.stages?.length ? su.stages[su.stages.length - 1] : null);
  const doneStages = $derived((su?.stages || []).filter((x) => x.state === 'done').slice(-6));
</script>

{#if S.conn !== 'open' || starting}
  <div class="splash" role="alert" aria-live="polite">
    <Logo size={72} />
    <div class="wm">PRISM</div>
    {#if starting}
      <div class="status">starting… {secs(elapsed)}</div>
      <div class="barwrap"><Bar equalize height={4} tone="accent" /></div>
      <div class="steps" aria-label="start-up progress">
        {#each doneStages as x (x.name)}<div class="st done"><span>✓ {x.name}</span><span class="t">{secs(x.ms)}</span></div>{/each}
        {#if current && current.state === 'running'}
          <div class="st now"><span>▸ {current.name}</span><span class="t">{secs(current.ms + Math.max(0, now.getTime() - (su._at || now.getTime())))}</span></div>
          {#if current.detail}<div class="det">{current.detail}</div>{/if}
        {/if}
      </div>
    {:else}
      <div class="status">{text}</div>
      <div class="barwrap"><Bar equalize height={4} tone="accent" /></div>
    {/if}
  </div>
{/if}

<style>
  .splash { position: fixed; inset: 0; z-index: 5000; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 12px;
    background:
      linear-gradient(rgb(var(--rgb-fg) / 0.028) 1px, transparent 1px) 0 0 / 19px 19px,
      linear-gradient(90deg, rgb(var(--rgb-fg) / 0.028) 1px, transparent 1px) 0 0 / 19px 19px,
      radial-gradient(ellipse 90% 60% at 50% -12%, rgb(var(--rgb-fg) / 0.11), transparent 60%),
      radial-gradient(ellipse 60% 60% at 105% 105%, rgb(var(--rgb-accent) / 0.09), transparent 62%),
      var(--bg);
  }
  .wm { font-weight: 700; letter-spacing: 0.32em; font-size: 22px; background: linear-gradient(90deg, var(--fg), var(--accent)); -webkit-background-clip: text; background-clip: text; color: transparent; filter: drop-shadow(0 0 5px rgb(var(--rgb-fg) / 0.45)); }
  .status { color: var(--fg-dim); font-size: var(--fs-sm); text-transform: uppercase; letter-spacing: 0.1em; }
  .barwrap { width: 180px; }
  .steps { width: min(360px, 86vw); display: flex; flex-direction: column; gap: 3px; font-size: var(--fs-sm); margin-top: 6px; }
  .st { display: flex; justify-content: space-between; gap: 12px; color: var(--fg-mute); }
  .st.now { color: var(--fg-hi); }
  .st .t { color: var(--fg-faint); }
  .det { color: var(--fg-dim); font-size: 11px; padding-left: 14px; overflow-wrap: anywhere; }
</style>
