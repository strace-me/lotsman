<script>
  // Structured editor for doc.Pools — the named groups a rule's VPN rung selects
  // through. Until now the only way to touch a pool was the raw-YAML hatch, which
  // meant the one place that decides WHICH exits a rule may use was the least
  // visible thing in the app.
  //
  // Pools are a map in the config, not a list, so the editor works over the key.
  // Renaming is therefore delete+insert, which also has to follow the rules that
  // point at the old name — an unreferenced pool silently stops being used, and a
  // rule naming a pool that no longer exists fails validation on save.
  export let doc
  export let touch = () => {}

  const TYPES = ['url_test', 'selector']

  $: pools = Object.entries(doc.Pools || {}).sort(([a], [b]) => a.localeCompare(b))

  // Which rules reference a pool, so deleting or renaming one is not a guess. A
  // rung names it in strategy_id.
  const usedBy = (name) =>
    (doc.Services || [])
      .filter((s) => (s.Chain || []).some((step) => step.StrategyID === name))
      .map((s) => s.Name)

  function add() {
    doc.Pools ||= {}
    let name = 'pool'
    for (let i = 2; doc.Pools[name]; i++) name = 'pool-' + i
    doc.Pools[name] = { Type: 'url_test', Filter: { Caps: [], TagsInclude: [], TagsExclude: [], CountriesInclude: [], CountriesExclude: [] } }
    doc.Pools = doc.Pools
    touch()
  }

  function remove(name) {
    const used = usedBy(name)
    if (used.length && !confirm(`Пул «${name}» используют правила: ${used.join(', ')}.\nПосле удаления конфигурация не сохранится, пока они на него ссылаются. Всё равно удалить?`)) return
    delete doc.Pools[name]
    doc.Pools = doc.Pools
    touch()
  }

  function rename(oldName, newName) {
    newName = (newName || '').trim()
    if (!newName || newName === oldName) return
    if (doc.Pools[newName]) return // taken; leave the field for the operator to fix
    doc.Pools[newName] = doc.Pools[oldName]
    delete doc.Pools[oldName]
    // Follow the references, or every rule pointing here breaks on save.
    for (const s of doc.Services || []) {
      for (const step of s.Chain || []) {
        if (step.StrategyID === oldName) step.StrategyID = newName
      }
    }
    doc.Pools = doc.Pools
    touch()
  }

  const listStr = (a) => (a || []).join(', ')
  function setList(p, field, v) {
    p.Filter ||= {}
    p.Filter[field] = v.split(',').map((x) => x.trim()).filter(Boolean)
    touch()
  }
</script>

<div class="row-head">
  <h2>Пулы</h2>
  <button class="fix" on:click={add}>+ Пул</button>
</div>
<div class="muted pool-hint">
  Пул — именованная группа нод, из которой правило выбирает выход на своей VPN-ступени.
  Фильтр решает, кто в неё попадёт: <code>caps</code> — что нода умеет (<code>udp_native</code>
  нужен голосу), теги и страны — откуда её брать. «Прогрев» держит пул постоянно
  опрошенным, чтобы подмена отвалившейся ноды происходила сразу.
</div>

{#if pools.length}
  <div class="cfg-list">
    {#each pools as [name, p] (name)}
      <div class="cfg-item col">
        <div class="cfg-fields">
          <label>Имя<input value={name} on:change={(e) => rename(name, e.target.value)} /></label>
          <label>Тип<input bind:value={p.Type} on:input={touch} list="cfg-pool-types" placeholder="url_test" /></label>
          <label class="chk"><input type="checkbox" bind:checked={p.Warmup} on:change={touch} /> прогрев</label>
          <button class="del" on:click={() => remove(name)} title="Удалить">✕</button>
        </div>
        <div class="cfg-fields">
          <label>Период опроса<input bind:value={p.Interval} on:input={touch} placeholder="1m" /></label>
          <label>Простой до сна<input bind:value={p.IdleTimeout} on:input={touch} placeholder="30m" /></label>
        </div>
        <label class="full"
          >Возможности (caps)<input value={listStr(p.Filter && p.Filter.Caps)}
            on:input={(e) => setList(p, 'Caps', e.target.value)} placeholder="tcp, udp_native" /></label>
        <div class="cfg-fields">
          <label>Теги — только эти<input value={listStr(p.Filter && p.Filter.TagsInclude)}
            on:input={(e) => setList(p, 'TagsInclude', e.target.value)} placeholder="normal" /></label>
          <label>Теги — исключить<input value={listStr(p.Filter && p.Filter.TagsExclude)}
            on:input={(e) => setList(p, 'TagsExclude', e.target.value)} placeholder="emergency" /></label>
        </div>
        <div class="cfg-fields">
          <label>Страны — только эти<input value={listStr(p.Filter && p.Filter.CountriesInclude)}
            on:input={(e) => setList(p, 'CountriesInclude', e.target.value)} placeholder="nl, de" /></label>
          <label title="Российский выход сидит за тем же ТСПУ, что и вы, — то есть выходом не является."
            >Страны — исключить<input value={listStr(p.Filter && p.Filter.CountriesExclude)}
            on:input={(e) => setList(p, 'CountriesExclude', e.target.value)} placeholder="ru" /></label>
        </div>
        <div class="muted pool-used">
          {#if usedBy(name).length}Используют правила: {usedBy(name).join(', ')}{:else}Ни одно правило на него не ссылается — значит он ни на что не влияет.{/if}
        </div>
      </div>
    {/each}
  </div>
  <datalist id="cfg-pool-types">{#each TYPES as t}<option value={t}></option>{/each}</datalist>
{:else}
  <div class="muted">
    Пулов нет. Лоцман подставит стандартные сам, но объявленный здесь пул — единственный
    способ решить, из каких нод правило выбирает выход.
  </div>
{/if}

<style>
  .pool-hint {
    margin: 0.15rem 0 0.7rem;
    line-height: 1.5;
  }
  .pool-used {
    font-size: 0.82rem;
  }
</style>
