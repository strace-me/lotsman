<script>
  import { onMount, onDestroy } from 'svelte'

  let report = null
  let events = []
  let error = ''
  let tab = 'dashboard'
  let timer

  // Wails injects window.go.main.App at runtime. In a plain `vite dev` browser it
  // is absent, so fall back to mock data — the skeleton still renders + designs.
  const backend =
    typeof window !== 'undefined' && window.go && window.go.main && window.go.main.App
      ? window.go.main.App
      : null

  async function load() {
    try {
      if (backend) {
        report = await backend.Status()
        events = await backend.Events(20, '')
      } else {
        const m = mock()
        report = m.report
        events = m.events
      }
      error = ''
    } catch (e) {
      error = String(e && e.message ? e.message : e)
    }
  }

  async function recheck(name) {
    if (backend) {
      try {
        await backend.Recheck(name)
      } catch (e) {
        error = String(e)
      }
    }
    setTimeout(load, 500)
  }

  onMount(() => {
    load()
    timer = setInterval(load, 2000)
  })
  onDestroy(() => clearInterval(timer))

  $: services = report ? report.services : []
  $: problems = services.filter((s) => s.broken || s.fails > 0)
  $: working = services.filter((s) => !s.broken && s.fails === 0)

  const verdictText = {
    working: 'всё работает',
    partial: 'не все сервисы доступны',
    'not-working': 'не работает',
    down: 'выключено',
  }
  function verdictKind(v) {
    if (!v) return 'dim'
    if (v.state === 'working') return 'ok'
    if (v.state === 'down' || v.state === 'not-working') return 'red'
    return 'amber'
  }

  function tileKind(s) {
    if (s.broken) return 'red'
    if (s.fails > 0) return 'amber'
    if (s.rungClass === 'direct') return 'dim'
    return 'ok'
  }
  function tileGlyph(s) {
    if (s.broken) return '✗'
    if (s.fails > 0) return '⟳'
    if (s.rungClass === 'direct') return '·'
    return '✓'
  }
  function tileWhere(s) {
    if (s.rungClass === 'direct') return 'напрямую'
    const bits = [s.rungClass, s.strategy].filter(Boolean)
    if (s.node) bits.push(s.node)
    return bits.join(' · ')
  }

  function fmtBytes(n) {
    if (!n) return '—'
    const gb = n / 1e9
    return gb >= 1 ? gb.toFixed(1) + ' ГБ' : (n / 1e6).toFixed(0) + ' МБ'
  }
  function fmtTime(t) {
    const d = new Date(t)
    return isNaN(d) ? '' : d.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' })
  }

  function mock() {
    return {
      report: {
        running: true,
        verdict: { state: 'partial', working: 6, failing: 1, broken: 1, total: 8 },
        network: { id: 'a1b2c3d4', kind: 'wifi', carrier: '', iface: 'wlan0', roaming: false },
        engines: [
          { name: 'lotsman', state: 'running' },
          { name: 'sing-box', state: 'running' },
          { name: 'nfqws', state: 'running' },
        ],
        fleet: { total: 150 },
        subscriptions: [
          { name: 'demo-vless', usedBytes: 8.6e9, totalBytes: 10.7e9, fractionUsed: 0.8, daysUntilExpire: 12, expired: false },
        ],
        services: [
          { service: 'youtube', state: 'VPN', node: 'nl-hy2-01', rung: 0, rungClass: 'vpn', strategy: 'vpn_pool', fails: 0, broken: false },
          { service: 'discord', state: 'PREFERRED', node: '', rung: 0, rungClass: 'zapret', strategy: 'flowseal-syndata', fails: 0, broken: false },
          { service: 'ai', state: 'BROKEN', node: '', rung: 3, rungClass: 'emergency', strategy: 'emergency_pool', stalledRatio: 0.91, fails: 6, broken: true },
          { service: 'web-blocked', state: 'VPN', node: 'de-vless-02', rung: 0, rungClass: 'vpn', strategy: 'vpn_pool', fails: 2, broken: false },
          { service: 'social', state: 'VPN', node: 'nl-hy2-01', rung: 0, rungClass: 'vpn', strategy: 'vpn_pool', fails: 0, broken: false },
          { service: 'gaming-epic', state: 'PREFERRED', node: '', rung: 0, rungClass: 'zapret', strategy: 'alt-zapret', fails: 0, broken: false },
          { service: 'dev', state: 'VPN', node: 'nl-hy2-01', rung: 0, rungClass: 'vpn', strategy: 'vpn_pool', fails: 0, broken: false },
          { service: 'ru-direct', state: 'LOCKED', node: '', rung: 0, rungClass: 'direct', strategy: 'direct', fails: 0, broken: false },
        ],
      },
      events: [
        { time: '2026-07-26T14:28:03Z', service: 'ai', from_position: 2, to_position: 3, state: 'BROKEN', strategy_class: 'emergency', reason: 'цепочка исчерпана' },
        { time: '2026-07-26T14:24:10Z', service: 'youtube', from_position: 1, to_position: 0, state: 'VPN', strategy_class: 'vpn', reason: 'предпочтительный снова здоров' },
        { time: '2026-07-26T14:19:52Z', service: 'discord', from_position: 1, to_position: 0, state: 'PREFERRED', strategy_class: 'zapret', reason: 'десинк заработал' },
      ],
    }
  }
