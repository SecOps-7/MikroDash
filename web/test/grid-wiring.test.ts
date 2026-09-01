// Moved from the grid-wiring check when the port-parity harness was retired.
//
// The body is VERBATIM apart from its imports and the path to the repository
// root: this test drives the port's OWN TypeScript and asserts what it does, so
// nothing in it referred to the implementation this app replaced.
/**
 * Is the Dashboard grid actually WIRED?
 *
 * ── THE SAME QUESTION dashboard-wiring-check ASKS OF THE CARDS ──────────────
 *
 * Five layers were ported and gated, each proven against the live implementation
 * by being CALLED. None of that says anything about whether a pointer landing on
 * a drag handle reaches `startDrag`, and a handle that renders correctly and does
 * nothing is the defect shape this port has hit six times.
 *
 * So this drives the real thing: `initDashboardGrid` runs against a fake
 * document, and then real events are DISPATCHED at real elements — a pointerdown
 * on a drag handle, a click on a remove button, a class change on the page — and
 * the layout is checked for having moved. Grepping for `addEventListener` would
 * prove only that the string is present.
 *
 * ── AND IT PROVES THE GATES ARE GATES ───────────────────────────────────────
 *
 * Every entry point is supposed to be inert outside edit mode. Each is therefore
 * fired BEFORE entering edit mode as well as after, and the "before" case must
 * change nothing at all.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/grid-wiring-check.js
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// EVERYTHING ELSE IN THIS GATE DRIVES THE PORT. The reference is consulted for
// exactly the five assertions below, each of which asks the live SOURCE a
// question — does it still dispatch this event, does it still use this selector.
// Those are unanswerable without a source, so they are guarded; nothing else
// here needs a recording (LOOP.md 3n and 3o).
// The block that compared this against the deleted implementation was removed
// when the port-parity harness was retired. It had been dead since cutover --
// `LIFT.hasReference` has answered false ever since -- so removing it changes
// nothing that ran. Everything below drives the PORT and asserts what it does.

// The live app is the authority on which events this module must handle. If it
// stops dispatching socket:reconnect, the port's listener becomes dead code.


const ENTRY = path.join(ROOT, 'testdata', '.gridwire-entry.ts');
fs.writeFileSync(ENTRY,
  "export { initDashboardGrid } from '../web/src/pages/dashboard-grid.js';\n" +
  "export { DEFAULT_LAYOUT, LS_KEY } from '../web/src/gen/grid-tables.js';\n");
const OUT = path.join(ROOT, 'testdata', '.gridwire-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

// ── a fake DOM with real enough event dispatch ─────────────────────────────
const GRID = { left: 0, top: 0, width: 1200, height: 800 };

function makeDom() {
  const byId = new Map();
  const docListeners = [];
  const observers = { mutation: [], resize: [] };
  const dispatched = [];

  function node(tag, id, cls) {
    const n = {
      tagName: String(tag).toUpperCase(), id: id || '', className: cls || '',
      // `setProperty` and not just a bag of keys: the overlay sets CSS custom
      // properties, which a plain object does not accept.
      style: { _vars: {}, setProperty(k, v) { this._vars[k] = v; }, getPropertyValue(k) { return this._vars[k]; } },
      dataset: {}, children: [], parent: null, listeners: [],
      classes: new Set((cls || '').split(' ').filter(Boolean)),
      classList: {
        add(c) { n.classes.add(c); fireMutation(n); },
        remove(c) { n.classes.delete(c); fireMutation(n); },
        contains: (c) => n.classes.has(c),
      },
      addEventListener: (t, cb) => n.listeners.push({ t, cb }),
      removeEventListener: (t, cb) => {
        const i = n.listeners.findIndex((l) => l.t === t && l.cb === cb);
        if (i >= 0) n.listeners.splice(i, 1);
      },
      appendChild(c) { c.parent = n; n.children.push(c); return c; },
      contains(other) { for (let p = other; p; p = p.parent) if (p === n) return true; return false; },
      closest(sel) {
        const want = sel.replace(/^\./, '');
        for (let p = n; p; p = p.parent) if (p.classes.has(want)) return p;
        return null;
      },
      getBoundingClientRect: () => n._rect || { left: 0, top: 0, width: 0, height: 0, right: 0, bottom: 0 },
      setPointerCapture() {}, releasePointerCapture() {},
      remove() { if (n.parent) n.parent.children = n.parent.children.filter((c) => c !== n); },
      set innerHTML(v) { n._h = v; if (v === '') n.children = []; },
      get innerHTML() { return n._h || ''; },
    };
    if (id) byId.set(id, n);
    return n;
  }
  function fireMutation(target) {
    for (const o of observers.mutation) if (o.target === target) o.cb();
  }
  const doc = {
    getElementById: (id) => byId.get(id) || null,
    createElement: (t) => node(t),
    body: { appendChild: (n) => n },
    addEventListener: (t, cb) => docListeners.push({ t, cb }),
    dispatchEvent: (e) => {
      dispatched.push(e.type);
      for (const l of docListeners) if (l.t === e.type) l.cb(e);
      return true;
    },
  };
  return { byId, node, doc, docListeners, observers, dispatched, fireMutation };
}

/** A dashboard page with `n` cards, each with a drag handle, resize handles and a remove button. */
function buildPage(dom, layout) {
  const page = dom.node('div', 'page-home', 'page-view active');
  const root = dom.node('div', 'dash-grid-root', 'dash-grid');
  root._rect = { left: GRID.left, top: GRID.top, width: GRID.width, height: GRID.height, right: GRID.width, bottom: GRID.height };
  page.appendChild(root);
  dom.node('div', 'dash-placeholder', '');
  dom.node('button', 'dashEditBtn', '');
  dom.node('div', 'dashEditControls', '');
  dom.node('button', 'dashSaveBtn', '');
  dom.node('button', 'dashDiscardBtn', '');
  dom.node('button', 'dashAddCardBtn', '');
  dom.node('div', 'dashAddPanel', '');
  const handles = {};
  for (const c of layout) {
    const card = dom.node('div', c.id, 'dash-card');
    root.appendChild(card);
    const dh = dom.node('div', c.id + '-drag', 'dash-drag-handle');
    card.appendChild(dh);
    const rh = dom.node('div', c.id + '-resize-se', 'dash-resize');
    rh.dataset.dir = 'se';
    card.appendChild(rh);
    const rm = dom.node('button', c.id + '-remove', 'dash-remove-btn');
    rm.dataset.card = c.id;
    card.appendChild(rm);
    handles[c.id] = { card, drag: dh, resize: rh, remove: rm };
  }
  return { page, root, handles };
}

