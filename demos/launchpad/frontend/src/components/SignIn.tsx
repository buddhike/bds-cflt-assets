import {FormEvent, useState} from 'react';
import {api, errorText} from '../api';
import {Logo} from './Logo';
import {Notice} from './Notice';

interface Props {
  onSignedIn: () => void;
}

/**
 * The owner's phone number, then the code texted to it. The page is told the
 * code was sent whatever the number is, so it cannot be used to find the right
 * one. If the number is wrong, no code arrives and no code works.
 */
export function SignIn({onSignedIn}: Props) {
  const [phone, setPhone] = useState('');
  const [code, setCode] = useState('');
  const [step, setStep] = useState<'phone' | 'code'>('phone');
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
      await api.start(phone);
      setStep('code');
    });
  };

  const verify = (e: FormEvent) => {
    e.preventDefault();
    return run(async () => {
      try {
        await api.verify(code);
        onSignedIn();
      } catch (err) {
        setCode('');
        throw err;
      }
    });
  };

  return (
    <main className="gate">
      <span className="brand"><Logo />Launchpad</span>
      <div>
        <h1>Sign in</h1>
        <p className="muted">Launch and tear down demos in AWS.</p>
      </div>
      {step === 'phone' ? (
        <form className="stack" onSubmit={sendCode}>
          <div className="field">
            <label htmlFor="phone">Phone number</label>
            <input
              id="phone"
              type="tel"
              autoComplete="tel"
              autoFocus
              required
              value={phone}
              onChange={(e) => setPhone(e.target.value)}
              placeholder="+61400000000"
            />
            <span className="hint">The owner's number, with the country code.</span>
          </div>
          <div><button className="primary" disabled={busy}>{busy ? 'Sending…' : 'Text me a code'}</button></div>
        </form>
      ) : (
        <form className="stack" onSubmit={verify}>
          <p>We sent a six digit code to <strong>{phone}</strong>.</p>
          <div className="field">
            <label htmlFor="code">Code</label>
            <input
              id="code"
              className="mono"
              inputMode="numeric"
              autoComplete="one-time-code"
              autoFocus
              required
              pattern="[0-9]{6}"
              maxLength={6}
              value={code}
              onChange={(e) => setCode(e.target.value.replace(/\D/g, ''))}
            />
          </div>
          <div className="row">
            <button className="primary" disabled={busy || code.length !== 6}>{busy ? 'Checking…' : 'Sign in'}</button>
            <button type="button" className="quiet" disabled={busy} onClick={() => { setStep('phone'); setCode(''); setError(''); }}>
              Use another number
            </button>
          </div>
        </form>
      )}
      {error && <Notice tone="danger" alert>{error}</Notice>}
    </main>
  );
}
