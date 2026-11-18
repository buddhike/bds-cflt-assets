import {useLayoutEffect, useMemo, useRef, useState} from 'react';
import {LatencyPoint, TableError} from '../api';

// Fixed series order: identity follows the table, never its rank. The
// three hues are the first slots of the validated categorical palette
// (all-pairs safe); aqua sits under 3:1 on the light surface, so every
// series is also direct-labelled and the latest values are shown as text.
export const SERIES = [
  {table: 'patients', label: 'Patients', color: 'var(--series-1)'},
  {table: 'appointments', label: 'Appointments', color: 'var(--series-2)'},
  {table: 'visits', label: 'Visits', color: 'var(--series-3)'},
];

const M = {top: 16, right: 150, bottom: 32, left: 52};
const HEIGHT = 300;

function fmtSecs(ms: number) {
  const s = ms / 1000;
  return s < 100 ? `${s.toFixed(1)}s` : `${Math.round(s)}s`;
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

export function LatencyChart({points, errors}: {points: LatencyPoint[]; errors: TableError[]}) {
  const wrap = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(640);
  const [hoverX, setHoverX] = useState<number | null>(null);
  const [showTable, setShowTable] = useState(false);

  useLayoutEffect(() => {
    const el = wrap.current;
    if (!el) return;
    const ro = new ResizeObserver(([e]) => setWidth(Math.max(320, e.contentRect.width)));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const series = useMemo(() => SERIES.map((s) => ({
    ...s,
    pts: points
      .filter((p) => p.table === s.table)
      .map((p) => ({t: Date.parse(p.committedAt), v: p.latencyMs, p}))
      .sort((a, b) => a.t - b.t),
  })), [points]);

  const all = series.flatMap((s) => s.pts);
  const innerW = width - M.left - M.right;
  const innerH = HEIGHT - M.top - M.bottom;
  const tMin = all.length ? Math.min(...all.map((p) => p.t)) : Date.now() - 600_000;
  const tMaxRaw = all.length ? Math.max(...all.map((p) => p.t)) : Date.now();
  const tMax = tMaxRaw === tMin ? tMin + 60_000 : tMaxRaw;
  const yTicks = niceTicks(Math.max(1000, ...all.map((p) => p.v)) * 1.1);
  const yMax = yTicks[yTicks.length - 1] || 1;
  const x = (t: number) => M.left + ((t - tMin) / (tMax - tMin)) * innerW;
  const y = (v: number) => M.top + innerH - (Math.max(0, v) / yMax) * innerH;
  const xTicks = Array.from({length: 5}, (_, i) => tMin + ((tMax - tMin) * i) / 4);

  // Crosshair: the nearest point per series to the pointer's time.
  const hoverT = hoverX === null ? null : tMin + ((hoverX - M.left) / innerW) * (tMax - tMin);
  const nearest = hoverT === null ? [] : series
    .filter((s) => s.pts.length)
    .map((s) => ({s, pt: s.pts.reduce((a, b) => (Math.abs(b.t - hoverT) < Math.abs(a.t - hoverT) ? b : a))}));
  const crossT = nearest.length ? nearest.reduce((a, b) => (Math.abs(b.pt.t - hoverT!) < Math.abs(a.pt.t - hoverT!) ? b : a)).pt.t : null;

  const latest = series.map((s) => ({...s, last: s.pts[s.pts.length - 1]}));

  return (
    <section className="card latency">
      <header className="card-head">
        <h2>End-to-end latency</h2>
        <button className="link" onClick={() => setShowTable(!showTable)}>{showTable ? 'Show chart' : 'Show table'}</button>
      </header>
      <p className="fine">
        Time from a row's MySQL <code>updated_at</code> to the commit of the Iceberg snapshot that published it.
        One point per Tableflow commit, at the longest wait among the inserts and updates it published.
      </p>

      <div className="tiles">
        {latest.map((s) => (
          <div key={s.table} className="tile">
            <span className="tile-label"><i className="swatch" style={{background: s.color}}/>{s.label}</span>
            <span className="tile-value">{s.last ? fmtSecs(s.last.v) : '—'}</span>
            <span className="tile-sub">{s.last ? `committed ${fmtTime(s.last.t)}` : (errors.find((e) => e.table === s.table)?.error ?? 'waiting for data')}</span>
          </div>
        ))}
      </div>

      <ul className="legend" aria-label="Legend">
        {SERIES.map((s) => <li key={s.table}><i className="swatch" style={{background: s.color}}/>{s.label}</li>)}
      </ul>

      {showTable ? (
        <div className="table-wrap">
          <table className="data">
            <thead><tr><th>Table</th><th>Rows</th><th>Oldest row updated</th><th>Snapshot committed</th><th>Latency (min–max)</th></tr></thead>
            <tbody>
              {[...points].reverse().slice(0, 200).map((p, i) => (
                <tr key={i}>
                  <td>{p.table}</td>
                  <td className="num">{p.records}</td>
                  <td>{new Date(p.recordAt).toLocaleTimeString()}</td>
                  <td>{new Date(p.committedAt).toLocaleTimeString()}</td>
                  <td className="num">{fmtSecs(p.minLatencyMs)}–{fmtSecs(p.latencyMs)}</td>
                </tr>
              ))}
              {points.length === 0 && <tr><td colSpan={5} className="empty">No measurements yet</td></tr>}
            </tbody>
          </table>
        </div>
      ) : (
        <div ref={wrap} className="chart-wrap">
          <svg
            width={width}
            height={HEIGHT}
            role="img"
            aria-label="Line chart of end-to-end replication latency in seconds per table over time"
            onPointerMove={(e) => {
              const r = (e.currentTarget as SVGSVGElement).getBoundingClientRect();
              const px = e.clientX - r.left;
              setHoverX(px >= M.left && px <= M.left + innerW ? px : null);
            }}
            onPointerLeave={() => setHoverX(null)}
          >
            {yTicks.map((v) => (
              <g key={v}>
                <line className="grid" x1={M.left} x2={M.left + innerW} y1={y(v)} y2={y(v)}/>
                <text className="axis-label" x={M.left - 8} y={y(v)} textAnchor="end" dominantBaseline="middle">{fmtSecs(v)}</text>
              </g>
            ))}
            {xTicks.map((t, i) => (
              <text key={i} className="axis-label" x={x(t)} y={HEIGHT - 10} textAnchor="middle">{fmtTime(t)}</text>
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
                {s.pts.map((p, i) => (
                  <circle key={i} cx={x(p.t)} cy={y(p.v)} r={4} fill={s.color} stroke="var(--surface-1)" strokeWidth={2}/>
                ))}
              </g>
            ))}

            {/* Direct labels at each series' latest point, nudged apart. */}
            {(() => {
              const labels = latest.filter((s) => s.last).map((s) => ({s, ly: y(s.last!.v)})).sort((a, b) => a.ly - b.ly);
              for (let i = 1; i < labels.length; i++) labels[i].ly = Math.max(labels[i].ly, labels[i - 1].ly + 14);
              return labels.map(({s, ly}) => (
                <text key={s.table} className="direct-label" x={M.left + innerW + 10} y={ly} dominantBaseline="middle">
                  {s.label} {fmtSecs(s.last!.v)}
                </text>
              ));
            })()}

            {crossT !== null && <line className="crosshair" x1={x(crossT)} x2={x(crossT)} y1={M.top} y2={M.top + innerH}/>}

            {all.length === 0 && (
              <text className="empty" x={M.left + innerW / 2} y={M.top + innerH / 2} textAnchor="middle">
                Waiting for Tableflow to commit the first snapshots…
              </text>
            )}
          </svg>

          {crossT !== null && nearest.length > 0 && (
            <div className="tooltip" style={{left: Math.min(x(crossT) + 12, width - 200), top: M.top}}>
              <div className="tooltip-head">{new Date(crossT).toLocaleTimeString()}</div>
              {nearest.map(({s, pt}) => (
                <div key={s.table} className="tooltip-row">
                  <i className="swatch" style={{background: s.color}}/>
                  <span>{s.label}</span>
                  <span className="num">{fmtSecs(pt.v)}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      )}

      {errors.some((e) => e.table === '*') && (
        <p className="error">{errors.find((e) => e.table === '*')!.error}</p>
      )}
    </section>
  );
}
