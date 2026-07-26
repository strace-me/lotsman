<script>
  export let report
  export let onStop = () => {}
  let showRaw = false
</script>

<section>
  <h2>Движки</h2>
  <div class="rows">
    {#each report.engines as e (e.name)}
      <div class="row">
        <span class="glyph {e.state === 'running' ? 'ok' : 'red'}">●</span>
        <span class="name">{e.name}</span>
        <span class="where">{e.state === 'running' ? 'работает' : 'остановлен'}</span>
        <button class="ghost" disabled title="Управление отдельными движками подключим позже">старт / стоп</button>
      </div>
    {/each}
  </div>
  <div class="muted">
    Пуск/останов отдельных движков (sing-box · nfqws) подключим к control-API следующим —
    сейчас это только чтение состояния (останов sing-box уронил бы туннель, поэтому делаем аккуратно).
  </div>
</section>

<section>
  <h2>Питание</h2>
  <button class="danger" on:click={onStop}>Выключить сервис Lotsman</button>
  <div class="muted">Мастер-выключатель: остановит туннель, десинк и сам сервис.</div>
</section>

<section>
  <h2>Отладка</h2>
  <button class="fix" on:click={() => (showRaw = !showRaw)}>{showRaw ? 'Скрыть' : 'Показать'} сырой /status</button>
  {#if showRaw}<pre class="raw">{JSON.stringify(report, null, 2)}</pre>{/if}
</section>
