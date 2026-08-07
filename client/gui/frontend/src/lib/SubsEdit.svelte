<script>
  // Structured editor for doc.Subscriptions. Mutates the shared doc in place and
  // calls touch() so the container recomputes "dirty" (Svelte won't see a nested
  // mutation otherwise).
  export let doc
  export let touch = () => {}

  function add() {
    ;(doc.Subscriptions ||= []).push({ Name: '', URL: '', Format: 'auto', Tags: ['normal'], Enabled: true, Expires: '' })
    touch()
  }
  function remove(i) {
    doc.Subscriptions.splice(i, 1)
    touch()
  }
</script>

<div class="row-head">
  <h2>Подписки</h2>
  <button class="fix" on:click={add}>+ Подписка</button>
</div>

{#if doc.Subscriptions && doc.Subscriptions.length}
  <div class="cfg-list">
    {#each doc.Subscriptions as s, i (i)}
      <div class="cfg-item">
        <label>Имя<input bind:value={s.Name} on:input={touch} placeholder="demo-vless" /></label>
        <label class="grow">URL<input bind:value={s.URL} on:input={touch} placeholder="https://…/sub" /></label>
        <label title="Только если провайдер НЕ шлёт срок сам (заголовок Subscription-Userinfo). Его собственный ответ всегда важнее — это запасной источник, а не переопределение."
          >Истекает<input type="date" bind:value={s.Expires} on:input={touch} /></label>
        <label class="chk"><input type="checkbox" bind:checked={s.Enabled} on:change={touch} /> вкл</label>
        <button class="del" on:click={() => remove(i)} title="Удалить">✕</button>
      </div>
    {/each}
  </div>
{:else}
  <div class="muted">Пока нет подписок. Добавь хотя бы одну — с неё Лоцман берёт ноды.</div>
{/if}
