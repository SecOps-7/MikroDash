'use strict';
/**
 * The appearance layer, live against ported.
 *
 * Every function here writes only to the DOM and to localStorage, so the two
 * implementations can be driven from ONE fake document each and compared on the
 * state they leave behind — attributes, custom properties, stored values and
 * which swatch ended up active. That is a stronger check than a screenshot:
 * a swatch that lights up while the colours come from the wrong palette looks
 * correct in a picture and is caught here.
 *
 * ── WHAT THE CORPUS IS BUILT TO CATCH ───────────────────────────────────────
 *
 *   the neutral notch     REMOVES the properties. A port that computed and set
 *                         the base colour instead would look identical today and
 *                         override the stylesheet forever after. The comparison
 *                         distinguishes "absent" from "set to the same value".
 *   a stored level of 0   reads back as 8, because `parseInt('0') || 8` is 8.
 *                         Unreachable from the slider, reachable by hand.
 *   out-of-range levels   clamp rather than throw or wrap.
 *   `default` palette     REMOVES data-palette but STORES the string "default".
 *                         The two differ on purpose.
 *   an unknown font       falls back BY POSITION — FONTS[1], Syne — so a table
 *                         reordered upstream changes what "unknown" means.
 *   px: null              removes font-size rather than writing 16px.
 *   both light and dark   of every palette, because the fallback for a missing
 *                         key is `default:dark` and a scheme dropped from the
 *                         table would silently paint the dark colours.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/appearance-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/appearance-check.js --freeze
const G = L.golden('appearance-check');
const src = L.liveSource(ROOT);
const T = JSON.parse(fs.readFileSync(path.join(ROOT, 'testdata', 'appearance-tables.json'), 'utf8'));

function slice(decl, close, name) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent, and this then threw `cannot find <name>` at module scope — before
  // any frozen output could be served. An empty slice is harmless because the
  // live half is never entered when the output is frozen.
  if (src === '') return '';
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find ' + name + ' — it has moved or been rewritten');
  const j = src.indexOf(close, i);
  if (j === -1) throw new Error(name + ' is never closed');
  return src.slice(i, j + close.length);
}
function fn(decl, name) { return slice(decl, '\n}', name); }

// The live half, lifted verbatim. The tables come from the generated JSON
// rather than being sliced again: they are the SAME tables the port imports, so
// a difference found here is a difference in the LOGIC, which is the point.
const liveSrc = [
  fn('function applyTheme(t){', 'applyTheme'),
  fn('function _scaleBright(c, factor) {', '_scaleBright'),
  fn('function _reapplyTextVars() {', '_reapplyTextVars'),
  fn('function _reapplyBgVars() {', '_reapplyBgVars'),
  fn('function applyPalette(palette, scheme) {', 'applyPalette'),
  fn('function _syncSwatches() {', '_syncSwatches'),
  fn('function applyFont(fontId) {', 'applyFont'),
  fn('function applyFontSize(sizeId) {', 'applyFontSize'),
].join('\n\n');

// The three boot blocks, as IIFEs, sliced whole. ORDER MATTERS and is the order
// they appear in the file: theme, then font, then palette. `applyTheme`
// recomputes the colour variables before the contrast and brightness attributes
// exist, so running it after the palette block would leave that first, default
// computation standing.
const bootThemeSrc = slice("(function(){\n  var saved='dark';", '})();', 'the theme boot block');
const bootFontSrc = slice("(function(){\n  var savedFont     = 'syne';", '})();', 'the font boot block');
const bootPaletteSrc = slice("(function(){\n  var savedPalette   = 'default';", '})();', 'the palette boot block');
// The theme button is wired at TOP LEVEL, outside the wiring IIFE below, so it
// is lifted separately and appended to it here.
const themeWireSrc = slice("var themeToggle = $('themeToggle');", '\n});', 'the theme toggle wiring');

// The WIRING block, whole. Running the real IIFE rather than lifting the one
// handler out of it means the listeners themselves are compared: which event
// each control listens for, and whether it is connected at all.
const wireSrc = slice("(function(){\n  document.querySelectorAll('.theme-swatch').forEach(function(sw) {", '})();',
  'the wiring block');

const OUT = path.join(ROOT, 'testdata', '.appearance-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'appearance.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

// ── One fake world, shared in shape by both sides ───────────────────────────

function makeStyle() {
  const props = {};
  const style = {
    setProperty(k, v) { props[k] = String(v); },
    removeProperty(k) { delete props[k]; },
    getPropertyValue(k) { return props[k] === undefined ? '' : props[k]; },
    _props: props,
  };
  // `applyFontSize` assigns `.fontSize` directly, and `removeProperty('font-size')`
  // must clear that same value — they are one property spelled two ways.
  Object.defineProperty(style, 'fontSize', {
    get() { return props['font-size'] === undefined ? '' : props['font-size']; },
    set(v) { props['font-size'] = String(v); },
  });
  return style;
}

/** A node that can take listeners, so the WIRING is under test and not only
 *  the functions it wires. A control that is never connected renders perfectly
 *  and does nothing — the failure this whole tick is about. */
