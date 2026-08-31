'use strict';
/**
 * Resizing a Dashboard card, live against ported.
 *
 * ── THE DELTAS ARE FROM THE ORIGIN, SO THE SCRIPTS WANDER ───────────────────
 *
 * Every move recomputes from the size captured at pointer-down plus the TOTAL
 * pointer delta, so out-and-back must return the card exactly to where it
 * started. An incremental implementation passes a single-move corpus and drifts
 * over a long one, so the cases move the pointer around and come back.
 *
 * ── AND A REFUSAL LOOKS LIKE NOTHING HAPPENING ──────────────────────────────
 *
 * Unlike a drag, an invalid size RETURNS rather than snapping — the card simply
 * stops growing. That is indistinguishable from "the handler did not run" unless
 * the case also proves the card WOULD have grown, so the refusal cases drag past
 * an obstacle and then back to a legal size, and check the card recovers.
 *
 * Every direction and both corners are driven, because the direction test is a
 * substring match and a corner exercises two branches at once.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/grid-resize-check.js
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
const LIFT = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/grid-resize-check.js --freeze
const G = LIFT.golden('grid-resize-check');
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

const ENTRY = path.join(ROOT, 'testdata', '.gridresize-entry.ts');
fs.writeFileSync(ENTRY,
  "export { createGridResize } from '../web/src/pages/dashboard-grid-resize.js';\n" +
  "export { createGridEditor } from '../web/src/pages/dashboard-grid-edit.js';\n");
const OUT = path.join(ROOT, 'testdata', '.gridresize-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const GRID = { left: 100, top: 50, width: 1200, height: 800 };
const COLS = 24, ROWS = 22, GAP = 12, PAD = 20;
const colW = (GRID.width - 2 * PAD - (COLS - 1) * GAP) / COLS;
const rowH = (GRID.height - 2 * PAD - (ROWS - 1) * GAP) / ROWS;

function makeWorld() {
  const w = { nodes: new Map(), captures: [] };
  const mk = (tag, id) => {
    const n = {
      tagName: String(tag).toUpperCase(), id: id || '', style: {}, listeners: [],
      classes: new Set(),
      classList: { add: (c) => n.classes.add(c), remove: (c) => n.classes.delete(c), contains: (c) => n.classes.has(c) },
      addEventListener: (t, cb) => n.listeners.push({ t, cb }),
      removeEventListener: (t, cb) => {
        const i = n.listeners.findIndex((l) => l.t === t && l.cb === cb);
        if (i >= 0) n.listeners.splice(i, 1);
      },
      setPointerCapture: (i) => w.captures.push('set:' + i),
      releasePointerCapture: (i) => {
        if (!w.captures.includes('set:' + i)) throw new Error('no capture');
        w.captures.push('release:' + i);
      },
      getBoundingClientRect: () => n._rect || { left: 0, top: 0, width: 0, height: 0, right: 0, bottom: 0 },
      appendChild: (c) => c,
      set innerHTML(v) { n._h = v; }, get innerHTML() { return n._h || ''; },
    };
    if (id) w.nodes.set(id, n);
    return n;
  };
  w.mk = mk;
  const root = mk('div', 'dash-grid-root');
  root._rect = { left: GRID.left, top: GRID.top, width: GRID.width, height: GRID.height, right: GRID.left + GRID.width, bottom: GRID.top + GRID.height };
  mk('div', 'dash-placeholder');
  w.doc = {
    getElementById: (id) => w.nodes.get(id) || null,
    createElement: (t) => mk(t),
    body: { appendChild: (n) => n },
    dispatchEvent: () => true,
  };
  return w;
}
function ensureCards(w, layout) {
  for (const c of layout) if (!w.nodes.has(c.id)) w.mk('div', c.id);
}

function snapshot(w, layout) {
  return JSON.stringify({
    layout: layout.map((c) => ({ id: c.id, x: c.x, y: c.y, w: c.w, h: c.h })),
    captures: w.captures.slice(),
    handleListeners: (w.nodes.get('__handle__') || { listeners: [] }).listeners.map((l) => l.t).sort(),
    styles: [...w.nodes].map(([id, n]) => id + ':' + (n.style.gridColumn || '') + '|' + (n.style.gridRow || '')).sort(),
  });
}

function liveSide(layout) {
  const w = makeWorld();
  ensureCards(w, layout);
  const handle = w.mk('div', '__handle__');
  const ctx = {
    Math, Object, JSON, Array, Error, parseFloat, console: { warn() {}, log() {} },
    document: w.doc, localStorage: { getItem: () => null, setItem() {} },
    fetch: () => Promise.resolve({ ok: true, status: 200 }),
    setTimeout: () => 0, clearTimeout: () => {},
    layout, resizeState: null,
    gridRoot: w.nodes.get('dash-grid-root'),
    placeholder: w.nodes.get('dash-placeholder'),
  };
  vm.createContext(ctx);
  vm.runInContext([
    grab('var COLS = 24', ';', 'constants'), grab('var MIN_W', ';', 'MIN_W'),
    grab('var CARD_ROOMS = {', '};', 'CARD_ROOMS'),
    grab('var DEFAULT_LAYOUT = [', '];', 'DEFAULT_LAYOUT'),
    fn('applyLayout'), fn('getCard'), fn('rectOverlaps'), fn('hasOverlap'),
    fn('inBounds'), fn('getCellSize'),
    fn('startResize'), fn('onResizeMove'), fn('onResizeEnd'),
  ].join('\n'), ctx);
  return { w, handle, api: ctx };
}
function portSide(layout) {
  const w = makeWorld();
  ensureCards(w, layout);
  const handle = w.mk('div', '__handle__');
  globalThis.document = w.doc;
  globalThis.localStorage = { getItem: () => null, setItem() {} };
  globalThis.fetch = () => Promise.resolve({ ok: true, status: 200 });
  delete require.cache[require.resolve(OUT)];
  const m = require(OUT);
  const ed = m.createGridEditor(layout);
  return { w, handle, api: m.createGridResize(ed) };
}

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (a === b) return;
  bad++;
  if (bad <= 4) {
    const A = JSON.parse(a), B = JSON.parse(b);
    shout('DIFF %s', what);
    for (const k of Object.keys(A)) {
      if (JSON.stringify(A[k]) !== JSON.stringify(B[k])) {
        shout('  %s\n    live: %s\n    port: %s', k,
          JSON.stringify(A[k]).slice(0, 260), JSON.stringify(B[k]).slice(0, 260));
      }
    }
  }
}

const ev = (x, y) => ({ clientX: x, clientY: y, pointerId: 9, preventDefault() {}, stopPropagation() {} });
// Pointer deltas expressed in CELLS, which is the unit the code thinks in.
const px = (cols) => cols * (colW + GAP);
const py = (rows) => rows * (rowH + GAP);

function baseLayout() {
  return [
    { id: 'A', x: 5, y: 5, w: 6, h: 4, visible: true },
    { id: 'B', x: 13, y: 5, w: 4, h: 4, visible: true },   // to A's right
    { id: 'C', x: 5, y: 11, w: 6, h: 3, visible: true },   // below A
    { id: 'D', x: 1, y: 5, w: 3, h: 4, visible: true },    // to A's left
    { id: 'E', x: 5, y: 1, w: 6, h: 3, visible: true },    // above A
  ];
}

const START = { x: 500, y: 400 };
const SCRIPTS = {};
for (const dir of ['e', 'w', 'n', 's', 'se', 'sw', 'ne', 'nw']) {
  SCRIPTS['grow ' + dir] = [['start', 'A', dir], ['move', 1, 1]];
  SCRIPTS['shrink ' + dir] = [['start', 'A', dir], ['move', -1, -1]];
  SCRIPTS['out and back ' + dir] = [
    ['start', 'A', dir], ['move', 2, 2], ['move', -2, -2], ['move', 0, 0], ['end'],
  ];
  SCRIPTS['far ' + dir] = [['start', 'A', dir], ['move', 40, 40], ['end']];
  SCRIPTS['far negative ' + dir] = [['start', 'A', dir], ['move', -40, -40], ['end']];
}
Object.assign(SCRIPTS, {
  'no resize in progress: a move': [['move', 1, 1]],
  'no resize in progress: an end': [['end']],
  'two ends in a row': [['start', 'A', 'e'], ['end'], ['end']],
  'a card that does not exist': [['start', 'ZZZ', 'e'], ['move', 1, 1]],
  'an unknown direction changes nothing': [['start', 'A', 'x'], ['move', 3, 3], ['end']],
  'the empty direction changes nothing': [['start', 'A', ''], ['move', 3, 3], ['end']],
  // REFUSED, then recovered: growing east hits B, and coming back must resume.
  'grow east into B, then back': [
    ['start', 'A', 'e'], ['move', 1, 0], ['move', 3, 0], ['move', 1, 0], ['end'],
  ],
  'grow south into C, then back': [
    ['start', 'A', 's'], ['move', 0, 1], ['move', 0, 5], ['move', 0, 1], ['end'],
  ],
  'grow west into D': [['start', 'A', 'w'], ['move', -1, 0], ['move', -3, 0], ['end']],
  'grow north into E': [['start', 'A', 'n'], ['move', 0, -1], ['move', 0, -3], ['end']],
  'half a cell rounds up': [['start', 'A', 'e'], ['moveRaw', px(0.6), 0], ['end']],
  'just under half a cell rounds down': [['start', 'A', 'e'], ['moveRaw', px(0.4), 0], ['end']],
  'exactly half a cell': [['start', 'A', 'e'], ['moveRaw', px(0.5), 0], ['end']],
  'a long wander returns home': [
    ['start', 'A', 'se'], ['move', 1, 1], ['move', 3, 2], ['move', -2, 4],
    ['move', 5, -3], ['move', 0, 0], ['end'],
  ],
});

function step(api, w, handle, layout, s) {
  const [op, a, b] = s;
  if (op === 'start') return api.startResize(a, b, handle, ev(START.x, START.y));
  if (op === 'move') return api.onResizeMove(ev(START.x + px(a), START.y + py(b)));
  if (op === 'moveRaw') return api.onResizeMove(ev(START.x + a, START.y + b));
  if (op === 'end') return api.onResizeEnd(ev(0, 0));
  throw new Error('unknown step ' + op);
}

for (const [name, script] of Object.entries(SCRIPTS)) {
  // THE LIVE RUN, FROZEN AS ONE ORDERED SEQUENCE. `step` drives the live world
  // outside the comparison, so freezing the read alone would leave the driver
  // running on replay. Recipe 3i.
  const live = G.value(name + ' live run', () => {
    const ll = baseLayout(), L = liveSide(ll);
    return script.map((s) => { step(L.api, L.w, L.handle, ll, s); return snapshot(L.w, ll); });
  });
  const pl = baseLayout();
  const P = portSide(pl);
  script.forEach((s, i) => {
    step(P.api, P.w, P.handle, pl, s);
    cmp(name + ' after step ' + (i + 1) + ' (' + s[0] + ')', live[i], snapshot(P.w, pl));
  });
}

// ── AN ISOLATED CARD, WITH ROOM IN EVERY DIRECTION ─────────────────────────
//
// The base layout deliberately boxes A in on all four sides, which is right for
// testing refusals and WRONG for testing the clamps: an obstacle refuses the
// resize before the clamp decides anything, so both implementations agree by
// doing nothing. Four mutations to the clamps SURVIVED on that layout alone.
// Here nothing is in the way, so the only thing that can stop the card growing
// is the grid edge — which is exactly what the clamps are for.
function loneLayout() {
  return [{ id: 'A', x: 5, y: 5, w: 6, h: 4, visible: true }];
}
// A card whose stored geometry ALREADY leaves the grid. After the clamps,
// `inBounds` is redundant for every card that starts legal — it is this that
// makes it matter, and `mergeLayout` takes a stored entry verbatim.
function strandedLayout() {
  return [{ id: 'A', x: 20, y: 20, w: 10, h: 8, visible: true }];
}
{
  const dirs = ['e', 'w', 'n', 's', 'se', 'sw', 'ne', 'nw'];
  const lonely = {};
  for (const dir of dirs) {
    lonely['lone: far ' + dir] = [['start', 'A', dir], ['move', 40, 40], ['end']];
    lonely['lone: far negative ' + dir] = [['start', 'A', dir], ['move', -40, -40], ['end']];
    lonely['lone: to the edge ' + dir] = [
      ['start', 'A', dir], ['move', 19, 17], ['move', -19, -17], ['end'],
    ];
  }
  lonely['lone: shrink to the minimum from the east'] = [['start', 'A', 'e'], ['move', -20, 0], ['end']];
  lonely['lone: shrink to the minimum from the west'] = [['start', 'A', 'w'], ['move', 20, 0], ['end']];
  lonely['lone: shrink to the minimum from the north'] = [['start', 'A', 'n'], ['move', 0, 20], ['end']];
  lonely['lone: shrink to the minimum from the south'] = [['start', 'A', 's'], ['move', 0, -20], ['end']];
  for (const [name, script] of Object.entries(lonely)) {
    const live = G.value(name + ' live run', () => {
      const ll = loneLayout(), L = liveSide(ll);
      return script.map((st) => { step(L.api, L.w, L.handle, ll, st); return snapshot(L.w, ll); });
    });
    const pl = loneLayout();
    const P = portSide(pl);
    script.forEach((st, i) => {
      step(P.api, P.w, P.handle, pl, st);
      cmp(name + ' step ' + (i + 1), live[i], snapshot(P.w, pl));
    });
  }
  for (const dir of dirs) {
    for (const [dc, dr] of [[1, 1], [-1, -1], [3, 3]]) {
      const script = [['start', 'A', dir], ['move', dc, dr], ['end']];
      const key = 'stranded ' + dir + ' (' + dc + ',' + dr + ')';
      const live = G.value(key + ' live run', () => {
        const ll = strandedLayout(), L = liveSide(ll);
        return script.map((st) => { step(L.api, L.w, L.handle, ll, st); return snapshot(L.w, ll); });
      });
      const pl = strandedLayout();
      const P = portSide(pl);
      script.forEach((st, i) => {
        step(P.api, P.w, P.handle, pl, st);
        cmp(key + ' step ' + (i + 1), live[i], snapshot(P.w, pl));
      });
    }
  }
  // The clamps really do bite here: growing east without limit must stop at the
  // grid edge rather than being refused outright.
  const ll = loneLayout();
  const L = portSide(ll);
  L.api.startResize('A', 'e', L.handle, ev(START.x, START.y));
  L.api.onResizeMove(ev(START.x + px(40), START.y));
  assert.equal(ll[0].x + ll[0].w - 1, COLS,
    'growing east without obstruction did not reach the grid edge (w=' + ll[0].w + ') — ' +
    'the clamp is not being exercised and its mutations cannot be caught');
}

// ── believability ──────────────────────────────────────────────────────────
{
  // A resize really does change the card, and out-and-back really does restore.
  const ll = baseLayout();
  const L = portSide(ll);
  L.api.startResize('A', 'e', L.handle, ev(START.x, START.y));
  L.api.onResizeMove(ev(START.x + px(2), START.y));
  assert.equal(ll[0].w, 8, 'the resize did not grow the card (w=' + ll[0].w + ')');
  L.api.onResizeMove(ev(START.x, START.y));
  assert.equal(ll[0].w, 6, 'out-and-back did not restore the width');
  L.api.onResizeEnd(ev(0, 0));
  assert.ok(L.w.captures.includes('release:9'), 'the pointer capture was not released');
}
{
  // The refusal is real: growing east into B must be REFUSED, and the card must
  // still be at the last size it legally reached.
  const ll = baseLayout();
  const L = portSide(ll);
  L.api.startResize('A', 'e', L.handle, ev(START.x, START.y));
  L.api.onResizeMove(ev(START.x + px(1), START.y));
  assert.equal(ll[0].w, 7, 'precondition: one cell of growth should be legal');
  L.api.onResizeMove(ev(START.x + px(3), START.y));
  assert.equal(ll[0].w, 7, 'growing into B was NOT refused — the cards now overlap');
}
{
  // West moves the origin and keeps the right edge fixed.
  const ll = baseLayout();
  const L = portSide(ll);
  L.api.startResize('A', 'w', L.handle, ev(START.x, START.y));
  L.api.onResizeMove(ev(START.x - px(1), START.y));
  assert.equal(ll[0].x, 4, 'west did not move the origin');
  assert.equal(ll[0].x + ll[0].w, 11, 'west moved the RIGHT edge, which must stay put');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d comparisons differ', bad, checked); process.exit(1); }
say('grid-resize-check: %d comparisons identical', checked);
