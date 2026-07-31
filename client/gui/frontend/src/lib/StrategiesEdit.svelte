<script>
  // Structured editor for doc.Strategies — custom desync recipes added on top of the
  // builtin catalog. This is the nfqws engine's user-facing knob: the brain picks
  // among catalog + these by what the KB has learned works. Args are edited one per
  // line rather than as a single string, so an arg containing spaces stays one arg.
  export let doc
  export let touch = () => {}

  const classes = ['zapret', 'byedpi', 'vpn', 'direct', 'emergency']

  function add() {
    ;(doc.Strategies ||= []).push({ ID: '', Class: 'zapret', NFQWSArgs: [], BlockTypes: [], Notes: '' })
    touch()
  }
  function remove(i) {
    doc.Strategies.splice(i, 1)
    touch()
  }
  const argsText = (s) => (s.NFQWSArgs || []).join('\n')
  function setArgs(s, v) {
    s.NFQWSArgs = v.split('\n').map((x) => x.trim()).filter(Boolean)
    touch()
  }
  const typesText = (s) => (s.BlockTypes || []).join(', ')
  function setTypes(s, v) {
    s.BlockTypes = v.split(',').map((x) => x.trim()).filter(Boolean)
    touch()
  }
</script>

<div class="row-head">
  <h2>Стратегии</h2>
  <button class="fix" on:click={add}>+ Стратегия</button>
</div>
<div class="muted str-hint">
  Свои рецепты десинка поверх встроенного каталога. Лоцман сам выбирает из них по тому, что показала проверка —
  вручную назначать не нужно.
</div>

{#if doc.Strategies && doc.Strategies.length}
  <div class="cfg-list">
    {#each doc.Strategies as s, i (i)}
      <div class="cfg-item col">
        <div class="cfg-fields">
          <label>ID<input bind:value={s.ID} on:input={touch} placeholder="my-split-tls" /></label>
          <label
            >Класс
            <select bind:value={s.Class} on:change={touch}>
              {#each classes as c}<option value={c}>{c}</option>{/each}
            </select>
          </label>
          <button class="del" on:click={() => remove(i)} title="Удалить">✕</button>
        </div>
        <label class="full"
          >Аргументы nfqws (по одному в строке)
          <textarea class="str-args" rows="4" spellcheck="false" value={argsText(s)} on:input={(e) => setArgs(s, e.target.value)}
            placeholder="--dpi-desync=fake,multisplit&#10;--dpi-desync-fooling=md5sig"></textarea>
        </label>
        <label class="full">Типы блокировки<input value={typesText(s)} on:input={(e) => setTypes(s, e.target.value)} placeholder="rst, dpi" /></label>
        <label class="full">Заметка<input bind:value={s.Notes} on:input={touch} placeholder="откуда рецепт, что чинит" /></label>
      </div>
    {/each}
  </div>
{:else}
  <div class="muted">Своих стратегий нет — работает встроенный каталог.</div>
{/if}

<style>
  .str-hint {
    margin: 0.15rem 0 0.6rem;
  }
  .str-args {
    width: 100%;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.85rem;
    resize: vertical;
  }
</style>
