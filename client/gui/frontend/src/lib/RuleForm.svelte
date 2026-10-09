<script>
  // One rule's settings, which is a FILE of its own. Split out so the Правила tab and
  // anything else that needs it edit the SAME form rather than two that drift.
  //
  // Three things here are not a mechanical rename, and each has a reason:
  //
  //  - `sticky` is a POINTER upstream, so UNSET ("inherit the default") is different
  //    from false ("override a default of true"). A checkbox has two states and would
  //    turn every rule that says nothing into a rule that asks to flap by latency, so
  //    it is a three-way select.
  //  - `static` is gone. It nailed a rule to its first rung and nothing ever moved it;
  //    a ladder of one rung says the same thing and cannot drift from the mechanism.
  //  - `exclude_domains` is gone from the rule. The exclusion has to survive the moment
  //    the strategy is applied, and the rule is the only thing that applies it — so it
  //    belongs to the strategy bundle, which declares its own --hostlist-exclude.
  export let rule
  export let cfg
  export let rules = []
  export let touch = () => {}
  export let renameId = () => {}
  export let file = ''

  const TIERS = ['preferred', 'fallback', 'emergency']
  const PROFILES = ['general', 'voice', 'streaming', 'gaming']
  const PROBE_TYPES = ['http', 'tcp', 'stun']
  // The built-in pools plus whatever the document declares: a rule's rung resolves
  // against one of them, and offering a name the loader rejects only ever produced a
  // save that failed.
  const BUILTIN_POOLS = ['vpn_url_test', 'vpn_url_test_udp', 'emergency_pool']

  const csv = (a) => (a || []).join(', ')

  // Nested writers. A rule is a tree, so every field is a path into it — and each one
  // creates the intermediate object, because a rule loaded from a minimal file has
  // none of them and a form that assumes they exist writes to undefined.
  function set(path, value) {
    let node = rule
    for (let i = 0; i < path.length - 1; i++) {
      const key = path[i]
      if (!node[key] || typeof node[key] !== 'object') node[key] = {}
      node = node[key]
    }
    node[path[path.length - 1]] = value
    touch()
  }
  const get = (path) => path.reduce((n, k) => (n == null ? undefined : n[k]), rule)

  const setNum = (path, v) => set(path, v === '' ? 0 : Number(v))
  const setCSV = (path, v) => set(path, v.split(',').map((x) => x.trim()).filter(Boolean))

  $: poolNames = [...new Set([...BUILTIN_POOLS, ...Object.keys((cfg && cfg.Pools) || {})])].sort()
  $: instanceNames = Object.keys((cfg && cfg.Desync && cfg.Desync.Instances) || {}).sort()
  // Strategy ids: the ones declared in the document, plus whatever the rules already
  // pin. The full bundle catalogue is upstream and lives on disk, so a complete list
  // here would rot.
  $: knownStrategies = [
    ...new Set([
      ...((cfg && cfg.Strategies) || []).map((s) => s.ID),
      ...rules.map((r) => r.rule).filter(Boolean).flatMap((r) => (r.Chain || []).flatMap((s) => (s.Action && s.Action.Desync ? [s.Action.Desync.Strategy] : []))),
    ]),
  ].filter((x) => x && x !== 'auto').sort()

  // Lists: BOTH namespaces a rule's domain_lists can name — a `lists.domains` entry,
  // which becomes a sing-box rule-set, and a `hostlists` pack, whose domains are merged
  // inline for the desync engine. The document decides which a name means; this editor
  // just offers what it declared, and flags anything a rule names but nothing declares.
  $: declaredLists = [
    ...Object.keys(((cfg && cfg.Lists) && cfg.Lists.Domains) || {}),
    ...((cfg && cfg.Hostlists) || []).map((h) => h.Name),
  ].filter(Boolean)
  $: declaredIPLists = Object.keys((((cfg && cfg.Lists) || {}).IPs) || {}).filter(Boolean)
  $: usedLists = [...new Set([...declaredLists, ...((get(['Match', 'DomainLists'])) || [])])]
  $: usedIPLists = [...new Set([...declaredIPLists, ...((get(['Match', 'IPLists'])) || [])])]
  const listStale = (name) => !declaredLists.includes(name)
  const ipListStale = (name) => !declaredIPLists.includes(name)
  const toggleIn = (path, name, on) => {
    const cur = new Set(get(path) || [])
    on ? cur.add(name) : cur.delete(name)
    set(path, [...cur])
  }

  // --- the ladder ---
  // A step is a TIER label plus exactly one ACTION, and the loader refuses anything
  // else: two actions is not "both if possible", it is a config error. So the form
  // picks the kind and then only shows that kind's fields.
  const actionOf = (step) => {
    const a = step.Action || {}
    if (a.Direct) return 'direct'
    if (a.Desync) return 'desync'
    if (a.VPN) return 'vpn'
    return 'vpn'
  }
  const setKind = (step, kind) => {
    step.Action =
      kind === 'direct'
        ? { Direct: true }
        : kind === 'desync'
          ? { Desync: { Instance: instanceNames[0] || '', Strategy: 'auto' } }
          : { VPN: { Pool: 'vpn_url_test' } }
    touch()
  }
  function addStep() {
    ;(rule.Chain ||= []).push({ Tier: 'fallback', Action: { VPN: { Pool: 'vpn_url_test' } } })
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
  // Tiers may repeat but not go backwards. Moving a step down past a stricter tier
  // would create exactly that, so the control is disabled rather than letting the save
  // fail — a refusal at the point of the action is cheaper to understand than one that
  // arrives as an error about a file the operator did not think they were touching.
  function canMoveDown(n) {
    const ranks = TIERS
    const rank = (s) => (s.Tier ? ranks.indexOf(s.Tier) : -1)
    if (n + 1 >= (rule.Chain || []).length) return false
    return rank(rule.Chain[n + 1]) <= rank(rule.Chain[n])
  }
</script>

<div class="cfg-fields">
  <label
    title="Идентификатор правила. Это его имя в рантайме: /status, логи и канарейка говорят им. Имя файла обязано совпадать с ним."
    >ID<input value={rule.ID || ''} on:change={(e) => renameId(file, e.target.value)} placeholder="youtube" /></label
  >
  <label>Отображаемое имя<input bind:value={rule.Name} on:input={touch} placeholder="YouTube" /></label>
  <label title="Меньше — проверяется раньше. Отрицательные впереди всех: так узкое правило успевает совпасть до широкого catch-all. Это НАШ порядок в списке маршрутов, в конфиг sing-box он не попадает."
    >Приоритет<input type="number" value={rule.Priority || ''} on:input={(e) => setNum(['Priority'], e.target.value)} placeholder="0" /></label
  >
</div>

<div class="cfg-fields">
  <label class="chk"><input type="checkbox" bind:checked={rule.Enabled} on:change={touch} /> включено</label>
  <label
    class="chk"
    title="«Эти домены — не дело Лоцмана». Правило выпадает из рантайма целиком, в отличие от выключенного: это заявление, а не переключатель."
    ><input type="checkbox" bind:checked={rule.Bypass} on:change={touch} /> в обход</label
  >
</div>

<label class="full">Проба (URL)<input value={get(['Probe', 'Target']) || ''} on:input={(e) => set(['Probe', 'Target'], e.target.value)} placeholder="https://…/generate_204" /></label>
<div class="cfg-fields">
  <label
    title="Тип пробы. quic — только на direct/desync-ступенях: на VPN-рунге он измеряет туннель, а не прямой путь, и даёт ложный отказ."
    >Тип пробы
    <select value={(get(['Probe', 'Type']) || 'http')} on:change={(e) => set(['Probe', 'Type'], e.target.value)}>
      {#each PROBE_TYPES as t}<option value={t}>{t}</option>{/each}
    </select>
  </label>
</div>
<label class="full">Домены<input value={csv(get(['Match', 'Domains']))} on:input={(e) => setCSV(['Match', 'Domains'], e.target.value)} placeholder="youtube.com, googlevideo.com" /></label>

{#if usedLists.length}
  <div class="svc-lists">
    <span class="muted">Списки доменов:</span>
    {#each usedLists as name}
      <label class="svc-chip" class:stale={listStale(name)} title={listStale(name) ? 'Такого списка нет ни в lists.domains, ни в hostlists — сними галочку, иначе правило не сохранится' : ''}>
        <input type="checkbox" checked={((get(['Match', 'DomainLists'])) || []).includes(name)} on:change={(e) => toggleIn(['Match', 'DomainLists'], name, e.target.checked)} />
        {name}{#if listStale(name)} ⚠{/if}
      </label>
    {/each}
  </div>
{/if}
{#if usedIPLists.length}
  <div class="svc-lists">
    <span class="muted">Списки IP:</span>
    {#each usedIPLists as name}
      <label class="svc-chip" class:stale={ipListStale(name)} title={ipListStale(name) ? 'Такого списка нет в lists.ips — сними галочку' : ''}>
        <input type="checkbox" checked={((get(['Match', 'IPLists'])) || []).includes(name)} on:change={(e) => toggleIn(['Match', 'IPLists'], name, e.target.checked)} />
        {name}{#if ipListStale(name)} ⚠{/if}
      </label>
    {/each}
  </div>
{/if}

<div class="muted chain-hint">
  Цепочка — лестница, по которой правило идёт, когда ступень перестаёт работать: сверху предпочтительная, ниже
  запасные. Уходит вниз быстро, возвращается медленно. Ярлык <code>tier</code> может повторяться (две ступени
  «десинк, но по-другому»), но не идти назад — <code>emergency</code> после <code>preferred</code> читается как
  «вернуться на лучший путь после последнего рубежа», а это не то, что делает лестница.
</div>
{#each rule.Chain || [] as step, n (n)}
  <div class="chain-step">
    <span class="chain-n">{n + 1}</span>
    <label
      title="Ярлык группы. Не влияет на порядок — порядок это позиция в списке."
      >tier
      <select bind:value={step.Tier} on:change={touch}>
        <option value="">— без ярлыка —</option>
        {#each TIERS as t}<option value={t}>{t}</option>{/each}
      </select>
    </label>
    <label
      title="Что ступень ДЕЛАЕТ. Ровно одно действие на ступень: шаг — это путь, а не набор пожеланий."
      >Действие
      <select value={actionOf(step)} on:change={(e) => setKind(step, e.target.value)}>
        <option value="desync">десинк (прямой выход)</option>
        <option value="vpn">vpn (пул)</option>
        <option value="direct">direct (как есть)</option>
      </select>
    </label>
    {#if actionOf(step) === 'desync'}
      <label
        >Инстанс
        <select bind:value={step.Action.Desync.Instance} on:change={touch}>
          {#each instanceNames as n2}<option value={n2}>{n2}</option>{/each}
        </select>
      </label>
      <label
        class="grow"
        title="Идентификатор рецепта из бандла или из объявленных стратегий. «auto» — выбирает база знаний."
        >Рецепт<input bind:value={step.Action.Desync.Strategy} on:input={touch} list="rf-strats" placeholder="auto" />
      </label>
    {:else if actionOf(step) === 'vpn'}
      <label>Пул<input bind:value={step.Action.VPN.Pool} on:input={touch} list="rf-pools" placeholder="vpn_url_test" /></label>
      <label
        class="grow"
        title="Обернуть выход в ДРУГОЙ пул: трафик идёт через ноду A, а сама A выходит через ноду B. Это композиция, а не запасной вариант, и она вкладывается произвольно глубоко — но без циклов."
        >Через пул<input
          value={step.Action.VPN.Detour || ''}
          on:input={(e) => set(['Chain', n, 'Action', 'VPN', 'Detour'], e.target.value)}
          list="rf-pools"
          placeholder="пусто = напрямую"
        />
      </label>
    {/if}
    <button class="ghost" on:click={() => moveStep(n, -1)} title="Выше">↑</button>
    <button
      class="ghost"
      on:click={() => moveStep(n, 1)}
      title={canMoveDown(n) ? 'Ниже' : 'Ниже нельзя: ступень ушла бы назад по tier'}
      disabled={!canMoveDown(n)}>↓</button
    >
    <button class="del" on:click={() => delStep(n)} title="Удалить ступень">✕</button>
  </div>
{/each}
<button class="fix step-add" on:click={addStep}>+ Ступень</button>

<div class="cfg-fields">
  <!-- 0 means "use the global default", and a number input showing a literal 0 says the
       opposite — that someone chose zero failures. Render unset as EMPTY so the
       placeholder (the actual default) is what you read. -->
  <label title="Сколько неудачных проб подряд до перехода на следующую ступень. Пусто = общий порог (3)."
    >Отказов до перехода<input type="number" min="0" value={get(['Behavior', 'EscalateAfter']) || ''} on:input={(e) => setNum(['Behavior', 'EscalateAfter'], e.target.value)} placeholder="3" /></label
  >
  <label title="Сколько успешных тихих проб подряд до возврата на ступень выше. Пусто = общий порог (5)."
    >Успехов до возврата<input type="number" min="0" value={get(['Behavior', 'RecoverAfter']) || ''} on:input={(e) => setNum(['Behavior', 'RecoverAfter'], e.target.value)} placeholder="5" /></label
  >
  <label title="Класс трафика. Влияет на то, как выбирается нода: для голоса важнее джиттер и потери, для видео — полоса."
    >Профиль
    <select value={get(['Behavior', 'Profile']) || ''} on:change={(e) => set(['Behavior', 'Profile'], e.target.value)}>
      <option value="">— по умолчанию —</option>
      {#each PROFILES as p}<option value={p}>{p}</option>{/each}
    </select>
  </label>
</div>
<div class="cfg-fields">
  <label
    class="chk"
    title="Держаться одной ноды и менять её только при настоящем отказе, а не при скачке задержки."
    >липкое
    <select
      value={get(['Behavior', 'Sticky']) === undefined || get(['Behavior', 'Sticky']) === null ? 'unset' : String(get(['Behavior', 'Sticky']))}
      on:change={(e) => set(['Behavior', 'Sticky'], e.target.value === 'unset' ? undefined : e.target.value === 'true')}
    >
      <option value="unset">липкое: по умолчанию</option>
      <option value="true">липкое: да</option>
      <option value="false">липкое: нет</option>
    </select>
  </label>
  <label class="chk" title="Резать TLS ClientHello средствами sing-box, чтобы DPI не прочитал SNI одним пакетом."
    ><input type="checkbox" checked={!!get(['Behavior', 'TLSFragment'])} on:change={(e) => set(['Behavior', 'TLSFragment'], e.target.checked)} /> tls_fragment</label
  >
</div>
<label
  class="full"
  title="Цели с реальным объёмом для канарейки. Проба намеренно мелкая и пропускную способность не измеряет — у youtube это 204 без тела, у discord 35 байт. Ставь только там, где объём и есть смысл сервиса."
  >Цели для замера объёма<input
    value={csv(get(['Volume', 'Targets']))}
    on:input={(e) => setCSV(['Volume', 'Targets'], e.target.value)}
    placeholder="https://…/большой-файл"
  /></label
>
<label
  class="full"
  title="Голос и всё, что без SNI, ловится только по адресу: у сырого UDP нет имени, которое можно сопоставить."
  >IP / подсети<input value={csv(get(['Match', 'CIDRs']))} on:input={(e) => setCSV(['Match', 'CIDRs'], e.target.value)} placeholder="66.22.192.0/18" /></label
>

<datalist id="rf-profiles">{#each PROFILES as x}<option value={x}></option>{/each}</datalist>
<datalist id="rf-pools">{#each poolNames as x}<option value={x}></option>{/each}</datalist>
<datalist id="rf-strats">{#each knownStrategies as x}<option value={x}></option>{/each}</datalist>

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
    flex-wrap: wrap;
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
