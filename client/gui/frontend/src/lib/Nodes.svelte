<script>
  // The fleet, as CONFIGURED. Deliberately no health colours: the client keeps no
  // per-node liveness of its own, and painting a node green because it exists is
  // the kind of unobserved claim this project keeps finding. What CAN be said
  // honestly is which pool took a node and whether that pool is kept warm — which
  // is the real answer to "what gets substituted when the one in use goes away".
  export let report

  $: fleet = report.fleet || {}
  $: nodes = fleet.nodes || []
  $: pools = Object.entries(fleet.pools || {}).sort(([a], [b]) => a.localeCompare(b))
  $: warm = nodes.filter((n) => n.warm)
  // Grouped by the subscription that supplied them: one dead provider should read
  // as a block, not as gaps scattered through a flat list.
  $: groups = Object.entries(
    nodes.reduce((m, n) => ((m[n.source || '—'] ||= []).push(n), m), {}),
  ).sort(([a], [b]) => a.localeCompare(b))

  const where = (n) => [n.protocol, n.server].filter(Boolean).join(' · ')
</script>

<section>
  <h2>Пул нод</h2>
  <div class="stats">
    <div class="stat">
      <div class="k">Всего</div>
      <div class="v">{fleet.total ?? '—'}</div>
      <!-- Both numbers matter, for different reasons: a provider may advertise
           many exits behind few front-ends, and one blocked address then takes a
           large share of the fleet with it. -->
      <div class="sub">{fleet.servers ? `на ${fleet.servers} адресах` : ''}</div>
    </div>
    {#each pools as [name, n] (name)}
      <div class="stat">
        <div class="k">{name}</div>
        <div class="v" class:red={n === 0}>{n}</div>
        <div class="sub">{n === 0 ? 'пусто — выбирать не из чего' : 'нод в пуле'}</div>
      </div>
    {/each}
  </div>
</section>

{#if warm.length}
  <section>
    <h2>Прогретые · {warm.length}</h2>
    <div class="muted node-hint">
      Эти ноды пул держит постоянно опрошенными, поэтому подмена отвалившейся
      происходит сразу, а не после холодной проверки.
    </div>
    <div class="rows">
      {#each warm as n (n.server + n.name)}
        <div class="row">
          <span class="glyph ok">●</span>
          <span class="name">{n.name}</span>
          <span class="where">{where(n)}</span>
          <span class="node-pools">{(n.pools || []).join(', ')}</span>
        </div>
      {/each}
    </div>
  </section>
{/if}

{#if nodes.length}
  {#each groups as [source, list] (source)}
    <section>
      <h2>{source} · {list.length}</h2>
      <div class="rows node-table">
        {#each list as n (n.server + n.name)}
          <div class="row node-row">
            <span class="glyph dim">●</span>
            <span class="name">{n.name}</span>
            <span class="where">{where(n)}</span>
            <span class="node-pools">
              {#if (n.pools || []).length}{n.pools.join(', ')}{:else}ни в одном пуле{/if}
            </span>
            {#if n.warm}<span class="node-warm" title="пул держится прогретым">прогрет</span>{/if}
          </div>
        {/each}
      </div>
    </section>
  {/each}
{:else}
  <div class="muted callout">
    Список нод пуст. Либо подписки ещё не загружены, либо ни одна не отдала нод —
    во втором случае причина будет в журнале службы.
  </div>
{/if}

<style>
  .node-pools {
    margin-left: auto;
    font-size: 0.8rem;
    opacity: 0.75;
  }
  .node-warm {
    margin-left: 12px;
    font-size: 0.75rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    opacity: 0.7;
  }
  .node-hint {
    margin: 0.15rem 0 0.6rem;
  }
</style>
