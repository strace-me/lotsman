<script>
  import { nodeKind, nodeStateText } from './format.js'

  export let report

  $: fleet = report.fleet || {}
  $: nodes = fleet.nodes || []
  // Group nodes by their subscription, like PassWall2's per-sub fleet list.
  $: groups = Object.entries(
    nodes.reduce((m, n) => ((m[n.sub || '—'] ||= []).push(n), m), {}),
  )
  // alive/frozen/dead is a server-side addition; show the breakdown only if present.
  $: hasBreakdown = fleet.alive != null
</script>

<section>
  <h2>Пул нод</h2>
  <div class="stats">
    <div class="stat">
      <div class="k">Всего</div>
      <div class="v">{fleet.total ?? '—'}</div>
    </div>
    {#if hasBreakdown}
      <div class="stat"><div class="k">Живы</div><div class="v ok">{fleet.alive}</div></div>
      <div class="stat"><div class="k">Заморожено</div><div class="v amber">{fleet.frozen}</div></div>
      <div class="stat"><div class="k">Мертвы</div><div class="v red">{fleet.dead}</div></div>
    {/if}
  </div>
  {#if hasBreakdown}
    <div class="fleet-bar">
      <span class="seg ok" style="flex:{fleet.alive}"></span>
      <span class="seg amber" style="flex:{fleet.frozen}"></span>
      <span class="seg red" style="flex:{fleet.dead}"></span>
    </div>
  {/if}
</section>

{#if nodes.length}
  {#each groups as [sub, list] (sub)}
    <section>
      <h2>{sub} · {list.length}</h2>
      <div class="rows node-table">
        {#each list as n (n.node)}
          <div class="row node-row">
            <span class="glyph {nodeKind(n.state)}">●</span>
            <span class="name">{n.node}</span>
            <span class="node-state {nodeKind(n.state)}">{nodeStateText[n.state] || n.state}</span>
            <span class="rtt">{n.rttMs ? n.rttMs + ' мс' : '—'}</span>
          </div>
        {/each}
      </div>
    </section>
  {/each}
{:else}
  <div class="muted callout">
    Детализация по нодам появится, когда демон начнёт отдавать список нод в
    <code>/status</code> (<code>fleet.nodes</code> + alive/frozen/dead) — это
    отложенный клиентский Ranker. Сейчас известно только общее число:
    <b>{fleet.total ?? '—'}</b>.
  </div>
{/if}
