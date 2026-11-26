import {useCallback, useEffect, useState} from 'react';
import {api, ApiError, User} from './api';
import {CredentialsPage} from './components/CredentialsPage';
import {Demos} from './components/Demos';
import {Logo} from './components/Logo';
import {LogView} from './components/LogView';
import {SignIn} from './components/SignIn';

type View = 'demos' | 'credentials';
const VIEWS: View[] = ['demos', 'credentials'];

// A demo's log has a page of its own: #log/<demo id>.
type Route = {view: View} | {log: string};
const LOG_PREFIX = 'log/';

// The address bar holds the page in the hash, so the page has no routes for the host to know about.
const fromHash = (): Route => {
  const h = window.location.hash.slice(1);
  if (h.startsWith(LOG_PREFIX) && h.length > LOG_PREFIX.length) {
    try {
      return {log: decodeURIComponent(h.slice(LOG_PREFIX.length))};
    } catch {
      return {view: 'demos'};
    }
  }
  return {view: VIEWS.includes(h as View) ? (h as View) : 'demos'};
};

export default function App() {
  // undefined while loading, null when signed out.
  const [user, setUser] = useState<User | null | undefined>(undefined);
  const [view, setView] = useState<Route>(fromHash);
  const [loadError, setLoadError] = useState('');

  const loadUser = useCallback(async () => {
    try {
      setUser(await api.me());
      setLoadError('');
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) setUser(null);
      else setLoadError(err instanceof Error ? err.message : String(err));
    }
  }, []);

  useEffect(() => { void loadUser(); }, [loadUser]);
  useEffect(() => {
    const on = () => setView(fromHash());
    window.addEventListener('hashchange', on);
    return () => window.removeEventListener('hashchange', on);
  }, []);

  const go = (v: View) => { window.location.hash = v; };
  const showLog = (id: string) => { window.location.hash = LOG_PREFIX + encodeURIComponent(id); };

  if (loadError) return <main className="gate"><p className="error" role="alert">{loadError}</p><button onClick={loadUser}>Retry</button></main>;
  if (user === undefined) return <main className="gate"><p className="muted">Loading…</p></main>;
  if (user === null) return <SignIn onSignedIn={loadUser} />;

  // The log belongs to the demos, so that tab stays lit while one is open.
  const current: View = 'view' in view ? view.view : 'demos';
  const tabs: [View, string][] = [['demos', 'Demos'], ['credentials', 'Confluent Cloud key']];

  return (
    <div className="app">
      <header className="topbar">
        <span className="brand"><Logo size={26} />Launchpad</span>
        <nav aria-label="Main">
          {tabs.map(([v, label]) => (
            <button key={v} className={`tab${current === v ? ' active' : ''}`} aria-current={current === v ? 'page' : undefined} onClick={() => go(v)}>
              {label}
            </button>
          ))}
        </nav>
        <span className="who">
          {user.email}
          <button className="link" onClick={async () => { await api.logout().catch(() => undefined); setUser(null); }}>Sign out</button>
        </span>
      </header>
      <main className={`page${'log' in view ? ' wide' : ''}`}>
        {'log' in view && <LogView key={view.log} id={view.log} onBack={() => go('demos')} />}
        {'view' in view && view.view === 'demos' && <Demos user={user} onNeedCredentials={() => go('credentials')} onShowLog={showLog} />}
        {'view' in view && view.view === 'credentials' && <CredentialsPage current={user.credentials} onChanged={loadUser} />}
      </main>
    </div>
  );
}
