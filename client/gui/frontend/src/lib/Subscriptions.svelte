<script>
  import { fmtBytes } from './format.js'

  export let report

  $: subs = report.subscriptions || []
  let adding = false
  let url = ''
</script>

<section>
  <div class="row-head">
    <h2>Подписки</h2>
    {#if subs.length}
      <button class="fix" on:click={() => (adding = !adding)}>+ Добавить</button>
    {/if}
  </div>

  {#if adding}
    <div class="add-sub">
      <input bind:value={url} placeholder="URL подписки (https://… или vless://…)" />
      <button class="fix" disabled>Добавить</button>
      <div class="muted">
        Добавление подключим к control-API (серверный <code>-init</code>-визард) следующим —
        сейчас это поле формы.
      </div>
    </div>
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
            <span class="days">
              {#if s.expired}истекла
              {:else if s.daysUntilExpire >= 0}{Math.round(s.daysUntilExpire)} дней
              {:else}бессрочно{/if}
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
