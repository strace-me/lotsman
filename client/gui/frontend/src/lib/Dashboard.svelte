<script>
  import { tileKind, tileGlyph, tileWhere, fmtBytes, fmtTime } from './format.js'

  export let report
  export let events = []
  export let onRecheck = () => {}

  $: services = report.services || []
  $: problems = services.filter((s) => s.broken || s.fails > 0)
  $: working = services.filter((s) => !s.broken && s.fails === 0)
</script>

<div class="stats">
  <div class="stat">
    <div class="k">Пул нод</div>
    <div class="v">{report.fleet.total}</div>
  </div>
  <div class="stat">
    <div class="k">Сеть</div>
    <div class="v small">{report.network.kind || '—'}{report.network.roaming ? ' · роуминг' : ''}</div>
    <div class="sub">{report.network.iface || report.network.id}</div>
  </div>
  <div class="stat">
    <div class="k">Сервисы</div>
    <div class="v small rollup">
      <span class="ok">{report.verdict.working} работают</span>
      <span class="amber">{report.verdict.failing} подбор</span>
      <span class="red">{report.verdict.broken} не работает</span>
    </div>
  </div>
</div>

{#if problems.length}
  <section>
    <h2>Требуют внимания</h2>
    <div class="tiles">
      {#each problems as s (s.service)}
        <div class="tile {tileKind(s)}">
          <div class="tile-head">
            <span class="glyph {tileKind(s)}">{tileGlyph(s)}</span>
            <span class="name">{s.service}</span>
          </div>
          <div class="where">{s.broken ? 'нет живых нод' : tileWhere(s)}</div>
          <button class="fix" on:click={() => onRecheck(s.service)}>перепроверить</button>
        </div>
      {/each}
    </div>
  </section>
{/if}

<section>
  <h2>Работают · {working.length}</h2>
  <div class="rows">
    {#each working as s (s.service)}
      <div class="row">
        <span class="glyph {tileKind(s)}">{tileGlyph(s)}</span>
        <span class="name">{s.service}</span>
        <span class="where">{tileWhere(s)}</span>
        <button class="ghost" on:click={() => onRecheck(s.service)}>перепроверить</button>
      </div>
    {/each}
  </div>
</section>

<div class="two-col">
  <section>
    <h2>История событий</h2>
    <div class="events">
      {#each events as ev}
        <div class="event">
          <span class="t">{fmtTime(ev.time)}</span>
          <span class="es">{ev.service}</span>
          <span class="er">{ev.reason || ev.state}</span>
        </div>
      {:else}
        <div class="muted">пока пусто</div>
      {/each}
    </div>
  </section>

  <section>
    <h2>Подписки</h2>
    <div class="subs">
      {#each report.subscriptions as s}
        <div class="subrow" class:warn={s.daysUntilExpire >= 0 && s.daysUntilExpire < 5}>
          <span class="name">{s.name}</span>
          <span class="meta">
            {#if s.daysUntilExpire >= 0}{Math.round(s.daysUntilExpire)} дней{/if}
            {#if s.totalBytes > 0}· {Math.round(s.fractionUsed * 100)}%{/if}
          </span>
          <span class="bytes">{fmtBytes(s.usedBytes)}{s.totalBytes ? ' / ' + fmtBytes(s.totalBytes) : ''}</span>
        </div>
      {:else}
        <div class="muted">нет подписок</div>
      {/each}
    </div>
  </section>
</div>
