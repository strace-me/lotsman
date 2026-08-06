<script>
  // Structured editor for doc.Hostlists — reusable packs of domains. A pack is
  // declared once here (its sources, excludes and shrink guard) and attached to any
  // number of services from the «Сервисы» section, instead of pasting domains inline.
  // Lotsman re-fetches each pack in the background and writes the merged result to its
  // file. Mutates the shared doc in place; touch() re-marks dirty.
  export let doc
  export let touch = () => {}

  function add() {
    ;(doc.Hostlists ||= []).push({ Name: '', Out: '', Sources: [], Exclude: [], Domains: [], MinKeepRatio: 0.8 })
    touch()
  }
  function remove(i) {
    // Drop the attachments too: a service left naming a deleted list makes the whole
    // config fail to save, and the checkbox for it is gone, so it could only be cleared
    // from the raw-YAML escape hatch.
    const name = doc.Hostlists[i].Name
    doc.Hostlists.splice(i, 1)
    for (const s of doc.Services || []) {
      if ((s.DomainLists || []).includes(name)) s.DomainLists = s.DomainLists.filter((n) => n !== name)
    }
    touch()
  }
  const linesOf = (arr) => (arr || []).join('\n')
  function setLines(hl, field, v) {
    hl[field] = v.split('\n').map((x) => x.trim()).filter(Boolean)
    touch()
  }
  const usedBy = (name) => (doc.Services || []).filter((s) => (s.DomainLists || []).includes(name)).map((s) => s.Name)
</script>

<div class="row-head">
  <h2>Списки доменов</h2>
  <button class="fix" on:click={add}>+ Список</button>
</div>
<div class="muted hl-hint">
  Переиспользуемая пачка доменов: впиши свои, подтяни из внешних источников или и то и другое. Объяви её здесь,
  а подключай в «Сервисы» — один список можно повесить на сколько угодно правил. Источники Лоцман обновляет сам и
  не затирает рабочий файл, если источник отдал огрызок; свои домены живут в конфиге и работают сразу, ещё до
  первой загрузки.
</div>

{#if doc.Hostlists && doc.Hostlists.length}
  <div class="cfg-list">
    {#each doc.Hostlists as hl, i (i)}
      <div class="cfg-item col">
        <div class="cfg-fields">
          <label>Имя<input bind:value={hl.Name} on:input={touch} placeholder="ru-blocked" /></label>
          <label>Мин. доля при сжатии<input type="number" min="0" max="1" step="0.05" bind:value={hl.MinKeepRatio} on:input={touch} /></label>
          <button class="del" on:click={() => remove(i)} title="Удалить">✕</button>
        </div>
        <label class="full">Файл<input bind:value={hl.Out} on:input={touch} placeholder="/var/lib/lotsman/ru-blocked.txt" /></label>
        <label class="full"
          >Свои домены (по одному в строке)
          <textarea class="hl-urls" rows="3" spellcheck="false" value={linesOf(hl.Domains)}
            on:input={(e) => setLines(hl, 'Domains', e.target.value)}
            placeholder="bank.example"></textarea>
        </label>
        <label class="full"
          >Источники (по одному URL в строке)
          <textarea class="hl-urls" rows="3" spellcheck="false" value={linesOf(hl.Sources)}
            on:input={(e) => setLines(hl, 'Sources', e.target.value)}
            placeholder="необязательно, если домены вписаны выше"></textarea>
        </label>
        <label class="full"
          >Исключения (по одному URL в строке)
          <textarea class="hl-urls" rows="2" spellcheck="false" value={linesOf(hl.Exclude)}
            on:input={(e) => setLines(hl, 'Exclude', e.target.value)}
            placeholder="необязательно"></textarea>
        </label>
        {#if hl.Name}
          <div class="muted hl-used">
            {#if usedBy(hl.Name).length}Подключён к: {usedBy(hl.Name).join(', ')}{:else}Пока ни к одному сервису не подключён.{/if}
          </div>
        {/if}
      </div>
    {/each}
  </div>
{:else}
  <div class="muted">Списков нет. Домены можно писать прямо в сервисе, но пачкой — удобнее и переиспользуемо.</div>
{/if}

<style>
  .hl-hint {
    margin: 0.15rem 0 0.6rem;
  }
  .hl-urls {
    width: 100%;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.82rem;
    resize: vertical;
  }
  .hl-used {
    font-size: 0.82rem;
  }
</style>
