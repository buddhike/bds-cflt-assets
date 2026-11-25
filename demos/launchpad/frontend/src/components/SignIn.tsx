import {FormEvent, useState} from 'react';
import {api, errorText, User} from '../api';

interface Props {
  onSignedIn: () => void;
}

/** Email, then the code we send to it. New users are registered by their first successful code. */
export function SignIn({onSignedIn}: Props) {
  const [email, setEmail] = useState('');
  const [code, setCode] = useState('');
  const [step, setStep] = useState<'email' | 'code'>('email');
  const [outcome, setOutcome] = useState<User['status'] | null>(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  async function run(fn: () => Promise<void>) {
    setBusy(true);
    setError('');
    try {
      await fn();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  const sendCode = (e: FormEvent) => {
    e.preventDefault();
    return run(async () => {
      await api.start(email);
      setStep('code');
    });
  };

  const verify = (e: FormEvent) => {
    e.preventDefault();
    return run(async () => {
      const res = await api.verify(email, code);
      if (res.status === 'approved') onSignedIn();
      else setOutcome(res.status);
    });
  };

  if (outcome === 'pending') {
    return (
      <main className="gate">
        <h1>Request received</h1>
        <p>
          Your email is verified. An administrator needs to approve your access before you can use Launchpad. Come
          back and sign in again once they have.
        </p>
        <button onClick={() => { setOutcome(null); setStep('email'); setCode(''); }}>Back</button>
      </main>
    );
  }
  if (outcome === 'denied') {
    return (
      <main className="gate">
        <h1>Access was not approved</h1>
        <p>An administrator declined this request. Ask them if you think that is a mistake.</p>
        <button onClick={() => { setOutcome(null); setStep('email'); setCode(''); }}>Back</button>
      </main>
    );
  }

  return (
    <main className="gate">
      <h1>Launchpad</h1>
      <p className="muted">Launch and tear down demos in AWS.</p>
      {step === 'email' ? (
        <form onSubmit={sendCode}>
          <label>
            Work email
            <input
              type="email"
              autoComplete="email"
              autoFocus
              required
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="you@confluent.io"
            />
          </label>
          <button className="primary" disabled={busy}>{busy ? 'Sending…' : 'Email me a code'}</button>
        </form>
      ) : (
        <form onSubmit={verify}>
          <p>We sent a six digit code to <strong>{email}</strong>.</p>
          <label>
            Code
            <input
              inputMode="numeric"
              autoComplete="one-time-code"
              autoFocus
              required
              pattern="[0-9]{6}"
              maxLength={6}
              value={code}
              onChange={(e) => setCode(e.target.value.replace(/\D/g, ''))}
            />
          </label>
          <div className="row">
            <button className="primary" disabled={busy || code.length !== 6}>{busy ? 'Checking…' : 'Sign in'}</button>
            <button type="button" disabled={busy} onClick={() => { setStep('email'); setCode(''); setError(''); }}>
              Use another email
            </button>
          </div>
        </form>
      )}
      {error && <p className="error" role="alert">{error}</p>}
    </main>
  );
}
