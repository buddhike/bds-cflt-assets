<script>
  // One load (the inventory events or the queries): its rate, Start/Stop and what it is
  // doing. The rate is the only number the browser sets; the server bounds it.
  let { title, unit, load, bounds, busy, disabledReason = '', onchange, children } = $props()

  let draft = $state(null)
  const shown = $derived(draft ?? load.rate)
  const valid = $derived(Number.isInteger(shown) && shown >= bounds.min && shown <= bounds.max)
  const dirty = $derived(draft !== null && draft !== load.rate)

  async function toggle() {
    const change = { running: !load.running }
    if (valid && dirty) change.rate = shown
    await onchange(change)
    draft = null
  }
  async function apply() {
    if (!valid) return
    await onchange({ rate: shown })
    draft = null
  }
</script>

<section class="panel">
  <div class="panel-head">
    <h2>{title}</h2>
    <span class="pill" class:ok={load.running}>{load.running ? 'Running' : 'Stopped'}</span>
  </div>
  <div class="stack">
    <div class="field" class:invalid={!valid}>
      <label for={`rate-${title}`}>{unit} per second</label>
      <div class="row">
        <input
          id={`rate-${title}`}
          type="number"
          min={bounds.min}
          max={bounds.max}
          step="1"
          value={shown}
          oninput={(e) => (draft = e.currentTarget.valueAsNumber)}
          onkeydown={(e) => e.key === 'Enter' && apply()}
          style="width: 7rem"
        />
        {#if dirty}<button class="sm" disabled={!valid || busy} onclick={apply}>Apply</button>{/if}
        <span class="spacer"></span>
        <button class:primary={!load.running} class:danger={load.running} disabled={busy || (!load.running && !!disabledReason)} onclick={toggle}>
          {load.running ? 'Stop' : 'Start'}
        </button>
      </div>
      <span class="hint">{valid ? `${bounds.min} to ${bounds.max}, default ${bounds.default}. ` : `Enter a whole number from ${bounds.min} to ${bounds.max}. `}{#if !load.running && disabledReason}{disabledReason}{/if}</span>
    </div>
    {@render children?.()}
  </div>
</section>
