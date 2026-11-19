import {useEffect, useRef, useState} from 'react';
import {LogEntry} from '../api';

export function ActivityLog({logs}: {logs: LogEntry[]}) {
  const box = useRef<HTMLDivElement>(null);
  const [follow, setFollow] = useState(true);

  useEffect(() => {
    if (follow && box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [logs, follow]);

  return (
    <section className="card log-card">
      <header className="card-head">
        <h2>Activity</h2>
        <label className="inline"><input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)}/> Follow</label>
      </header>
      <div className="log" ref={box} role="log" aria-live="polite">
        {logs.map((l, i) => (
          <div key={i} className={`log-line log-${l.level.toLowerCase()}`}>
            <span className="log-time">{new Date(l.time).toLocaleTimeString()}</span>
            <span className="log-level">{l.level}</span>
            <span className="log-text">
              <span className="log-msg">{l.message}</span>
              {l.attrs && <>{' '}<span className="log-attrs">{l.attrs}</span></>}
            </span>
          </div>
        ))}
      </div>
    </section>
  );
}
