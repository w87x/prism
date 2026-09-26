<script>
  // Full-screen briefing reader for tablets and phones: one briefing at a time in a large font, with previous / next,
  // mark read and dismiss. The queue is fixed when it opens (so acting on one does not shuffle the rest), and briefings
  // that arrive while it is open are appended and shown as they come.
  import { S, call, listen, toast, ago, stamp } from './store.svelte.js';
  import RichMessage from './rich/RichMessage.svelte';
  import Button from './ui/Button.svelte';
  import Badge from './ui/Badge.svelte';
  import Textarea from './ui/Textarea.svelte';

  let briefs = $state({}); // id -> briefing
  let queue = $state([]); // ids, oldest → newest
  let pos = $state(0);
  let mode = $state('unread'); // unread | all — what the queue was built from
  let replyOpen = $state(false);
  let replyText = $state('');
  let fresh = $state(0); // arrivals while reading that are not on screen
  let started = false;

  const cur = $derived(briefs[queue[pos]]);
  const live = (b) => b.status !== 'dismissed';

  async function fetchAll() {
    const r = (await call('briefings.list', {}, { quiet: true })) || [];
    for (const b of r) briefs[b.id] = b;
    return r;
  }
  async function build(startId) {
    const r = await fetchAll();
    const pick = r.filter((b) => (mode === 'unread' ? b.status === 'new' : live(b)));
    queue = pick.map((b) => b.id).sort((a, b) => a - b);
    // opening one specific briefing must always include it
    if (startId && !queue.includes(startId) && briefs[startId]) queue = [...queue, startId].sort((a, b) => a - b);
    pos = startId && queue.includes(startId) ? queue.indexOf(startId) : Math.max(0, queue.length - 1);
    fresh = 0;
    seen();
  }
  $effect(() => {
    if (S.reader.open && !started) { started = true; build(S.reader.id); }
    if (!S.reader.open) started = false;
  });
  $effect(() => listen('briefing.new', async () => {
    if (!S.reader.open) return;
    const known = new Set(queue);
    const r = await fetchAll();
    const added = r.filter((b) => b.status === 'new' && !known.has(b.id)).map((b) => b.id).sort((a, b) => a - b);
    if (!added.length) return;
    const atEnd = pos >= queue.length - 1;
    queue = [...queue, ...added];
    if (atEnd) { pos = queue.length - 1; seen(); toast('New briefing', 'info'); } else fresh += added.length;
  }));

  async function setStatus(b, status) {
    b.status = status;
    await call('briefings.status', { id: b.id, status });
    S.briefRev++; // the Autonomy list and Today reload
  }
  // showing a briefing counts as reading it
  function seen() { const b = cur; if (b && b.status === 'new') setStatus(b, 'delivered'); replyOpen = false; replyText = ''; }
  const go = (d) => { const n = pos + d; if (n >= 0 && n < queue.length) { pos = n; if (n >= queue.length - 1) fresh = 0; seen(); } };
  async function toggleRead() { if (cur) await setStatus(cur, cur.status === 'new' ? 'delivered' : 'new'); }
  async function dismiss() {
    if (!cur) return;
    await setStatus(cur, 'dismissed');
    const id = cur.id;
    queue = queue.filter((x) => x !== id);
    if (pos >= queue.length) pos = Math.max(0, queue.length - 1);
    seen();
    if (!queue.length) close();
  }
  async function reply() {
    const ok = await call('briefings.reply', { id: cur.id, text: replyText });
    if (ok) { cur.reply = replyText; replyText = ''; replyOpen = false; toast(`${cur.agent} got your answer`); S.briefRev++; }
  }
  async function obsidian() { const rel = await call('briefings.save_obsidian', { id: cur.id }); if (rel) toast(`Saved to Obsidian: ${rel}`); }
  const close = () => { S.reader.open = false; S.reader.id = null; };
  async function switchMode(m) { mode = m; started = true; await build(null); }
  function key(e) {
    if (!S.reader.open || e.target.closest?.('textarea,input')) return;
    if (e.key === 'Escape') close();
    else if (e.key === 'ArrowRight' || e.key === 'j') go(1);
    else if (e.key === 'ArrowLeft' || e.key === 'k') go(-1);
    else if (e.key === 'd') dismiss();
    else if (e.key === 'r') toggleRead();
  }
  // swipe left / right on a touch screen
  let sx = 0;
  const down = (e) => { sx = e.clientX; };
  const up = (e) => { const dx = e.clientX - sx; if (Math.abs(dx) > 90 && e.pointerType === 'touch') go(dx < 0 ? 1 : -1); };
  const asks = (b) => !b.reply && /\?/.test(b.body);
</script>

<svelte:window onkeydown={key} />

