'use strict';
/**
 * THE INTERFACE LIST, live against ported -- and the first gate in this repo to
 * compare NODE REUSE across frames rather than a single frame's markup.
 *
 * ---- WHY THIS COULD NOT BE WRITTEN BEFORE ----------------------------------
 *
 * `interfaces-page-check`'s header records the gap in as many words: both big
 * renderers on this page build NODES, `dom-shim` stores markup as a STRING, and
 * "emulating node identity on top of that would produce a harness sophisticated
 * enough to be wrong in ways nobody could see -- worse than an honest gap." It
 * then says the fix was WRITTEN UP rather than taken, because it looked like it
 * needed a dependency.
 *
 * `tools/lib/tree-shim.js` is that fix, built in this repo and already carrying
 * two gates. So the reason expired, and this is the gate it was blocking.
 *
 * ---- WHAT `renderIfaceList` ACTUALLY PROMISES ------------------------------
 *
 * Not "the right markup". Three things beyond it, none visible to a string:
 *
 *   REUSE     a row whose fingerprint is unchanged is left ALONE -- same node,
 *             untouched. That is what keeps text selection and hover alive
 *             while most interfaces sit idle, and it is the whole reason the
 *             fingerprint exists.
 *   REBUILD   a row whose data changed is rewritten IN PLACE, keeping its node.
 *   REORDER   rows are reused in place, so DOM order does not follow the sorted
 *             array by itself. Both sides re-append every row -- which MOVES it
 *             -- but ONLY when the order key changed, because doing it every
 *             tick would drop text selection for nothing.
 *
 * A single-frame gate cannot see any of the three: with nothing before it, every
 * row is new. So every case here is a SEQUENCE of frames, and each frame is
 * compared on three axes.
 *
 * ---- MOVE COUNTS, AND THE MUTANT THEY EXIST FOR ----------------------------
 *
 * `appendChild` on a node already in the tree MOVES it, and the picture after a
 * move is identical to the picture without one when the order already matched.
 * So "re-append every tick" and "re-append only when the order changed" draw the
 * same table and differ only in how much DOM they churn -- exactly the
 * optimisation both sides went out of their way to make.
 *
 * This gate therefore COUNTS moves, by wrapping the tbody's `appendChild` and
 * asking whether the node was already a child. That was recorded as an
 * equivalent-mutant gap when `sankey-check` was written; it is closed here.
 *
 * ---- MUTATIONS THIS KILLS (2026-08-25), and what each kill is worth ---------
 *
 *   re-append on EVERY frame          10/24   the move counts, and ONLY they.
 *   never re-append at all            22/24   broad because the live side does
 *                                             re-append on frame 1 (the order key
 *                                             starts ''), so the count moves
 *                                             almost everywhere.
 *   drop `errors` from the fingerprint 22/24  a CHEAP kill, and worth saying so:
 *                                             the fingerprint is stored as
 *                                             `data-fp`, so it is in the picture
 *                                             and any change to it differs
 *                                             immediately. This is not evidence
 *                                             that the REUSE logic is checked.
 *   never remove a vanished row        1/24
 *   skip the first-frame innerHTML     22/24
 *   remove the fingerprint reuse       1/24    expected to be EQUIVALENT -- the row
 *                                             is rewritten in place with the same
 *                                             values and keeps its node. It is not,
 *                                             and one case says why: `comment` is
 *                                             NOT in the fingerprint, so a reused
 *                                             row keeps its old `title` while a
 *                                             rewritten one picks up the new
 *                                             comment. The case that catches it was
 *                                             written for the rule, not the mutant.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/iface-list-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc } = require('./lib/dom-shim.js');
const { makeTree, serialise } = require('./lib/tree-shim.js');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/iface-list-check.js --freeze
const G = L.golden('iface-list-check');

const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT);

// `ifstatus:update` has THREE subscribers -- this page, the topology view and
// the bandwidth page. Selected by content; the bare anchor is refused.
const HANDLER = G.value('HANDLER', () => L.handler(src, 'ifstatus:update', { contains: 'ifaceGrid' }));
assert.ok(HANDLER.includes('renderIfaceList'), 'the lifted handler no longer renders the list');

const LIVE_FNS = [
  L.line(src, 'function esc('),
  L.whole(src, 'function fmtBytes('),
  L.whole(src, 'function fmtMbps('),
  L.whole(src, 'function portSvg('),
  L.whole(src, 'function renderIfPorts('),
  L.whole(src, 'function renderIfTypes('),
  L.whole(src, 'function ifaceSparkSvg('),
  L.whole(src, 'function ifaceRateRow('),
  L.whole(src, 'function renderIfaceList('),
  L.whole(src, 'function iflSortRows('),
  L.whole(src, 'function ifTypePill('),
  L.whole(src, 'function iflCounter('),
  L.whole(src, 'function iflBytes('),
  L.whole(src, 'function iflLastUp('),
].join('\n');

const ELEMENT_NAMES = G.value('ELEMENT_NAMES', () => L.fileScopeEls(src, HANDLER + ' ifaceGrid ifaceCount ifaceTypeFilter'))
  .map((e) => e.name);

// ONLY the list body.
//
// `ifaceListWrap` and `ifaceCardSize` are the VIEW SWITCH, and this gate does
// not drive it symmetrically: the port reaches list view through its real path
// (a saved size in `localStorage`), while the live side is put there by setting
// `_ifaceView` in the preamble -- the same treatment `interfaces-page-check`
// already gives the type filter. Poking state on one side and driving a control
// on the other is fine for GETTING to the state under test, and is not evidence
// about the switch itself. So the switch stays uncovered and belongs to whoever
// ports its own unit; `ifaceGrid` stays with the tile grid for the same reason.
const COVERS = ['ifaceListBody'];
if (process.argv.includes('--ids')) { console.log(JSON.stringify(COVERS)); process.exit(0); }

const IDS = ['ifaceCount', 'ifTypeGrid', 'ndWiredCount', 'ifaceGrid', 'ifPortsPanel',
  'ifaceTypeFilter', 'ifaceSelect', 'ifaceListBody', 'ifaceListWrap', 'ifaceCardSize'];

const ENTRY = path.join(ROOT, 'testdata', '.ifl-entry.ts');
fs.writeFileSync(ENTRY, "export { initInterfacesPage } from '../web/src/pages/interfaces.js';\n");
const OUT = path.join(ROOT, 'testdata', '.ifl-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

/**
 * A document whose `#ifaceListBody` models identity, and whose tbody counts the
 * `appendChild` calls that MOVED a node it already held.
 */