</script>

<div class="app">
  <header>
    <div class="brand">
      <span class="mark">◗</span>
      <span>Lotsman</span>
      <button class="host">local ▾</button>
    </div>

    <div class="engines">
      {#if report}
        {#each report.engines as e}
          <span class="engine">
            <span class="dot" class:ok={e.state === 'running'} class:off={e.state !== 'running'}></span>
            {e.name}
          </span>
        {/each}
      {/if}
    </div>

    <div class="verdict {verdictKind(report && report.verdict)}">
      {#if report}
        {verdictText[report.verdict.state] || report.verdict.state}
        <span class="counts">{report.verdict.working}/{report.verdict.total}</span>
      {:else if error}
        сервис недоступен
      {:else}
        …
      {/if}
    </div>
  </header>

  <nav>
    {#each [['dashboard', 'Обзор'], ['nodes', 'Ноды'], ['subs', 'Подписки'], ['advanced', 'Ещё']] as [id, label]}
      <button class:active={tab === id} on:click={() => (tab = id)}>{label}</button>
    {/each}
  </nav>

  <main>
    {#if error && !report}
      <div class="banner red">Сервис недоступен — {error}</div>
    {/if}

    {#if tab === 'dashboard' && report}
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
          <div class="v small">
            {report.verdict.working} работают · {report.verdict.failing} подбор · {report.verdict.broken} не работает
          </div>
        </div>
      </div>

      {#if problems.length}
        <section>
          <h2>Требуют внимания</h2>
          <div class="tiles">
            {#each problems as s}
              <div class="tile {tileKind(s)}">
                <div class="tile-head">
                  <span class="glyph">{tileGlyph(s)}</span>
                  <span class="name">{s.service}</span>
                </div>
                <div class="where">{s.broken ? 'нет живых нод' : tileWhere(s)}</div>
                <button class="fix" on:click={() => recheck(s.service)}>перепроверить</button>
              </div>
            {/each}
          </div>
        </section>
      {/if}

      <section>
        <h2>Работают · {working.length}</h2>
        <div class="rows">
          {#each working as s}
            <div class="row">
              <span class="glyph {tileKind(s)}">{tileGlyph(s)}</span>
              <span class="name">{s.service}</span>
              <span class="where">{tileWhere(s)}</span>
              <button class="ghost" on:click={() => recheck(s.service)}>перепроверить</button>
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
    {:else if tab !== 'dashboard'}
      <div class="stub">Экран «{tab}» — каркас, наполним следующим.</div>
    {/if}
  </main>
</div>

<style>
  .app {
    display: flex;
    flex-direction: column;
    min-height: 100vh;
  }
  header {
    display: flex;
    align-items: center;
    gap: 20px;
    padding: 12px 18px;
    background: var(--panel-2);
    border-bottom: 1px solid var(--line);
  }
  .brand {
    display: flex;
    align-items: center;
    gap: 10px;
    font-weight: 600;
  }
  .brand .mark {
    color: var(--accent);
    font-size: 18px;
  }
  .host {
    margin-left: 8px;
    background: transparent;
    border: 1px solid var(--line);
    color: var(--ink-dim);
    border-radius: 8px;
    padding: 2px 8px;
    font-size: 12px;
  }
  .engines {
    display: flex;
    gap: 14px;
    margin-left: auto;
    font-size: 12px;
    color: var(--ink-dim);
  }
  .engine {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--ink-faint);
  }
  .dot.ok {
    background: var(--ok);
  }
  .dot.off {
    background: var(--red);
  }
  .verdict {
    font-size: 13px;
    color: var(--ink-dim);
    padding-left: 18px;
    border-left: 1px solid var(--line);
  }
  .verdict .counts {
    font-variant-numeric: tabular-nums;
    color: var(--ink);
    margin-left: 6px;
  }
  .verdict.ok .counts {
    color: var(--ok);
  }
  .verdict.amber .counts {
    color: var(--amber);
  }
  .verdict.red .counts {
    color: var(--red);
  }

  nav {
    display: flex;
    gap: 4px;
    padding: 8px 14px 0;
    border-bottom: 1px solid var(--line);
    background: var(--panel-2);
  }
  nav button {
    background: transparent;
    border: none;
    color: var(--ink-dim);
    padding: 8px 14px;
    border-bottom: 2px solid transparent;
  }
  nav button.active {
    color: var(--ink);
    border-bottom-color: var(--accent);
  }

  main {
    padding: 18px;
    display: flex;
    flex-direction: column;
    gap: 20px;
  }
  h2 {
    font-size: 12px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--ink-faint);
    margin: 0 0 10px;
  }

  .stats {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 12px;
  }
  .stat {
    background: var(--panel);
    border: 1px solid var(--line);
    border-radius: var(--radius);
    padding: 14px 16px;
  }
  .stat .k {
    font-size: 11px;
    color: var(--ink-faint);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .stat .v {
    font-size: 26px;
    font-variant-numeric: tabular-nums;
    margin-top: 4px;
  }
  .stat .v.small {
    font-size: 15px;
  }
  .stat .sub {
    color: var(--ink-faint);
    font-size: 12px;
  }

  .tiles {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
    gap: 12px;
  }
  .tile {
    background: var(--panel);
    border: 1px solid var(--line);
    border-left: 3px solid var(--line);
    border-radius: var(--radius);
    padding: 12px 14px;
  }
  .tile.red {
    border-left-color: var(--red);
  }
  .tile.amber {
    border-left-color: var(--amber);
  }
  .tile-head {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .tile .glyph {
    font-size: 14px;
  }
  .tile.red .glyph {
    color: var(--red);
  }
  .tile.amber .glyph {
    color: var(--amber);
  }
  .name {
    font-weight: 600;
  }
  .where {
    color: var(--ink-dim);
    font-size: 12px;
    margin: 6px 0 10px;
  }
  .fix {
    background: transparent;
    border: 1px solid var(--line);
    color: var(--ink);
    border-radius: 8px;
    padding: 4px 10px;
    font-size: 12px;
  }

  .rows {
    display: flex;
    flex-direction: column;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 8px 4px;
    border-bottom: 1px solid var(--panel-2);
  }
  .row .glyph.ok {
    color: var(--ok);
  }
  .row .glyph.dim {
    color: var(--ink-faint);
  }
  .row .where {
    margin: 0;
    color: var(--ink-faint);
  }
  .row .name {
    min-width: 120px;
  }
  .row .ghost {
    margin-left: auto;
    background: transparent;
    border: none;
    color: var(--accent);
    font-size: 12px;
    opacity: 0.7;
  }
  .row .ghost:hover {
    opacity: 1;
  }

  .two-col {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 20px;
  }
  .events,
  .subs {
    display: flex;
    flex-direction: column;
  }
  .event {
    display: flex;
    gap: 10px;
    padding: 6px 0;
    border-bottom: 1px solid var(--panel-2);
    font-size: 13px;
  }
  .event .t {
    color: var(--ink-faint);
    font-variant-numeric: tabular-nums;
  }
  .event .es {
    color: var(--ink);
    min-width: 90px;
  }
  .event .er {
    color: var(--ink-dim);
  }

  .subrow {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 8px 0;
    border-bottom: 1px solid var(--panel-2);
  }
  .subrow.warn .meta {
    color: var(--amber);
  }
  .subrow .meta {
    color: var(--ink-dim);
    font-size: 12px;
  }
  .subrow .bytes {
    margin-left: auto;
    color: var(--ink-faint);
    font-size: 12px;
    font-variant-numeric: tabular-nums;
  }

  .banner {
    padding: 10px 14px;
    border-radius: var(--radius);
    border: 1px solid var(--line);
  }
  .banner.red {
    border-color: var(--red);
    color: var(--red);
  }
  .muted {
    color: var(--ink-faint);
    font-size: 13px;
  }
  .stub {
    color: var(--ink-faint);
    padding: 40px;
    text-align: center;
  }
</style>
