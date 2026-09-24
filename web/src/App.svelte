<script>
  import { S, init, go, toggleNav, toggleWidget } from './lib/store.svelte.js';
  import Logo from './lib/ui/Logo.svelte';
  import Icon from './lib/ui/Icon.svelte';
  import Led from './lib/ui/Led.svelte';
  import StatusBar from './lib/StatusBar.svelte';
  import Toasts from './lib/Toasts.svelte';
  import ConfirmHost from './lib/ConfirmHost.svelte';
  import AgentPeek from './lib/AgentPeek.svelte';
  import EditorBadge from './lib/EditorBadge.svelte';
  import ProposalReview from './lib/ProposalReview.svelte';
  import AskCard from './lib/AskCard.svelte';
  import ThinkingWall from './lib/ThinkingWall.svelte';
  import CommandPalette from './lib/CommandPalette.svelte';
  import Onboarding from './pages/Onboarding.svelte';
  import NotifyBell from './lib/NotifyBell.svelte';

  import Today from './pages/Today.svelte';
  import Chat from './pages/Chat.svelte';
  import ChatWidget from './pages/ChatWidget.svelte';
  import Agents from './pages/Agents.svelte';
  import AgentWidget from './pages/AgentWidget.svelte';
  import ToolWidget from './pages/ToolWidget.svelte';
  import Tasks from './pages/Tasks.svelte';
  import Memory from './pages/Memory.svelte';
  import Tools from './pages/Tools.svelte';
  import Autonomy from './pages/Autonomy.svelte';
  import Library from './pages/Library.svelte';
  import Trackers from './pages/Trackers.svelte';
  import Knowledge from './pages/Knowledge.svelte';
  import Settings from './pages/Settings.svelte';
  import Usage from './pages/Usage.svelte';
  import PageInfo from './pages/PageInfo.svelte';
  import SideRail from './pages/SideRail.svelte';

  init();
  // a window opened with ?view=wall is just the thinking wall (for a second display); it stays view-only
  const wallOnly = new URLSearchParams(location.search).get('view') === 'wall';
  if (wallOnly) S.wallOpen = true;

  // phone-sized: the sidebar becomes a slide-in drawer, the right widget rail is dropped, pages get the full width
  const mq = typeof matchMedia === 'function' ? matchMedia('(max-width: 820px)') : null;
  let narrow = $state(!!mq?.matches);
  let drawer = $state(false);
  $effect(() => { if (!mq) return; const f = () => { narrow = mq.matches; if (!narrow) drawer = false; }; mq.addEventListener('change', f); return () => mq.removeEventListener('change', f); });
  $effect(() => { S.page; drawer = false; });
  // on a phone the right rail (agent / tool inspector) is a bottom sheet, shown only while something is selected
  const sheet = $derived(narrow && ((S.page === 'agents' && S.selectedAgent) || (S.page === 'tools' && S.selectedTool)));
  const closeSheet = () => { S.selectedAgent = null; S.selectedTool = null; };
  const menu = () => (narrow ? (drawer = !drawer) : toggleNav());

  const pages = [
    { id: 'today', label: 'Today', icon: 'today' },
    { id: 'chat', label: 'Chat', icon: 'chat' },
    { id: 'agents', label: 'Agents', icon: 'graph' },
    { id: 'tasks', label: 'Tasks', icon: 'tasks' },
    { id: 'memory', label: 'Memory', icon: 'memory' },
    { id: 'kb', label: 'Knowledge', icon: 'kb' },
    { id: 'tools', label: 'Tools & MCP', icon: 'tools' },
    { id: 'autonomy', label: 'Autonomy', icon: 'auto' },
    { id: 'library', label: 'Library', icon: 'library' },
    { id: 'trackers', label: 'Trackers', icon: 'table' },
    { id: 'usage', label: 'Usage', icon: 'usage' },
    { id: 'settings', label: 'Settings', icon: 'settings' },
  ];
  const cur = $derived(pages.find((p) => p.id === S.page) || pages[0]);
  const setup = $derived(S.status?.setup);
  const needOnboarding = $derived(S.status && !S.skipOnboarding && (S.status.setup || !S.status.onboarded));
  const showOb = $derived(needOnboarding || S.onboardingOpen);
  const otherAsks = $derived(S.page === 'chat' ? [] : S.asks);
</script>

