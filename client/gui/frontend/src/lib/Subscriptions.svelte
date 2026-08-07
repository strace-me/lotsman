<script>
  import { fmtBytes } from './format.js'

  export let report
  export let onAdd = null // (url) => Promise<name>; null = no backend (mock/dev)

  $: subs = report.subscriptions || []
  let adding = false
  let url = ''
  let busy = false
  let error = ''
  let added = ''

  async function submit() {
    error = ''
    added = ''
    const u = url.trim()
    if (!u) {
      error = 'Вставь URL подписки'
      return
    }
    if (!onAdd) {
      error = 'Нет соединения со службой'
      return
    }
    busy = true
    try {
      // The daemon validates and applies; whatever it refuses comes back as text,
      // and it is shown verbatim rather than replaced with a friendlier guess.
      added = await onAdd(u)
      url = ''
      adding = false
    } catch (e) {
      error = String(e && e.message ? e.message : e)
    } finally {
      busy = false
    }
  }
</script>

<section>
  <div class="row-head">
    <h2>Подписки</h2>
    <button class="fix" on:click={() => (adding = !adding)}>+ Добавить</button>
  </div>

  {#if adding}
    <div class="add-sub">
      <input
        bind:value={url}
        placeholder="URL подписки (https://… или vless://…)"
        on:keydown={(e) => e.key === 'Enter' && submit()}
      />
      <button class="fix" on:click={submit} disabled={busy}>{busy ? 'Добавляю…' : 'Добавить'}</button>
      <div class="muted">
        Имя возьмётся из адреса, а теги останутся пустыми — теги решают, каким пулам достанется
        нода, и угадывать это за тебя нельзя. Поправить имя, теги и формат можно на «Конфиг».
      </div>
      {#if error}<div class="add-err">{error}</div>{/if}
    </div>
  {/if}
  {#if added}
    <div class="muted add-ok">Добавлена как <code>{added}</code> — ноды подтянутся при ближайшем обновлении.</div>
  {/if}

  {#if subs.length}
    <div class="sub-cards">
      {#each subs as s (s.name)}
        <div
          class="sub-card"
          class:warn={!s.expired && s.daysUntilExpire >= 0 && s.daysUntilExpire < 5}
          class:expired={s.expired}
        >
          <div class="sub-top">
            <span class="name">{s.name}</span>
            <span class="days" title={s.expirySource === 'manual' ? 'дата вписана вручную в конфигурации' : s.expirySource === 'provider' ? 'срок сообщил сам провайдер' : ''}>
              {#if s.expired}истекла
              {:else if s.daysUntilExpire >= 0}{Math.round(s.daysUntilExpire)} дней
              {:else}срок неизвестен{/if}
              <!-- Where the number came from, because in a month nobody remembers
                   whether it was reported or typed, and the two are not equally
                   trustworthy. -->
              {#if s.expirySource === 'manual'}<span class="src">вручную</span>
              {:else if s.expirySource === 'provider'}<span class="src">от провайдера</span>{/if}
            </span>
          </div>
          {#if s.totalBytes > 0}
            <div class="quota">
              <div class="quota-fill" style="width:{Math.min(100, Math.round(s.fractionUsed * 100))}%"></div>
            </div>
            <div class="quota-meta">
              {fmtBytes(s.usedBytes)} / {fmtBytes(s.totalBytes)} · {Math.round(s.fractionUsed * 100)}%
            </div>
          {:else}
            <div class="quota-meta">{fmtBytes(s.usedBytes)} использовано · квота не указана</div>
          {/if}
        </div>
      {/each}
    </div>
  {:else}
    <div class="empty">
      <div class="empty-title">Пока нет подписок</div>
      <div class="muted">Добавь подписку, чтобы Лоцман поднял туннель и начал вести сервисы.</div>
      <button class="cta" on:click={() => (adding = true)}>Добавить подписку</button>
    </div>
  {/if}
</section>
