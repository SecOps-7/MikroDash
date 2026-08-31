'use strict';
/**
 * The INTERFACES page's grid, type panel and counts, live against ported.
 *
 * `ifports-panel-check` covers the Ports panel; `element-coverage-audit` reported
 * the rest of the page — 9 of 10 elements — as uncovered.
 *
 * ── WHAT THIS GATE DOES NOT COVER, AND WHY IT IS MOST OF THE PAGE ──────────
 *
 * BOTH big renderers on this page build NODES rather than markup strings:
 *
 *   - `renderIfaceList` (#ifaceListBody) reads the existing `tr[data-iface]`
 *     nodes, fingerprints each row, rebuilds only the ones whose data changed,
 *     and re-appends to reorder.
 *   - The TILE GRID (#ifaceGrid) does the same — it calls `appendChild`, which
 *     is how this gate found out, three runs in.
 *
 * That is the right design: most interfaces are idle, and a full innerHTML sweep
 * every second would churn the DOM, break text selection and flicker on hover.
 * It needs `createElement`, `appendChild`, `dataset` and stable node identity.
 *
 * `tools/lib/dom-shim.js` stores markup as a STRING. Emulating node identity on
 * top of that would produce a harness sophisticated enough to be wrong in ways
 * nobody could see — worse than an honest gap. So this gate compares the TYPE
 * PANEL and the two COUNTS, and says so rather than implying more.
 *
 * **RESOLVED on 2026-08-25, and this note is kept rather than deleted because it
 * is the reasoning that produced the fix.** The decision written up here was
 * whether to take a DOM dependency. It was not needed: `tools/lib/tree-shim.js`
 * models node identity in this repo, and `tools/iface-list-check.js` now drives
 * `#ifaceListBody` with it — comparing reuse, replacement and MOVE COUNTS across
 * frames, which is what the list actually promises and what no string could hold.
 *
 * This gate is unchanged and covers neither renderer. The whole page is closed
 * now: the list by `iface-list-check.js`, the TILE GRID by
 * `iface-tiles-check.js` (both on `tree-shim`) and the VIEW SWITCH by
 * `iface-view-check.js`. `pages/interfaces` no longer appears in
 * `element-coverage-audit`'s PARTIAL list at all.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/interfaces-page-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc, withDocument } = require('./lib/dom-shim.js');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/interfaces-page-check.js --freeze
const G = L.golden('interfaces-page-check');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT);

// `ifstatus:update` has THREE subscribers — this page, the topology view and the
// bandwidth page. Selected by content; the bare anchor is refused.
const HANDLER = G.value('HANDLER', () => L.handler(src, 'ifstatus:update', { contains: 'ifaceGrid' }));
assert.ok(HANDLER.includes('renderIfTypes'), 'the lifted handler lost the type panel');

// The live helpers this handler calls, lifted whole. Hoisted to module scope so
// `fileScopeVars` can scan THEM for module state as well as the handler.
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
].join('\n');

// Element vars are declared separately by `L.declare`; excluded here so they are
// not emitted twice.
const ELEMENT_NAMES = G.value('ELEMENT_NAMES', () => L.fileScopeEls(src, HANDLER + ' ifaceGrid ifaceCount ifaceTypeFilter'))
  .map((e) => e.name);

const COMPARED = ['ifaceCount', 'ifTypeGrid', 'ndWiredCount'];
// COMPARED plus the TYPE FILTER, which cases set and whose effect is in the
// compared markup.
if (process.argv.includes('--ids')) {
  console.log(JSON.stringify(COMPARED.concat(['ifaceTypeFilter']))); process.exit(0);
}

// Every id either side touches, so neither returns early on a missing element.
// `ifaceGrid` and `ifaceListBody` are PROVIDED so neither side returns early,
// but not COMPARED — see the header.
const IDS = [...new Set([...COMPARED, 'ifaceGrid', 'ifPortsPanel', 'ifaceTypeFilter',
  'ifaceSelect', 'ifaceListBody', 'ifaceListWrap', 'ifaceCardSize'])];

const ENTRY = path.join(ROOT, 'testdata', '.if-entry.ts');
fs.writeFileSync(ENTRY, "export { initInterfacesPage } from '../web/src/pages/interfaces.js';\n");
const OUT = path.join(ROOT, 'testdata', '.if-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const snap = (doc) => {
  const n = doc.nodes;
  const out = {};
  for (const id of COMPARED) {
    out[id] = n[id] ? { h: n[id].innerHTML, t: n[id].textContent } : null;
  }
  return JSON.stringify(out);
};

function liveRun(payload, filter) {
  const doc = makeDoc(IDS, {});
  if (filter) doc.nodes.ifaceTypeFilter.value = filter;
  const ctx = {
    String, Array, Math, Number, Object, JSON, parseInt, parseFloat, isFinite,
    document: doc,
    setTimeout: (fn) => { fn(); return 0; }, clearTimeout: () => {}, window: {},
    __run: null,
  };
  vm.createContext(ctx);
  vm.runInContext([
    LIVE_FNS,
    'function $(id){return document.getElementById(id);}',
    // ALL the module state this code reaches, taken FROM THE SOURCE. These carry
    // defaults — `_ifaceView = 'sm'` is what a viewer sees before touching
    // anything — so a guessed value would test a configuration nobody starts in.
    // Derived rather than listed, because discovering them one ReferenceError at
    // a time is how three runs of this gate were spent.
    L.fileScopeVars(src, HANDLER + LIVE_FNS, ELEMENT_NAMES),
    // A multi-line table, which `fileScopeVars` deliberately will not capture —
    // a partial capture of a spanning declaration is worse than an obvious
    // absence, so the caller names it. Lifted whole, not retyped: it is the
    // colour every type badge is drawn from.
    L.whole(src, 'var IF_TYPE_COLOURS'),
    L.whole(src, 'var IF_TYPE_FALLBACKS'),
    'var _ifaceTypeFilter = ' + JSON.stringify(filter || '') + ';',
    'var _iflOrder = "";',
    L.declare(L.fileScopeEls(src, HANDLER + ' ifaceGrid ifaceCount ifaceTypeFilter')),
    '__run = function (data) {' + HANDLER + '};',
  ].join('\n'), ctx);
  ctx.__run(payload);
  return snap(doc);
}

function portRun(payload, filter) {
  const doc = makeDoc(IDS, {});
  if (filter) doc.nodes.ifaceTypeFilter.value = filter;
  const handlers = {};
  const prevWin = globalThis.window;
  globalThis.window = {};
  try {
    return withDocument(doc, () => {
      delete require.cache[require.resolve(OUT)];
      require(OUT).initInterfacesPage({ on: (ev, fn) => { handlers[ev] = fn; }, emit() {} }, () => true);
      if (!handlers['ifstatus:update']) throw new Error('the port registered no ifstatus:update handler');
      // The filter is read from the select on both sides; firing its change is
      // how a viewer sets it, and how the port learns.
      if (filter) doc.nodes.ifaceTypeFilter.fire('change');
      handlers['ifstatus:update'](payload);
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
      if (x !== y) shout('DIFF %s [%s]\n  live: %s\n  port: %s', what, k,
        String(x).slice(0, 380), String(y).slice(0, 380));
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
const P = (o) => Object.assign({ interfaces: [] }, o);

const CASES = {
  'no interfaces': [P({}), ''],
  'one interface': [P({ interfaces: [I({})] }), ''],
  'several of one type': [P({ interfaces: [I({}), I({ name: 'ether2' })] }), ''],
  'several types': [P({ interfaces: [
    I({}), I({ name: 'bridge1', type: 'bridge' }), I({ name: 'vlan10', type: 'vlan' })] }), ''],
  // The type panel counts by type and preserves INSERTION order, not
  // alphabetical — a router's own ordering is meaningful.
  'type order follows the payload': [P({ interfaces: [
    I({ name: 'vlan10', type: 'vlan' }), I({ name: 'ether1', type: 'ether' })] }), ''],
  'an interface with no type defaults to ether': [P({ interfaces: [I({ type: '' })] }), ''],
  'many of one type': [P({ interfaces: Array.from({ length: 6 }, (_, i) => I({ name: 'e' + i })) }), ''],
  // States.
  'a down interface': [P({ interfaces: [I({ running: false })] }), ''],
  'a disabled interface': [P({ interfaces: [I({ disabled: true })] }), ''],
  'disabled AND running': [P({ interfaces: [I({ disabled: true, running: true })] }), ''],
  // Addresses and rates.
  'no addresses': [P({ interfaces: [I({ ips: [] })] }), ''],
  'several addresses': [P({ interfaces: [I({ ips: ['198.51.100.1/24', '198.51.100.9/24'] })] }), ''],
  'zero rates': [P({ interfaces: [I({ rxMbps: 0, txMbps: 0 })] }), ''],
  'no mac': [P({ interfaces: [I({ mac: '' })] }), ''],
  'errors and drops': [P({ interfaces: [I({ errors: 5, drops: 2, errorsDelta: 1, dropsDelta: 1 })] }), ''],
  'a comment': [P({ interfaces: [I({ comment: 'uplink' })] }), ''],
  // The type filter.
  'filtered to ether': [P({ interfaces: [
    I({}), I({ name: 'bridge1', type: 'bridge' })] }), 'ether'],
  'filtered to bridge': [P({ interfaces: [
    I({}), I({ name: 'bridge1', type: 'bridge' })] }), 'bridge'],
  'filtered to a type nothing has': [P({ interfaces: [I({})] }), 'wg'],
  // Escaping.
  'markup in a name': [P({ interfaces: [I({ name: '<img src=x>' })] }), ''],
  'a quote in a name': [P({ interfaces: [I({ name: 'a"b' })] }), ''],
  'markup in a comment': [P({ interfaces: [I({ comment: '<b>x</b>' })] }), ''],
  'markup in a type': [P({ interfaces: [I({ type: '<i>t</i>' })] }), ''],
  // The wired count on the nav.
  'wired count with mixed types': [P({ interfaces: [
    I({}), I({ name: 'e2' }), I({ name: 'wlan1', type: 'wlan' })] }), ''],
};

for (const [name, [payload, filter]] of Object.entries(CASES)) {
  let a, b;
  try { a = G.live(name, () => liveRun(payload, filter)); } catch (e) { shout('LIVE THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  try { b = portRun(payload, filter); } catch (e) { shout('PORT THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  cmp(name, a, b);
}

// ── believability ──────────────────────────────────────────────────────────
{
  const s = JSON.parse(G.live('auto:3', () => liveRun(P({ interfaces: [I({}), I({ name: 'bridge1', type: 'bridge' })] }), '')));
  assert.match(s.ifTypeGrid.h, /if-type-item/, 'the type panel rendered nothing');
  assert.match(s.ifTypeGrid.h, /bridge/, 'the type panel is missing a type');
  assert.equal(s.ifaceCount.t, '2', 'the interface count is ' + s.ifaceCount.t);
}
{
  const s = JSON.parse(G.live('auto:2', () => liveRun(P({}), '')));
  assert.equal(s.ifaceCount.t, '0', 'an empty payload gave a count of ' + s.ifaceCount.t);
}
{
  // The filter really removes tiles.
  // The TYPE PANEL shows every type even under a filter, because it is how you
  // change the filter: hiding the types you filtered out would strand the viewer.
  const one = JSON.parse(G.live('auto:1', () => liveRun(P({ interfaces: [I({}), I({ name: 'bridge1', type: 'bridge' })] }), 'ether')));
  assert.match(one.ifTypeGrid.h, /bridge/,
    'the type panel followed the filter — it must show every type, or a viewer cannot get back');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('interfaces-page-check: %d cases identical', checked);
