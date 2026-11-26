import {useLayoutEffect, useMemo, useRef, useState} from 'react';
import {LatencyResult, LatencySample} from '../api';
import {Notice} from './Notice';

// Fixed series order: identity follows the table, never its rank. The
// three hues are the first slots of the validated categorical palette
// (all-pairs safe); aqua sits under 3:1 on the light surface, so the latest
// values are also shown as text in the tiles above the charts.
export const SERIES = [
  {table: 'patients', label: 'Patients', color: 'var(--s1)'},
  {table: 'appointments', label: 'Appointments', color: 'var(--s2)'},
  {table: 'visits', label: 'Visits', color: 'var(--s3)'},
];

const M = {top: 12, right: 16, bottom: 28, left: 56};
const HEIGHT = 240;
const SECOND = 1000;
const MINUTE = 60 * SECOND;

/** fmtDuration renders milliseconds as 850ms, 12.3s, 2m 05s or 1h 02m. */
export function fmtDuration(ms: number): string {
  if (ms < SECOND) return `${Math.round(ms)}ms`;
  if (ms < 99_950) return `${(ms / SECOND).toFixed(1)}s`;
  const total = Math.round(ms / SECOND);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const pad = (n: number) => String(n).padStart(2, '0');
  if (h > 0) return m ? `${h}h ${pad(m)}m` : `${h}h`;
  return s ? `${m}m ${pad(s)}s` : `${m}m`;
}

function fmtTime(t: number) {
  return new Date(t).toLocaleTimeString(undefined, {hour: '2-digit', minute: '2-digit'});
}

/** niceTicks returns ~count round values covering [0, max]; the last tick is at or above max. */
function niceTicks(max: number, count = 4): number[] {
  if (max <= 0) return [0];
  const raw = max / count;
  const mag = Math.pow(10, Math.floor(Math.log10(raw)));
  const step = [1, 2, 5, 10].map((m) => m * mag).find((s) => s >= raw) ?? raw;
  const top = Math.ceil(max / step - 1e-9) * step;
  const ticks = [];
  for (let v = 0; v <= top + step * 0.01; v += step) ticks.push(v);
  return ticks;
}

/** yAxis picks seconds, or minutes for large values, and round ticks covering [0, maxMs]. */
function yAxis(maxMs: number) {
  const unit = maxMs >= 10 * MINUTE ? MINUTE : SECOND;
  const ticks = niceTicks(Math.max(1, maxMs / unit)).map((v) => v * unit);
  const label = (ms: number) => `${+(ms / unit).toFixed(2)}${unit === MINUTE ? 'm' : 's'}`;
  return {ticks, label, max: ticks[ticks.length - 1] || unit};
}

function lastFor(samples: LatencySample[], table: string): LatencySample | undefined {
  for (let i = samples.length - 1; i >= 0; i--) if (samples[i].table === table) return samples[i];
  return undefined;
}

interface Row {
  table: string;
  t: number;
  event?: number;
  processing?: number;
}

/** mergeRows joins the two metrics on (table, minute), newest first. */
function mergeRows(event: LatencySample[], processing: LatencySample[]): Row[] {
  const rows = new Map<string, Row>();
  const add = (samples: LatencySample[], key: 'event' | 'processing') => {
    for (const s of samples) {
      const t = Date.parse(s.time);
      const id = `${s.table}@${t}`;
      const row = rows.get(id) ?? {table: s.table, t};
      row[key] = s.ms;
      rows.set(id, row);
    }
  };
  add(event, 'event');
  add(processing, 'processing');
  const order = (table: string) => SERIES.findIndex((s) => s.table === table);
  return [...rows.values()].sort((a, b) => b.t - a.t || order(a.table) - order(b.table));
}

