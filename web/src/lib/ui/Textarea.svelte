<script>
  let { value = $bindable(), rows = 4, placeholder = '', disabled = false, readonly = false, mono = true, autosize = false, maxRows = 12, onenter, oninput, onkey, ...rest } = $props();
  let el;
  function fit() {
    if (!autosize || !el) return;
    el.style.height = 'auto';
    const lh = parseFloat(getComputedStyle(el).lineHeight) || 18;
    el.style.height = Math.min(el.scrollHeight, lh * maxRows + 10) + 'px';
  }
  $effect(() => { value; fit(); });
  $effect(() => {
    if (!el || !autosize) return;
    let w = el.offsetWidth;
    const ro = new ResizeObserver(() => { if (el.offsetWidth !== w) { w = el.offsetWidth; fit(); } });
    ro.observe(el);
    fit();
    return () => ro.disconnect();
  });
  export function focus() { el?.focus(); }
</script>

<textarea bind:this={el} {...rest} bind:value {rows} {placeholder} {disabled} {readonly} class:mono spellcheck="false"
  oninput={(e) => { fit(); oninput?.(e); }}
  onkeydown={(e) => { if (onkey?.(e)) return; if (onenter && e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); onenter(e); } }}></textarea>

<style>
  /* textareas are inline-block by default, which leaves baseline descender space below them inside a flex
     item (a wrapping div ends up taller than the textarea itself) and throws off flex alignment of siblings. */
  textarea { display: block; width: 100%; resize: vertical; background: var(--bg); border: 1px solid var(--line-2); border-radius: var(--r); padding: 4px 7px; color: var(--fg-hi); caret-color: var(--fg); outline: 0; line-height: 1.45; }
  textarea:focus { border-color: var(--fg); box-shadow: var(--glow-sm); }
  textarea::placeholder { color: var(--fg-faint); }
  textarea:disabled { opacity: 0.5; }
</style>
