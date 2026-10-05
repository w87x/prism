<script>
  // Colour theme selector: one card per theme with a swatch strip; the choice applies at once and is remembered on this device.
  import { THEMES, applyTheme, currentTheme } from '../themes.js';
  let cur = $state(currentTheme());
  const pick = (id) => { cur = applyTheme(id); };
</script>

<div class="themes" role="radiogroup" aria-label="Colour theme">
  {#each THEMES as t (t.id)}
    <button type="button" role="radio" aria-checked={cur === t.id} class="card" class:on={cur === t.id} onclick={() => pick(t.id)}>
      <span class="sw">{#each t.swatch as c}<i style="background:{c}"></i>{/each}</span>
      <b>{t.name}</b>
      <span class="sm mute">{t.blurb}</span>
    </button>
  {/each}
</div>

<style>
  .themes { display: grid; grid-template-columns: repeat(auto-fill, minmax(190px, 1fr)); gap: 8px; }
  .card { display: flex; flex-direction: column; align-items: flex-start; gap: 5px; padding: 8px; text-align: left; background: var(--bg-2); border: 1px solid var(--line); color: var(--fg); }
  .card:hover { border-color: var(--line-3); }
  .card.on { border-color: var(--accent); box-shadow: var(--glow-accent); }
  .card b { color: var(--fg-hi); letter-spacing: 0.06em; text-transform: uppercase; font-size: var(--fs-sm); }
  .sw { display: flex; width: 100%; height: 14px; border: 1px solid var(--line-2); }
  .sw i { flex: 1; display: block; }
</style>