function makeNode(extra) {
  return Object.assign({ _h: {}, addEventListener(n, f) { (this._h[n] = this._h[n] || []).push(f); } }, extra);
}

function makeWorld(swatchKeys) {
  const attrs = {};
  const style = makeStyle();
  const docEl = {
    style,
    getAttribute: (k) => (attrs[k] === undefined ? null : attrs[k]),
    setAttribute: (k, v) => { attrs[k] = String(v); },
    removeAttribute: (k) => { delete attrs[k]; },
    _attrs: attrs,
  };
  const swatches = swatchKeys.map((key) => {
    const [palette, mode] = key.split(':');
    const classes = new Set();
    return makeNode({
      dataset: { palette, mode },
      _key: key,
      classList: { toggle: (c, on) => { if (on) classes.add(c); else classes.delete(c); }, _set: classes },
    });
  });
  const nodes = {
    themeIconPath: makeNode({ _d: null, setAttribute(k, v) { if (k === 'd') this._d = v; } }),
    themeToggle: makeNode({}),
    appearanceContrast: makeNode({ value: '' }),
    appearanceTextBright: makeNode({ value: '' }),
    appearanceBgBright: makeNode({ value: '' }),
    appearanceFont: makeNode({ value: '' }),
    appearanceFontSize: makeNode({ value: '' }),
  };
  const store = {};
  const doc = {
    documentElement: docEl,
    getElementById: (id) => nodes[id] || null,
    querySelectorAll: (sel) => {
      if (sel === '.theme-swatch') return swatches;
      throw new Error('unexpected selector ' + sel);
    },
    _h: {},
    addEventListener(n, f) { (this._h[n] = this._h[n] || []).push(f); },
  };
  const localStorage = {
    getItem: (k) => (store[k] === undefined ? null : store[k]),
    setItem: (k, v) => { store[k] = String(v); },
  };
  return {
    doc, localStorage, store, nodes, swatches,
    /** `this` is bound to the target: the live slider handlers read `this.value`. */
    fire(target, name, ev) {
      for (const f of (target._h[name] || [])) f.call(target, ev);
    },
    swatch(key) {
      const sw = swatches.find((x) => x._key === key);
      if (!sw) throw new Error('no swatch ' + key);
      return sw;
    },
    state() {
      return JSON.stringify({
        attrs: docEl._attrs,
        props: style._props,
        store,
        icon: nodes.themeIconPath._d,
        active: swatches.filter((s) => s.classList._set.has('active')).map((s) => s._key).sort(),
        controls: {
          contrast: nodes.appearanceContrast.value,
          textBright: nodes.appearanceTextBright.value,
          bgBright: nodes.appearanceBgBright.value,
          font: nodes.appearanceFont.value,
          fontSize: nodes.appearanceFontSize.value,
        },
      }, null, 1);
    },
  };
}

const SWATCHES = Object.keys(T.paletteColors);

