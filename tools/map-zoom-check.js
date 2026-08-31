'use strict';
/**
 * The Connections map's ZOOM AND PAN arithmetic, live against ported.
 *
 * `attachMapZoom` was driven by NOTHING -- no gate, no nodecheck test -- and it
 * had been treated as browser work. It is not: its whole observable output is a
 * `transform` string, a `transform-origin` and a cursor, which is arithmetic a
 * string-storing shim holds exactly. 170 lines deciding where the map sits.
 *
 * ---- THE WHEEL PATH COMPARES DIRECTLY -------------------------------------
 *
 * Both sides zoom on wheel by the same factor and the same rule --
 * `e.deltaY < 0 ? 1.15 : 1 / 1.15` here, `app.js:4461` there -- toward the
 * pointer, clamped to [1, 8], with the translate clamped to the map's own edges.
 * So the events go in and the transform comes out, on each side through its own
 * listeners.
 *
 * ---- WHAT IS NOT COMPARED, AND WHY -----------------------------------------
 *
 *   THE BUTTONS' STEP. The live app calls `zoomAt(1.5, ...)` from a click; the
 *   port dispatches a synthetic WheelEvent at 1.15. That is a mechanism
 *   difference the port rules allow, so a shared corpus would be comparing two
 *   different step sizes. The button WIRING -- that In sends a negative deltaY
 *   and Out a positive one -- is checked at the end of this file instead,
 *   textually, because swapped buttons are the plausible failure and nothing
 *   else notices.
 *
 *   DRAG EVENTS. Live listens for `mousedown`/`mousemove` on `wrap` and
 *   `window`; the port listens for `pointerdown`/`pointermove` on a retargetable
 *   element. Each side is driven through its OWN events and the RESULT is
 *   compared, which is the division `queues-page-check` already draws for a
 *   debounced search.
 *
 *   TOUCH. The live app has pinch handling this port has not reached, and a gate
 *   that drove it would be comparing something to nothing.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/map-zoom-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('map-zoom-check');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource, which yields '' when the reference is gone rather
// than throwing ENOENT. What the gate lifts from it is frozen below.
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

// It READS `connections.ts` for the button contract at the end, and reading is
// not gating -- see `page-gate-audit`'s `--not-gates`.
if (process.argv.includes('--not-gates')) {
  console.log(JSON.stringify(['pages/connections'])); process.exit(0);
}
// The three button ids appear below as literals for that contract check. They
// are NOT claimed as covered: the wiring is checked, the zoom behind them is
// compared through the wheel path, and the buttons' own step size is not. An
// EXPLICIT empty list is required to say that -- a gate answering no `--ids` is
// text-scanned, and silence would claim all three.
if (process.argv.includes('--ids')) { console.log(JSON.stringify([])); process.exit(0); }

/** The live zoom block, from its state to the end of the mouse drag. */
function liveSlice() {
  const from = src.indexOf('    var scale=1, tx=0, ty=0;');
  if (LIFT.hasReference(ROOT)) assert.ok(from > 0, 'the live zoom state has moved -- this gate lifts it by that declaration');
  const to = src.indexOf('    // Touch pinch zoom + drag', from);
  if (LIFT.hasReference(ROOT)) assert.ok(to > from, 'the live drag block no longer ends where this gate expects');
  // FROZEN — the slice is EXECUTED below, so the text must survive. Inside a
  // function, which is why no module-scope pattern reached it.
  const slice = G.value('the lifted zoom slice', () => src.slice(from, to));
  if (!slice || slice.length < 100) throw new Error('the recorded zoom slice is empty');
  // BELIEVABILITY OF THE LIFT: a shorter slice would still evaluate and would
  // register no listeners, and every case would then compare two untouched
  // transforms.
  assert.match(slice, /addEventListener\('wheel'/, 'the slice carries no wheel listener');
  assert.match(slice, /zoomAt\(factor,cx,cy\)/, 'the slice carries no zoom call');
  assert.match(slice, /clampTranslate/, 'the slice carries no clamp');
  return slice;
}

const ENTRY = path.join(ROOT, 'testdata', '.mz-entry.ts');
fs.writeFileSync(ENTRY, "export { attachMapZoom } from '../web/src/pages/connections-worldmap.js';\n");
const OUT = path.join(ROOT, 'testdata', '.mz.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

/** The wrapper and the SVG, with only what the two implementations touch. */
function makeDom() {
  const listeners = {};
  const wrap = {
    style: {},
    addEventListener: (ev, fn) => { (listeners[ev] = listeners[ev] || []).push(fn); },
    removeEventListener: (ev, fn) => {
      listeners[ev] = (listeners[ev] || []).filter((f) => f !== fn);
    },
    // A wrapper OFFSET from the viewport, so a side that forgot to subtract it
    // would land the zoom somewhere else. At 0,0 the subtraction is invisible.
    getBoundingClientRect: () => ({ left: 40, top: 25, width: 800, height: 400 }),
  };
  const svg = { style: {}, clientWidth: 800, clientHeight: 400 };
  // ---- THE EVENT TARGET IS A REAL ENOUGH ELEMENT ------------------------
  //
  // The live mousedown asks `e.target.tagName === 'BUTTON' || e.target.closest(
  // 'button')` before starting a drag, so a press on the zoom controls does not
  // begin a pan. A bare `{}` target threw, which is the shim being too thin
  // rather than the page being wrong -- so the target answers both, and a case
  // below presses ON a button to exercise the guard.
  const asTarget = (kind) => (kind === 'button'
    ? { tagName: 'BUTTON', closest: (sel) => (sel === 'button' ? { tagName: 'BUTTON' } : null) }
    : { tagName: 'DIV', closest: () => null });
  const fire = (ev, extra, kind) => {
    for (const fn of (listeners[ev] || []).slice()) {
      fn(Object.assign({ preventDefault() {}, target: asTarget(kind) }, extra));
    }
  };
  return { wrap, svg, fire };
}

/** What a viewer would see. */
const snap = (dom) => JSON.stringify({
  transform: dom.svg.style.transform,
  origin: dom.svg.style.transformOrigin,
  cursor: dom.wrap.style.cursor,
});

/** A script of wheel notches and drags, in order. */
function drive(dom, steps, names) {
  for (const s of steps) {
    if (s.wheel !== undefined) {
      dom.fire('wheel', { deltaY: s.wheel, clientX: s.x ?? 440, clientY: s.y ?? 225 });
    } else if (s.down) {
      dom.fire(names.down, { clientX: s.down[0], clientY: s.down[1] }, s.on);
    } else if (s.move) {
      dom.fire(names.move, { clientX: s.move[0], clientY: s.move[1] });
    } else if (s.up) {
      dom.fire(names.up, {});
    }
  }
}

function liveRun(steps) {
  const dom = makeDom();
  const ctx = {
    Math, Array, Object, Number, String,
    wrap: dom.wrap, mapEl: dom.svg,
    // The live drag listens on `window` for move and up; routed to the same
    // recorder so one `fire` drives both halves.
    window: {
      addEventListener: (ev, fn) => dom.wrap.addEventListener(ev, fn),
      removeEventListener: (ev, fn) => dom.wrap.removeEventListener(ev, fn),
    },
  };
  vm.createContext(ctx);
  vm.runInContext('(function(){ var svg = mapEl;\n' + liveSlice() + '\n}());', ctx);
  drive(dom, steps, { down: 'mousedown', move: 'mousemove', up: 'mouseup' });
  return snap(dom);
}

function portRun(steps) {
  const dom = makeDom();
  delete require.cache[require.resolve(OUT)];
  require(OUT).attachMapZoom(dom.wrap, dom.svg);
  drive(dom, steps, { down: 'pointerdown', move: 'pointermove', up: 'pointerup' });
  return snap(dom);
}

const CASES = {
  'nothing happens at all': [],
  'one notch in': [{ wheel: -100 }],
  'two notches in': [{ wheel: -100 }, { wheel: -100 }],
  'one notch out from rest': [{ wheel: 100 }],
  // The floor: zooming out at scale 1 must change NOTHING, the transform string
  // included, and `if (next === scale) return` is what does it.
  'out, out, out from rest': [{ wheel: 100 }, { wheel: 100 }, { wheel: 100 }],
  'in then out returns': [{ wheel: -100 }, { wheel: 100 }],
  // The ceiling at 8. Fifteen notches of 1.15 overshoot it comfortably.
  'past the ceiling': Array.from({ length: 15 }, () => ({ wheel: -100 })),
  'to the ceiling and one back': Array.from({ length: 15 }, () => ({ wheel: -100 }))
    .concat([{ wheel: 100 }]),
  // TOWARD THE POINTER. The same notch at three different points must land the
  // map in three different places -- what the `cx - (cx - tx) * k` term is for --
  // and the wrapper's own offset is subtracted first.
  'zoom at the left edge': [{ wheel: -100, x: 40, y: 25 }],
  'zoom at the right edge': [{ wheel: -100, x: 840, y: 425 }],
  'zoom off the top-left, outside the wrapper': [{ wheel: -100, x: 0, y: 0 }],
  'two notches at different points': [{ wheel: -100, x: 100, y: 60 },
    { wheel: -100, x: 700, y: 380 }],

  // DRAG. At rest it must not move at all; above it, the translate is clamped to
  // the map's edges, so a big drag stops rather than losing the map off the side.
  'drag at rest does nothing': [{ down: [400, 200] }, { move: [500, 260] }, { up: true }],
  'zoom in, then drag': [{ wheel: -100 }, { down: [400, 200] }, { move: [430, 220] }, { up: true }],
  'zoom in, then drag the other way': [{ wheel: -100 }, { down: [400, 200] },
    { move: [370, 180] }, { up: true }],
  'a drag far past the edge is clamped': [{ wheel: -100 }, { down: [400, 200] },
    { move: [9000, 9000] }, { up: true }],
  'a drag far past the other edge': [{ wheel: -100 }, { down: [400, 200] },
    { move: [-9000, -9000] }, { up: true }],
  // A press ON one of the zoom BUTTONS must not begin a pan.
  'a press on a button does not start a drag': [{ wheel: -100 }, { down: [400, 200], on: 'button' },
    { move: [500, 260] }, { up: true }],
  'move without a down does nothing': [{ wheel: -100 }, { move: [500, 260] }],
  'a drag continues across several moves': [{ wheel: -100 }, { down: [400, 200] },
    { move: [420, 210] }, { move: [440, 220] }, { move: [460, 230] }, { up: true }],
  'a move after the up is ignored': [{ wheel: -100 }, { down: [400, 200] },
    { move: [430, 220] }, { up: true }, { move: [600, 300] }],
  // Zooming back to rest after a pan must bring the map home: the clamp allows
  // no translate at scale 1, so a leftover offset would show as a stuck map.
  'pan, then zoom back to rest': [{ wheel: -100 }, { down: [400, 200] }, { move: [300, 150] },
    { up: true }, { wheel: 100 }],
};

let bad = 0;
let checked = 0;
for (const [name, steps] of Object.entries(CASES)) {
  let a, b;
  try { a = liveRun(steps); } catch (e) { shout('LIVE THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  try { b = portRun(steps); } catch (e) { shout('PORT THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  checked++;
  if (a !== b) { bad++; shout('DIFF %s\n  live: %s\n  port: %s', name, a, b); }
}

// ---- BELIEVABILITY --------------------------------------------------------
//
// Every case compares two snapshots, and two implementations that ignored every
// event would produce identical empty ones. So the LIVE side alone must move,
// and must move DIFFERENTLY where the cases are supposed to differ.
{
  const rest = JSON.parse(liveRun([]));
  assert.ok(!rest.transform, 'the live map has a transform before anything happened');
  const one = JSON.parse(liveRun([{ wheel: -100 }]));
  assert.match(one.transform, /scale\(1\.15\)/, 'a wheel notch did not zoom the LIVE map');
  assert.equal(one.cursor, 'grab', 'the zoomed LIVE map did not offer a grab cursor');
  const left = liveRun([{ wheel: -100, x: 40, y: 25 }]);
  const right = liveRun([{ wheel: -100, x: 840, y: 425 }]);
  assert.notEqual(left, right, 'the same notch at opposite corners produced the same transform ' +
    'on the LIVE side -- the toward-the-pointer term is not being exercised');
  const dragged = liveRun([{ wheel: -100 }, { down: [400, 200] }, { move: [370, 180] },
    { up: true }]);
  assert.notEqual(dragged, JSON.stringify(one), 'dragging moved nothing on the LIVE side');
}

// ---- THE BUTTONS' WIRING --------------------------------------------------
//
// Checked textually because the two sides zoom by different steps from a click,
// and a shared corpus would be comparing 1.5 against 1.15. What must not differ
// is WHICH WAY each button goes: swapped In and Out is the plausible failure, it
// is user-visible immediately, and no case above would notice.
{
  const page = fs.readFileSync(path.join(ROOT, 'web', 'src', 'pages', 'connections.ts'), 'utf8');
  const wiring = (id) => {
    const at = page.indexOf("el('" + id + "')");
    if (LIFT.hasReference(ROOT)) assert.ok(at > 0, 'connections.ts no longer wires #' + id);
    return page.slice(at, at + 420);
  };
  assert.match(wiring('mapZoomIn'), /deltaY:\s*-\d/,
    '#mapZoomIn does not send a NEGATIVE deltaY -- it would zoom OUT');
  assert.match(wiring('mapZoomOut'), /deltaY:\s*\d/,
    '#mapZoomOut does not send a POSITIVE deltaY -- it would zoom IN');
  assert.match(wiring('mapZoomReset'), /zoom\.reset/,
    '#mapZoomReset is not wired to zoom.reset');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say("map-zoom-check: %d cases identical (wheel and drag; the buttons' wiring checked textually)",
  checked);