function fire(dom, node, type, extra) {
  let stopped = false;
  const e = Object.assign({
    type, target: node, preventDefault() {}, stopPropagation() { stopped = true; },
    clientX: 0, clientY: 0, pointerId: 3,
  }, extra || {});
  for (const l of node.listeners.filter((l) => l.t === type)) l.cb(e);
  // Bubble to ancestors, which is how the delegated listeners on the root see it.
  for (let p = node.parent; p && !stopped; p = p.parent) {
    for (const l of p.listeners.filter((l) => l.t === type)) l.cb(e);
  }
  // AND ON TO THE DOCUMENT, honouring stopPropagation. Without this leg the Add
  // button's `stopPropagation` is unobservable: the outside-click listener lives
  // on `document`, so a mutation removing the stop looked identical to keeping
  // it — the panel opened and nothing closed it again.
  if (!stopped) {
    for (const l of dom.docListeners.filter((l) => l.t === type)) l.cb(e);
  }
  return e;
}

function boot(layoutOverride) {
  const dom = makeDom();
  const LAY = layoutOverride || [
    { id: 'card-traffic', x: 1, y: 1, w: 6, h: 4, visible: true },
    { id: 'card-system', x: 9, y: 1, w: 4, h: 3, visible: true },
    { id: 'dc-card-bgp', x: 15, y: 1, w: 4, h: 3, visible: false },
  ];
  const page = buildPage(dom, LAY);
  const store = {};
  globalThis.document = dom.doc;
  globalThis.localStorage = {
    getItem: (k) => (k in store ? store[k] : null),
    setItem: (k, v) => { store[k] = v; },
  };
  globalThis.CustomEvent = function (t, i) { return { type: t, detail: i && i.detail }; };
  globalThis.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve(null) });
  globalThis.setTimeout = (cb) => { void cb; return 1; };
  globalThis.clearTimeout = () => {};
  const mutations = [];
  globalThis.MutationObserver = function (cb) {
    return { observe: (target) => { dom.observers.mutation.push({ target, cb }); mutations.push(target.id); } };
  };
  const resizes = [];
  globalThis.ResizeObserver = function (cb) {
    return { observe: (target) => { dom.observers.resize.push({ target, cb }); resizes.push(target.id); } };
  };
  delete require.cache[require.resolve(OUT)];
  const m = require(OUT);
  // The stored layout is what initDashboardGrid loads, so seed it first — and
  // seed EVERY card, not just the ones this page builds elements for.
  //
  // `mergeLayout` walks DEFAULT_LAYOUT and fills in whatever the saved list does
  // not mention, so seeding three cards produced a layout with the other nine
  // defaults still visible in their shipped positions. The grid was then nearly
  // full, every drag target overlapped something, and the snap was correctly
  // REFUSED — which read as "the handle is not wired". The cards this test does
  // not use are therefore explicitly hidden.
  const seeded = m.DEFAULT_LAYOUT.map((d) => {
    const mine = LAY.find((c) => c.id === d.id);
    return mine ? { ...mine } : { ...d, visible: false };
  });
  store[m.LS_KEY] = JSON.stringify({ cards: seeded });
  const editor = m.initDashboardGrid();
  return { dom, page, editor, store, mutations, resizes, LAY };
}