function liveRun(seedStore, body) {
  const w = makeWorld(SWATCHES);
  Object.assign(w.store, seedStore);
  const ctx = {
    document: w.doc, localStorage: w.localStorage, Math, JSON, String, Number, parseInt, Object,
    $: (id) => w.doc.getElementById(id),
    PALETTE_COLORS: T.paletteColors, CONTRAST_FACTORS: T.contrastFactors,
    TEXT_BRIGHT_FACTORS: T.textBrightFactors, BG_BRIGHT_FACTORS: T.bgBrightFactors,
    FONTS: T.fonts, FONT_SIZES: T.fontSizes, APPEAR_DEFAULT: T.appearDefault,
    PALETTE_KEY: T.keys.palette, THEME_KEY: T.keys.theme, CONTRAST_KEY: T.keys.contrast,
    TEXT_BRIGHT_KEY: T.keys.textBright, BG_BRIGHT_KEY: T.keys.bgBright,
    FONT_KEY: T.keys.font, FONT_SIZE_KEY: T.keys.fontSize,
  };
  vm.createContext(ctx);
  vm.runInContext(liveSrc, ctx);
  vm.runInContext(
    'var __boot = function(){' + bootThemeSrc + bootFontSrc + bootPaletteSrc + '};' +
    'var __wire = function(){' + wireSrc + themeWireSrc + '};', ctx);
  body(ctx, w);
  return w.state();
}

function portRun(seedStore, body) {
  const w = makeWorld(SWATCHES);
  Object.assign(w.store, seedStore);
  const prevDoc = global.document, prevLS = global.localStorage;
  global.document = w.doc;
  global.localStorage = w.localStorage;
  try {
    delete require.cache[require.resolve(OUT)];
    body(require(OUT), w);
  } finally {
    if (prevDoc === undefined) delete global.document; else global.document = prevDoc;
    if (prevLS === undefined) delete global.localStorage; else global.localStorage = prevLS;
  }
  return w.state();
}

const bad = [];
let cases = 0;
function compare(what, seed, liveBody, portBody) {
  cases++;
  const a = G.live(G.seq(), () => liveRun(seed, liveBody));
  const b = portRun(seed, portBody);
  if (a !== b) bad.push(what + '\n  live: ' + a + '\n  port: ' + b);
}

// ── 1. Every palette, in both schemes it is offered in ──────────────────────
for (const key of SWATCHES) {
  const [palette, mode] = key.split(':');
  compare('applyPalette(' + palette + ', ' + mode + ')', {},
    (c) => c.applyPalette(palette, mode),
    (p) => p.applyPalette(palette, mode));
}
// A falsy palette, and the explicit "default" — both REMOVE the attribute and
// STORE the string.
for (const p of ['', 'default']) {
  for (const m of ['dark', 'light']) {
    compare('applyPalette(' + JSON.stringify(p) + ', ' + m + ')', {},
      (c) => c.applyPalette(p, m), (mod) => mod.applyPalette(p, m));
  }
}
// No scheme argument at all: it reads data-theme, which is absent here, so the
// `|| 'dark'` tail is what answers.
compare('applyPalette with no scheme', {},
  (c) => c.applyPalette('nord'), (p) => p.applyPalette('nord'));

