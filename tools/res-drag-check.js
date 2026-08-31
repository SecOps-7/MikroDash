'use strict';
/**
 * Drag-to-reorder, live against ported.
 *
 * ── WHAT IS COMPARED ────────────────────────────────────────────────────────
 *
 * The DECISION, not the rendering: given a table, a row being dragged and a
 * pointer position, which order does the table end in, and which anchor does the
 * drop send? Those are the two things a wrong port gets wrong in a way an
 * operator sees — a rule that lands in the wrong place, or a move request that
 * names the wrong neighbour.
 *
 * Both sides run against the same fake DOM. The live code touches a small,
 * bounded API — elementFromPoint, closest, contains, insertBefore,
 * compareDocumentPosition, nextElementSibling — so modelling it is cheap and the
 * comparison is exact rather than approximate.
 *
 * ── THE CASES THAT MATTER ───────────────────────────────────────────────────
 *
 *   dragging up vs down    `compareDocumentPosition` decides whether the row
 *                          lands before or after the row under the pointer, and
 *                          getting it backwards moves the rule the wrong way.
 *   back over the gap      the origin marker is a drop target, and landing on it
 *                          must restore the ORIGINAL slot rather than oscillate.
 *   the marker is not      it carries no data-id, so `anchorAfter` must skip it
 *   an anchor              — and `endDrag` removes it before the walk anyway,
 *                          which is belt and braces the live code states.
 *   the end of the table   sends anchor '' — PRESENT and empty, which the server
 *                          reads as "land at the end". Absent would mean the
 *                          arrow spelling instead. See HasAnchor in resource.go.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/res-drag-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/res-drag-check.js --freeze
const G = L.golden('res-drag-check');
const src = L.liveSource(ROOT);

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
const liveSrc = [
  slice('  function rowUnder(host, x, y) {', '\n  }', 'rowUnder'),
  slice('  function makeOriginMarker(row) {', '\n  }', 'makeOriginMarker'),
  slice('  function dragTo(x, y) {', '\n  }', 'dragTo'),
  slice('  function syncOriginMarker() {', '\n  }', 'syncOriginMarker'),
  slice('  function endDrag() {', '\n  }', 'endDrag'),
].join('\n');

const ENTRY = path.join(ROOT, 'testdata', '.drag-entry.ts');
// NO TEST-ONLY SEAM. The drag state is private and startable only through a
// real `pointerdown`, so the harness fires one — which also proves the handle,
// the row, the host and the schema check all line up, and not merely that the
// placement maths is right.
fs.writeFileSync(ENTRY, "export { dragTo, endDrag, anchorAfter, mountRows } from '../web/src/resource.js';\n");
const OUT = path.join(ROOT, 'testdata', '.drag-port.cjs');

// ── A DOM small enough to reason about ──────────────────────────────────────

function makeDom(ids, dragIdx) {
  const mk = (id, cls) => {
    const node = {
      _id: id, className: cls || '', children: [{}, {}, {}],
      parentNode: null, _classes: new Set(cls ? [cls] : []),
      getAttribute: (k) => (k === 'data-id' ? (id || null)
        : k === 'data-identity' ? (id ? 'name-' + id : null) : null),
      getBoundingClientRect: () => ({ height: 20 }),
      classList: {
        add: (c) => node._classes.add(c), remove: (c) => node._classes.delete(c),
        toggle: (c, on) => { if (on) node._classes.add(c); else node._classes.delete(c); },
        contains: (c) => node._classes.has(c),
      },
      closest: (sel) => {
        if (sel === 'tr[data-id], tr.res-drag-origin') {
          return node._id || node._classes.has('res-drag-origin') ? node : null;
        }
        return null;
      },
      style: {},
      appendChild() {},
    };
    return node;
  };
  const rows = ids.map((id) => mk(id));
  const host = {
    _rows: rows,
    contains: (n) => host._rows.indexOf(n) !== -1,
    insertBefore(node, before) {
      const at = host._rows.indexOf(node);
      if (at !== -1) host._rows.splice(at, 1);
      const to = before ? host._rows.indexOf(before) : -1;
      if (to === -1) host._rows.push(node); else host._rows.splice(to, 0, node);
      node.parentNode = host;
      return node;
    },
    removeChild(node) {
      const at = host._rows.indexOf(node);
      if (at !== -1) host._rows.splice(at, 1);
      node.parentNode = null;
      return node;
    },
  };
  for (const r of rows) r.parentNode = host;
  // Sibling links and document position are derived from the live array, so a
  // move updates them without bookkeeping.
  for (const r of rows) {
    Object.defineProperty(r, 'nextElementSibling', {
      get: () => host._rows[host._rows.indexOf(r) + 1] || null,
    });
    Object.defineProperty(r, 'nextSibling', {
      get: () => host._rows[host._rows.indexOf(r) + 1] || null,
    });
    r.compareDocumentPosition = (other) =>
      (host._rows.indexOf(other) < host._rows.indexOf(r) ? 2 : 4); // 2 = PRECEDING
  }
  return { host, rows, row: rows[dragIdx], mk };
}

/** Wire a document whose elementFromPoint answers from a caller-set target. */
function makeDoc(dom) {
  let hit = null;
  const doc = {
    elementFromPoint: () => hit,
    createElement: (tag) => {
      const n = dom.mk('', tag === 'tr' ? 'res-drag-origin' : '');
      n.className = '';
      Object.defineProperty(n, 'nextElementSibling', {
        get: () => dom.host._rows[dom.host._rows.indexOf(n) + 1] || null,
      });
      Object.defineProperty(n, 'nextSibling', {
        get: () => dom.host._rows[dom.host._rows.indexOf(n) + 1] || null,
      });
      n.compareDocumentPosition = (other) =>
        (dom.host._rows.indexOf(other) < dom.host._rows.indexOf(n) ? 2 : 4);
      // className is what marks it, and the live code sets it after creation.
      Object.defineProperty(n, 'className', {
        get: () => [...n._classes].join(' '),
        set: (v) => { n._classes = new Set(v ? v.split(' ') : []); },
      });
      return n;
    },
    body: { classList: { add() {}, remove() {}, contains: () => false } },
    _h: {},
    addEventListener(n, f) { (doc._h[n] = doc._h[n] || []).push(f); },
    getElementById: () => null,
    querySelectorAll: () => [],
  };
  return {
    doc,
    aim: (t) => { hit = t; },
    fire: (n, ev) => { for (const f of (doc._h[n] || [])) f(ev); },
  };
}