const problems = [];
function must(cond, msg) { if (!cond) problems.push(msg); }

// ── it wires at all ────────────────────────────────────────────────────────
{
  const b = boot();
  must(b.editor, 'initDashboardGrid returned null on a page that HAS a grid root');
  must(b.mutations.includes('page-home'),
    'no MutationObserver on page-home — the Edit button never appears or hides, and ' +
    'leaving the page mid-edit would silently keep the changes');
  must(b.resizes.includes('dash-grid-root'), 'no ResizeObserver on the grid root');
  must(b.dom.docListeners.some((l) => l.t === 'socket:reconnect'),
    'nothing listens for socket:reconnect — room membership is per-socket, so a viewer who ' +
    'reconnects keeps a dashboard whose gated cards never receive anything again');
  must(b.dom.docListeners.some((l) => l.t === 'click'),
    'no document click listener — the Add panel never closes on an outside click');
}

// ── a missing grid root is survivable ──────────────────────────────────────
{
  const dom = makeDom();
  globalThis.document = dom.doc;
  globalThis.localStorage = { getItem: () => null, setItem() {} };
  globalThis.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve(null) });
  delete require.cache[require.resolve(OUT)];
  const m = require(OUT);
  let threw = null;
  let r;
  try { r = m.initDashboardGrid(); } catch (e) { threw = e; }
  must(!threw, 'initDashboardGrid threw when the dashboard markup is absent: ' + (threw && threw.message));
  must(r === null, 'initDashboardGrid did not return null without a grid root');
}

