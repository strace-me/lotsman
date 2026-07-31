<script>
  // Structured editor for the engine-tuning knobs: the uTLS fingerprint and multiplex
  // injected into sing-box outbounds, the fakeip DNS section, and the target sing-box
  // version that gates version-specific knobs. These are Lotsman's own passthroughs —
  // the generated sing-box config itself is owned and rewritten by the daemon, so
  // these are the supported way to tune the engine. Mutates the shared doc in place.
  export let doc
  export let touch = () => {}

  // sing-box's uTLS set; the config does not validate it, so the input stays free text.
  const fingerprints = ['chrome', 'firefox', 'safari', 'ios', 'android', 'edge', 'random', 'randomized']
  const muxProtocols = ['smux', 'yamux', 'h2mux']

  function toggleMux(on) {
    if (on) doc.Multiplex = { Enabled: true, Protocol: 'h2mux', MaxConnections: 1, MinStreams: 4, Padding: true, BrutalUp: 0, BrutalDown: 0 }
    else doc.Multiplex = null
    touch()
  }
  function toggleFakeIP(on) {
    if (on) doc.FakeIP = { Enabled: true, Inet4Range: '198.18.0.0/15', Inet6Range: '', Resolver: '' }
    else doc.FakeIP = null
    touch()
  }
</script>

<div class="row-head"><h2>Движки</h2></div>
<div class="muted eng-hint">
  Конфиг sing-box генерирует и перезаписывает сам Лоцман — править его руками бесполезно. Эти настройки —
  поддерживаемый способ его подкрутить.
</div>

<div class="cfg-item col">
  <label class="full"
    >Отпечаток TLS (utls)
    <input bind:value={doc.UTLSFingerprint} on:input={touch} list="eng-fp" placeholder="пусто = не подменять" />
  </label>
  <div class="muted eng-note">Маскирует TLS-отпечаток клиента под браузерный. Пустое — не трогать.</div>
  <datalist id="eng-fp">{#each fingerprints as f}<option value={f}></option>{/each}</datalist>

  <label class="full"
    >Версия sing-box
    <input bind:value={doc.SingboxVersion} on:input={touch} placeholder="напр. 1.13.14 — пусто = базовая" />
  </label>
  <div class="muted eng-note">Ограничивает генератор тем, что умеет установленный бинарь (напр. DNS-блок 1.12+).</div>
</div>

<div class="cfg-item col">
  <label class="chk">
    <input type="checkbox" checked={!!doc.Multiplex} on:change={(e) => toggleMux(e.target.checked)} />
    Мультиплекс
  </label>
  <div class="muted eng-note">
    Один долгоживущий коннект вместо пачки параллельных рукопожатий — снимает срабатывание DPI по числу сессий.
  </div>
  {#if doc.Multiplex}
    <div class="cfg-fields">
      <label
        >Протокол
        <select bind:value={doc.Multiplex.Protocol} on:change={touch}>
          {#each muxProtocols as p}<option value={p}>{p}</option>{/each}
        </select>
      </label>
      <label>Макс. коннектов<input type="number" min="0" bind:value={doc.Multiplex.MaxConnections} on:input={touch} /></label>
      <label>Мин. потоков<input type="number" min="0" bind:value={doc.Multiplex.MinStreams} on:input={touch} /></label>
    </div>
    <label class="chk"><input type="checkbox" bind:checked={doc.Multiplex.Padding} on:change={touch} /> padding</label>
    <div class="cfg-fields">
      <label>Brutal ↑ (Мбит/с)<input type="number" min="0" bind:value={doc.Multiplex.BrutalUp} on:input={touch} /></label>
      <label>Brutal ↓ (Мбит/с)<input type="number" min="0" bind:value={doc.Multiplex.BrutalDown} on:input={touch} /></label>
    </div>
  {/if}
</div>

<div class="cfg-item col">
  <label class="chk">
    <input type="checkbox" checked={!!doc.FakeIP} on:change={(e) => toggleFakeIP(e.target.checked)} />
    fakeip
  </label>
  <div class="muted eng-note">
    Синтетические IP на домены: приложение не делает системный lookup, который можно отравить. Для протоколов,
    что ходят по IP, а не по имени.
  </div>
  {#if doc.FakeIP}
    <div class="cfg-fields">
      <label>inet4<input bind:value={doc.FakeIP.Inet4Range} on:input={touch} placeholder="198.18.0.0/15" /></label>
      <label>inet6<input bind:value={doc.FakeIP.Inet6Range} on:input={touch} placeholder="fc00::/18" /></label>
    </div>
    <label class="full">Резолвер<input bind:value={doc.FakeIP.Resolver} on:input={touch} placeholder="https://1.1.1.1/dns-query" /></label>
  {/if}
</div>

<style>
  .eng-hint {
    margin: 0.15rem 0 0.6rem;
  }
  .eng-note {
    margin: -0.2rem 0 0.5rem;
    font-size: 0.82rem;
  }
  .chk {
    flex-direction: row;
    align-items: center;
    gap: 0.4rem;
  }
</style>
