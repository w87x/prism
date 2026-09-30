<script>
  // Full-screen cover while the backend is unreachable, replacing the earlier "tint the whole UI"
  // approach: that quiet wash was too easy to miss entirely, and this is the app's own splash screen
  // (not a grayscale/error look — full color, the same brand it opens with) showing real reconnect
  // progress instead of nothing. It blocks interaction on purpose: every RPC would fail right now anyway.
  import { S, connStatusText } from './store.svelte.js';
  import Logo from './ui/Logo.svelte';
  import Bar from './ui/Bar.svelte';

  let now = $state(new Date());
  $effect(() => {
    if (S.conn === 'open') return;
    const i = setInterval(() => (now = new Date()), 1000);
    return () => clearInterval(i);
  });
  const text = $derived(connStatusText(now));
</script>

{#if S.conn !== 'open'}
  <div class="splash" role="alert" aria-live="polite">
    <Logo size={72} />
    <div class="wm">PRISM</div>
    <div class="status">{text}</div>
    <div class="barwrap"><Bar equalize height={4} tone="accent" /></div>
  </div>
{/if}

<style>
  .splash { position: fixed; inset: 0; z-index: 5000; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 12px;
    background:
      linear-gradient(rgba(62, 232, 166, 0.028) 1px, transparent 1px) 0 0 / 19px 19px,
      linear-gradient(90deg, rgba(62, 232, 166, 0.028) 1px, transparent 1px) 0 0 / 19px 19px,
      radial-gradient(ellipse 90% 60% at 50% -12%, rgba(62, 232, 166, 0.11), transparent 60%),
      radial-gradient(ellipse 60% 60% at 105% 105%, rgba(68, 153, 238, 0.09), transparent 62%),
      var(--bg);
  }
  .wm { font-weight: 700; letter-spacing: 0.32em; font-size: 22px; background: linear-gradient(90deg, #3ee8a6, #4499ee); -webkit-background-clip: text; background-clip: text; color: transparent; filter: drop-shadow(0 0 5px rgba(62, 232, 166, 0.45)); }
  .status { color: var(--fg-dim); font-size: var(--fs-sm); text-transform: uppercase; letter-spacing: 0.1em; }
  .barwrap { width: 180px; }
</style>