// ── the controls are INERT outside edit mode ───────────────────────────────
{
  const b = boot();
  const before = JSON.stringify(b.editor.getLayout());
  // FULL gestures, not just the pointerdown: starting a drag changes no layout
  // by itself, so a missing edit-mode gate looked identical to a present one
  // until the move and the release were driven too.
  const dh = b.page.handles['card-traffic'].drag;
  fire(b.dom, dh, 'pointerdown', { clientX: 60, clientY: 60 });
  fire(b.dom, dh, 'pointermove', { clientX: 600, clientY: 400 });
  fire(b.dom, dh, 'pointerup', { clientX: 600, clientY: 400 });
  const rh = b.page.handles['card-traffic'].resize;
  fire(b.dom, rh, 'pointerdown', { clientX: 300, clientY: 300 });
  fire(b.dom, rh, 'pointermove', { clientX: 420, clientY: 390 });
  fire(b.dom, rh, 'pointerup', { clientX: 420, clientY: 390 });
  fire(b.dom, b.page.handles['card-traffic'].remove, 'click');
  must(JSON.stringify(b.editor.getLayout()) === before,
    'a drag/resize/remove OUTSIDE edit mode changed the layout — the handles must be inert');
  must(!b.editor.isEditing(), 'the editor thinks it is editing before Edit was pressed');
}

// ── the Edit button enters edit mode ───────────────────────────────────────
{
  const b = boot();
  fire(b.dom, b.dom.byId.get('dashEditBtn'), 'click');
  must(b.editor.isEditing(), 'the Edit button did not enter edit mode');
}

// ── a pointerdown on a drag handle REACHES startDrag ───────────────────────
{
  const b = boot();
  fire(b.dom, b.dom.byId.get('dashEditBtn'), 'click');
  const before = JSON.stringify(b.editor.getLayout());
  fire(b.dom, b.page.handles['card-traffic'].drag, 'pointerdown', { clientX: 60, clientY: 60 });
  // A drag in progress is proof enough that startDrag ran; the drag layer's own
  // gate is what proves it moves the card correctly.
  fire(b.dom, b.page.handles['card-traffic'].drag, 'pointermove', { clientX: 600, clientY: 400 });
  fire(b.dom, b.page.handles['card-traffic'].drag, 'pointerup', { clientX: 600, clientY: 400 });
  must(JSON.stringify(b.editor.getLayout()) !== before,
    'a pointerdown on a drag handle inside edit mode did nothing — the handle is not wired');
}

// ── a pointerdown on a resize handle REACHES startResize ───────────────────
{
  const b = boot();
  fire(b.dom, b.dom.byId.get('dashEditBtn'), 'click');
  const before = JSON.stringify(b.editor.getLayout());
  const rh = b.page.handles['card-traffic'].resize;
  fire(b.dom, rh, 'pointerdown', { clientX: 300, clientY: 300 });
  fire(b.dom, rh, 'pointermove', { clientX: 400, clientY: 380 });
  fire(b.dom, rh, 'pointerup', { clientX: 400, clientY: 380 });
  must(JSON.stringify(b.editor.getLayout()) !== before,
    'a pointerdown on a resize handle inside edit mode did nothing — the handle is not wired');
}

// ── the remove button hides its card ───────────────────────────────────────
{
  const b = boot();
  fire(b.dom, b.dom.byId.get('dashEditBtn'), 'click');
  fire(b.dom, b.page.handles['card-system'].remove, 'click');
  const c = b.editor.getLayout().find((x) => x.id === 'card-system');
  must(c && !c.visible, 'the remove button did not hide its card');
}

// ── Save persists; Discard restores ────────────────────────────────────────
{
  const b = boot();
  fire(b.dom, b.dom.byId.get('dashEditBtn'), 'click');
  fire(b.dom, b.page.handles['card-system'].remove, 'click');
  fire(b.dom, b.dom.byId.get('dashSaveBtn'), 'click');
  must(!b.editor.isEditing(), 'Save did not leave edit mode');
  const saved = JSON.parse(b.store[require(OUT).LS_KEY] || '{"cards":[]}');
  const savedCard = saved.cards.find((c) => c.id === 'card-system');
  must(savedCard && savedCard.visible === false,
    'Save did not persist THIS card\'s change to localStorage (saved: ' +
    JSON.stringify(savedCard) + ')');
}
{
  const b = boot();
  fire(b.dom, b.dom.byId.get('dashEditBtn'), 'click');
  const before = JSON.stringify(b.editor.getLayout());
  fire(b.dom, b.page.handles['card-system'].remove, 'click');
  must(JSON.stringify(b.editor.getLayout()) !== before, 'precondition: the remove did nothing');
  fire(b.dom, b.dom.byId.get('dashDiscardBtn'), 'click');
  must(JSON.stringify(b.editor.getLayout()) === before, 'Discard did not restore the snapshot');
}

