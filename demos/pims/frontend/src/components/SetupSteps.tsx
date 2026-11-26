import {Step} from '../api';

const ICON: Record<Step['status'], string> = {pending: '○', running: '●', done: '✓', failed: '✕'};
const LABEL: Record<Step['status'], string> = {pending: 'Pending', running: 'Running', done: 'Done', failed: 'Failed'};
const CLASS: Record<Step['status'], string> = {pending: '', running: 'run', done: 'done', failed: 'fail'};

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
        <li key={s.id} className={`step ${CLASS[s.status]}`}>
          <span className="ico" aria-label={LABEL[s.status]} title={LABEL[s.status]}>{ICON[s.status]}</span>
          <span>
            {s.title}
            {s.detail && <small>{s.detail}</small>}
          </span>
          <time>{duration(s)}</time>
        </li>
      ))}
    </ol>
  );
}
