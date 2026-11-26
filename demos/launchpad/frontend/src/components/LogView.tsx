import {useCallback, useEffect, useRef, useState} from 'react';
import {api, ApiError, Demo, errorText} from '../api';
import {BUSY, statusText, tone} from '../status';
import {Notice} from './Notice';

// How far from the bottom the reader may be and still count as following the log.
const FOLLOW_SLACK = 24;

interface Props {
  id: string;
  onBack: () => void;
}

/** The whole log of one demo's latest run, on a page of its own. */
export function LogView({id, onBack}: Props) {
  const [demo, setDemo] = useState<Demo | null>(null);
  const [error, setError] = useState('');
  const [gone, setGone] = useState(false);
  const box = useRef<HTMLPreElement>(null);
  const following = useRef(true);

  const load = useCallback(async () => {
    try {
      setDemo(await api.demo(id));
      setError('');
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) setGone(true);
      else setError(errorText(err));
    }
  }, [id]);

  useEffect(() => { void load(); }, [load]);

  const status = demo?.state?.status;
  const busy = !!status && BUSY.includes(status);
  useEffect(() => {
    // The log only grows while a run is going, so otherwise there is little to ask for.
    const t = setInterval(() => { void load(); }, busy ? 3000 : 30000);
    return () => clearInterval(t);
  }, [busy, load]);

  const log = demo?.state?.logTail ?? '';
  // Stay at the end of a growing log, unless the reader scrolled up to read something.
  useEffect(() => {
    const el = box.current;
    if (el && following.current) el.scrollTop = el.scrollHeight;
  }, [log]);

  function onScroll() {
    const el = box.current;
    if (el) following.current = el.scrollHeight - el.scrollTop - el.clientHeight < FOLLOW_SLACK;
  }

  function toEnd() {
    const el = box.current;
    if (!el) return;
    following.current = true;
    el.scrollTop = el.scrollHeight;
  }

  const name = demo?.name ?? id;
  return (
    <section className="log-view">
      <div className="log-head">
        <button className="quiet" onClick={onBack}>← Demos</button>
        <h1>{name}</h1>
        {status && <span className={`pill ${tone(status)}`}>{statusText(status)}</span>}
        <span className="grow" />
        {log && <button onClick={toEnd}>Jump to end</button>}
      </div>
      {error && <Notice tone="danger" alert>{error}</Notice>}
      {gone && <p className="muted">There is no demo called “{id}”.</p>}
      {demo && !demo.state && <p className="muted">This demo is not launched, so it has no log.</p>}
      {!demo && !error && !gone && <p className="muted">Loading…</p>}
      {demo?.state && (
        <pre className="log" ref={box} onScroll={onScroll} tabIndex={0} aria-label={`${name} log`}>
          {log || 'No output yet.'}
        </pre>
      )}
    </section>
  );
}
