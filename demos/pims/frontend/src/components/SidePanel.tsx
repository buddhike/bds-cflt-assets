import {ReactNode} from 'react';
import {LatencyResult, LogEntry} from '../api';
import {useResize} from '../useResize';
import {ActivityLog} from './ActivityLog';
import {DataFlow} from './DataFlow';
import {LatencyChart} from './LatencyChart';

export type SideSection = 'metrics' | 'logs' | 'flow';

const icon = (paths: ReactNode) => (
  <svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    {paths}
  </svg>
);

const SECTIONS: {id: SideSection; label: string; icon: ReactNode; width: number}[] = [
  {id: 'metrics', label: 'Metrics', width: 500, icon: icon(<><path d="M4 20V4"/><path d="M4 20h16"/><path d="M8 15l4-5 3 3 5-7"/></>)},
  {id: 'logs', label: 'Logs', width: 560, icon: icon(<><path d="M8 6h12M8 12h12M8 18h12"/><path d="M4 6h.01M4 12h.01M4 18h.01"/></>)},
  // The diagram is drawn for about 760px; narrower and it scrolls sideways.
  {id: 'flow', label: 'Data flow', width: 800, icon: icon(<><rect x="3" y="4" width="6" height="6" rx="1.5"/><rect x="15" y="14" width="6" height="6" rx="1.5"/><path d="M9 7h3a3 3 0 0 1 3 3v4"/></>)},
];

const DRAWER_ID = 'side-drawer';

interface Props {
  /** The open section, or null when the drawer is drawn in. */
  section: SideSection | null;
  onSelect: (section: SideSection | null) => void;
  logs: LogEntry[];
  latency: LatencyResult;
  simRunning: boolean;
  errorCount: number;
  warnCount: number;
}

/** SidePanel is a rail of section buttons and the drawer they draw out from the left edge. */
export function SidePanel({section, onSelect, logs, latency, simRunning, errorCount, warnCount}: Props) {
  return (
    <>
      <nav className="rail" aria-label="Panels">
        {SECTIONS.map((s) => {
          const active = s.id === section;
          return (
            <button
              key={s.id}
              aria-current={active}
              aria-expanded={active}
              aria-controls={DRAWER_ID}
              onClick={() => onSelect(active ? null : s.id)}
            >
              {s.icon}
              <span>{s.label}</span>
              {s.id === 'logs' && errorCount > 0
                ? <span className="badge-count" aria-label={`${errorCount} errors`}>{errorCount}</span>
                : s.id === 'logs' && warnCount > 0 && <span className="badge-count warn" aria-label={`${warnCount} warnings`}>{warnCount}</span>}
            </button>
          );
        })}
      </nav>

      {/* Keyed by section so each one is drawn out to the width the operator last gave it. */}
      {section && (
        <Drawer key={section} section={section}>
          {section === 'metrics' && <LatencyChart latency={latency}/>}
          {section === 'logs' && <ActivityLog logs={logs}/>}
          {section === 'flow' && <DataFlow active={simRunning}/>}
        </Drawer>
      )}
    </>
  );
}

function Drawer({section, children}: {section: SideSection; children: ReactNode}) {
  const spec = SECTIONS.find((s) => s.id === section)!;
  const {size, dragging, handleProps} = useResize({
    axis: 'x',
    initial: Math.min(spec.width, Math.round(window.innerWidth * 0.6)),
    min: 320,
    max: () => Math.round(window.innerWidth * 0.75),
    storageKey: `pims.drawer.${section}`,
  });
  return (
    <aside id={DRAWER_ID} className="drawer" style={{width: size}} aria-label={spec.label}>
      <div className="drawer-body">{children}</div>
      <div
        className={dragging ? 'resizer resizer-v is-dragging' : 'resizer resizer-v'}
        aria-label={`Resize the ${spec.label} panel`}
        {...handleProps}
      />
    </aside>
  );
}
