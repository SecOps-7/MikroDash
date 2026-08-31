'use strict';
/**
 * THE INTERFACE TILE GRID, live against ported — the second node-building
 * renderer on this page, and the one `interfaces-page-check` found out about
 * "three runs in" when it called `appendChild`.
 *
 * `iface-list-check` closed the LIST with `tree-shim`. This closes the GRID,
 * which promises something different and in one way stricter.
 *
 * ---- WHAT A TILE PROMISES --------------------------------------------------
 *
 *   COLD START   the grid ships holding a "Waiting…" placeholder. The FIRST
 *                tile to be created clears it — and only then, which is why the
 *                flag is consumed rather than tested twice. A poll carrying no
 *                interfaces must NOT clear it.
 *   IN PLACE     an existing tile is never rebuilt. Its class, its dot, its
 *                spark, its address line and its rate rows are each written
 *                individually, and everything else is left alone. That is what
 *                stops the rate bars flashing every second.
 *   THE ADDRESS  LINE IS NEVER REMOVED. An interface without an address keeps a
 *                U+00A0 placeholder, because dropping the line makes the tile a
 *                line shorter than its neighbours and the whole row comes up
 *                short. Both sides carry that comment; this gate carries the
 *                case.
 *   NO REORDER   unlike the list, the grid NEVER re-appends. Tiles sit in
 *                creation order forever, so a payload that changes order does
 *                not move them. Pinned here because it looks like an omission
 *                and is the design.
 *
 * ---- WHY THE SPARK IS THE INTERESTING PART ---------------------------------
 *
 * A tile's sparkline is REPLACED, not rewritten: the new SVG is parsed into a
 * throwaway div and `replaceChild`d over the old one, so that ONE child gets a
 * new identity while the tile around it keeps its own. Nothing else in this port
 * does that, and it is invisible to markup — the picture after a replace is
 * identical to the picture after an in-place rewrite.
 *
 * Serial deltas are what tell them apart, and this gate reports a per-tile
 * verdict for the tile AND for its spark child.
 *
 * ---- MUTATIONS (2026-08-25): SEVEN KILLED, ONE EQUIVALENT ------------------
 *
 *   never clear the placeholder              25/26
 *   clear it for every new tile               8/26
 *   drop the U+00A0 address placeholder       1/26
 *   stop refreshing the type on a live tile   1/26
 *   never remove a vanished tile              2/26
 *   prepend new tiles instead of appending    8/26
 *   leave the status dot stale                1/26
 *
 *   EQUIVALENT — `replaceChild(fresh, old)` rewritten as
 *   `old.remove(); tile.insertAdjacentHTML('afterbegin', newSpark)`. Both give
 *   the spark a NEW node, so the serial changes either way and the verdict reads
 *   'replaced' for both; and because the spark IS the tile's first child,
 *   'afterbegin' lands it in the same position. There is no observable
 *   difference, in this gate or in a browser. It is recorded rather than dropped
 *   because it stops being equivalent the moment the spark is not first — which
 *   is a real thing a future edit could do.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/iface-tiles-check.js
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
// in lib/lift.js. Re-freeze with: node tools/iface-tiles-check.js --freeze
const G = L.golden('iface-tiles-check');

const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT);

const HANDLER = G.value('HANDLER', () => L.handler(src, 'ifstatus:update', { contains: 'ifaceGrid' }));
assert.ok(HANDLER.includes('iface-tile'), 'the lifted handler no longer builds tiles');

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

// The GRID. `ifaceCount` is written by the same handler and its value depends on
// the filter pass THIS gate drives, so it is claimed too — but the list, its
// wrapper and the size control are not: see `iface-list-check`'s note.
const COVERS = ['ifaceGrid'];
if (process.argv.includes('--ids')) { console.log(JSON.stringify(COVERS)); process.exit(0); }

const IDS = ['ifaceCount', 'ifTypeGrid', 'ndWiredCount', 'ifaceGrid', 'ifPortsPanel',
  'ifaceTypeFilter', 'ifaceSelect', 'ifaceListBody', 'ifaceListWrap', 'ifaceCardSize'];

// The grid's INITIAL contents, read from the markup this port serves rather than
// typed. `coldStart` is `!tiles && grid.querySelector('.empty-state')`, so a gate
// that started from an empty grid would never take that branch at all — and the
// placeholder is the only thing that makes it true.
const PAGE = fs.readFileSync(path.join(ROOT, 'web', 'src', 'ui', 'page-interfaces.html'), 'utf8');
const SEED = /<div class="iface-grid" id="ifaceGrid">([\s\S]*?)<\/div>\s*$/m
  .exec(PAGE.split('\n').find((l) => l.includes('id="ifaceGrid"')) || '');
assert.ok(SEED && SEED[1].includes('empty-state'),
  'page-interfaces.html no longer ships #ifaceGrid holding an empty-state placeholder — ' +
  'the cold-start branch this gate drives depends on it');

const ENTRY = path.join(ROOT, 'testdata', '.ift-entry.ts');
fs.writeFileSync(ENTRY, "export { initInterfacesPage } from '../web/src/pages/interfaces.js';\n");
const OUT = path.join(ROOT, 'testdata', '.ift-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

function newDoc(filter) {
  const doc = makeDoc(IDS, {});
  const tree = makeTree();
  const grid = tree.mk('div');
  grid.className = 'iface-grid';
  grid.innerHTML = SEED[1];
  const base = doc.getElementById.bind(doc);
  const wrapped = Object.assign(Object.create(doc), {
    createElement: (tag) => tree.mk(tag),
    getElementById: (id) => (id === 'ifaceGrid' ? grid : base(id)),
  });
  wrapped.nodes = doc.nodes;
  wrapped.unknown = doc.unknown;
  if (filter) doc.nodes.ifaceTypeFilter.value = filter;
  return { doc: wrapped, grid };
}

/**
 * One frame: the picture, plus the identity of each tile AND of its spark.
 *
 * The spark is separate on purpose — `replaceChild` gives that one child a new
 * serial while the tile keeps its own, and no markup comparison can see it.
 */
