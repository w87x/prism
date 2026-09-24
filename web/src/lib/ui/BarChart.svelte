<script>
  // Column chart, stacked or single-series: thin bars (≤24px) with a 4px rounded data-end, a 2px surface gap
  // between stacked segments, hairline grid, hover/focus tooltip, and an optional table view.
  // series: [{ name, color, values }] in stack order (bottom first).
  let { labels = [], series = [], height = 170, format = (n) => n.toLocaleString(), label = (l) => l, ariaLabel = '', table = false, unit = '' } = $props();

  let w = $state(0);
  const M = { l: 44, r: 8, t: 14, b: 22 };
  const totals = $derived(labels.map((_, i) => series.reduce((a, s) => a + (s.values[i] || 0), 0)));
  const rawMax = $derived(Math.max(0, ...totals));

  // round the axis to clean numbers: 0 / 1,000 / 2,000 …
  const scale = $derived.by(() => {
    if (rawMax <= 0) return { max: 4, ticks: [0, 1, 2, 3, 4] };
    const rough = rawMax / 4;
    const mag = 10 ** Math.floor(Math.log10(rough));
    const step = [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => s >= rough);
    const max = Math.ceil(rawMax / step) * step;
    const ticks = [];
    for (let v = 0; v <= max + 1e-9; v += step) ticks.push(v);
    return { max, ticks };
  });

  const plotW = $derived(Math.max(0, w - M.l - M.r));
  const plotH = $derived(height - M.t - M.b);
  const band = $derived(labels.length ? plotW / labels.length : 0);
  const barW = $derived(Math.max(3, Math.min(24, band * 0.62)));
  const y = (v) => M.t + plotH - (v / scale.max) * plotH;
  const every = $derived(Math.max(1, Math.ceil(labels.length / Math.max(1, Math.floor(plotW / 54)))));
  const peak = $derived(totals.indexOf(rawMax));

  // top-rounded rect path (square at the baseline side)
  function cap(x, top, wd, h) {
    const r = Math.min(4, wd / 2, h);
    return `M${x},${top + h}V${top + r}Q${x},${top} ${x + r},${top}H${x + wd - r}Q${x + wd},${top} ${x + wd},${top + r}V${top + h}Z`;
  }
  // segments of one column, bottom-up, each separated from the one below by a 2px gap
  function segs(i) {
    const out = [];
    let acc = 0;
    let top = -1;
    series.forEach((s, k) => { if ((s.values[i] || 0) > 0) top = k; });
    series.forEach((s, k) => {
      const v = s.values[i] || 0;
      if (v <= 0) return;
      const y1 = y(acc + v), y0 = y(acc);
      const gap = acc > 0 ? 2 : 0;
      const h = Math.max(1, y0 - y1 - gap);
      out.push({ k, color: s.color, top: y1, h, round: k === top });
      acc += v;
    });
    return out;
  }

  let hover = $state(-1);
  const tipX = $derived(hover < 0 ? 0 : Math.min(Math.max(M.l + band * (hover + 0.5), 70), w - 70));
  const desc = (i) => `${label(labels[i])}: ${series.map((s) => `${s.name} ${format(s.values[i] || 0)}`).join(', ')}`;
</script>

<div class="chart" bind:clientWidth={w}>
  {#if series.length > 1}
    <div class="legend">{#each series as s}<span><i style="background:{s.color}"></i>{s.name}</span>{/each}</div>
  {/if}
  {#if table}
    <table class="tv">
      <thead><tr><th></th>{#each series as s}<th>{s.name}</th>{/each}</tr></thead>
      <tbody>{#each labels as l, i}<tr><td>{label(l)}</td>{#each series as s}<td>{format(s.values[i] || 0)}</td>{/each}</tr>{/each}</tbody>
    </table>
  {:else if w > 0}
    <svg width={w} {height} role="img" aria-label={ariaLabel} onpointerleave={() => (hover = -1)}>
      {#each scale.ticks as t}
        <line x1={M.l} x2={w - M.r} y1={y(t)} y2={y(t)} class="grid" />
        <text x={M.l - 6} y={y(t) + 3.5} class="ax" text-anchor="end">{format(t)}{unit}</text>
      {/each}
      {#each labels as l, i}
        {@const x = M.l + band * i + (band - barW) / 2}
        <g class="col" class:dim={hover >= 0 && hover !== i} tabindex="0" role="img" aria-label={desc(i)}
          onpointermove={() => (hover = i)} onfocus={() => (hover = i)} onblur={() => (hover = -1)}>
          <rect x={M.l + band * i} y={M.t} width={band} height={plotH} class="hit" />
          {#each segs(i) as s}
            {#if s.round}<path d={cap(x, s.top, barW, s.h)} fill={s.color} />{:else}<rect {x} y={s.top} width={barW} height={s.h} fill={s.color} />{/if}
          {/each}
        </g>
        {#if i % every === 0 || i === labels.length - 1 && every === 1}<text x={M.l + band * (i + 0.5)} y={height - 6} class="ax" text-anchor="middle">{label(l)}</text>{/if}
      {/each}
      {#if peak >= 0 && rawMax > 0}<text x={M.l + band * (peak + 0.5)} y={y(rawMax) - 5} class="pk" text-anchor="middle">{format(rawMax)}</text>{/if}
    </svg>
    {#if hover >= 0}
      <div class="tip" style="left:{tipX}px" role="presentation">
        <div class="th">{label(labels[hover])}</div>
        {#each series as s}<div class="tr"><i style="background:{s.color}"></i><b>{format(s.values[hover] || 0)}</b><span>{s.name}</span></div>{/each}
      </div>
    {/if}
  {/if}
</div>

<style>
  .chart { position: relative; width: 100%; }
  .legend { display: flex; gap: 14px; margin-bottom: 4px; font-size: var(--fs-sm); color: var(--fg-dim); }
  .legend i { display: inline-block; width: 10px; height: 10px; margin-right: 5px; border-radius: 2px; vertical-align: -1px; }
  svg { display: block; overflow: visible; }
  .grid { stroke: var(--line); stroke-width: 1; }
  .ax { fill: var(--fg-mute); font-size: 10px; }
  .pk { fill: var(--fg-dim); font-size: 10px; }
  .hit { fill: transparent; }
  .col { outline: none; cursor: default; }
  .col.dim { opacity: 0.55; }
  .col:focus-visible .hit { stroke: var(--fg-mute); stroke-width: 1; }
  .tip { position: absolute; top: 18px; transform: translateX(-50%); pointer-events: none; z-index: 5; background: var(--bg-2); border: 1px solid var(--line-3); padding: 5px 9px; min-width: 120px; box-shadow: 0 6px 20px rgba(0, 0, 0, 0.6); }
  .th { color: var(--fg-mute); font-size: 10px; text-transform: uppercase; letter-spacing: 0.08em; margin-bottom: 3px; }
  .tr { display: flex; align-items: center; gap: 6px; line-height: 1.5; }
  .tr i { width: 10px; height: 3px; border-radius: 1px; flex: none; }
  .tr b { color: var(--fg-hi); font-weight: 600; min-width: 4.5em; text-align: right; }
  .tr span { color: var(--fg-dim); font-size: var(--fs-sm); }
  .tv { width: 100%; border-collapse: collapse; font-size: var(--fs-sm); }
  .tv th, .tv td { padding: 2px 8px; text-align: right; border-bottom: 1px solid var(--line); color: var(--fg-dim); }
  .tv th:first-child, .tv td:first-child { text-align: left; color: var(--fg); }
</style>
