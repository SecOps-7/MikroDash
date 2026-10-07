---
version: alpha
name: MikroDash
description: A dark-first, dense operations console for MikroTik RouterOS fleets. Live numbers in monospace on translucent cards over a near-black field.
colors:
  primary: "#38bdf8"
  secondary: "#34d399"
  tertiary: "#a78bfa"
  neutral: "#07090f"
  surface: "rgba(13,18,30,.85)"
  on-surface: "rgba(200,215,240,.9)"
  on-surface-muted: "rgba(148,163,190,.55)"
  border: "rgba(99,130,190,.13)"
  nav: "rgba(5,8,16,.92)"
  rx: "#38bdf8"
  tx: "#34d399"
  ok: "#4ade80"
  warn: "#f59f00"
  error: "#f87171"
  alt: "#a78bfa"
  pink: "#f472b6"
  light-neutral: "#e8eaee"
  light-surface: "rgba(255,255,255,.92)"
  light-on-surface: "#1a2030"
  light-on-surface-muted: "#5f7196"
  light-border: "#b1bbcf"
  light-rx: "#247ba1"
  light-tx: "#20835f"
typography:
  wordmark:
    fontFamily: Orbitron
    fontSize: 1.2rem
    fontWeight: 700
    letterSpacing: -0.02em
  stat-lg:
    fontFamily: JetBrains Mono
    fontSize: 1.9rem
    fontWeight: 700
    lineHeight: 1
  stat-md:
    fontFamily: JetBrains Mono
    fontSize: 1.3rem
    fontWeight: 600
  page-title:
    fontFamily: Oxanium
    fontSize: 0.85rem
    fontWeight: 600
    letterSpacing: 0.01em
  card-title:
    fontFamily: Oxanium
    fontSize: 0.82rem
    fontWeight: 600
    letterSpacing: 0.04em
  body-md:
    fontFamily: Oxanium
    fontSize: 0.8rem
    fontWeight: 400
    lineHeight: 1.5
  label-caps:
    fontFamily: Oxanium
    fontSize: 0.72rem
    fontWeight: 600
    letterSpacing: 0.06em
  table-head:
    fontFamily: Oxanium
    fontSize: 0.66rem
    fontWeight: 400
    letterSpacing: 0.06em
  data-md:
    fontFamily: JetBrains Mono
    fontSize: 0.72rem
    fontWeight: 400
  data-sm:
    fontFamily: JetBrains Mono
    fontSize: 0.68rem
    fontWeight: 600
  pill:
    fontFamily: JetBrains Mono
    fontSize: 0.62rem
    fontWeight: 400
rounded:
  xs: 3px
  sm: 4px
  badge: 5px
  md: 6px
  lg: 10px
  xl: 12px
  full: 999px
spacing:
  xs: 0.25rem
  sm: 0.5rem
  md: 0.75rem
  lg: 1rem
  xl: 1.5rem
  card-header-y: 0.65rem
  card-header-x: 1rem
  modal-x: 1.1rem
  nav-collapsed: 52px
  nav-open: 190px
