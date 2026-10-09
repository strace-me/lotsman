<script>
  // First run — the state a machine is in before a service exists.
  //
  // This screen exists because of a real deadlock rather than a missing feature:
  // every other screen in this window edits the configuration THROUGH a running
  // service, and the service refuses to start without a valid deployment. So the
  // one thing a new user needs is the one thing the window cannot produce while
  // there is no service to ask.
  //
  // It therefore writes the deployment DIRECTLY, to a path the user owns, so this
  // unprivileged window needs no root and never writes a file it does not own.
  // The service reads a materialized copy of it (see -config-source).
  //
  // The honest limit, said on screen rather than discovered later: writing this does
  // NOT start the service. On a packaged install the unit is started by systemd,
  // which this window deliberately does not do — a window that shells out to sudo at
  // the exact moment a new user is least likely to know what it is being asked for is
  // how an install turns into a support ticket.

  export let backend = null
  // Where the deployment will be written. Passed in rather than fetched again: the
  // window already asked, and a component that re-asks the same question is a second
  // place for the answer to be missing.
  export let sourcePath = ''
  export let legacy = false
  // How the service on THIS machine is started, as reported by the backend. Asked
  // rather than assumed: the first version of this screen printed
  // `systemctl --user restart lotsman`, which was wrong twice over — the unit is a
  // SYSTEM unit, and on this machine it is called `lotsman-client.service`. A command
  // the app prints that cannot work sends the operator to a shell to be told
  // "no such unit" at the moment they have just finished setup.
  let unit = { installed: false, name: '', scope: 'system', running: false }

  async function findUnit() {
    if (!backend || !backend.ServiceUnitInfo) return
    try {
      unit = await backend.ServiceUnitInfo()
    } catch (e) {
      /* an older window: no unit information, and the screen says so below */
    }
  }

  // Only a SYSTEM unit needs sudo and no --user; getting either wrong produces a
  // command that silently does nothing.
  $: restartCmd = !unit.installed
    ? ''
    : 'systemctl ' + (unit.scope === 'user' ? '--user ' : '') +
      'restart ' + unit.name + (unit.scope === 'user' ? '' : '')

  let state = null
  let choice = 'recommended'
  let sub = ''
  let busy = false
  let error = ''
  let written = ''

  // Two cards, not a text box. The choice is between two curated sets, and the whole
  // point of the recommended one is that the user does not have to know what belongs
  // in a config file yet.
  const SETS = [
    {
      id: 'recommended',
      name: 'Рекомендуемый набор',
      note: 'YouTube, Discord, соцсети, AI, игры — на рабочих цепочках. Без учётных данных.',
      recommended: true,
    },
    {
      id: 'minimal',
      name: 'Минимальный',
      note: 'YouTube, Discord, Instagram. Подписка обязательна: без нод сервис не запустится.',
    },
  ]

  async function check() {
    if (!backend || !backend.SetupState) return
    try {
      state = await backend.SetupState()
      error = ''
    } catch (e) {
      error = String(e && e.message ? e.message : e)
    }
  }

  async function write() {
    if (!backend) {
      // Demo browser: pretend it landed, so the confirmation step is designable too.
      written = sourcePath || '/home/user/.config/lotsman'
      return
    }
    busy = true
    error = ''
    try {
      const urls = sub
        .split(/[\s,]+/)
        .map((s) => s.trim())
        .filter(Boolean)
      written = await backend.WriteFirstDeployment(choice, urls)
    } catch (e) {
      error = String(e && e.message ? e.message : e)
    } finally {
      busy = false
    }
  }

  $: if (backend) {
    check()
    findUnit()
  }
</script>

