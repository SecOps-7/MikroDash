'use strict';
/**
 * Dragging a Dashboard card, live against ported.
 *
 * ── THE SNAP IS THE PROPERTY UNDER TEST ─────────────────────────────────────
 *
 * `snapX`/`snapY` advance only when the cell under the pointer is in bounds and
 * free, so dragging ACROSS an occupied region and letting go there must land the
 * card at the last legal cell — not under the cursor, and not nowhere. That is
 * only observable across a SEQUENCE of moves, so every case is a pointer script
 * and the whole world is compared after each step.
 *
 * ── AND THE SWAP TIMER IS DRIVEN, NOT WAITED ON ─────────────────────────────
 *
 * The 1.5s dwell is a `setTimeout`. Both sides get a fake clock whose timers
 * fire only when the script says so, which is what makes the interesting case
 * cheap to write: move to a THIRD card before the timer fires, and check the
 * swap that lands is the one whose highlight the user was watching — not
 * whichever card is hovered at fire time.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/grid-drag-check.js
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
// in lib/lift.js. Re-freeze with: node tools/grid-drag-check.js --freeze
const G = LIFT.golden('grid-drag-check');
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

const ENTRY = path.join(ROOT, 'testdata', '.griddrag-entry.ts');
fs.writeFileSync(ENTRY,
  "export { createGridDrag } from '../web/src/pages/dashboard-grid-drag.js';\n" +
  "export { createGridEditor } from '../web/src/pages/dashboard-grid-edit.js';\n" +
  "export { DEFAULT_LAYOUT, CARD_LABELS } from '../web/src/gen/grid-tables.js';\n");
const OUT = path.join(ROOT, 'testdata', '.griddrag-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

// The grid's pixel geometry, fixed so both sides see identical rects.
const GRID = { left: 100, top: 50, width: 1200, height: 800 };
const COLS = 24, ROWS = 22, GAP = 12, PAD = 20;
const colW = (GRID.width - 2 * PAD - (COLS - 1) * GAP) / COLS;
const rowH = (GRID.height - 2 * PAD - (ROWS - 1) * GAP) / ROWS;

function makeWorld(layout) {
  const w = { nodes: new Map(), body: [], timers: [], nextTimer: 1, captures: [], storage: {} };
  const mk = (tag, id) => {
    const n = {
      tagName: String(tag).toUpperCase(), id: id || '', className: '', textContent: '',
      style: {}, classes: new Set(), listeners: [], removed: false,
      classList: {
        add: (c) => n.classes.add(c), remove: (c) => n.classes.delete(c),
        contains: (c) => n.classes.has(c),
      },
      addEventListener: (t, cb) => n.listeners.push({ t, cb }),
      removeEventListener: (t, cb) => {
        const i = n.listeners.findIndex((l) => l.t === t && l.cb === cb);
        if (i >= 0) n.listeners.splice(i, 1);
      },
      setPointerCapture: (id2) => w.captures.push('set:' + id2),
      releasePointerCapture: (id2) => {
        if (!w.captures.includes('set:' + id2)) throw new Error('no capture');
        w.captures.push('release:' + id2);
      },
      remove() { this.removed = true; const i = w.body.indexOf(this); if (i >= 0) w.body.splice(i, 1); },
      getBoundingClientRect() { return this._rect || { left: 0, top: 0, right: 0, bottom: 0, width: 0, height: 0 }; },
      appendChild(c) { this.children = this.children || []; this.children.push(c); return c; },
    };
    if (id) w.nodes.set(id, n);
    return n;
  };
  w.mk = mk;
  const root = mk('div', 'dash-grid-root');
  root._rect = { left: GRID.left, top: GRID.top, width: GRID.width, height: GRID.height, right: GRID.left + GRID.width, bottom: GRID.top + GRID.height };
  mk('div', 'dash-placeholder');
  // One node per card, positioned exactly where its cells are, so the hover
  // test has real rectangles to hit.
  for (const c of layout) {
    const n = mk('div', c.id);
    const left = GRID.left + PAD + (c.x - 1) * (colW + GAP);
    const top = GRID.top + PAD + (c.y - 1) * (rowH + GAP);
    const width = c.w * colW + (c.w - 1) * GAP;
    const height = c.h * rowH + (c.h - 1) * GAP;
    n._rect = { left, top, width, height, right: left + width, bottom: top + height };
  }
  w.doc = {
    getElementById: (id) => w.nodes.get(id) || null,
    createElement: (tag) => mk(tag),
    body: { appendChild: (n) => { w.body.push(n); return n; } },
    dispatchEvent: () => true,
  };
  w.setTimeout = (cb, ms) => { const id = w.nextTimer++; w.timers.push({ id, cb, ms }); return id; };
  w.clearTimeout = (id) => { const i = w.timers.findIndex((t) => t.id === id); if (i >= 0) w.timers.splice(i, 1); };
  w.fireTimers = () => { for (const t of w.timers.splice(0)) t.cb(); };
  return w;
}

function snapshot(w, layout) {
  const ghost = w.body[0];
  const ph = w.nodes.get('dash-placeholder');
  return JSON.stringify({
    layout: layout.map((c) => ({ id: c.id, x: c.x, y: c.y, w: c.w, h: c.h, v: c.visible })),
    ghost: ghost ? { id: ghost.id, text: ghost.textContent, style: ghost.style, removed: ghost.removed } : null,
    bodyCount: w.body.length,
    placeholder: ph ? { style: ph.style } : null,
    pending: [...w.nodes].filter(([, n]) => n.classes.has('dash-swap-pending')).map(([id]) => id).sort(),
    opacity: [...w.nodes].filter(([, n]) => n.style.opacity !== undefined && n.style.opacity !== '')
      .map(([id, n]) => id + '=' + n.style.opacity).sort(),
    captures: w.captures.slice(),
    timers: w.timers.map((t) => t.ms),
    handleListeners: (w.nodes.get('__handle__') || { listeners: [] }).listeners.map((l) => l.t).sort(),
  });
}

function liveSide(layout) {
  const w = makeWorld(layout);
  const handle = w.mk('div', '__handle__');
  const ctx = {
    Math, Object, JSON, Array, Error, parseFloat, console: { warn() {}, log() {} },
    document: w.doc, localStorage: { getItem: () => null, setItem() {} },
    CustomEvent: function (t, i) { return { type: t, detail: i && i.detail }; },
    fetch: () => Promise.resolve({ ok: true, status: 200 }),
    setTimeout: w.setTimeout, clearTimeout: w.clearTimeout,
    layout, dragState: null,
    gridRoot: w.nodes.get('dash-grid-root'),
    placeholder: w.nodes.get('dash-placeholder'),
  };
  vm.createContext(ctx);
  vm.runInContext([
    grab('var COLS = 24', ';', 'constants'), grab('var MIN_W', ';', 'MIN_W'),
    grab('var CARD_LABELS = {', '};', 'CARD_LABELS'),
    grab('var CARD_ROOMS = {', '};', 'CARD_ROOMS'),
    grab('var DEFAULT_LAYOUT = [', '];', 'DEFAULT_LAYOUT'),
    grab("var LS_KEY = '", ';', 'LS_KEY'),
    fn('cloneLayout'), fn('applyLayout'), fn('getCard'), fn('rectOverlaps'),
    fn('hasOverlap'), fn('inBounds'), fn('getCellSize'), fn('cellToPixel'), fn('ptrToCell'),
    fn('startDrag'), fn('onDragMove'), fn('clearSwapPending'), fn('doSwap'),
    fn('endDrag'), fn('onDragEnd'), fn('updatePlaceholder'),
  ].join('\n'), ctx);
  return { w, handle, api: ctx };
}
function portSide(layout) {
  const w = makeWorld(layout);
  const handle = w.mk('div', '__handle__');
  globalThis.document = w.doc;
  globalThis.setTimeout = w.setTimeout;
  globalThis.clearTimeout = w.clearTimeout;
  globalThis.localStorage = { getItem: () => null, setItem() {} };
  globalThis.fetch = () => Promise.resolve({ ok: true, status: 200 });
  delete require.cache[require.resolve(OUT)];
  const m = require(OUT);
  const ed = m.createGridEditor(layout);
  return { w, handle, api: m.createGridDrag(ed), m };
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

// Pixel centre of a cell, for aiming the pointer.
const cellPx = (col, row) => ({
  x: GRID.left + PAD + (col - 1) * (colW + GAP) + colW / 2,
  y: GRID.top + PAD + (row - 1) * (rowH + GAP) + rowH / 2,
});
const ev = (x, y) => ({ clientX: x, clientY: y, pointerId: 7, preventDefault() {} });

/**
 * Where the pointer must be for the GHOST'S CENTRE to land on a card.
 *
 * The hover test uses the ghost centre, not the pointer, and the two are far
 * apart: the ghost keeps the grab offset, so for a 6-cell-wide card grabbed at
 * its first cell the centre trails about 2.5 cells to the RIGHT of the cursor.
 * Aiming the POINTER at a target therefore misses it entirely — which is how an
 * earlier version of this file ran every swap case without ever arming the
 * dwell timer, and passed, because both sides did nothing in exactly the same
 * way. The believability assertion at the foot is what caught it.
 */