function frame(grid) {
  const tiles = grid.querySelectorAll('.iface-tile[data-iface]');
  return {
    picture: serialise(grid, { serials: false }),
    order: tiles.map((t) => t.dataset.iface),
    serials: tiles.map((t) => {
      const spark = t.querySelector('.iface-spark');
      return [t.dataset.iface, t.serial, spark ? spark.serial : null];
    }),
  };
}

function verdicts(prev, cur) {
  const wasTile = new Map((prev ? prev.serials : []).map(([n, s]) => [n, s]));
  const wasSpark = new Map((prev ? prev.serials : []).map(([n, , s]) => [n, s]));
  return cur.serials.map(([name, s, sp]) => {
    const tile = !wasTile.has(name) ? 'new' : wasTile.get(name) === s ? 'kept' : 'replaced';
    let spark;
    if (sp === null) spark = 'none';
    else if (!wasSpark.has(name) || wasSpark.get(name) === null) spark = 'new';
    else spark = wasSpark.get(name) === sp ? 'kept' : 'replaced';
    return name + ':' + tile + '/spark:' + spark;
  });
}

function reduce(frames, doc) {
  return JSON.stringify(frames.map((f, i) => ({
    order: f.order,
    picture: f.picture,
    verdicts: verdicts(i ? frames[i - 1] : null, f),
  })).concat([{ count: doc.nodes.ifaceCount.textContent }]), null, 1);
}

function liveRun(frames, filter) {
  const { doc, grid } = newDoc(filter);
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
    L.whole(src, 'var IFL_COLS'),
    L.line(src, 'var _iflSort'),
    'var _ifaceTypeFilter = ' + JSON.stringify(filter || '') + ';',
    'var _iflOrder = "";',
    // TILE VIEW is the LIVE DEFAULT ('sm'), so nothing is poked here — unlike
    // `iface-list-check`, which has to reach a state a viewer chose.
    L.declare(L.fileScopeEls(src, HANDLER + ' ifaceGrid ifaceCount ifaceTypeFilter')),
    '__run = function (data) {' + HANDLER + '};',
  ].join('\n'), ctx);
  // WHAT THE LIVE MOUNT DOES TO THE GRID, replicated because this gate lifts the
  // HANDLER and not the mount. `apply(saved)` runs at startup with the default
  // 'sm' and stamps it on the grid, so a returning viewer's card scale survives a
  // trip through list view. The port does it for real at mount; without this the
  // two differ by one attribute on every single frame.
  //
  // It is set HERE rather than seeded into both grids on purpose: seeding both
  // would also hide the port DROPPING it, and this way the live value is what the
  // port is compared against.
  grid.dataset.size = 'sm';
  const out = [];
  for (const p of frames) { ctx.__run(p); out.push(frame(grid)); }
  return reduce(out, doc);
}

