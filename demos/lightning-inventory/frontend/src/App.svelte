<script>
  import { onMount } from 'svelte'
  import { GetState, GetLatency, ChangeEvents, ChangeQueries, LoadCatalogue } from './api.js'
  import Logo from './Logo.svelte'
  import Notice from './Notice.svelte'
  import LoadPanel from './LoadPanel.svelte'
  import Chart from './Chart.svelte'

  let state = $state(null)
  let latency = $state(null)
  let latencyNow = $state(0)
  let latencyError = $state('')
  let error = $state('')
  let busy = $state(false)
  let minutes = $state(5)

  async function refresh() {
    try {
      state = await GetState()
      error = ''
    } catch (err) {
      error = String(err)
    }
  }
  async function refreshLatency() {
    try {
      const r = await GetLatency(minutes)
      latency = r.series
      latencyNow = r.now
      latencyError = ''
    } catch (err) {
      latencyError = String(err)
    }
  }
  onMount(() => {
    refresh()
    refreshLatency()
    const a = setInterval(refresh, 1000)
    const b = setInterval(refreshLatency, 3000)
    return () => {
      clearInterval(a)
      clearInterval(b)
    }
  })

  async function act(fn) {
    busy = true
    try {
      const r = await fn()
      if (r && r.demo) state = r
      error = ''
    } catch (err) {
      error = String(err)
    } finally {
      busy = false
    }
  }
  const changeEvents = (c) => act(() => ChangeEvents(c))
  const changeQueries = (c) => act(() => ChangeQueries(c))
  const loadCatalogue = () => act(LoadCatalogue)

  const phase = $derived(state?.table.phase ?? '')
  const tablePill = $derived(
    phase === 'ACTIVE'
      ? { tone: 'ok', text: 'Active' }
      : phase === 'FAILED'
        ? { tone: 'danger', text: 'Failed' }
        : phase === 'UNAVAILABLE'
          ? { tone: 'warn', text: 'Unavailable' }
          : phase === 'DELETING'
            ? { tone: 'busy', text: 'Deleting…' }
            : { tone: 'busy', text: phase ? 'Setting up…' : 'Not there yet' },
  )
  const active = $derived(phase === 'ACTIVE')
  const cat = $derived(state?.catalogue)
  const queriesBlock = $derived(active ? '' : 'Needs the Lightning Table to be active.')
  const n = (v, d = 0) => (v ?? 0).toLocaleString(undefined, { maximumFractionDigits: d, minimumFractionDigits: d })
  const bytes = (v) => {
    const u = ['B', 'KB', 'MB', 'GB', 'TB']
    let i = 0
    while (v >= 1024 && i < u.length - 1) {
      v /= 1024
      i++
    }
    return `${n(v, i ? 1 : 0)} ${u[i]}`
  }
  const when = (s) => (s ? new Date(s * 1000).toLocaleTimeString() : 'no data yet')
  const scenarioNames = { by_sku: 'By SKU', by_locations: 'By locations' }
</script>