function newDoc(filter) {
  const doc = makeDoc(IDS, {});
  const tree = makeTree();
  const tbody = tree.mk('tbody');
  const realAppend = tbody.appendChild;
  const moves = { n: 0 };
  tbody.appendChild = (c) => {
    if (c.parentNode === tbody) moves.n++;
    return realAppend(c);
  };
  const base = doc.getElementById.bind(doc);
  const wrapped = Object.assign(Object.create(doc), {
    createElement: (tag) => tree.mk(tag),
    getElementById: (id) => (id === 'ifaceListBody' ? tbody : base(id)),
  });
  wrapped.nodes = doc.nodes;
  wrapped.unknown = doc.unknown;
  if (filter) doc.nodes.ifaceTypeFilter.value = filter;
  return { doc: wrapped, tbody, moves };
}

/**
 * One frame, on three axes.
 *
 * The PICTURE drops serials: two implementations number their own nodes and
 * should not fail on the numbering. IDENTITY is carried by `serials` instead,
 * which is only ever read as a DIFFERENCE against the previous frame -- so it
 * compares across implementations even though the numbers never will.
 */
function frame(tbody, moves) {
  const rows = tbody.querySelectorAll('tr[data-iface]');
  return {
    picture: serialise(tbody, { serials: false }),
    order: rows.map((r) => r.dataset.iface),
    serials: rows.map((r) => [r.dataset.iface, r.serial]),
    moves: moves.n,
  };
}

/** new / reused / rebuilt, derived from the serial deltas between two frames. */
function verdicts(prev, cur) {
  const was = new Map(prev ? prev.serials : []);
  return cur.serials.map(([name, s]) => {
    if (!was.has(name)) return name + ':new';
    return name + (was.get(name) === s ? ':kept' : ':replaced');
  });
}

/** The comparable shape of a whole SEQUENCE. */
function reduce(frames) {
  return JSON.stringify(frames.map((f, i) => ({
    order: f.order,
    picture: f.picture,
    verdicts: verdicts(i ? frames[i - 1] : null, f),
    // The moves THIS frame made, not the running total.
    moved: f.moves - (i ? frames[i - 1].moves : 0),
  })), null, 1);
}

