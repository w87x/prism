<script>
  import { S } from './store.svelte.js';
  import Modal from './ui/Modal.svelte';
  import Button from './ui/Button.svelte';

  let open = $state(false);
  $effect(() => { open = !!S.confirm; });
  function done(v) {
    const c = S.confirm;
    S.confirm = null;
    open = false;
    c?.resolve(v);
  }
</script>

<Modal bind:open title={S.confirm?.title || ''} width={440} tone={S.confirm?.danger ? 'err' : ''} onclose={() => done(false)}>
  <div class="pre">{S.confirm?.text}</div>
  {#snippet footer()}
    <Button variant="ghost" onclick={() => done(false)}>Cancel</Button>
    <Button variant={S.confirm?.danger ? 'danger' : 'primary'} onclick={() => done(true)}>{S.confirm?.ok || 'OK'}</Button>
  {/snippet}
</Modal>
