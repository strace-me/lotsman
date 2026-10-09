<script>
  import { onMount } from 'svelte'
  import { mockConfig } from './mock.js'
  import SubsEdit from './SubsEdit.svelte'
  import DnsEdit from './DnsEdit.svelte'
  import EnginesEdit from './EnginesEdit.svelte'
  import HostlistsEdit from './HostlistsEdit.svelte'
  import PoolsEdit from './PoolsEdit.svelte'
  import StrategiesEdit from './StrategiesEdit.svelte'

  export let backend = null
  // Reported upward so the tab bar can switch away. A child that navigates has to say
  // so — reaching into the parent's state from here is how two components end up
  // disagreeing about which screen is current.
  export let onBack = () => {}
  // Set when a background refresh changed a declared domain pack: the running config
  // routes a slightly older set until something applies it. Applying is the operator's
  // call, so it is offered here rather than done on a timer.
  export let drifted = false

  let path = ''
  let rulesDir = ''
  let cfg = null // the document, as JSON with Go's PascalCase keys
  let rules = [] // one entry per rule file: {file, yaml, Rule}
  let originalJSON = ''
  let yaml = '' // configuration.yaml, raw — the escape hatch
  let originalYAML = ''
  let mode = 'structured' // structured | yaml
  let section = 'subs'
  let busy = '' // '' | check | save
  let message = ''
  let messageKind = '' // ok | err

  const errStr = (e) => String(e && e.message ? e.message : e)
  const touch = () => (cfg = cfg) // force reactivity after a nested mutation
  // The whole deployment is the unit, so the dirty check covers the document AND the
  // rules. Rules are sent back structured: absent would mean "leave them alone", and
  // this editor never means that — it always holds the complete set it was given.
  $: structuredJSON = JSON.stringify([cfg || null, rules.map((r) => [r.file, r.rule || null])])
  $: dirty = mode === 'yaml' ? yaml !== originalYAML : structuredJSON !== originalJSON

  const sections = [
    ['subs', 'Подписки'],
    ['pools', 'Пулы'],
    ['hostlists', 'Списки'],
    ['dns', 'DNS'],
    ['engines', 'Движки'],
    ['strategies', 'Стратегии'],
  ]
  // Rules are edited on their own tab: one file per rule, so a save is a write per
  // file and the diff the service compares is per rule.

  async function load() {
    message = ''
    try {
      const d = backend ? await backend.Config() : mockConfig()
      path = d.path
      rulesDir = d.rules_dir || ''
      yaml = d.yaml
      originalYAML = d.yaml
      cfg = d.config || null
      rules = d.rules || []
      originalJSON = structuredJSON
      // A document the service could not parse can only be fixed as raw text, and so
      // can a rule file — the structured view is best-effort per file.
      if (!cfg) mode = 'yaml'
    } catch (e) {
      messageKind = 'err'
      message = 'Не удалось загрузить конфиг: ' + errStr(e)
    }
  }

  async function check() {
    message = ''
    if (!backend) {
      messageKind = 'ok'
      message = 'Мок-режим: проверка на сервере недоступна'
      return
    }
    busy = 'check'
    try {
      if (mode === 'yaml') await backend.ValidateConfig(yaml)
      else await backend.ValidateConfigDoc(cfg, JSON.stringify(rules.map((r) => ({ file: r.file, rule: r.rule || null }))))
      messageKind = 'ok'
      message = 'Конфигурация валидна ✓'
    } catch (e) {
      messageKind = 'err'
      message = errStr(e)
    }
    busy = ''
  }

  async function save() {
    if (!confirm('Сохранить и применить? Сервис перезапустится — туннель и десинк на пару секунд оборвутся.')) return
    message = ''
    if (!backend) {
      messageKind = 'ok'
      message = 'Мок-режим: сохранение недоступно'
      return
    }
    busy = 'save'
    try {
      if (mode === 'yaml') {
        // Only the document. The service reads ABSENT rules as "leave them alone", so
        // the rules survive a hand edit of the document — which is the whole point of
        // an escape hatch that is not the whole deployment.
        await backend.SetConfig(yaml)
        originalYAML = yaml
      } else {
        await backend.SaveConfig(cfg, JSON.stringify(rules.map((r) => ({ file: r.file, rule: r.rule || null }))))
        originalJSON = structuredJSON
      }
      messageKind = 'ok'
      message = 'Сохранено. Сервис перезапускается…'
    } catch (e) {
      messageKind = 'err'
      message = errStr(e)
    }
    busy = ''
  }

  onMount(load)
