#!/usr/bin/env node
'use strict';
/**
 * CSS CLASSES THIS PORT TOGGLES THAT NOTHING RESPONDS TO.
 *
 * ── TWO DEFECTS IN TWO DAYS, BOTH THIS SHAPE ────────────────────────────────
 *
 * `main.ts` toggled `body.nav-open`. The stylesheet — the LIVE app's, extracted
 * verbatim — has six rules on `#sidenav.mobile-open` and zero on `nav-open`. So
 * the burger did nothing, the sidenav never opened, and on a narrow screen a
 * user could not navigate at all. Nothing failed: the class was spelled
 * consistently in the TypeScript, the element existed, the listener fired.
 *
 * The stylesheet is not this port's to extend. A class it invents is a hook into
 * nothing, and the failure is always silent — the DOM changes and the page does
 * not.
 *
 * ── WHAT COUNTS AS "RESPONDED TO" ───────────────────────────────────────────
 *
 * Styled by `web/public/app.css`, OR read back by this port's own code
 * (`classList.contains`, `querySelector('.x')`, `matches`), OR present in the
 * extracted markup — a class the live app already puts on an element is one the
 * live stylesheet knows, even if it reaches it through a selector this scan
 * cannot parse.
 *
 * That last case is why this is a LEDGER and not a hard rule: the check is a
 * string search over CSS, and a class reached through `[class*=…]` or built in a
 * selector would be missed. It errs toward silence, so a hit is worth reading.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/class-hook-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('class-hook-audit');

function readAll(dir, ext, out = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    // `web/src/entry/` holds the bundles for the OTHER DOCUMENTS — the login
    // page and the <head> script (see its README). This audit asks whether a
    // class the app toggles is styled by the APP'S stylesheet, and `login.ts`
    // toggles `.visible`, which is defined in `login.html`'s own <style> block.
    // Scanning it here reported a correct hook as a hook into nothing.
    if (e.isDirectory()) { if (e.name !== 'entry') readAll(p, ext, out); continue; }
    if (e.name.endsWith(ext)) out.push({ path: p, body: fs.readFileSync(p, 'utf8') });
  }
  return out;
}

const ts = readAll(path.join(ROOT, 'web', 'src'), '.ts');
const tsAll = ts.map((f) => f.body).join('\n');
// EVERY stylesheet the served page can reach, not just this port's own.
//
// The first run flagged four classes and all four were mine: `dashboard--editing`
// and `dash-swap-pending` live in the live app's `public/css/dashboard-grid.css`,
// `is-animated` and `is-panning` in `public/css/topology.css`. Both are proxied
// (`/css/*` goes to Node) and `web/build.mjs` links them, so they are as much
// the page's stylesheet as app.css is. An audit that accuses working code is one
// people learn to ignore — the same lesson fixture-key-audit learned on ITS
// first run, and evidently one worth relearning.
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
let css = fs.readFileSync(path.join(ROOT, 'web', 'public', 'app.css'), 'utf8');
// FROZEN — the SET OF CLASS NAMES the live stylesheets answer to, not the
// stylesheets themselves. The audit only ever asks "does anything style this
// class", so the names are the whole of what it consumes, and they are a few
// kilobytes against a few hundred.
//
// Without this the live half vanishes and every class styled ONLY over there is
// accused of hooking into nothing — `is-panning` and `is-animated` were exactly
// that, and the audit failed on them.
const liveClasses = new Set(G.value('the live stylesheet class names', () => {
  let text = '';
  const dir = path.join(LIVE, 'public', 'css');
  if (fs.existsSync(dir)) {
    for (const f of fs.readdirSync(dir)) {
      if (f.endsWith('.css')) text += '\n' + fs.readFileSync(path.join(dir, f), 'utf8');
    }
  }
  // The live index.html carries a large inline stylesheet too — the shell's own
  // rules, including the mobile media query that hides the topbar switcher.
  const idx = path.join(LIVE, 'public', 'index.html');
  if (fs.existsSync(idx)) text += '\n' + fs.readFileSync(idx, 'utf8');
  return [...new Set([...text.matchAll(/\.([A-Za-z_][\w-]*)/g)].map((m) => m[1]))].sort();
}));
if (liveClasses.size < 100) {
  throw new Error('only ' + liveClasses.size + ' live class names recorded — the golden is '
    + 'broken, and this audit would accuse every live-styled class of hooking into nothing');
}
const markup = readAll(path.join(ROOT, 'web', 'src', 'ui'), '.html').map((f) => f.body).join('\n');

// Where each toggled class is written, so a hit names a file rather than a set.
const toggled = new Map();
for (const f of ts) {
  const rel = path.relative(path.join(ROOT, 'web', 'src'), f.path).split(path.sep).join('/');
  for (const m of f.body.matchAll(/classList\.(?:add|remove|toggle)\(\s*'([a-zA-Z][\w-]*)'/g)) {
    if (!toggled.has(m[1])) toggled.set(m[1], new Set());
    toggled.get(m[1]).add(rel);
  }
}

// Escape every regex metacharacter. These three sites escaped only `-`, which
// happened to be enough for a CSS class name and is not a property of the
// expression -- `-` outside a character class does not even need escaping.
const reEsc = (v) => v.replace(/[.*+?^${}()|[\]\\-]/g, '\\$&');

const styled = (c) => liveClasses.has(c)
  || new RegExp('\\.' + reEsc(c) + '(?![\\w-])').test(css);
const readBack = (c) => {
  const q = reEsc(c);
  return new RegExp("classList\\.contains\\(\\s*'" + q + "'"
    + "|querySelector\\w*\\(\\s*'[^']*\\." + q + "(?![\\w-])"
    + "|matches\\(\\s*'[^']*\\." + q + "(?![\\w-])"
    + "|closest\\(\\s*'[^']*\\." + q + "(?![\\w-])").test(tsAll);
};
const inMarkup = (c) => new RegExp('class="[^"]*\\b' + reEsc(c) + '\\b').test(markup);

// class -> why toggling it with nothing responding is nonetheless correct.
// Empty is the goal: unlike lookup-audit's orphans, there is no live behaviour
// to reproduce here — a class nothing answers does nothing on either side.
const EXPECTED = {};

const dead = [];
for (const [c, where] of [...toggled].sort()) {
  if (styled(c) || readBack(c) || inMarkup(c)) continue;
  dead.push({ c, where: [...where].sort() });
}

const problems = [];
for (const d of dead) {
  if (EXPECTED[d.c]) continue;
  problems.push(`${d.c} — toggled by ${d.where.join(', ')} and nothing responds: not in app.css, `
    + 'not read back by this port, not in the extracted markup. The stylesheet is the live app\'s '
    + 'and is not this port\'s to extend, so this is a hook into nothing.');
}
for (const c of Object.keys(EXPECTED)) {
  if (!dead.some((d) => d.c === c)) {
    problems.push(c + ' is recorded as unanswered and is answered now — delete the entry.');
  }
}

say(`class-hook-audit: ${toggled.size} classes toggled, ${dead.length} with nothing responding `
  + `(${Object.keys(EXPECTED).length} recorded)`);
if (problems.length) {
  shout('');
  for (const p of problems) shout('  ✗ ' + p);
  process.exit(1);
}
say('every class this port toggles is answered by the stylesheet, the markup or this port');
