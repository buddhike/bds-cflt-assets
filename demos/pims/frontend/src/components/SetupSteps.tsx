import {Step} from '../api';

const ICON: Record<Step['status'], string> = {pending: '○', running: '◐', done: '✓', failed: '✕'};
const LABEL: Record<Step['status'], string> = {pending: 'Pending', running: 'Running', done: 'Done', failed: 'Failed'};

function duration(s: Step): string {
  if (!s.startedAt) return '';
  const end = s.finishedAt ? Date.parse(s.finishedAt) : Date.now();
  const secs = Math.max(0, Math.round((end - Date.parse(s.startedAt)) / 1000));
  return secs < 60 ? `${secs}s` : `${Math.floor(secs / 60)}m ${secs % 60}s`;
}

export function SetupSteps({steps}: {steps: Step[]}) {
  return (
    <ol className="steps">
      {steps.map((s) => (
        <li key={s.id} className={`step step-${s.status}`}>
          <span className="step-icon" aria-label={LABEL[s.status]} title={LABEL[s.status]}>{ICON[s.status]}</span>
          <span className="step-body">
            <span className="step-title">{s.title}</span>
            {s.detail && <span className="step-detail">{s.detail}</span>}
          </span>
          <span className="step-time">{duration(s)}</span>
        </li>
      ))}
    </ol>
  );
}
