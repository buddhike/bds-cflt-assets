import {DemoStatus} from './api';

/** The states in which a runner is working, so the page asks for news often. */
export const BUSY: DemoStatus[] = ['launching', 'updating', 'tearing_down'];

export const LABEL: Record<DemoStatus, string> = {
  launching: 'Launching',
  running: 'Running',
  launch_failed: 'Launch failed',
  updating: 'Updating',
  update_failed: 'Update failed',
  tearing_down: 'Tearing down',
  teardown_failed: 'Tear down failed',
};

export type Tone = 'ok' | 'busy' | 'warn' | 'danger';

/** The pill tone of a state: green running, the accent while a runner works, red when it failed. */
export function tone(status: DemoStatus): Tone {
  if (BUSY.includes(status)) return 'busy';
  return status === 'running' ? 'ok' : 'danger';
}

/** The words on a state's pill. A state in progress ends in an ellipsis. */
export function statusText(status: DemoStatus): string {
  return LABEL[status] + (BUSY.includes(status) ? '…' : '');
}
