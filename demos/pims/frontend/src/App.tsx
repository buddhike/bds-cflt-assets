import {useCallback, useEffect, useState} from 'react';
import {api, AppState, errorText, LatencyResult, LogEntry, Step} from './api';
import {CredentialsForm} from './components/CredentialsForm';
import {SimulationBar} from './components/SimulationBar';
import {QueryPanel} from './components/QueryPanel';
import {SidePanel, SideSection} from './components/SidePanel';
import {StatusBar} from './components/StatusBar';
import {Logo} from './components/Logo';
import {Notice} from './components/Notice';
import {events} from './api';

const MAX_LOGS = 500;
const NO_LATENCY: LatencyResult = {event: null, processing: null};

export default function App() {
  const [state, setState] = useState<AppState | null>(null);
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [latency, setLatency] = useState<LatencyResult>(NO_LATENCY);
  const [actionError, setActionError] = useState('');
  const [confirmTeardown, setConfirmTeardown] = useState(false);
  const [side, setSide] = useState<SideSection | null>(null);

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
      // Events sent while the browser was disconnected are gone: start from the server's state again.
      events.onReconnect(refresh),
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

  if (!state) return <main><div className="gate"><p className="muted">Loading…</p></div></main>;
  if (!state.hasCredentials) return <CredentialsForm onSaved={refresh}/>;

  const busy = state.busy;
  const stepsStarted = state.steps.some((s) => s.status !== 'pending');
  const schemaReady = state.steps.find((s) => s.id === 'schema')?.status === 'done' || state.sim.running || state.sim.events > 0;
  // Queries run in the DuckDB container, which setup starts, and the teardown deletes.
  const queryReady = state.steps.find((s) => s.id === 'services')?.status === 'done' && busy !== 'teardown';
  const managed = !!state.managed;
  const setupFailed = state.steps.some((s) => s.status === 'failed');
  const errorCount = logs.filter((l) => l.level.toUpperCase() === 'ERROR').length;
  const warnCount = logs.filter((l) => l.level.toUpperCase() === 'WARN').length;

  return (
    <div className="app">
      <header className="topbar">
        <h1
          className="brand"
          title="Patient Information Management System: streaming live MySQL changes into queryable Iceberg tables with Debezium CDC and Confluent Cloud Tableflow, and showing how fresh they are"
        >
          <Logo/>
          {managed && <>Launchpad <span className="sep">/</span></>} PIMS
        </h1>
        {busy && <span className="pill busy">{busy === 'setup' ? 'Setting up' : 'Tearing down'}…</span>}
        <span className="spacer"/>
        <div className="who">
          {managed ? (
            !busy && setupFailed && <button className="primary sm" onClick={() => run(api.setup)}>Retry setup</button>
          ) : (
            <>
              <button className="primary sm" disabled={!!busy} onClick={() => run(api.setup)}>
                {stepsStarted ? 'Re-run setup' : 'Set up demo'}
              </button>
              {confirmTeardown ? (
                <span className="confirm">
                  Delete all cloud and local resources?
                  <button className="danger sm" onClick={() => { setConfirmTeardown(false); run(api.teardown); }}>Delete everything</button>
                  <button className="sm" onClick={() => setConfirmTeardown(false)}>Cancel</button>
                </span>
              ) : (
                <button className="quiet sm" disabled={!!busy} onClick={() => setConfirmTeardown(true)}>Tear down</button>
              )}
            </>
          )}
        </div>
      </header>

      <SimulationBar sim={state.sim} limits={state.limits} ready={schemaReady}/>

      {(actionError || state.lastError) && <div className="banner"><Notice tone="danger" alert>{actionError || state.lastError}</Notice></div>}

      <div className="workspace">
        <SidePanel
          section={side}
          onSelect={setSide}
          logs={logs}
          latency={latency}
          simRunning={state.sim.running}
          errorCount={errorCount}
          warnCount={warnCount}
        />
        <main className="main">
          <QueryPanel info={state.query ?? {}} ready={queryReady}/>
        </main>
      </div>

      <StatusBar
        steps={state.steps}
        busy={busy}
        region={state.awsRegion}
        errorCount={errorCount}
        warnCount={warnCount}
        onOpenLogs={() => setSide('logs')}
      />
    </div>
  );
}