{#if S.reader.open}
  <div class="rd" role="dialog" aria-label="Briefing reader">
    <header>
      <button type="button" class="x" aria-label="close" onclick={close}>✕</button>
      <span class="pos">{queue.length ? pos + 1 : 0} / {queue.length}</span>
      <div class="seg" role="tablist">
        <button type="button" class:on={mode === 'unread'} onclick={() => switchMode('unread')}>Unread</button>
        <button type="button" class:on={mode === 'all'} onclick={() => switchMode('all')}>All</button>
      </div>
      <span class="grow"></span>
      {#if fresh}<button type="button" class="pill" onclick={() => { pos = queue.length - 1; fresh = 0; seen(); }}>{fresh} new ›</button>{/if}
    </header>

    {#if cur}
      <main onpointerdown={down} onpointerup={up}>
        <article>
          <div class="meta">
            <Badge tone={cur.importance >= 4 ? 'attn' : 'mute'}>P{cur.importance}</Badge>
            <span>{cur.agent}</span><span class="mute">{stamp(cur.created_at)} · {ago(cur.created_at)}</span>
            {#if cur.status === 'new'}<Badge tone="accent">unread</Badge>{/if}
            {#if asks(cur)}<Badge tone="attn">asks you something</Badge>{:else if cur.reply}<Badge tone="ok">answered</Badge>{/if}
          </div>
          <h1>{cur.title}</h1>
          <div class="body"><RichMessage text={cur.body} /></div>
          {#if cur.reply}<h2>Your answer</h2><div class="body dim"><RichMessage text={cur.reply} /></div>{/if}
          {#if replyOpen}
            <h2>{cur.reply ? 'Add to your answer' : 'Answer'}</h2>
            <Textarea bind:value={replyText} rows={4} mono={false} placeholder="{cur.agent} reads this, remembers what matters and acts on it" />
            <div class="rrow"><Button variant="ghost" onclick={() => (replyOpen = false)}>Cancel</Button><Button variant="primary" disabled={!replyText.trim()} onclick={reply}>Send</Button></div>
          {/if}
        </article>
      </main>
    {:else}
      <main class="none"><div>{mode === 'unread' ? 'No unread briefings.' : 'No briefings.'}<br /><span class="mute">New ones appear here as they arrive.</span></div></main>
    {/if}

    <footer>
      <button type="button" class="nav" aria-label="previous" disabled={pos <= 0} onclick={() => go(-1)}>‹</button>
      <div class="acts">
        <Button variant="ghost" disabled={!cur} onclick={toggleRead}>{cur?.status === 'new' ? 'Mark read' : 'Mark unread'}</Button>
        <Button variant="ghost" disabled={!cur} onclick={() => (replyOpen = !replyOpen)}>Reply</Button>
        <Button variant="ghost" disabled={!cur} onclick={obsidian}>Obsidian</Button>
        <Button variant="danger" disabled={!cur} onclick={dismiss}>Dismiss</Button>
      </div>
      <button type="button" class="nav" aria-label="next" disabled={pos >= queue.length - 1} onclick={() => go(1)}>›</button>
    </footer>
  </div>
{/if}

<style>
  .rd { position: fixed; inset: 0; z-index: 200; display: flex; flex-direction: column; background: var(--bg); padding-top: env(safe-area-inset-top); }
  header { display: flex; align-items: center; gap: 12px; padding: 8px 12px; border-bottom: 1px solid var(--line-2); flex: none; }
  .x { background: none; border: 0; color: var(--fg-dim); font-size: 20px; min-width: 44px; min-height: 44px; }
  .pos { color: var(--fg-dim); font-variant-numeric: tabular-nums; font-size: 15px; }
  .seg { display: inline-flex; border: 1px solid var(--line-3); }
  .seg button { background: none; border: 0; color: var(--fg-dim); padding: 0 14px; min-height: 38px; font-size: 13px; text-transform: uppercase; letter-spacing: 0.08em; }
  .seg button.on { color: var(--fg-hi); background: linear-gradient(180deg, rgba(62, 232, 166, 0.16), transparent); }
  .grow { flex: 1; }
  .pill { background: var(--bg-2); border: 1px solid var(--accent); color: var(--accent-hi); min-height: 38px; padding: 0 16px; font-size: 14px; animation: pop 0.4s ease; }
  @keyframes pop { from { transform: scale(0.85); opacity: 0; } }
  main { flex: 1; min-height: 0; overflow-y: auto; padding: 24px 20px 32px; -webkit-overflow-scrolling: touch; }
  article { max-width: 780px; margin: 0 auto; }
  .meta { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; font-size: 14px; color: var(--fg-dim); margin-bottom: 10px; }
  h1 { margin: 0 0 18px; font-size: clamp(24px, 4.2vw, 34px); line-height: 1.25; color: var(--fg-hi); font-weight: 700; }
  h2 { margin: 26px 0 8px; font-size: 13px; text-transform: uppercase; letter-spacing: 0.12em; color: var(--fg-mute); }
  .body { font-size: clamp(17px, 2.6vw, 21px); line-height: 1.65; color: var(--fg); overflow-wrap: anywhere; }
  .body.dim { color: var(--fg-dim); }
  .rrow { display: flex; justify-content: flex-end; gap: 8px; margin-top: 8px; }
  .none { display: flex; align-items: center; justify-content: center; text-align: center; font-size: 20px; color: var(--fg-dim); line-height: 1.8; }
  footer { display: flex; align-items: stretch; gap: 8px; padding: 8px 10px calc(8px + env(safe-area-inset-bottom)); border-top: 1px solid var(--line-2); flex: none; background: var(--panel-bg); }
  .acts { flex: 1; display: flex; flex-wrap: wrap; justify-content: center; align-items: center; gap: 8px; }
  .nav { flex: none; width: 64px; min-height: 56px; background: var(--bg-1); border: 1px solid var(--line-3); color: var(--fg-hi); font-size: 34px; line-height: 1; }
  .nav:disabled { opacity: 0.3; }
  .acts :global(.btn) { min-height: 52px; padding: 0 20px; font-size: 15px; }
</style>
