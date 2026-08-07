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

  // Rules are matched FIRST-MATCH-WINS, like a firewall, so the order they run in
  // is load-bearing. But it is NOT the order of the file: the generator sorts by
  // `priority` (lower first, negatives before everything) and only falls back to
  // file order for ties. Showing file order would therefore be a claim about
  // behaviour that does not hold — ru-direct sits at -10 precisely so it matches
  // before the broad catch-alls, and a list that hid that would be lying quietly.
  //
  // So the editor shows the EFFECTIVE order, with the priority visible and
  // editable, and says which is which.
  $: ordered = (doc.Services || [])
    .map((s, i) => ({ s, i }))
    .sort((a, b) => (a.s.Priority || 0) - (b.s.Priority || 0) || a.i - b.i)
  $: reordered = ordered.some((e, n) => e.i !== n)

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
  <h2>Правила</h2>
  <button class="fix" on:click={add}>+ Правило</button>
</div>
<div class="muted rules-hint">
  Порядок здесь — тот, в котором правила <b>реально</b> проверяются: побеждает первое
  совпавшее, как в файрволе. Решает поле «приоритет» (меньше — раньше, отрицательные
  впереди всех), и только при равных приоритетах — порядок в файле.
  {#if reordered}<b> Он отличается от порядка в файле</b> — значит приоритеты уже расставлены.{/if}
</div>

{#if doc.Services && doc.Services.length}
  <div class="cfg-list">
    {#each ordered as { s, i } (i)}
      <div class="cfg-item col">
        <div class="cfg-fields">
          <label>Имя<input bind:value={s.Name} on:input={touch} placeholder="youtube" /></label>
          <label>Категория<input bind:value={s.Category} on:input={touch} list="cfg-cats" placeholder="streaming" /></label>
          <label title="Меньше — проверяется раньше. Отрицательные впереди всех: так узкое правило (ru-direct) успевает совпасть до широкого catch-all."
            >Приоритет<input type="number" bind:value={s.Priority} on:input={touch} placeholder="0" /></label>
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
  .rules-hint {
    margin: 0.15rem 0 0.7rem;
    line-height: 1.5;
  }
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
