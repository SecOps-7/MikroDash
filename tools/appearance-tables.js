'use strict';
/**
 * The appearance layer's tables, lifted from the live `public/app.js`.
 *
 * Six of them, and every one is INDEXED BY SOMETHING THE MARKUP OFFERS: the
 * font `<select>` names a font id, a `.theme-swatch` names a palette and a mode,
 * a slider hands over a level that indexes a factor array. That makes the markup
 * an enumerable source, and this generator checks the tables against it rather
 * than against a pattern — the lesson the thirteen missed alert toggles taught.
 *
 * ── WHY THE CHECK MATTERS HERE IN PARTICULAR ────────────────────────────────
 *
 * Every one of these lookups FALLS BACK rather than failing:
 *
 *   a font id not in FONTS            -> FONTS[1], Syne
 *   a size id not in FONT_SIZES       -> FONT_SIZES[2], normal
 *   a palette:mode not in the colours -> `default:dark`
 *   a level past the end of a factor array -> clamped to the last entry
 *
 * So a table that fell behind the markup produces no error anywhere. The
 * operator picks "Orbitron" and keeps Syne; they click the Everforest swatch,
 * it lights up as active, and the colours that arrive are the default palette's.
 * Nothing in the console, nothing in a log. The only way to catch that is to
 * compare the tables to the control that offers the value.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/appearance-tables.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const src = fs.readFileSync(path.join(LIVE, 'public', 'app.js'), 'utf8');
const OUT = path.join(ROOT, 'testdata', 'appearance-tables.json');

/** The whole `var NAME = ...;` line, for the one-line declarations. */
function line(name) {
  const re = new RegExp('^var ' + name + '\\s*=.*?;\\s*(//.*)?$', 'm');
  const m = src.match(re);
  if (!m) throw new Error('cannot find `var ' + name + '` as a single line');
  return m[0];
}
/** A multi-line declaration, from `decl` through its closing `close`. */
function block(decl, close, name) {
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find ' + name);
  const j = src.indexOf(close, i);
  if (j === -1) throw new Error(name + ' is never closed');
  return src.slice(i, j + close.length);
}

// RUN the declarations rather than parsing them. A regex over `[0.15, 0.25, …]`
// would work today and quietly mis-read the first entry written as `.15` or
// `1e-1`; evaluating what the browser evaluates cannot drift from it.
const decls = [
  line('THEME_KEY'), line('PALETTE_KEY'), line('CONTRAST_KEY'), line('TEXT_BRIGHT_KEY'),
  line('BG_BRIGHT_KEY'), line('FONT_KEY'), line('FONT_SIZE_KEY'), line('APPEAR_DEFAULT'),
  line('CONTRAST_FACTORS'), line('TEXT_BRIGHT_FACTORS'), line('BG_BRIGHT_FACTORS'),
  block('var FONTS = [', '\n];', 'FONTS'),
  block('var FONT_SIZES = [', '\n];', 'FONT_SIZES'),
  block('var PALETTE_COLORS = {', '\n};', 'PALETTE_COLORS'),
];
const ctx = {};
vm.createContext(ctx);
vm.runInContext(decls.join('\n'), ctx);

const tables = {
  appearDefault: ctx.APPEAR_DEFAULT,
  keys: {
    theme: ctx.THEME_KEY, palette: ctx.PALETTE_KEY, contrast: ctx.CONTRAST_KEY,
    textBright: ctx.TEXT_BRIGHT_KEY, bgBright: ctx.BG_BRIGHT_KEY,
    font: ctx.FONT_KEY, fontSize: ctx.FONT_SIZE_KEY,
  },
  fonts: ctx.FONTS,
  fontSizes: ctx.FONT_SIZES,
  contrastFactors: ctx.CONTRAST_FACTORS,
  textBrightFactors: ctx.TEXT_BRIGHT_FACTORS,
  bgBrightFactors: ctx.BG_BRIGHT_FACTORS,
  paletteColors: ctx.PALETTE_COLORS,
};