function portRun(frames, filter) {
  const { doc, grid } = newDoc(filter);
  const handlers = {};
  const prev = { doc: globalThis.document, win: globalThis.window, ls: globalThis.localStorage };
  globalThis.document = doc;
  globalThis.window = {};
  globalThis.localStorage = { getItem: () => 'sm', setItem() {}, removeItem() {} };
  try {
    delete require.cache[require.resolve(OUT)];
    require(OUT).initInterfacesPage({ on: (ev, fn) => { handlers[ev] = fn; }, emit() {} },
      () => true);
    assert.ok(handlers['ifstatus:update'], 'the port registered no ifstatus:update handler');
    // As in `iface-list-check`: exactly one unanswered lookup, named rather than
    // tolerated, so a second one still fails.
    assert.deepEqual([...doc.unknown], ['.iface-list thead'],
      'the port looked up ids this gate does not provide: ' + [...doc.unknown].join(', '));
    if (filter) doc.nodes.ifaceTypeFilter.fire('change');
    const out = [];
    for (const p of frames) { handlers['ifstatus:update'](p); out.push(frame(grid)); }
    return reduce(out, doc);
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
  // ---- the COLD START, which only the shipped placeholder can reach ---------
  'the very first tile clears the placeholder': [[P([A])], ''],
  'a poll with NO interfaces leaves the placeholder alone': [[P([])], ''],
  'empty, then interfaces': [[P([]), P([A])], ''],
  'two tiles on the first frame': [[P([A, B])], ''],

  // ---- tiles are updated IN PLACE ------------------------------------------
  'an identical poll keeps every tile': [[P([A, B]), P([A, B])], ''],
  'a rate change keeps the tile': [[P([A]), P([E('ether1', { rxMbps: 80 })])], ''],
  'a state change keeps the tile': [[P([A]), P([E('ether1', { running: false })])], ''],
  'a type change keeps the tile': [[P([A]), P([E('ether1', { type: 'bridge' })])], ''],
  'a tile that appears is new': [[P([A]), P([A, B])], ''],
  'a tile that vanishes is removed': [[P([A, B]), P([A])], ''],

  // ---- the SPARK, which is REPLACED rather than rewritten -------------------
  // FOUR polls, not three: no spark after poll 1, INSERTED at poll 2, REPLACED
  // at 3 and 4. Three frames would exercise the insert once and the replace once;
  // the fourth is what shows a replace following a replace.
  'the spark across four polls': [[P([A]), P([A]), P([A]), P([A])], ''],
  'a spark on a tile whose rate keeps moving':
    [[P([E('ether1', { rxMbps: 1 })]), P([E('ether1', { rxMbps: 40 })]),
      P([E('ether1', { rxMbps: 90 })])], ''],
  'a tile at zero throughout': [[P([E('ether1', { rxMbps: 0, txMbps: 0 })]),
    P([E('ether1', { rxMbps: 0, txMbps: 0 })])], ''],

  // ---- the ADDRESS LINE, which is never removed ----------------------------
  'no address at all': [[P([E('ether1', { ips: [] })])], ''],
  'an address that goes away': [[P([A]), P([E('ether1', { ips: [] })])], ''],
  'an address that appears': [[P([E('ether1', { ips: [] })]), P([A])], ''],
  'an address that changes': [[P([A]), P([E('ether1', { ips: ['198.51.100.9/24'] })])], ''],
  'only the FIRST address is shown':
    [[P([E('ether1', { ips: ['198.51.100.1/24', '198.51.100.2/24'] })])], ''],

  // ---- NO REORDER: tiles keep creation order -------------------------------
  'a reversed payload does NOT reorder the tiles': [[P([A, B, C]), P([C, B, A])], ''],
  'a tile re-appearing goes to the END': [[P([A, B]), P([A]), P([A, B])], ''],

  // ---- the type FILTER, which hides rather than removes ---------------------
  'filtered to ether': [[P([A, E('bridge1', { type: 'bridge' })])], 'ether'],
  'a filter matching nothing': [[P([A])], 'wg'],
  'a filter across two polls': [[P([A, E('bridge1', { type: 'bridge' })]),
    P([A, E('bridge1', { type: 'bridge' })])], 'ether'],

  // ---- escaping ------------------------------------------------------------
  'markup in a name': [[P([E('<img src=x>')])], ''],
  'a quote in a name': [[P([E('a"b')])], ''],
  'a comment on a tile': [[P([E('ether1', { comment: '<b>x</b>' })])], ''],
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
        shout('  frame ' + i + '\n    live: ' + String(x).slice(0, 420) +
          '\n    port: ' + String(y).slice(0, 420));
      }
    }
  }
}

