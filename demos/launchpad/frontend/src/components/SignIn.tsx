import {FormEvent, useEffect, useRef, useState} from 'react';
import {api, ApiError, errorText} from '../api';

interface Props {
  onSignedIn: () => void;
}

type Step = 'sending' | 'code' | 'failed';

/**
 * Whoever arrives without a session is sent a code, by text message, to the
 * number the owner keeps in Secrets Manager. There is no address to type: the
 * page cannot choose where the code goes.
 */
export function SignIn({onSignedIn}: Props) {
  const [step, setStep] = useState<Step>('sending');
  const [note, setNote] = useState('');
  const [code, setCode] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const asked = useRef(false);

  async function requestCode(resend: boolean) {
    setBusy(true);
    setError('');
    setNote('');
    try {
      const {sent} = await api.start(resend);
      setNote(sent ? 'We sent a six digit code to your phone.' : 'A code was sent to your phone a moment ago. Enter it below.');
      setStep('code');
    } catch (err) {
      if (err instanceof ApiError && err.code === 'too_soon') {
        // A code went out in the last half minute, so there is one to enter.
        setNote('A code was sent to your phone a moment ago. Enter it below.');
        setStep('code');
      } else {
        setError(errorText(err));
        setStep(resend && step === 'code' ? 'code' : 'failed');
      }
    } finally {
      setBusy(false);
    }
  }

  // Once per visit. The ref keeps development's double effect from asking twice.
  useEffect(() => {
    if (asked.current) return;
    asked.current = true;
    void requestCode(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function verify(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError('');
    try {
      await api.verify(code);
      onSignedIn();
    } catch (err) {
      setError(errorText(err));
      setCode('');
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="gate">
      <h1>Launchpad</h1>
      <p className="muted">Launch and tear down demos in AWS.</p>
      {step === 'sending' && <p className="muted" role="status">Sending a code to your phone…</p>}
      {step === 'code' && (
        <form onSubmit={verify}>
          <p>{note}</p>
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
            <button type="button" disabled={busy} onClick={() => { setCode(''); void requestCode(true); }}>
              Send a new code
            </button>
          </div>
        </form>
      )}
      {step === 'failed' && <button onClick={() => void requestCode(false)} disabled={busy}>Try again</button>}
      {error && <p className="error" role="alert">{error}</p>}
    </main>
  );
}
