// Typed access to the Go backend and its events. Wails generates the call
// bindings in ../wailsjs; this module narrows them to the shapes the UI
// uses and parses timestamps once.
import * as App from '../wailsjs/go/main/App';
import {EventsOn} from '../wailsjs/runtime/runtime';

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
  hasCredentials: boolean;
  awsRegion: string;
  busy: '' | 'setup' | 'teardown';
  lastError: string;
  steps: Step[];
  sim: SimStats;
  logs: LogEntry[] | null;
  latency: LatencyResult;
  limits: {minRate: number; maxRate: number; defaultRate: number};
  query: QueryInfo;
}

export interface Credentials {
  awsRegion: string;
  apiKey: string;
  apiSecret: string;
}

export const api = {
  getDefaults: () => App.GetDefaults(),
  getState: () => App.GetState() as unknown as Promise<AppState>,
  setCredentials: (c: Credentials) => App.SetCredentials(c as never),
  setup: () => App.Setup(),
  teardown: () => App.Teardown(),
  startSimulation: (rate: number) => App.StartSimulation(rate),
  setRate: (rate: number) => App.SetRate(rate),
  stopSimulation: () => App.StopSimulation(),
  runQuery: (sql: string) => App.RunQuery(sql) as unknown as Promise<QueryResult>,
};

export const events = {
  onLog: (fn: (e: LogEntry) => void) => EventsOn('log', fn),
  onStep: (fn: (s: Step) => void) => EventsOn('step', fn),
  onLatency: (fn: (r: LatencyResult) => void) => EventsOn('latency', fn),
  onSim: (fn: (s: SimStats) => void) => EventsOn('sim', fn),
  onSetupDone: (fn: (r: {error: string}) => void) => EventsOn('setup:done', fn),
  onTeardownDone: (fn: (r: {error: string}) => void) => EventsOn('teardown:done', fn),
};

/** errorText turns a rejected Wails call into a readable message. */
export function errorText(err: unknown): string {
  if (typeof err === 'string') return err;
  if (err instanceof Error) return err.message;
  return JSON.stringify(err);
}
