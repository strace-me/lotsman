<script>
  // One rule's settings. Split out of the config section so the Правила tab and
  // anything else that needs it edit the SAME form rather than two that drift.
  export let rule
  export let doc
  export let touch = () => {}

  // The four categories the validator ships (registry.BuiltinCategories). A config
  // may define its own, which the free-text input still accepts. Offering ones the
  // validator rejects only ever produced a save that fails.
  const CATEGORIES = ['streaming', 'messaging', 'gaming', 'generic']
  const STATES = ['PREFERRED', 'ALT_ZAPRET', 'VPN', 'EMERGENCY', 'LOCKED']
  const CLASSES = ['zapret', 'vpn', 'direct', 'emergency']
  const PROFILES = ['', 'general', 'voice', 'streaming', 'gaming']

  const csv = (a) => (a || []).join(', ')
  // Empty means "unset", which the config spells as 0 (= use the global default).
  function setNum(field, v) {
    rule[field] = v === '' ? 0 : Number(v)
    touch()
  }
  function setCSV(field, v) {
    rule[field] = v.split(',').map((x) => x.trim()).filter(Boolean)
    touch()
  }

  // Packs declared in «Списки»; attaching one merges its domains into this rule's,
  // so a list is maintained once and reused.
  $: declared = (doc.Hostlists || []).map((h) => h.Name).filter(Boolean)
  // A rule can still name a list that was renamed or deleted. Render those too —
  // otherwise the attachment is invisible here yet fails validation on save, and
  // the only way out is the raw-YAML editor.
  $: lists = [...new Set([...declared, ...(rule.DomainLists || [])])]
  const isStale = (name) => !declared.includes(name)
  const attached = (name) => (rule.DomainLists || []).includes(name)
  function toggleList(name, on) {
    const cur = new Set(rule.DomainLists || [])
    on ? cur.add(name) : cur.delete(name)
    rule.DomainLists = [...cur]
    touch()
  }

  $: poolNames = Object.keys(doc.Pools || {}).sort()
  // Offer what this config already uses: the full geosite/geoip catalogue is
  // thousands of names and lives upstream, so a complete list here would rot.
  $: knownSets = [...new Set((doc.Services || []).flatMap((s) => s.RuleSets || []))].sort()

  function addStep() {
    ;(rule.Chain ||= []).push({ State: 'VPN', Class: 'vpn', StrategyID: '' })
    touch()
  }
  function delStep(n) {
    rule.Chain.splice(n, 1)
    touch()
  }
  function moveStep(n, by) {
    const t = n + by
    if (t < 0 || t >= rule.Chain.length) return
    const [x] = rule.Chain.splice(n, 1)
    rule.Chain.splice(t, 0, x)
    touch()
  }
</script>

<div class="cfg-fields">
  <label>Имя<input bind:value={rule.Name} on:input={touch} placeholder="youtube" /></label>
  <label>Категория<input bind:value={rule.Category} on:input={touch} list="rf-cats" placeholder="streaming" /></label>
  <label title="Меньше — проверяется раньше. Отрицательные впереди всех: так узкое правило успевает совпасть до широкого catch-all. Это НАШ порядок в списке маршрутов, в конфиг sing-box он не попадает."
    >Приоритет<input type="number" value={rule.Priority || ''} on:input={(e) => setNum('Priority', e.target.value)} placeholder="0" /></label>
</div>

<label class="full">Проба (URL)<input bind:value={rule.ProbeTarget} on:input={touch} placeholder="https://…/generate_204" /></label>
<label class="full">Домены<input value={csv(rule.Domains)} on:input={(e) => setCSV('Domains', e.target.value)} placeholder="youtube.com, googlevideo.com" /></label>
<!-- Rule-sets were not editable here at all, and `scope` did not count them, so a
     rule defined entirely by them looked EMPTY — which is how the owner came to
     ask what web-blocked was and where it had come from. Those two (ru-direct and
     web-blocked) are exactly the rules that match the most traffic. -->
<label class="full" title="Готовые наборы адресов, которые sing-box скачивает сам (geosite-*/geoip-*). Их содержимое здесь не показать — оно приходит с обновлением. Правило может состоять из одних наборов: так устроены ru-direct и web-blocked."
  >Наборы правил<input value={csv(rule.RuleSets)} on:input={(e) => setCSV('RuleSets', e.target.value)} list="rf-sets" placeholder="geosite-ru-blocked, geoip-telegram" /></label>

