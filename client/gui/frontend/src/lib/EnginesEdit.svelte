<script>
  // Structured editor for the engine-tuning knobs: the uTLS fingerprint and multiplex
  // injected into sing-box outbounds, the target sing-box version that gates
  // version-specific features, and the desync engine's own declaration. These are
  // Lotsman's passthroughs — the generated sing-box config itself is owned and
  // rewritten by the daemon, so these are the supported way to tune the engine.
  export let cfg
  export let touch = () => {}

  // sing-box's uTLS set; the config does not validate it, so the input stays free text.
  const fingerprints = ['chrome', 'firefox', 'safari', 'ios', 'android', 'edge', 'random', 'randomized']
  const muxProtocols = ['smux', 'yamux', 'h2mux']
  const engines = ['nfqws1', 'nfqws2']

  function toggleMux(on) {
    if (on) cfg.Multiplex = { Enabled: true, Protocol: 'h2mux', MaxConnections: 1, MinStreams: 4, Padding: true, BrutalUp: 0, BrutalDown: 0 }
    else cfg.Multiplex = null
    touch()
  }
  function ensureDesync() {
    cfg.Desync ||= { Instances: {} }
    cfg.Desync.Instances ||= {}
    return cfg.Desync
  }
  function addInstance() {
    const d = ensureDesync()
    let name = 'nfqws1-main'
    for (let i = 2; d.Instances[name]; i++) name = 'nfqws1-main-' + i
    d.Instances[name] = { Engine: 'nfqws1', Enabled: true, Capture: { TCP: ['80', '443'], UDP: ['443'] }, Connbytes: 0 }
    touch()
  }
  function removeInstance(name) {
    delete ensureDesync().Instances[name]
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
    <input bind:value={cfg.UTLSFingerprint} on:input={touch} list="eng-fp" placeholder="пусто = не подменять" />
  </label>
  <div class="muted eng-note">Маскирует TLS-отпечаток клиента под браузерный. Пустое — не трогать.</div>
  <datalist id="eng-fp">{#each fingerprints as f}<option value={f}></option>{/each}</datalist>

  <label class="full"
    >Версия sing-box
    <input bind:value={cfg.TargetVersion} on:input={touch} placeholder="напр. 1.14.1 — пусто = базовая 1.12.17" />
  </label>
  <div class="muted eng-note">
    Это ДЕКЛАРАЦИЯ, а не умолчание. Без неё генератор работает на базовой версии и молча срезает версионные фичи
    узлов — hy2-нода теряет gecko-obfs, и ничто об этом не сообщает.
  </div>
</div>

<div class="cfg-item col">
  <label class="chk">
    <input type="checkbox" checked={!!cfg.Multiplex} on:change={(e) => toggleMux(e.target.checked)} />
    Мультиплекс
  </label>
  <div class="muted eng-note">
    Один долгоживущий коннект вместо пачки параллельных рукопожатий — снимает срабатывание DPI по числу сессий.
  </div>
  {#if cfg.Multiplex}
    <div class="cfg-fields">
      <label
        >Протокол
        <select bind:value={cfg.Multiplex.Protocol} on:change={touch}>
          {#each muxProtocols as p}<option value={p}>{p}</option>{/each}
        </select>
      </label>
      <label>Макс. коннектов<input type="number" min="0" bind:value={cfg.Multiplex.MaxConnections} on:input={touch} /></label>
      <label>Мин. потоков<input type="number" min="0" bind:value={cfg.Multiplex.MinStreams} on:input={touch} /></label>
    </div>
    <label class="chk"><input type="checkbox" bind:checked={cfg.Multiplex.Padding} on:change={touch} /> padding</label>
    <div class="cfg-fields">
      <label>Brutal ↑ (Мбит/с)<input type="number" min="0" bind:value={cfg.Multiplex.BrutalUp} on:input={touch} /></label>
      <label>Brutal ↓ (Мбит/с)<input type="number" min="0" bind:value={cfg.Multiplex.BrutalDown} on:input={touch} /></label>
    </div>
  {/if}
</div>

<div class="cfg-item col">
  <div class="row-head">
    <h3>Десинк-движки</h3>
    <button class="fix" on:click={addInstance}>+ Инстанс</button>
  </div>
  <div class="muted eng-note">
    Один инстанс — это процесс + своя NFQUEUE + свой набор портов. Порт НЕ опционален: инстанс без портов
    ставит правило nft, которое не матчит ничего, то есть десинк отсутствует, а статус честно рапортует, что всё
    хорошо. Номер очереди выводится из порядка, задавать руками не нужно.
  </div>
  {#if cfg.Desync && cfg.Desync.Instances}
    {#each Object.entries(cfg.Desync.Instances) as [name, inst] (name)}
      <div class="cfg-item">
        <div class="cfg-fields">
          <label
            >Инстанс
            <input value={name} readonly />
          </label>
          <label
            >Движок
            <select bind:value={inst.Engine} on:change={touch}>
              {#each engines as e}<option value={e}>{e}</option>{/each}
            </select>
          </label>
          <label class="chk"><input type="checkbox" bind:checked={inst.Enabled} on:change={touch} /> включён</label>
          <button class="del" on:click={() => removeInstance(name)} title="Удалить">✕</button>
        </div>
        <div class="cfg-fields">
          <label class="grow"
            >Порты TCP
            <input
              value={(inst.Capture && inst.Capture.TCP ? inst.Capture.TCP : []).join(', ')}
              on:input={(e) => {
                inst.Capture ||= { TCP: [], UDP: [] }
                inst.Capture.TCP = e.target.value.split(',').map((x) => x.trim()).filter(Boolean)
                touch()
              }}
              placeholder="80, 443"
            />
          </label>
          <label class="grow"
            >Порты UDP
            <input
              value={(inst.Capture && inst.Capture.UDP ? inst.Capture.UDP : []).join(', ')}
              on:input={(e) => {
                inst.Capture ||= { TCP: [], UDP: [] }
                inst.Capture.UDP = e.target.value.split(',').map((x) => x.trim()).filter(Boolean)
                touch()
              }}
              placeholder="443, 50000-50100"
            />
          </label>
        </div>
      </div>
    {/each}
  {:else}
    <div class="muted">Инстансов нет — правила без десинк-рунгов сразу пойдут на VPN.</div>
  {/if}
</div>

<div class="cfg-item col">
  <label class="chk">
    <input
      type="checkbox"
      checked={!!(cfg.Desync && cfg.Desync.Override)}
      on:change={(e) => {
        ensureDesync()
        cfg.Desync.Override = e.target.checked
          ? { Name: '', ArgsFile: '', Dir: '/var/lib/lotsman/strategies' }
          : null
        touch()
      }}
    />
    Override: одна сборка на весь инстанс
  </label>
  <div class="muted eng-note">
    Запускает бандлupstream ДОСЛОВНО, разбирая его на правила. Так переживают сборки вроде ALT12, у которых
    профили держат РАЗНЫЕ хостлисты: при сборке на правило оба профиля получили бы один список, второй не смог бы
    сматчиться, и рецепт был бы отвергнут как самозатенение. Цена названа: обучение рецептов на правило
    ВЫКЛЮЧЕНО — один бандл на всех, выбирать не из чего, и `strategy_id` у десинк-рунга при этом игнорируется.
  </div>
  {#if cfg.Desync && cfg.Desync.Override}
    <div class="cfg-fields">
      <label>Имя<input bind:value={cfg.Desync.Override.Name} on:input={touch} placeholder="ALT12" /></label>
      <label class="grow"
        >Файл аргументов
        <input bind:value={cfg.Desync.Override.ArgsFile} on:input={touch} placeholder="/var/lib/lotsman/strategies/flowseal-alt12.args" />
      </label>
    </div>
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
