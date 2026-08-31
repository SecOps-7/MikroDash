'use strict';
/**
 * Which preset the ticked page toggles correspond to.
 *
 * `_detectViewPreset` and `_setViewPresetUI` are lifted from `public/app.js`
 * along with the two tables they close over, and run against the same checkbox
 * state as the port.
 *
 * ── THE CASES ARE THE ONES THAT MISREPORT ──────────────────────────────────
 *
 *   subset order      does NOT matter, though it looks as though it must. The
 *                     comparison is EXACT over every rendered toggle, so a `home`
 *                     selection fails `standard` on the first page `standard`
 *                     adds — at most one preset matches any state. Reversing the
 *                     list changes no answer here, which is how that was settled
 *                     rather than by argument.
 *   advanced derived  every page ticked must be `advanced`, and it is built from
 *                     the nav map rather than listed — so a page added upstream
 *                     joins it by existing.
 *   a missing toggle  is SKIPPED, not counted as off. Treating it as unchecked
 *                     reports 'custom' for a form that is simply showing fewer
 *                     rows.
 *   one box different from a preset is 'custom'.
 *
 *   node tools/view-preset-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('view-preset-check');
// ROUTED. `slice` below already carried the empty-source guard from the earlier
// batch, and its comment already said `L.liveSource` returns '' — but the read
// itself was never routed, so the gate died of ENOENT before reaching the guard
// the comment describes.
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));
const TABLES = JSON.parse(fs.readFileSync(path.join(ROOT, 'testdata', 'view-presets.json'), 'utf8'));

function slice(decl, close, name) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent; without this the helper throws at module scope before a frozen
  // output can be served. Harmless because the live half is never entered
  // once the output is frozen.
  if (src === '') return '';
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find ' + name);
  const j = src.indexOf(close, i);
  if (j === -1) throw new Error(name + ' is never closed');
  return src.slice(i, j + close.length);
}
const navSrc = slice('var PAGE_NAV_MAP = {', '\n};', 'PAGE_NAV_MAP');
const presetSrc = slice('  var VIEW_PRESETS = {', '\n  };', 'VIEW_PRESETS');
const deriveSrc = "  VIEW_PRESETS.advanced = Object.keys(PAGE_NAV_MAP).map(function (k) { return PAGE_NAV_MAP[k]; });";
// GUARDED. This asks the live SOURCE whether it still contains a line, and it
// sits OUTSIDE `slice`, so the empty-source guard the earlier batch added there
// did not cover it — the gate still died at module scope without a reference.
// Worth noting for the remaining conversions: a batch patch to one helper does
// not reach the checks written beside it.
if (LIFT.hasReference(ROOT) && src.indexOf(deriveSrc) === -1) {
  throw new Error('the `advanced` derivation line has changed — the port derives it the same way ' +
    'and would need updating with it');
}
const detectSrc = slice('  function _detectViewPreset() {', '\n  }', '_detectViewPreset');

const SETTING_KEYS = Object.keys(TABLES.navMap);

/** A document whose page checkboxes are set from a page-key predicate. */
function makeDoc(onPages, omit) {
  const nodes = {};
  for (const sKey of SETTING_KEYS) {
    if (omit && omit.indexOf(sKey) !== -1) continue;
    nodes['s_' + sKey] = { checked: onPages.indexOf(TABLES.navMap[sKey]) !== -1 };
  }
  return { getElementById: (id) => nodes[id] || null, querySelectorAll: () => [] };
}

function runLive(doc) {
  return new Function('document', '$',
    navSrc + '\n' + presetSrc + '\n' + deriveSrc + '\n' + detectSrc +
    '\nreturn _detectViewPreset();')(doc, (id) => doc.getElementById(id));
}

const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-presets.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'settings.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

function runPort(doc) {
  const prev = global.document;
  global.document = doc;
  try {
    delete require.cache[require.resolve(OUT)];
    return require(OUT).detectViewPreset();
  } finally {
    if (prev === undefined) delete global.document; else global.document = prev;
  }
}

const ALL_PAGES = SETTING_KEYS.map((k) => TABLES.navMap[k]);
const CASES = [
  ['nothing ticked', [], null],
  ['exactly home', TABLES.presets.home, null],
  ['exactly standard', TABLES.presets.standard, null],
  ['every page (advanced, DERIVED)', ALL_PAGES, null],
  ['home plus one extra', TABLES.presets.home.concat([ALL_PAGES.find((p) => TABLES.presets.home.indexOf(p) === -1)]), null],
  ['standard minus one', TABLES.presets.standard.slice(1), null],
  ['all but one', ALL_PAGES.slice(1), null],
  // A TOGGLE MISSING FROM THE DOM IS SKIPPED, so this still matches home.
  ['home with one checkbox absent from the page', TABLES.presets.home, [SETTING_KEYS[0]]],
  ['every page with several checkboxes absent', ALL_PAGES, SETTING_KEYS.slice(0, 3)],
];

const bad = [];
const liveAnswers = [];
for (const [name, pages, omit] of CASES) {
  const a = G.live(name, () => runLive(makeDoc(pages, omit)));
  liveAnswers.push(a);
  const b = runPort(makeDoc(pages, omit));
  if (a !== b) bad.push({ name, live: a, port: b });
}

// GUARDS. The corpus must actually produce more than one answer, or a port that
// always said 'custom' would agree with it everywhere.
//
// BUILT FROM THE VALUES THE LOOP ALREADY HAS rather than by running the live
// detector a second time — so it survives the reference going away, costs no
// second recording per case, and now also catches a golden flattened to one
// repeated answer.
const answers = new Set(liveAnswers);
if (answers.size < 3) {
  console.error('the LIVE detector produced only ' + answers.size + ' distinct answers (' +
                [...answers].join(', ') + ') — the corpus is not exercising the branches');
  process.exit(1);
}
if (!answers.has('advanced')) {
  console.error('no case reached `advanced` — the derived preset is untested, which is the ' +
                'one that changes when a page is added upstream');
  process.exit(1);
}

if (bad.length) {
  for (const d of bad) {
    console.error('\n' + d.name);
    console.error('  live: ' + d.live);
    console.error('  port: ' + d.port);
  }
  process.exit(1);
}
console.log('view-preset detection matches the live one (' + CASES.length + ' cases, ' +
            answers.size + ' distinct answers incl. the derived `advanced`)');