// ---- BELIEVABILITY ---------------------------------------------------------
//
// `iface-list-check` passed 23 sequences against an EMPTY tbody on both sides
// before this block existed there. Every property this gate claims is therefore
// asserted on the LIVE side ALONE.
{
  const cold = JSON.parse(G.live('auto:5', () => liveRun([P([A])], '')));
  assert.ok(JSON.stringify(cold[0].picture).includes('iface-tile'),
    'the LIVE tile grid built no tiles — every case here is comparing two empty grids');
  assert.ok(!JSON.stringify(cold[0].picture).includes('empty-state'),
    'the LIVE cold start did not clear the placeholder');

  const idle = JSON.parse(G.live('auto:4', () => liveRun([P([]), P([])], '')));
  assert.ok(JSON.stringify(idle[0].picture).includes('empty-state'),
    'a LIVE poll with no interfaces cleared the placeholder, which is the cold-start bug ' +
    'the flag exists to avoid');

  const kept = JSON.parse(G.live('auto:3', () => liveRun([P([A, B]), P([A, B]), P([A, B])], '')));
  assert.deepEqual(kept[1].verdicts.map((v) => v.split(':')[1].split('/')[0]), ['kept', 'kept'],
    'the LIVE grid rebuilt an unchanged tile');

  // THE SPARK TAKES TWO DIFFERENT PATHS, and asserting only one of them was
  // wrong. `ifaceSparkSvg` returns '' until the history holds TWO points, so a
  // tile has no spark after its first poll. The SECOND poll therefore INSERTS
  // one (`insertAdjacentHTML('afterbegin')`, no old node to replace) and only
  // the THIRD replaces it (`replaceChild`). A two-frame assertion demanded
  // 'replaced' on the frame that inserts, and failed — correctly.
  assert.ok(kept[1].verdicts.every((v) => v.endsWith('/spark:new')),
    'the LIVE spark did not APPEAR on the second poll — the insert path is not being taken');
  assert.ok(kept[2].verdicts.every((v) => v.endsWith('/spark:replaced')),
    'the LIVE spark was not REPLACED on the third poll — the one identity change this gate ' +
    'exists to see is not happening, so the spark cases prove nothing');
  assert.deepEqual(kept[2].verdicts.map((v) => v.split(':')[1].split('/')[0]), ['kept', 'kept'],
    'replacing the spark also replaced the TILE — the point is that it does not');

  const order = JSON.parse(G.live('auto:2', () => liveRun([P([A, B, C]), P([C, B, A])], '')));
  assert.deepEqual(order[1].order, ['ether1', 'ether2', 'ether3'],
    'the LIVE tile grid REORDERED — it is not supposed to, and the case pinning that is wrong');

  const ip = JSON.parse(G.live('auto:1', () => liveRun([P([A]), P([E('ether1', { ips: [] })])], '')));
  assert.ok(JSON.stringify(ip[1].picture).includes('iface-ip'),
    'the LIVE grid dropped the address line when the address went away — the placeholder rule');
}

fs.rmSync(OUT, { force: true });
if (bad) {
  shout('\niface-tiles-check: ' + bad + ' of ' + checked + ' cases differ');
  process.exit(1);
}
console.log('iface-tiles-check: ' + checked + ' frame sequences identical ' +
  '(picture, tile identity and spark identity)');
