<script>
  // Rules get their own tab, as a flat list: one line per rule, opened by
  // clicking it. Everything a rule can carry — chain, thresholds, IPs, exclusions
  // — is real but rarely edited, and showing it all at once made eleven rules
  // unreadable. The line answers the two questions you actually arrive with:
  // what is this called, and where does it send traffic.
  //
  // This tab OWNS rules; «Конфигурация» no longer has a rules section. Editing the
  // same thing in two places is what the Подписки tab was doing before it was
  // removed.
  import { onMount } from 'svelte'
  import { mockConfig } from './mock.js'
  import RuleForm from './RuleForm.svelte'
  import { pluralList, pluralDomain, pluralCidr } from './format.js'

  export let backend = null
  // Live state per rule, from /status — so a line can say what the rule is doing
  // right now, not merely how it is configured.
  export let report = null

  // A rule is a FILE. The document holds the deployment; the rules hold what it steers.
  // So this tab holds a list of {file, yaml, Rule} and sends all of them back — absent
  // would mean "leave the rules alone", which is never what this tab means.
  let cfg = null
  let rules = []
  let original = ''
  let path = ''
  let rulesDir = ''
  let busy = ''
  let message = ''
  let messageKind = ''
  let openIdx = -1

  const errStr = (e) => String(e && e.message ? e.message : e)
  const touch = () => (rules = rules)
  const snapshot = () => JSON.stringify([cfg || null, rules.map((r) => [r.file, r.rule || null])])
  $: dirty = snapshot() !== original

  // The order the rules are MATCHED in: priority first (lower earlier, negatives
  // ahead of all), then ID. Not file order — the loader reads a directory and sorts,
  // file position does not survive it, and the generator has always tie-broken by
  // name. A list claiming an order the engine does not use is worse than an unordered
  // one. The runtime's service name IS the rule id, which is why /status keys on it.
  $: ordered = rules
    .map((r, i) => ({ r, i, rule: r.rule }))
    .filter((x) => x.rule)
    .sort(
      (a, b) =>
        (a.rule.Priority || 0) - (b.rule.Priority || 0) ||
        String(a.rule.ID || '').localeCompare(String(b.rule.ID || ''))
    )

  $: live = Object.fromEntries(((report && report.services) || []).map((r) => [r.service, r]))
  $: disabled = new Set(((report && report.disabled) || []))

  // The ladder in one glance: desync → vpn. This is the thing you scan the list for,
  // and it is not derivable from the rule's name. The step carries an ACTION, not a
  // class, so the label is read off whichever of the three is present.
  const stepLabel = (step) => {
    const a = step && step.Action
    if (!a) return '?'
    if (a.Direct) return 'direct'
    if (a.Desync) return 'desync'
    if (a.VPN) return 'vpn'
    return '?'
  }
  const ladder = (r) => (r.Chain || []).map(stepLabel).join(' → ') || 'пусто'

  // Named lists count too. Without them the two rules that are defined ENTIRELY by
  // lists — ru-direct and the web-blocked catch-all — read as «ничего не выбрано»,
  // i.e. as broken, when they are the two that match the most traffic.
  const scope = (r) => {
    const m = r.Match || {}
    const lists = (m.DomainLists || []).length + (m.IPLists || []).length
    const n = (m.Domains || []).length
    const ips = (m.CIDRs || []).length
    const bits = []
    if (lists) bits.push(pluralList(lists))
    if (n) bits.push(pluralDomain(n))
    if (ips) bits.push(pluralCidr(ips))
    return bits.join(' · ') || 'ничего не выбрано'
  }

  async function load() {
    message = ''
    try {
      const d = backend ? await backend.Config() : mockConfig()
      path = d.path
      rulesDir = d.rules_dir || ''
      cfg = d.config || null
      rules = d.rules || []
      original = snapshot()
      if (!cfg) {
        messageKind = 'err'
        message = 'Документ не разбирается структурно — почини его на вкладке «Конфигурация», в режиме YAML.'
      }
    } catch (e) {
      messageKind = 'err'
      message = 'Не удалось загрузить конфигурацию: ' + errStr(e)
    }
  }

  async function save() {
    if (!confirm('Сохранить и применить? Правила маршрутизации изменятся, живые соединения могут оборваться.')) return
    message = ''
    if (!backend) {
      messageKind = 'ok'
      message = 'Мок-режим: сохранение недоступно'
      return
    }
    busy = 'save'
    try {
      await backend.SaveConfig(cfg, JSON.stringify(rules.map((r) => ({ file: r.file, rule: r.rule || null }))))
      original = snapshot()
      messageKind = 'ok'
      message = 'Сохранено, применяется…'
    } catch (e) {
      messageKind = 'err'
      message = errStr(e)
    }
    busy = ''
  }

  // The file name IS the id, so a new rule gets one derived from the id and the two
  // cannot drift — a deployment is readable by listing the directory.
  const slug = (s) =>
    (s || '')
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-|-$/g, '')

  function add() {
    let id = 'new-rule'
    for (let i = 2; rules.some((r) => r.file === id + '.yaml'); i++) id = 'new-rule-' + i
    rules.push({
      file: id + '.yaml',
      yaml: '',
      Rule: {
        ID: id,
        Name: 'Новое правило',
        Enabled: true,
        Priority: 0,
        Match: { Domains: [], DomainLists: [], IPLists: [], CIDRs: [] },
        Probe: { Type: 'http', Target: '' },
        Behavior: {},
        Chain: [{ Tier: 'fallback', Action: { VPN: { Pool: 'vpn_url_test' } } }],
      },
    })
    openIdx = rules.length - 1
    touch()
  }
  function remove(i) {
    const r = rules[i]
    if (!confirm(`Удалить правило «${(r.rule && (r.rule.Name || r.rule.ID)) || r.file}»?`)) return
    rules.splice(i, 1)
    openIdx = -1
    touch()
  }
  // Renaming the id renames the file, because the loader refuses a file whose name is
  // not its rule's id — a rename that left the file behind would stop the deployment
  // loading at all, and the error would point at the file, not at the rename.
  function renameId(oldFile, newId) {
    const id = slug(newId)
    if (!id || id + '.yaml' === oldFile) return
    if (rules.some((r) => r.file === id + '.yaml')) return // taken; leave the field
    const item = rules.find((r) => r.file === oldFile)
    if (!item || !item.rule) return
    item.rule.ID = id
    item.file = id + '.yaml'
    touch()
  }

  onMount(load)
