<script>
  import { onDestroy, onMount } from 'svelte'
  import {
    StartSimulation,
    StopSimulation,
    IsSimulationRunning,
    GetAPIs,
    GetAPIDefaults,
    UpdateAPIConfig,
    EventsOn,
    EventsOff,
    OnReconnect,
  } from './api.js'
  import ApiTile from './ApiTile.svelte'

  export let summary

  let simRunning = false
  let simError = ''
  let apis = []
  let defaultsByName = {}

  function clone(config) {
    return JSON.parse(JSON.stringify(config))
  }

  async function load() {
    try {
      const [current, defaults] = await Promise.all([GetAPIs(), GetAPIDefaults()])
      apis = current
      defaultsByName = Object.fromEntries(defaults.map((a) => [a.name, clone(a)]))
      simRunning = await IsSimulationRunning()
    } catch (err) {
      simError = String(err)
    }
  }

  let offReconnect = () => {}
  onMount(() => {
    load()
    // Events sent while the stream was down are gone, so ask for the state again.
    offReconnect = OnReconnect(load)
  })

  async function pushIfRunning(cfg) {
    if (!simRunning) return
    try {
      await UpdateAPIConfig(cfg)
    } catch (err) {
      simError = String(err)
    }
  }

  async function resetAll() {
    apis = apis.map((api) => clone(defaultsByName[api.name] ?? api))
    for (const cfg of apis) {
      await pushIfRunning(cfg)
    }
  }

  async function resetOne(name) {
    const def = defaultsByName[name]
    if (!def) return
    const idx = apis.findIndex((a) => a.name === name)
    if (idx === -1) return
    const reset = clone(def)
    apis[idx] = reset
    await pushIfRunning(reset)
  }

  EventsOn('simulator:error', (payload) => {
    simError = `${payload.api}: ${payload.error}`
  })
  // Another tab started or stopped it.
  EventsOn('simulator:state', (payload) => {
    simRunning = payload.running
  })

  onDestroy(() => {
    offReconnect()
    EventsOff('simulator:error', 'simulator:state')
  })

  async function toggleSimulation() {
    simError = ''
    try {
      if (simRunning) {
        await StopSimulation()
        simRunning = false
      } else {
        await StartSimulation(apis)
        simRunning = true
      }
    } catch (err) {
      simError = String(err)
    }
  }

  async function handleConfigChange(event) {
    await pushIfRunning(event.detail)
  }
</script>

<main>
  <header>
    <div>
      <h1>{summary.clusterName}</h1>
      <p class="subtitle">{summary.provider}/{summary.region} · topic {summary.topicName}</p>
    </div>
    <div class="actions">
      <button on:click={toggleSimulation}>{simRunning ? 'Stop' : 'Start'} simulation</button>
      <button on:click={resetAll}>Reset all to defaults</button>
    </div>
  </header>

  {#if simError}
    <div class="error">{simError}</div>
  {/if}
  <div class="grid">
    {#each apis as api (api.name)}
      <ApiTile
        config={api}
        otherNames={apis.map((a) => a.name).filter((n) => n !== api.name)}
        on:change={handleConfigChange}
        on:reset={() => resetOne(api.name)}
      />
    {/each}
  </div>
</main>

<style>
  main {
    max-width: 1400px;
    margin: 0 auto;
    padding: 1.5rem;
  }

  header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    margin-bottom: 1rem;
  }

  h1 {
    margin: 0;
    font-size: 1.2rem;
  }

  .subtitle {
    margin: 0.2rem 0 0;
    font-size: 0.8rem;
    opacity: 0.7;
    font-family: monospace;
  }

  .error {
    color: #ff6b6b;
    font-size: 0.9rem;
    margin-bottom: 1rem;
  }

  .actions {
    display: flex;
    gap: 0.75rem;
  }

  .actions button {
    padding: 0.6rem 1rem;
    border-radius: 4px;
    border: none;
    cursor: pointer;
    font-weight: 600;
  }

  .actions button {
    background: #4a9eff;
    color: white;
  }


  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
    gap: 1rem;
  }


</style>
