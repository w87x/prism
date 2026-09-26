<script>
  import { S, call, go, ago, loadNotifs, openRef } from './store.svelte.js';
  import Icon from './ui/Icon.svelte';
  import Led from './ui/Led.svelte';
  import Empty from './ui/Empty.svelte';

  let open = $state(false);
  let root = $state();
  const led = (l) => (l === 'error' ? 'error' : l === 'attention' || l === 'warning' ? 'attention' : 'standby');

  async function pick(n) {
    if (!n.read) { n.read = true; S.notifs.unread = Math.max(0, S.notifs.unread - 1); call('notifications.read', { id: n.id }, { quiet: true }); }
    if (n.ref) { openRef(n.ref); open = false; }
  }
  async function readAll() { await call('notifications.read', { id: 0 }); loadNotifs(); }
  async function clear() { await call('notifications.clear'); loadNotifs(); }
  function outside(e) { if (open && !root?.contains(e.target)) open = false; }
</script>

<svelte:window onpointerdown={outside} onkeydown={(e) => e.key === 'Escape' && (open = false)} />

<div class="nb" bind:this={root}>
  <button type="button" class="bell" class:on={S.notifs.unread > 0} title="Notifications" onclick={() => (open = !open)}>
    <Icon name="bell" size={15} />{#if S.notifs.unread > 0}<span class="ct">{S.notifs.unread > 99 ? '99+' : S.notifs.unread}</span>{/if}
  </button>
  {#if open}
    <div class="dd">
      <div class="hd"><span>Notifications</span><span class="grow"></span><button type="button" onclick={readAll}>mark all read</button><button type="button" onclick={clear}>clear</button></div>
      <div class="list scroll">
        {#each S.notifs.items as n (n.id)}
          <button type="button" class="it" class:unread={!n.read} onclick={() => pick(n)}>
            <Led state={led(n.level)} size={7} />
            <span class="bd"><span class="ti">{n.title}</span>{#if n.text}<span class="tx">{n.text}</span>{/if}</span>
            <span class="ag">{ago(n.ts)}</span>
          </button>
        {:else}<Empty>nothing yet</Empty>{/each}
      </div>
    </div>
  {/if}
</div>

<style>
  .nb { position: relative; }
  .bell { position: relative; background: none; border: 0; color: var(--ico); padding: 3px; display: flex; align-items: center; justify-content: center; } .bell:hover { color: var(--ico-hi); }
  .bell.on { color: var(--attn); filter: drop-shadow(0 0 4px var(--attn)); }
  .ct { position: absolute; top: -3px; right: -5px; min-width: 14px; height: 14px; padding: 0 3px; background: var(--attn); color: #000; font-size: 9px; font-weight: 700; display: flex; align-items: center; justify-content: center; }
  .dd { position: absolute; right: 0; top: 30px; width: 380px; max-width: 92vw; background: var(--bg-1); border: 1px solid var(--line-3); box-shadow: 0 10px 34px rgba(0, 0, 0, 0.75); z-index: 1500; }
  .hd { display: flex; gap: 10px; align-items: center; padding: 4px 10px; border-bottom: 1px solid var(--line-2); background: var(--bg-2); text-transform: uppercase; letter-spacing: 0.1em; font-size: var(--fs-sm); color: var(--fg-dim); }
  .hd button { background: none; border: 0; color: var(--accent); font-size: 10px; letter-spacing: 0.08em; text-transform: uppercase; } .hd button:hover { color: var(--accent-hi); }
  .list { max-height: 60vh; }
  .it { display: flex; gap: 8px; align-items: flex-start; width: 100%; background: none; border: 0; border-bottom: 1px solid var(--line); padding: 5px 10px; text-align: left; color: var(--fg-dim); }
  .it:hover { background: var(--bg-3); }
  .it.unread { background: color-mix(in srgb, var(--attn) 6%, transparent); }
  .it :global(.led) { margin-top: 5px; }
  .bd { flex: 1; min-width: 0; display: flex; flex-direction: column; }
  .ti { color: var(--fg-hi); } .unread .ti { font-weight: 600; }
  .tx { font-size: var(--fs-sm); color: var(--fg-mute); overflow: hidden; text-overflow: ellipsis; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; }
  .ag { font-size: 10px; color: var(--fg-faint); flex: none; padding-top: 2px; }
</style>
