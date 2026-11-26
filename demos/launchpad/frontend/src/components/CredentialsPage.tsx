import {FormEvent, useState} from 'react';
import {api, Credentials, errorText} from '../api';
import {Notice} from './Notice';

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
    <section className="narrow stack">
      <div className="page-head">
        <div>
          <h1>Confluent Cloud key</h1>
          <p>
            Demos that create Confluent Cloud resources use your Cloud API key. It is encrypted before it is stored, is
            never shown again, and is only decrypted while a demo you launch is being deployed or torn down.
          </p>
        </div>
      </div>
      {current?.configured ? (
        <Notice tone="ok" title="Key saved.">
          A key ending in <code>{current.apiKeyHint}</code> is saved.{' '}
          <button className="link" disabled={busy} onClick={remove}>Delete it</button>
        </Notice>
      ) : (
        <Notice>No key is saved.</Notice>
      )}
      <form className="card stack" onSubmit={save} autoComplete="off">
        <div className="field">
          <label htmlFor="api-key">Cloud API key</label>
          <input id="api-key" className="mono" required value={apiKey} onChange={(e) => setApiKey(e.target.value)} autoComplete="off" spellCheck={false} />
        </div>
        <div className="field">
          <label htmlFor="api-secret">Cloud API secret</label>
          <input
            id="api-secret"
            className="mono"
            required
            type="password"
            value={apiSecret}
            onChange={(e) => setApiSecret(e.target.value)}
            autoComplete="new-password"
            spellCheck={false}
          />
        </div>
        <div><button className="primary" disabled={busy}>{current?.configured ? 'Replace key' : 'Save key'}</button></div>
      </form>
      {saved && <Notice tone="ok">Saved.</Notice>}
      {error && <Notice tone="danger" alert>{error}</Notice>}
    </section>
  );
}
