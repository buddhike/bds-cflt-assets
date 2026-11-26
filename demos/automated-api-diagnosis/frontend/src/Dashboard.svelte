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
  import Notice from './Notice.svelte'

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
  <div class="page-head">
    <div>
      <h2>{summary.clusterName}</h2>
      <p class="subtitle mono faint">{summary.provider}/{summary.region} · topic {summary.topicName}</p>
    </div>
    <div class="row">
      <span class="pill" class:ok={simRunning}>{simRunning ? 'Simulation running' : 'Simulation stopped'}</span>
      <button class:primary={!simRunning} on:click={toggleSimulation}>{simRunning ? 'Stop' : 'Start'} simulation</button>
      <button on:click={resetAll}>Reset all to defaults</button>
    </div>
  </div>

  {#if simError}
    <div class="notice-row"><Notice tone="danger" alert>{simError}</Notice></div>
  {/if}
  <div class="cards">
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
    padding: 1rem 1.25rem 2rem;
  }

  .subtitle {
    margin: 0.2rem 0 0;
  }

  .notice-row {
    margin-bottom: 1rem;
  }

  .cards {
    grid-template-columns: repeat(auto-fill, minmax(15rem, 1fr));
  }
</style>
