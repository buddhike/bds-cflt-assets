// The app icon (build/appicon.svg), cropped to its tile so it sits flush in the UI.
export function Logo({className}: {className?: string}) {
  return (
    <svg className={className} viewBox="100 100 824 824" aria-hidden="true">
      <defs>
        <linearGradient id="logo-bg" x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="#2b7fe0"/>
          <stop offset="1" stopColor="#0b2a4f"/>
        </linearGradient>
        <linearGradient id="logo-pulse" x1="0" y1="0" x2="1" y2="0">
          <stop offset="0" stopColor="#5eead4"/>
          <stop offset="1" stopColor="#34d399"/>
        </linearGradient>
      </defs>
      <rect x="100" y="100" width="824" height="824" rx="185" fill="url(#logo-bg)"/>
      <rect x="417" y="257" width="190" height="510" rx="44" fill="#fff"/>
      <rect x="257" y="417" width="510" height="190" rx="44" fill="#fff"/>
      <polyline points={PULSE} fill="none" stroke="#0d3563" strokeWidth="76" strokeLinecap="round" strokeLinejoin="round"/>
      <polyline points={PULSE} fill="none" stroke="url(#logo-pulse)" strokeWidth="40" strokeLinecap="round" strokeLinejoin="round"/>
    </svg>
  );
}

const PULSE = '170,512 372,512 424,424 494,648 560,336 618,560 656,512 854,512';
