<script>
  let { value = $bindable(), type = 'text', placeholder = '', disabled = false, readonly = false, onenter, oninput, invalid = false, mono = false, size = 'md', ...rest } = $props();
  let reveal = $state(false);
  const isPw = $derived(type === 'password');
</script>

<span class="inp {size}" class:invalid class:disabled>
  <input {...rest} type={isPw && !reveal ? 'password' : 'text'} bind:value {placeholder} {disabled} {readonly} class:mono autocomplete="off" spellcheck="false"
    oninput={(e) => oninput?.(e)} onkeydown={(e) => { if (e.key === 'Enter') onenter?.(e); }} />
  {#if isPw}<button type="button" class="eye" tabindex="-1" title={reveal ? 'hide' : 'show'} onclick={() => (reveal = !reveal)}>{reveal ? '◉' : '○'}</button>{/if}
</span>

<style>
  .inp { display: flex; align-items: center; width: 100%; background: var(--bg); border: 1px solid var(--line-2); border-radius: var(--r); }
  .inp:focus-within { border-color: var(--fg); box-shadow: var(--glow-sm); }
  .invalid { border-color: var(--err-dim); }
  .disabled { opacity: 0.5; }
  input { flex: 1; min-width: 0; background: transparent; border: 0; outline: 0; padding: 3px 7px; color: var(--fg-hi); caret-color: var(--fg); }
  .sm input { padding: 1px 5px; font-size: var(--fs-sm); }
  input::placeholder { color: var(--fg-faint); }
  .eye { background: none; border: 0; color: var(--fg-mute); padding: 0 6px; }
  .eye:hover { color: var(--fg); }
</style>
