# Visual design: Graphite

The visual design of Launchpad and of every demo it launches. Its purpose is one
user experience: a person who signs in to Launchpad, opens a demo, and goes back
should feel they never left the product. Same colours, type, spacing, status
language and controls, and the same top bar.

This is the contract for the front end of `demos/launchpad` and of every demo.
[`libs/design/graphite.css`](../../../../libs/design/graphite.css) implements it.
When this document and that file disagree about a value, the file wins, and this
document should be corrected.

## Decision

Graphite was chosen from three candidates that were compared on the same screens.

| | Chosen | Why |
|---|---|---|
| **Graphite** (IBM Plex, flat, bordered, dense) | yes | Fits the data-heavy demos best (tables, charts, SQL, logs). No shadows or gradients, so it is the cheapest to keep identical across React and Svelte. Closest to PIMS, the most complex front end. |
| Indigo (Inter, soft, rounded) | no | Closest to Launchpad as it was, but its shadows and gradients need restraint in dense screens. |
| Midnight (Geist, dark-first, cyan) | no | Most distinctive, but the most work, and light mode is its weaker theme. |

Consequence: Launchpad is the app that changes most. It was indigo, rounded and
shadowed, and becomes flat, square-cornered and denser.

## Principles

1. **One system, two densities.** Launchpad is comfortable. Demos are compact.
   Same tokens, same components.
2. **Colour carries one meaning each.** Green is running and healthy. The accent
   (blue) is interaction and work in progress. Amber is "this needs your
   attention". Red is failed. Nothing else uses those hues for decoration.
3. **Colour is never the only signal.** Every status has a word, and often a
   shape. A person who cannot tell green from red can still read the screen.
4. **Flat.** Hierarchy comes from a 1px border and a change of surface, not from
   shadows, gradients or glows.
5. **Tokens, not values.** An app never writes a colour, a font or a radius of
   its own. If a token is missing, add it here first.
