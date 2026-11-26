<script>
  import { onDestroy, onMount } from 'svelte'
  import {
    GetAPIs,
    GetHealthBacklog,
    EventsOn,
    EventsOff,
    OnReconnect,
  } from './api.js'
  import HealthTile from './HealthTile.svelte'

  const GROUP_DEBOUNCE_MS = 300

  let apis = []
  let services = {}
  let monitorError = ''

  // Buffers rows sharing the same (api_id, detected_at) - each window can
  // fan out into several Kafka messages (one per contributing HTTP status
  // code, or a single 'RECOVERED'-sentinel row if none), and they need to
  // be grouped so the status breakdown reflects the whole window, not
  // just whichever row happened to arrive first (see finalizeGroup).
  const pendingGroups = {}

  function emptyServiceState() {
    return {
      unhealthyRate: 0,
      isAnomaly: false,
      statusCounts: {},
      totalCount: 0,
    }
  }

  // The Flink job emits a row only when an API turns unhealthy or recovers, so a page
  // opened later learns each API's current state from what the server has kept.
  async function replayBacklog() {
    try {
      for (const ev of await GetHealthBacklog()) bufferRow(ev)
    } catch (err) {
      monitorError = String(err)
    }
  }

  let offReconnect = () => {}

  onMount(async () => {
    apis = await GetAPIs()
    const initial = {}
    for (const api of apis) {
      initial[api.name] = emptyServiceState()
    }
    services = initial

    await replayBacklog()
    // Events sent while the stream was down are gone: start from what the server has.
    offReconnect = OnReconnect(async () => {
      services = { ...initial }
      await replayBacklog()
    })
  })

  EventsOn('health:event', bufferRow)
  EventsOn('health:error', (msg) => {
    monitorError = msg
  })

  onDestroy(() => {
    offReconnect()
    EventsOff('health:event', 'health:error')
    for (const group of Object.values(pendingGroups)) {
      clearTimeout(group.timer)
    }
  })

  function bufferRow(ev) {
    // A working monitor clears an earlier error, which may have been the topic not existing yet.
    monitorError = ''
    const key = `${ev.api_id}|${ev.detected_at}`
    if (!pendingGroups[key]) {
      pendingGroups[key] = { apiId: ev.api_id, rows: [], timer: null }
    }
    const group = pendingGroups[key]
    // (api_id, detected_at, http_status) is the row's key. The same row can arrive twice,
    // from the backlog and from the stream, and must not count twice.
    if (group.rows.some((r) => r.http_status === ev.http_status)) return
    group.rows.push(ev)
    clearTimeout(group.timer)
    group.timer = setTimeout(() => finalizeGroup(key), GROUP_DEBOUNCE_MS)
  }

  function finalizeGroup(key) {
    const group = pendingGroups[key]
    if (!group) return
    delete pendingGroups[key]

    // unhealthy_rate/is_anomaly/etc. are window-level aggregates - every
    // row in the group carries the same values, so any one row's is fine.
    const rep = group.rows[0]
    const totalCount = rep.total_count
    const statusCounts = {}
    for (const row of group.rows) {
      // 'RECOVERED' is a join sentinel (no contributing >=400 rows this
      // window), not a real status code - exclude it from the breakdown.
      if (row.http_status === 'RECOVERED') continue
      statusCounts[row.http_status] = (statusCounts[row.http_status] ?? 0) + row.status_count
    }

    services = {
      ...services,
      [group.apiId]: {
        unhealthyRate: rep.unhealthy_rate,
        isAnomaly: rep.is_anomaly,
        statusCounts,
        totalCount,
      },
    }
  }

</script>

<main>
  <h2>Service health</h2>

  {#if monitorError}
    <div class="error">{monitorError}</div>
  {/if}

  <div class="stack">
    {#each apis as api (api.name)}
      <HealthTile name={api.name} service={services[api.name] ?? emptyServiceState()} />
    {/each}
  </div>
</main>

<style>
  main {
    padding: 0.6rem;
    height: 100%;
    box-sizing: border-box;
  }

  h2 {
    margin: 0 0 0.5rem;
    font-size: 0.75rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    opacity: 0.7;
  }

  .error {
    color: #ff6b6b;
    font-size: 0.8rem;
    margin-bottom: 0.5rem;
  }

  .stack {
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
  }
</style>