{#if !wallOnly}
<div class="shell" class:mobile={narrow} class:sheet class:drawer class:nav-c={!narrow && !S.navOpen} class:w-c={!narrow && (!S.widgetOpen || S.page === 'trackers')}>
  <header class="top">
    <button class="ico" onclick={menu} title="Toggle menu"><Icon name="menu" /></button>
    <div class="brand"><Logo size={24} /><span class="wm">PRISM</span></div>
    <span class="sep"></span>
    <span class="pg">{cur.label}</span>
    <span class="grow"></span>
    {#if S.status?.thinking}<span class="act"><Led state="ok" pulse size={8} /> {S.status.thinking} agent{S.status.thinking > 1 ? 's' : ''} working</span>{/if}
    <EditorBadge />
    <button class="ico" onclick={() => (S.searchOpen = true)} title="Search everything (⌘K)"><Icon name="search" /></button>
    <NotifyBell />
    {#if !narrow}<button class="ico" onclick={toggleWidget} title="Toggle widgets"><Icon name="panel" /></button>{/if}
  </header>

  {#if narrow && drawer}<button type="button" class="backdrop" aria-label="close menu" onclick={() => (drawer = false)}></button>{/if}
  <nav class="nav">
    {#each pages as p}
      <button class="ni" class:on={S.page === p.id} onclick={() => go(p.id)} title={p.label}>
        <span class="ic"><Icon name={p.icon} size={15} /></span>
        {#if S.navOpen || narrow}<span>{p.label}</span>{/if}
        {#if p.id === 'chat' && S.asks.length}<span class="dot"></span>{/if}
        {#if p.id === 'agents' && S.proposals > 0}<span class="dot" title="{S.proposals} proposal(s) waiting for review"></span>{/if}
      </button>
    {/each}
  </nav>

  <main class="main">
    {#if otherAsks.length}
      <div class="askbar">{#each otherAsks as a (a.id)}<AskCard ask={a} compact />{/each}</div>
    {/if}
    <div class="pagebox">
      {#if setup}
        <PageInfo />
      {:else if S.page === 'today'}<Today />
      {:else if S.page === 'chat'}<Chat />
      {:else if S.page === 'agents'}<Agents />
      {:else if S.page === 'tasks'}<Tasks />
      {:else if S.page === 'memory'}<Memory />
      {:else if S.page === 'kb'}<Knowledge />
      {:else if S.page === 'tools'}<Tools />
      {:else if S.page === 'autonomy'}<Autonomy />
      {:else if S.page === 'library'}<Library />
      {:else if S.page === 'trackers'}<Trackers />
      {:else if S.page === 'usage'}<Usage />
      {:else if S.page === 'settings'}<Settings />
      {/if}
    </div>
  </main>

  <aside class="wid">
    {#if sheet}<button type="button" class="sheetx" onclick={closeSheet}>✕ close</button>{/if}
    {#if S.page === 'chat'}<ChatWidget />
    {:else if S.page === 'agents'}<AgentWidget />
    {:else if S.page === 'tools'}<ToolWidget />
    {:else}<SideRail />
    {/if}
  </aside>

  <StatusBar />
</div>
{/if}

{#if showOb}<Onboarding />{/if}
<CommandPalette />
<ThinkingWall />
<Toasts />
<ConfirmHost />
<AgentPeek />
<ProposalReview />

<style>
  .shell { height: 100%; display: grid; grid-template-columns: 158px minmax(0, 1fr) 286px; grid-template-rows: 38px minmax(0, 1fr) 24px; grid-template-areas: 'top top top' 'nav main wid' 'sb sb sb'; }
  .shell.nav-c { grid-template-columns: 42px minmax(0, 1fr) 286px; }
  .shell.w-c { grid-template-columns: 158px minmax(0, 1fr) 0; }
  .shell.nav-c.w-c { grid-template-columns: 42px minmax(0, 1fr) 0; }
  .top { grid-area: top; display: flex; align-items: center; gap: 10px; padding: 0 8px; background: var(--panel-bg); border-bottom: 1px solid var(--line-2); box-shadow: 0 1px 12px rgba(62, 232, 166, 0.06); }
  .ico { background: none; border: 0; color: var(--ico); padding: 3px; display: flex; } .ico:hover { color: var(--ico-hi); }
  .brand { display: flex; align-items: center; gap: 8px; }
  .wm { font-weight: 700; letter-spacing: 0.32em; font-size: 15px; background: linear-gradient(90deg, #3ee8a6, #4499ee); -webkit-background-clip: text; background-clip: text; color: transparent; filter: drop-shadow(0 0 4px rgba(62, 232, 166, 0.45)); }
  .sep { width: 1px; height: 18px; background: var(--line-2); }
  .pg { text-transform: uppercase; letter-spacing: 0.14em; font-size: var(--fs-sm); color: var(--fg-dim); }
  .act { display: inline-flex; align-items: center; gap: 6px; font-size: var(--fs-sm); color: var(--fg); text-transform: uppercase; letter-spacing: 0.07em; }
  .nav { grid-area: nav; background: var(--panel-bg); border-right: 1px solid var(--line-2); padding: 6px 0; display: flex; flex-direction: column; gap: 1px; overflow: hidden; }
  .ni { display: flex; align-items: center; gap: 9px; padding: 6px 12px; background: none; border: 0; border-left: 2px solid transparent; color: var(--fg-dim); text-align: left; text-transform: uppercase; letter-spacing: 0.08em; font-size: var(--fs-sm); font-weight: 400; position: relative; white-space: nowrap; }
  .nav-c .ni { padding: 7px 0; justify-content: center; }
  .ic { display: inline-flex; color: var(--ico); }
  .ni:hover { color: var(--fg); background: var(--bg-2); } .ni:hover .ic { color: var(--ico-hi); }
  .ni.on .ic { color: var(--ico-hi); filter: drop-shadow(0 0 3px rgba(106, 169, 189, 0.55)); }
  .ni.on { color: var(--fg-hi); border-left-color: var(--fg); background: linear-gradient(90deg, rgba(62, 232, 166, 0.12), transparent); text-shadow: var(--glow-sm); }
  .dot { position: absolute; right: 8px; width: 6px; height: 6px; border-radius: 50%; background: var(--attn); box-shadow: var(--glow-attn); animation: pulse 1s infinite; }
  .main { grid-area: main; min-width: 0; min-height: 0; display: flex; flex-direction: column; gap: 6px; padding: 6px; }
  .pagebox { flex: 1; min-height: 0; }
  .askbar { flex: none; border: 1px solid var(--attn); background: var(--attn-bg); box-shadow: var(--glow-attn); padding: 5px 8px; display: flex; flex-direction: column; gap: 6px; }
  .wid { grid-area: wid; min-width: 0; min-height: 0; background: transparent; border-left: 1px solid var(--line-2); padding: 6px; display: flex; flex-direction: column; gap: 6px; overflow: auto; }
  .w-c .wid { display: none; }
  /* ── phone layout ── */
  .backdrop { position: fixed; inset: 0; z-index: 110; background: rgba(0, 0, 0, 0.6); border: 0; padding: 0; }
  .shell.mobile { grid-template-columns: minmax(0, 1fr); grid-template-rows: calc(42px + env(safe-area-inset-top)) minmax(0, 1fr) auto; grid-template-areas: 'top' 'main' 'sb'; height: 100dvh; }
  .shell.mobile .top { padding-top: env(safe-area-inset-top); gap: 8px; }
  .shell.mobile .brand .wm, .shell.mobile .sep, .shell.mobile .act { display: none; }
  .shell.mobile .wid { display: none; }
  .shell.mobile.sheet .wid { display: flex; position: fixed; z-index: 100; left: 0; right: 0; bottom: 0; top: calc(64px + env(safe-area-inset-top)); background: var(--bg); border-left: 0; border-top: 1px solid var(--line-3); box-shadow: 0 -8px 28px rgba(0, 0, 0, 0.6); padding-bottom: env(safe-area-inset-bottom); }
  .sheetx { flex: none; align-self: flex-end; background: none; border: 1px solid var(--line-2); color: var(--fg-dim); padding: 3px 12px; }
  .shell.mobile .main { padding: 4px 4px 2px; }
  .shell.mobile .nav { background: var(--bg); position: fixed; z-index: 120; top: 0; bottom: 0; left: 0; width: 236px; padding-top: calc(env(safe-area-inset-top) + 8px); transform: translateX(-102%); transition: transform 0.2s ease; box-shadow: 8px 0 28px rgba(0, 0, 0, 0.6); overflow: auto; }
  .shell.mobile.drawer .nav { transform: none; }
  .shell.mobile .ni { padding: 11px 16px; font-size: 12px; }
</style>
