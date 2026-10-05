<script>
  // Colour theme selector: one card per theme with a swatch strip; the choice applies at once and is remembered on this device.
  import { THEMES, TEXTURES, applyTheme, currentTheme, applyTexture, currentTexture, applyMix, currentMix, MINERAL_IDS, MAX_MIX } from '../themes.js';
  let cur = $state(currentTheme());
  const pick = (id) => { cur = applyTheme(id); };
  let tex = $state(currentTexture());
  const pickTex = (id) => { tex = applyTexture(id); };
  // blend up to three minerals; none picked = the theme's own
  let mix = $state(currentMix());
  const toggle = (id) => { mix = applyMix(mix.includes(id) ? mix.filter((x) => x !== id) : mix.length < MAX_MIX ? [...mix, id] : [...mix.slice(1), id]); };
  const label = (id) => id[0].toUpperCase() + id.slice(1);
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
<div class="tex" role="radiogroup" aria-label="Background texture">
  <span class="sm mute">Background</span>
  {#each TEXTURES as x (x.id)}<button type="button" role="radio" aria-checked={tex === x.id} class="chip" class:on={tex === x.id} onclick={() => pickTex(x.id)} title={x.id === 'mineral' ? 'a pattern drawn from the theme\'s own mineral' : x.id === 'grid' ? 'the plain fine grid' : 'no pattern'}>{x.name}</button>{/each}
</div>
{#if tex === 'mineral'}
  <div class="tex" aria-label="Minerals to blend">
    <span class="sm mute">Minerals</span>
    <button type="button" class="chip" class:on={!mix.length} onclick={() => (mix = applyMix([]))} title="the pattern of the selected theme">Theme's own</button>
    {#each MINERAL_IDS as id (id)}<button type="button" class="chip" class:on={mix.includes(id)} onclick={() => toggle(id)}>{label(id)}</button>{/each}
  </div>
  <div class="sm mute hint">Pick up to {MAX_MIX} to blend them; a fourth replaces the oldest.</div>
{/if}

<style>
  .themes { display: grid; grid-template-columns: repeat(auto-fill, minmax(190px, 1fr)); gap: 8px; }
  .card { display: flex; flex-direction: column; align-items: flex-start; gap: 5px; padding: 8px; text-align: left; background: var(--bg-2); border: 1px solid var(--line); color: var(--fg); }
  .card:hover { border-color: var(--line-3); }
  .card.on { border-color: var(--accent); box-shadow: var(--glow-accent); }
  .card b { color: var(--fg-hi); letter-spacing: 0.06em; text-transform: uppercase; font-size: var(--fs-sm); }
  .sw { display: flex; width: 100%; height: 14px; border: 1px solid var(--line-2); }
  .sw i { flex: 1; display: block; }
  .tex { display: flex; align-items: center; gap: 6px; margin-top: 8px; flex-wrap: wrap; }
  .hint { margin-top: 4px; }
  .chip { padding: 2px 9px; background: var(--bg-2); border: 1px solid var(--line); color: var(--fg-dim); font-size: var(--fs-sm); text-transform: uppercase; letter-spacing: 0.06em; }
  .chip.on { border-color: var(--accent); color: var(--fg-hi); box-shadow: var(--glow-accent); }
</style>