</script>

<section>
  <!-- Back is STICKY, not just present. The section content scrolls inside `main`
       while the tab bar sits above it, so a long section (DNS, Движки) scrolls the
       navigation out of sight and leaves no way back but the window chrome. A control
       that disappears when you need it is not a control. -->
  <div class="row-head cfg-head">
    <button class="back" on:click={() => onBack()} title="Вернуться к обзору">← Назад</button>
    <h2>Конфигурация</h2>
    <span class="cfg-path">{path}</span>
  </div>

  <div class="cfg-mode">
    <button class:active={mode === 'structured'} on:click={() => (mode = 'structured')} disabled={!cfg}>Настройки</button>
    <button class:active={mode === 'yaml'} on:click={() => (mode = 'yaml')}>YAML</button>
  </div>

  {#if mode === 'structured'}
    {#if cfg}
      <div class="cfg-secnav">
        {#each sections as [id, label]}
          <button class:active={section === id} on:click={() => (section = id)}>{label}</button>
        {/each}
      </div>
      {#if section === 'subs'}
        <SubsEdit {cfg} {touch} {backend} />
      {:else if section === 'pools'}
        <PoolsEdit {cfg} {rules} {touch} />
      {:else if section === 'hostlists'}
        <HostlistsEdit {cfg} {rules} {touch} />
      {:else if section === 'dns'}
        <DnsEdit {cfg} {touch} />
      {:else if section === 'engines'}
        <EnginesEdit {cfg} {touch} />
      {:else if section === 'strategies'}
        <StrategiesEdit {cfg} {touch} />
      {/if}
    {:else}
      <div class="muted">Структурный вид недоступен — конфиг не распарсился. Открой YAML и почини вручную.</div>
    {/if}
  {:else}
    <div class="muted cfg-hint">
      Сырой YAML <code>configuration.yaml</code> — escape-hatch для тех, кому проще руками. Правь документ; правила в
      <code>rules/</code> сервис не трогает, пока не пришлёшь их явно. Сервис проверит перед записью.
    </div>
    <textarea class="cfg" bind:value={yaml} spellcheck="false" placeholder="загрузка…"></textarea>
  {/if}

  {#if drifted && cfg}
    <div class="cfg-drift">
      Списки доменов обновились в фоне. Пока применяется прежний набор — применить сейчас? Соединения на пару секунд
      оборвутся.
      <button class="fix" on:click={save} disabled={busy !== ''}>Применить списки</button>
    </div>
  {/if}

  <div class="cfg-actions">
    <button class="fix" on:click={check} disabled={busy !== ''}>Проверить</button>
    <button class="cta" on:click={save} disabled={busy !== '' || !dirty}>Сохранить и применить</button>
    <button class="ghost" on:click={load} disabled={!dirty || busy !== ''}>Сбросить</button>
    {#if message}<pre class="cfg-msg {messageKind}">{message}</pre>{/if}
  </div>
  <div class="muted cfg-note">
    Сохранение перезапускает сервис (короткий обрыв туннеля). Развёртка: <code>{path}</code>{#if rulesDir} и{' '}
    <code>{rulesDir}/</code>{/if}. Комментарии сохраняются только в режиме YAML — структурная запись их не переносит.
  </div>
</section>

<style>
  .cfg-head {
    position: sticky;
    top: 0;
    z-index: 2;
    background: var(--bg);
    /* The header sits over scrolling content, so it needs an edge to read as a bar
       rather than as text floating on top of a list. */
    box-shadow: 0 1px 0 var(--line);
    padding-bottom: 6px;
  }
  .back {
    background: none;
    border: 1px solid var(--line);
    border-radius: 6px;
    color: var(--dim);
    cursor: pointer;
    font: inherit;
    padding: 4px 10px;
  }
  .back:hover {
    color: var(--fg);
    border-color: var(--accent);
  }
  .cfg-drift {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.6rem;
    margin: 0.6rem 0 0;
    padding: 0.5rem 0.7rem;
    border: 1px solid currentColor;
    border-radius: 6px;
    opacity: 0.9;
    font-size: 0.88rem;
  }
</style>
