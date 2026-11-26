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

<div class="tile" class:error={config.gatewayError}>
  <div class="tile-header">
    <h3>{config.name}</h3>
    <button class="reset" on:click={() => dispatch('reset')} title="Reset this API to its defaults">Reset</button>
  </div>
  <p class="path">{config.path}</p>

  <div class="stats">
    <span class="rate">{(stats?.eventsPerSec ?? 0).toFixed(1)} evt/s</span>
    <span class="duration">{stats?.lastDurationMs ?? '-'} ms</span>
  </div>
  <div class="status-counts">
    {#each ['200', '403', '404', '500', '502'] as code}
      {#if statusCounts[code]}
        <span class="status-badge status-{code}">{code}: {statusCounts[code]}</span>
      {/if}
    {/each}
  </div>

  <label>
    Rate ({config.rate.toFixed(1)}/s)
    <input type="range" min="0" max="20" step="0.5" bind:value={config.rate} on:input={emitChange} />
  </label>
  <label>
    Latency ({config.latencyMs}ms)
    <input type="range" min="1" max="1000" step="1" bind:value={config.latencyMs} on:input={emitChange} />
  </label>
  <label>
    500s ({config.pct500}%)
    <input type="range" min="0" max="100" step="1" bind:value={config.pct500} on:input={emitChange} />
  </label>
  <label>
    403s ({config.pct403}%)
    <input type="range" min="0" max="100" step="1" bind:value={config.pct403} on:input={emitChange} />
  </label>
  <label>
    404s ({config.pct404}%)
    <input type="range" min="0" max="100" step="1" bind:value={config.pct404} on:input={emitChange} />
  </label>

  <label class="toggle">
    <input type="checkbox" bind:checked={config.gatewayError} on:change={emitChange} />
    Gateway error (upstream unreachable)
  </label>

  {#if otherNames.length > 0}
    <details class="deps">
      <summary>Depends on ({(config.dependsOn ?? []).length})</summary>
      <div class="dep-list">
        {#each otherNames as name}
          <label class="dep-item">
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
  .tile {
    border: 1px solid rgba(255, 255, 255, 0.15);
    border-radius: 8px;
    padding: 1rem;
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    background: rgba(255, 255, 255, 0.03);
  }

  .tile.error {
    border-color: #ff5c5c;
  }

  .tile-header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 0.5rem;
  }

  h3 {
    margin: 0;
    font-size: 0.85rem;
    word-break: break-word;
  }

  .reset {
    flex-shrink: 0;
    font-size: 0.68rem;
    padding: 0.15rem 0.4rem;
    border-radius: 3px;
    border: 1px solid rgba(255, 255, 255, 0.25);
    background: transparent;
    color: inherit;
    cursor: pointer;
    opacity: 0.75;
  }

  .reset:hover {
    opacity: 1;
  }

  .path {
    margin: 0;
    font-size: 0.75rem;
    opacity: 0.6;
    font-family: monospace;
  }

  .stats {
    display: flex;
    justify-content: space-between;
    font-family: monospace;
    font-size: 0.85rem;
  }

  .status-counts {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem;
    min-height: 1.2rem;
  }

  .status-badge {
    font-size: 0.7rem;
    padding: 0.1rem 0.4rem;
    border-radius: 3px;
    background: rgba(255, 255, 255, 0.1);
  }

  .status-badge.status-500,
  .status-badge.status-502 {
    background: rgba(255, 92, 92, 0.3);
  }

  .status-badge.status-403,
  .status-badge.status-404 {
    background: rgba(255, 200, 80, 0.3);
  }

  label {
    display: flex;
    flex-direction: column;
    font-size: 0.75rem;
    gap: 0.15rem;
  }

  label.toggle {
    flex-direction: row;
    align-items: center;
    gap: 0.4rem;
  }

  .deps summary {
    font-size: 0.75rem;
    cursor: pointer;
    opacity: 0.8;
  }

  .dep-list {
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
    margin-top: 0.4rem;
    max-height: 120px;
    overflow-y: auto;
  }

  .dep-item {
    flex-direction: row;
    align-items: center;
    gap: 0.4rem;
    font-size: 0.72rem;
  }
</style>
