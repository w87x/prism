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
  import StatusSheet from './lib/StatusSheet.svelte';
  import BriefingReader from './lib/BriefingReader.svelte';
  import MToday from './mobile/MToday.svelte';
  import MMemory from './mobile/MMemory.svelte';
  import Hint from './lib/ui/Hint.svelte';
  import { PAGE_HELP } from './lib/help.js';
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
  let small = $state(!!mq?.matches); // the viewport is phone-sized
  let desktopView = $state((() => { try { return localStorage.getItem('prism.desktopView') === '1'; } catch { return false; } })());
  const narrow = $derived(small && !desktopView);
  let drawer = $state(false); // the "More" sheet on a phone
  $effect(() => { if (!mq) return; const f = () => { small = mq.matches; if (!small) drawer = false; }; mq.addEventListener('change', f); return () => mq.removeEventListener('change', f); });
  $effect(() => { S.page; drawer = false; });
  $effect(() => { S.narrow = narrow; });
  function setDesktopView(on) { desktopView = on; drawer = false; try { localStorage.setItem('prism.desktopView', on ? '1' : '0'); } catch {} }
  // bottom bar on a phone: the four things used all day; everything else lives under "More"
  const tabs = ['today', 'chat', 'tasks', 'memory'];
  const moreOn = $derived(!tabs.includes(S.page));
  // on a phone the right rail (agent / tool inspector) is a bottom sheet, shown only while something is selected
  const sheet = $derived(narrow && ((S.page === 'agents' && S.selectedAgent) || (S.page === 'tools' && S.selectedTool)));
  const closeSheet = () => { S.selectedAgent = null; S.selectedTool = null; };
  const menu = () => toggleNav();

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
    {#if !narrow}<button class="ico" onclick={menu} title="Toggle menu"><Icon name="menu" /></button>{/if}
    <div class="brand"><Logo size={24} /><span class="wm">PRISM</span></div>
    <span class="sep"></span>
    <span class="pg">{cur.label}</span>
    {#if PAGE_HELP[cur.id]}<Hint title={cur.label} text={PAGE_HELP[cur.id]} />{/if}
    <span class="grow"></span>
    {#if S.status?.thinking}<span class="act"><Led state="ok" pulse size={8} /> {S.status.thinking} agent{S.status.thinking > 1 ? 's' : ''} working</span>{/if}
    {#if !(narrow && S.editor.editor)}<EditorBadge />{/if}
    <button class="ico" onclick={() => (S.searchOpen = true)} title="Search everything (⌘K)"><Icon name="search" /></button>
    {#if narrow}<StatusSheet />{/if}
    <NotifyBell />
    {#if small && desktopView}<button class="ico" onclick={() => setDesktopView(false)} title="Back to the phone layout"><Icon name="panel" /></button>{/if}
    {#if !narrow}<button class="ico" onclick={toggleWidget} title="Toggle widgets"><Icon name="panel" /></button>{/if}
  </header>

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
      {:else if S.page === 'today'}{#if narrow}<MToday />{:else}<Today />{/if}
      {:else if S.page === 'chat'}<Chat />
      {:else if S.page === 'agents'}<Agents />
      {:else if S.page === 'tasks'}<Tasks />
      {:else if S.page === 'memory'}{#if narrow}<MMemory />{:else}<Memory />{/if}
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

  {#if narrow}
    <nav class="tabbar" aria-label="main">
      {#each tabs as id}
        {@const p = pages.find((x) => x.id === id)}
        <button type="button" class="tb" class:on={S.page === id} onclick={() => go(id)}>
          <Icon name={p.icon} size={20} /><span>{p.label}</span>
          {#if id === 'chat' && S.asks.length}<i class="dot"></i>{/if}
        </button>
      {/each}
      <button type="button" class="tb" class:on={moreOn || drawer} onclick={() => (drawer = !drawer)}>
        <Icon name="menu" size={20} /><span>More</span>
        {#if S.proposals > 0}<i class="dot"></i>{/if}
      </button>
    </nav>
    {#if drawer}
      <button type="button" class="backdrop" aria-label="close" onclick={() => (drawer = false)}></button>
      <div class="more" role="menu">
        {#each pages.filter((p) => !tabs.includes(p.id)) as p}
          <button type="button" class="mi" class:on={S.page === p.id} onclick={() => go(p.id)}>
            <Icon name={p.icon} size={20} /><span>{p.label}</span>
            {#if p.id === 'agents' && S.proposals > 0}<i class="dot"></i>{/if}
          </button>
        {/each}
        <button type="button" class="mi wide" onclick={() => setDesktopView(true)}><Icon name="panel" size={20} /><span>Desktop view</span></button>
      </div>
    {/if}
  {/if}
  {#if !narrow}<StatusBar />{/if}
</div>
{/if}

{#if showOb}<Onboarding />{/if}
<BriefingReader />
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
  /* centred both ways: on a phone every button has a 30px minimum height, which left the icon stuck at the top of it */
  .ico { background: none; border: 0; color: var(--ico); padding: 3px; display: flex; align-items: center; justify-content: center; } .ico:hover { color: var(--ico-hi); }
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
  .shell.mobile { grid-template-columns: minmax(0, 1fr); grid-template-rows: calc(38px + env(safe-area-inset-top)) minmax(0, 1fr) auto; grid-template-areas: 'top' 'main' 'tabs'; height: 100dvh; }
  .shell.mobile .top { padding-top: env(safe-area-inset-top); gap: 8px; }
  .shell.mobile .brand .wm, .shell.mobile .sep, .shell.mobile .act { display: none; }
  .shell.mobile .wid { display: none; }
  .shell.mobile.sheet .wid { display: flex; position: fixed; z-index: 100; left: 0; right: 0; bottom: 0; top: calc(64px + env(safe-area-inset-top)); background: var(--bg); border-left: 0; border-top: 1px solid var(--line-3); box-shadow: 0 -8px 28px rgba(0, 0, 0, 0.6); padding-bottom: env(safe-area-inset-bottom); }
  .sheetx { flex: none; align-self: flex-end; background: none; border: 1px solid var(--line-2); color: var(--fg-dim); padding: 3px 12px; }
  .shell.mobile .main { padding: 4px 4px 2px; }
  /* iOS zooms into any field under 16px when it is focused */
  .shell.mobile :global(input), .shell.mobile :global(textarea), .shell.mobile :global(select) { font-size: 16px; }
  /* tables become stacked rows on a phone (headers hidden; each row wraps its cells) */
  .shell.mobile :global(table.t), .shell.mobile :global(table.t tbody) { display: block; width: 100%; }
  .shell.mobile :global(table.t thead) { display: none; }
  .shell.mobile :global(table.t tr) { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 12px; padding: 10px 12px; border-bottom: 1px solid var(--line); }
  .shell.mobile :global(table.t td) { display: block; padding: 0; border: 0; width: auto !important; max-width: 100%; }
  .shell.mobile :global(table.t td[colspan]) { flex: 1 1 100%; }
  .shell.mobile :global(table.t td.end) { margin-left: auto; }
  .shell.mobile :global(.btn), .shell.mobile :global(button.tab) { min-height: 36px; }
  .shell.mobile :global(.bar) { flex-wrap: wrap; row-gap: 6px; }
  .shell.mobile .nav { display: none; }
  .shell.mobile .nav-old { background: var(--bg); position: fixed; z-index: 120; top: 0; bottom: 0; left: 0; width: 236px; padding-top: calc(env(safe-area-inset-top) + 8px); transform: translateX(-102%); transition: transform 0.2s ease; box-shadow: 8px 0 28px rgba(0, 0, 0, 0.6); overflow: auto; }
  .tabbar { grid-area: tabs; display: grid; grid-template-columns: repeat(5, 1fr); background: var(--panel-bg); border-top: 1px solid var(--line-2); padding-bottom: env(safe-area-inset-bottom); }
  .tb { position: relative; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 2px; min-height: 46px; padding: 4px 2px; background: none; border: 0; border-top: 2px solid transparent; color: var(--fg-mute); font-size: 10px; text-transform: uppercase; letter-spacing: 0.06em; }
  .tb.on { color: var(--fg-hi); border-top-color: var(--accent); background: linear-gradient(180deg, rgba(62, 232, 166, 0.1), transparent); }
  .tb .dot, .mi .dot { position: absolute; top: 6px; right: 26%; }
  .more { position: fixed; z-index: 120; left: 0; right: 0; bottom: calc(46px + env(safe-area-inset-bottom)); display: grid; grid-template-columns: repeat(3, 1fr); background: var(--bg); border-top: 1px solid var(--line-3); box-shadow: 0 -10px 30px rgba(0, 0, 0, 0.6); }
  .mi { position: relative; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 5px; min-height: 76px; padding: 8px 4px; background: var(--bg); border: 0; border-right: 1px solid var(--line); border-bottom: 1px solid var(--line); color: var(--fg-dim); font-size: 11px; text-transform: uppercase; letter-spacing: 0.06em; }
  .mi.on { color: var(--fg-hi); background: var(--bg-2); }
  .mi.wide { grid-column: 1 / -1; min-height: 52px; flex-direction: row; gap: 10px; color: var(--fg-mute); }
  .shell.mobile .ni { padding: 11px 16px; font-size: 12px; }
</style>
