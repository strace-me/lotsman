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

  export let backend = null
  // Live state per rule, from /status — so a line can say what the rule is doing
  // right now, not merely how it is configured.
  export let report = null

  let doc = null
  let original = ''
  let path = ''
  let busy = ''
  let message = ''
  let messageKind = ''
  let openIdx = -1

  const errStr = (e) => String(e && e.message ? e.message : e)
  const touch = () => (doc = doc)
  $: dirty = doc && JSON.stringify(doc) !== original

  // Effective order: priority first (lower earlier, negatives ahead of all),
  // file order only as the tie-break. Showing file order would be a claim about
  // behaviour that does not hold.
  $: ordered = (doc?.Services || [])
    .map((s, i) => ({ s, i }))
    .sort((a, b) => (a.s.Priority || 0) - (b.s.Priority || 0) || a.i - b.i)

  $: live = Object.fromEntries(((report && report.services) || []).map((r) => [r.service, r]))
  $: disabled = new Set(((report && report.disabled) || []))

  // The ladder in one glance: zapret → vpn. This is the thing you scan the list
  // for, and it is not derivable from the rule's name.
  const ladder = (s) =>
    (s.Chain || []).map((st) => st.Class || st.State || '?').join(' → ') || 'по категории'

  const scope = (s) => {
    const n = (s.Domains || []).length + (s.DomainLists || []).length
    const ips = (s.IPs || []).length
    const bits = []
    if (n) bits.push(`${n} доменов`)
    if (ips) bits.push(`${ips} подсетей`)
    return bits.join(' · ') || 'ничего не выбрано'
  }

  async function load() {
    message = ''
    try {
      const d = backend ? await backend.Config() : mockConfig()
      path = d.path
      doc = d.doc || null
      original = doc ? JSON.stringify(doc) : ''
      if (!doc) {
        messageKind = 'err'
        message = 'Конфигурация не разбирается структурно — почини её на вкладке «Конфигурация», в режиме YAML.'
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
      await backend.SaveConfig(doc)
      original = JSON.stringify(doc)
      messageKind = 'ok'
      message = 'Сохранено, применяется…'
    } catch (e) {
      messageKind = 'err'
      message = errStr(e)
    }
    busy = ''
  }

  function add() {
    ;(doc.Services ||= []).push({ Name: '', Category: 'generic', ProbeTarget: '', Domains: [] })
    openIdx = doc.Services.length - 1
    touch()
  }
  function remove(i) {
    if (!confirm(`Удалить правило «${doc.Services[i].Name || 'без имени'}»?`)) return
    doc.Services.splice(i, 1)
    openIdx = -1
    touch()
  }

  onMount(load)
</script>

<div class="row-head">
  <h2>Правила</h2>
  <div class="rules-actions">
    <button class="fix" on:click={add} disabled={!doc}>+ Правило</button>
    <button class="cta" on:click={save} disabled={!dirty || busy !== ''}>Сохранить и применить</button>
    <button class="ghost" on:click={load} disabled={!dirty || busy !== ''}>Сбросить</button>
  </div>
</div>

{#if message}
  <div class="banner" class:red={messageKind === 'err'}>{message}</div>
{/if}

<div class="muted rules-hint">
  Сверху вниз — тот порядок, в котором правила <b>реально</b> проверяются: побеждает
  первое совпавшее, как в файрволе. Решает «приоритет», и лишь при равных — порядок в
  файле. <span class="dim">{path}</span>
</div>

{#if doc}
  <div class="rules">
    {#each ordered as { s, i } (i)}
      {@const st = live[s.Name]}
      <div class="rule" class:open={openIdx === i}>
        <button class="rule-line" on:click={() => (openIdx = openIdx === i ? -1 : i)}>
          <span class="caret">{openIdx === i ? '▾' : '▸'}</span>
          <span class="rule-name">{s.Name || 'без имени'}</span>
          <span class="rule-ladder">{ladder(s)}</span>
          <span class="rule-scope">{scope(s)}</span>
          <!-- What it is doing right now, next to how it is configured. The two
               disagree more often than anyone expects, and that is worth seeing. -->
          <span class="rule-live">
            {#if disabled.has(s.Name)}<span class="dim">выключено</span>
            {:else if st}
              <span class:red={st.broken} class:amber={!st.broken && st.fails > 0} class:ok={!st.broken && !st.fails}>
                {st.broken ? 'цепочка исчерпана' : st.fails ? `подбор · ${st.fails}` : st.state}
              </span>
            {/if}
          </span>
        </button>
        {#if openIdx === i}
          <div class="rule-body">
            <RuleForm rule={s} {doc} {touch} />
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
  .rules {
    display: flex;
    flex-direction: column;
    gap: 6px;
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