{#if lists.length}
  <div class="svc-lists">
    <span class="muted">Списки:</span>
    {#each lists as name}
      <label class="svc-chip" class:stale={isStale(name)} title={isStale(name) ? 'Такого списка нет — сними галочку, иначе конфигурация не сохранится' : ''}>
        <input type="checkbox" checked={attached(name)} on:change={(e) => toggleList(name, e.target.checked)} />
        {name}{#if isStale(name)} ⚠{/if}
      </label>
    {/each}
  </div>
{/if}

<div class="muted chain-hint">
  Цепочка — лестница, по которой правило идёт, когда ступень перестаёт работать:
  сверху предпочтительная, ниже запасные. Уходит вниз быстро (по трём отказам),
  возвращается медленно (по пяти успехам). Ступень, для класса которой на этой
  машине нет исполнителя, молча выбрасывается при старте.
</div>
{#each rule.Chain || [] as step, n}
  <div class="chain-step">
    <span class="chain-n">{n + 1}</span>
    <label>Состояние<input bind:value={step.State} on:input={touch} list="rf-states" /></label>
    <label>Класс<input bind:value={step.Class} on:input={touch} list="rf-classes" /></label>
    <label class="grow" title="Для VPN — имя пула. Для запрета — идентификатор рецепта; пусто = выбирает база знаний."
      >Стратегия / пул<input bind:value={step.StrategyID} on:input={touch} list="rf-pools" placeholder="пусто = автоподбор" /></label>
    <button class="ghost" on:click={() => moveStep(n, -1)} title="Выше">↑</button>
    <button class="ghost" on:click={() => moveStep(n, 1)} title="Ниже">↓</button>
    <button class="del" on:click={() => delStep(n)} title="Удалить ступень">✕</button>
  </div>
{/each}
<button class="fix step-add" on:click={addStep}>+ Ступень</button>

<div class="cfg-fields">
  <!-- 0 means "use the global default", and a number input showing a literal 0
       says the opposite — that someone chose zero failures. Render unset as EMPTY
       so the placeholder (the actual default) is what you read. -->
  <label title="Сколько неудачных проб подряд до перехода на следующую ступень. Пусто = общий порог (3)."
    >Отказов до перехода<input type="number" min="0" value={rule.EscalateAfter || ''} on:input={(e) => setNum('EscalateAfter', e.target.value)} placeholder="3" /></label>
  <label title="Сколько успешных тихих проб подряд до возврата на ступень выше. Пусто = общий порог (5)."
    >Успехов до возврата<input type="number" min="0" value={rule.RecoverAfter || ''} on:input={(e) => setNum('RecoverAfter', e.target.value)} placeholder="5" /></label>
  <label>Профиль<input bind:value={rule.Profile} on:input={touch} list="rf-profiles" placeholder="general" /></label>
</div>
<div class="cfg-fields">
  <label class="chk" title="Держаться одной ноды и менять её только при настоящем отказе, а не при скачке задержки."
    ><input type="checkbox" bind:checked={rule.Sticky} on:change={touch} /> липкое</label>
  <label class="chk" title="Прибито: Лоцман никогда не двигает это правило по цепочке сам."
    ><input type="checkbox" bind:checked={rule.Static} on:change={touch} /> прибито</label>
  <label class="chk" title="Резать TLS ClientHello средствами sing-box, чтобы DPI не прочитал SNI одним пакетом."
    ><input type="checkbox" bind:checked={rule.TLSFragment} on:change={touch} /> tls_fragment</label>
</div>
<label class="full" title="Цель с реальным объёмом для канарейки. Проба намеренно мелкая и пропускную способность не измеряет — у youtube это 204 без тела, у discord 35 байт. Ставь только там, где объём и есть смысл сервиса."
  >Цель для замера объёма<input bind:value={rule.VolumeTarget} on:input={touch} placeholder="https://…/большой-файл" /></label>
<label class="full" title="Голос и всё, что без SNI, ловится только по адресу: у сырого UDP нет имени, которое можно сопоставить."
  >IP / подсети<input value={csv(rule.IPs)} on:input={(e) => setCSV('IPs', e.target.value)} placeholder="66.22.192.0/18" /></label>
<label class="full" title="Эти домены проходят через десинк СЫРЫМИ — для CDN, которые без него работают, а с ним ломаются."
  >Исключить из десинка<input value={csv(rule.ExcludeDomains)} on:input={(e) => setCSV('ExcludeDomains', e.target.value)} placeholder="download.epicgames.com" /></label>

<datalist id="rf-cats">{#each CATEGORIES as x}<option value={x}></option>{/each}</datalist>
<datalist id="rf-states">{#each STATES as x}<option value={x}></option>{/each}</datalist>
<datalist id="rf-classes">{#each CLASSES as x}<option value={x}></option>{/each}</datalist>
<datalist id="rf-profiles">{#each PROFILES as x}<option value={x}></option>{/each}</datalist>
<datalist id="rf-pools">{#each poolNames as x}<option value={x}></option>{/each}</datalist>
<datalist id="rf-sets">{#each knownSets as x}<option value={x}></option>{/each}</datalist>

<style>
  /* These lived in the old ServicesEdit and went with it. Without them the
     chips inherit the form's column layout and stack label-over-checkbox. */
  .svc-lists {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.5rem;
    margin-top: 0.35rem;
    font-size: 0.85rem;
  }
  .svc-lists :global(.svc-chip),
  .svc-chip {
    display: flex;
    flex-direction: row;
    align-items: center;
    gap: 0.3rem;
    text-transform: none;
    letter-spacing: 0;
    font-size: 0.85rem;
    color: var(--ink-dim);
  }
  .svc-chip.stale {
    opacity: 0.75;
    text-decoration: line-through;
  }
  .chain-hint {
    line-height: 1.5;
  }
  .chain-step {
    display: flex;
    align-items: flex-end;
    gap: 8px;
  }
  .chain-n {
    color: var(--ink-faint);
    font-variant-numeric: tabular-nums;
    min-width: 1.2em;
    padding-bottom: 6px;
  }
  .step-add {
    align-self: flex-start;
  }
</style>
