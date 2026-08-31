'use strict';
/**
 * The Dashboard grid's layout arithmetic, live against ported.
 *
 * ── FIXED CASES FIRST, THEN A SWEEP ─────────────────────────────────────────
 *
 * The named cases cover the shapes a reader would think of: touching edges,
 * containment, a full grid, a card at each corner. The sweep then asks the same
 * questions a few thousand times over generated layouts, because `findFreeSlot`
 * is a nested scan whose answer depends on EVERY other card — the case that
 * separates a correct scan from one that transposes its loops is not a case
 * anybody writes down, it is the twenty-third card on a crowded grid.
 *
 * The generator is SEEDED and deterministic. A gate that fails once every
 * hundred runs is worse than no gate: it teaches people that red means "run it
 * again".
 *
 * ── THE TABLES ARE COMPARED TOO ─────────────────────────────────────────────
 *
 * `web/src/gen/grid-tables.ts` is generated, but a generator with a bug emits
 * consistent nonsense. The live tables are lifted here independently and
 * compared against what the port actually imports — 92 numbers that all look
 * alike, and a wrong one would simply look like the layout.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/grid-layout-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const LIFT = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/grid-layout-check.js --freeze
const G = LIFT.golden('grid-layout-check');
const src = LIFT.liveSource(ROOT, path.join('public', 'js', 'dashboard-grid.js'));

function grab(decl, close, name) {
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
const fn = (name) => grab('function ' + name + '(', '\n  }', name);

const ENTRY = path.join(ROOT, 'testdata', '.gridlayout-entry.ts');
fs.writeFileSync(ENTRY,
  "export * from '../web/src/pages/dashboard-grid-layout.js';\n" +
  "export { DEFAULT_LAYOUT, CARD_LABELS, CARD_ROOMS, LS_KEY } from '../web/src/gen/grid-tables.js';\n");
const OUT = path.join(ROOT, 'testdata', '.gridlayout-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const port = require(OUT);

// ── the live side ──────────────────────────────────────────────────────────
function liveCtx() {
  const ctx = { Math, Object, JSON, layout: [], gridRoot: null };
  vm.createContext(ctx);
  vm.runInContext([
    grab('var COLS = 24', ';', 'the grid constants'),
    grab('var MIN_W', ';', 'MIN_W/MIN_H'),
    grab('var CARD_LABELS = {', '};', 'CARD_LABELS'),
    grab('var CARD_ROOMS = {', '};', 'CARD_ROOMS'),
    grab('var DEFAULT_LAYOUT = [', '];', 'DEFAULT_LAYOUT'),
    grab("var LS_KEY = '", ';', 'LS_KEY'),
    fn('cloneLayout'), fn('mergeLayout'), fn('rectOverlaps'), fn('hasOverlap'),
    fn('inBounds'), fn('findFreeSlot'), fn('cellToPixel'), fn('ptrToCell'),
    // getCellSize reads the DOM; the port takes width/height instead. The live
    // one is redefined over a plain rect so the two conversions below compare
    // the SAME cell size rather than one measured and one supplied.
    'function getCellSize(){ var r = gridRoot; ' +
      'return { colW:(r.width - 2*PAD - (COLS-1)*GAP)/COLS, rowH:(r.height - 2*PAD - (ROWS-1)*GAP)/ROWS, r:r }; }',
  ].join('\n'), ctx);
  return ctx;
}
// LIFT-VALIDITY, GUARDED. These assert that the LIVE source yielded working
// functions, which is a question that stops existing with the reference. Without
// it the goldens answer instead, so the assertions are skipped rather than made
// to fail on an absence they were never about.
const L = LIFT.hasReference(ROOT) ? liveCtx() : {};
if (LIFT.hasReference(ROOT)) {
  assert.equal(typeof L.findFreeSlot, 'function', 'findFreeSlot did not lift');
  assert.equal(typeof L.mergeLayout, 'function', 'mergeLayout did not lift');
}

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (JSON.stringify(a) === JSON.stringify(b)) return;
  bad++;
  if (bad <= 5) console.error('DIFF %s\n  live: %j\n  port: %j', what, a, b);
}

// ── the tables ─────────────────────────────────────────────────────────────
cmp('COLS/ROWS/GAP/PAD/MIN_W/MIN_H', G.live('COLS/ROWS/GAP/PAD/MIN_W/MIN_H', () => ([L.COLS, L.ROWS, L.GAP, L.PAD, L.MIN_W, L.MIN_H])), [port.COLS, port.ROWS, port.GAP, port.PAD, port.MIN_W, port.MIN_H]);

// THE CASE TABLES BELOW ARE BUILT FROM THE LIVE CONSTANTS, and that is why they
// collapse once the reference is gone: `L.COLS` becomes undefined, every derived
// key turns into `inBounds(,,1,1)`, and no golden matches. Frozen here so the
// tables are byte-identical either side of the boundary.
//
// This does NOT weaken anything. The line above still compares the live
// constants against the port's, which is the only check these values were ever
// carrying; what follows just needs a stable grid to walk.
const [COLS, ROWS] = G.value('the live grid constants', () => [L.COLS, L.ROWS]);
cmp('DEFAULT_LAYOUT', G.live('DEFAULT_LAYOUT', () => (L.DEFAULT_LAYOUT)), port.DEFAULT_LAYOUT);
cmp('CARD_LABELS', G.live('CARD_LABELS', () => (L.CARD_LABELS)), port.CARD_LABELS);
cmp('CARD_ROOMS', G.live('CARD_ROOMS', () => (L.CARD_ROOMS)), port.CARD_ROOMS);
cmp('LS_KEY', G.live('LS_KEY', () => (L.LS_KEY)), port.LS_KEY);

// ── rectOverlaps: the named shapes ─────────────────────────────────────────
const R = (x, y, w, h) => ({ x, y, w, h });
const PAIRS = {
  identical: [R(1, 1, 2, 2), R(1, 1, 2, 2)],
  'touching on the right': [R(1, 1, 2, 2), R(3, 1, 2, 2)],
  'touching below': [R(1, 1, 2, 2), R(1, 3, 2, 2)],
  'touching at a corner': [R(1, 1, 2, 2), R(3, 3, 2, 2)],
  'overlapping by one column': [R(1, 1, 3, 2), R(3, 1, 2, 2)],
  'overlapping by one row': [R(1, 1, 2, 3), R(1, 3, 2, 2)],
  'one containing the other': [R(1, 1, 6, 6), R(2, 2, 2, 2)],
  'contained, reversed': [R(2, 2, 2, 2), R(1, 1, 6, 6)],
  'far apart': [R(1, 1, 2, 2), R(20, 20, 2, 2)],
  'same column, different rows': [R(5, 1, 4, 2), R(5, 10, 4, 2)],
  'zero width': [R(1, 1, 0, 2), R(1, 1, 2, 2)],
  'zero height': [R(1, 1, 2, 0), R(1, 1, 2, 2)],
};
for (const [name, [a, b]] of Object.entries(PAIRS)) {
  cmp('rectOverlaps(' + name + ')', G.live('rectOverlaps(' + name + ')', () => (L.rectOverlaps(a, b))), port.rectOverlaps(a, b));
  cmp('rectOverlaps(' + name + ', swapped)', G.live('rectOverlaps(' + name + ', swapped)', () => (L.rectOverlaps(b, a))), port.rectOverlaps(b, a));
}

// ── inBounds, walked over every edge ───────────────────────────────────────
for (const [x, y, w, h] of [
  [1, 1, 1, 1], [0, 1, 1, 1], [1, 0, 1, 1], [-1, -1, 1, 1],
  [COLS, ROWS, 1, 1], [COLS, ROWS, 2, 1], [COLS, ROWS, 1, 2],
  [1, 1, COLS, ROWS], [1, 1, COLS + 1, ROWS], [1, 1, COLS, ROWS + 1],
  [COLS - 3, ROWS - 3, 4, 4], [COLS - 3, ROWS - 3, 5, 4],
]) {
  cmp('inBounds(' + [x, y, w, h] + ')', G.live('inBounds(' + [x, y, w, h] + ')', () => (L.inBounds(x, y, w, h))), port.inBounds(x, y, w, h));
}

// ── hasOverlap and findFreeSlot against the shipped default ────────────────
const card = (id, x, y, w, h, visible) => ({ id, x, y, w, h, visible: visible !== false });
const LIVE_DEFAULT = G.value('the live default layout, cloned', () => L.cloneLayout(L.DEFAULT_LAYOUT));
{
  const layouts = {
    // FROZEN, for the same reason the constants above are: this row is built by
    // CALLING the live side, so it is a case table that cannot be constructed
    // once the reference is gone. `cloneLayout` and `DEFAULT_LAYOUT` are both
    // compared against the port elsewhere in this gate.
    'the shipped default': LIVE_DEFAULT,
    empty: [],
    'one card': [card('a', 5, 5, 4, 4)],
    'all hidden': LIVE_DEFAULT.map((c) => Object.assign({}, c, { visible: false })),
    'a full grid': [card('big', 1, 1, COLS, ROWS)],
    'a full grid but hidden': [card('big', 1, 1, COLS, ROWS, false)],
    'everything but the last column': [card('big', 1, 1, COLS - 1, ROWS)],
    'everything but the last row': [card('big', 1, 1, COLS, ROWS - 1)],
  };
  for (const [name, lay] of Object.entries(layouts)) {
    L.layout = lay;
    for (const [w, h] of [[1, 1], [2, 2], [4, 4], [8, 8], [COLS, 1], [1, ROWS], [COLS, ROWS]]) {
      cmp('findFreeSlot(' + name + ',' + w + 'x' + h + ')', G.live('findFreeSlot(' + name + ',' + w + 'x' + h + ')', () => (L.findFreeSlot(w, h))), port.findFreeSlot(lay, w, h));
    }
    for (const c of [R(1, 1, 2, 2), R(9, 6, 8, 4), R(20, 20, 4, 2)]) {
      for (const ex of ['__test__', 'card-system', 'card-traffic']) {
        cmp('hasOverlap(' + name + ',' + JSON.stringify(c) + ',' + ex + ')', G.live('hasOverlap(' + name + ',' + JSON.stringify(c) + ',' + ex + ')', () => (L.hasOverlap(c, ex))), port.hasOverlap(lay, c, ex));
      }
    }
  }
}

// ── the pixel conversions ──────────────────────────────────────────────────
for (const [width, height] of [[1920, 1080], [1280, 720], [800, 600], [400, 300], [100, 100]]) {
  L.gridRoot = { width, height };
  const sz = port.getCellSize(width, height);
  // THE LIVE CALL BELONGS INSIDE THE CLOSURE. Hoisted, it runs whether or not
  // the golden is being replayed — which is exactly the thing freezing exists
  // to avoid.
  cmp('getCellSize(' + width + 'x' + height + ')', G.live('getCellSize(' + width + 'x' + height + ')', () => { const live = L.getCellSize(); return { colW: live.colW, rowH: live.rowH }; }), sz);
  for (const [x, y, w, h] of [[1, 1, 1, 1], [1, 1, 24, 22], [9, 6, 8, 4], [17, 14, 8, 8], [24, 22, 1, 1]]) {
    cmp('cellToPixel(' + [width, x, y, w, h] + ')', G.live('cellToPixel(' + [width, x, y, w, h] + ')', () => (L.cellToPixel(x, y, w, h))), port.cellToPixel(sz, x, y, w, h));
  }
  for (const [px, py] of [[0, 0], [-500, -500], [20, 20], [960, 540], [1e6, 1e6], [19.9, 20.1]]) {
    cmp('ptrToCell(' + [width, px, py] + ')', G.live('ptrToCell(' + [width, px, py] + ')', () => (L.ptrToCell(px, py))), port.ptrToCell(sz, px, py));
  }
}

// ── mergeLayout ────────────────────────────────────────────────────────────
{
  const def = LIVE_DEFAULT;
  const cases = {
    'nothing saved': [],
    'one card moved': [card('card-system', 2, 2, 6, 6)],
    'every card saved': def.map((c) => Object.assign({}, c, { x: 1, y: 1 })),
    'a card that no longer exists': [card('card-obsolete', 3, 3, 2, 2)],
    'a mix of known and unknown': [card('card-system', 4, 4, 4, 4), card('card-gone', 1, 1, 1, 1)],
    'the same id twice — LAST wins': [card('card-system', 2, 2, 2, 2), card('card-system', 7, 7, 7, 7)],
    'a hidden card made visible': [card('dc-card-bgp', 3, 3, 4, 4, true)],
    'a visible card hidden': [card('card-traffic', 1, 1, 20, 5, false)],
  };
  for (const [name, saved] of Object.entries(cases)) {
    cmp('mergeLayout(' + name + ')', G.live('mergeLayout(' + name + ')', () => (L.mergeLayout(saved))), port.mergeLayout(saved));
  }
  cmp('cloneLayout is a copy, not a reference', G.live('cloneLayout is a copy, not a reference', () => (L.cloneLayout(def)[0] === def[0])), port.cloneLayout(def)[0] === def[0]);
}

// ── the seeded sweep ───────────────────────────────────────────────────────
{
  // A small deterministic PRNG. Seeded so a failure is reproducible: a gate that
  // fails once every hundred runs teaches people that red means "run it again".
  let seed = 20260824;
  const rnd = () => { seed = (seed * 1103515245 + 12345) & 0x7fffffff; return seed / 0x7fffffff; };
  const ri = (lo, hi) => lo + Math.floor(rnd() * (hi - lo + 1));
  let sweep = 0;
  for (let iter = 0; iter < 400; iter++) {
    const n = ri(0, 12);
    const lay = [];
    for (let i = 0; i < n; i++) {
      const w = ri(1, 8), h = ri(1, 8);
      lay.push(card('c' + i, ri(1, COLS - w + 1), ri(1, ROWS - h + 1), w, h, rnd() > 0.25));
    }
    L.layout = lay;
    const w = ri(1, 10), h = ri(1, 10);
    // FROZEN PER ITERATION, not per disagreement. `cmp` only fires when the two
    // sides differ, so the goldens it writes are empty on a passing run — and
    // the live call above it still ran 400 times. The correspondence between a
    // frozen answer and its layout is guaranteed by CONSTRUCTION: the RNG is
    // seeded and the port loop regenerates the identical `lay`, so iteration N
    // is the same case in both directions.
    const a = G.value('sweep#' + iter + ' live findFreeSlot', () => L.findFreeSlot(w, h));
    const b = port.findFreeSlot(lay, w, h);
    if (JSON.stringify(a) !== JSON.stringify(b)) {
      cmp('sweep#' + iter + ' findFreeSlot(' + w + 'x' + h + ') layout=' + JSON.stringify(lay), G.live('sweep#' + iter + ' findFreeSlot(' + w + 'x' + h + ') layout=' + JSON.stringify(lay), () => (a)), b);
    } else { checked++; }
    const cand = R(ri(1, COLS), ri(1, ROWS), ri(1, 6), ri(1, 6));
    const ex = 'c' + ri(0, Math.max(0, n - 1));
    const ha = G.value('sweep#' + iter + ' live hasOverlap', () => L.hasOverlap(cand, ex));
    const hb = port.hasOverlap(lay, cand, ex);
    if (ha !== hb) cmp('sweep#' + iter + ' hasOverlap', G.live('sweep#' + iter + ' hasOverlap', () => (ha)), hb); else checked++;
    sweep += 2;
  }
  // The sweep must actually EXERCISE both answers, or it proves only that two
  // functions agree on "no".
  let trues = 0, falses = 0;
  seed = 20260824;
  for (let iter = 0; iter < 400; iter++) {
    const n = ri(0, 12), lay = [];
    for (let i = 0; i < n; i++) {
      const w = ri(1, 8), h = ri(1, 8);
      lay.push(card('c' + i, ri(1, COLS - w + 1), ri(1, ROWS - h + 1), w, h, rnd() > 0.25));
    }
    const cand = R(ri(1, COLS), ri(1, ROWS), ri(1, 6), ri(1, 6));
    if (port.hasOverlap(lay, cand, 'c' + ri(0, Math.max(0, n - 1)))) trues++; else falses++;
  }
  assert.ok(trues > 20 && falses > 20,
    'the sweep is one-sided (' + trues + ' overlaps, ' + falses + ' clear) — it proves little');
  console.log('  sweep: %d comparisons, %d overlapping / %d clear', sweep, trues, falses);
}

fs.rmSync(OUT, { force: true });
if (bad) { console.error('\n%d of %d comparisons differ', bad, checked); process.exit(1); }
console.log('grid-layout-check: %d comparisons identical', checked);