function aimGhostAt(dragged, target) {
  const ghostW = dragged.w * colW + (dragged.w - 1) * GAP;
  const ghostH = dragged.h * rowH + (dragged.h - 1) * GAP;
  const tLeft = GRID.left + PAD + (target.x - 1) * (colW + GAP);
  const tTop = GRID.top + PAD + (target.y - 1) * (rowH + GAP);
  const tCx = tLeft + (target.w * colW + (target.w - 1) * GAP) / 2;
  const tCy = tTop + (target.h * rowH + (target.h - 1) * GAP) / 2;
  // The grab offset, given every script starts at the centre of the card's
  // FIRST cell.
  const ptrOffX = colW / 2, ptrOffY = rowH / 2;
  return { x: tCx - ghostW / 2 + ptrOffX, y: tCy - ghostH / 2 + ptrOffY };
}

function baseLayout() {
  return [
    { id: 'card-traffic', x: 1, y: 1, w: 6, h: 4, visible: true },
    // DELIBERATELY a different size from card-traffic: the swap exchanges
    // position AND size, and a mutation dropping the size half is invisible when
    // both cards are the same shape. Every swap case pairs these two.
    { id: 'card-system', x: 9, y: 1, w: 4, h: 3, visible: true },
    { id: 'card-network', x: 17, y: 1, w: 6, h: 4, visible: true },
    { id: 'card-toptalkers', x: 1, y: 7, w: 6, h: 4, visible: true },
    { id: 'dc-card-bgp', x: 9, y: 7, w: 4, h: 3, visible: false },
    // An id CARD_LABELS does not know, so the ghost's `|| ''` fallback is
    // reachable. Without it that fallback is dead corpus and a port rendering
    // the raw id instead of an empty ghost passes.
    { id: 'card-unlabelled', x: 17, y: 7, w: 3, h: 2, visible: true },
  ];
}

