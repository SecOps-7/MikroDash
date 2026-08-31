'use strict';
/**
 * The TOPOLOGY page's layout arithmetic, live against ported.
 *
 * ── THE LIVE CODE IS NOT IN app.js ──────────────────────────────────────────
 *
 * It is `public/js/topology.js`, a separate 1,180-line file. Every earlier
 * search for `computeLayout` in app.js found nothing, which reads as "the
 * function does not exist" rather than "you are looking in the wrong file".
 * `lift.liveSource()` now takes a path for exactly that.
 *
 * ── WHAT IS GATED, AND WHAT CANNOT BE ───────────────────────────────────────
 *
 * The page BUILDS AN SVG GRAPH — `createElementNS`, `animateMotion`, drag
 * handlers. None of that is comparable with a string shim, and pretending
 * otherwise would be the sophisticated-harness trap the Interfaces page already
 * demonstrated.
 *
 * What IS comparable is the arithmetic that decides the picture: where each node
 * goes, the curve between two of them, how thick a link is drawn, and how a rate
 * is abbreviated. That is the same division `routers-grid-check.js` draws — the
 * SVG to `live-renderer.js` against a running stack, the arithmetic here.
 *
 * ── ONE SIGNATURE DIFFERENCE, WHICH IS NOT A BEHAVIOUR DIFFERENCE ──────────
 *
 * Live's `computeLayout(nodes)` reads a module-level `_saved`; the port takes it
 * as a second parameter. Same values, passed differently — so the adapter sets
 * `_saved` on the live side and passes it on the port's. Comparing the two
 * without that would compare a layout that remembered dragged positions against
 * one that did not.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/topology-layout-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/topology-layout-check.js --freeze
const G = L.golden('topology-layout-check');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT, 'public/js/topology.js');

const FNS = ['computeLayout', 'edgePath', 'loadWidth', 'fmtShort', 'glyph'];
if (process.argv.includes('--ids')) { console.log(JSON.stringify([])); process.exit(0); }

const ENTRY = path.join(ROOT, 'testdata', '.tp-entry.ts');
fs.writeFileSync(ENTRY,
  "export { computeLayout, edgePath, loadWidth, fmtShort, glyph } from '../web/src/pages/topology.js';\n");
const OUT = path.join(ROOT, 'testdata', '.tp-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const ctx = {
  String, Array, Math, Number, Object, JSON, parseInt, parseFloat, isFinite,
  document: { createElementNS: () => ({ setAttribute() {} }) },
  window: {}, localStorage: { getItem: () => null, setItem() {} },
  __out: null, _saved: {},
};
vm.createContext(ctx);
// FROZEN AS ONE SCRIPT — the pieces are lifted inside an array literal, so an
// unfrozen lift leaves them empty and the VM reports `computeLayout is not
// defined` rather than anything about the layout.
vm.runInContext(G.value('liveScript', () => [
  L.whole(src, 'var GLYPHS = {'),
  L.whole(src, 'var TYPE_LABEL = {'),
  ...FNS.map((f) => L.whole(src, 'function ' + f + '(')),
  '__out = { ' + FNS.map((f) => f + ': ' + f).join(', ') + ' };',
].join('\n')), ctx);
const LIVE = ctx.__out;
assert.ok(LIVE.computeLayout, 'the lift did not publish computeLayout');

const PORT = require(OUT);

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  const x = JSON.stringify(a), y = JSON.stringify(b);
  if (x === y) return;
  bad++;
  if (bad <= 5) shout('DIFF %s\n  live: %s\n  port: %s', what,
    String(x).slice(0, 300), String(y).slice(0, 300));
}

const N = (o) => Object.assign({ key: 'n1', kind: 'device', name: 'sw1', parent: 'core',
  ifaces: ['ether1'], type: 'switch' }, o);

// ── computeLayout ───────────────────────────────────────────────────────────
const LAYOUTS = {
  'no nodes at all': [[], {}],
  'only the core': [[N({ key: 'core', kind: 'core' })], {}],
  'one child': [[N({})], {}],
  'two children of the core': [[N({}), N({ key: 'n2', name: 'sw2' })], {}],
  'a child behind a child': [[N({}), N({ key: 'n2', name: 'ap1', parent: 'n1' })], {}],
  'three levels deep': [[N({}), N({ key: 'n2', parent: 'n1' }), N({ key: 'n3', parent: 'n2' })], {}],
  'many siblings': [Array.from({ length: 8 }, (_, i) => N({ key: 'n' + i, name: 's' + i })), {}],
  // Ordering: by first interface, then by name. Both rungs matter.
  // THE INPUT ORDER IS THE REVERSE OF THE SORTED ORDER. With the inputs already
  // in interface order, a comparator returning 0 leaves them alone and looks
  // correct — which is how a mutation ignoring the interface survived.
  'siblings sorted by interface': [[
    N({ key: 'a', name: 'amy', ifaces: ['ether2'] }),
    N({ key: 'b', name: 'zed', ifaces: ['ether1'] })], {}],
  'siblings already in interface order': [[
    N({ key: 'a', name: 'zed', ifaces: ['ether1'] }),
    N({ key: 'b', name: 'amy', ifaces: ['ether2'] })], {}],
  'siblings on the SAME interface fall back to name': [[
    N({ key: 'a', name: 'zed', ifaces: ['ether1'] }),
    N({ key: 'b', name: 'amy', ifaces: ['ether1'] })], {}],
  'same interface, already in name order': [[
    N({ key: 'a', name: 'amy', ifaces: ['ether1'] }),
    N({ key: 'b', name: 'zed', ifaces: ['ether1'] })], {}],
  'a node with no interfaces': [[N({ ifaces: [] }), N({ key: 'n2', ifaces: ['ether1'] })], {}],
  'a node with no parent defaults to core': [[N({ parent: '' })], {}],
  // SAVED positions: the core can be dragged, and everything is relative to it.
  'a saved core position moves everything': [[N({})], { core: { x: 200, y: -50 } }],
  'a saved position for a child': [[N({})], { n1: { x: 10, y: 20 } }],
  'saved positions for both': [[N({})], { core: { x: 5, y: 5 }, n1: { x: 10, y: 20 } }],
  'a saved position for a node that is gone': [[N({})], { ghost: { x: 1, y: 1 } }],
};
for (const [name, [nodes, saved]] of Object.entries(LAYOUTS)) {
  ctx._saved = saved;
  vm.runInContext('_saved = ' + JSON.stringify(saved) + ';', ctx);
  let a, b;
  try { a = LIVE.computeLayout(nodes); } catch (e) { a = 'THREW:' + e.message; }
  try { b = PORT.computeLayout(nodes, saved); } catch (e) { b = 'THREW:' + e.message; }
  cmp('layout: ' + name, a, b);
}

// ── edgePath ────────────────────────────────────────────────────────────────
const EDGES = [
  [0, 0, 100, 0], [0, 0, 0, 100], [0, 0, 100, 100], [100, 100, 0, 0],
  [-50, -50, 50, 50], [0, 0, 0, 0], [10, 20, 10, 200], [0, 0, 1, 1],
  [1e4, 1e4, -1e4, -1e4], [0.5, 0.5, 1.5, 1.5],
];
for (const [x0, y0, x1, y1] of EDGES) {
  cmp('edge: ' + [x0, y0, x1, y1].join(','),
    LIVE.edgePath(x0, y0, x1, y1), PORT.edgePath(x0, y0, x1, y1));
}

// ── loadWidth and fmtShort ──────────────────────────────────────────────────
const RATES = [0, 0.001, 0.5, 1, 9.99, 10, 99, 100, 500, 999, 1000, 1001, 9999,
  1e5, 1e6, -1, NaN, Infinity];
for (const v of RATES) {
  cmp('loadWidth: ' + v, LIVE.loadWidth(v), PORT.loadWidth(v));
  cmp('fmtShort: ' + v, LIVE.fmtShort(v), PORT.fmtShort(v));
}

// ── glyph ───────────────────────────────────────────────────────────────────
for (const t of ['switch', 'router', 'ap', 'client', 'core', 'unknown', '', 'nonsense', 'SWITCH']) {
  cmp('glyph: ' + JSON.stringify(t), LIVE.glyph(t), PORT.glyph(t));
}

// ── believability ──────────────────────────────────────────────────────────
{
  const out = LIVE.computeLayout([N({}), N({ key: 'n2', name: 'sw2' })]);
  assert.ok(out.core, 'the layout has no core');
  assert.ok(out.n1 && out.n2, 'the layout placed no children');
  assert.notDeepEqual(out.n1, out.n2, 'two children were placed on top of each other');
}
{
  // The core is NOT pinned to the origin: a saved core position moves the whole
  // picture, which the live comment records as a real bug once — a hardcoded
  // origin left the map fanned around the core's ORIGINAL spot after a drag.
  vm.runInContext('_saved = {};', ctx);
  const at0 = LIVE.computeLayout([N({})]);
  vm.runInContext('_saved = ' + JSON.stringify({ core: { x: 200, y: -50 } }) + ';', ctx);
  const moved = LIVE.computeLayout([N({})]);
  assert.notDeepEqual(at0.n1, moved.n1,
    'moving the core did not move its children — the layout is pinned to the origin');
  assert.equal(moved.core.x, 200, 'the core did not take its saved position');
}
{
  // loadWidth really varies with the rate, and fmtShort abbreviates.
  assert.notEqual(LIVE.loadWidth(1), LIVE.loadWidth(1000), 'loadWidth is constant');
  assert.notEqual(LIVE.fmtShort(1), LIVE.fmtShort(1000), 'fmtShort is constant');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d comparisons differ', bad, checked); process.exit(1); }
say('topology-layout-check: %d comparisons identical', checked);