// ── The completeness check, against the markup ──────────────────────────────
const html = fs.readFileSync(path.join(ROOT, 'web', 'src', 'ui', 'page-settings.html'), 'utf8');
const bad = [];

function selectValues(id) {
  const m = html.match(new RegExp('id="' + id + '"([\\s\\S]*?)</select>'));
  if (!m) throw new Error('no <select id="' + id + '"> in the extracted markup');
  return [...m[1].matchAll(/value="([^"]*)"/g)].map((x) => x[1]);
}
function sameList(what, got, want) {
  const g = got.join(','), w = want.join(',');
  if (g !== w) bad.push(what + ':\n  markup: ' + g + '\n  table:  ' + w);
}
// ORDER, not just membership. The two lists are rendered side by side — the
// select shows the label, the table supplies the family — so a reordering that
// left the sets equal would pair every label with the wrong font.
sameList('the font select and FONTS', selectValues('appearanceFont'), tables.fonts.map((f) => f.id));
sameList('the size select and FONT_SIZES', selectValues('appearanceFontSize'), tables.fontSizes.map((f) => f.id));

const swatches = [...html.matchAll(/data-palette="([^"]*)"\s+data-mode="([^"]*)"/g)]
  .map((m) => m[1] + ':' + m[2]);
const offered = [...new Set(swatches)].sort();
const known = Object.keys(tables.paletteColors).sort();
for (const s of offered) {
  if (!known.includes(s)) bad.push('the swatch `' + s + '` has no entry in PALETTE_COLORS — ' +
    'clicking it lights up as active and paints `default:dark`');
}
for (const k of known) {
  if (!offered.includes(k)) bad.push('PALETTE_COLORS has `' + k + '` but no swatch offers it — ' +
    'either a swatch was dropped from the markup or the entry is dead');
}

// The sliders index the factor arrays by `level - 1`, clamped. A max beyond the
// end of an array is not an error: it clamps, so the top of the slider's travel
// silently does nothing at all.
for (const [id, arr] of [['appearanceContrast', 'contrastFactors'],
                         ['appearanceTextBright', 'textBrightFactors'],
                         ['appearanceBgBright', 'bgBrightFactors']]) {
  const m = html.match(new RegExp('id="' + id + '"[^>]*'));
  if (!m) { bad.push('no slider #' + id + ' in the extracted markup'); continue; }
  const min = +(m[0].match(/min="(\d+)"/) || [])[1];
  const max = +(m[0].match(/max="(\d+)"/) || [])[1];
  if (min !== 1) bad.push('#' + id + ' starts at ' + min + '; the tables are indexed from 1');
  if (max !== tables[arr].length) {
    bad.push('#' + id + ' runs to ' + max + ' but ' + arr + ' holds ' + tables[arr].length +
      ' — levels past the end clamp, so that part of the slider does nothing');
  }
}
// And the neutral midpoint has to BE one, or "no change" is off-centre and the
// remove-the-property shortcut fires at the wrong notch.
if (tables.contrastFactors[tables.appearDefault - 1] !== 1) {
  bad.push('APPEAR_DEFAULT=' + tables.appearDefault + ' is not the neutral 1.0 notch of CONTRAST_FACTORS');
}

if (bad.length) {
  console.error('the appearance tables and the markup disagree:\n\n' + bad.join('\n') + '\n');
  process.exit(1);
}

const body = JSON.stringify(tables, null, 2) + '\n';
if (process.argv.includes('--check')) {
  const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : null;
  if (cur !== body) {
    console.error('testdata/appearance-tables.json is stale — run: node tools/appearance-tables.js');
    process.exit(1);
  }
  console.log(`appearance tables up to date (${tables.fonts.length} fonts, ${tables.fontSizes.length} sizes, ` +
    `${known.length} palettes, ${tables.contrastFactors.length} levels)`);
} else {
  fs.writeFileSync(OUT, body);
  console.log('wrote testdata/appearance-tables.json (' + known.length + ' palettes, all offered by a swatch)');
}
