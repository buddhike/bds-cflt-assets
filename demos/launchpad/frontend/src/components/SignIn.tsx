import {FormEvent, useState} from 'react';
import {api, errorText} from '../api';
import {Logo} from './Logo';

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
      <Logo size={40} />
      <h1>Launchpad</h1>
      <p className="muted">Launch and tear down demos in AWS.</p>
      {step === 'phone' ? (
        <form onSubmit={sendCode}>
          <label>
            Phone number
            <input
              type="tel"
              autoComplete="tel"
              autoFocus
              required
              value={phone}
              onChange={(e) => setPhone(e.target.value)}
              placeholder="+61400000000"
            />
          </label>
          <button className="primary" disabled={busy}>{busy ? 'Sending…' : 'Text me a code'}</button>
        </form>
      ) : (
        <form onSubmit={verify}>
          <p>We sent a six digit code to <strong>{phone}</strong>.</p>
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
            <button type="button" disabled={busy} onClick={() => { setStep('phone'); setCode(''); setError(''); }}>
              Use another number
            </button>
          </div>
        </form>
      )}
      {error && <p className="error" role="alert">{error}</p>}
    </main>
  );
}