// ── 2. Every level of every slider, against every palette ───────────────────
const LEVELS = ['1', '7', '8', '9', '15', '0', '16', '-3', 'abc', '', '8.9'];
for (const key of SWATCHES) {
  const [palette, mode] = key.split(':');
  for (const lvl of LEVELS) {
    compare('contrast=' + JSON.stringify(lvl) + ' on ' + key, {},
      (c, w) => {
        c.applyPalette(palette, mode);
        w.doc.documentElement.setAttribute('data-contrast', lvl);
        c._reapplyTextVars();
      },
      (p, w) => {
        p.applyPalette(palette, mode);
        w.doc.documentElement.setAttribute('data-contrast', lvl);
        p.reapplyTextVars();
      });
  }
}
// Text brightness and background brightness, with the OTHER slider off neutral
// so the "both at default" early return cannot mask a difference.
for (const key of SWATCHES) {
  const [palette, mode] = key.split(':');
  for (const lvl of LEVELS) {
    compare('textBright=' + JSON.stringify(lvl) + ' (contrast 12) on ' + key, {},
      (c, w) => {
        c.applyPalette(palette, mode);
        w.doc.documentElement.setAttribute('data-contrast', '12');
        w.doc.documentElement.setAttribute('data-text-bright', lvl);
        c._reapplyTextVars();
      },
      (p, w) => {
        p.applyPalette(palette, mode);
        w.doc.documentElement.setAttribute('data-contrast', '12');
        w.doc.documentElement.setAttribute('data-text-bright', lvl);
        p.reapplyTextVars();
      });
    compare('bgBright=' + JSON.stringify(lvl) + ' on ' + key, {},
      (c, w) => {
        c.applyPalette(palette, mode);
        w.doc.documentElement.setAttribute('data-bg-bright', lvl);
        c._reapplyBgVars();
      },
      (p, w) => {
        p.applyPalette(palette, mode);
        w.doc.documentElement.setAttribute('data-bg-bright', lvl);
        p.reapplyBgVars();
      });
  }
}
// An attribute that is ABSENT, not merely odd — the `|| String(APPEAR_DEFAULT)`
// path, which is a different expression from the `|| APPEAR_DEFAULT` one.
compare('no level attributes at all', {},
  (c) => c._reapplyTextVars(), (p) => p.reapplyTextVars());
compare('no bg level attribute', {},
  (c) => c._reapplyBgVars(), (p) => p.reapplyBgVars());
// An unknown palette:scheme pair, which falls back to default:dark.
compare('an unknown palette', {},
  (c, w) => {
    w.doc.documentElement.setAttribute('data-palette', 'nosuchtheme');
    w.doc.documentElement.setAttribute('data-theme', 'dark');
    w.doc.documentElement.setAttribute('data-contrast', '3');
    c._reapplyTextVars();
  },
  (p, w) => {
    w.doc.documentElement.setAttribute('data-palette', 'nosuchtheme');
    w.doc.documentElement.setAttribute('data-theme', 'dark');
    w.doc.documentElement.setAttribute('data-contrast', '3');
    p.reapplyTextVars();
  });

// ── 3. Fonts and sizes, every id plus the misses ────────────────────────────
for (const f of T.fonts.map((x) => x.id).concat(['nosuchfont', ''])) {
  compare('applyFont(' + JSON.stringify(f) + ')', {},
    (c) => c.applyFont(f), (p) => p.applyFont(f));
}
for (const s of T.fontSizes.map((x) => x.id).concat(['nosuchsize', ''])) {
  compare('applyFontSize(' + JSON.stringify(s) + ')', {},
    (c) => c.applyFontSize(s), (p) => p.applyFontSize(s));
}
// normal -> a size -> normal again. The middle step writes font-size and the
// last must REMOVE it; a port that wrote 16px would pass a single-step test.
compare('size round trip through normal', {},
  (c) => { c.applyFontSize('xl'); c.applyFontSize('normal'); },
  (p) => { p.applyFontSize('xl'); p.applyFontSize('normal'); });

// ── 4. Boot, from stored state ──────────────────────────────────────────────
const K = T.keys;
const BOOTS = [
  ['nothing stored', {}],
  ['a full set', { [K.font]: 'orbitron', [K.fontSize]: 'lg', [K.palette]: 'nord',
    [K.contrast]: '12', [K.textBright]: '3', [K.bgBright]: '14' }],
  ['a stored 0 (parseInt 0 is falsy, so it reads back as the default)',
    { [K.contrast]: '0', [K.textBright]: '0', [K.bgBright]: '0' }],
  ['unparseable levels', { [K.contrast]: 'abc', [K.textBright]: '', [K.bgBright]: 'NaN' }],
  ['out-of-range levels', { [K.contrast]: '99', [K.bgBright]: '-4' }],
  ['palette explicitly default', { [K.palette]: 'default' }],
  ['an unknown palette', { [K.palette]: 'nosuchtheme' }],
  ['an unknown font and size', { [K.font]: 'nosuchfont', [K.fontSize]: 'nosuchsize' }],
  ['a light palette (but boot never sets data-theme)', { [K.palette]: 'github', [K.contrast]: '5' }],
];
for (const [what, seed] of BOOTS) {
  compare('boot: ' + what, seed, (c) => c.__boot(), (p) => p.initAppearance());
}

