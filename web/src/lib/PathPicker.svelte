<script>
  // Browse this Mac (through the backend, under the agents' file policy) and pick files or a folder to attach.
  import { call, toast } from './store.svelte.js';
  import { sizeText } from './attach.js';
  import Modal from './ui/Modal.svelte';
  import Button from './ui/Button.svelte';
  import Input from './ui/Input.svelte';
  import Checkbox from './ui/Checkbox.svelte';
  import Icon from './ui/Icon.svelte';

  let { open = $bindable(false), onpick } = $props();
  let list = $state(null);
  let typed = $state('');
  let picked = $state([]); // absolute paths of ticked files
  let hidden = $state(false);
  let busy = $state(false);

  async function go(path) {
    busy = true;
    const r = await call('fs.browse', { path, hidden }, { quiet: true, });
    busy = false;
    if (r) { list = r; typed = r.path; picked = []; }
    else toast('That place cannot be opened (missing, or protected)', 'warn');
  }
  $effect(() => { if (open && !list) go(''); });
  const join = (n) => (list.path === '/' ? '/' + n : list.path + '/' + n);
  function toggle(e, on) { picked = on ? [...picked, join(e.name)] : picked.filter((x) => x !== join(e.name)); }
  function finish(paths) { onpick?.(paths); open = false; }
  const crumbs = $derived(list ? list.path.split('/').filter(Boolean) : []);
</script>

<Modal bind:open title="Attach from this Mac" width={640}>
  <div class="row">
    <Button size="sm" variant="ghost" title="up one level" disabled={!list?.parent} onclick={() => go(list.parent)}>↑</Button>
    <Button size="sm" variant="ghost" title="home folder" onclick={() => go(list?.home || '')}>~</Button>
    <div class="grow"><Input size="sm" mono bind:value={typed} onenter={() => go(typed)} placeholder="/path/to/folder" /></div>
    <Checkbox bind:checked={hidden} label="hidden" onchange={() => go(list?.path || '')} />
  </div>
  <div class="crumbs sm mute">{#each crumbs as c, i}<button type="button" onclick={() => go('/' + crumbs.slice(0, i + 1).join('/'))}>{c}</button>{i < crumbs.length - 1 ? ' / ' : ''}{/each}</div>
  <div class="ents scroll" class:busy>
    {#if list}
      {#each list.entries as e (e.name)}
        {#if e.dir}
          <button type="button" class="ent dir" ondblclick={() => go(join(e.name))} onclick={() => go(join(e.name))}><Icon name="folder" size={12} /><span class="ellipsis">{e.name}</span></button>
        {:else}
          <div class="ent"><Checkbox checked={picked.includes(join(e.name))} onchange={(v) => toggle(e, v)} label={e.name} /><span class="sm mute nowrap">{sizeText(e.size)}</span></div>
        {/if}
      {:else}<div class="sm mute pad">empty</div>{/each}
      {#if list.more}<div class="sm mute pad">…and {list.more} more not shown</div>{/if}
    {/if}
  </div>
  <div class="sm mute">Nothing is copied: the agents read these places directly, and only where their file policy allows it (keys, keychains and browser data stay off limits).</div>
  {#snippet footer()}
    <Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
    <Button disabled={!list} onclick={() => finish([list.path])}><Icon name="folder" size={12} /> Attach this folder</Button>
    <Button variant="primary" disabled={!picked.length} onclick={() => finish(picked)}>Attach {picked.length || ''} file{picked.length === 1 ? '' : 's'}</Button>
  {/snippet}
</Modal>

<style>
  .crumbs { display: flex; flex-wrap: wrap; gap: 2px; }
  .crumbs button { background: none; border: 0; padding: 0; color: var(--fg-dim); }
  .crumbs button:hover { color: var(--fg-hi); text-decoration: underline; }
  .ents { height: 300px; border: 1px solid var(--line); background: var(--bg); display: flex; flex-direction: column; }
  .ents.busy { opacity: 0.6; }
  .ent { display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 1px 8px; }
  .ent:hover { background: var(--bg-2); }
  .dir { width: 100%; justify-content: flex-start; background: none; border: 0; color: var(--fg-hi); text-align: left; }
  .pad { padding: 8px; }
</style>
