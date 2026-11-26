import {useCallback, useEffect, useState} from 'react';
import {api, ApiError, Demo, errorText, safeUrl, User} from '../api';
import {BUSY, statusText, tone} from '../status';
import {Icon} from './Icon';
import {Notice} from './Notice';

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
      <div className="page-head">
        <div>
          <h1>Demos</h1>
          <p>Launch a demo into this AWS account, update it when its code changes, and tear it down when you are done.</p>
        </div>
        {user.role === 'admin' && <button onClick={refresh}>Re-read repository</button>}
      </div>
      {error && <Notice tone="danger" alert>{error}</Notice>}
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
      <header className="card-head">
        <span className="tile" aria-hidden="true">{demo.name.slice(0, 1)}</span>
        <h3>{demo.name}</h3>
        {state?.updateAvailable && <span className="pill warn">Update available</span>}
        {state ? <span className={`pill ${tone(state.status)}`}>{statusText(state.status)}</span> : <span className="pill">Not deployed</span>}
      </header>
      {demo.description && <p className="muted">{demo.description}</p>}
      {demo.tags && demo.tags.length > 0 && (
        <ul className="tags" aria-label="Confluent Cloud products">
          {demo.tags.map((tag) => <li key={tag} className="tag">{tag}</li>)}
        </ul>
      )}
      <p className="small faint">
        {commitLabel}<code title={shown}>{shown.slice(0, 7)}</code>
        {state
          ? state.updateAvailable && <>{' · '}Newer commit <code title={demo.ref}>{demo.ref.slice(0, 7)}</code> available</>
          : <>{' · '}{demo.latest ? 'Latest on main' : 'Not the latest on main'}</>}
      </p>

      {!demo.valid && (
        <Notice tone="danger" title="Not a Launchpad demo.">
          It does not follow the Launchpad interface: {demo.problems.join('; ')}.
        </Notice>
      )}
      {demo.valid && demo.needsConfluent && !hasCredentials && (
        <Notice tone="warn" title="Needs your Confluent Cloud API key.">
          <button className="link" onClick={onNeedCredentials}>Save it</button>
        </Notice>
      )}
      {state && <p className="small faint">Launched by {state.launchedBy}</p>}
      {state?.error && <Notice tone="danger" alert>{state.error}</Notice>}
      {error && <Notice tone="danger" alert>{error}</Notice>}
      {notice && <p className="small muted" role="status">{notice}</p>}

      <div className="row actions">
        {!state && (
          <button className="primary" disabled={working || blocked} onClick={() => act(() => api.launch(demo.id))}>
            Launch
          </button>
        )}
        {state?.status === 'running' && url && (
          <a className="button primary" href={url} target="_blank" rel="noopener noreferrer">Open demo<Icon name="external" /></a>
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
            Open previous address<Icon name="external" />
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
            <button className="quiet" disabled={working} onClick={() => setConfirming(true)}>Tear down</button>
          )
        )}
        {state && <button className="link" onClick={() => onShowLog(demo.id)}>Show log</button>}
      </div>
    </article>
  );
}
