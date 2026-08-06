<script>
  import { onMount, onDestroy } from 'svelte'
  import Sextant from './lib/Sextant.svelte'
  import Dashboard from './lib/Dashboard.svelte'
  import Nodes from './lib/Nodes.svelte'
  import Subscriptions from './lib/Subscriptions.svelte'
  import Advanced from './lib/Advanced.svelte'
  import Config from './lib/Config.svelte'
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

  // Switching a service off is a config edit the daemon applies in place, so the
  // next poll shows the result. Reload straight away rather than waiting for the
  // tick — a toggle that appears to do nothing for two seconds reads as broken.
  async function setEnabled(name, on) {
    if (!backend) return
    try {
      await backend.SetServiceEnabled(name, on)
    } catch (e) {
      error = String(e && e.message ? e.message : e)
    }
    setTimeout(load, 400)
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

  // Window zoom (Ctrl +/-/0 and Ctrl+wheel), persisted. Fractional on purpose: under
  // XWayland double-scaling (the compositor upscales AND GDK_SCALE applies) an integer
  // GDK_SCALE can only over- or under-shoot, so the user dials the comfortable size here
  // and it sticks. CSS `zoom` reflows and keeps vh/% correct (unlike transform:scale), so
  // .app's min-height:100vh still fits the window at any zoom.
  const ZOOM_KEY = 'lotsman.zoom'
  const ZOOM_MIN = 0.6
  const ZOOM_MAX = 2.0
  const ZOOM_STEP = 0.1
  let zoom = 1

  function setZoom(z) {
    zoom = Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, Math.round(z * 100) / 100))
    document.documentElement.style.zoom = String(zoom)
    try {
      localStorage.setItem(ZOOM_KEY, String(zoom))
    } catch (e) {
      // storage disabled (private mode) — zoom still applies for this session
    }
  }

  function onZoomKey(e) {
    if (!(e.ctrlKey || e.metaKey)) return
    if (e.key === '=' || e.key === '+') {
      e.preventDefault()
      setZoom(zoom + ZOOM_STEP)
    } else if (e.key === '-' || e.key === '_') {
      e.preventDefault()
      setZoom(zoom - ZOOM_STEP)
    } else if (e.key === '0') {
      e.preventDefault()
      setZoom(1)
    }
  }

  function onZoomWheel(e) {
    if (!(e.ctrlKey || e.metaKey)) return
    e.preventDefault()
    setZoom(zoom + (e.deltaY < 0 ? ZOOM_STEP : -ZOOM_STEP))
  }

  onMount(() => {
    const saved = parseFloat(localStorage.getItem(ZOOM_KEY) || '')
    setZoom(Number.isFinite(saved) && saved > 0 ? saved : 1)
    window.addEventListener('keydown', onZoomKey)
    window.addEventListener('wheel', onZoomWheel, { passive: false })
    load()
    timer = setInterval(load, 2000)
  })
  onDestroy(() => {
    clearInterval(timer)
    window.removeEventListener('keydown', onZoomKey)
    window.removeEventListener('wheel', onZoomWheel)
  })

  const tabs = [
    ['dashboard', 'Обзор'],
    ['nodes', 'Ноды'],
    ['subs', 'Подписки'],
    ['config', 'Конфиг'],
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
    {#if error && !report && tab !== 'config'}
      <div class="banner red">Сервис недоступен — {error}</div>
    {/if}
    {#if tab === 'config'}
      <Config {backend} drifted={report && report.domain_lists_drifted} />
    {:else if report}
      {#if tab === 'dashboard'}
        <Dashboard {report} {events} onRecheck={recheck} onToggle={setEnabled} />
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