<div class="shell">
  <header class="topbar">
    <h1 class="brand"><Logo />Launchpad <span class="sep">/</span> Lightning inventory</h1>
    <span class="spacer"></span>
    {#if state}
      <span class="pill {tablePill.tone}" title="Lightning Table status">Table: {tablePill.text}</span>
    {/if}
  </header>

  {#if !state}
    <main>
      {#if error}<div class="gate"><Notice tone="danger" alert>{error}</Notice></div>{:else}<div class="gate"><p class="muted">Loading…</p></div>{/if}
    </main>
  {:else}
    <div class="workbench">
      <aside class="controls">
        {#if error}<Notice tone="danger" alert>{error}</Notice>{/if}
        {#if state.table.error}<Notice tone="warn" title="Lightning Table.">{state.table.error}</Notice>{/if}
        {#if phase === 'FAILED'}<Notice tone="danger" title="The Lightning Table failed.">{state.table.message || 'No reason was given.'}</Notice>{/if}

        <section class="panel">
          <div class="panel-head">
            <h2>Catalogue</h2>
            <span class="pill" class:ok={cat.status === 'loaded'} class:busy={cat.status === 'loading'}>
              {cat.status === 'loaded' ? 'Loaded' : cat.status === 'loading' ? 'Loading…' : 'Not loaded'}
            </span>
          </div>
          <div class="stack">
            <p class="muted">
              {n(cat.skus)} SKUs at {n(cat.locations)} locations, each SKU stocked at about 10: some 10 million positions, produced once to <code>{state.topic}</code>.
            </p>
            {#if cat.status !== 'not_loaded'}
              <div role="progressbar" aria-label="Catalogue loaded" aria-valuemin="0" aria-valuemax={cat.shards_total} aria-valuenow={cat.shards_done} class="bar">
                <div class="fill" style={`width: ${(100 * cat.shards_done) / cat.shards_total}%`}></div>
              </div>
              <span class="hint">{cat.shards_done} of {cat.shards_total} parts, {n(cat.records)} records sent{cat.errors ? `, ${n(cat.errors)} errors` : ''}.</span>
            {/if}
            <div class="row">
              <button class="primary" disabled={busy || !active || cat.status === 'loading'} onclick={loadCatalogue}>
                {cat.status === 'loaded' ? 'Load again' : 'Load catalogue'}
              </button>
              {#if !active}<span class="hint">Needs the Lightning Table to be active.</span>{/if}
            </div>
          </div>
        </section>

        <LoadPanel title="Inventory events" unit="Events" load={state.events} bounds={state.events_bounds} {busy} onchange={changeEvents}>
          <div class="kpis">
            <div class="kpi"><span class="kpi-label">Sent</span><span class="kpi-value">{n(state.events.per_sec, 1)}</span><span class="kpi-sub">events per second</span></div>
            <div class="kpi"><span class="kpi-label">Errors</span><span class="kpi-value">{n(state.events.errors_per_sec, 1)}</span><span class="kpi-sub" class:danger={state.events.errors_per_sec > 0}>per second</span></div>
            <div class="kpi"><span class="kpi-label">Producer tasks</span><span class="kpi-value">{state.events.workers}</span><span class="kpi-sub">rate split between them</span></div>
          </div>
        </LoadPanel>

        <LoadPanel title="Queries" unit="Queries" load={state.queries} bounds={state.queries_bounds} {busy} disabledReason={queriesBlock} onchange={changeQueries}>
          <div class="kpis">
            <div class="kpi"><span class="kpi-label">Answered</span><span class="kpi-value">{n(state.queries.per_sec, 1)}</span><span class="kpi-sub">queries per second</span></div>
            <div class="kpi"><span class="kpi-label">Errors and drops</span><span class="kpi-value">{n(state.queries.errors_per_sec, 1)}</span><span class="kpi-sub" class:danger={state.queries.errors_per_sec > 0}>per second</span></div>
            <div class="kpi"><span class="kpi-label">Query tasks</span><span class="kpi-value">{state.queries.workers}</span><span class="kpi-sub">rate split between them</span></div>
          </div>
          <div class="table-box">
            <table class="data">
              <thead><tr><th>Scenario</th><th class="r">Per second</th><th class="r">Avg rows</th><th class="r">Errors</th></tr></thead>
              <tbody>
                {#each Object.keys(scenarioNames) as sc}
                  <tr>
                    <td>{scenarioNames[sc]}</td>
                    <td class="r">{n(state.queries.by_scenario[sc]?.ok_per_sec, 1)}</td>
                    <td class="r">{n(state.queries.by_scenario[sc]?.avg_rows, 1)}</td>
                    <td class="r">{n((state.queries.by_scenario[sc]?.errors_per_sec ?? 0) + (state.queries.by_scenario[sc]?.dropped_per_sec ?? 0), 1)}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
          <span class="hint">The rate is the total, half of the queries per scenario. A few percent ask for SKUs that do not exist.</span>
        </LoadPanel>
      </aside>

      <main class="charts">
        <div class="panel-head">
          <h2>Query latency</h2>
          <div class="row" role="group" aria-label="Chart range">
            {#each [5, 15, 60] as m}
              <button class="sm" class:primary={minutes === m} aria-pressed={minutes === m} onclick={() => { minutes = m; refreshLatency() }}>{m === 60 ? '1 hour' : `${m} min`}</button>
            {/each}
          </div>
        </div>
        <p class="muted">
          Measured by the query tasks, from sending the request to reading the whole answer. Percentiles per second, computed by CloudWatch over all query tasks together. Samples appear a few seconds late.
        </p>
        {#if latencyError}<Notice tone="warn" title="Charts.">{latencyError}</Notice>{/if}
        <Chart title="By SKU: all locations of one SKU" series={latency?.by_sku} windowMinutes={minutes} now={latencyNow} />
        <Chart title="By locations: one SKU at 1 to 2000 locations" series={latency?.by_locations} windowMinutes={minutes} now={latencyNow} />

        <section class="panel">
          <div class="panel-head">
            <h3>Reported by Confluent</h3>
            <span class="tag">Metrics API, last hour</span>
          </div>
          {#if state.usage.error}<Notice tone="warn">Not available: {state.usage.error}</Notice>{/if}
          <div class="kpis">
            <div class="kpi"><span class="kpi-label">Compute</span><span class="kpi-value">{n(state.usage.ccu_minutes, 1)}</span><span class="kpi-sub">CCU minutes</span></div>
            <div class="kpi"><span class="kpi-label">Scanned by queries</span><span class="kpi-value">{bytes(state.usage.scanned_bytes)}</span><span class="kpi-sub">bytes scanned</span></div>
            <div class="kpi"><span class="kpi-label">Table size</span><span class="kpi-value">{bytes(state.usage.storage_bytes)}</span><span class="kpi-sub">storage</span></div>
          </div>
          <span class="hint">Usage, not latency. Latest data point: {when(state.usage.as_of)}. Confluent publishes these minutes late.</span>
        </section>
      </main>
    </div>
    <footer class="statusbar">
      <span>Region {state.region}</span>
      <span>Topic {state.topic}</span>
      <span>6 partitions, key is SKU and location</span>
    </footer>
  {/if}
</div>

<style>
  .shell { display: flex; flex-direction: column; height: 100%; }
  .shell > .topbar { flex: none; position: static; }
  .workbench { flex: 1; min-height: 0; display: flex; }
  .controls { width: 26rem; flex: none; overflow-y: auto; padding: .8rem; display: grid; gap: .8rem; align-content: start; border-right: 1px solid var(--border); }
  .charts { flex: 1; min-width: 0; overflow-y: auto; padding: .8rem; display: grid; gap: .8rem; align-content: start; }
  .charts > p { margin: 0; }
  .bar { height: .5rem; border-radius: var(--r-pill); background: var(--surface-2); overflow: hidden; }
  .fill { height: 100%; background: var(--accent); transition: width .5s; }
  .kpi-label { font-size: var(--fs-small); color: var(--text-2); }
  .shell > .statusbar { flex: none; }
  @media (max-width: 60rem) {
    .workbench { flex-direction: column; overflow-y: auto; }
    .controls { width: auto; border-right: 0; overflow: visible; }
    .charts { overflow: visible; }
  }
</style>