const order = (dom) => dom.host._rows.map((r) => r._id || '<gap>').join(',');

function liveRun(ids, dragIdx, aimAt) {
  const dom = makeDom(ids, dragIdx);
  const { doc, aim } = makeDoc(dom);
  const ctx = {
    document: doc, Node: { DOCUMENT_POSITION_PRECEDING: 2 },
    cancelAnimationFrame() {},
    _drag: { host: dom.host, row: dom.row, key: 'firewallFilter', raf: 0, x: 0, y: 0, marker: null },
  };
  vm.createContext(ctx);
  vm.runInContext(liveSrc, ctx);
  const steps = [];
  for (const idx of aimAt) {
    if (idx === 'detach') {
      dom.host._rows = dom.host._rows.filter((r) => r !== dom.row);
      aim(dom.rows[0]);
    } else {
      aim(idx === 'gap' ? ctx._drag.marker : dom.rows[idx]);
    }
    ctx.dragTo(0, 0);
    // WITH THE GAP, and per step. Comparing only the final order missed three
    // mutations whose whole effect is transient: where the marker sits while
    // the drag is in flight, and whether it is created at all. endDrag removes
    // it, so by the end the two are indistinguishable — which is exactly why
    // the intermediate states have to be part of the comparison.
    steps.push(order(dom) + (ctx._drag && ctx._drag.marker
      ? '|home=' + (ctx._drag.marker.classList.contains('is-home') ? 'y' : 'n') : '|nomarker'));
  }
  // `pointerup` places once more at the release point BEFORE ending the drag.
  // Leaving it out made this side skip a step the port performs, and the
  // difference read as a port defect. The cursor is over the DRAGGED row by
  // then — it has been following the pointer — so the call settles rather than
  // swapping again.
  aim(dom.row);
  ctx.dragTo(0, 0);
  const d = ctx.endDrag();
  let anchor = null;
  if (d && d.host.contains(d.row)) {
    let next = d.row.nextElementSibling;
    while (next && !next.getAttribute('data-id')) next = next.nextElementSibling;
    anchor = next ? next.getAttribute('data-id') : '';
  }
  return JSON.stringify({ steps, order: order(dom), anchor }, null, 1);
}

execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

/** The pointerdown target: the handle, inside the row, inside the host. */
function handleFor(dom) {
  const chain = {
    '[data-res-drag]': null, // filled below, once the handle exists
    '[data-id]': dom.row,
    '[data-res-rows]': dom.host,
  };
  const handle = {
    closest: (sel) => (sel in chain ? chain[sel] : null),
    setPointerCapture() {},
    getAttribute: () => null,
  };
  chain['[data-res-drag]'] = handle;
  dom.host.getAttribute = (k) => (k === 'data-res-rows' ? 'firewallFilter' : null);
  return handle;
}

