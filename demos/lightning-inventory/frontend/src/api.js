// The one place the UI talks to the Go control service: calls over HTTP. Components import
// from here and never call fetch themselves. The browser can set only whether each load
// runs and its rate: the server bounds both.

async function http(method, path, body) {
  const res = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers: { 'X-LIGHTNING': '1', ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
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

/** Everything the page shows apart from the charts. */
export const GetState = () => http('GET', '/api/state')

/** The latency series of both scenarios for the last 5, 15 or 60 minutes, at 1 s. */
export const GetLatency = (minutes) => http('GET', `/api/latency?minutes=${encodeURIComponent(minutes)}`)

/** Starts or stops the live events, and/or sets their rate. Resolves to the new state. */
export const ChangeEvents = (change) => http('POST', '/api/events', change)

/** Starts or stops the queries, and/or sets their rate. Resolves to the new state. */
export const ChangeQueries = (change) => http('POST', '/api/queries', change)

/** Loads the whole catalogue into the topic, once. */
export const LoadCatalogue = () => http('POST', '/api/catalogue/load')
