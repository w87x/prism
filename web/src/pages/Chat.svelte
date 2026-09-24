<script>
  import { onMount } from 'svelte';
  import { S, call, toast, clock, stamp, setActivityMode } from '../lib/store.svelte.js';
  import { artifactUrl } from '../lib/ws.js';
  import { prepareImage, imagesFrom, MAX_IMAGES } from '../lib/image.js';
  import { readFile, filesFrom, sizeText, MAX_FILES, MAX_TOTAL } from '../lib/attach.js';
  import PathPicker from '../lib/PathPicker.svelte';
  import RichMessage from '../lib/rich/RichMessage.svelte';
  import ThinkingPanel from '../lib/ThinkingPanel.svelte';
  import Textarea from '../lib/ui/Textarea.svelte';
  import Button from '../lib/ui/Button.svelte';
  import Glyph from '../lib/ui/Glyph.svelte';
  import Segmented from '../lib/ui/Segmented.svelte';
  import Icon from '../lib/ui/Icon.svelte';
  import Modal from '../lib/ui/Modal.svelte';

  let text = $state('');
  let list = $state();
  let stick = true;
  let hover = false; // pointer over the log: never move it under the reader
  let unread = $state(0);
  let sending = $state(false);
  let ta = $state();
  let cmds = $state([]);
  let sel = $state(0);
  let menuOff = $state(false);

  // ── pictures: paste, drop or pick; shown as removable thumbnails until sent ──
  let atts = $state([]); // {name, mime, data, preview}
  let dragging = $state(false);
  let picker = $state();
  // ── other files (uploaded into the workspace) and places on this Mac (read in place) ──
  let docs = $state([]); // {name, mime, data, size}
  let paths = $state([]); // absolute paths
  let filePicker = $state();
  let pathOpen = $state(false);
  let attachOpen = $state(false);
  let attachWrap = $state();
  function pickImage() { attachOpen = false; picker.click(); }
  function pickFiles() { attachOpen = false; filePicker.click(); }
  function pickFolder() { attachOpen = false; pathOpen = true; }
  function outsideAttach(e) { if (attachOpen && !attachWrap?.contains(e.target)) attachOpen = false; }
  async function addDocs(files) {
    for (const f of files) {
      if (docs.length >= MAX_FILES) { toast(`At most ${MAX_FILES} files per message`, 'warn'); break; }
      if (docs.reduce((n, d) => n + d.size, f.size) > MAX_TOTAL) { toast('Those files are too big together — attach from this Mac instead', 'warn'); break; }
      try { docs.push(await readFile(f)); } catch (e) { toast(e.message, 'warn'); }
    }
  }
  function addPaths(ps) { for (const p of ps) if (!paths.includes(p)) paths.push(p); }
  const base = (p) => p.replace(/\/+$/, '').split('/').pop() || '/';
  async function addFiles(files) {
    for (const f of files) {
      if (atts.length >= MAX_IMAGES) { toast(`At most ${MAX_IMAGES} images per message`, 'warn'); break; }
      try { atts.push(await prepareImage(f)); } catch (e) { toast(e.message, 'warn'); }
    }
  }
  function dropAtt(i) { const [a] = atts.splice(i, 1); if (a) URL.revokeObjectURL(a.preview); }
  function onpaste(e) { const fs = imagesFrom(e.clipboardData); if (fs.length) { e.preventDefault(); addFiles(fs); } }
  function ondrop(e) { dragging = false; const fs = imagesFrom(e.dataTransfer), ds = filesFrom(e.dataTransfer); if (fs.length || ds.length) { e.preventDefault(); addFiles(fs); addDocs(ds); } }
  function ondragover(e) { if ([...(e.dataTransfer?.types || [])].includes('Files')) { e.preventDefault(); dragging = true; } }

  // ── feed: messages plus (optionally) agent activity, merged by time ──
  const feed = $derived.by(() => {
    const items = S.chat.map((m) => ({ k: 'msg', ts: Date.parse(m.created_at) || 0, m }));
    const mode = S.activityMode;
    if (mode !== 'off') {
      for (const a of S.activity) {
        // activity from an unrelated run tree (e.g. a Quick Ask, possibly several delegation hops deep)
        // must never show up in this conversation — see runKind inheritance in register().
        if (a.runKind === 'quick') continue;
        const sub = a.depth > 0;
        if (mode === 'agents' && !(sub && (a.kind === 'start' || a.kind === 'end'))) continue;
        if (mode === 'tools' && a.kind !== 'tool' && !(sub && (a.kind === 'start' || a.kind === 'end'))) continue;
        items.push({ k: 'act', ts: a.ts, a });
      }
    }
    return items.sort((x, y) => x.ts - y.ts);
  });

  // ── scrolling: follow new content unless the user scrolled up to read ──
  const toBottom = () => { if (list) { list.scrollTop = list.scrollHeight; stick = true; unread = 0; } };
  function onScroll() { stick = list.scrollHeight - list.scrollTop - list.clientHeight < 48; if (stick) unread = 0; }
  let seen = 0;
  $effect(() => {
    const n = feed.length; S.chat.at(-1)?.text;
    queueMicrotask(() => {
      if (stick || !hover) toBottom();
      else if (n > seen) unread += n - seen;
      seen = n;
    });
  });
  onMount(() => {
    toBottom();
    call('chat.commands', {}, { quiet: true }).then((c) => (cmds = c || []));
    const wake = () => !hover && toBottom(); // coming back to the window/tab: catch up with the latest message
    window.addEventListener('focus', wake);
    document.addEventListener('visibilitychange', wake);
    return () => { window.removeEventListener('focus', wake); document.removeEventListener('visibilitychange', wake); };
  });

  // ── slash commands ──
  const matches = $derived(text.startsWith('/') && !text.includes(' ') ? cmds.filter((c) => ('/' + c.name).startsWith(text.toLowerCase())) : []);
  const showMenu = $derived(matches.length > 0 && !menuOff);
  $effect(() => { text; menuOff = false; sel = 0; });
  function onkey(e) {
    if (!showMenu) return false;
    if (e.key === 'ArrowDown') { e.preventDefault(); sel = (sel + 1) % matches.length; return true; }
    if (e.key === 'ArrowUp') { e.preventDefault(); sel = (sel - 1 + matches.length) % matches.length; return true; }
    if (e.key === 'Escape') { e.preventDefault(); menuOff = true; return true; }
    if (e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey && text.trim() !== '/' + matches[sel].name)) { e.preventDefault(); pickCmd(matches[sel]); return true; }
    return false;
  }
  function pickCmd(c) { text = '/' + c.name + (c.args ? ' ' : ''); ta?.focus(); }

  async function send() {
    const t = text.trim();
    if ((!t && !atts.length && !docs.length && !paths.length) || sending) return;
    sending = true;
    text = '';
    stick = true;
    const sent = atts, sentDocs = docs, sentPaths = paths;
    let r;
    if (t.startsWith('/') && !sent.length && !sentDocs.length && !sentPaths.length) {
      r = await call('chat.command', { text: t });
      if (r === false) r = await call('chat.send', { text: t }); // not a command: an ordinary message
    } else {
      atts = []; docs = []; paths = [];
      r = await call('chat.send', { text: t, images: sent.map(({ name, mime, data }) => ({ name, mime, data })), files: sentDocs.map(({ name, mime, data }) => ({ name, mime, data })), paths: sentPaths });
      if (r === undefined) { atts = sent; docs = sentDocs; paths = sentPaths; } // keep the draft (and what was attached) if it failed
      else sent.forEach((a) => URL.revokeObjectURL(a.preview));
    }
    if (r === undefined) text = t; // keep the draft if it failed
    sending = false;
    ta?.focus();
  }
  // two-step inline confirm: first click arms it (red "sure?"), second click within a few seconds clears
  let clearArmed = $state(false);
  let clearTimer;
  function clear() {
    clearTimeout(clearTimer);
    if (!clearArmed) { clearArmed = true; clearTimer = setTimeout(() => (clearArmed = false), 3500); return; }
    clearArmed = false;
    call('chat.clear', { purge: true });
  }
  async function compact() {
    const r = await call('chat.compact');
    if (r) toast('Context compacted');
  }
  function exportChat() {
    if (!S.chat.length) { toast('Nothing to export yet', 'warn'); return; }
    const md = S.chat.map((m) => `### ${m.role === 'user' ? 'You' : m.agent || (m.role === 'system' ? 'System' : 'Agent')} · ${stamp(m.created_at)}\n\n${m.text}\n`).join('\n');
    const url = URL.createObjectURL(new Blob([`# PRISM conversation\n\n${md}`], { type: 'text/markdown' }));
    const a = Object.assign(document.createElement('a'), { href: url, download: `prism-chat-${new Date().toISOString().slice(0, 16).replace(/[:T]/g, '-')}.md` });
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
  const modes = [{ value: 'off', label: 'quiet' }, { value: 'agents', label: 'agents' }, { value: 'tools', label: '+ tools' }];

  // Quick Ask: a small, self-contained request run outside the main conversation — its own fresh session,
  // no automatic memory recall — for something like "create an agent that uses the tracker tools" that
  // doesn't need the full conversational history/context of a normal chat turn.
  let quickOpen = $state(false);
  let quickText = $state('');
  let quickBusy = $state(false);
  let quickLog = $state([]); // { q, a } pairs for this popup session (cleared on close)
  async function quickSend() {
    const q = quickText.trim();
    if (!q || quickBusy) return;
    quickText = '';
    quickBusy = true;
    const a = await call('chat.quick', { text: q });
    quickBusy = false;
    if (a !== undefined) quickLog.push({ q, a });
  }
  function openQuick() { quickOpen = true; quickLog = []; quickText = ''; }
</script>

<svelte:window onpointerdown={outsideAttach} />

<div class="page" class:drag={dragging} role="presentation" {ondragover} ondragleave={() => (dragging = false)} {ondrop}>
  <div class="log-wrap">
    <div class="log scroll" role="log" bind:this={list} onscroll={onScroll} onpointerenter={() => (hover = true)} onpointerleave={() => { hover = false; if (unread > 0) toBottom(); }}>
      {#each feed as it (it.k === 'msg' ? 'm' + it.m.id : 'a' + it.ts + it.a.run + (it.a.tool || it.a.kind))}
        {#if it.k === 'act'}
          {@const a = it.a}
          <div class="act" style="margin-left:{22 + Math.max(0, a.depth - 1) * 12}px">
            {#if a.kind === 'start'}↳ <Glyph name={a.agent} /> <b>{a.agent}</b> started{a.text ? ` — ${a.text}` : ''}
            {:else if a.kind === 'tool'}<Glyph name={a.agent} /> {a.agent} ▸ <b>{a.tool}</b> <span class="args">{a.text}</span>
            {:else}<Glyph name={a.agent} /> <b>{a.agent}</b> {a.status === 'done' ? 'finished' : a.status}{a.text ? ` — ${a.text}` : ''}{/if}
          </div>
        {:else}
          {@const m = it.m}
          {#if m.role === 'user'}
            <div class="m user" class:steer={m.steered}><span class="who">user:</span>{#if m.steered}<span class="steertag" title="sent while Atlas was working — it steers the current turn">↳ steering</span>{/if}<span class="tx"><span class="pre">{m.text}</span>
              {#if m.images?.length}<span class="pics">{#each m.images as id}<a href={artifactUrl(id)} target="_blank" rel="noopener noreferrer"><img src={artifactUrl(id)} alt="attached" loading="lazy" /></a>{/each}</span>{/if}</span><span class="ts">{clock(m.created_at)}</span></div>
          {:else if m.role === 'system'}
            <div class="m sys" class:bad={m.text.startsWith('Error')}><span class="who">system:</span><span class="tx pre">{m.text}</span></div>
          {:else}
            <div class="m agent"><span class="who"><span class="gl"><Glyph name={m.agent} /></span>{m.agent || 'agent'}:</span><span class="tx"><RichMessage text={m.text} /></span><span class="ts">{clock(m.created_at)}</span></div>
          {/if}
        {/if}
      {:else}
        <div class="m agent"><span class="who"><span class="gl"><Glyph name="Atlas" /></span>Atlas:</span><span class="tx">Hi! I'm Atlas — tell me what you need and I'll take it from there. Type <code>/</code> for commands.</span></div>
      {/each}
    </div>
    {#if unread > 0}<button class="pill" onclick={toBottom}>↓ {unread} new</button>{/if}
  </div>

  <ThinkingPanel />

  <div class="input">
    {#if showMenu}
      <div class="menu" role="listbox">
        {#each matches as c, i}
          <div class="cm" class:on={i === sel} role="option" aria-selected={i === sel} tabindex="-1" onpointerenter={() => (sel = i)} onpointerdown={(e) => { e.preventDefault(); pickCmd(c); }}>
            <b>/{c.name}</b><span class="ar">{c.args}</span><span class="hp">{c.help}</span>
          </div>
        {/each}
      </div>
    {/if}
    {#if docs.length || paths.length}
      <div class="chips">
        {#each docs as d, i}<span class="chip" title={d.name}><Icon name="clip" size={11} /><span class="ellipsis">{d.name}</span><span class="mute">{sizeText(d.size)}</span><button type="button" aria-label="remove {d.name}" onclick={() => docs.splice(i, 1)}>×</button></span>{/each}
        {#each paths as p, i}<span class="chip path" title={p}><Icon name="folder" size={11} /><span class="ellipsis">{base(p)}</span><button type="button" aria-label="remove {p}" onclick={() => paths.splice(i, 1)}>×</button></span>{/each}
      </div>
    {/if}
    {#if atts.length}
      <div class="atts">
        {#each atts as a, i}
          <span class="att"><img src={a.preview} alt={a.name} /><button type="button" class="rm" title="remove" aria-label="remove {a.name}" onclick={() => dropAtt(i)}>×</button></span>
        {/each}
      </div>
    {/if}
    <div class="row">
      <input bind:this={picker} type="file" accept="image/jpeg,image/png,image/gif,image/webp" multiple hidden onchange={(e) => { addFiles([...e.target.files]); e.target.value = ''; }} />
      <input bind:this={filePicker} type="file" multiple hidden onchange={(e) => { addDocs([...e.target.files]); e.target.value = ''; }} />
      <div class="attach" bind:this={attachWrap}>
        <Button class="iconbtn" variant="ghost" title="Attach: image, files, or a folder from this Mac" onclick={() => (attachOpen = !attachOpen)}>@</Button>
        {#if attachOpen}
          <div class="amenu" role="menu">
            <button type="button" class="ai" role="menuitem" onclick={pickImage}><Icon name="image" size={13} /> Image<span class="hp">paste / drop too</span></button>
            <button type="button" class="ai" role="menuitem" onclick={pickFiles}><Icon name="clip" size={13} /> Files<span class="hp">uploaded</span></button>
            <button type="button" class="ai" role="menuitem" onclick={pickFolder}><Icon name="folder" size={13} /> Folder…<span class="hp">from this Mac, in place</span></button>
          </div>
        {/if}
      </div>
      <div class="grow">
        <Textarea bind:this={ta} bind:value={text} autosize rows={1} maxRows={7} mono={false} {onkey} {onpaste}
          placeholder={S.busy ? 'Atlas is working — messages you send now steer the current turn…' : 'Message Atlas   (Enter to send · Shift+Enter new line · / for commands)'}
          onenter={send} />
      </div>
      <div class="send">
        {#if S.busy}<Button variant="danger" onclick={() => call('chat.stop')}><Icon name="stop" size={12} /> Stop</Button>{/if}
        <Button variant="primary" disabled={(!text.trim() && !atts.length && !docs.length && !paths.length) || sending} onclick={send}><Icon name="send" size={12} /> Send</Button>
      </div>
    </div>
    <div class="btns">
      <div class="mode" title="Show what other agents are doing inside the conversation">
        <span>activity</span><Segmented size="sm" value={S.activityMode} options={modes} onchange={setActivityMode} />
      </div>
      <span class="vsep"></span>
      <Button variant={clearArmed ? 'danger' : 'ghost'} size="sm" onclick={clear} title={clearArmed ? 'Click again to clear' : 'Clear the conversation'}><Icon name="trash" size={12} /> {clearArmed ? 'Sure?' : 'Clear'}</Button>
      <Button variant="ghost" size="sm" onclick={compact} title="Compact the context now"><Icon name="compact" size={12} /> Compact</Button>
      <Button variant="ghost" size="sm" onclick={exportChat} title="Download the conversation as Markdown"><Icon name="dl" size={12} /> Export</Button>
      <span class="vsep"></span>
      <Button variant="ghost" size="sm" onclick={openQuick} title="A small, self-contained request run outside this conversation — its own fresh session, no memory recall"><Icon name="send" size={12} /> Quick Ask</Button>
    </div>
  </div>
</div>

<PathPicker bind:open={pathOpen} onpick={addPaths} />

<Modal bind:open={quickOpen} title="Quick Ask" width={1400}>
  <!-- almost-full-size on purpose: the button that opens this lives at the bottom of the page, but Modal
       itself always anchors near the top of the screen — reserving a tall frame here (rather than letting it
       shrink to its content) keeps the input near the bottom of that frame, close to where the mouse already
       is, instead of forcing a long trip to the top for a "quick" ask. -->
  <div class="qframe">
    <div class="qhint sm mute">Runs as a fresh, minimal turn — no chat history, no memory recall. Good for small, self-contained asks like "create an agent that uses the tracker tools".</div>
    <div class="qlog-wrap">
      {#if quickLog.length}
        <div class="qlog">
          {#each quickLog as { q, a } (q + a)}
            <div class="qpair"><div class="qq">{q}</div><div class="qa">{a}</div></div>
          {/each}
        </div>
      {:else}
        <div class="qempty mute sm">Ask away — the answer will appear here.</div>
      {/if}
    </div>
    <div class="qinput">
      <div class="grow">
        <Textarea bind:value={quickText} rows={2} maxRows={6} autosize mono={false} placeholder="e.g. create an agent that uses the tracker tools"
          onenter={quickSend} disabled={quickBusy} />
      </div>
      <Button variant="primary" disabled={!quickText.trim() || quickBusy} loading={quickBusy} onclick={quickSend}><Icon name="send" size={12} /> Ask</Button>
    </div>
  </div>
</Modal>

<style>
  .page { display: flex; flex-direction: column; gap: 6px; height: 100%; min-height: 0; }
  .log-wrap { position: relative; flex: 1; min-height: 0; display: flex; }
  .log { flex: 1; border: 1px solid var(--line); background: var(--panel-bg); padding: 6px 10px 8px; display: flex; flex-direction: column; gap: 6px; }
  .m { display: flex; gap: 8px; align-items: baseline; }
  .who { flex: none; font-weight: 600; }
  .agent .who { color: var(--fg-hi); }
  .agent .tx { color: var(--fg); }
  .user { margin-left: 22px; }
  .user .who { color: var(--accent); }
  .user .tx { color: var(--accent-hi); }
  .sys .who { color: var(--fg-mute); } .sys .tx { color: var(--fg-dim); font-size: 0.95em; }
  .sys.bad .who, .sys.bad .tx { color: var(--err); }
  .tx { min-width: 0; flex: 1; line-height: 1.5; }
  .gl { display: inline-block; width: 1.5em; text-align: center; margin-right: 2px; opacity: 0.85; font-size: 0.9em; }
  .ts { flex: none; font-size: 10px; color: var(--fg-faint); align-self: flex-start; padding-top: 2px; }
  .act { font-size: 11px; color: var(--fg-mute); line-height: 1.4; }
  .act b { font-weight: 500; color: var(--fg-dim); } .act .args { color: var(--fg-faint); }
  .page.drag .log { outline: 2px dashed var(--accent); outline-offset: -4px; }
  .pics { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 4px; }
  .steertag { flex: none; align-self: flex-start; font-size: 9.5px; text-transform: uppercase; letter-spacing: 0.08em; color: var(--attn); border: 1px solid var(--attn-dim); padding: 0 5px; margin-right: 6px; }
  .m.steer .tx { border-left: 2px solid var(--attn-dim); padding-left: 6px; }
  .pics img { max-width: 220px; max-height: 160px; border: 1px solid var(--line-2); display: block; }
  .atts { display: flex; flex-wrap: wrap; gap: 6px; }
  .chips { display: flex; flex-wrap: wrap; gap: 5px; }
  .chip { display: inline-flex; align-items: center; gap: 5px; max-width: 260px; padding: 1px 6px; font-size: var(--fs-sm); background: var(--bg-2); border: 1px solid var(--line-2); color: var(--fg-dim); }
  .chip.path { border-color: var(--accent-dim); color: var(--accent-hi); }
  .chip button { background: none; border: 0; padding: 0 0 0 2px; color: var(--fg-mute); line-height: 1; }
  .chip button:hover { color: var(--err); }
  .att { position: relative; }
  .att img { height: 54px; max-width: 110px; object-fit: cover; border: 1px solid var(--line-2); display: block; }
  .att .rm { position: absolute; top: -6px; right: -6px; width: 16px; height: 16px; padding: 0; line-height: 14px; text-align: center; background: var(--bg-2); border: 1px solid var(--line-3); color: var(--fg-hi); border-radius: 50%; }
  .pill { position: absolute; left: 50%; bottom: 30px; transform: translateX(-50%); background: var(--bg-2); border: 1px solid var(--accent); color: var(--accent-hi); font-size: var(--fs-sm); padding: 2px 12px; box-shadow: var(--glow-accent); }
  .mode { display: flex; align-items: center; gap: 6px; font-size: 10px; text-transform: uppercase; letter-spacing: 0.1em; color: var(--fg-faint); }
  .row { display: flex; gap: 6px; align-items: flex-end; }
  /* the toolbar buttons match the textarea's single-line height (26px) and stay pinned to its bottom edge as it grows */
  .row :global(.iconbtn) { width: 26px; height: 26px; padding: 0; flex: none; }
  .attach { position: relative; }
  .attach :global(.iconbtn) { font-size: 15px; font-weight: 700; }
  .amenu { position: absolute; left: 0; bottom: 100%; margin-bottom: 4px; min-width: 190px; background: var(--bg-1); border: 1px solid var(--line-3); box-shadow: 0 8px 28px rgba(0, 0, 0, 0.7); z-index: 20; }
  .ai { display: flex; align-items: center; gap: 8px; width: 100%; padding: 5px 10px; background: none; border: 0; color: var(--fg); text-align: left; cursor: pointer; }
  .ai:hover { background: var(--bg-3); color: var(--fg-hi); }
  .ai .hp { margin-left: auto; font-size: 9.5px; }
  .send { display: flex; gap: 4px; flex: none; }
  .send :global(.btn) { height: 26px; }
  .vsep { width: 1px; height: 14px; background: var(--line-2); margin: 0 2px; }
  .input { position: relative; display: flex; flex-direction: column; gap: 5px; flex: none; }
  .btns { display: flex; gap: 6px; align-items: center; }
  .menu { position: absolute; left: 0; bottom: 100%; margin-bottom: 4px; min-width: min(380px, 92vw); max-width: 100%; background: var(--bg-1); border: 1px solid var(--line-3); box-shadow: 0 8px 28px rgba(0, 0, 0, 0.7); z-index: 20; }
  .cm { display: flex; gap: 10px; align-items: baseline; padding: 3px 10px; cursor: pointer; }
  .cm.on { background: var(--bg-4); }
  .cm b { color: var(--fg-hi); font-weight: 500; } .ar { color: var(--accent); font-size: var(--fs-sm); } .hp { color: var(--fg-mute); font-size: var(--fs-sm); margin-left: auto; }
  .qframe { display: flex; flex-direction: column; gap: 10px; min-height: 70vh; }
  .qhint { line-height: 1.4; flex: none; }
  .qlog-wrap { flex: 1; min-height: 0; display: flex; flex-direction: column; }
  .qlog { flex: 1; display: flex; flex-direction: column; gap: 8px; overflow: auto; }
  .qempty { flex: 1; display: flex; align-items: center; justify-content: center; text-align: center; }
  .qpair { border-top: 1px solid var(--line-2); padding-top: 6px; }
  .qq { color: var(--fg-dim); font-size: var(--fs-sm); text-transform: uppercase; letter-spacing: 0.06em; margin-bottom: 3px; }
  .qa { white-space: pre-wrap; }
  .qinput { display: flex; gap: 6px; align-items: flex-end; flex: none; }
</style>
