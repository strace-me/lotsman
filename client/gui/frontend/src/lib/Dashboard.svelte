<script>
  import { tileKind, tileGlyph, tileWhere, tileRequested, fmtBytes, fmtTime } from './format.js'

  export let report
  export let events = []
  export let onRecheck = () => {}
  export let onToggle = () => {}

  // These are RULES — a list of addresses and what to do with them. The three
  // SERVICES are lotsman, sing-box and nfqws, and they live in the header.
  $: rules = report.services || []
  // "Требуют внимания" means LOTSMAN HAS RUN OUT OF MOVES — the chain is
  // exhausted and nothing it can do will help, so the operator has to. A service
  // merely failing a probe is Lotsman WORKING: it is escalating, and calling that
  // an alarm is the crying-wolf this project deliberately has no alerting layer to
  // avoid. Measured the day it mattered: Discord carried 3.5 MB of voice while its
  // probe timed out, and the panel called it a problem.
  $: problems = rules.filter((s) => s.broken)
  $: searching = rules.filter((s) => !s.broken && s.fails > 0)
  $: working = rules.filter((s) => !s.broken && s.fails === 0)
  $: disabled = report.disabled || []
  $: notices = report.notices || []
</script>

<!-- Above everything, because the point of a notice is that the numbers below it
     look fine. The text comes from the daemon verbatim — it is the thing that
     evaluated the condition, and a second copy of the wording here would drift. -->
{#each notices as n (n.code)}
  <div class="notice">
    <span class="notice-mark">!</span>
    <div>
      <div>{n.text}</div>
      {#if n.services && n.services.length}
        <div class="notice-svc">Затронуты: {n.services.join(', ')}</div>
      {/if}
    </div>
  </div>
{/each}

<div class="stats">
  <div class="stat">
    <!-- Just the count. The tile used to say «Пул нод» over the whole-fleet
         number — the label named one entity and the number another, which is how
         nobody noticed fifty exits arriving as six. The breakdown belongs on
         «Ноды», not here. -->
    <div class="k">Ноды</div>
    <div class="v">{report.fleet.total}</div>
  </div>
  <div class="stat">
    <div class="k">Сеть</div>
    <div class="v small">{report.network.kind || '—'}{report.network.roaming ? ' · роуминг' : ''}</div>
    <div class="sub">{report.network.iface || report.network.id}</div>
  </div>
  <div class="stat">
    <div class="k">Правила</div>
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
          <div class="where">
            {tileWhere(s)} · цепочка исчерпана
            {#if tileRequested(s)}<div class="mismatch">{tileRequested(s)}</div>{/if}
          </div>
          <div class="tile-actions">
            <button class="fix" on:click={() => onRecheck(s.service)}>перепроверить</button>
            <button class="ghost" on:click={() => onToggle(s.service, false)}>выключить</button>
          </div>
        </div>
      {/each}
    </div>
  </section>
{/if}

{#if searching.length}
  <section>
    <h2>Подбирает · {searching.length}</h2>
    <!-- Amber, not red, and no verb telling the operator to act: this is the tool
         doing its job. It is shown at all only because a service stuck here for a
         long time is worth noticing. -->
    <div class="rows">
      {#each searching as s (s.service)}
        <div class="row">
          <span class="glyph amber">↻</span>
          <span class="name">{s.service}</span>
          <span class="where">
            {tileWhere(s)}{#if tileRequested(s)} <span class="mismatch">({tileRequested(s)})</span>{/if}
            <span class="dim"> · проба не прошла {s.fails}×</span>
          </span>
          <button class="ghost" on:click={() => onRecheck(s.service)}>перепроверить</button>
          <button class="ghost" on:click={() => onToggle(s.service, false)}>выключить</button>
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
        <span class="where">{tileWhere(s)}{#if tileRequested(s)} <span class="mismatch">({tileRequested(s)})</span>{/if}</span>
        <button class="ghost" on:click={() => onRecheck(s.service)}>перепроверить</button>
        <button class="ghost" on:click={() => onToggle(s.service, false)}>выключить</button>
      </div>
    {/each}
  </div>
</section>

{#if disabled.length}
  <section>
    <h2>Выключены · {disabled.length}</h2>
    <!-- Shown because a service that is absent on purpose and a service that has
         gone missing look identical otherwise, and the operator needs to tell them
         apart without opening the config. -->
    <div class="rows">
      {#each disabled as name (name)}
        <div class="row off">
          <span class="glyph dim">○</span>
          <span class="name">{name}</span>
          <span class="where">не проверяется и не маршрутизируется</span>
          <button class="ghost" on:click={() => onToggle(name, true)}>включить</button>
        </div>
      {/each}
    </div>
  </section>
{/if}

<div class="two-col">
  <section>
    <h2>История событий</h2>
    <div class="events">
      {#each events || [] as ev}
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
      {#each report.subscriptions || [] as s}
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
