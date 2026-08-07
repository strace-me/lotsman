<script>
  import { onMount, onDestroy } from 'svelte'
  import Sextant from './lib/Sextant.svelte'
  import Dashboard from './lib/Dashboard.svelte'
  import Nodes from './lib/Nodes.svelte'
  import Rules from './lib/Rules.svelte'
  import Advanced from './lib/Advanced.svelte'
  import Config from './lib/Config.svelte'
  import { mock } from './lib/mock.js'

  let report = null
  let events = []
  // This window's OWN build, asked once. The bar shows the SERVICE's version
  // because that is what a reader needs first, but the GUI is the one target
  // scripts/build.sh does not build — it needs wails and a webkit toolchain — so
  // it is built by hand and drifts. On 2026-08-07 the window was two commits
  // behind its service and the only tell was a stale hint sentence in a
  // screenshot. Now the window says so itself.
  let ownVersion = ''
  // "v7.1 (8e128fe, 2026-08-07)" -> "v7.1 · 8e128fe". Two commits at the same tag
  // are two different builds, and the bar has to be able to tell them apart.
  function buildLabel(v) {
    if (!v) return '—'
    const tag = v.split(' ')[0]
    const m = v.match(/\(([^,)]+)/)
    return m ? tag + ' · ' + m[1] : tag
  }
  $: serviceVersion = (report && report.version) || ''
  $: stale =
    ownVersion && serviceVersion && ownVersion.split(' ')[0] !== serviceVersion.split(' ')[0]
  let error = ''
  let tab = 'dashboard'
  let timer

  // Wails injects window.go.main.App at runtime. In a plain `vite dev` browser it
  // is absent, so fall back to mock data — the skeleton still renders + designs.
  //
  // And it SAYS SO. Without the banner below this window renders invented rules,
  // invented nodes and an invented config as though it had read them off the
  // machine: mock youtube carries three domains where the real one carries seven,
  // and nothing on screen distinguished the two. A UI asserting a state it never
  // observed is the same defect the daemon keeps being audited for.
  const backend =
    (typeof window !== 'undefined' && window.go && window.go.main && window.go.main.App) || null
  const demo = !backend

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
    if (backend && backend.OwnVersion) {
      backend
        .OwnVersion()
        .then((v) => (ownVersion = v || ''))
        .catch(() => {})
    }
    timer = setInterval(load, 2000)
  })
  onDestroy(() => {
    clearInterval(timer)
    window.removeEventListener('keydown', onZoomKey)
    window.removeEventListener('wheel', onZoomWheel)
  })

  // Naming follows what the things ARE, which the owner had to point out: a
  // "service" here used to mean both the three PROCESSES Lotsman runs and the
  // routing rules it steers, in the same document. Rules are lists of addresses
  // plus what to do with them; services are lotsman/sing-box/nfqws; nodes are
  // concrete exits; pools are pools.
  const tabs = [
    ['dashboard', 'Обзор'],
    ['rules', 'Правила'],
    ['nodes', 'Ноды'],
    ['config', 'Конфигурация'],
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

    <!-- The verdict used to live here. It is on the Обзор tile already, and the
         same number in two places is one place to disagree with itself. What the
         bar cannot say anywhere else is WHICH BUILD is answering. -->
    <div class="verdict">
      {#if report}
        <!-- Tag AND commit. Showing the tag alone was fine while `git describe`
             produced v7.0-41-g1f49f35 — the count and the revision were IN the tag
             string. At an exact tag it produces just "v7.1", and the commit lives
             only in the parenthetical, so stripping it left the bar unable to say
             which build was answering: exactly what this field is for. The date
             stays on hover; the bar is not where you read one. -->
        <span class="build" title="версия службы, отвечающей этому окну: {report.version || 'неизвестна'}"
          >{buildLabel(report.version)}</span>
        {#if stale}
          <!-- Only when they DISAGREE. Printing both always would make the ordinary
               case look like a fault, which is how a real one stops being noticed. -->
          <span
            class="build drift"
            title="окно собрано отдельно от службы и отстало: GUI {ownVersion}, служба {serviceVersion}. Пересобери GUI: npm run build, затем wails build."
            >окно {ownVersion.split(' ')[0]}</span
          >
        {/if}
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
    {#if demo}
      <div class="banner amber">
        Демо-данные. Это окно не подключено к службе, всё ниже — выдумка для отладки
        вёрстки, а не состояние машины.
      </div>
    {/if}
    {#if error && !report && tab !== 'config'}
      <div class="banner red">Сервис недоступен — {error}</div>
    {/if}
    {#if tab === 'config'}
      <Config {backend} drifted={report && report.domain_lists_drifted} />
    {:else if tab === 'rules'}
      <Rules {backend} {report} />
    {:else if report}
      {#if tab === 'dashboard'}
        <Dashboard {report} {events} onRecheck={recheck} onToggle={setEnabled} />
      {:else if tab === 'nodes'}
        <Nodes {report} />
      {:else if tab === 'advanced'}
        <Advanced {report} onStop={stop} />
      {/if}
    {/if}
  </main>
</div>
