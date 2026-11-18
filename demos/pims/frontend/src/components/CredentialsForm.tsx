import {FormEvent, useEffect, useState} from 'react';
import {api, errorText} from '../api';
import {Logo} from './Logo';

// Regions where Confluent Cloud offers Standard clusters and Tableflow.
// Any valid region code can still be typed in.
const REGIONS = ['us-east-1', 'us-east-2', 'us-west-2', 'ca-central-1', 'eu-west-1', 'eu-west-2', 'eu-central-1', 'ap-southeast-1', 'ap-southeast-2', 'ap-northeast-1', 'ap-south-1'];

export function CredentialsForm({onSaved}: {onSaved: () => void}) {
  const [region, setRegion] = useState('us-east-1');
  const [apiKey, setApiKey] = useState('');
  const [apiSecret, setApiSecret] = useState('');
  const [workspace, setWorkspace] = useState('');
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    api.getDefaults().then((d) => {
      setRegion(d.awsRegion);
      setWorkspace(d.workspace);
    });
  }, []);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setSaving(true);
    setError('');
    try {
      await api.setCredentials({awsRegion: region, apiKey, apiSecret});
      setApiSecret('');
      onSaved();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setSaving(false);
    }
  }

  return (
    <main className="gate">
      <form className="card gate-card" onSubmit={submit}>
        <div className="brand">
          <Logo className="brand-mark"/>
          <h1>PIMS</h1>
        </div>
        <p className="lede">
          Patient Information Management System: a demo of Debezium CDC from MySQL into Confluent Cloud Tableflow
          Iceberg tables, with end-to-end latency measured in DuckDB.
        </p>

        <p className="notice" role="note">
          <svg className="notice-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
               strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z"/>
            <path d="M12 9v4M12 17h.01"/>
          </svg>
          <span>
            <strong>Don't use credentials that can reach production.</strong> PIMS creates and deletes Confluent
            Cloud resources. Use an API key for a dedicated demo or sandbox organisation.
          </span>
        </p>

        <label>
          AWS region
          <input list="regions" value={region} onChange={(e) => setRegion(e.target.value)} required spellCheck={false}/>
          <datalist id="regions">{REGIONS.map((r) => <option key={r} value={r}/>)}</datalist>
        </label>
        <label>
          Confluent Cloud API key
          <input value={apiKey} onChange={(e) => setApiKey(e.target.value)} required autoComplete="off" spellCheck={false}/>
        </label>
        <label>
          Confluent Cloud API secret
          <input type="password" value={apiSecret} onChange={(e) => setApiSecret(e.target.value)} required autoComplete="off"/>
        </label>

        {error && <p className="error" role="alert">{error}</p>}

        <button type="submit" className="primary" disabled={saving}>{saving ? 'Checking…' : 'Continue'}</button>

        <p className="fine">
          Use a Cloud resource management API key that can create environments. The key and secret stay in memory for
          this session only; PIMS remembers just the region. Tableflow's native Debezium support is in Early Access, so
          your organisation must be enrolled. Provisioned resources are billed to your Confluent Cloud account until you
          tear the demo down.
          {workspace && <> Working files: <code>{workspace}</code></>}
        </p>
      </form>
    </main>
  );
}
