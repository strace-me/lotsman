<script>
  // Structured editor for cfg.Hostlists — reusable packs of domains. A pack is
  // declared once here (its sources, excludes and shrink guard) and attached to any
  // number of services from the «Сервисы» section, instead of pasting domains inline.
  // Lotsman re-fetches each pack in the background and writes the merged result to its
  // file. Mutates the shared doc in place; touch() re-marks dirty.
  export let cfg
  // Rules live in their own files; they are passed in only so this editor can follow
  // the references. A pack renamed or removed here has to move with it, or the rule
  // that named it stops loading.
  export let rules = []
  export let touch = () => {}

  function add() {
    ;(cfg.Hostlists ||= []).push({ Name: '', Out: '', Sources: [], Exclude: [], Domains: [], MinKeepRatio: 0.8 })
    touch()
  }
  function remove(i) {
    // Drop the attachments too: a service left naming a deleted list makes the whole
    // config fail to save, and the checkbox for it is gone, so it could only be cleared
    // from the raw-YAML escape hatch.
    const name = cfg.Hostlists[i].Name
    cfg.Hostlists.splice(i, 1)
    for (const r of rules.map((x) => x.rule).filter(Boolean)) {
      const m = r.Match
      if (m && (m.DomainLists || []).includes(name)) m.DomainLists = m.DomainLists.filter((n) => n !== name)
    }
    touch()
  }
  const linesOf = (arr) => (arr || []).join('\n')
  function setLines(hl, field, v) {
    hl[field] = v.split('\n').map((x) => x.trim()).filter(Boolean)
    touch()
  }
  // A rule names a hostlist from match.domain_lists — the same field that names a
  // `lists.domains` entry, because the document decides which one a name means. So
  // "used by" here is every rule referencing the name, list or pack; a rule can only
  // use one of them, never both.
  const usedBy = (name) =>
    rules
      .map((r) => r.rule)
      .filter(Boolean)
      .filter((r) => r.Match && (r.Match.DomainLists || []).includes(name))
      .map((r) => r.Name || r.ID)
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

{#if cfg.Hostlists && cfg.Hostlists.length}
  <div class="cfg-list">
    {#each cfg.Hostlists as hl, i (i)}
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