components:
  card:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.on-surface}"
    rounded: "{rounded.lg}"
  card-title:
    textColor: "{colors.on-surface-muted}"
    typography: "{typography.card-title}"
  count-badge:
    backgroundColor: "rgba(99,130,190,.12)"
    textColor: "rgba(148,163,190,.7)"
    typography: "{typography.data-sm}"
    rounded: "{rounded.badge}"
  count-badge-active-blue:
    backgroundColor: "rgba(56,189,248,.15)"
    textColor: "rgba(56,189,248,.9)"
  tab:
    textColor: "{colors.on-surface-muted}"
    padding: 0.5rem
  tab-active:
    textColor: "{colors.rx}"
  button-primary:
    backgroundColor: "rgba(56,189,248,.15)"
    textColor: "{colors.rx}"
    rounded: "{rounded.md}"
    padding: 0.5rem
  button-primary-hover:
    backgroundColor: "rgba(56,189,248,.25)"
  button-danger:
    backgroundColor: "rgba(248,113,113,.12)"
    textColor: "rgba(248,113,113,.9)"
    rounded: "{rounded.md}"
  input:
    backgroundColor: "{colors.neutral}"
    textColor: "{colors.on-surface}"
    rounded: "{rounded.md}"
    padding: 0.45rem
  pill-ok:
    backgroundColor: "rgba(74,222,128,.1)"
    textColor: "{colors.ok}"
    typography: "{typography.pill}"
    rounded: "{rounded.sm}"
  pill-warn:
    backgroundColor: "rgba(245,159,0,.1)"
    textColor: "{colors.warn}"
    rounded: "{rounded.sm}"
  pill-bad:
    backgroundColor: "rgba(248,113,113,.1)"
    textColor: "{colors.error}"
    rounded: "{rounded.sm}"
  pill-info:
    backgroundColor: "rgba(56,189,248,.1)"
    textColor: "{colors.rx}"
    rounded: "{rounded.sm}"
  pill-neutral:
    backgroundColor: "rgba(99,130,190,.07)"
    textColor: "{colors.on-surface-muted}"
    rounded: "{rounded.sm}"
  modal:
    backgroundColor: "{colors.surface}"
    rounded: "{rounded.xl}"
    width: 960px
---

# DESIGN.md

How MikroDash looks, and the rules a new screen follows so it looks like the rest of the app. Read
this before any UI work. `CLAUDE.md` ("A new page's furniture") holds the page-layout rules that tests
enforce; this file covers the visual language around them.

**The stylesheet is the source of truth.** The tokens above are the default dark palette, copied
from `:root` in `web/public/app.css`. Components never use them as literals: they use the CSS custom
properties (`var(--accent-rx)` and so on) so that every palette, the light theme and the Appearance
settings can restyle them.

## Overview

MikroDash is a network operations console for people who run MikroTik routers. It is used for long
stretches, often on a second screen, so it is **dark first, dense and calm**:

- **Data first.** Most of the screen is numbers, names and states from routers. Decoration only
  helps you read those faster.
- **Live.** Values update in place over a WebSocket. Movement means the network changed, never
  that the UI wants attention.
- **Technical, not playful.** The square, slightly futuristic Oxanium for the interface, JetBrains
  Mono for every value a router reports, and the Orbitron wordmark.
- **Glass on night.** Translucent cards over a near-black background with two faint glows (blue at
  the top left, green at the bottom right). Thin blue-grey borders separate things; heavy shadows
  are not used.

## Colors

All colours are CSS custom properties on `:root`, and the palette layer swaps them:

- **17 palettes**: the default plus Nord, Catppuccin, Dracula, Tokyo, Gruvbox, Rosé Pine (and Moon),
  One Dark, Solarized, Everforest, Kanagawa, Monokai (and Pro), Material, Palenight and GitHub.
- **A light theme** for the default and for eight of the named palettes
  (`html[data-theme="light"]`).
- **Per-browser Appearance settings** (`web/src/appearance.ts`) for contrast, text and background
  brightness, font and font size.

A hard-coded colour drops out of all of that, so components never use one.

### Surfaces and text

| Role | Property | Default (dark) |
|---|---|---|
| Page background | `--bg-deep` | Night `#07090f` |
| Card, modal | `--bg-card` | Translucent ink `rgba(13,18,30,.85)` |
| Divider, outline | `--border` | Faint steel `rgba(99,130,190,.13)` |
| Primary text | `--text-main` | Pale steel `rgba(200,215,240,.9)` |
| Labels, metadata | `--text-muted` | Dim steel `rgba(148,163,190,.55)` |
| Side nav, top bar | `--nav-bg`, `--topbar-bg` | Near-black glass |

### Meaning

Every accent has a job, and the jobs do not move:

- **`--accent-rx`, sky blue: received traffic, and the interactive accent.** It colours Rx values,
  the active tab, focused inputs, primary buttons, links and info pills.
- **`--accent-tx`, emerald: transmitted traffic.** It colours Tx values and UDP.
- **Rx and Tx are fixed everywhere:** a received value is always `--accent-rx` and a transmitted one
  is always `--accent-tx`, on every page, chart and table.
