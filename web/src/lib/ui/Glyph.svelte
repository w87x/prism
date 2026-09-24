<script>
  // An agent's Font Awesome icon (assigned automatically from its soul).
  import { iconOf } from '../store.svelte.js';
  let { name, size = 0 } = $props();
</script>

<i class="fa" style={size ? `font-size:${size}px` : null}>{iconOf(name)}</i>