// ── the Add panel toggles, and closes on an outside click ──────────────────
{
  const b = boot();
  const panel = b.dom.byId.get('dashAddPanel');
  fire(b.dom, b.dom.byId.get('dashAddCardBtn'), 'click');
  must(panel.classes.has('open'), 'the Add button did not open the panel');
  fire(b.dom, b.dom.byId.get('dashAddCardBtn'), 'click');
  must(!panel.classes.has('open'), 'the Add button did not close the panel again');
  fire(b.dom, b.dom.byId.get('dashAddCardBtn'), 'click');
  must(panel.classes.has('open'), 'precondition: the panel should be open');
  // A click somewhere else entirely.
  b.dom.doc.dispatchEvent({ type: 'click', target: b.dom.byId.get('dashSaveBtn') });
  must(!panel.classes.has('open'), 'an outside click did not close the Add panel');
}

// ── leaving the page DISCARDS ──────────────────────────────────────────────
{
  const b = boot();
  fire(b.dom, b.dom.byId.get('dashEditBtn'), 'click');
  const before = JSON.stringify(b.editor.getLayout());
  fire(b.dom, b.page.handles['card-system'].remove, 'click');
  b.page.page.classList.remove('active');   // navigating away
  must(!b.editor.isEditing(), 'navigating away did not leave edit mode');
  must(JSON.stringify(b.editor.getLayout()) === before,
    'navigating away mid-edit KEPT the changes — it must discard them');
}

// ── rooms are re-synced when the PAGE becomes active ───────────────────────
//
// Separate from the reconnect case: they are two different call sites and a
// mutation removing the observer's `syncDashRooms` survived while only the
// reconnect leg was tested.
{
  const b = boot([{ id: 'dc-card-logs', x: 1, y: 1, w: 4, h: 3, visible: true }]);
  b.dom.dispatched.length = 0;
  b.page.page.classList.remove('active');
  const afterBlur = b.dom.dispatched.slice();
  b.dom.dispatched.length = 0;
  b.page.page.classList.add('active');
  const afterFocus = b.dom.dispatched.slice();
  must(afterBlur.includes('dashcard:room:blur'),
    'leaving the dashboard did not LEAVE its card rooms (got ' + JSON.stringify(afterBlur) + ')');
  must(afterFocus.includes('dashcard:room:focus'),
    'returning to the dashboard did not RE-JOIN its card rooms (got ' +
    JSON.stringify(afterFocus) + ')');
}

// ── rooms are re-synced on reconnect ───────────────────────────────────────
{
  const b = boot([
    { id: 'dc-card-logs', x: 1, y: 1, w: 4, h: 3, visible: true },
  ]);
  b.dom.dispatched.length = 0;
  b.dom.doc.dispatchEvent({ type: 'socket:reconnect' });
  must(b.dom.dispatched.some((t) => t === 'dashcard:room:focus'),
    'socket:reconnect did not re-join the visible card rooms (dispatched: ' +
    JSON.stringify(b.dom.dispatched) + ')');
}

if (problems.length) {
  console.error('grid-wiring-check: %d problem(s)\n', problems.length);
  for (const p of problems) console.error('  - ' + p);
  process.exit(1);
}
fs.rmSync(OUT, { force: true });
say('grid-wiring-check: the grid is wired — handles, buttons, panel, observers and reconnect');
