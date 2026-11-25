import {useCallback, useEffect, useState} from 'react';
import {api, errorText, User} from '../api';

const when = (t?: number | null) => (t ? new Date(t * 1000).toLocaleString() : '');

export function AccessRequests() {
  const [users, setUsers] = useState<User[] | null>(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState('');

  const load = useCallback(async () => {
    try {
      setUsers((await api.users()).users);
      setError('');
    } catch (err) {
      setError(errorText(err));
    }
  }, []);
  useEffect(() => { void load(); }, [load]);

  async function decide(email: string, approve: boolean) {
    setBusy(email);
    setError('');
    try {
      await api.decide(email, approve);
      await load();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy('');
    }
  }

  const pending = users?.filter((u) => u.status === 'pending') ?? [];
  const others = users?.filter((u) => u.status !== 'pending') ?? [];

  return (
    <section>
      <h2>Access requests</h2>
      {error && <p className="error" role="alert">{error}</p>}
      {users && pending.length === 0 && <p className="muted">Nobody is waiting for approval.</p>}
      {pending.length > 0 && (
        <table>
          <thead><tr><th>Email</th><th>Requested</th><th /></tr></thead>
          <tbody>
            {pending.map((u) => (
              <tr key={u.email}>
                <td>{u.email}</td>
                <td>{when(u.createdAt)}</td>
                <td className="actions">
                  <button className="primary" disabled={busy === u.email} onClick={() => decide(u.email, true)}>Approve</button>
                  <button disabled={busy === u.email} onClick={() => decide(u.email, false)}>Deny</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <h3>Everyone else</h3>
      {others.length === 0 ? <p className="muted">No other users yet.</p> : (
        <table>
          <thead><tr><th>Email</th><th>Role</th><th>Status</th><th>Decided by</th><th /></tr></thead>
          <tbody>
            {others.map((u) => (
              <tr key={u.email}>
                <td>{u.email}</td>
                <td>{u.role}</td>
                <td>{u.status}</td>
                <td>{u.decidedBy ?? ''}</td>
                <td className="actions">
                  {u.role !== 'admin' && (
                    <button disabled={busy === u.email} onClick={() => decide(u.email, u.status !== 'approved')}>
                      {u.status === 'approved' ? 'Revoke' : 'Approve'}
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}
