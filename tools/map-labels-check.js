'use strict';
/**
 * The fleet map's LABEL COLLISION rule, live against ported.
 *
 * ── WHAT THIS GATES, AND WHAT IT DELIBERATELY DOES NOT ─────────────────────
 *
 * `web/src/pages/routers-map.ts` is the map's imperative half: SVG construction,
 * pointer gestures, `getBoundingClientRect` positioning. None of that survives a
 * headless harness faithfully — a fake `getBoundingClientRect` would be
 * comparing this file's arithmetic against itself — so it is verified in a
 * browser and recorded as such in `tools/page-gate-audit.js`.
 *
 * ONE PIECE OF IT IS PURE, and it is the piece with a real decision in it:
 * deciding which place names to drop when they would overlap. It takes numbers
 * and returns a subset, so it can be compared exactly.
 *
 * ── WHY IT IS WORTH GATING ON ITS OWN ──────────────────────────────────────
 *
 * The rule compares the BOXES the text will occupy, not the anchor points. The
 * live comment says why: "Berlin, BE, DE" is many times wider than the gap
 * between two capitals, so comparing anchors keeps every label and none is
 * readable. Everything is divided by `scale`, so zooming in separates the boxes
 * and the hidden names return one by one — which means a wrong divisor is
 * invisible at one zoom level and wrong at every other.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/map-labels-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('map-labels-check');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource, which yields '' when the reference is gone rather
// than throwing ENOENT. What the gate lifts from it is frozen below.
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

// The live rule is an INLINE IIFE inside `apply`, with no function boundary to
// slice on. Anchored on its first statement and its closing `}());`, then
// wrapped so it can be called — the same shape `tools/poll-tables.js` uses for
// the three statements of `populate`'s poll half.
const START = '      var fsz = 8 / scale;';
const END = '    }());';
const a = src.indexOf(START);
if (LIFT.hasReference(ROOT)) if (a === -1) throw new Error('cannot find the label-collision block — it has moved or been rewritten');
const b = src.indexOf(END, a);
if (LIFT.hasReference(ROOT)) if (b === -1) throw new Error('the label-collision block is never closed');
const body = G.value('body', () => src.slice(a, b));
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['body', body]]) {
  if (typeof __v !== 'string' || __v.length <= 4) {
    throw new Error('the recorded ' + __n + ' is empty — the golden is broken');
  }
}

const ctx = { Math };
vm.createContext(ctx);
vm.runInContext(
  'this.keep = function (labelled, scale) {\n' + body + '\n  return labelled;\n};', ctx);

const OUT = path.join(ROOT, 'testdata', '.maplabels-port.cjs');
const ENTRY = path.join(ROOT, 'testdata', '.maplabels-entry.ts');
fs.writeFileSync(ENTRY, "export { keepLabels } from '../web/src/pages/routers-map.js';\n");
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const port = require(OUT);

// ── the corpus ──────────────────────────────────────────────────────────────
//
// Real place labels, because LENGTH is half the rule: a long name occupies a
// wider box and hides more of its neighbours than a short one at the same
// spacing.

const CASES = [
  { why: 'nothing at all', labels: [], scale: 1 },
  { why: 'one label survives', labels: [{ text: 'Berlin', x: 500, y: 200 }], scale: 1 },
  {
    why: 'two far apart both survive',
    labels: [{ text: 'Berlin', x: 100, y: 100 }, { text: 'Tokyo', x: 800, y: 300 }],
    scale: 1,
  },
  {
    why: 'two on the same point: the SECOND is dropped',
    labels: [{ text: 'Berlin', x: 500, y: 200 }, { text: 'Potsdam', x: 500, y: 200 }],
    scale: 1,
  },
  {
    why: 'ORDER decides which survives — the same pair, reversed',
    labels: [{ text: 'Potsdam', x: 500, y: 200 }, { text: 'Berlin', x: 500, y: 200 }],
    scale: 1,
  },
  {
    why: 'a LONG name hides a neighbour a short one would not',
    labels: [{ text: 'Berlin, BE, DE', x: 500, y: 200 }, { text: 'Kiel', x: 530, y: 200 }],
    scale: 1,
  },
  {
    why: 'the same pair with the SHORT name first',
    labels: [{ text: 'Kiel', x: 500, y: 200 }, { text: 'Berlin, BE, DE', x: 530, y: 200 }],
    scale: 1,
  },
  {
    why: 'zoomed IN, the boxes shrink and both return',
    labels: [{ text: 'Berlin, BE, DE', x: 500, y: 200 }, { text: 'Kiel', x: 530, y: 200 }],
    scale: 8,
  },
  {
    why: 'zoomed to a fractional scale',
    labels: [{ text: 'Berlin, BE, DE', x: 500, y: 200 }, { text: 'Kiel', x: 530, y: 200 }],
    scale: 2.5,
  },
  {
    why: 'VERTICAL separation only — the ly offset is what decides',
    labels: [{ text: 'Berlin', x: 500, y: 200 }, { text: 'Leipzig', x: 500, y: 203 }],
    scale: 1,
  },
  {
    why: 'a European cluster at world zoom',
    labels: [
      { text: 'Amsterdam, NH, NL', x: 512, y: 154 },
      { text: 'Brussels, BE', x: 511, y: 158 },
      { text: 'Cologne, NW, DE', x: 519, y: 159 },
      { text: 'Paris, IDF, FR', x: 506, y: 165 },
      { text: 'Frankfurt, HE, DE', x: 523, y: 163 },
      { text: 'Zurich, ZH, CH', x: 523, y: 173 },
    ],
    scale: 1,
  },
  {
    why: 'the same cluster zoomed to 4x',
    labels: [
      { text: 'Amsterdam, NH, NL', x: 512, y: 154 },
      { text: 'Brussels, BE', x: 511, y: 158 },
      { text: 'Cologne, NW, DE', x: 519, y: 159 },
      { text: 'Paris, IDF, FR', x: 506, y: 165 },
      { text: 'Frankfurt, HE, DE', x: 523, y: 163 },
      { text: 'Zurich, ZH, CH', x: 523, y: 173 },
    ],
    scale: 4,
  },
  {
    why: 'an empty string is a zero-width box',
    labels: [{ text: '', x: 500, y: 200 }, { text: 'Berlin', x: 500, y: 200 }],
    scale: 1,
  },
];

let bad = 0;
let totalKept = 0, totalDropped = 0;
for (const c of CASES) {
  // FRESH COPIES: the live block mutates the objects it is given (it writes hw,
  // hh and ly onto them), so a shared array would let one side see the other's
  // scribbles.
  const liveIn = c.labels.map((L) => ({ ...L }));
  const portIn = c.labels.map((L) => ({ ...L }));
  const live = ctx.keep(liveIn, c.scale);
  const got = port.keepLabels(portIn, c.scale);

  totalKept += live.length;
  totalDropped += c.labels.length - live.length;

  const A = JSON.stringify(live.map((L) => [L.text, L.x, L.y, L.hw, L.hh, L.ly]));
  const B = JSON.stringify(got.map((L) => [L.text, L.x, L.y, L.hw, L.hh, L.ly]));
  if (A === B) {
    console.log('  ok  ' + c.why + '  (' + live.length + ' of ' + c.labels.length + ' kept)');
    continue;
  }
  bad++;
  console.log('  DIFF  ' + c.why);
  console.log('        live: ' + A);
  console.log('        port: ' + B);
}

fs.rmSync(OUT, { force: true });

// BELIEVABILITY. A corpus in which nothing is ever dropped would pass against a
// port that kept every label, and one in which nothing is ever kept would pass
// against a port that dropped them all.
if (totalDropped === 0 || totalKept === 0) {
  console.log('\nthe corpus kept ' + totalKept + ' and dropped ' + totalDropped +
              ' — it does not exercise both directions');
  process.exit(1);
}

console.log('\n' + CASES.length + ' cases, ' + totalKept + ' labels kept and ' +
            totalDropped + ' dropped');
if (bad) { console.log(bad + ' FAILED'); process.exit(1); }
console.log('all agree');
