<script>
  let { name, size = 15 } = $props();
  const P = {
    chat: 'M2 3h12v8H7l-3 3v-3H2z',
    agents: 'M8 1.5l5.5 3.2v6.6L8 14.5 2.5 11.3V4.7z M8 6.2a1.8 1.8 0 1 0 0 3.6 1.8 1.8 0 0 0 0-3.6',
    tasks: 'M5.5 4h8.5M5.5 8h8.5M5.5 12h8.5M2 4h.5M2 8h.5M2 12h.5',
    memory: 'M3 4c0-1.1 2.2-2 5-2s5 .9 5 2-2.2 2-5 2-5-.9-5-2zM3 4v8c0 1.1 2.2 2 5 2s5-.9 5-2V4M3 8c0 1.1 2.2 2 5 2s5-.9 5-2',
    tools: 'M10.5 2a3.5 3.5 0 0 0-3.2 4.9L2 12.2 3.8 14l5.3-5.3A3.5 3.5 0 0 0 14 5.5l-2 2-2-.5-.5-2 2-2z',
    auto: 'M8 2a6 6 0 1 0 0 12A6 6 0 0 0 8 2zM8 4.8V8l2.4 1.6',
    kb: 'M3 2h7a2 2 0 0 1 2 2v9H5a2 2 0 0 1-2-2zM6 5.5h4M6 8h4M6 10.5h2',
    bell: 'M8 2a4 4 0 0 0-4 4v3l-1.5 2h11L12 9V6a4 4 0 0 0-4-4zM6.4 13a1.7 1.7 0 0 0 3.2 0',
    library: 'M3 2h7a2 2 0 0 1 2 2v10H5a2 2 0 0 1-2-2zM3 12a2 2 0 0 1 2-2h7',
    settings: 'M2 4h6M11 4h3M2 8h3M8 8h6M2 12h8M13 12h1M9.5 2v4M6.5 6v4M11.5 10v4',
    menu: 'M2 4h12M2 8h12M2 12h12',
    panel: 'M2 3h12v10H2zM10 3v10',
    plus: 'M8 3v10M3 8h10',
    x: 'M3.5 3.5l9 9M12.5 3.5l-9 9',
    refresh: 'M13 8a5 5 0 1 1-1.6-3.7M13 2.5V5h-2.5',
    send: 'M2 8l12-5-4.5 11-2-4.5z',
    usage: 'M2.5 13.5h11M4.5 13V8.5M8 13V3.5M11.5 13V6',
    image: 'M2 3h12v10H2zM2 11.5l4-4 3 3 2-2 3 3M10.6 6.2a.8.8 0 1 0 0 .01',
    stop: 'M4 4h8v8H4z',
    trash: 'M3 4h10M6 4V2.5h4V4M4.5 4l.6 9h5.8l.6-9',
    compact: 'M3 6l5 3 5-3M3 10l5 3 5-3M8 1v5',
    edit: 'M2 14l1-4 8-8 3 3-8 8zM10 3l3 3',
    check: 'M3 8.5l3.3 3.3L13 4.5',
    warn: 'M8 2l6.5 11.5h-13zM8 6.5v3.2M8 11.2v.4',
    link: 'M6.5 9.5a3 3 0 0 0 4.2 0l2-2a3 3 0 0 0-4.2-4.2l-.8.8M9.5 6.5a3 3 0 0 0-4.2 0l-2 2a3 3 0 0 0 4.2 4.2l.8-.8',
    play: 'M4 2.5l9 5.5-9 5.5z',
    graph: 'M3 3h3v3H3zM10 3h3v3h-3zM6.5 10h3v3h-3zM6 4.5h4M4.5 6l3 4M11.5 6l-3 4',
    dl: 'M8 2v8M4.5 7L8 10.5 11.5 7M3 13h10',
    clip: 'M12.5 7.5l-4.6 4.6a2.6 2.6 0 0 1-3.7-3.7l5-5a1.7 1.7 0 0 1 2.5 2.4l-5 5a.8.8 0 0 1-1.2-1.2l4.4-4.4',
    folder: 'M2 4.5h4l1.5 1.5H14v7H2zM2 4.5V3h4l1.5 1.5',
    merge: 'M3 3v3c0 2 2 3 5 3s5-1 5-3V3M8 9v5M5.5 11.5L8 14l2.5-2.5',
    split: 'M8 2v5M8 7c0 2-3 2-3 4v3M8 7c0 2 3 2 3 4v3',
    today: 'M8 3v2M8 11v2M3 8h2M11 8h2M4.8 4.8l1.4 1.4M9.8 9.8l1.4 1.4M11.2 4.8l-1.4 1.4M6.2 9.8l-1.4 1.4M8 5.5a2.5 2.5 0 1 0 0 5 2.5 2.5 0 0 0 0-5',
    pin: 'M8 2a2.5 2.5 0 0 1 2.5 2.5c0 1.2-.8 2.1-1.6 2.6L10.5 11h-5l1.6-3.9C6.3 6.6 5.5 5.7 5.5 4.5A2.5 2.5 0 0 1 8 2zM8 11v3',
    table: 'M2 3h12v10H2zM2 6.5h12M2 10h12M6 3v10M10 3v10',
    search: 'M7 2a5 5 0 1 0 0 10 5 5 0 0 0 0-10zM10.8 10.8L14 14',
  };
</script>

<svg viewBox="0 0 16 16" width={size} height={size} fill="none" stroke="currentColor" stroke-width="0.8" stroke-linecap="square" stroke-linejoin="miter" aria-hidden="true">
  <path d={P[name] || ''} />
</svg>
