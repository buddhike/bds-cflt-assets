import {useState} from 'react';
import {AppState, Step} from '../api';
import {useResize} from '../useResize';
import {SetupSteps} from './SetupSteps';

type Tone = 'idle' | 'busy' | 'ok' | 'bad';

/** The shared .dot tone of each state. */
const DOT: Record<Tone, string> = {idle: '', busy: 'busy', ok: 'ok', bad: 'danger'};

/** summarize reduces the setup steps to the one line the status bar shows. */
function summarize(steps: Step[], busy: AppState['busy']): {tone: Tone; text: string; done: number} {
  const done = steps.filter((s) => s.status === 'done').length;
  const running = steps.find((s) => s.status === 'running');
  const failed = steps.find((s) => s.status === 'failed');
  if (busy === 'teardown') return {tone: 'busy', text: 'Tearing down…', done};
  if (busy === 'setup') return {tone: 'busy', text: running ? `Setting up: ${running.title}` : 'Setting up…', done};
  if (failed) return {tone: 'bad', text: `Setup failed: ${failed.title}`, done};
  if (steps.length > 0 && done === steps.length) return {tone: 'ok', text: 'Ready', done};
  if (done > 0) return {tone: 'idle', text: 'Setup incomplete', done};
  return {tone: 'idle', text: 'Not set up', done};
}

interface Props {
  steps: Step[];
  busy: AppState['busy'];
  region: string;
  errorCount: number;
  warnCount: number;
  onOpenLogs: () => void;
}

/**
 * StatusBar sits along the bottom edge and says where setup is. Clicking it
 * draws out the list of setup steps above it.
 */
export function StatusBar({steps, busy, region, errorCount, warnCount, onOpenLogs}: Props) {
  const [open, setOpen] = useState(false);
  const {size, dragging, handleProps} = useResize({
    axis: 'y',
    initial: Math.min(280, Math.round(window.innerHeight * 0.35)),
    min: 120,
    max: () => Math.round(window.innerHeight * 0.45),
    storageKey: 'pims.status.height',
    reverse: true,
  });
  const {tone, text, done} = summarize(steps, busy);

  return (
    <footer className="status">
      {open && (
        <section id="status-drawer" className="status-drawer" style={{height: size}} aria-label="Setup steps">
          <div
            className={dragging ? 'resizer resizer-h is-dragging' : 'resizer resizer-h'}
            aria-label="Resize the setup panel"
            {...handleProps}
          />
          <div className="status-drawer-body">
            <header className="panel-head"><h2>Setup</h2></header>
            <SetupSteps steps={steps}/>
          </div>
        </section>
      )}

      <div className="statusbar">
        <button className="quiet sm status-toggle" aria-expanded={open} aria-controls="status-drawer" onClick={() => setOpen(!open)}>
          <span className={`dot ${DOT[tone]}`} aria-hidden="true"/>
          <span className="status-text" role="status">{text}</span>
          {steps.length > 0 && (
            <>
              <span
                className="meter"
                role="progressbar"
                aria-label="Setup steps done"
                aria-valuemin={0}
                aria-valuemax={steps.length}
                aria-valuenow={done}
              >
                <i style={{width: `${(done / steps.length) * 100}%`}}/>
              </span>
              <span className="num">{done}/{steps.length}</span>
            </>
          )}
          <svg className={open ? 'status-chevron status-chevron-open' : 'status-chevron'} viewBox="0 0 12 12" width="12" height="12" aria-hidden="true">
            <path d="M2 8l4-4 4 4" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round"/>
          </svg>
        </button>

        <span className="spacer"/>
        {errorCount > 0 && (
          <button className="quiet sm" onClick={onOpenLogs}>
            <span className="text-danger">{errorCount} {errorCount === 1 ? 'error' : 'errors'}</span>
          </button>
        )}
        {warnCount > 0 && (
          <button className="quiet sm" onClick={onOpenLogs}>
            <span className="text-warn">{warnCount} {warnCount === 1 ? 'warning' : 'warnings'}</span>
          </button>
        )}
        <span className="faint" title="AWS region">{region}</span>
      </div>
    </footer>
  );
}