</script>

<div class="row-head">
  <h2>Правила</h2>
  <div class="rules-actions">
    <button class="fix" on:click={add} disabled={!cfg}>+ Правило</button>
    <button class="cta" on:click={save} disabled={!dirty || busy !== ''}>Сохранить и применить</button>
    <button class="ghost" on:click={load} disabled={!dirty || busy !== ''}>Сбросить</button>
  </div>
</div>

{#if !backend}
  <!-- The mock config is not this machine'"'"'s config, and the two are otherwise
       indistinguishable on screen — the path below is the only tell. -->
  <div class="banner amber">Демо-конфигурация, не файл этой машины.</div>
{/if}
{#if message}
  <div class="banner" class:red={messageKind === 'err'}>{message}</div>
{/if}

<div class="muted rules-hint">
  Сверху вниз — тот порядок, в котором правила <b>реально</b> проверяются: побеждает
  первое совпавшее, как в файрволе. Решает «приоритет» (меньше — раньше, отрицательные
  впереди всех), при равных — имя. «Приоритет» наш, а не sing-box: в его конфиг он не
  попадает, он лишь задаёт место правила в списке маршрутов. Одна оговорка: правила,
  совпадающие только по IP, идут после <i>всех</i> доменных, каким бы ни был приоритет.
  <span class="dim">{path}{#if rulesDir} · {rulesDir}/{/if}</span>
</div>

{#if cfg}
  <div class="rules">
    {#each ordered as { r, rule, i } (r.file)}
      {@const st = live[rule.ID]}
      <div class="rule" class:open={openIdx === i}>
        <button class="rule-line" on:click={() => (openIdx = openIdx === i ? -1 : i)}>
          <span class="caret">{openIdx === i ? '▾' : '▸'}</span>
          <span class="rule-name">{rule.Name || rule.ID || 'без имени'}</span>
          <span class="rule-ladder">{ladder(rule)}</span>
          <span class="rule-scope">{scope(rule)}</span>
          <!-- What it is doing right now, next to how it is configured. The two
               disagree more often than anyone expects, and that is worth seeing. -->
          <span class="rule-live">
            {#if rule.Bypass}<span class="dim">в обход</span>
            {:else if !rule.Enabled}<span class="dim">выключено</span>
            {:else if st}
              <span class:red={st.broken} class:amber={!st.broken && st.fails > 0} class:ok={!st.broken && !st.fails}>
                {st.broken ? 'цепочка исчерпана' : st.fails ? `подбор · ${st.fails}` : st.state}
              </span>
            {/if}
          </span>
        </button>
        {#if openIdx === i}
          <div class="rule-body">
            <RuleForm {rule} {cfg} {rules} {touch} {renameId} file={r.file} />
            <button class="danger rule-del" on:click={() => remove(i)}>Удалить правило</button>
          </div>
        {/if}
      </div>
    {/each}
  </div>
{/if}

<style>
  .rules-actions {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .rules-actions .cta {
    margin-top: 0;
    padding: 4px 12px;
  }
  .rules-hint {
    margin: 0.15rem 0 0.7rem;
    line-height: 1.5;
  }
  /* The list scrolls, not the tab. overflow-y alone did nothing: with no bound and
     no shrinkable parent the container simply grew and <main> scrolled instead,
     which is what the owner saw. flex + min-height:0 makes it the shrinkable child
     of the column <main> already is. */
  .rules {
    display: flex;
    flex-direction: column;
    gap: 6px;
    flex: 1 1 auto;
    min-height: 0;
    overflow-y: auto;
  }
  .rule {
    border: 1px solid var(--line);
    border-radius: var(--radius);
    background: var(--panel);
  }
  .rule.open {
    border-color: var(--accent);
  }
  .rule-line {
    display: flex;
    align-items: baseline;
    gap: 14px;
    width: 100%;
    background: transparent;
    border: none;
    text-align: left;
    padding: 10px 14px;
  }
  .caret {
    color: var(--ink-faint);
    width: 1em;
  }
  .rule-name {
    font-weight: 600;
    min-width: 9em;
  }
  .rule-ladder {
    color: var(--accent);
    font-size: 12px;
    min-width: 14em;
  }
  .rule-scope {
    color: var(--ink-faint);
    font-size: 12px;
  }
  .rule-live {
    margin-left: auto;
    font-size: 12px;
  }
  .rule-body {
    border-top: 1px solid var(--panel-2);
    padding: 12px 14px;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .rule-del {
    align-self: flex-start;
  }
</style>
