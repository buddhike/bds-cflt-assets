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

export interface LatencyPoint {
  table: string;
  recordAt: string;
  committedAt: string;
  latencyMs: number;
  observedAt: string;
}

export interface TableError {
  table: string;
  error: string;
}

export interface LatencyResult {
  points: LatencyPoint[] | null;
  errors?: TableError[] | null;
}

export interface AppState {
  hasCredentials: boolean;
  awsRegion: string;
  busy: '' | 'setup' | 'teardown';
  lastError: string;
  steps: Step[];
  sim: SimStats;
  logs: LogEntry[] | null;
  latency: LatencyPoint[] | null;
  tableErrors: TableError[] | null;
  limits: {minRate: number; maxRate: number; defaultRate: number};
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