const SCRIPTS = {
  'start a drag': [['start', 'card-traffic', 1, 1]],
  'start on a hidden card does nothing': [['start', 'dc-card-bgp', 9, 7]],
  'start on a card with no element': [['start', 'card-missing', 1, 1]],
  'drag to a free cell and drop': [
    ['start', 'card-traffic', 1, 1], ['move', 1, 13], ['end'],
  ],
  'drag OVER an occupied region and drop there': [
    // The pointer ends over card-system, which is occupied: the drop must land
    // at the last legal cell, not under the cursor and not nowhere.
    ['start', 'card-traffic', 1, 1], ['move', 7, 1], ['move', 9, 1], ['end'],
  ],
  'drag off the right edge is clamped': [
    ['start', 'card-traffic', 1, 1], ['move', 24, 1], ['end'],
  ],
  'drag off the top-left is clamped': [
    ['start', 'card-system', 9, 1], ['moveRaw', -5000, -5000], ['end'],
  ],
  'a move with no drag in progress': [['move', 5, 5]],
  'an end with no drag in progress': [['end']],
  'two ends in a row': [['start', 'card-traffic', 1, 1], ['end'], ['end']],
  'hover a card without dwelling': [
    ['start', 'card-traffic', 1, 1], ['hover', 'card-traffic', 'card-system'], ['end'],
  ],
  'hover and DWELL — the two swap': [
    ['start', 'card-traffic', 1, 1], ['hover', 'card-traffic', 'card-system'], ['fire'], ['end'],
  ],
  'hover, move to a THIRD card, then fire': [
    // The captured id is what makes this correct: the timer for card-system was
    // cleared when the ghost left it, so firing must swap nothing.
    ['start', 'card-traffic', 1, 1], ['hover', 'card-traffic', 'card-system'],
    ['hover', 'card-traffic', 'card-network'], ['fire'], ['end'],
  ],
  'hover, leave to empty space, then fire': [
    ['start', 'card-traffic', 1, 1], ['hover', 'card-traffic', 'card-system'],
    ['move', 1, 13], ['fire'], ['end'],
  ],
  'hover the same card twice does not restart the timer': [
    ['start', 'card-traffic', 1, 1], ['hover', 'card-traffic', 'card-system'],
    ['hover', 'card-traffic', 'card-system'], ['fire'], ['end'],
  ],
  'a swap ends the drag — a later end is a no-op': [
    ['start', 'card-traffic', 1, 1], ['hover', 'card-traffic', 'card-system'],
    ['fire'], ['end'], ['end'],
  ],
  'dragging a card over a HIDDEN one is not a swap': [
    ['start', 'card-traffic', 1, 1], ['hover', 'card-traffic', 'dc-card-bgp'], ['fire'], ['end'],
  ],
  // The right edge with NOTHING in the way. The earlier version of this case
  // dragged along row 1, where card-network occupies the right — so the clamped
  // position OVERLAPPED and the snap was rejected on both sides, and a mutation
  // dropping the card's own width from the clamp SURVIVED. Row 13 is empty.
  'drag to the right edge on an EMPTY row': [
    ['start', 'card-traffic', 1, 1], ['move', 24, 13], ['end'],
  ],
  'drag to the bottom edge on an empty column': [
    ['start', 'card-traffic', 1, 1], ['move', 1, 22], ['end'],
  ],
  'drag an UNLABELLED card — the ghost is empty': [
    ['start', 'card-unlabelled', 17, 7], ['move', 5, 13], ['end'],
  ],
  'a full drag across the grid': [
    ['start', 'card-toptalkers', 1, 7], ['move', 3, 7], ['move', 5, 9],
    ['move', 9, 13], ['move', 13, 15], ['end'],
  ],
};