function portRun(ids, dragIdx, aimAt) {
  const dom = makeDom(ids, dragIdx);
  const { doc, aim, fire } = makeDoc(dom);
  const saved = { document: global.document, window: global.window, Node: global.Node,
                  requestAnimationFrame: global.requestAnimationFrame,
                  cancelAnimationFrame: global.cancelAnimationFrame };
  global.document = doc;
  global.window = { confirm: () => true };
  global.Node = { DOCUMENT_POSITION_PRECEDING: 2 };
  // DEFERRED, not inline. The port writes `drag.raf = requestAnimationFrame(cb)`
  // and `cb` clears `drag.raf` — so running cb inline lets the assignment
  // overwrite the cleared flag with a live handle, and every later pointermove
  // returns early as "already scheduled". Only the first move ever landed, which
  // looked exactly like a broken port.
  //
  // A real rAF is asynchronous, so the assignment always wins the race. The
  // harness flushes the pending frame after dispatching each event instead.
  let frame = null;
  global.requestAnimationFrame = (fn) => { frame = fn; return 1; };
  global.cancelAnimationFrame = () => { frame = null; };
  const flushFrame = () => { const f = frame; frame = null; if (f) f(); };
  const sent = [];
  const socketHandlers = new Map();
  try {
    delete require.cache[require.resolve(OUT)];
    const mod = require(OUT);
    mod.mountRows({
      emit: (ev, payload) => sent.push({ ev, payload }),
      on: (ev, fn) => socketHandlers.set(ev, fn),
      isOpen: () => true,
    });
    socketHandlers.get('res:schema')({ key: 'firewallFilter', permitted: true });
    const handle = handleFor(dom);
    var steps = [];
    fire('pointerdown', { target: handle, clientX: 0, clientY: 0, preventDefault() {}, pointerId: 1 });
    for (const idx of aimAt) {
      if (idx === 'gap') {
        aim(dom.host._rows.find((r) => r.classList.contains('res-drag-origin')) || null);
      } else if (idx === 'detach') {
        // The table was re-rendered out from under the drag.
        dom.host._rows = dom.host._rows.filter((r) => r !== dom.row);
        aim(dom.rows[0]);
      } else {
        aim(dom.rows[idx]);
      }
      fire('pointermove', { clientX: 0, clientY: 0 });
      flushFrame();
      const marker = dom.host._rows.find((r) => r.classList.contains('res-drag-origin'));
      steps.push(order(dom) + (marker
        ? '|home=' + (marker.classList.contains('is-home') ? 'y' : 'n') : '|nomarker'));
    }
    sent.length = 0;
    // The pointer ends over the row it dragged, because the row has been moving
    // to meet it. Without this the final placement inside `pointerup` saw the
    // OLD target still under the cursor and swapped the row back.
    aim(dom.row);
    fire('pointerup', {});
  } finally {
    for (const k of Object.keys(saved)) {
      if (saved[k] === undefined) delete global[k]; else global[k] = saved[k];
    }
  }
  const move = sent.find((s) => s.ev === 'res:move');
  return JSON.stringify({ steps, order: order(dom),
    anchor: move ? move.payload.anchor : null }, null, 1);
}

const bad = [];
let cases = 0;
function compare(what, ids, dragIdx, aimAt) {
  cases++;
  const a = G.live(G.seq(), () => liveRun(ids, dragIdx, aimAt));
  const b = portRun(ids, dragIdx, aimAt);
  if (a !== b) bad.push(what + '\n  live: ' + a + '\n  port: ' + b);
}

const FIVE = ['a', 'b', 'c', 'd', 'e'];

// Dragging DOWN: the row lands after the row under the pointer.
compare('drag the first row down one', FIVE, 0, [1]);
compare('drag the first row to the end', FIVE, 0, [1, 2, 3, 4]);
compare('drag the middle row down one', FIVE, 2, [3]);
// Dragging UP: it lands before.
compare('drag the last row up one', FIVE, 4, [3]);
compare('drag the last row to the top', FIVE, 4, [3, 2, 1, 0]);
compare('drag the middle row up one', FIVE, 2, [1]);
// Aiming at the row being dragged does nothing.
compare('aim at the dragged row itself', FIVE, 2, [2]);
compare('aim at nothing at all', FIVE, 2, []);
// Out and back: the original slot must be restored, and the anchor with it.
compare('drag down one then back over the gap', FIVE, 1, [2, 'gap']);
compare('drag up one then back over the gap', FIVE, 3, [2, 'gap']);
compare('drag away two then back', FIVE, 0, [1, 2, 'gap']);
// A two-row table, where every move is an edge case.
compare('two rows, drag the first down', ['a', 'b'], 0, [1]);
compare('two rows, drag the second up', ['a', 'b'], 1, [0]);
// The table re-rendered mid-drag — a tab switch, a router switch, anything that
// rebuilds it. Re-inserting the detached node would put a SECOND copy beside its
// replacement, so the drag has to end instead.
compare('the row is re-rendered away mid-drag', FIVE, 1, [2, 'detach']);
compare('the row is re-rendered away before any move', FIVE, 1, ['detach']);

// A single row has nowhere to go.
compare('one row, nowhere to go', ['a'], 0, [0]);

(async () => {
  fs.rmSync(OUT, { force: true });
  if (bad.length) {
    console.error('drag-to-reorder differs from the live engine:\n\n' + bad.slice(0, 3).join('\n\n') + '\n');
    process.exit(1);
  }
  console.log('drag-to-reorder matches the live engine (' + cases + ' cases: up, down, ' +
    'back-over-the-gap, and the end-of-table anchor)');
})();
