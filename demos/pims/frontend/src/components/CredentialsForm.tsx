import {FormEvent, useEffect, useState} from 'react';
import {api, errorText} from '../api';
import {Logo} from './Logo';
import {Notice} from './Notice';

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
    <main>
      <form className="gate stack" onSubmit={submit}>
        <h1 className="brand"><Logo/>PIMS</h1>
        <p className="muted">
          Patient Information Management System: a demo of Debezium CDC from MySQL into Confluent Cloud Tableflow
          Iceberg tables, with Tableflow's freshness metrics charted live.
        </p>

        <Notice tone="warn" title="Don't use credentials that can reach production.">
          PIMS creates and deletes Confluent Cloud resources. Use an API key for a dedicated demo or sandbox organisation.
        </Notice>

        <div className="field">
          <label htmlFor="region">AWS region</label>
          <input id="region" list="regions" value={region} onChange={(e) => setRegion(e.target.value)} required spellCheck={false}/>
          <datalist id="regions">{REGIONS.map((r) => <option key={r} value={r}/>)}</datalist>
        </div>
        <div className="field">
          <label htmlFor="api-key">Confluent Cloud API key</label>
          <input id="api-key" className="mono" value={apiKey} onChange={(e) => setApiKey(e.target.value)} required autoComplete="off" spellCheck={false}/>
        </div>
        <div className="field">
          <label htmlFor="api-secret">Confluent Cloud API secret</label>
          <input id="api-secret" className="mono" type="password" value={apiSecret} onChange={(e) => setApiSecret(e.target.value)} required autoComplete="off"/>
        </div>

        {error && <Notice tone="danger" alert>{error}</Notice>}

        <div><button type="submit" className="primary" disabled={saving}>{saving ? 'Checking…' : 'Continue'}</button></div>

        <p className="small faint">
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
