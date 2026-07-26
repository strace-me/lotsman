<script>
  import { onMount, onDestroy } from 'svelte'
  import Sextant from './lib/Sextant.svelte'
  import Dashboard from './lib/Dashboard.svelte'
  import Nodes from './lib/Nodes.svelte'
  import Subscriptions from './lib/Subscriptions.svelte'
  import Advanced from './lib/Advanced.svelte'
  import { mock } from './lib/mock.js'
  import { verdictText, verdictKind } from './lib/format.js'

  let report = null
  let events = []
  let error = ''
  let tab = 'dashboard'
  let timer

  // Wails injects window.go.main.App at runtime. In a plain `vite dev` browser it
  // is absent, so fall back to mock data — the skeleton still renders + designs.
  const backend =
    (typeof window !== 'undefined' && window.go && window.go.main && window.go.main.App) || null

  async function load() {
    try {
      if (backend) {
        report = await backend.Status()
        events = await backend.Events(30, '')
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

  async function stop() {
    if (!backend) return
    if (!confirm('Выключить сервис Lotsman? Туннель и десинк остановятся.')) return
    try {
      await backend.Stop()
    } catch (e) {
      error = String(e)
    }
  }

  onMount(() => {
    load()
    timer = setInterval(load, 2000)
  })
  onDestroy(() => clearInterval(timer))

  const tabs = [
    ['dashboard', 'Обзор'],
    ['nodes', 'Ноды'],
    ['subs', 'Подписки'],
    ['advanced', 'Ещё'],
  ]
</script>

<div class="app">
  <header>
    <div class="brand">
      <span class="mark"><Sextant size={20} /></span>
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
    {#each tabs as [id, label]}
      <button class:active={tab === id} on:click={() => (tab = id)}>{label}</button>
    {/each}
  </nav>

  <main>
    {#if error && !report}
      <div class="banner red">Сервис недоступен — {error}</div>
    {:else if report}
      {#if tab === 'dashboard'}
        <Dashboard {report} {events} onRecheck={recheck} />
      {:else if tab === 'nodes'}
        <Nodes {report} />
      {:else if tab === 'subs'}
        <Subscriptions {report} />
      {:else if tab === 'advanced'}
        <Advanced {report} onStop={stop} />
      {/if}
    {/if}
  </main>
</div>
