<script>
  // Structured editor for doc.Subscriptions. Mutates the shared doc in place and
  // calls touch() so the container recomputes "dirty" (Svelte won't see a nested
  // mutation otherwise).
  export let doc
  export let touch = () => {}
  export let backend = null

  // Refresh results, keyed by subscription name. A refresh answers with what the
  // subscription YIELDED, not just ok/failed: "why did the one I just added give
  // me nothing" is the question the button exists for, and its answer used to live
  // only in a log that rotates in six minutes.
  let result = {}
  let busy = ''

  function add() {
    ;(doc.Subscriptions ||= []).push({ Name: '', URL: '', Format: 'auto', Tags: ['normal'], Enabled: true, Expires: '' })
    touch()
  }
  function remove(i) {
    doc.Subscriptions.splice(i, 1)
    touch()
  }

  async function refresh(name) {
    if (!backend) {
      result = { ...result, [name || '*']: { err: 'Мок-режим: обновление недоступно' } }
      return
    }
    busy = name || '*'
    try {
      const rows = await backend.RefreshSubscriptions(name || '')
      const next = { ...result }
      for (const r of rows || []) next[r.name] = { nodes: r.nodes, err: r.err || '' }
      result = next
    } catch (e) {
      result = { ...result, [name || '*']: { err: String(e && e.message ? e.message : e) } }
    }
    busy = ''
  }
</script>

<div class="row-head">
  <h2>Подписки</h2>
  <div class="head-actions">
    <button class="fix" on:click={() => refresh('')} disabled={busy !== ''}>
      {busy === '*' ? 'Обновляю…' : 'Обновить все'}
    </button>
    <button class="fix" on:click={add}>+ Подписка</button>
  </div>
</div>

{#if result['*'] && result['*'].err}
  <div class="sub-err">{result['*'].err}</div>
{/if}

{#if doc.Subscriptions && doc.Subscriptions.length}
  <div class="cfg-list">
    {#each doc.Subscriptions as s, i (i)}
      <div class="cfg-item">
        <label>Имя<input bind:value={s.Name} on:input={touch} placeholder="demo-vless" /></label>
        <label class="grow">URL<input bind:value={s.URL} on:input={touch} placeholder="https://…/sub" /></label>
        <label title="Только если провайдер НЕ шлёт срок сам (заголовок Subscription-Userinfo). Его собственный ответ всегда важнее — это запасной источник, а не переопределение."
          >Истекает<input type="date" bind:value={s.Expires} on:input={touch} /></label>
        <label class="chk"><input type="checkbox" bind:checked={s.Enabled} on:change={touch} /> вкл</label>
        <button
          class="fix"
          on:click={() => refresh(s.Name)}
          disabled={busy !== '' || !s.Name}
          title="Забрать ноды у этой подписки прямо сейчас и применить. Туннель не перезапускается.">
          {busy === s.Name ? '…' : '⟳'}
        </button>
        <button class="del" on:click={() => remove(i)} title="Удалить">✕</button>
      </div>
      {#if result[s.Name]}
        <div class="sub-result" class:bad={result[s.Name].err}>
          {#if result[s.Name].err}
            {result[s.Name].err}
          {:else}
            нод получено: {result[s.Name].nodes}
          {/if}
        </div>
      {/if}
    {/each}
  </div>
{:else}
  <div class="muted">Пока нет подписок. Добавь хотя бы одну — с неё Лоцман берёт ноды.</div>
{/if}

<style>
  .head-actions {
    display: flex;
    gap: 0.5rem;
  }
  /* The answer belongs under the row it is about — a count in a corner would not
     say WHICH subscription returned it, which is the whole point. */
  .sub-result {
    margin: -0.25rem 0 0.5rem 0.5rem;
    font-size: 0.85em;
    opacity: 0.85;
  }
  .sub-result.bad,
  .sub-err {
    color: #e06c75;
    opacity: 1;
  }
  .sub-err {
    margin: 0.25rem 0 0.5rem 0;
    font-size: 0.85em;
  }
</style>