- **`--accent-ok`, green:** up, running, bound, healthy.
- **`--accent-warn`, amber:** degraded, waiting, pending, and ICMP.
- **`--accent-err`, soft red:** down, failed, expired, and destructive actions.
- **`--accent-alt`, violet:** a secondary category, for example a service name.
- **`--accent-pink`:** a last series colour for charts that need one.

### How colour is applied

A state is a **tinted pill**, not a block of solid colour:

- the text in the accent colour;
- the background at about 10-15% of it;
- a 1px border at about 25-30% of it.

New code builds these with `color-mix(in srgb, var(--accent-ok) 13%, transparent)` so the tint
follows the palette. Older rules spell the default palette's `rgba()` out literally; don't copy
them.

## Typography

- **`--font-ui` (Oxanium)** for interface text: titles, labels, buttons, tabs, prose.
- **`--font-mono` (JetBrains Mono)** for anything a router reports: addresses, MACs, rates, counters,
  versions, interface names, timestamps, count pills and state pills.
- **Orbitron** for the wordmark only. Branding can swap it for another of the shipped fonts.
- **Always use the properties, never a family name.** The Appearance tab lets each user choose the UI
  font, from system fonts to about thirty bundled ones (`web/public/css/app-fonts.css`), and
  `var(--font-ui)` is how a component follows that choice.

The scale is small, because the app is dense:

| Use | Style |
|---|---|
| Hero numbers (Bandwidth, Connections) | mono, 1.3-1.9rem, 600-700 |
| Card titles | UI, .82rem, 600, UPPERCASE, .04em tracking, muted |
| Form labels, stat labels | UI, .65-.72rem, 600-700, UPPERCASE, .06-.08em tracking, muted |
| Table headers | UI, .66rem, UPPERCASE, .06em tracking, muted, with a sort caret |
| Table cells, values | mono, .68-.76rem |
| Pills | mono, .62-.68rem |

**Weights are 400, 600 and 700.** Uppercase with letter spacing marks a label; values are never
uppercased.

## Layout

The shell is the same on every page:

- **A left side nav**: 52px wide, opening to 190px on hover. Under 768px it becomes a drawer behind a
  burger button.
- **A top bar**: the wordmark, the page title and the router picker.
- **The page**: a stack or grid of cards.

Spacing is in rem, mostly from this ladder: .25, .4, .5, .65, .75, 1 and 1.5rem.

- Card header: .65rem by 1rem.
- Modal body: 1rem by 1.1rem.
- Gaps between pills and icons: .3-.4rem.

**Every page has the same furniture.** CLAUDE.md is the rule; tests enforce it:

- the title at the far left, with a blue count pill beside it;
- its tabs to the right of the title;
- actions alone in the right-hand `.hdr-actions` corner;
- sortable headers;
- its own nav icon;
- key columns as coloured pills.

Generated pages get all of this from `cmd/areagen` and `web/src/pages/area.ts`. A hand-built page
copies the same markup.

**Responsive:**

- Grids step down at roughly 1300, 1100, 980, 900, 760 and 620px, and end in one column.
- Toolbars and headers wrap rather than scroll.
- A wide table scrolls inside its card, never the page.
- Check every new screen at phone width and at a wide desktop.

## Elevation & Depth

Depth comes from **translucency and borders**, not from stacked shadows. There are three levels:

1. **The page**: `--bg-deep` with the two radial glows.
2. **Cards**: `--bg-card`, a 1px `--border` and one soft shadow, `0 4px 24px rgba(0,0,0,.25)`.
3. **Overlays**:
   - Modals sit on a `rgba(0,0,0,.55)` backdrop blurred 4px, with `0 16px 40px rgba(0,0,0,.5)`.
   - Banners and the mobile nav blur what is behind them.

**Inside a card, use borders and tint for structure, not a nested card.** A section inside a card
uses a border, a faint fill (`rgba(99,130,190,.04)`) or a tab bar.

**Card state is drawn on the card itself:**

- A device card has a 3px status stripe on its left edge.
- A card whose data has gone stale dims under its `.stale-overlay`. That overlay is always in the
  DOM; it is shown by opacity.