6. **Accessible by construction.** WCAG 2.2 AA, checked by a script (see
   [Accessibility](#accessibility)).

## Using it

| | |
|---|---|
| Canonical file | `libs/design/graphite.css` |
| Each app's copy | `frontend/src/graphite.css`. A Docker build's context is the app's own directory, so an app cannot read `libs/`. |
| Edit | Change the canonical file, run `libs/design/sync.sh`, commit both. |
| Guard | CI runs `libs/design/sync.sh --check` and `node libs/design/contrast.mjs` (`.github/workflows/design-check.yml`). |
| Fonts | `npm i @fontsource-variable/ibm-plex-sans @fontsource/ibm-plex-mono`. The stylesheet's `@font-face` rules resolve them, so nothing loads from a CDN and a demo works offline. |
| Import | First thing in the app's entry file: `import './graphite.css'`. App styles come after and only add layout. |
| Density | Demos set `class="dense"` on `<body>`. Launchpad does not. |
| Theme | Follows the operating system. `data-theme="light"` or `"dark"` on `<html>` forces one, for screenshots and tests. There is no in-app toggle. |

The stylesheet styles bare elements (`button`, `input`, `table.data`, `h1` to
`h4`, `code`) with zero specificity, so a class in the app always wins and no
`!important` is needed.

## Colour

Hex values below are copies. Contrast figures are against `--surface` unless
stated.

### Surfaces and lines

| Token | Light | Dark | Use |
|---|---|---|---|
| `--bg` | `#f3f4f2` | `#121211` | The page |
| `--surface` | `#fcfcfb` | `#1a1a19` | Cards, panels, top bar, inputs |
| `--surface-2` | `#eceeea` | `#232321` | Inset areas, hover, table header, inline `code` |
| `--border` | `#dcdeda` | `#2e2e2b` | Dividers, card edges |
| `--border-strong` | `#c9ccc6` | `#403f3b` | Button edges, tag outlines, emphasised dividers |
| `--control-border` | `#7d8078` | `#76766f` | Input edges. At least 3:1, so a field is findable |
| `--grid` | `#e6e8e4` | `#262624` | Chart grid lines, meter tracks |

### Text

| Token | Light | Dark | Use |
|---|---|---|---|
| `--text` | `#11120f` | `#f0efec` | Primary |
| `--text-2` | `#4a4c46` | `#c3c2b7` | Secondary: descriptions, table headers |
| `--text-3` | `#5f625b` | `#96958c` | Muted: hints, timestamps, metadata. Still 4.5:1 |

### Accent

| Token | Light | Dark | Use |
|---|---|---|---|
| `--accent` | `#1b66c9` | `#5ca3f5` | Primary button fill, focus ring, active tab underline, chart series 1 |
| `--accent-hover` | `#1556ad` | `#7bb4f7` | Primary button hover |
| `--accent-text` | `#ffffff` | `#08111c` | Text and icons on `--accent` |
| `--accent-ink` | `#1556ad` | `#7bb4f7` | Accent used **as text**: links, "busy" status |

### Status

| Token | Light | Dark | Means |
|---|---|---|---|
| `--ok` | `#086a31` | `#3ecf72` | Running, healthy, done |
| `--warn` | `#8a5a00` | `#f2c46b` | Needs attention: update available, missing key, degraded |
| `--danger` | `#b3261f` | `#f08080` | Failed, unhealthy, destructive |
| `--on-danger` | `#ffffff` | `#1c0707` | Text on a `--danger` fill |

"In progress" is the accent (`.pill.busy`), not amber. Amber means a person has
something to do.

### Data series

`--s1` to `--s5`, in this order, for charts and any categorical colouring:

| | Light | Dark |
|---|---|---|
| `--s1` blue | `#1b66c9` | `#5ca3f5` |
| `--s2` orange | `#d4551f` | `#ee7a4a` |
| `--s3` green | `#14906a` | `#2fc795` |
| `--s4` purple | `#8250c8` | `#b48df0` |
| `--s5` gold | `#b88400` | `#e5b93a` |

Each is at least 3:1 on `--surface`. Use at most five series in one chart. Always
add a legend or a direct label: colour alone does not name a series.

### Code and logs

`--code-bg`, `--code-text`, `--code-muted`, `--code-keyword`, `--code-type`,
`--code-builtin`, `--code-ident`, `--code-string`, `--code-number`, `--code-warn`,
`--code-error`. They follow the
theme (a light panel in light mode, a dark one in dark mode), and every one is
4.5:1 on `--code-bg`. SQL editors, logs and any other code use only these.

## Typography

| | |
|---|---|
| Interface | **IBM Plex Sans** (variable, 100 to 700). Fallback: the system UI font |
| Code and live data | **IBM Plex Mono** (400, 500, 600) |
| Numbers that change | `font-variant-numeric: tabular-nums` (`.num`), so a figure does not jitter |

| Role | Token | Comfortable | Compact (`.dense`) | Weight | Use |
|---|---|---|---|---|---|
| Display | `--fs-display`, `.t-display` | 28px | 28px | 600 | Rare: empty states |
| h1 | `--fs-h1` | 22px | 22px | 600 | Page title, once per page |
| h2 | `--fs-h2` | 17px | 17px | 600 | Section heading |
| h3 | `--fs-h3` | 14px | 14px | 600 | Card and panel titles |
| Body | `--fs-body` | 14px | 13px | 400 | Default text, controls |
| Small | `--fs-small` | 12.5px | 12px | 400 | Metadata, hints, table cells |
| Label | `--fs-label`, `.label` | 11px | 11px | 600, uppercase, +0.08em | KPI labels, group headings |
| Mono | `--fs-mono`, `.mono` | 12.5px | 12.5px | 400 | Logs, SQL, ids, commits |

Headings use a tracking of -0.012em. Body text uses none. Do not set
`font-size` in `px` in app CSS: use a token or an `em` relative to one.

## Shape and density

| Token | Value | Use |
|---|---|---|
| `--r-lg` | 8px | Cards, panels, the gate |
| `--r-md` | 6px | Buttons, notices, KPI tiles, code and log boxes |
| `--r-sm` | 5px | Inputs, small tiles, step rows |
| `--r-pill` | 4px | Status pills and tags (squared on purpose) |

There are no shadows, gradients or translucent surfaces. Overlays (a menu, a
tooltip) use `--surface` with `--border-strong` and nothing else.

| Density | `--ctl-h` | `--pad-card` | `--fs-body` | Used by |
|---|---|---|---|---|
| Comfortable (default) | 34px | 16px | 14px | Launchpad |
| Compact (`.dense`) | 28px | 12px | 13px | Demos |

Spacing is in multiples of 4px. Use `rem` or `em` for gaps and padding.

## Status language

One vocabulary for every screen. A status is a `.pill` with a tone class and a
word. Launchpad's lifecycle states map as follows, and demos use the same tones
for the same meanings.

| Meaning | Pill | Example words |
|---|---|---|
| Nothing yet | `.pill` (neutral) | Not deployed |
| In progress | `.pill.busy` (pulses) | Launching…, Updating…, Tearing down…, Setting up |
| Good | `.pill.ok` | Running, Live, Healthy, Done |
| Needs attention | `.pill.warn` | Update available, Degraded, Needs your key |
| Failed | `.pill.danger` | Launch failed, Update failed, Tear down failed, Unhealthy |

An in-progress word ends in an ellipsis. A failed word says what failed.

## Components

Classes are defined in `graphite.css`. Use them as they are; do not restyle them
per app.

| Component | Class or element | Rules |
|---|---|---|
| Button | `button`, `a.button` | One `.primary` per card or toolbar. `.danger` only inside a confirmation. `.quiet` for tertiary actions, `.link` for an inline action, `.sm` for dense toolbars. A toggle sets `aria-pressed`, which the stylesheet draws |
| Stack | `.stack` | Vertical rhythm of 1rem between children: forms, gates, page sections. Prefer it to margins on the children |
| Field | `.field` with a `label`, an `input` and `.hint` | Label above the control. `.invalid` turns the hint into the error message |
| Status | `.pill` + `.ok` `.busy` `.warn` `.danger` | See [Status language](#status-language) |
| Tag | `ul.tags > li.tag` | The Confluent Cloud products a demo shows, in the order of `demo.json` |
| Notice | `.notice` + `.ok` `.warn` `.danger`, an icon, a bold lead-in | For anything a person must read. A left bar, never a full tint |
| Card | `.card` in `.cards` | `.card-head` has a `.tile`, an `h3` and a `.pill`. Buttons sit in `.actions` at the bottom |
| Panel | `.panel` with `.panel-head` | A titled region of a workbench |
| KPI | `.kpis > .kpi` | `.label`, `.kpi-value`, `.kpi-sub` (+ `.ok` `.warn` `.danger`) |
| Health tile | `.health` + `.warn` `.danger` | A named thing and two numbers, with a left bar in the status tone |
| Steps | `ol.steps > li.step` + `.done` `.run` `.fail` | A setup or progress list |
| Table | `table.data` in `.table-box` | Numbers right-aligned with `.r` |
| Chart | `.chart` around an SVG, `.legend`, `.swatch` | See [Data visualisation](#data-visualisation) |
| Code | `.code`, `.log` | Mono, from the code tokens |
| Status bar | `.statusbar` with `.dot`, `.meter` | The bottom edge of a workbench |
| Rail | `.rail` of `button`s, `aria-current="true"` on the open one | A vertical switcher for a workbench's panels. `.badge-count` (+ `.warn`) counts what needs attention |
| Spacer | `.spacer` | Pushes what follows it to the far end of a flex row |

### Inline confirmation

A destructive action is confirmed in place, not in a dialog: the button is replaced
by the question, a `.danger` button and a Cancel.

```
Delete everything this demo created?  [Tear down]  [Cancel]
```

### Icons

Inline SVG, 16px, `class="icon"` (1.5px stroke, round caps, `currentColor`). A
button with an icon and a word puts the icon first, except for "opens in a new
tab", which puts it last.

## Layout

### The shell

Every screen has the same top bar (`.topbar`), at least 3rem tall, on `--surface`
with a bottom border, in this order:

1. `.brand`: the Launchpad mark (`.logo`), then the name. In a demo the name is
   `Launchpad / <demo name>`, with `/` as `.sep`. A demo that can also run outside
   Launchpad (PIMS on the desktop) shows the prefix only when Launchpad launched it.
2. `.tabs` (Launchpad's sections, or a demo's views). The current one has
   `aria-current` and a 2px accent underline.
3. A status (`.pill`) when the demo has one.
4. `.who` at the far right: the signed-in person, or the demo's main actions.

The Launchpad mark is shared. A demo does not draw its own logo in the interface.
Its colours come from `.logo` in the stylesheet, so it follows the theme:

```html
<svg class="logo" viewBox="0 0 32 32" aria-hidden="true">
  <rect width="32" height="32" rx="9"/>
  <path d="M10 19.5 16 12l6 7.5"/>
  <path d="M12.5 24h7" opacity=".65"/>
</svg>
```

### Launchpad

Comfortable density. `.page` is 64rem wide, centred. A page begins with
`.page-head` (title, one sentence, the page's one action). Demos are `.cards`.
Sign in and "credentials needed" use `.gate`. A demo's log opens on its own
`.page.wide` and uses `.log`.

### A demo

Compact density. Fill the viewport and scroll inside panels, not the page: top
bar, then the workbench, then `.statusbar`. A workbench is a `.rail` and a main
area of `.kpis`, `.panel`s and tables. A monitor-style demo (API diagnosis) can
use a single column of `.health` tiles under the top bar.

### Narrow screens

Launchpad works at 400px: the gate becomes full height, cards stack, the top bar
wraps. Demos are desktop first, but nothing may force a horizontal page scroll.
Only tables, code and charts scroll sideways, inside their own box.

## Data visualisation

* Series colours are `--s1` to `--s5`, in order, and never the status colours for
  a series that is not a status.
* Grid is `--grid`, axis text is `--text-3` at 11px with tabular figures, a target
  or threshold is a dashed `--text-3` line labelled at its end.
* Lines are 2px with round joins. Give every chart a title, a legend and an
  `aria-label` that says what it shows.
* Colour a number by status only with `.kpi-sub.ok|warn|danger`, and put words
  beside it.

## Accessibility

* Contrast: text 4.5:1, input borders, focus ring and series 3:1, code colours
  4.5:1, a status colour on a tint of itself 4.5:1. `node libs/design/contrast.mjs`
  checks all of it against the real stylesheet, in both themes, and CI runs it. A
  token change that breaks a pair fails the build.
* Focus: every interactive element shows a 2px accent outline. Never remove it.
* Keyboard: everything a mouse can do, a keyboard can. Tabs and the rail are
  buttons or links, never `div`s.
* Motion: only `.pill.busy` and `.dot.busy` pulse, and `prefers-reduced-motion`
  turns every animation and transition off.
* Meaning: a status always has a word. An icon-only button has an `aria-label`.

## What an app may add

An app's own CSS adds layout (grids, the shape of its workbench) and the few
components only that app has (PIMS's data-flow diagram, a CodeMirror theme). It:

* uses only `var(--…)` for colour, font and radius, including inside SVG and any
  JavaScript that sets a colour (read the variable, do not copy the hex);
* does not redefine a shared class (`.card`, `.pill`, `.tab`…). If a shared
  component is wrong, fix it here;
* does not scope colours inside a component (a Svelte `<style>` block with a hex
  or `rgba()` is a defect).

## Review checklist for a demo

1. `graphite.css` is the canonical copy (`libs/design/sync.sh --check`).
2. `<body class="dense">`, and the page follows the OS theme in both.
3. The top bar has the shared mark, `Launchpad / <name>`, and its status.
4. No hex, `rgb()`, `rgba()` or `hsl()` in app CSS or components, except in the
   shared mark.
5. Every status uses a `.pill` with the right tone and a word.
6. Charts use `--s1`…, with a legend and an `aria-label`.
7. Looks right in light and dark, with the keyboard, and at 1280px wide.

## Not decided

* **Brand.** The palette is not taken from a Confluent brand guideline. If the
  system should match it, change the accent and series tokens and run the contrast
  check.
* **A way back.** `Launchpad` in a demo's top bar is not a link, because a demo
  does not know Launchpad's address. Launchpad could pass it as a variable.
* **An in-app theme switch.** There is none; the system follows the OS.

## Rollout

All three front ends are on Graphite.

| App | What changed |
|---|---|
| Launchpad | New stylesheet and IBM Plex in place of Inter. Underlined tabs, outlined status pills, flat cards. In-progress states use the accent instead of amber, and a card shows "Update available" as a pill |
| PIMS | Its tokens were renamed to these. It uses the shared top bar, steps, KPI tiles, status bar and notices, and its SQL editor draws on the code tokens |
| Automated API diagnosis | Had no design system (forced dark navy, Nunito, hard-coded rgba). Rebuilt from the shared cards, health tiles, pills and notices, with a light theme |

The React apps carry copies of `Logo`, `Icon` and `Notice`; the Svelte app has
`Logo.svelte` and `Notice.svelte`. They are small and carry no colour of their own,
so a copy cannot drift in look. If a fourth app is added, copy the nearest set.

`.claude/skills/demo-builder/SKILL.md` tells whoever builds a demo to follow this
document, and `.github/workflows/design-check.yml` keeps the copies and the
contrast honest.