/** MetricChart is a line chart of one metric, a line per table. */
function MetricChart({title, subtitle, samples}: {title: string; subtitle: string; samples: LatencySample[]}) {
  const wrap = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(520);
  const [hoverX, setHoverX] = useState<number | null>(null);

  useLayoutEffect(() => {
    const el = wrap.current;
    if (!el) return;
    const ro = new ResizeObserver(([e]) => setWidth(Math.max(280, e.contentRect.width)));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const series = useMemo(() => SERIES.map((s) => ({
    ...s,
    pts: samples
      .filter((p) => p.table === s.table)
      .map((p) => ({t: Date.parse(p.time), v: p.ms}))
      .sort((a, b) => a.t - b.t),
  })), [samples]);

  const all = series.flatMap((s) => s.pts);
  const innerW = width - M.left - M.right;
  const innerH = HEIGHT - M.top - M.bottom;
  const tMin = all.length ? Math.min(...all.map((p) => p.t)) : Date.now() - 600_000;
  const tMaxRaw = all.length ? Math.max(...all.map((p) => p.t)) : Date.now();
  const tMax = tMaxRaw === tMin ? tMin + MINUTE : tMaxRaw;
  const axis = yAxis(Math.max(SECOND, ...all.map((p) => p.v)) * 1.1);
  const x = (t: number) => M.left + ((t - tMin) / (tMax - tMin)) * innerW;
  const y = (v: number) => M.top + innerH - (Math.max(0, v) / axis.max) * innerH;
  const xTicks = Array.from({length: 4}, (_, i) => tMin + ((tMax - tMin) * i) / 3);

  // Crosshair: the nearest point per series to the pointer's time.
  const hoverT = hoverX === null ? null : tMin + ((hoverX - M.left) / innerW) * (tMax - tMin);
  const nearest = hoverT === null ? [] : series
    .filter((s) => s.pts.length)
    .map((s) => ({s, pt: s.pts.reduce((a, b) => (Math.abs(b.t - hoverT) < Math.abs(a.t - hoverT) ? b : a))}));
  const crossT = nearest.length ? nearest.reduce((a, b) => (Math.abs(b.pt.t - hoverT!) < Math.abs(a.pt.t - hoverT!) ? b : a)).pt.t : null;

  return (
    <div className="metric">
      <h3>{title}</h3>
      <p className="small faint">{subtitle}</p>
      <div ref={wrap} className="chart-wrap">
        <svg
          width={width}
          height={HEIGHT}
          role="img"
          aria-label={`Line chart of ${title.toLowerCase()} per table over the last 5 hours`}
          onPointerMove={(e) => {
            const r = (e.currentTarget as SVGSVGElement).getBoundingClientRect();
            const px = e.clientX - r.left;
            setHoverX(px >= M.left && px <= M.left + innerW ? px : null);
          }}
          onPointerLeave={() => setHoverX(null)}
        >
          {axis.ticks.map((v) => (
            <g key={v}>
              <line className="grid" x1={M.left} x2={M.left + innerW} y1={y(v)} y2={y(v)}/>
              <text className="axis-label" x={M.left - 8} y={y(v)} textAnchor="end" dominantBaseline="middle">{axis.label(v)}</text>
            </g>
          ))}
          {xTicks.map((t, i) => (
            <text
              key={i}
              className="axis-label"
              x={x(t)}
              y={HEIGHT - 8}
              textAnchor={i === 0 ? 'start' : i === xTicks.length - 1 ? 'end' : 'middle'}
            >
              {fmtTime(t)}
            </text>
          ))}

          {series.map((s) => s.pts.length > 0 && (
            <g key={s.table}>
              <polyline
                fill="none"
                stroke={s.color}
                strokeWidth={2}
                strokeLinejoin="round"
                strokeLinecap="round"
                points={s.pts.map((p) => `${x(p.t)},${y(p.v)}`).join(' ')}
              />
              {/* The latest value gets a marker, which also shows a table with a single sample. */}
              <circle cx={x(s.pts[s.pts.length - 1].t)} cy={y(s.pts[s.pts.length - 1].v)} r={4} fill={s.color} stroke="var(--surface)" strokeWidth={2}/>
            </g>
          ))}

          {crossT !== null && <line className="crosshair" x1={x(crossT)} x2={x(crossT)} y1={M.top} y2={M.top + innerH}/>}

          {all.length === 0 && (
            <text className="empty" x={M.left + innerW / 2} y={M.top + innerH / 2} textAnchor="middle">
              Waiting for Tableflow to report this metric…
            </text>
          )}
        </svg>

        {crossT !== null && nearest.length > 0 && (
          <div className="tooltip" style={{left: Math.max(0, Math.min(x(crossT) + 12, width - 190)), top: M.top}}>
            <div className="tooltip-head">{new Date(crossT).toLocaleTimeString()}</div>
            {nearest.map(({s, pt}) => (
              <div key={s.table} className="tooltip-row">
                <i className="swatch" style={{background: s.color}}/>
                <span>{s.label}</span>
                <span className="num">{fmtDuration(pt.v)}</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

export function LatencyChart({latency}: {latency: LatencyResult}) {
  const [showTable, setShowTable] = useState(false);
  const event = useMemo(() => latency.event ?? [], [latency.event]);
  const processing = useMemo(() => latency.processing ?? [], [latency.processing]);
  const rows = useMemo(() => mergeRows(event, processing), [event, processing]);
  const hasData = event.length + processing.length > 0;

  const latest = SERIES.map((s) => {
    const e = lastFor(event, s.table);
    const p = lastFor(processing, s.table);
    const t = Math.max(e ? Date.parse(e.time) : 0, p ? Date.parse(p.time) : 0);
    return {...s, e, p, t};
  });

  return (
    <section className="pane latency">
      <header className="panel-head">
        <h2>Tableflow freshness latency</h2>
        <div className="head-actions">
          <span className="pill" title="Tableflow's freshness metrics are in Preview and can change before general availability.">Preview metrics</span>
          <button className="link" onClick={() => setShowTable(!showTable)}>{showTable ? 'Show charts' : 'Show table'}</button>
        </div>
      </header>
      <p className="small faint">
        Tableflow's own freshness metrics, read from the Confluent Cloud Metrics API once a minute: one value per table
        per minute over the last 5 hours. Tableflow updates them when it commits to a table, so they move in steps, and
        they reach the API about three minutes late.
        {latency.observedAt && <> Last read {new Date(latency.observedAt).toLocaleTimeString()}.</>}
      </p>

      <div className="kpis">
        {latest.map((s) => (
          <div key={s.table} className="kpi">
            <span className="label"><i className="swatch" style={{background: s.color}}/>{s.label}</span>
            <div className="kpi-pair">
              <div>
                <span className="label">Event</span>
                <span className="kpi-value">{s.e ? fmtDuration(s.e.ms) : '—'}</span>
              </div>
              <div>
                <span className="label">Processing</span>
                <span className="kpi-value">{s.p ? fmtDuration(s.p.ms) : '—'}</span>
              </div>
            </div>
            <span className="kpi-sub">{s.t ? `as of ${fmtTime(s.t)}` : (latency.error ? 'metrics unavailable' : 'waiting for Tableflow to report')}</span>
          </div>
        ))}
      </div>

      <ul className="legend" aria-label="Legend">
        {SERIES.map((s) => <li key={s.table}><i className="swatch" style={{background: s.color}}/>{s.label}</li>)}
      </ul>

      {showTable ? (
        <div className="table-box table-wrap">
          <table className="data">
            <thead><tr><th>Minute</th><th>Table</th><th>Event freshness</th><th>Processing freshness</th></tr></thead>
            <tbody>
              {rows.map((r) => (
                <tr key={`${r.table}@${r.t}`}>
                  <td>{new Date(r.t).toLocaleTimeString()}</td>
                  <td>{r.table}</td>
                  <td className="num">{r.event === undefined ? '—' : fmtDuration(r.event)}</td>
                  <td className="num">{r.processing === undefined ? '—' : fmtDuration(r.processing)}</td>
                </tr>
              ))}
              {rows.length === 0 && <tr><td colSpan={4} className="empty">No measurements yet</td></tr>}
            </tbody>
          </table>
        </div>
      ) : (
        <div className="charts">
          <MetricChart
            title="Event freshness"
            subtitle="Age of the newest record Tableflow has written: from the record's timestamp to when it reached the table."
            samples={event}
          />
          <MetricChart
            title="Processing freshness"
            subtitle="Tableflow's own processing time: from picking a record up to writing it to the table."
            samples={processing}
          />
        </div>
      )}

      {latency.error && (
        <Notice tone="warn" title="Could not read Tableflow's metrics.">
          {hasData ? 'Showing the last reading. ' : ''}{latency.error}
        </Notice>
      )}
    </section>
  );
}
