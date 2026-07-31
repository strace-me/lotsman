<script>
  // Structured editor for doc.DNS — the split-DNS block. Each server is either a
  // curated provider (pick provider + method) or a manual endpoint (type + address).
  // final/direct pick a declared server; failover lists the providers the remote
  // resolver rotates through. Mutates the shared doc in place; touch() re-marks dirty.
  export let doc
  export let touch = () => {}

  // The catalog + valid transports are package-private in pkg/config, so mirror them
  // here the way ServicesEdit mirrors its category list.
  const providers = ['cloudflare', 'quad9', 'google', 'adguard', 'mullvad']
  const methods = ['https', 'tls', 'quic', 'h3', 'tcp', 'udp']
  const manualTypes = ['local', 'udp', 'tcp', 'tls', 'https', 'quic', 'h3']
  const strategies = ['prefer_ipv4', 'prefer_ipv6', 'ipv4_only', 'ipv6_only']
  const detours = ['vpn', 'direct']

  // doc.DNS is null when the config has no dns block; init it before editing.
  function ensure() {
    doc.DNS ||= { Servers: [], Direct: '', Final: '', Strategy: '', FakeIP: false, Failover: [] }
    doc.DNS.Servers ||= []
    doc.DNS.Failover ||= []
    return doc.DNS
  }

  function addServer() {
    ensure().Servers.push({ Name: '', Provider: 'cloudflare', Method: 'https', Type: '', Address: '', ServerName: '', Path: '', Detour: 'vpn' })
    touch()
  }
  function removeServer(i) {
    doc.DNS.Servers.splice(i, 1)
    touch()
  }
  function addFailover() {
    ensure().Failover.push('cloudflare')
    touch()
  }
  function removeFailover(i) {
    doc.DNS.Failover.splice(i, 1)
    touch()
  }

  $: dns = doc.DNS
  $: names = (dns && dns.Servers ? dns.Servers : []).map((s) => s.Name).filter(Boolean)
</script>

<div class="row-head">
  <h2>DNS</h2>
  <button class="fix" on:click={addServer}>+ Резолвер</button>
</div>

{#if dns && dns.Servers && dns.Servers.length}
  <div class="cfg-list">
    {#each dns.Servers as s, i (i)}
      <div class="cfg-item col">
        <div class="cfg-fields">
          <label>Имя<input bind:value={s.Name} on:input={touch} placeholder="cf-remote" /></label>
          <label
            >Провайдер
            <select bind:value={s.Provider} on:change={touch}>
              <option value="">— вручную —</option>
              {#each providers as p}<option value={p}>{p}</option>{/each}
            </select>
          </label>
          <button class="del" on:click={() => removeServer(i)} title="Удалить">✕</button>
        </div>
        {#if s.Provider}
          <div class="cfg-fields">
            <label
              >Метод
              <select bind:value={s.Method} on:change={touch}>
                {#each methods as m}<option value={m}>{m}</option>{/each}
              </select>
            </label>
            <label
              >Detour
              <select bind:value={s.Detour} on:change={touch}>
                {#each detours as d}<option value={d}>{d}</option>{/each}
              </select>
            </label>
          </div>
        {:else}
          <div class="cfg-fields">
            <label
              >Тип
              <select bind:value={s.Type} on:change={touch}>
                {#each manualTypes as t}<option value={t}>{t}</option>{/each}
              </select>
            </label>
            {#if s.Type !== 'local'}
              <label
                >Detour
                <select bind:value={s.Detour} on:change={touch}>
                  {#each detours as d}<option value={d}>{d}</option>{/each}
                </select>
              </label>
            {/if}
          </div>
          {#if s.Type !== 'local'}
            <label class="full">Адрес<input bind:value={s.Address} on:input={touch} placeholder="dns.example.net или 1.1.1.1" /></label>
            <label class="full">SNI (server_name)<input bind:value={s.ServerName} on:input={touch} placeholder="dns.example.net" /></label>
          {/if}
        {/if}
      </div>
    {/each}
  </div>
{:else}
  <div class="muted">Пока нет резолверов. Дефолт: Cloudflare DoH через VPN + системный резолвер.</div>
{/if}

{#if dns && dns.Servers && dns.Servers.length}
  <div class="cfg-fields dns-top">
    <label
      >final (по умолчанию)
      <select bind:value={dns.Final} on:change={touch}>
        {#each names as n}<option value={n}>{n}</option>{/each}
      </select>
    </label>
    <label
      >direct (RU-прямые)
      <select bind:value={dns.Direct} on:change={touch}>
        <option value="">—</option>
        {#each names as n}<option value={n}>{n}</option>{/each}
      </select>
    </label>
    <label
      >strategy
      <select bind:value={dns.Strategy} on:change={touch}>
        <option value="">prefer_ipv4</option>
        {#each strategies as st}<option value={st}>{st}</option>{/each}
      </select>
    </label>
    <label class="chk"><input type="checkbox" bind:checked={dns.FakeIP} on:change={touch} /> fakeip</label>
  </div>

  <div class="row-head dns-fo-head">
    <h3>Failover</h3>
    <button class="fix" on:click={addFailover}>+ Провайдер</button>
  </div>
  <div class="muted dns-hint">
    Порядок провайдеров, на которые перекатывается final-резолвер, когда активный перестаёт отвечать. Требует, чтобы final был провайдерным.
  </div>
  {#if dns.Failover && dns.Failover.length}
    <div class="cfg-list">
      {#each dns.Failover as _, i (i)}
        <div class="cfg-fields dns-fo-row">
          <span class="dns-fo-ord">{i + 1}.</span>
          <select bind:value={dns.Failover[i]} on:change={touch}>
            {#each providers as p}<option value={p}>{p}</option>{/each}
          </select>
          <button class="del" on:click={() => removeFailover(i)} title="Удалить">✕</button>
        </div>
      {/each}
    </div>
  {/if}
{/if}

<style>
  .dns-top {
    margin-top: 0.75rem;
    flex-wrap: wrap;
  }
  .dns-fo-head {
    margin-top: 1rem;
  }
  .dns-fo-head h3 {
    margin: 0;
    font-size: 0.95rem;
  }
  .dns-hint {
    margin: 0.15rem 0 0.5rem;
  }
  .dns-fo-row {
    align-items: center;
  }
  .dns-fo-ord {
    opacity: 0.6;
    min-width: 1.2rem;
  }
  .chk {
    flex-direction: row;
    align-items: center;
    gap: 0.4rem;
  }
</style>