function liveRun(frames, filter) {
  const { doc, tbody, moves } = newDoc(filter);
  const ctx = {
    String, Array, Math, Number, Object, JSON, Map, parseInt, parseFloat, isFinite,
    document: doc, setTimeout: (fn) => { fn(); return 0; }, clearTimeout: () => {}, window: {},
    __run: null,
  };
  vm.createContext(ctx);
  vm.runInContext([
    LIVE_FNS,
    'function $(id){return document.getElementById(id);}',
    L.fileScopeVars(src, HANDLER + LIVE_FNS, ELEMENT_NAMES),
    L.whole(src, 'var IF_TYPE_COLOURS'),
    L.whole(src, 'var IF_TYPE_FALLBACKS'),
    // The sort-column table: another multi-line declaration `fileScopeVars`
    // deliberately will not capture. Lifted whole, not retyped -- it decides
    // which value each column sorts on and whether it collates as text.
    L.whole(src, 'var IFL_COLS'),
    // The sort STATE, lifted rather than written here so the default is the
    // live one. `{ key: '', dir: 1 }` means NO sort, which is what a viewer sees
    // before touching a header -- and it is why payload order reaches the DOM
    // directly, which is what the reorder cases below depend on.
    L.line(src, 'var _iflSort'),
    'var _ifaceTypeFilter = ' + JSON.stringify(filter || '') + ';',
    'var _iflOrder = "";',
    // LIST VIEW, or this gate tests nothing. The handler renders the list only
    // `if (_ifaceView === 'list')`, and the default is 'sm' -- the tile grid. A
    // first version of this gate missed that and 23 sequences passed against an
    // empty tbody on both sides; the believability block below is what caught it,
    // which is exactly the job it was added for.
    "var _ifaceView = 'list';",
    L.declare(L.fileScopeEls(src, HANDLER + ' ifaceGrid ifaceCount ifaceTypeFilter')),
    '__run = function (data) {' + HANDLER + '};',
  ].join('\n'), ctx);
  const out = [];
  for (const p of frames) { ctx.__run(p); out.push(frame(tbody, moves)); }
  return reduce(out);
}

function portRun(frames, filter) {
  const { doc, tbody, moves } = newDoc(filter);
  const handlers = {};
  const prev = { doc: globalThis.document, win: globalThis.window, ls: globalThis.localStorage };
  globalThis.document = doc;
  globalThis.window = {};
  // The port reaches list view the way a returning viewer does: it reads the
  // size it saved last time. That is its real path, not a poke.
  globalThis.localStorage = { getItem: () => 'list', setItem() {}, removeItem() {} };
  try {
    delete require.cache[require.resolve(OUT)];
    require(OUT).initInterfacesPage({ on: (ev, fn) => { handlers[ev] = fn; }, emit() {} },
      () => true);
    assert.ok(handlers['ifstatus:update'], 'the port registered no ifstatus:update handler');
    // EXACTLY ONE lookup goes unanswered, and it is named rather than tolerated.
    // `.iface-list thead` is the delegated SORT-HEADER binding: both sides do the
    // identical `document.querySelector` for it, the port guards it with `?.`,
    // and no case here clicks a header. Pinning the set rather than raising the
    // threshold means a SECOND unanswered lookup still fails -- the assertion
    // stays a measurement of the shim's completeness instead of becoming a
    // formality that grows a little every time it is in the way.
    assert.deepEqual([...doc.unknown], ['.iface-list thead'],
      'the port looked up ids this gate does not provide: ' + [...doc.unknown].join(', '));
    if (filter) doc.nodes.ifaceTypeFilter.fire('change');
    const out = [];
    for (const p of frames) { handlers['ifstatus:update'](p); out.push(frame(tbody, moves)); }
    return reduce(out);
  } finally {
    for (const [k, g] of [['doc', 'document'], ['win', 'window'], ['ls', 'localStorage']]) {
      if (prev[k] === undefined) delete globalThis[g]; else globalThis[g] = prev[k];
    }
  }
}

const I = (o) => Object.assign({
  name: 'ether1', type: 'ether', running: true, disabled: false,
  ips: ['198.51.100.1/24'], mac: '02:00:00:00:00:01', mtu: 1500,
  rxMbps: 12.5, txMbps: 3.25, rxBytes: 1048576, txBytes: 2097152,
  errors: 0, drops: 0, errorsDelta: 0, dropsDelta: 0, linkDowns: 0, lastLinkUp: '',
  comment: '',
}, o);
const P = (list) => ({ interfaces: list });
const E = (n, o) => I(Object.assign({ name: n }, o));

const A = E('ether1'), B = E('ether2'), C = E('ether3');

