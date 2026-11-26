import {useCallback, useEffect, useState} from 'react';
import {api, ApiError, Demo, DemoStatus, errorText, safeUrl, User} from '../api';

const BUSY: DemoStatus[] = ['launching', 'updating', 'tearing_down'];
const LABEL: Record<DemoStatus, string> = {
  launching: 'Launching',
  running: 'Running',
  launch_failed: 'Launch failed',
  updating: 'Updating',
  update_failed: 'Update failed',
  tearing_down: 'Tearing down',
  teardown_failed: 'Tear down failed',
};

interface Props {
  user: User;
  onNeedCredentials: () => void;
  onShowLog: (id: string) => void;
}

export function Demos({user, onNeedCredentials, onShowLog}: Props) {
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
          <p className="muted">Launch a demo into this AWS account, update it when its code changes, and tear it down when you are done.</p>
        </div>
        {user.role === 'admin' && <button onClick={refresh}>Re-read repository</button>}
      </div>
      {error && <p className="error" role="alert">{error}</p>}
      {!demos && !error && <p className="muted">Loading…</p>}
      {demos && demos.length === 0 && <p className="muted">No demos were found in the repository.</p>}
      <div className="cards">
        {demos?.map((d) => (
          <DemoCard key={d.id} demo={d} user={user} onChange={replace} onNeedCredentials={onNeedCredentials} onShowLog={onShowLog} />
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
  onShowLog: (id: string) => void;
}

function DemoCard({demo, user, onChange, onNeedCredentials, onShowLog}: CardProps) {
  const [working, setWorking] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [confirming, setConfirming] = useState(false);

  const state = demo.state;
  const transient = !!state && BUSY.includes(state.status);
  const hasCredentials = user.credentials?.configured ?? false;
  const blocked = !demo.valid || (demo.needsConfluent && !hasCredentials);

  // What a message said is about the card as it was.
  useEffect(() => { setError(''); setNotice(''); }, [state?.status, state?.ref]);

  async function act(fn: () => Promise<Demo>) {
    setWorking(true);
    setError('');
    setNotice('');
    try {
      onChange(await fn());
    } catch (err) {
      // Nothing to update is an answer, not a failure.
      if (err instanceof ApiError && err.code === 'up_to_date') setNotice(err.message);
      else setError(errorText(err));
    } finally {
      setWorking(false);
      setConfirming(false);
    }
  }

  const url = state?.url ? safeUrl(state.url) : null;
  // A deployed demo is at the commit that was applied, which may be older than the repository's.
  const shown = state ? state.ref : demo.ref;
  const commitLabel = state?.status === 'running' ? 'Running commit ' : state?.status === 'updating' ? 'Updating to commit ' : 'Commit ';
  // The address a demo had stays through an update, but nothing says it is up until the update ends.
  const oldAddress = (state?.status === 'updating' || state?.status === 'update_failed') && url;

  return (
    <article className="card">
      <header>
        <span className="tile" aria-hidden="true">{demo.name.slice(0, 1)}</span>
        <div className="grow"><h3>{demo.name}</h3></div>
        {state && <span className={`pill ${state.status}`}>{LABEL[state.status]}{transient ? '…' : ''}</span>}
      </header>
      {demo.description && <p>{demo.description}</p>}
      {demo.tags && demo.tags.length > 0 && (
        <ul className="tags" aria-label="Confluent Cloud products">
          {demo.tags.map((tag) => <li key={tag} className="tag">{tag}</li>)}
        </ul>
      )}
      <p className="muted small">
        {commitLabel}<code title={shown}>{shown.slice(0, 7)}</code>
        {state
          ? state.updateAvailable && <>{' · '}Newer commit <code title={demo.ref}>{demo.ref.slice(0, 7)}</code> available</>
          : <>{' · '}{demo.latest ? 'Latest on main' : 'Not the latest on main'}</>}
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
      {notice && <p className="muted small" role="status">{notice}</p>}

      <div className="row">
        {!state && (
          <button className="primary" disabled={working || blocked} onClick={() => act(() => api.launch(demo.id))}>
            Launch
          </button>
        )}
        {state?.status === 'running' && url && (
          <a className="button primary" href={url} target="_blank" rel="noopener noreferrer">Open demo</a>
        )}
        {state?.status === 'running' && state.canUpdate && (
          // The repository is only read now and then, so a commit just pushed may not be in the
          // page yet. Asking for the update reads it, and says so when there is nothing new.
          <button disabled={working || !demo.valid} onClick={() => act(() => api.update(demo.id))}>
            {state.updateAvailable ? `Update to ${demo.ref.slice(0, 7)}` : 'Check for update'}
          </button>
        )}
        {state?.status === 'update_failed' && state.canUpdate && (
          <button className="primary" disabled={working || !demo.valid} onClick={() => act(() => api.update(demo.id))}>
            {state.updateAvailable ? `Retry update with ${demo.ref.slice(0, 7)}` : 'Retry update'}
          </button>
        )}
        {oldAddress && (
          <a className="button" href={oldAddress} target="_blank" rel="noopener noreferrer"
             title="The address the demo had. It may not work until the update is done.">
            Open previous address
          </a>
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
        {state && <button className="link" onClick={() => onShowLog(demo.id)}>Show log</button>}
      </div>
    </article>
  );
}
