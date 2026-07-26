<script>
  // Structured editor for doc.Services — the common fields (name, category, probe,
  // domains). Rare per-service knobs (chain, ips, sticky…) stay in the YAML escape
  // hatch for now. Mutates the shared doc in place; touch() re-marks dirty.
  export let doc
  export let touch = () => {}

  const categories = ['streaming', 'messaging', 'gaming', 'social', 'dev', 'web', 'general']

  function add() {
    ;(doc.Services ||= []).push({ Name: '', Category: 'streaming', ProbeTarget: '', Domains: [] })
    touch()
  }
  function remove(i) {
    doc.Services.splice(i, 1)
    touch()
  }
  const domainsStr = (s) => (s.Domains || []).join(', ')
  function setDomains(s, v) {
    s.Domains = v.split(',').map((x) => x.trim()).filter(Boolean)
    touch()
  }
</script>

<div class="row-head">
  <h2>Сервисы</h2>
  <button class="fix" on:click={add}>+ Сервис</button>
</div>

{#if doc.Services && doc.Services.length}
  <div class="cfg-list">
    {#each doc.Services as s, i (i)}
      <div class="cfg-item col">
        <div class="cfg-fields">
          <label>Имя<input bind:value={s.Name} on:input={touch} placeholder="youtube" /></label>
          <label>Категория<input bind:value={s.Category} on:input={touch} list="cfg-cats" placeholder="streaming" /></label>
          <button class="del" on:click={() => remove(i)} title="Удалить">✕</button>
        </div>
        <label class="full">Проба (URL)<input bind:value={s.ProbeTarget} on:input={touch} placeholder="https://…/generate_204" /></label>
        <label class="full">Домены<input value={domainsStr(s)} on:input={(e) => setDomains(s, e.target.value)} placeholder="youtube.com, googlevideo.com" /></label>
      </div>
    {/each}
  </div>
  <datalist id="cfg-cats">
    {#each categories as c}<option value={c}></option>{/each}
  </datalist>
{:else}
  <div class="muted">Пока нет сервисов. Добавь то, что нужно разблокировать.</div>
{/if}
