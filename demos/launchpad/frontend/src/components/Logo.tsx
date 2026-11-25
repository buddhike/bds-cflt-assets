/** The Launchpad mark: a rising chevron on a gradient tile. Decorative, so hidden from screen readers. */
export function Logo({size = 28}: {size?: number}) {
  return (
    <svg className="logo" width={size} height={size} viewBox="0 0 32 32" aria-hidden="true" focusable="false">
      <defs>
        <linearGradient id="lp-logo" x1="4" y1="2" x2="28" y2="30" gradientUnits="userSpaceOnUse">
          <stop stopColor="var(--accent-2)" />
          <stop offset="1" stopColor="var(--accent)" />
        </linearGradient>
      </defs>
      <rect width="32" height="32" rx="9" fill="url(#lp-logo)" />
      <path d="M10 19.5 16 12l6 7.5" fill="none" stroke="#fff" strokeWidth="2.6" strokeLinecap="round" strokeLinejoin="round" />
      <path d="M12.5 24h7" fill="none" stroke="#fff" strokeOpacity=".65" strokeWidth="2.6" strokeLinecap="round" />
    </svg>
  );
}
