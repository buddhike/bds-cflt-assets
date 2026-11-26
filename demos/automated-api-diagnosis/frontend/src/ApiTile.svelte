<script>
  import { onDestroy, createEventDispatcher } from 'svelte'
  import { EventsOn, EventsOff } from './api.js'

  export let config // { name, path, rate, pct500, pct403, pct404, latencyMs, gatewayError, dependsOn }
  export let otherNames = [] // all other API names, for the dependency select

  const dispatch = createEventDispatcher()

  let stats = null
  let debounceHandle

  const statsEvent = `api:stats:${config.name}`
  EventsOn(statsEvent, (payload) => {
    stats = payload
  })
  onDestroy(() => EventsOff(statsEvent))

  function emitChange() {
    clearTimeout(debounceHandle)
    debounceHandle = setTimeout(() => dispatch('change', config), 150)
  }

  function toggleDependency(name) {
    const set = new Set(config.dependsOn ?? [])
    if (set.has(name)) {
      set.delete(name)
    } else {
      set.add(name)
    }
    config.dependsOn = [...set]
    emitChange()
  }

  $: statusCounts = stats?.statusCounts ?? {}
</script>

<div class="card" class:is-error={config.gatewayError}>
  <div class="card-head">
    <h3 class="mono">{config.name}</h3>
    <button class="quiet sm" on:click={() => dispatch('reset')} title="Reset this API to its defaults">Reset</button>
  </div>
  <p class="mono small faint path">{config.path}</p>
  {#if config.gatewayError}<div><span class="pill danger">Gateway error</span></div>{/if}

  <div class="stats mono">
    <span class="num">{(stats?.eventsPerSec ?? 0).toFixed(1)} evt/s</span>
    <span class="num">{stats?.lastDurationMs ?? '-'} ms</span>
  </div>
  <div class="codes">
    {#each ['200', '403', '404', '500', '502'] as code}
      {#if statusCounts[code]}
        <span class="pill num" class:warn={code === '403' || code === '404'} class:danger={code === '500' || code === '502'}>{code}: {statusCounts[code]}</span>
      {/if}
    {/each}
  </div>

  <div class="field">
    <label for="{config.name}-rate">Rate (<span class="num">{config.rate.toFixed(1)}</span>/s)</label>
    <input id="{config.name}-rate" type="range" min="0" max="20" step="0.5" bind:value={config.rate} on:input={emitChange} />
  </div>
  <div class="field">
    <label for="{config.name}-latency">Latency (<span class="num">{config.latencyMs}</span> ms)</label>
    <input id="{config.name}-latency" type="range" min="1" max="1000" step="1" bind:value={config.latencyMs} on:input={emitChange} />
  </div>
  <div class="field">
    <label for="{config.name}-500">500s (<span class="num">{config.pct500}</span>%)</label>
    <input id="{config.name}-500" type="range" min="0" max="100" step="1" bind:value={config.pct500} on:input={emitChange} />
  </div>
  <div class="field">
    <label for="{config.name}-403">403s (<span class="num">{config.pct403}</span>%)</label>
    <input id="{config.name}-403" type="range" min="0" max="100" step="1" bind:value={config.pct403} on:input={emitChange} />
  </div>
  <div class="field">
    <label for="{config.name}-404">404s (<span class="num">{config.pct404}</span>%)</label>
    <input id="{config.name}-404" type="range" min="0" max="100" step="1" bind:value={config.pct404} on:input={emitChange} />
  </div>

  <label class="check small">
    <input type="checkbox" bind:checked={config.gatewayError} on:change={emitChange} />
    Gateway error (upstream unreachable)
  </label>

  {#if otherNames.length > 0}
    <details class="deps small">
      <summary>Depends on ({(config.dependsOn ?? []).length})</summary>
      <div class="dep-list">
        {#each otherNames as name}
          <label class="check">
            <input
              type="checkbox"
              checked={(config.dependsOn ?? []).includes(name)}
              on:change={() => toggleDependency(name)}
            />
            {name}
          </label>
        {/each}
      </div>
    </details>
  {/if}
</div>

<style>
  .card {
    gap: 0.5rem;
  }

  .card.is-error {
    border-color: var(--danger);
  }

  .path {
    margin: 0;
  }

  .stats {
    display: flex;
    justify-content: space-between;
  }

  .codes {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem;
    min-height: 1.5rem;
  }

  .field {
    gap: 0.15rem;
  }

  .check {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    font-size: var(--fs-small);
  }

  .deps summary {
    cursor: pointer;
    color: var(--text-2);
  }

  .dep-list {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    margin-top: 0.4rem;
    max-height: 7.5rem;
    overflow-y: auto;
  }
</style>