const CASES = {
  // ---- one frame: the picture, which the string gate could already hold -----
  'empty': [[P([])], ''],
  'one row': [[P([A])], ''],
  'three rows': [[P([A, B, C])], ''],

  // ---- two frames: the REUSE rule -----------------------------------------
  'an identical poll keeps every node': [[P([A, B]), P([A, B])], ''],
  'a changed rate replaces nothing but rewrites the row':
    [[P([A, B]), P([E('ether1', { rxMbps: 99 }), B])], ''],
  'a row that appears is new, the others are kept': [[P([A, B]), P([A, B, C])], ''],
  'a row that vanishes leaves the rest kept': [[P([A, B, C]), P([A, B])], ''],
  'every field that feeds the fingerprint': [[P([A]), P([E('ether1', { errors: 3 })]),
    P([E('ether1', { errors: 3, drops: 1 })]), P([E('ether1', { errors: 3, drops: 1, linkDowns: 2 })])], ''],
  'an address change rewrites the row': [[P([A]), P([E('ether1', { ips: ['198.51.100.7/24'] })])], ''],
  'a state change rewrites the row': [[P([A]), P([E('ether1', { running: false })])], ''],
  'a comment change does NOT rewrite the row -- it is not in the fingerprint':
    [[P([A]), P([E('ether1', { comment: 'uplink' })])], ''],

  // ---- the EMPTY STATE, which throws every node away ------------------------
  'going empty and back builds fresh nodes': [[P([A, B]), P([]), P([A, B])], ''],
  'empty twice': [[P([A]), P([]), P([])], ''],

  // ---- the REORDER rule, which is what move counts are for ------------------
  'a stable order moves nothing': [[P([A, B, C]), P([A, B, C]), P([A, B, C])], ''],
  'a reversed order re-appends every row': [[P([A, B, C]), P([C, B, A])], ''],
  'one swap': [[P([A, B]), P([B, A])], ''],
  'order restored': [[P([A, B, C]), P([C, B, A]), P([A, B, C])], ''],
  'a new row at the front changes the order key': [[P([B, C]), P([A, B, C])], ''],

  // ---- the filter, which changes the row SET between frames -----------------
  'filtering out a row': [[P([A, E('bridge1', { type: 'bridge' })])], 'ether'],
  'a filter that matches nothing': [[P([A])], 'wg'],
  'a filter, then a poll': [[P([A, E('bridge1', { type: 'bridge' })]),
    P([A, E('bridge1', { type: 'bridge' })])], 'ether'],

  // ---- escaping, in a renderer that now also has to survive being reused ----
  'markup in a name': [[P([E('<img src=x>')]), P([E('<img src=x>')])], ''],
  'a quote in a name': [[P([E('a"b')]), P([E('a"b')])], ''],
  'markup in a comment': [[P([E('ether1', { comment: '<b>x</b>' })])], ''],
};

let bad = 0, checked = 0;
for (const [name, [frames, filter]] of Object.entries(CASES)) {
  const a = G.live(name, () => liveRun(frames, filter));
  const b = portRun(frames, filter);
  checked++;
  if (a === b) continue;
  bad++;
  if (bad <= 3) {
    shout('DIFF [' + name + ']');
    const A2 = JSON.parse(a), B2 = JSON.parse(b);
    for (let i = 0; i < Math.max(A2.length, B2.length); i++) {
      const x = JSON.stringify(A2[i]), y = JSON.stringify(B2[i]);
      if (x !== y) {
        shout('  frame ' + i + '\n    live: ' + String(x).slice(0, 400) +
          '\n    port: ' + String(y).slice(0, 400));
      }
    }
  }
}

// ---- BELIEVABILITY ---------------------------------------------------------
//
// Every case above compares two runs, and two renderers that built nothing would
// agree perfectly. So the LIVE side alone must show each of the three properties
// this gate exists for -- otherwise the cases are pinning an empty table.
{
  const kept = JSON.parse(G.live('auto:3', () => liveRun([P([A, B]), P([A, B])], '')));
  assert.deepEqual(kept[1].verdicts, ['ether1:kept', 'ether2:kept'],
    'the LIVE renderer rebuilt an unchanged row -- the fingerprint reuse this gate ' +
    'compares is not happening, so every "kept" case below proves nothing');
  assert.equal(kept[1].moved, 0, 'the LIVE renderer moved rows whose order did not change');

  const fresh = JSON.parse(G.live('auto:2', () => liveRun([P([A]), P([]), P([A])], '')));
  assert.deepEqual(fresh[2].verdicts, ['ether1:new'],
    'the LIVE empty state did not throw its nodes away');

  const moved = JSON.parse(G.live('auto:1', () => liveRun([P([A, B, C]), P([C, B, A])], '')));
  assert.ok(moved[1].moved > 0, 'the LIVE renderer re-appended nothing on a reorder');
  assert.deepEqual(moved[1].order, ['ether3', 'ether2', 'ether1'],
    'the LIVE reorder did not reach the DOM');
  assert.deepEqual(moved[1].verdicts, ['ether3:kept', 'ether2:kept', 'ether1:kept'],
    'the LIVE reorder REBUILT rows instead of moving them, which is the churn ' +
    'the order key exists to avoid');
}

fs.rmSync(OUT, { force: true });
if (bad) {
  shout('\niface-list-check: ' + bad + ' of ' + checked + ' cases differ');
  process.exit(1);
}
console.log('iface-list-check: ' + checked + ' frame sequences identical ' +
  '(picture, node identity and move counts)');