// ── 5. The wired controls, driven through the events they listen for ────────
//
// Everything below goes through `__wire` / `wireAppearance` and then FIRES an
// event, so a control that was never connected fails here. That is the failure
// mode this tick exists for: the markup renders, the function is correct, and
// nothing joins them.

/** Wire both sides, then run a body that fires events at the world. */
function wired(what, seed, attrs, act) {
  compare(what, seed,
    (c, w) => {
      for (const [k, v] of Object.entries(attrs || {})) w.doc.documentElement.setAttribute(k, v);
      c.__wire();
      act(w);
    },
    (p, w) => {
      for (const [k, v] of Object.entries(attrs || {})) w.doc.documentElement.setAttribute(k, v);
      p.wireAppearance();
      act(w);
    });
}

// Opening Settings pushes the DOM's state back into the controls.
const SYNCS = [
  ['from a full set', { [K.font]: 'poppins', [K.fontSize]: 'sm' },
    { 'data-contrast': '11', 'data-text-bright': '2', 'data-bg-bright': '15',
      'data-palette': 'dracula', 'data-theme': 'dark' }],
  ['with nothing stored and no attributes', {}, {}],
  ['a light scheme, so only the light swatch is active', {},
    { 'data-palette': 'github', 'data-theme': 'light' }],
  ['the default palette has no attribute but still has a swatch', {},
    { 'data-theme': 'dark' }],
];
for (const [what, seed, attrs] of SYNCS) {
  wired('pagechange sync ' + what, seed, attrs,
    (w) => w.fire(w.doc, 'mikrodash:pagechange', { detail: 'settings' }));
}
// A pagechange for ANY OTHER page must leave everything alone — including the
// controls, which still hold their initial empty values.
wired('pagechange for another page is ignored', { [K.font]: 'poppins' },
  { 'data-contrast': '11' }, (w) => w.fire(w.doc, 'mikrodash:pagechange', { detail: 'dashboard' }));

// Clicking a swatch. Every one of them, because the click handler reads the
// dataset and a swatch wired to the wrong palette is invisible in a screenshot.
for (const key of SWATCHES) {
  wired('click the ' + key + ' swatch', {}, {}, (w) => w.fire(w.swatch(key), 'click', {}));
}

// Dragging a slider: the attribute, the stored value and the repaint, on the
// `input` event specifically. A port that listened for `change` would pass every
// function-level test and stop the page updating while the handle moves.
for (const [id, lvl] of [['appearanceContrast', '3'], ['appearanceContrast', '15'],
                         ['appearanceTextBright', '1'], ['appearanceBgBright', '13'],
                         ['appearanceBgBright', '8']]) {
  wired('drag ' + id + ' to ' + lvl, {}, { 'data-palette': 'nord', 'data-theme': 'dark' },
    (w) => { w.nodes[id].value = lvl; w.fire(w.nodes[id], 'input', {}); });
}
// Two sliders in sequence, so the second recomputation sees the first's result.
wired('drag contrast then text brightness', {}, { 'data-theme': 'dark' }, (w) => {
  w.nodes.appearanceContrast.value = '13';
  w.fire(w.nodes.appearanceContrast, 'input', {});
  w.nodes.appearanceTextBright.value = '2';
  w.fire(w.nodes.appearanceTextBright, 'input', {});
});
// And back to neutral, which must REMOVE the properties it just set.
wired('drag contrast away and back to neutral', {}, {}, (w) => {
  for (const v of ['4', String(T.appearDefault)]) {
    w.nodes.appearanceContrast.value = v;
    w.fire(w.nodes.appearanceContrast, 'input', {});
  }
});

