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

/**
 * SimulationBar sits just below the header. It always has the rate and the
 * start and stop control; once the simulator has run it also shows the
 * condensed counts, with the per-kind breakdown one click away.
 */
export function SimulationBar({sim, limits, ready}: {sim: SimStats; limits: AppState['limits']; ready: boolean}) {
  const [rate, setRate] = useState(sim.rate || limits.defaultRate);
  const [error, setError] = useState('');
  const [editing, setEditing] = useState(false);
  const [breakdown, setBreakdown] = useState(false);

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
  // The counts outlive a stop, so they stay until there is nothing to count.
  const showStats = sim.running || sim.events > 0;

  return (
    <section className="simbar" aria-label="Simulation">
      <div className="simbar-row">
        <h2>Simulation</h2>
        <span className={`pill ${sim.running ? 'pill-on' : ''}`}>{sim.running ? '● Running' : 'Stopped'}</span>

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

        {sim.running
          ? <button onClick={() => call(api.stopSimulation)}>Stop</button>
          : <button className="primary" disabled={!ready} onClick={() => call(() => api.startSimulation(rate))}>Start</button>}

        {!ready && <span className="fine simbar-hint">Available once setup has installed the schema.</span>}

        {showStats && (
          <>
            <dl className="simbar-stats">
              <div><dt>Clinic clock (UTC)</dt><dd>{simulated}</dd></div>
              <div><dt>Events</dt><dd>{sim.events.toLocaleString()}</dd></div>
              <div><dt>Patients</dt><dd>{sim.patients.toLocaleString()}</dd></div>
              <div><dt>Upcoming</dt><dd>{sim.scheduled.toLocaleString()}</dd></div>
              <div><dt>In clinic</dt><dd>{sim.inClinic.toLocaleString()}</dd></div>
              <div><dt>Conflicts / errors</dt><dd>{sim.conflicts} / {sim.errors}</dd></div>
            </dl>
            <button className="link" aria-expanded={breakdown} aria-controls="sim-breakdown" onClick={() => setBreakdown(!breakdown)}>
              {breakdown ? 'Hide breakdown' : 'Breakdown'}
            </button>
          </>
        )}
      </div>

      {showStats && breakdown && (
        <dl id="sim-breakdown" className="simbar-kinds">
          {Object.entries(KIND_LABELS).map(([k, label]) => (
            <div key={k}><dt>{label}</dt><dd>{(sim.byKind?.[k] ?? 0).toLocaleString()}</dd></div>
          ))}
        </dl>
      )}
      {error && <p className="error" role="alert">{error}</p>}
      {sim.lastError && <p className="error">Last error: {sim.lastError}</p>}
    </section>
  );
}
