'use strict';
/**
 * The Connections Map's arc geometry and highlight rule, live against ported.
 *
 * ── A PATH STRING COMPARES EXACTLY, SO THE SWEEP IS WIDE ────────────────────
 *
 * `mapArcD` is four numbers in and a path out, with everything at one decimal.
 * That is the cheapest thing in this card to compare and the easiest to get
 * subtly wrong — a normal flipped the other way, a rise floor at the wrong
 * value, one more decimal place. So it is driven over a seeded sweep of a few
 * thousand point pairs as well as the named cases, because the interesting
 * inputs are the ones where the normal flips and the rise floor bites, and those
 * are hard to pick by hand.
 *
 * ── THE HIGHLIGHT RULE IS RELATIVE ──────────────────────────────────────────
 *
 * `hot` at half the busiest, so the cases fix a max and walk a count across it.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/map-geometry-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource. This gate already carried the empty-source guard in
// its slicing helper, but the READ itself still used fs.readFileSync and died of
// ENOENT before reaching it — the guard's own comment described behaviour the
// code did not have.
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('map-geometry-check');
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

function grab(decl, close, name) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent; without this the helper throws at module scope before a frozen
  // output can be served. Harmless because the live half is never entered
  // once the output is frozen.
  if (src === '') return '';
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find ' + name);
  return src.slice(i, src.indexOf(close, i) + close.length);
}
const arcSrc = G.value('arcSrc', () => grab('function _dcMapMakeArcD(', '\n  }', '_dcMapMakeArcD'));
const hlSrc = G.value('hlSrc', () => grab('function _dcMapUpdateHighlights(', '\n  }', '_dcMapUpdateHighlights'));
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['arcSrc', arcSrc], ['hlSrc', hlSrc]]) {
  assert.ok(typeof __v === 'string' && __v.length > 4,
    'the recorded ' + __n + ' is empty — the golden is broken');
}
if (LIFT.hasReference(ROOT)) assert.ok(arcSrc.includes('toFixed(1)'), 'the arc slice lost its rounding');
if (LIFT.hasReference(ROOT)) assert.ok(hlSrc.includes("'hot'"), 'the highlight slice lost its classes');

const ENTRY = path.join(ROOT, 'testdata', '.mapgeo-entry.ts');
fs.writeFileSync(ENTRY,
  "export { mapArcD, applyMapHighlights, mapMaxCount } from '../web/src/pages/dashboard-map-geometry.js';\n");
const OUT = path.join(ROOT, 'testdata', '.mapgeo-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const port = require(OUT);

const ctx = { Math, String, Object, _dcMapPathEls: {} };
vm.createContext(ctx);
vm.runInContext(arcSrc + '\n' + hlSrc, ctx);

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (JSON.stringify(a) === JSON.stringify(b)) return;
  bad++;
  if (bad <= 5) shout('DIFF %s\n  live: %j\n  port: %j', what, a, b);
}

// ── named arcs ─────────────────────────────────────────────────────────────
const PAIRS = [
  [0, 0, 0, 0],            // zero length — no arc
  [10, 10, 10, 10],        // zero length elsewhere
  [0, 0, 100, 0],          // due east
  [100, 0, 0, 0],          // due west
  [0, 0, 0, 100],          // due south — the normal must flip
  [0, 0, 0, -100],         // due north
  [0, 0, 100, 100],        // diagonal
  [0, 0, -100, 100],
  [0, 0, 100, -100],
  [0, 0, -100, -100],
  [0, 0, 1, 0],            // shorter than the rise floor
  [0, 0, 114, 0],          // 114*0.35 = 39.9, just under the floor
  [0, 0, 115, 0],          // 40.25, just over
  [500, 250, 100, 400],    // map-ish coordinates
  [123.456, 78.9, 456.789, 12.3],
  [0, 0, 0.0001, 0],       // tiny but non-zero
  [-50, -50, 50, 50],
];
for (const [x1, y1, x2, y2] of PAIRS) {
  cmp('mapArcD(' + [x1, y1, x2, y2] + ')', ctx._dcMapMakeArcD(x1, y1, x2, y2), port.mapArcD(x1, y1, x2, y2));
}

// ── a seeded sweep ─────────────────────────────────────────────────────────
{
  let seed = 20260824;
  const rnd = () => { seed = (seed * 1103515245 + 12345) & 0x7fffffff; return seed / 0x7fffffff; };
  const r = (lo, hi) => lo + rnd() * (hi - lo);
  let flips = 0, floored = 0, n = 0;
  for (let i = 0; i < 3000; i++) {
    const x1 = r(-600, 600), y1 = r(-300, 300), x2 = r(-600, 600), y2 = r(-300, 300);
    const a = ctx._dcMapMakeArcD(x1, y1, x2, y2), b = port.mapArcD(x1, y1, x2, y2);
    if (a !== b) { cmp('sweep#' + i + ' (' + [x1, y1, x2, y2] + ')', a, b); } else { checked++; }
    n++;
    const dx = x2 - x1, dy = y2 - y1, dist = Math.hypot(dx, dy);
    if (dx / dist > 0) flips++;
    if (dist * 0.35 < 40) floored++;
  }
  // The sweep must reach both branches, or it is 3000 copies of one case.
  assert.ok(flips > 200, 'only ' + flips + ' of ' + n + ' sweep arcs flipped the normal');
  assert.ok(floored > 20, 'only ' + floored + ' of ' + n + ' sweep arcs hit the rise floor');
  say('  sweep: %d arcs, %d flipped the normal, %d hit the rise floor', n, flips, floored);
}

// ── the highlight rule ─────────────────────────────────────────────────────
function classesFor(counts, ccs) {
  const mk = () => {
    const set = new Set();
    return {
      classList: {
        add: (c) => set.add(c),
        remove: (...c) => c.forEach((x) => set.delete(x)),
      },
      set,
    };
  };
  const live = {}, mine = {};
  for (const cc of ccs) { live[cc] = mk(); mine[cc] = mk(); }
  ctx._dcMapPathEls = live;
  ctx._dcMapUpdateHighlights(counts);
  port.applyMapHighlights(mine, counts);
  const read = (o) => Object.fromEntries(Object.keys(o).map((k) => [k, [...o[k].set].sort()]));
  return [read(live), read(mine)];
}
const HL = {
  'one country': [{ US: 5 }, ['US']],
  'a clear leader': [{ US: 100, DE: 10, FR: 1 }, ['US', 'DE', 'FR']],
  'exactly half is HOT': [{ US: 100, DE: 50 }, ['US', 'DE']],
  'just under half': [{ US: 100, DE: 49 }, ['US', 'DE']],
  'all equal': [{ US: 5, DE: 5, FR: 5 }, ['US', 'DE', 'FR']],
  'a country with zero': [{ US: 5, DE: 0 }, ['US', 'DE']],
  'no counts at all': [{}, ['US', 'DE']],
  'a country with no shape': [{ ZZ: 9 }, ['US']],
  'a shape with no count': [{ US: 3 }, ['US', 'DE', 'FR']],
  'all zero': [{ US: 0, DE: 0 }, ['US', 'DE']],
};
for (const [name, [counts, ccs]] of Object.entries(HL)) {
  const [a, b] = classesFor(counts, ccs);
  cmp('highlights: ' + name, a, b);
}
// A country that goes quiet must LOSE its class — two rounds on the same nodes.
{
  const mk = () => {
    const set = new Set();
    return { classList: { add: (c) => set.add(c), remove: (...c) => c.forEach((x) => set.delete(x)) }, set };
  };
  const live = { US: mk(), DE: mk() }, mine = { US: mk(), DE: mk() };
  ctx._dcMapPathEls = live;
  ctx._dcMapUpdateHighlights({ US: 10, DE: 10 });
  port.applyMapHighlights(mine, { US: 10, DE: 10 });
  ctx._dcMapUpdateHighlights({ US: 10 });
  port.applyMapHighlights(mine, { US: 10 });
  const read = (o) => Object.fromEntries(Object.keys(o).map((k) => [k, [...o[k].set].sort()]));
  cmp('highlights: a country that goes quiet loses its class', read(live), read(mine));
  assert.deepEqual(read(live).DE, [], 'the live map left a quiet country lit');
}

// ── believability ──────────────────────────────────────────────────────────
{
  const d = ctx._dcMapMakeArcD(0, 0, 100, 0);
  assert.match(d, /^M0\.0,0\.0 Q/, 'the live arc does not start where it should: ' + d);
  assert.ok(/Q[\d.-]+,-\d/.test(d), 'the live arc does not bow upward: ' + d);
  assert.equal(ctx._dcMapMakeArcD(5, 5, 5, 5), '', 'a zero-length chord should give no arc');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d comparisons differ', bad, checked); process.exit(1); }
say('map-geometry-check: %d comparisons identical', checked);
