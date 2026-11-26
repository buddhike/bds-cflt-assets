// The one place the UI talks to the Go backend (cmd/server): calls over HTTP, and events
// pushed over Server-Sent Events. Components import from here and never call fetch or
// EventSource themselves.

async function http(method, path, body) {
  const res = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers: { 'X-APIDIAG': '1', ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  let data = null
  try {
    data = await res.json()
  } catch {
    // Reported by status below.
  }
  if (!res.ok) {
    // Components show String(err), so reject with the message itself.
    throw (data && data.error) || `The request failed (${res.status})`
  }
  return data
}

// --- calls --------------------------------------------------------------------------

/** The demo's name, region and topic, and whether an AI agent is deployed. */
export const GetState = () => http('GET', '/api/state')

/** The simulated APIs as they are now. */
export async function GetAPIs() {
  return (await http('GET', '/api/apis')).current
}

/** The simulated APIs as they start out, which Reset returns to. */
export async function GetAPIDefaults() {
  return (await http('GET', '/api/apis')).defaults
}

export async function IsSimulationRunning() {
  return (await http('GET', '/api/state')).running
}

export async function StartSimulation(apis) {
  await http('POST', '/api/simulation/start', { apis })
}

export async function StopSimulation() {
  await http('POST', '/api/simulation/stop')
}

/** Applies a change to one API. Resolves to whether the simulation is running. */
export async function UpdateAPIConfig(cfg) {
  return (await http('POST', '/api/apis/update', cfg)).running
}

/** What the monitor has seen so far: the Flink job emits a row only when an API changes state. */
export const GetHealthBacklog = () => http('GET', '/api/health/backlog')

/** The AI reports so far. */
export const GetReportBacklog = () => http('GET', '/api/reports/backlog')

// --- events -------------------------------------------------------------------------

// One event stream serves every listener. The browser reconnects by itself, and whatever
// was sent in between is gone, so a reconnect tells the components to reload what they show.
let stream = null
const listeners = new Map() // event name -> Set of callbacks
const reconnectListeners = new Set()

function ensureStream() {
  if (stream) return stream
  stream = new EventSource('/api/events')
  let opened = false
  stream.onopen = () => {
    if (opened) reconnectListeners.forEach((fn) => fn())
    opened = true
  }
  return stream
}

function listen(name) {
  ensureStream().addEventListener(name, (e) => {
    let data
    try {
      data = JSON.parse(e.data)
    } catch {
      return
    }
    listeners.get(name)?.forEach((fn) => fn(data))
  })
}

/** Subscribes to an event. Returns a function that ends the subscription. */
export function EventsOn(name, callback) {
  if (!listeners.has(name)) {
    listeners.set(name, new Set())
    listen(name)
  }
  listeners.get(name).add(callback)
  return () => listeners.get(name)?.delete(callback)
}

/** Ends every subscription to the named events. */
export function EventsOff(...names) {
  for (const n of names) listeners.get(n)?.clear()
}

/** Calls fn when the event stream comes back after a break. */
export function OnReconnect(fn) {
  reconnectListeners.add(fn)
  return () => reconnectListeners.delete(fn)
}

/** Opens a link from an AI report in a new tab, without giving it this page. */
export function OpenURL(url) {
  window.open(url, '_blank', 'noopener,noreferrer')
}