function step(api, w, handle, layout, s) {
  const [op, a, b, c] = s;
  if (op === 'start') {
    const p = cellPx(b, c);
    return api.startDrag(a, handle, ev(p.x, p.y));
  }
  if (op === 'move') { const p = cellPx(a, b); return api.onDragMove(ev(p.x, p.y)); }
  if (op === 'moveRaw') return api.onDragMove(ev(a, b));
  if (op === 'hover') {
    // `a` is the dragged card's id, `b` the target's.
    const d = layout.find((x) => x.id === a), t = layout.find((x) => x.id === b);
    const p = aimGhostAt(d, t);
    return api.onDragMove(ev(p.x, p.y));
  }
  if (op === 'end') return api.onDragEnd(ev(0, 0));
  if (op === 'fire') return w.fireTimers();
  throw new Error('unknown step ' + op);
}

for (const [name, script] of Object.entries(SCRIPTS)) {
  const pl = baseLayout();
  const P = portSide(pl);
  // ONE FROZEN VALUE PER SCRIPT, not per comparison: the live side is STATEFUL
  // across steps, so the run has to be replayed as a unit and its snapshots
  // recorded in order. The port loop then indexes into that sequence.
  const liveSnaps = G.value('snaps:' + name, () => {
    const ll = baseLayout();
    const L = liveSide(ll);
    return script.map((s) => {
      step(L.api, L.w, L.handle, ll, s);
      return snapshot(L.w, ll);
    });
  });
  script.forEach((s, i) => {
    step(P.api, P.w, P.handle, pl, s);
    cmp(name + ' after step ' + (i + 1) + ' (' + s[0] + ')',
      liveSnaps[i], snapshot(P.w, pl));
  });
}

