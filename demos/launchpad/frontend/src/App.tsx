import {useCallback, useEffect, useState} from 'react';
import {api, ApiError, User} from './api';
import {AccessRequests} from './components/AccessRequests';
import {CredentialsPage} from './components/CredentialsPage';
import {Demos} from './components/Demos';
import {SignIn} from './components/SignIn';

type View = 'demos' | 'credentials' | 'access';
const VIEWS: View[] = ['demos', 'credentials', 'access'];

// The address bar holds the view in the hash, so the page has no routes for the host to know about.
const fromHash = (): View => {
  const h = window.location.hash.slice(1) as View;
  return VIEWS.includes(h) ? h : 'demos';
};

export default function App() {
  // undefined while loading, null when signed out.
  const [user, setUser] = useState<User | null | undefined>(undefined);
  const [view, setView] = useState<View>(fromHash);
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

  if (loadError) return <main className="gate"><p className="error" role="alert">{loadError}</p><button onClick={loadUser}>Retry</button></main>;
  if (user === undefined) return <main className="gate"><p className="muted">Loading…</p></main>;
  if (user === null) return <SignIn onSignedIn={loadUser} />;

  const current = view === 'access' && user.role !== 'admin' ? 'demos' : view;
  const tabs: [View, string][] = [['demos', 'Demos'], ['credentials', 'Confluent Cloud key']];
  if (user.role === 'admin') tabs.push(['access', 'Access requests']);

  return (
    <div className="app">
      <header className="topbar">
        <strong className="brand">Launchpad</strong>
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
      <main className="page">
        {current === 'demos' && <Demos user={user} onNeedCredentials={() => go('credentials')} />}
        {current === 'credentials' && <CredentialsPage current={user.credentials} onChanged={loadUser} />}
        {current === 'access' && <AccessRequests />}
      </main>
    </div>
  );
}
