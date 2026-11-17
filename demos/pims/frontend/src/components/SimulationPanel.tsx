import {useEffect, useState} from 'react';
import {api, AppState, errorText, SimStats} from '../api';

const KIND_LABELS: Record<string, string> = {
  register_patient: 'Registrations',
  update_patient: 'Contact updates',
  book_appointment: 'Bookings',
  cancel_appointment: 'Cancellations',
  check_in: 'Check-ins',
  walk_in: 'Walk-ins',
  no_show: 'No-shows',
  complete_visit: 'Completed visits',
};

// The slider is logarithmic so both 0.5/s and 150/s are easy to pick.
const STEPS = 1000;
function toRate(v: number, min: number, max: number) {
  return min * Math.pow(max / min, v / STEPS);
}
function toSlider(rate: number, min: number, max: number) {
  return Math.round((Math.log(rate / min) / Math.log(max / min)) * STEPS);
}
function round(rate: number) {
  return rate < 10 ? Math.round(rate * 10) / 10 : Math.round(rate);
}

export function SimulationPanel({sim, limits, ready}: {sim: SimStats; limits: AppState['limits']; ready: boolean}) {
  const [rate, setRate] = useState(sim.rate || limits.defaultRate);
  const [error, setError] = useState('');
  const [editing, setEditing] = useState(false);

  // Follow the backend's rate unless the operator is dragging the slider.
  useEffect(() => {
    if (!editing && sim.rate) setRate(sim.rate);
  }, [sim.rate, editing]);

  async function call(fn: () => Promise<void>) {
    setError('');
    try {
      await fn();
    } catch (err) {
      setError(errorText(err));
    }
  }

  function commitRate(r: number) {
    setEditing(false);
    const clamped = Math.min(limits.maxRate, Math.max(limits.minRate, r));
    setRate(clamped);
    call(() => api.setRate(clamped));
  }

  const simulated = sim.simulatedAt && !sim.simulatedAt.startsWith('0001')
    ? new Date(sim.simulatedAt).toLocaleString(undefined, {weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit', timeZone: 'UTC'})
    : '—';

  return (
    <section className="card">
      <header className="card-head">
        <h2>Simulation</h2>
        <span className={`pill ${sim.running ? 'pill-on' : ''}`}>{sim.running ? '● Running' : 'Stopped'}</span>
      </header>

      <div className="rate">
        <label htmlFor="rate">Events per second</label>
        <input
          id="rate"
          type="range"
          min={0}
          max={STEPS}
          value={toSlider(rate, limits.minRate, limits.maxRate)}
          disabled={!ready}
          onChange={(e) => { setEditing(true); setRate(round(toRate(+e.target.value, limits.minRate, limits.maxRate))); }}
          onPointerUp={() => commitRate(rate)}
          onKeyUp={() => commitRate(rate)}
        />
        <input
          className="rate-number"
          type="number"
          min={limits.minRate}
          max={limits.maxRate}
          step="0.1"
          value={rate}
          disabled={!ready}
          onFocus={() => setEditing(true)}
          onChange={(e) => setRate(+e.target.value)}
          onBlur={() => commitRate(rate)}
          onKeyDown={(e) => e.key === 'Enter' && commitRate(rate)}
          aria-label="Events per second"
        />
      </div>

      <div className="actions">
        {sim.running
          ? <button onClick={() => call(api.stopSimulation)}>Stop</button>
          : <button className="primary" disabled={!ready} onClick={() => call(() => api.startSimulation(rate))}>Start</button>}
      </div>
      {!ready && <p className="fine">Available once setup has installed the schema.</p>}
      {error && <p className="error" role="alert">{error}</p>}

      <dl className="stats">
        <div><dt>Clinic clock (UTC)</dt><dd>{simulated}</dd></div>
        <div><dt>Events</dt><dd>{sim.events.toLocaleString()}</dd></div>
        <div><dt>Patients</dt><dd>{sim.patients.toLocaleString()}</dd></div>
        <div><dt>Upcoming appointments</dt><dd>{sim.scheduled.toLocaleString()}</dd></div>
        <div><dt>In clinic now</dt><dd>{sim.inClinic.toLocaleString()}</dd></div>
        <div><dt>Conflicts / errors</dt><dd>{sim.conflicts} / {sim.errors}</dd></div>
      </dl>

      <table className="kinds">
        <tbody>
          {Object.entries(KIND_LABELS).map(([k, label]) => (
            <tr key={k}><th scope="row">{label}</th><td>{(sim.byKind?.[k] ?? 0).toLocaleString()}</td></tr>
          ))}
        </tbody>
      </table>
      {sim.lastError && <p className="error">Last error: {sim.lastError}</p>}
    </section>
  );
}
