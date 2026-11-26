import {ReactNode} from 'react';
import {Icon, IconName} from './Icon';

const ICON: Record<string, IconName> = {info: 'info', ok: 'check', warn: 'warn', danger: 'error'};

interface Props {
  tone?: 'info' | 'ok' | 'warn' | 'danger';
  /** A bold lead-in, before the text. */
  title?: string;
  /** Errors are announced as soon as they appear. */
  alert?: boolean;
  children: ReactNode;
}

/** Anything a person must read: a left bar in the tone's colour, an icon and a bold lead-in. */
export function Notice({tone = 'info', title, alert, children}: Props) {
  return (
    <div className={`notice${tone === 'info' ? '' : ' ' + tone}`} role={alert ? 'alert' : undefined}>
      <Icon name={ICON[tone]} />
      <div>{title && <b>{title} </b>}{children}</div>
    </div>
  );
}
