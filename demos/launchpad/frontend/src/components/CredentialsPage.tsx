import {FormEvent, useState} from 'react';
import {api, Credentials, errorText} from '../api';

interface Props {
  current: Credentials | undefined;
  onChanged: () => void;
}

export function CredentialsPage({current, onChanged}: Props) {
  const [apiKey, setApiKey] = useState('');
  const [apiSecret, setApiSecret] = useState('');
  const [error, setError] = useState('');
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError('');
    setSaved(false);
    try {
      await api.saveCredentials(apiKey, apiSecret);
      setApiKey('');
      setApiSecret('');
      setSaved(true);
      onChanged();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    setBusy(true);
    setError('');
    setSaved(false);
    try {
      await api.deleteCredentials();
      onChanged();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section>
      <h2>Confluent Cloud credentials</h2>
      <p>
        Demos that create Confluent Cloud resources use your Cloud API key. It is encrypted before it is stored, is
        never shown again, and is only decrypted while a demo you launch is being deployed or torn down.
      </p>
      {current?.configured ? (
        <p className="notice">
          A key ending in <code>{current.apiKeyHint}</code> is saved.{' '}
          <button className="link" disabled={busy} onClick={remove}>Delete it</button>
        </p>
      ) : (
        <p className="muted">No key is saved.</p>
      )}
      <form onSubmit={save} autoComplete="off">
        <label>
          Cloud API key
          <input required value={apiKey} onChange={(e) => setApiKey(e.target.value)} autoComplete="off" spellCheck={false} />
        </label>
        <label>
          Cloud API secret
          <input
            required
            type="password"
            value={apiSecret}
            onChange={(e) => setApiSecret(e.target.value)}
            autoComplete="new-password"
            spellCheck={false}
          />
        </label>
        <button className="primary" disabled={busy}>{current?.configured ? 'Replace key' : 'Save key'}</button>
      </form>
      {saved && <p className="notice" role="status">Saved.</p>}
      {error && <p className="error" role="alert">{error}</p>}
    </section>
  );
}
