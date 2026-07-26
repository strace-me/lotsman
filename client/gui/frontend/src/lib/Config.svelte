<script>
  import { onMount } from 'svelte'
  import { mockConfig } from './mock.js'

  export let backend = null

  let path = ''
  let yaml = ''
  let original = ''
  let busy = '' // '' | 'check' | 'save'
  let message = ''
  let messageKind = '' // ok | err

  const errStr = (e) => String(e && e.message ? e.message : e)
  $: dirty = yaml !== original

  async function load() {
    message = ''
    try {
      const d = backend ? await backend.Config() : mockConfig()
      path = d.path
      yaml = d.yaml
      original = d.yaml
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
      await backend.ValidateConfig(yaml)
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
      await backend.SetConfig(yaml)
      original = yaml
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
  <div class="muted cfg-hint">
    Правь YAML целиком — подписки, сервисы, цепочки, пулы. Сервис <b>проверит его перед
    записью</b>, невалидный не сохранится. Сохранение перезапускает сервис (короткий обрыв туннеля).
  </div>

  <textarea class="cfg" bind:value={yaml} spellcheck="false" placeholder="загрузка…"></textarea>

  <div class="cfg-actions">
    <button class="fix" on:click={check} disabled={busy !== ''}>Проверить</button>
    <button class="cta" on:click={save} disabled={busy !== '' || !dirty}>Сохранить и применить</button>
    <button class="ghost" on:click={load} disabled={!dirty || busy !== ''}>Сбросить</button>
    {#if message}<pre class="cfg-msg {messageKind}">{message}</pre>{/if}
  </div>
</section>
