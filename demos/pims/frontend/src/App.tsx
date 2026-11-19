import {useCallback, useEffect, useState} from 'react';
import {api, AppState, errorText, LatencyResult, LogEntry, Step} from './api';
import {CredentialsForm} from './components/CredentialsForm';
import {DataFlow} from './components/DataFlow';
import {SetupSteps} from './components/SetupSteps';
import {SimulationPanel} from './components/SimulationPanel';
import {LatencyChart} from './components/LatencyChart';
import {QueryPanel} from './components/QueryPanel';
import {ActivityLog} from './components/ActivityLog';
import {Logo} from './components/Logo';
import {events} from './api';

const ACTIVITY_ID = 'activity';
const MAX_LOGS = 500;
const NO_LATENCY: LatencyResult = {event: null, processing: null};

export default function App() {
  const [state, setState] = useState<AppState | null>(null);
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [latency, setLatency] = useState<LatencyResult>(NO_LATENCY);
  const [actionError, setActionError] = useState('');
  const [confirmTeardown, setConfirmTeardown] = useState(false);

  const refresh = useCallback(async () => {
    const s = await api.getState();
    setState(s);
    setLogs(s.logs ?? []);
    setLatency(s.latency ?? NO_LATENCY);
  }, []);

  useEffect(() => {
    refresh();
    const offs = [
      events.onLog((e) => setLogs((l) => [...l.slice(-(MAX_LOGS - 1)), e])),
      events.onStep((step: Step) => setState((s) => s && {...s, steps: s.steps.map((x) => (x.id === step.id ? step : x))})),
      events.onSim((sim) => setState((s) => s && {...s, sim})),
      // Each event is the whole current reading, so it replaces the last one.
      events.onLatency(setLatency),
      events.onSetupDone((r) => { setActionError(r.error); refresh(); }),
      events.onTeardownDone((r) => { setActionError(r.error); refresh(); }),
    ];
    return () => offs.forEach((off) => off());
  }, [refresh]);

  async function run(fn: () => Promise<void>) {
    setActionError('');
    try {
      await fn();
      await refresh();
    } catch (err) {
      setActionError(errorText(err));
    }
  }

  if (!state) return <main className="gate"><p>Loading…</p></main>;
  if (!state.hasCredentials) return <CredentialsForm onSaved={refresh}/>;

  const busy = state.busy;
  const stepsStarted = state.steps.some((s) => s.status !== 'pending');
  const schemaReady = state.steps.find((s) => s.id === 'schema')?.status === 'done' || state.sim.running || state.sim.events > 0;
  // Queries run in the DuckDB container, which setup starts, and the teardown deletes.
  const queryReady = state.steps.find((s) => s.id === 'services')?.status === 'done' && busy !== 'teardown';
  const errorCount = logs.filter((l) => l.level.toUpperCase() === 'ERROR').length;
  const warnCount = logs.filter((l) => l.level.toUpperCase() === 'WARN').length;

  return (
    <div className="app">
      <header className="topbar">
        <div className="brand">
          <Logo className="brand-mark"/>
          <div className="brand-text">
            <h1>PIMS</h1>
            <span className="sub">
              Streaming live MySQL changes into queryable Iceberg tables with Debezium CDC and Confluent Cloud
              Tableflow, and showing how fresh they are · {state.awsRegion}
            </span>
          </div>
        </div>
        <div className="actions">
          {busy && <span className="pill pill-on">● {busy === 'setup' ? 'Setting up' : 'Tearing down'}…</span>}
          <button onClick={() => document.getElementById(ACTIVITY_ID)?.scrollIntoView({block: 'start'})}>
            Activity log
            {errorCount > 0
              ? <span className="badge-count" aria-label={`${errorCount} errors`}>{errorCount}</span>
              : warnCount > 0 && <span className="badge-count badge-warn" aria-label={`${warnCount} warnings`}>{warnCount}</span>}
          </button>
          <button className="primary" disabled={!!busy} onClick={() => run(api.setup)}>
            {stepsStarted ? 'Re-run setup' : 'Set up demo'}
          </button>
          {confirmTeardown ? (
            <span className="confirm">
              Delete all cloud and local resources?
              <button className="danger" onClick={() => { setConfirmTeardown(false); run(api.teardown); }}>Delete everything</button>
              <button onClick={() => setConfirmTeardown(false)}>Cancel</button>
            </span>
          ) : (
            <button disabled={!!busy} onClick={() => setConfirmTeardown(true)}>Tear down</button>
          )}
        </div>
      </header>

      {(actionError || state.lastError) && <p className="banner error" role="alert">{actionError || state.lastError}</p>}

      <div className="grid">
        <DataFlow active={state.sim.running}/>
        <section className="card">
          <header className="card-head"><h2>Setup</h2></header>
          <SetupSteps steps={state.steps}/>
        </section>
        <SimulationPanel sim={state.sim} limits={state.limits} ready={schemaReady}/>
        <LatencyChart latency={latency}/>
        <QueryPanel info={state.query ?? {}} ready={queryReady}/>
        <ActivityLog id={ACTIVITY_ID} logs={logs}/>
      </div>
    </div>
  );
}
