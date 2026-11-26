/** The Launchpad mark, shared by Launchpad and every demo. Its colours come from `.logo` in graphite.css. */
export function Logo({size = 26}: {size?: number}) {
  return (
    <svg className="logo" width={size} height={size} viewBox="0 0 32 32" aria-hidden="true" focusable="false">
      <rect width="32" height="32" rx="9" />
      <path d="M10 19.5 16 12l6 7.5" />
      <path d="M12.5 24h7" opacity=".65" />
    </svg>
  );
}
