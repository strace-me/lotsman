<script>
  import { tileKind, tileGlyph, tileWhere, tileRequested, fmtBytes, fmtTime, fmtAgo } from './format.js'

  export let report
  export let events = []
  export let onRecheck = () => {}
  export let onToggle = () => {}

  // These are RULES — a list of addresses and what to do with them. The three
  // SERVICES are lotsman, sing-box and nfqws, and they live in the header.
  //
  // ONE list, in the order the daemon returns, which is the order the rules are
  // MATCHED in. It used to be three sections — «Требуют внимания» / «Подбирает» /
  // «Работают» — and a rule changed section the moment its state changed, so the
  // row you were reading moved out from under the cursor and everything below it
  // shifted. The state belongs ON the row, not in which box the row lives in.
  //
  // «Цепочка исчерпана» still means LOTSMAN HAS RUN OUT OF MOVES and the operator
  // has to act; a rule merely failing a probe is Lotsman WORKING, which is why it
  // is amber and carries no verb. Measured the day it mattered: Discord carried
  // 3.5 MB of voice while its probe timed out, and the panel called it a problem.
  $: rules = report.services || []
  $: disabled = report.disabled || []
  $: notices = report.notices || []

  // Recomputed on every poll (report is a fresh object each time), so the "checked
  // N ago" column ticks without a timer of its own.
  $: now = report ? Date.now() : 0

  // A recheck is pending until a probe has COMPLETED since the click. We keep the
  // timestamp seen at click time and wait for it to advance — the feedback then
  // comes from the probe running, not from the button having been pressed. The
  // deadline is the honest bound: the daemon may have dropped the request because
  // one was already queued, and an indicator that spun forever would be a lie.
  let pending = {}
  function recheck(s) {
    pending[s.service] = { at: s.lastProbeMs || 0, until: Date.now() + 15000 }
    pending = pending
    onRecheck(s.service)
  }
  // Derived, not computed in the template: a helper that mutated `pending` while
  // rendering would be writing state from inside the render it feeds.
  $: waiting = new Set(
    rules
      .filter((s) => {
        const p = pending[s.service]
        return p && (s.lastProbeMs || 0) <= p.at && now <= p.until
      })
      .map((s) => s.service)
  )
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
    <!-- `kind` is set ONLY for a modem — netid leaves it empty for Wi-Fi and
         Ethernet on purpose — so it used to render a dash as the tile's headline
         while the one useful fact, the interface, sat underneath in small type.
         Show what is known: the interface, and the kind only when there is one. -->
    <div class="v small">
      {report.network.iface || report.network.id || 'не определена'}{report.network.roaming ? ' · роуминг' : ''}
    </div>
    <div class="sub">
      {#if report.network.kind === 'cellular'}мобильная{#if report.network.carrier} · {report.network.carrier}{/if}
      {:else}отпечаток {report.network.id || '—'}{/if}
    </div>
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

<section class="rules-section">
  <div class="row-head">
    <h2>Правила · {rules.length}</h2>
    <span class="muted order-note">в порядке совпадения, как в файрволе</span>
  </div>
  <div class="rows">
    {#each rules as s (s.service)}
      <div class="row" class:attention={s.broken}>
        <!-- Fixed-width so a glyph change cannot shift the name beside it. -->
        <span class="glyph {tileKind(s)}" class:spin={!s.broken && s.fails > 0}>{tileGlyph(s)}</span>
        <span class="name">{s.service}</span>
        <span class="where" title={[tileWhere(s), tileRequested(s)].filter(Boolean).join(' · ')}>
          {tileWhere(s)}{#if tileRequested(s)} <span class="mismatch">({tileRequested(s)})</span>{/if}
          {#if s.broken}<span class="red"> · цепочка исчерпана</span>
          {:else if s.fails > 0}<span class="dim"> · проба не прошла {s.fails}×</span>{/if}
        </span>
        <span class="checked" class:waiting={waiting.has(s.service)}>
          {#if waiting.has(s.service)}проверяю…{:else}{fmtAgo(s.lastProbeMs, now)}{/if}
        </span>
        <button class="ghost" on:click={() => recheck(s)} disabled={waiting.has(s.service)}>перепроверить</button>
        <button class="ghost" on:click={() => onToggle(s.service, false)}>выключить</button>
      </div>
    {/each}

    <!-- Kept in the same list, at the end: a rule that is off on purpose and a rule
         that has gone missing look identical otherwise, and the operator needs to
         tell them apart without opening the config. -->
    {#each disabled as name (name)}
      <div class="row off">
        <span class="glyph dim">○</span>
        <span class="name">{name}</span>
        <span class="where">не проверяется и не маршрутизируется</span>
        <span class="checked"></span>
        <button class="ghost" on:click={() => onToggle(name, true)}>включить</button>
      </div>
    {/each}
  </div>
</section>

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

<style>
  .rules-section .row-head {
    align-items: baseline;
    gap: 12px;
  }
  .order-note {
    font-size: 11px;
  }
  /* A rule whose chain is exhausted is the one case the operator must act on, so
     it is marked in place rather than moved to a section of its own. */
  .row.attention {
    border-left: 2px solid var(--red);
    padding-left: 8px;
  }
  /* The подбор glyph turns. Static, it read as a state rather than as work in
     progress, and the whole point is that Lotsman is busy and no one need act. */
  .glyph.spin {
    display: inline-block;
    animation: turn 1.4s linear infinite;
  }
  @keyframes turn {
    to {
      transform: rotate(360deg);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .glyph.spin {
      animation: none;
    }
  }
  /* The row is a fixed set of columns so nothing shifts when a state changes: the
     glyph reserves its width, «где» absorbs the slack and truncates rather than
     wrapping (a wrapped line changes the row HEIGHT, which moves every row below
     it), and «проверено» has a column of its own instead of trailing the text. */
  .glyph {
    flex: none;
    width: 1.2em;
    text-align: center;
  }
  .rows .row .where {
    flex: 1 1 auto;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .rows .row .ghost {
    margin-left: 0;
  }
  .checked {
    flex: none;
    color: var(--ink-faint);
    font-size: 11px;
    font-variant-numeric: tabular-nums;
    min-width: 8.5em;
    text-align: right;
  }
  .checked.waiting {
    color: var(--accent);
  }
</style>
