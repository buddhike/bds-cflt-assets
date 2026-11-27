// Typed access to the Go backend and its events. The server under cmd/server
// answers calls over HTTP and pushes events over SSE; this module narrows both
// to the shapes the UI uses.

export type StepStatus = 'pending' | 'running' | 'done' | 'failed';

export interface Step {
  id: string;
  title: string;
  status: StepStatus;
  detail?: string;
  startedAt?: string;
  finishedAt?: string;
}

export interface SimStats {
  running: boolean;
  rate: number;
  events: number;
  byKind: Record<string, number>;
  conflicts: number;
  errors: number;
  lastError?: string;
  simulatedAt: string;
  patients: number;
  scheduled: number;
  inClinic: number;
}

export interface LogEntry {
  time: string;
  level: string;
  message: string;
  attrs: string;
}

/** Tableflow's reading of a freshness metric for one table, for the minute starting at `time`. */
export interface LatencySample {
  table: string;
  time: string;
  ms: number;
}

/**
 * Tableflow's two freshness metrics over the last 5 hours, oldest sample first.
 * A table Tableflow has not reported on yet has no samples.
 */
export interface LatencyResult {
  /** io.confluent.tableflow/event_freshness_latency_milliseconds */
  event: LatencySample[] | null;
  /** io.confluent.tableflow/processing_freshness_latency_milliseconds */
  processing: LatencySample[] | null;
  /** When PIMS last read the metrics; absent before the first poll. */
  observedAt?: string;
  /** Why the latest poll failed. The samples are those of the last good poll. */
  error?: string;
}

/** What the query window can reach: the Tableflow tables, once Tableflow is enabled. */
export interface QueryInfo {
  /** The DuckDB catalog the tables are attached as. */
  catalog?: string;
  /** The schema in `catalog` that holds the tables: the Kafka cluster id. */
  namespace?: string;
  tables?: string[];
}

/** The last result set of a query. Cells are DuckDB's own text, or null for NULL. */
export interface QueryResult {
  columns: string[];
  /** At most the first 1000 rows. */
  rows: (string | null)[][];
  /** How many rows the statement returned. */
  totalRows: number;
  elapsedMs: number;
}

export interface AppState {
  awsRegion: string;
  /** Launchpad runs the demo's life, so setup starts by itself and nothing here tears it down. */
  busy: '' | 'setup';
  lastError: string;
  steps: Step[];
  sim: SimStats;
  logs: LogEntry[] | null;
  latency: LatencyResult;
  limits: {minRate: number; maxRate: number; defaultRate: number};
  query: QueryInfo;
}

async function http<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers: {'X-PIMS': '1', ...(body === undefined ? {} : {'Content-Type': 'application/json'})},
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  let data: unknown = null;
  try {
    data = await res.json();
  } catch {
    // Reported by status below.
  }
  if (!res.ok) {
    throw new Error((data as {error?: string} | null)?.error ?? `The request failed (${res.status})`);
  }
  return data as T;
}

export const api = {
  getState: () => http<AppState>('GET', '/api/state'),
  setup: async () => { await http('POST', '/api/setup'); },
  startSimulation: async (rate: number) => { await http('POST', '/api/simulation/start', {rate}); },
  setRate: async (rate: number) => { await http('POST', '/api/simulation/rate', {rate}); },
  stopSimulation: async () => { await http('POST', '/api/simulation/stop'); },
  runQuery: (sql: string) => http<QueryResult>('POST', '/api/query', {sql}),
};

// One event stream serves every listener. The browser reconnects by itself, and
// whatever was sent in between is gone, so a reconnect makes the UI reload its state.
let stream: EventSource | null = null;
const reconnectListeners = new Set<() => void>();

function webStream(): EventSource {
  if (!stream) {
    stream = new EventSource('/api/events');
    let opened = false;
    stream.onopen = () => {
      if (opened) reconnectListeners.forEach((fn) => fn());
      opened = true;
    };
  }
  return stream;
}

function subscribe<T>(name: string, fn: (v: T) => void): () => void {
  const src = webStream();
  const listener = (e: Event) => fn(JSON.parse((e as MessageEvent<string>).data) as T);
  src.addEventListener(name, listener);
  return () => src.removeEventListener(name, listener);
}

export const events = {
  onLog: (fn: (e: LogEntry) => void) => subscribe('log', fn),
  onStep: (fn: (s: Step) => void) => subscribe('step', fn),
  onLatency: (fn: (r: LatencyResult) => void) => subscribe('latency', fn),
  onSim: (fn: (s: SimStats) => void) => subscribe('sim', fn),
  onSetupDone: (fn: (r: {error: string}) => void) => subscribe('setup:done', fn),
  /** Called when the event stream reconnects after a break. */
  onReconnect: (fn: () => void) => {
    reconnectListeners.add(fn);
    return () => { reconnectListeners.delete(fn); };
  },
};

/** errorText turns a rejected call into a readable message. */
export function errorText(err: unknown): string {
  if (typeof err === 'string') return err;
  if (err instanceof Error) return err.message;
  return JSON.stringify(err);
}
