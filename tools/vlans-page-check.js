'use strict';
/**
 * The VLANs page, live against ported.
 *
 * The first gate built on `tools/lib/lift.js`, which carries the four things
 * every previous page gate got wrong at least once: bounding a region by what it
 * EXCLUDES, brace-matching a function rather than cutting at the first `}`,
 * finding element ids in all three spellings, and re-declaring the file-scope
 * element vars a region merely references.
 *
 * ── WHAT THIS PAGE ADDS OVER THE OTHERS ─────────────────────────────────────
 *
 * A VLAN that exists only at layer 2 — membership through a bridge port's pvid,
 * with no `/interface/vlan` row — has nothing to edit, so it gets NO data-id and
 * is deliberately not clickable. That is `resRow(i0 && i0.id, …)` on a possibly
 * empty `interfaces` array, and it is the kind of thing a corpus of well-formed
 * rows never reaches.
 *
 * `null` rates mean "the router did not report a rate", which is NOT idle. Both
 * are in the corpus, separately.
 *
 * WHAT IT CANNOT SEE: layout, focus, the sparkline geometry beyond its markup.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/vlans-page-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc, withDocument } = require('./lib/dom-shim.js');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/vlans-page-check.js --freeze
const G = L.golden('vlans-page-check');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT);

const iife = G.value('iife', () => L.region(src, {
  banner: '/* ── VLANs page',
  must: ['vlansBridgeTable', 'No VLANs configured on this router.'],
  mustNot: ['DNS page', 'Queues page', 'backupsPage', 'dnsSettingsBody', 'qSimpleTable'],
}));
const IDS = G.value('IDS', () => L.idsFor(src, iife));

// Declare what this gate provides, for `tools/element-coverage-audit.js`. Placed
// BEFORE the bundle step so asking costs nothing: a text scan cannot see ids
// derived at runtime, and guessing at them is what the audit exists to stop.
if (process.argv.includes('--ids')) { console.log(JSON.stringify(IDS)); process.exit(0); }

const FILE_ELS = G.value('FILE_ELS', () => L.fileScopeEls(src, iife));

const ENTRY = path.join(ROOT, 'testdata', '.vl-entry.ts');
fs.writeFileSync(ENTRY, "export { initVlansPage } from '../web/src/pages/vlans.js';\n");
const OUT = path.join(ROOT, 'testdata', '.vl-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const snap = (doc) => {
  const n = doc.nodes;
  const g = (id) => (n[id] ? { h: n[id].innerHTML, t: n[id].textContent } : null);
  return JSON.stringify({
    table: g('vlansTable'), thead: g('vlansThead'), bridge: g('vlansBridgeTable'),
    badge: g('vlansBadge'), badgeCls: n.vlansBadge ? n.vlansBadge.className : null,
    bridgeBadge: g('vlansBridgeBadge'), dynChip: g('vlansDynChip'),
    sum: ['vlSumCount', 'vlSumTagged', 'vlSumUntagged', 'vlSumRate'].map(g),
  });
};

function clickHeaders(doc, clicks) {
  for (const i of clicks || []) {
    const cells = doc.nodes.vlansThead.querySelectorAll('th');
    if (!cells[i]) throw new Error('no header cell at index ' + i);
    cells[i].click();
  }
}

function liveRun(payload, opts) {
  const o = opts || {};
  const doc = makeDoc(IDS, {});
  if (o.query) doc.nodes.vlansSearch.value = o.query;
  if (o.showDynamic) doc.nodes.vlansShowDynamic.checked = true;
  const handlers = {};
  const ctx = {
    String, Array, Math, Number, Object, Set, JSON, parseInt, parseFloat,
    document: doc,
    socket: { on: (ev, fn) => { handlers[ev] = fn; }, emit() {} },
    setTimeout: () => 0, clearTimeout: () => {}, window: {},
  };
  vm.createContext(ctx);
  vm.runInContext([
    L.line(src, 'function esc('),
    L.whole(src, 'function fmtMbps('),
    L.whole(src, 'function _sortMul('),
    L.whole(src, 'function resRow('),
    L.whole(src, 'function _renderSortHeader('),
    'function $(id){return document.getElementById(id);}',
    'function pageVisible(){return true;}',
    'function _debounce(fn){return fn;}',
    L.declare(FILE_ELS),
    '(function () {' + iife + '})();',
  ].join('\n'), ctx);
  if (!handlers['vlans:update']) {
    throw new Error('the live page registered no handler; ids it wanted and this gate does not ' +
      'provide: ' + ([...doc.unknown].join(', ') || 'none'));
  }
  handlers['vlans:update'](payload);
  clickHeaders(doc, o.clicks);
  return snap(doc);
}

function portRun(payload, opts) {
  const o = opts || {};
  const doc = makeDoc(IDS, {});
  if (o.query) doc.nodes.vlansSearch.value = o.query;
  if (o.showDynamic) doc.nodes.vlansShowDynamic.checked = true;
  const handlers = {};
  const prevWin = globalThis.window;
  globalThis.window = {};
  try {
    return withDocument(doc, () => {
      delete require.cache[require.resolve(OUT)];
      require(OUT).initVlansPage({ on: (ev, fn) => { handlers[ev] = fn; }, emit() {} }, () => true);
      if (!handlers['vlans:update']) throw new Error('the port registered no vlans:update handler');
      handlers['vlans:update'](payload);
      clickHeaders(doc, o.clicks);
      return snap(doc);
    });
  } finally {
    if (prevWin === undefined) delete globalThis.window; else globalThis.window = prevWin;
  }
}

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (a === b) return;
  bad++;
  if (bad <= 3) {
    const A = JSON.parse(a), B = JSON.parse(b);
    for (const k of Object.keys(A)) {
      const x = JSON.stringify(A[k]), y = JSON.stringify(B[k]);
      if (x !== y) shout('DIFF %s [%s]\n  live: %s\n  port: %s', what, k, x, y);
    }
  }
}

const IF = (o) => Object.assign({ id: '*1', name: 'vlan10', parent: 'bridge1', mtu: 1500 }, o);
const V = (o) => Object.assign({
  vlanId: 10, name: 'vlan10', interfaces: [IF({})], tagged: ['ether1'], untagged: ['ether2'],
  clients: 3, rxMbps: 1.5, txMbps: 2.5,
}, o);
const BV = (o) => Object.assign({
  bridge: 'bridge1', raw: '10', tagged: ['ether1'], untagged: ['ether2'], dynamic: false,
}, o);
const P = (o) => Object.assign({ vlans: [], bridgeVlans: [], dynamicCount: 0 }, o);

const CASES = {
  'no vlans': [P({}), {}],
  'one vlan': [P({ vlans: [V({})] }), {}],
  'several vlans': [P({ vlans: [V({}), V({ vlanId: 20, name: 'vlan20' })] }), {}],
  // THE LAYER-2-ONLY VLAN: no /interface/vlan row, so no data-id and no name.
  'a layer-2-only vlan has no interfaces': [P({ vlans: [V({ interfaces: [], name: '' })] }), {}],
  'a vlan with no name but an interface': [P({ vlans: [V({ name: '' })] }), {}],
  'a vlan whose interface has no id': [P({ vlans: [V({ interfaces: [IF({ id: '' })] })] }), {}],
  'a vlan whose interface has no mtu': [P({ vlans: [V({ interfaces: [IF({ mtu: 0 })] })] }), {}],
  'a vlan whose interface has no parent': [P({ vlans: [V({ interfaces: [IF({ parent: '' })] })] }), {}],
  // Rates: null is NOT idle.
  'both rates null': [P({ vlans: [V({ rxMbps: null, txMbps: null })] }), {}],
  'rx null, tx a number': [P({ vlans: [V({ rxMbps: null })] }), {}],
  'tx null, rx a number': [P({ vlans: [V({ txMbps: null })] }), {}],
  'both rates zero is idle, not unknown': [P({ vlans: [V({ rxMbps: 0, txMbps: 0 })] }), {}],
  'no clients': [P({ vlans: [V({ clients: 0 })] }), {}],
  // `v.clients || 0` only differs from `v.clients` when the key is ABSENT, and
  // every corpus row had a number. A zero is not an absence — both are here.
  'a vlan with NO clients key at all': [P({ vlans: [V({ clients: undefined })] }), {}],
  'a vlan with a null clients count': [P({ vlans: [V({ clients: null })] }), {}],
  'a vlan with no ports': [P({ vlans: [V({ tagged: [], untagged: [] })] }), {}],
  'many tagged ports': [P({ vlans: [V({ tagged: ['ether1', 'ether2', 'ether3', 'sfp1'] })] }), {}],
  // Escaping.
  'markup in a vlan name': [P({ vlans: [V({ name: '<img src=x>' })] }), {}],
  'a quote in a port name': [P({ vlans: [V({ tagged: ['a"b'] })] }), {}],
  'markup in a parent': [P({ vlans: [V({ interfaces: [IF({ parent: '<b>br</b>' })] })] }), {}],
  // Search: id is a PREFIX match, name and ports are substring.
  'search by vlan id prefix': [P({ vlans: [V({ vlanId: 10 }), V({ vlanId: 200, name: 'v200' })] }), { query: '20' }],
  // THE NAME MUST NOT CONTAIN THE QUERY, or the row matches by name whichever
  // way the id is tested and the case proves nothing — which is exactly how the
  // substring mutation first survived. `v120` contained "20".
  'search by vlan id is a prefix, not a substring': [P({ vlans: [V({ vlanId: 120, name: 'aaa', tagged: [], untagged: [] })] }), { query: '20' }],
  'search by vlan id prefix matches the start': [P({ vlans: [V({ vlanId: 201, name: 'aaa', tagged: [], untagged: [] })] }), { query: '20' }],
  'search by name': [P({ vlans: [V({}), V({ vlanId: 20, name: 'other' })] }), { query: 'other' }],
  'search by a tagged port': [P({ vlans: [V({})] }), { query: 'ether1' }],
  'search by an untagged port': [P({ vlans: [V({})] }), { query: 'ether2' }],
  'search matching nothing': [P({ vlans: [V({})] }), { query: 'zzz' }],
  'search is trimmed and lowercased': [P({ vlans: [V({})] }), { query: '  VLAN10  ' }],
  // The bridge table, and the dynamic filter.
  'no bridge vlans': [P({ vlans: [V({})], bridgeVlans: [] }), {}],
  'one static bridge vlan': [P({ bridgeVlans: [BV({})], dynamicCount: 0 }), {}],
  'a dynamic bridge vlan is hidden by default': [P({ bridgeVlans: [BV({ dynamic: true })], dynamicCount: 1 }), {}],
  'a dynamic bridge vlan shown when asked': [P({ bridgeVlans: [BV({ dynamic: true })], dynamicCount: 1 }), { showDynamic: true }],
  'a mix, dynamic hidden': [P({ bridgeVlans: [BV({}), BV({ raw: '20', dynamic: true })], dynamicCount: 1 }), {}],
  'a mix, dynamic shown': [P({ bridgeVlans: [BV({}), BV({ raw: '20', dynamic: true })], dynamicCount: 1 }), { showDynamic: true }],
  'a bridge vlan range in raw': [P({ bridgeVlans: [BV({ raw: '10-20' })] }), {}],
  'markup in a bridge name': [P({ bridgeVlans: [BV({ bridge: '<b>br</b>' })] }), {}],
  // Summary: ports are DEDUPED across vlans, and a null rate contributes nothing.
  'summary dedupes ports across vlans': [P({ vlans: [
    V({ vlanId: 10, tagged: ['ether1'], untagged: ['ether2'] }),
    V({ vlanId: 20, name: 'v20', tagged: ['ether1'], untagged: ['ether3'] })] }), {}],
  'summary rate is a dash when every rate is null': [P({ vlans: [
    V({ rxMbps: null, txMbps: null })] }), {}],
  'summary rate counts one reported vlan': [P({ vlans: [
    V({ vlanId: 10, rxMbps: 1, txMbps: 2 }),
    V({ vlanId: 20, name: 'v20', rxMbps: null, txMbps: null })] }), {}],
  // Sorting.
  'sorted by the first column': [P({ vlans: [V({ vlanId: 20, name: 'b' }), V({ vlanId: 10, name: 'a' })] }), { clicks: [0] }],
  'first column descending': [P({ vlans: [V({ vlanId: 20, name: 'b' }), V({ vlanId: 10, name: 'a' })] }), { clicks: [0, 0] }],
  'sorted by name': [P({ vlans: [V({ vlanId: 20, name: 'aaa' }), V({ vlanId: 10, name: 'zzz' })] }), { clicks: [1] }],
  // Column 6 is Clients, and `sortVal` has its OWN `|| 0` fallback — a separate
  // site from the cell's, which the cell cases cannot reach. A row with no
  // clients key must sort as zero rather than dragging NaN through the compare.
  'sorted by clients, one row missing the key': [P({ vlans: [
    V({ vlanId: 10, name: 'a', clients: 5 }),
    V({ vlanId: 20, name: 'b', clients: undefined }),
    V({ vlanId: 30, name: 'c', clients: 2 })] }), { clicks: [6] }],
  'sorted by clients descending with a missing key': [P({ vlans: [
    V({ vlanId: 10, name: 'a', clients: 5 }),
    V({ vlanId: 20, name: 'b', clients: undefined }),
    V({ vlanId: 30, name: 'c', clients: 2 })] }), { clicks: [6, 6] }],
  'a sort survives a search': [P({ vlans: [V({ vlanId: 20, name: 'bvl' }), V({ vlanId: 10, name: 'avl' })] }), { query: 'vl', clicks: [1, 1] }],
};

for (const [name, [payload, opts]] of Object.entries(CASES)) {
  let a, b;
  try { a = G.live(name, () => liveRun(payload, opts)); } catch (e) { shout('LIVE THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  try { b = portRun(payload, opts); } catch (e) { shout('PORT THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  cmp(name, a, b);
}

// ── believability ──────────────────────────────────────────────────────────
{
  const s = JSON.parse(G.live('auto:5', () => liveRun(P({ vlans: [V({})], bridgeVlans: [BV({})], dynamicCount: 2 }), {})));
  assert.match(s.table.h, /wl-band-24/, 'the live vlan table rendered no row');
  assert.match(s.table.h, /data-id="\*1"/, 'the row lost its resource id');
  assert.match(s.bridge.h, /bridge1/, 'the bridge table rendered no row');
  assert.match(s.thead.h, /<th/, 'the sort header rendered nothing');
  assert.equal(s.badge.t, '1', 'the vlan badge is ' + s.badge.t);
  assert.match(s.badgeCls, /active-blue/, 'a non-empty table left the badge inactive');
  assert.equal(s.dynChip.t, '2', 'the dynamic chip is ' + s.dynChip.t);
}
{
  // The layer-2-only VLAN: no data-id, and a stated reason instead of a name.
  const s = JSON.parse(G.live('auto:4', () => liveRun(P({ vlans: [V({ interfaces: [], name: '' })] }), {})));
  assert.ok(!/data-id=/.test(s.table.h),
    'a layer-2-only VLAN got a data-id — it has no /interface/vlan row to edit: ' + s.table.h);
  assert.match(s.table.h, /no L3 interface/, 'the layer-2-only VLAN did not say why it has no name');
}
{
  const s = JSON.parse(G.live('auto:3', () => liveRun(P({}), {})));
  assert.match(s.table.h, /No VLANs configured on this router\./, 'the empty state did not render');
  assert.match(s.bridge.h, /No bridge VLAN entries\./, 'the bridge empty state did not render');
  assert.ok(!/active-blue/.test(s.badgeCls), 'an empty table left the badge active');
}
{
  // null is not idle.
  const unknown = JSON.parse(G.live('auto:2', () => liveRun(P({ vlans: [V({ rxMbps: null, txMbps: null })] }), {})));
  const idle = JSON.parse(G.live('auto:1', () => liveRun(P({ vlans: [V({ rxMbps: 0, txMbps: 0 })] }), {})));
  assert.match(unknown.table.h, /rates are unavailable/, 'a null rate did not say it was unavailable');
  assert.notEqual(unknown.table.h, idle.table.h, 'a null rate rendered the same as an idle one');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('vlans-page-check: %d cases identical', checked);
