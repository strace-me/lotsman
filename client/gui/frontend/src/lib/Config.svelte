<script>
  import { onMount } from 'svelte'
  import { mockConfig } from './mock.js'
  import SubsEdit from './SubsEdit.svelte'
  import ServicesEdit from './ServicesEdit.svelte'
  import DnsEdit from './DnsEdit.svelte'
  import EnginesEdit from './EnginesEdit.svelte'
  import StrategiesEdit from './StrategiesEdit.svelte'

  export let backend = null

  let path = ''
  let doc = null // structured config (config.Document as JSON, PascalCase keys)
  let originalJSON = ''
  let yaml = '' // raw escape-hatch text
  let originalYAML = ''
  let mode = 'structured' // structured | yaml
  let section = 'subs'
  let busy = '' // '' | check | save
  let message = ''
  let messageKind = '' // ok | err

  const errStr = (e) => String(e && e.message ? e.message : e)
  const touch = () => (doc = doc) // force reactivity after a nested mutation
  $: dirty = mode === 'yaml' ? yaml !== originalYAML : doc && JSON.stringify(doc) !== originalJSON

  const sections = [
    ['subs', 'Подписки'],
    ['services', 'Сервисы'],
    ['dns', 'DNS'],
    ['engines', 'Движки'],
    ['strategies', 'Стратегии'],
  ]

  async function load() {
    message = ''
    try {
      const d = backend ? await backend.Config() : mockConfig()
      path = d.path
      yaml = d.yaml
      originalYAML = d.yaml
      doc = d.doc || null
      originalJSON = doc ? JSON.stringify(doc) : ''
      if (!doc) mode = 'yaml' // a non-parsing file can only be fixed as raw text
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
      else await backend.ValidateConfigDoc(doc)
      messageKind = 'ok'
      message = 'Конфиг валиден ✓'
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
        await backend.SetConfig(yaml)
        originalYAML = yaml
      } else {
        await backend.SaveConfig(doc)
        originalJSON = JSON.stringify(doc)
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
  <div class="row-head">
    <h2>Конфиг</h2>
    <span class="cfg-path">{path}</span>
  </div>

  <div class="cfg-mode">
    <button class:active={mode === 'structured'} on:click={() => (mode = 'structured')} disabled={!doc}>Настройки</button>
    <button class:active={mode === 'yaml'} on:click={() => (mode = 'yaml')}>YAML</button>
  </div>

  {#if mode === 'structured'}
    {#if doc}
      <div class="cfg-secnav">
        {#each sections as [id, label]}
          <button class:active={section === id} on:click={() => (section = id)}>{label}</button>
        {/each}
      </div>
      {#if section === 'subs'}
        <SubsEdit {doc} {touch} />
      {:else if section === 'services'}
        <ServicesEdit {doc} {touch} />
      {:else if section === 'dns'}
        <DnsEdit {doc} {touch} />
      {:else if section === 'engines'}
        <EnginesEdit {doc} {touch} />
      {:else if section === 'strategies'}
        <StrategiesEdit {doc} {touch} />
      {/if}
    {:else}
      <div class="muted">Структурный вид недоступен — конфиг не распарсился. Открой YAML и почини вручную.</div>
    {/if}
  {:else}
    <div class="muted cfg-hint">Сырой YAML — escape-hatch для тех, кому проще руками. Правь целиком; сервис проверит перед записью.</div>
    <textarea class="cfg" bind:value={yaml} spellcheck="false" placeholder="загрузка…"></textarea>
  {/if}

  <div class="cfg-actions">
    <button class="fix" on:click={check} disabled={busy !== ''}>Проверить</button>
    <button class="cta" on:click={save} disabled={busy !== '' || !dirty}>Сохранить и применить</button>
    <button class="ghost" on:click={load} disabled={!dirty || busy !== ''}>Сбросить</button>
    {#if message}<pre class="cfg-msg {messageKind}">{message}</pre>{/if}
  </div>
  <div class="muted cfg-note">
    Сохранение перезапускает сервис (короткий обрыв туннеля). Комментарии в YAML сохраняются только в режиме YAML —
    структурная запись их не переносит.
  </div>
</section>
