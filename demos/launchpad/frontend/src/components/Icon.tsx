const PATHS = {
  info: <><circle cx="8" cy="8" r="6.5" /><path d="M8 7.2v4M8 4.9v.1" /></>,
  warn: <><path d="M8 2.2 14.6 13.6H1.4z" /><path d="M8 6.6v3.2M8 11.7v.1" /></>,
  error: <><circle cx="8" cy="8" r="6.5" /><path d="M5.8 5.8l4.4 4.4M10.2 5.8l-4.4 4.4" /></>,
  check: <><circle cx="8" cy="8" r="6.5" /><path d="M5.2 8.2l2 2 3.6-4" /></>,
  external: <path d="M9 3h4v4M13 3 7.5 8.5M6.5 4H3.5v8.5H12V9.5" />,
};

export type IconName = keyof typeof PATHS;

/** A 16px icon from the design system: 1.5px stroke, in the colour of the text around it. */
export function Icon({name}: {name: IconName}) {
  return <svg className="icon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" focusable="false">{PATHS[name]}</svg>;
}