// ── a card WIDER than the grid ─────────────────────────────────────────────
//
// After the clamp, `inBounds` is redundant for every card that fits — which is
// why a mutation removing it survives on the ordinary corpus. It stops being
// redundant when the card cannot fit at all: `Math.max(1, ...)` then yields a
// column whose far edge is off the grid, and only inBounds refuses it. Reachable
// the same way a zero-sized card is: `mergeLayout` takes a stored entry verbatim.
// NOT GUARDED, deliberately: the live half below is FROZEN, so this block
// compares the port against a recording and answers perfectly well without a
// reference. It carried a `hasReference` guard copied from its neighbours and
// quietly checked 9 fewer cases without one — caught by the gate census, which
// is the only thing that could have caught it.
{
  for (const [name, over] of Object.entries({
    'wider than the grid': { w: COLS + 2, h: 2 },
    'taller than the grid': { w: 2, h: ROWS + 2 },
    'exactly the whole grid': { w: COLS, h: ROWS },
  })) {
    const mkLay = () => baseLayout().map((c) =>
      c.id === 'card-traffic' ? { ...c, ...over } : { ...c, visible: c.id === 'card-traffic' });
    const pl = mkLay();
    const P = portSide(pl);
    const script = [['start', 'card-traffic', 1, 1], ['move', 12, 11], ['end']];
    // Same shape as the main loop: the live run is stateful, so its snapshots
    // are frozen as one ordered sequence and the port loop indexes into it.
    const liveSnaps = G.value('oversized:' + name, () => {
      const ll = mkLay();
      const L = liveSide(ll);
      return script.map((st) => {
        step(L.api, L.w, L.handle, ll, st);
        return snapshot(L.w, ll);
      });
    });
    script.forEach((st, i) => {
      step(P.api, P.w, P.handle, pl, st);
      cmp('oversized (' + name + ') step ' + (i + 1), liveSnaps[i], snapshot(P.w, pl));
    });
  }
}

// ── believability ──────────────────────────────────────────────────────────
// RE-AIMED AT THE PORT, not guarded. It asks whether a drag really builds a
// ghost and shows the placeholder — a property the PORT has to keep, which was
// being asked of the live side only because the live side was in scope.
{
  const ll = baseLayout();
  const L = portSide(ll);
  const p0 = cellPx(1, 1);
  L.api.startDrag('card-traffic', L.handle, ev(p0.x, p0.y));
  assert.equal(L.w.body.length, 1, 'the drag created no ghost');
  assert.equal(L.w.nodes.get('dash-placeholder').style.display, 'block', 'the placeholder never showed');
  const p1 = cellPx(1, 13);
  L.api.onDragMove(ev(p1.x, p1.y));
  assert.notEqual(ll[0].y, 13, 'the layout moved during a drag — it must only move on drop');
  L.api.onDragEnd(ev(0, 0));
  assert.equal(ll[0].y, 13, 'the drop did not move the card (snap=' + JSON.stringify(ll[0]) + ')');
  assert.equal(L.w.body.length, 0, 'the ghost was not removed');
  assert.ok(L.w.captures.includes('release:7'), 'the pointer capture was not released');
}
// RE-AIMED AT THE PORT, for the same reason as the block above.
{ // the swap really does exchange SIZE as well as position
  const ll = baseLayout();
  const L = portSide(ll);
  const p0 = cellPx(1, 1);
  L.api.startDrag('card-traffic', L.handle, ev(p0.x, p0.y));
  const p1 = aimGhostAt(ll[0], ll[1]);
  L.api.onDragMove(ev(p1.x, p1.y));
  assert.equal(L.w.timers.length, 1, 'the dwell timer was not armed');
  assert.equal(L.w.timers[0].ms, 1500, 'the dwell is ' + L.w.timers[0].ms + 'ms, not 1500');
  L.w.fireTimers();
  assert.equal(ll[0].w, 4, 'the swap did not exchange WIDTH');
  assert.equal(ll[1].w, 6, 'the swap did not exchange width back');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d comparisons differ', bad, checked); process.exit(1); }
say('grid-drag-check: %d comparisons identical', checked);
