import {useCallback, useEffect, useState} from 'react';
import {api, Demo, DemoStatus, errorText, safeUrl, User} from '../api';

const BUSY: DemoStatus[] = ['launching', 'tearing_down'];
const LABEL: Record<DemoStatus, string> = {
  launching: 'Launching',
  running: 'Running',
  launch_failed: 'Launch failed',
  tearing_down: 'Tearing down',
  teardown_failed: 'Tear down failed',
};

interface Props {
  user: User;
  onNeedCredentials: () => void;
}

export function Demos({user, onNeedCredentials}: Props) {
  const [demos, setDemos] = useState<Demo[] | null>(null);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    try {
      setDemos((await api.demos()).demos);
      setError('');
    } catch (err) {
      setError(errorText(err));
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  // Poll quickly while something is changing, slowly otherwise.
  const changing = demos?.some((d) => d.state && BUSY.includes(d.state.status)) ?? false;
  useEffect(() => {
    const t = setInterval(() => { void load(); }, changing ? 4000 : 30000);
    return () => clearInterval(t);
  }, [changing, load]);

  async function refresh() {
    try {
      setDemos((await api.refreshDemos()).demos);
      setError('');
    } catch (err) {
      setError(errorText(err));
    }
  }

  function replace(updated: Demo) {
    setDemos((list) => list && list.map((d) => (d.id === updated.id ? updated : d)));
  }

  return (
    <section>
      <div className="section-head">
        <div>
          <h2>Demos</h2>
          <p className="muted">Launch a demo into this AWS account, and tear it down when you are done.</p>
        </div>
        {user.role === 'admin' && <button onClick={refresh}>Re-read repository</button>}
      </div>
      {error && <p className="error" role="alert">{error}</p>}
      {!demos && !error && <p className="muted">Loading…</p>}
      {demos && demos.length === 0 && <p className="muted">No demos were found in the repository.</p>}
      <div className="cards">
        {demos?.map((d) => (
          <DemoCard key={d.id} demo={d} user={user} onChange={replace} onNeedCredentials={onNeedCredentials} />
        ))}
      </div>
    </section>
  );
}

interface CardProps {
  demo: Demo;
  user: User;
  onChange: (d: Demo) => void;
  onNeedCredentials: () => void;
}

function DemoCard({demo, user, onChange, onNeedCredentials}: CardProps) {
  const [working, setWorking] = useState(false);
  const [error, setError] = useState('');
  const [confirming, setConfirming] = useState(false);
  const [showLog, setShowLog] = useState(false);
  const [log, setLog] = useState('');

  const state = demo.state;
  const transient = !!state && BUSY.includes(state.status);
  const hasCredentials = user.credentials?.configured ?? false;
  const blocked = !demo.valid || (demo.needsConfluent && !hasCredentials);

  useEffect(() => {
    if (!showLog) return;
    let live = true;
    const read = async () => {
      try {
        const d = await api.demo(demo.id);
        if (live) setLog(d.state?.logTail ?? '');
      } catch {
        // The card's own refresh reports connection problems.
      }
    };
    void read();
    const t = transient ? setInterval(read, 4000) : undefined;
    return () => { live = false; if (t) clearInterval(t); };
  }, [showLog, transient, demo.id, state?.updatedAt]);

  async function act(fn: () => Promise<Demo>) {
    setWorking(true);
    setError('');
    try {
      onChange(await fn());
    } catch (err) {
      setError(errorText(err));
    } finally {
      setWorking(false);
      setConfirming(false);
    }
  }

  const url = state?.url ? safeUrl(state.url) : null;

  return (
    <article className="card">
      <header>
        <span className="tile" aria-hidden="true">{demo.name.slice(0, 1)}</span>
        <div className="grow"><h3>{demo.name}</h3></div>
        {state && <span className={`pill ${state.status}`}>{LABEL[state.status]}{transient ? '…' : ''}</span>}
      </header>
      {demo.description && <p>{demo.description}</p>}
      <p className="muted small">
        Commit <code title={demo.ref}>{demo.ref.slice(0, 7)}</code>
        {' · '}{demo.latest ? 'Latest on main' : 'Not the latest on main'}
      </p>

      {!demo.valid && (
        <p className="error">
          This demo does not follow the Launchpad interface: {demo.problems.join('; ')}.
        </p>
      )}
      {demo.valid && demo.needsConfluent && !hasCredentials && (
        <p className="notice">
          Needs your Confluent Cloud API key. <button className="link" onClick={onNeedCredentials}>Save it</button>
        </p>
      )}
      {state && <p className="muted small">Launched by {state.launchedBy}</p>}
      {state?.error && <p className="error" role="alert">{state.error}</p>}
      {error && <p className="error" role="alert">{error}</p>}

      <div className="row">
        {!state && (
          <button className="primary" disabled={working || blocked} onClick={() => act(() => api.launch(demo.id))}>
            Launch
          </button>
        )}
        {state?.status === 'running' && url && (
          <a className="button primary" href={url} target="_blank" rel="noopener noreferrer">Open demo</a>
        )}
        {state?.status === 'running' && !url && <span className="muted">No address was published.</span>}
        {state?.status === 'launch_failed' && (
          <button className="primary" disabled={working || blocked} onClick={() => act(() => api.launch(demo.id))}>
            Retry launch
          </button>
        )}
        {state?.status === 'teardown_failed' && state.canTearDown && (
          <button className="primary" disabled={working} onClick={() => act(() => api.teardown(demo.id))}>
            Retry tear down
          </button>
        )}
        {state && !transient && state.canTearDown && state.status !== 'teardown_failed' && (
          confirming ? (
            <span className="confirm">
              Delete everything this demo created?
              <button className="danger" disabled={working} onClick={() => act(() => api.teardown(demo.id))}>Tear down</button>
              <button onClick={() => setConfirming(false)}>Cancel</button>
            </span>
          ) : (
            <button disabled={working} onClick={() => setConfirming(true)}>Tear down</button>
          )
        )}
        {state && <button className="link" onClick={() => setShowLog((s) => !s)}>{showLog ? 'Hide log' : 'Show log'}</button>}
      </div>
      {showLog && <pre className="log" aria-label={`${demo.name} log`}>{log || 'No output yet.'}</pre>}
    </article>
  );
}
