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

  // Everything below was reachable only through the raw-YAML hatch. The chain
  // especially: it is the ladder the rule walks when a step stops working, and it
  // decides what happens when things go wrong — the least suitable thing to keep
  // behind an escape hatch.
  const csv = (a) => (a || []).join(', ')
  function setCSV(s, field, v) {
    s[field] = v.split(',').map((x) => x.trim()).filter(Boolean)
    touch()
  }

  let open = {}
  const toggleOpen = (i) => ((open[i] = !open[i]), (open = open))

  // Chain steps. State is the label the UI and logs use; class is what actually
  // selects an executor. Keeping both editable is deliberate — they are not
  // derivable from each other, and a step whose class has no executor on this host
  // is silently dropped at startup.
  const STATES = ['PREFERRED', 'ALT_ZAPRET', 'VPN', 'EMERGENCY', 'LOCKED']
  const CLASSES = ['zapret', 'vpn', 'direct', 'emergency']
  const PROFILES = ['', 'general', 'voice', 'streaming', 'gaming']

  function addStep(s) {
    ;(s.Chain ||= []).push({ State: 'VPN', Class: 'vpn', StrategyID: '' })
    touch()
  }
  function delStep(s, n) {
    s.Chain.splice(n, 1)
    touch()
  }
  function moveStep(s, n, by) {
    const t = n + by
    if (t < 0 || t >= s.Chain.length) return
    const [x] = s.Chain.splice(n, 1)
    s.Chain.splice(t, 0, x)
    touch()
  }
  // Pools a rung can name, so a VPN step is a choice rather than a typed string.
  $: poolNames = Object.keys(doc.Pools || {}).sort()
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

        <button class="ghost more" on:click={() => toggleOpen(i)}>
          {open[i] ? '− свернуть' : '+ цепочка и поведение'}
          {#if !open[i] && (s.Chain || []).length}<span class="muted"> · {s.Chain.length} ступ.</span>{/if}
        </button>

        {#if open[i]}
          <div class="svc-adv">
            <div class="muted chain-hint">
              Цепочка — лестница, по которой правило идёт, когда ступень перестаёт
              работать: сверху предпочтительная, ниже запасные. Уходит вниз быстро
              (по трём отказам), возвращается медленно (по пяти успехам).
              Ступень, для класса которой на этой машине нет исполнителя, молча
              выбрасывается при старте.
            </div>
            {#each s.Chain || [] as step, n}
              <div class="chain-step">
                <span class="chain-n">{n + 1}</span>
                <label>Состояние<input bind:value={step.State} on:input={touch} list="cfg-states" /></label>
                <label>Класс<input bind:value={step.Class} on:input={touch} list="cfg-classes" /></label>
                <label class="grow" title="Для VPN — имя пула. Для запрета — идентификатор рецепта; пусто = выбирает база знаний."
                  >Стратегия / пул<input bind:value={step.StrategyID} on:input={touch} list="cfg-pools" placeholder="пусто = автоподбор" /></label>
                <button class="ghost" on:click={() => moveStep(s, n, -1)} title="Выше">↑</button>
                <button class="ghost" on:click={() => moveStep(s, n, 1)} title="Ниже">↓</button>
                <button class="del" on:click={() => delStep(s, n)} title="Удалить ступень">✕</button>
              </div>
            {/each}
            <button class="fix" on:click={() => addStep(s)}>+ Ступень</button>

            <div class="cfg-fields adv-row">
              <label title="Сколько неудачных проб подряд до перехода на следующую ступень. Пусто = общий порог (3)."
                >Отказов до перехода<input type="number" bind:value={s.EscalateAfter} on:input={touch} placeholder="3" /></label>
              <label title="Сколько успешных тихих проб подряд до возврата на ступень выше. Пусто = общий порог (5)."
                >Успехов до возврата<input type="number" bind:value={s.RecoverAfter} on:input={touch} placeholder="5" /></label>
              <label>Профиль<input bind:value={s.Profile} on:input={touch} list="cfg-profiles" placeholder="general" /></label>
            </div>
            <div class="cfg-fields adv-row">
              <label class="chk" title="Держаться одной ноды и менять её только при настоящем отказе, а не при скачке задержки."
                ><input type="checkbox" bind:checked={s.Sticky} on:change={touch} /> липкое</label>
              <label class="chk" title="Прибито: Лоцман никогда не двигает это правило по цепочке сам."
                ><input type="checkbox" bind:checked={s.Static} on:change={touch} /> прибито</label>
              <label class="chk" title="Резать TLS ClientHello средствами sing-box, чтобы DPI не прочитал SNI одним пакетом."
                ><input type="checkbox" bind:checked={s.TLSFragment} on:change={touch} /> tls_fragment</label>
            </div>
            <label class="full" title="Цель с реальным объёмом для канарейки. Проба намеренно мелкая и пропускную способность не измеряет — у youtube это 204 без тела, у discord 35 байт."
              >Цель для замера объёма<input bind:value={s.VolumeTarget} on:input={touch} placeholder="https://…/большой-файл" /></label>
            <label class="full" title="Голос и всё, что без SNI, ловится только по IP. Каноническая форма CIDR."
              >IP / подсети<input value={csv(s.IPs)} on:input={(e) => setCSV(s, 'IPs', e.target.value)} placeholder="66.22.192.0/18" /></label>
            <label class="full" title="Эти домены проходят через десинк СЫРЫМИ — для CDN, которые без него работают, а с ним ломаются."
              >Исключить из десинка<input value={csv(s.ExcludeDomains)} on:input={(e) => setCSV(s, 'ExcludeDomains', e.target.value)} placeholder="download.epicgames.com" /></label>
          </div>
        {/if}
      </div>
    {/each}
  </div>
  <datalist id="cfg-cats">{#each categories as c}<option value={c}></option>{/each}</datalist>
  <datalist id="cfg-states">{#each STATES as x}<option value={x}></option>{/each}</datalist>
  <datalist id="cfg-classes">{#each CLASSES as x}<option value={x}></option>{/each}</datalist>
  <datalist id="cfg-profiles">{#each PROFILES as x}<option value={x}></option>{/each}</datalist>
  <datalist id="cfg-pools">{#each poolNames as x}<option value={x}></option>{/each}</datalist>
{:else}
  <div class="muted">Пока нет правил. Добавь то, что нужно разблокировать.</div>
{/if}

<style>
  .more {
    align-self: flex-start;
    padding: 2px 0;
  }
  .svc-adv {
    border-top: 1px solid var(--panel-2);
    padding-top: 10px;
    display: flex;
    flex-direction: column;
    gap: 8px;
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
  .adv-row {
    align-items: flex-end;
  }
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