## Shapes

Corners are gently rounded, and the radius grows with the size of the element:

| Element | Radius |
|---|---|
| Keyboard keys, protocol tags | 3px |
| State pills, chips | 4px |
| Count pills, small chips | 5px |
| Buttons, inputs, selects, tab tops | 6px |
| Cards, the stale overlay | 10px |
| Modals | 12px |
| Status dots | 50% |
| Rare fully rounded tags | 999px |

**Icons are inline SVG line icons:**

- a 24px viewBox;
- a 2px stroke in `currentColor`, round caps and joins, no fill;
- drawn at 13-17px.

Each nav entry has its own icon that suits the page. **Don't use emoji as icons.** The one exception
is country flags.

## Components

Before writing new styles, reuse the existing class. The class names say where each one started, not
where it is limited to.

- **Card**: `.card` > `.card-header` (with `.card-title`) and `.card-body`.
- **Count pill**: `.card-badge`, plus `active-blue` or `active-green` when it is counting something
  live.
- **Tabs**: `.stab-bar` / `.stab`.
  - The active tab gets a 2px `--accent-rx` underline and blue text.
  - Page tabs use `rttab-bar` for the compact size.
- **Buttons**: `.sbtn` plus a variant.
  - `sbtn-primary` is the blue tint; the variants are `-outline`, `-danger`, `-warn` and `-purple`.
  - Buttons are tinted, not solid.
  - Disabled is .4-.45 opacity with a `not-allowed` cursor.
- **Forms**: `.sform-label` (uppercase, muted) over `.sform-input`.
  - The input is `--bg-deep` with a 1px border that turns `--accent-rx` on focus.
  - Toggles are `.stoggle`.
- **Modal**: `.rtr-modal-bg` > `.rtr-modal` with `-hdr`, `-body` and `-footer`.
  - The footer's actions are right-aligned.
  - Wide modals split the body into two columns from 900px.
  - The `pmodal` variant is a single 640px column for short forms.
- **State pills**: `.vpn-hs-badge` with
  - `hs-ok` (green),
  - `hs-warn` (amber),
  - `hs-stale` (red),
  - `hs-info` (blue),
  - `hs-never` (neutral).

  Generated pages pick these by **kind** (`state`, `action`, `good`, `warn`, `bad`, `info`), never by
  colour.
- **Protocols**: `protoPill()` in `web/src/dom.ts`.
  - TCP is blue, UDP is green, ICMP is amber and anything else is grey.
  - Firewall actions use `actionBadge`.
- **Tables**: use `renderSortHeader` / `sortRows`.
  - An **ordered** table (firewall chains, routing rules, IPsec policies, queues) keeps the router's
    order and its move arrows, and does not sort.
- **Live graphs**: a new graph uses the Dashboard traffic graph's mechanics:
  - it scrolls on server time;
  - its y-axis eases;
  - it stops animating while its page is hidden.
- **Empty and unknown**: a value the router did not send is a `-` in muted text. A `title` tooltip
  says why when the reason is useful, as the Public IP row does.

## Do's and Don'ts

- **Do** use `var(--…)` for every colour and font. **Don't** write a hex or rgba literal for a new
  component, and don't add a colour to the palette to make something stand out.
- **Do** keep Rx blue and Tx green, everywhere.
- **Do** show a state as a tinted pill using its meaning colour. **Don't** use solid colour blocks or
  gradients on components.
- **Do** put router data in the mono font, and interface text in the UI font.
- **Do** check a new screen:
  - in the default palette;
  - in the light theme;
  - in one other palette;
  - at phone width.
- **Do** respect `prefers-reduced-motion`, and pause animations on pages that are not shown.
- **Do** reuse a component before writing a new one. If two pages need the same thing, they share the
  class.
- **Don't** use em dashes anywhere in the UI or the docs; use a hyphen.
- **Don't** add emoji icons, a new font, or a shadow to make something "pop".
- **Don't** give a page tabs, pills or a count pill it has no rows for. The Terminal and the Tools
  pages are the recorded exceptions in CLAUDE.md.
