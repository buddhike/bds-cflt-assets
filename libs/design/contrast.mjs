// Checks the colour tokens of graphite.css against WCAG 2.2 AA, in both themes.
//
//   node libs/design/contrast.mjs
//
// Text needs 4.5:1. Borders of controls, the focus ring and chart series need 3:1.
// Pills and notices put a status colour on a tint of itself, so that is checked too.
import {readFileSync} from 'node:fs';

const css = readFileSync(new URL('./graphite.css', import.meta.url), 'utf8');

// Light tokens are the colours in the first :root block, dark ones those in :root[data-theme="dark"].
const block = (re) => {
  const m = css.match(re);
  if (!m) throw new Error(`graphite.css: no block matches ${re}`);
  return Object.fromEntries([...m[1].matchAll(/--([a-z0-9-]+):\s*(#[0-9a-fA-F]{6})\s*;/g)].map((t) => [t[1], t[2].toLowerCase()]));
};
const themes = {
  light: block(/:root\s*\{([\s\S]*?)\n\}/),
  dark: block(/:root\[data-theme="dark"\]\s*\{([\s\S]*?)\n\}/),
};

const rgb = (h) => [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16));
const lin = (c) => { c /= 255; return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4; };
const lum = (h) => { const [r, g, b] = rgb(h); return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b); };
const ratio = (a, b) => { const [x, y] = [lum(a), lum(b)]; return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05); };
const mix = (fg, bg, p) => '#' + rgb(fg).map((c, i) => Math.round(c * p + rgb(bg)[i] * (1 - p)).toString(16).padStart(2, '0')).join('');

let checks = 0;
const fails = [];
for (const [theme, t] of Object.entries(themes)) {
  const need = (what, fg, bg, min) => {
    for (const k of [fg, bg]) if (!/^#/.test(k) && !t[k]) throw new Error(`${theme}: token --${k} is missing`);
    const f = t[fg] ?? fg, b = t[bg] ?? bg;
    checks++;
    const r = ratio(f, b);
    if (r < min) fails.push(`${theme}: ${what}: ${f} on ${b} is ${r.toFixed(2)}:1, needs ${min}`);
  };
  for (const bg of ['bg', 'surface', 'surface-2']) for (const fg of ['text', 'text-2', 'text-3']) need(`--${fg} on --${bg}`, fg, bg, 4.5);
  need('--accent-text on --accent', 'accent-text', 'accent', 4.5);
  need('--accent-text on --accent-hover', 'accent-text', 'accent-hover', 4.5);
  need('--on-danger on --danger', 'on-danger', 'danger', 4.5);
  need('focus ring --accent on --surface', 'accent', 'surface', 3);
  need('focus ring --accent on --bg', 'accent', 'bg', 3);
  need('--control-border on --surface', 'control-border', 'surface', 3);
  need('--control-border on --bg', 'control-border', 'bg', 3);
  for (const fg of ['accent-ink', 'ok', 'warn', 'danger']) {
    for (const bg of ['bg', 'surface', 'surface-2']) need(`--${fg} on --${bg}`, fg, bg, 4.5);
    need(`--${fg} on a 12% tint of itself over --surface`, fg, mix(t[fg], t.surface, 0.12), 4.5);
    need(`--${fg} on a 12% tint of itself over --bg`, fg, mix(t[fg], t.bg, 0.12), 4.5);
  }
  for (let i = 1; i <= 5; i++) need(`--s${i} on --surface`, `s${i}`, 'surface', 3);
  for (const fg of ['code-text', 'code-muted', 'code-keyword', 'code-type', 'code-string', 'code-number', 'code-warn', 'code-error']) {
    need(`--${fg} on --code-bg`, fg, 'code-bg', 4.5);
  }
}

if (fails.length) {
  console.error(fails.join('\n'));
  console.error(`\n${fails.length} of ${checks} contrast checks fail`);
  process.exit(1);
}
console.log(`${checks} contrast checks pass`);
