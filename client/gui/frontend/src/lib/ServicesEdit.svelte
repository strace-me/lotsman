<script>
  // Structured editor for doc.Services — the common fields (name, category, probe,
  // domains). Rare per-service knobs (chain, ips, sticky…) stay in the YAML escape
  // hatch for now. Mutates the shared doc in place; touch() re-marks dirty.
  export let doc
  export let touch = () => {}

  // The four categories the config validator ships (registry.BuiltinCategories); a
  // config may also define its own, which the free-text input still accepts. Offering
  // categories the validator rejects (social/dev/web, or the "general" typo) only led
  // to a save that fails validation.
  const categories = ['streaming', 'messaging', 'gaming', 'generic']

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

  // Packs declared in «Списки доменов»; attaching one merges its domains into this
  // service's, so a list is maintained in one place and reused across rules.
  $: declared = (doc.Hostlists || []).map((h) => h.Name).filter(Boolean)
  // A service can still name a list that was renamed or deleted. Render those too —
  // otherwise the attachment is invisible here yet fails validation on save, and the
  // only way out is the raw-YAML editor.
  const listsFor = (s) => [...new Set([...declared, ...(s.DomainLists || [])])]
  const isStale = (name) => !declared.includes(name)
  const attached = (s, name) => (s.DomainLists || []).includes(name)
  function toggleList(s, name, on) {
    const cur = new Set(s.DomainLists || [])
    on ? cur.add(name) : cur.delete(name)
    s.DomainLists = [...cur]
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
        {#if listsFor(s).length}
          <div class="svc-lists">
            <span class="muted">Списки:</span>
            {#each listsFor(s) as name}
              <label class="svc-chip" class:stale={isStale(name)} title={isStale(name) ? 'Такого списка нет — сними галочку, иначе конфиг не сохранится' : ''}>
                <input type="checkbox" checked={attached(s, name)} on:change={(e) => toggleList(s, name, e.target.checked)} />
                {name}{#if isStale(name)} ⚠{/if}
              </label>
            {/each}
          </div>
        {/if}
      </div>
    {/each}
  </div>
  <datalist id="cfg-cats">
    {#each categories as c}<option value={c}></option>{/each}
  </datalist>
{:else}
  <div class="muted">Пока нет сервисов. Добавь то, что нужно разблокировать.</div>
{/if}

<style>
  .svc-lists {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.5rem;
    margin-top: 0.35rem;
    font-size: 0.85rem;
  }
  .svc-chip {
    display: flex;
    flex-direction: row;
    align-items: center;
    gap: 0.3rem;
  }
  .svc-chip.stale {
    opacity: 0.75;
    text-decoration: line-through;
  }
</style>