// Choosing a font or a size, on `change`.
for (const f of ['orbitron', 'system', 'nosuchfont']) {
  wired('choose the font ' + f, {}, {},
    (w) => { w.nodes.appearanceFont.value = f; w.fire(w.nodes.appearanceFont, 'change', {}); });
}
for (const sz of ['xs', 'normal', 'xl']) {
  wired('choose the size ' + sz, {}, {},
    (w) => { w.nodes.appearanceFontSize.value = sz; w.fire(w.nodes.appearanceFontSize, 'change', {}); });
}

// ── 6. The light/dark toggle ────────────────────────────────────────────────
//
// A separate axis from the palette: `applyTheme` changes the scheme and leaves
// the palette alone, and — unlike `applyPalette` — it does NOT re-sync the
// swatches. The comparison covers which swatch is active, so that quirk is
// pinned rather than accidentally corrected.
for (const t of ['light', 'dark', 'weird', '']) {
  compare('applyTheme(' + JSON.stringify(t) + ')', {},
    (c) => c.applyTheme(t), (p) => p.applyTheme(t));
}
// On top of a palette, where the scheme decides which colour table is read.
for (const key of SWATCHES) {
  const [palette, mode] = key.split(':');
  compare('applyTheme after selecting ' + key, {},
    (c) => { c.applyPalette(palette, mode); c.applyTheme(mode === 'light' ? 'dark' : 'light'); },
    (p) => { p.applyPalette(palette, mode); p.applyTheme(mode === 'light' ? 'dark' : 'light'); });
}
// With the sliders off neutral, so the recomputation actually produces values.
compare('applyTheme with contrast and brightness set', {},
  (c, w) => {
    w.doc.documentElement.setAttribute('data-contrast', '13');
    w.doc.documentElement.setAttribute('data-bg-bright', '3');
    c.applyTheme('light');
  },
  (p, w) => {
    w.doc.documentElement.setAttribute('data-contrast', '13');
    w.doc.documentElement.setAttribute('data-bg-bright', '3');
    p.applyTheme('light');
  });

// Booting from each stored theme, including none and a value that is neither.
for (const [what, seed] of [
  ['no theme stored', {}],
  ['light', { [K.theme]: 'light' }],
  ['dark', { [K.theme]: 'dark' }],
  ['a theme that is neither', { [K.theme]: 'sepia' }],
  ['light, with a palette and levels', { [K.theme]: 'light', [K.palette]: 'github', [K.contrast]: '11' }],
]) {
  compare('boot theme: ' + what, seed, (c) => c.__boot(), (p) => p.initAppearance());
}

// Clicking the button, through the real wiring on both sides. It reads the
// CURRENT attribute, so the third click has to land back where it started.
for (const [what, attrs, clicks] of [
  ['from the default (no attribute)', {}, 1],
  ['from dark', { 'data-theme': 'dark' }, 1],
  ['from light', { 'data-theme': 'light' }, 1],
  ['twice, back to where it started', { 'data-theme': 'dark' }, 2],
  ['three times', { 'data-theme': 'light' }, 3],
  ['from an attribute that is neither', { 'data-theme': 'sepia' }, 1],
]) {
  wired('click the theme toggle ' + what, {}, attrs, (w) => {
    for (let i = 0; i < clicks; i++) w.fire(w.nodes.themeToggle, 'click', {});
  });
}
// And the swatch highlight after a toggle — stale until Settings re-syncs it,
// which is exactly what the two halves of this case check.
wired('toggle the theme, then open Settings', {}, { 'data-palette': 'nord', 'data-theme': 'dark' },
  (w) => {
    w.fire(w.nodes.themeToggle, 'click', {});
    w.fire(w.doc, 'mikrodash:pagechange', { detail: 'settings' });
  });

fs.rmSync(OUT, { force: true });
if (bad.length) {
  console.error('the appearance layer differs from the live one:\n\n' + bad.slice(0, 4).join('\n\n') +
    (bad.length > 4 ? '\n\n… and ' + (bad.length - 4) + ' more' : '') + '\n');
  process.exit(1);
}
console.log(`appearance matches the live layer (${cases} cases across ${SWATCHES.length} palettes, ` +
  `${T.fonts.length} fonts, ${LEVELS.length} slider levels)`);