<div class="setup">
  <h1>Настройка</h1>

    {#if error}
    <div class="banner red">{error}</div>
  {/if}
  {#if legacy}
    <div class="banner amber">
      Найдено развёртывание версии 1 — настроенная машина, просто не этой схемой. Записать новый набор
      поверх него здесь нельзя: сначала <code>lotsmanctl v2-migrate</code>.
    </div>
  {/if}

  {#if written}
    <div class="done">
      <h2>Конфигурация записана</h2>
      <p class="path">{written}</p>
      <p>Теперь запустите службу — она скопирует эту папку туда, откуда читает сама.</p>
      {#if unit.installed}
        {#if unit.scope === 'system'}
          <pre>sudo {restartCmd}</pre>
        {:else}
          <pre>{restartCmd}</pre>
        {/if}
        <p class="fine">
          Найдено systemd-юнитом: <code>{unit.name}</code>
          {unit.running ? '(уже запущена)' : '(не запущена)'}. Окно покажет состояние службы, как только она
          ответит.
        </p>
      {:else}
        <p class="fine">
          systemd-юнит Lotsman на этой машине не найден — поэтому команды не показываю: выдуманная
          команда хуже её отсутствия. Запусти бинарь напрямую из <code>client/desktop</code> либо
          установи юнит.
        </p>
      {/if}
    </div>
  {:else}
    <p class="lead">
      Выберите набор правил и укажите подписку. Оба варианта можно будет изменить потом во вкладке
      «Конфиг» — здесь нужно только получить рабочую конфигурацию.
    </p>

    <div class="sets">
      {#each SETS as s}
        <label class="set" class:chosen={choice === s.id}>
          <input type="radio" bind:group={choice} value={s.id} />
          <div>
            <div class="name">
              {s.name}
              {#if s.recommended}<span class="badge">рекомендуется</span>{/if}
            </div>
            <div class="note">{s.note}</div>
          </div>
        </label>
      {/each}
    </div>

    <label class="field">
      <span>Подписка (URL)</span>
      <!-- The recommendation matters and it is not obvious: a subscription URL is a
           credential. It belongs in a file the user owns and nowhere else, which is
           why this writes to the user config dir rather than a system path. -->
      <input
        type="text"
        bind:value={sub}
        placeholder="https://.../sub"
        disabled={choice === 'recommended' && !sub}
      />
      <span class="fine">
        Несколько URL — через пробел или запятую. Можно пропустить и добавить позже во вкладке
        «Подписки», но без нод туннель не поднимется.
      </span>
    </label>

    <button class="go" on:click={write} disabled={busy}>
      {busy ? 'Записываю…' : 'Записать конфигурацию'}
    </button>

    {#if sourcePath}
      <p class="fine">Будет записано в <code>{sourcePath}</code></p>
    {/if}
  {/if}
</div>

<style>
  .setup {
    max-width: 640px;
    padding: 24px 4px;
  }
  h1 {
    margin: 0 0 4px;
    font-size: 20px;
    font-weight: 600;
  }
  h2 {
    margin: 0 0 6px;
    font-size: 16px;
  }
  .lead {
    color: var(--dim);
    margin: 0 0 18px;
  }
  .sets {
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin-bottom: 18px;
  }
  .set {
    display: flex;
    gap: 10px;
    align-items: flex-start;
    padding: 12px 14px;
    border: 1px solid var(--line);
    border-radius: 8px;
    cursor: pointer;
  }
  .set.chosen {
    border-color: var(--accent);
    /* The chosen one must be legible without relying on colour alone — see the
       accessibility rule in PRODUCT.md: state never rides on hue by itself. */
    box-shadow: inset 0 0 0 1px var(--accent);
  }
  .name {
    font-weight: 600;
    display: flex;
    gap: 8px;
    align-items: center;
  }
  .badge {
    font-size: 11px;
    font-weight: 500;
    padding: 1px 6px;
    border-radius: 4px;
    border: 1px solid var(--line);
    color: var(--dim);
  }
  .note {
    color: var(--dim);
    font-size: 13px;
    margin-top: 2px;
  }
  .field {
    display: block;
    margin-bottom: 18px;
  }
  .field > span:first-child {
    display: block;
    font-size: 13px;
    margin-bottom: 4px;
  }
  input[type='text'] {
    width: 100%;
    box-sizing: border-box;
    padding: 7px 9px;
    border-radius: 6px;
    border: 1px solid var(--line);
    background: var(--bg);
    color: var(--fg);
    font-family: inherit;
  }
  .go {
    padding: 8px 16px;
    border-radius: 6px;
    border: 1px solid var(--accent);
    background: var(--accent);
    color: #06121a;
    font-weight: 600;
    cursor: pointer;
  }
  .go:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .done {
    border: 1px solid var(--line);
    border-radius: 8px;
    padding: 16px 18px;
  }
  .path {
    font-family: ui-monospace, monospace;
    color: var(--dim);
    font-size: 13px;
    word-break: break-all;
  }
  pre {
    background: var(--bg);
    border: 1px solid var(--line);
    border-radius: 6px;
    padding: 10px 12px;
    overflow-x: auto;
  }
  .fine {
    color: var(--dim);
    font-size: 12px;
  }
</style>
