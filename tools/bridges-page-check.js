'use strict';
/**
 * The BRIDGES page, live against ported. Built on `tools/lib/lift.js`.
 *
 * Three tables in one page — bridges, their ports, and the learned host table —
 * plus four summary counters and a tab bar that decides which is visible.
 *
 * ── WHAT THIS PAGE CONTRIBUTES ──────────────────────────────────────────────
 *
 * The sort comparator is a per-column FUNCTION rather than a field name, and the
 * columns it handles are heterogeneous: `ports` sorts on a count, `rate` on the
 * SUM of rx and tx, `vlan` and `igmp` on booleans coerced to 1/0, and the rest
 * lexicographically on a lowercased string. Each rung is a separate way to be
 * wrong, so each is a case.
 *
 * ── ONE EQUIVALENT MUTANT, WITH THE REASON ─────────────────────────────────
 *
 * `setTab` ends `if (data) render();` on BOTH sides, and deleting it from the
 * port survives every case here. That is not a hole: `render()` does not read
 * `tab` — it redraws all three tables regardless — so on a switch it recomputes
 * exactly what is already on screen. The re-render is redundant in both
 * implementations, and reproducing a redundancy is what a port is for. A case
 * that could tell the difference would have to be one where `render()` began
 * depending on the tab, and then this note is the thing to delete.
 *
 * The host table is the one that grows: a bridge with a hundred learned MACs is
 * ordinary, so its search and its badge count are what an operator actually
 * uses.
 *
 * ── TWO EQUIVALENT MUTANTS, WITH EVIDENCE ──────────────────────────────────
 *
 * Dropping `.toLowerCase()` from the `proto` or `mac` comparator survives, and
 * that is equivalent rather than untested: the comparator ends in
 * `localeCompare`, which already orders case-insensitively in this locale —
 * `'RSTP'.localeCompare('none')` and `'rstp'.localeCompare('none')` both return
 * 1. The lowercasing would matter if the values were compared with `<` or
 * `===`; they are not. Checked with the runtime rather than assumed.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/bridges-page-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc, withDocument } = require('./lib/dom-shim.js');
const L = require('./lib/lift.js');
// THE LIVE HALF IS FROZEN so this gate outlives `../MikroDash`. See
// `golden()` in lib/lift.js: with the reference present it records and
// re-verifies; without it, it asserts the port against the recording.
const G = L.golden('bridges-page-check');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT);

const iife = L.region(src, {
  banner: '/* ── Bridges page',
  must: ['bridgesTable', 'bridgesHostTable', 'brSumStp'],
  mustNot: ['DNS page', 'VLANs page', 'Queues page', 'backupsPage'],
});
const IDS = G.value('IDS', () => L.idsFor(src, iife));
// ── THE TAB STRIP IS DRIVEN NOW ────────────────────────────────────────────
//
// This gate's own header says the page has "a tab bar that decides which is
// visible", and nothing drove it. Misspelling `el('page-bridges')` — the
// container the panel switch reads — SURVIVED, which is the measurement that
// says the switch was never exercised.
//
// `page-bridges` stays out of COMPARED because its innerHTML is the whole page
// and comparing it would drown every other difference. It is COVERED all the
// same: the panels are found through it, so a wrong id leaves them unswitched.
// A THIRD button carrying a value neither tab uses. `setTab` maps anything that
// is not 'hosts' to 'ports', and without a bogus value that fallback is
// unreachable — nothing else can produce one. The same reason
// `routing-page-check` carries one.
const TABS = ['ports', 'hosts', 'nonsense'];
// Only the REAL tabs have panels; the bogus button has none, which is also what
// the page would do.
const PANES = ['ports', 'hosts'].map((t) => ({ id: 'brtab-' + t }));
const COMPARED = IDS.filter((id) => id !== 'page-bridges');
if (process.argv.includes('--ids')) {
  console.log(JSON.stringify(COMPARED.concat(['page-bridges']))); process.exit(0);
}

/**
 * The bar is queried for `.stab`, the PAGE for `.brtab-panel`. Both selectors
 * name the same four buttons through an alias so the two paths cannot mark
 * different objects — the trap `routing-page-check` hit first.
 */
const TAB_DOM = {
  elementQuery: {
    brTabBar: { '.stab': TABS, '[data-brtab]': '.stab' },
    'page-bridges': { '.brtab-panel': PANES },
  },
  queryAttr: { '.stab': 'data-brtab' },
};

const ENTRY = path.join(ROOT, 'testdata', '.br-entry.ts');
fs.writeFileSync(ENTRY, "export { initBridgesPage } from '../web/src/pages/bridges.js';\n");
const OUT = path.join(ROOT, 'testdata', '.br-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const snap = (doc) => {
  const n = doc.nodes;
  const out = {};
  for (const id of COMPARED.slice().sort()) {
    out[id] = n[id] ? { h: n[id].innerHTML, t: n[id].textContent } : null;
  }
  // ── WHICH TAB IS SHOWING ────────────────────────────────────────────────
  //
  // The lit button, its `aria-selected`, the active panel, and whether the host
  // TOOLS are shown — the search box and the "showing 500 of N" note belong to
  // the host view and are hidden on the ports one. None of that is in any
  // table's markup.
  const stabs = doc.nodes.brTabBar.querySelectorAll('.stab');
  out.__tabs = {
    lit: stabs.filter((b) => b.classList.contains('active'))
      .map((b) => b.getAttribute('data-brtab')),
    aria: stabs.map((b) => [b.getAttribute('data-brtab'),
      b.attributes && b.attributes['aria-selected']]),
    shown: doc.nodes['page-bridges'].querySelectorAll('.brtab-panel')
      .filter((p) => p.classList.contains('active')).map((p) => p.id),
    hostTools: n.bridgesHostTools ? n.bridgesHostTools.hidden : null,
  };
  return JSON.stringify(out);
};

/**
 * Press tabs, and send arrow keys.
 *
 * Delegated on the BAR with `closest`, which is what a real click does. The
 * arrow handler is on the same bar and toggles rather than walking a list —
 * two tabs, so left and right do the same thing, and that is worth pinning
 * because it is the kind of shortcut a rewrite tidies into a walk.
 */
function pressTabs(doc, o) {
  for (const t of o.tabs || []) {
    const btn = doc.nodes.brTabBar.querySelectorAll('.stab')
      .find((b) => b.getAttribute('data-brtab') === t);
    if (!btn) throw new Error('no tab button for ' + t);
    btn.closest = (sel) => (sel === '[data-brtab]' ? btn : null);
    doc.nodes.brTabBar.fire('click', { target: btn });
  }
  for (const key of o.keys || []) {
    doc.nodes.brTabBar.fire('keydown', { key, preventDefault() {} });
  }
}

function clickHeaders(doc, headId, clicks) {
  for (const i of clicks || []) {
    const cells = doc.nodes[headId].querySelectorAll('th');
    if (!cells[i]) throw new Error('no header cell at index ' + i + ' in #' + headId);
    cells[i].click();
  }
}

function liveRun(payload, opts) {
  const o = opts || {};
  const doc = makeDoc([...IDS, ...PANES.map((p) => p.id)], TAB_DOM);
  if (o.hostSearch) doc.nodes.bridgesHostSearch.value = o.hostSearch;
  const handlers = {};
  const ctx = {
    String, Array, Math, Number, Object, JSON, parseInt, parseFloat, isFinite,
    document: doc,
    socket: { on: (ev, fn) => { handlers[ev] = fn; }, emit() {} },
    setTimeout: (fn) => { fn(); return 0; }, clearTimeout: () => {}, window: {},
    requestAnimationFrame: (fn) => { fn(); return 0; },
  };
  vm.createContext(ctx);
  vm.runInContext([
    L.line(src, 'function esc('),
    L.whole(src, 'function fmtMbps('),
    L.whole(src, 'function resRow('),
    L.whole(src, 'function _sortMul('),
    L.whole(src, 'function _renderSortHeader('),
    'function $(id){return document.getElementById(id);}',
    'function pageVisible(){return true;}',
    'function _debounce(fn){return fn;}',
    L.declare(L.fileScopeEls(src, iife)),
    '(function () {' + iife + '})();',
  ].join('\n'), ctx);
  if (!handlers['bridges:update']) {
    throw new Error('the live page registered no handler; ids it wanted and this gate does not ' +
      'provide: ' + ([...doc.unknown].join(', ') || 'none'));
  }
  handlers['bridges:update'](payload);
  pressTabs(doc, o);
  if (o.hostSearch) doc.nodes.bridgesHostSearch.fire('input');
  clickHeaders(doc, 'bridgesThead', o.clicks);
  return snap(doc);
}

function portRun(payload, opts) {
  const o = opts || {};
  const doc = makeDoc([...IDS, ...PANES.map((p) => p.id)], TAB_DOM);
  if (o.hostSearch) doc.nodes.bridgesHostSearch.value = o.hostSearch;
  const handlers = {};
  const prevWin = globalThis.window;
  const prevST = globalThis.setTimeout;
  const prevRaf = globalThis.requestAnimationFrame;
  globalThis.window = {};
  globalThis.setTimeout = ((fn) => { fn(); return 0; });
  globalThis.requestAnimationFrame = ((fn) => { fn(); return 0; });
  try {
    return withDocument(doc, () => {
      delete require.cache[require.resolve(OUT)];
      require(OUT).initBridgesPage({ on: (ev, fn) => { handlers[ev] = fn; }, emit() {} }, () => true);
      if (!handlers['bridges:update']) throw new Error('the port registered no bridges:update handler');
      handlers['bridges:update'](payload);
      pressTabs(doc, o);
      if (o.hostSearch) doc.nodes.bridgesHostSearch.fire('input');
      clickHeaders(doc, 'bridgesThead', o.clicks);
      return snap(doc);
    });
  } finally {
    globalThis.requestAnimationFrame = prevRaf;
    globalThis.setTimeout = prevST;
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
        String(x).slice(0, 340), String(y).slice(0, 340));
    }
  }
}

const B = (o) => Object.assign({
  id: '*1', name: 'bridge1', macAddress: '02:00:00:00:00:01', mtu: 1500,
  protocolMode: 'rstp', vlanFiltering: true, igmpSnooping: false,
  portCount: 3, rxMbps: 12.5, txMbps: 3.25, disabled: false, comment: '',
}, o);
const PT = (o) => Object.assign({
  id: '*2', bridge: 'bridge1', interface: 'ether1', pvid: 1,
  edge: 'auto', horizon: 'none', learn: 'auto', disabled: false, comment: '',
}, o);
const H = (o) => Object.assign({
  mac: '02:00:00:00:00:99', bridge: 'bridge1', onInterface: 'ether1',
  dynamic: true, local: false, age: '5m',
}, o);
const P = (o) => Object.assign({ bridges: [], ports: [], hosts: [] }, o);

const CASES = {
  'nothing': [P({}), {}],
  'one bridge': [P({ bridges: [B({})] }), {}],
  'several bridges': [P({ bridges: [B({}), B({ id: '*9', name: 'bridge2' })] }), {}],
  // Bridge fields.
  'a disabled bridge': [P({ bridges: [B({ disabled: true })] }), {}],
  'vlan filtering off': [P({ bridges: [B({ vlanFiltering: false })] }), {}],
  'igmp snooping on': [P({ bridges: [B({ igmpSnooping: true })] }), {}],
  'no protocol mode': [P({ bridges: [B({ protocolMode: '' })] }), {}],
  'protocol mode none': [P({ bridges: [B({ protocolMode: 'none' })] }), {}],
  'no mtu': [P({ bridges: [B({ mtu: 0 })] }), {}],
  'no mac': [P({ bridges: [B({ macAddress: '' })] }), {}],
  'no ports': [P({ bridges: [B({ portCount: 0 })] }), {}],
  'zero rates': [P({ bridges: [B({ rxMbps: 0, txMbps: 0 })] }), {}],
  'null rates': [P({ bridges: [B({ rxMbps: null, txMbps: null })] }), {}],
  'a comment': [P({ bridges: [B({ comment: 'core' })] }), {}],
  // `(b.name || '')` only differs from `b.name` when the name is ABSENT, and
  // every corpus bridge had one — so dropping the fallback threw nowhere and
  // survived. A bridge with no name is unusual but the payload can carry it.
  'a bridge with NO name': [P({ bridges: [B({ name: '' })] }), {}],
  'a bridge with an undefined name': [P({ bridges: [B({ name: undefined })] }), {}],
  'sorting a nameless bridge against a named one': [P({ bridges: [
    B({ name: undefined }), B({ id: '*2', name: 'b' })] }), { clicks: [0] }],
  // Ports.
  'one port': [P({ bridges: [B({})], ports: [PT({})] }), {}],
  'several ports': [P({ bridges: [B({})], ports: [PT({}), PT({ id: '*3', interface: 'ether2' })] }), {}],
  'a disabled port': [P({ ports: [PT({ disabled: true })] }), {}],
  'a port with a pvid': [P({ ports: [PT({ pvid: 10 })] }), {}],
  'a port with horizon set': [P({ ports: [PT({ horizon: '1' })] }), {}],
  // Hosts.
  'one host': [P({ hosts: [H({})] }), {}],
  'a local host': [P({ hosts: [H({ local: true, dynamic: false })] }), {}],
  'a static host': [P({ hosts: [H({ dynamic: false })] }), {}],
  'many hosts': [P({ hosts: Array.from({ length: 40 }, (_, i) =>
    H({ mac: '02:00:00:00:00:' + String(i).padStart(2, '0') })) }), {}],
  'host search by mac': [P({ hosts: [H({}), H({ mac: '02:00:00:00:00:aa' })] }), { hostSearch: '99' }],
  'host search by interface': [P({ hosts: [H({}), H({ onInterface: 'ether9' })] }), { hostSearch: 'ether9' }],
  'host search matching nothing': [P({ hosts: [H({})] }), { hostSearch: 'zzzz' }],
  // Escaping.
  'markup in a bridge name': [P({ bridges: [B({ name: '<img src=x>' })] }), {}],
  'a quote in a comment': [P({ bridges: [B({ comment: 'a"b' })] }), {}],
  'markup in a host mac': [P({ hosts: [H({ mac: '<b>x</b>' })] }), {}],
  // Sorting, one click per heterogeneous column type.
  'sorted by name': [P({ bridges: [B({ name: 'z' }), B({ id: '*2', name: 'a' })] }), { clicks: [0] }],
  'sorted by name descending': [P({ bridges: [B({ name: 'z' }), B({ id: '*2', name: 'a' })] }), { clicks: [0, 0] }],
  // COLUMN INDICES, from COLS_B: 0 name, 1 proto, 2 vlan, 3 igmp, 4 mac, 5 mtu,
  // 6 ports, 7 rate. The first version clicked 2 for "rate" and was actually
  // sorting by VLAN — the two sides still agreed, so the case PASSED while
  // testing the wrong column. Read from the source rather than counted by eye.
  'sorted by a COUNT column (ports)': [P({ bridges: [
    B({ portCount: 1 }), B({ id: '*2', name: 'b', portCount: 9 })] }), { clicks: [6] }],
  'sorted by a SUMMED rate column': [P({ bridges: [
    B({ rxMbps: 1, txMbps: 1 }), B({ id: '*2', name: 'b', rxMbps: 0, txMbps: 50 })] }), { clicks: [7] }],
  'summed rate descending': [P({ bridges: [
    B({ rxMbps: 1, txMbps: 1 }), B({ id: '*2', name: 'b', rxMbps: 0, txMbps: 50 })] }), { clicks: [7, 7] }],
  'sorted by a BOOLEAN column (vlan)': [P({ bridges: [
    B({ vlanFiltering: false }), B({ id: '*2', name: 'b', vlanFiltering: true })] }), { clicks: [2] }],
  'sorted by the OTHER boolean (igmp)': [P({ bridges: [
    B({ igmpSnooping: true }), B({ id: '*2', name: 'b', igmpSnooping: false })] }), { clicks: [3] }],
  'sorted by a lowercased string (proto)': [P({ bridges: [
    B({ protocolMode: 'RSTP' }), B({ id: '*2', name: 'b', protocolMode: 'none' })] }), { clicks: [1] }],
  'sorted by mtu': [P({ bridges: [
    B({ mtu: 9000 }), B({ id: '*2', name: 'b', mtu: 1500 })] }), { clicks: [5] }],
  // The summary counters.
  'summary counts across bridges': [P({
    bridges: [B({ portCount: 2, protocolMode: 'rstp' }), B({ id: '*2', name: 'b', portCount: 3, protocolMode: 'none' })],
    ports: [PT({}), PT({ id: '*3' })],
    hosts: [H({}), H({ mac: '02:00:00:00:00:aa' })] }), {}],
  'summary with no stp anywhere': [P({ bridges: [B({ protocolMode: 'none' })] }), {}],
  // ── THE TAB STRIP ────────────────────────────────────────────────────────
  //
  // Two tabs sharing one bar and one pair of panels. Nothing drove this until
  // 2026-08-25: misspelling the container the panel switch reads SURVIVED.
  //
  // The HOST TOOLS follow the tab — the search box and the "showing 500 of N"
  // note belong to the host view — so a switch that forgot them leaves a search
  // box above a table it does not filter.
  'switch to hosts': [P({ bridges: [B({})], hosts: [H({})] }), { tabs: ['hosts'] }],
  'switch to hosts and back': [P({ bridges: [B({})], hosts: [H({})] }),
    { tabs: ['hosts', 'ports'] }],
  'the same tab twice': [P({ bridges: [B({})], hosts: [H({})] }), { tabs: ['hosts', 'hosts'] }],
  // ARROW KEYS. Two tabs, so left and right do the same thing — a toggle rather
  // than a walk along a list, which is the kind of shortcut a rewrite tidies.
  'arrow right toggles': [P({ bridges: [B({})], hosts: [H({})] }), { keys: ['ArrowRight'] }],
  'arrow left toggles the same way': [P({ bridges: [B({})], hosts: [H({})] }),
    { keys: ['ArrowLeft'] }],
  'arrow twice returns': [P({ bridges: [B({})], hosts: [H({})] }),
    { keys: ['ArrowRight', 'ArrowRight'] }],
  'a key that is not an arrow does nothing': [P({ bridges: [B({})], hosts: [H({})] }),
    { keys: ['Enter', 'a', 'ArrowUp'] }],
  'click then arrow': [P({ bridges: [B({})], hosts: [H({})] }),
    { tabs: ['hosts'], keys: ['ArrowRight'] }],
  // Switching with the host SEARCH set: the tab change re-renders, and the
  // filter has to survive it.
  // The fallback: an unknown value must land on ports, not hosts.
  'a button carrying an unknown value': [P({ bridges: [B({})], hosts: [H({})] }),
    { tabs: ['hosts', 'nonsense'] }],
  'switch to hosts with a search set': [P({ bridges: [B({})], hosts: [H({}), H({ mac: '02:00:00:00:00:aa' })] }),
    { tabs: ['hosts'], hostSearch: '99' }],

};

for (const [name, [payload, opts]] of Object.entries(CASES)) {
  let a, b;
  try { a = G.live(name, () => liveRun(payload, opts)); } catch (e) { shout('LIVE THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  try { b = portRun(payload, opts); } catch (e) { shout('PORT THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  cmp(name, a, b);
}

// ── believability ──────────────────────────────────────────────────────────
{
  const s = JSON.parse(G.live('believability:tables', () => liveRun(P({ bridges: [B({})], ports: [PT({})], hosts: [H({})] }), {})));
  assert.match(s.bridgesTable.h, /bridge1/, 'the live bridge table rendered no row');
  assert.match(s.bridgesPortTable.h, /ether1/, 'the port table rendered no row');
  assert.match(s.bridgesHostTable.h, /02:00:00:00:00:99/, 'the host table rendered no row');
  assert.equal(s.bridgesBadge.t, '1', 'the bridge badge is ' + s.bridgesBadge.t);
  assert.match(s.bridgesThead.h, /<th/, 'the sort header rendered nothing');
}
{
  // The host search really filters, and the badge follows what is SHOWN.
  const all = JSON.parse(G.live('believability:hosts-all', () => liveRun(P({ hosts: [H({}), H({ mac: '02:00:00:00:00:aa' })] }), {})));
  const one = JSON.parse(G.live('believability:hosts-filtered', () => liveRun(P({ hosts: [H({}), H({ mac: '02:00:00:00:00:aa' })] }), { hostSearch: '99' })));
  assert.ok(all.bridgesHostTable.h.length > one.bridgesHostTable.h.length,
    'the host search removed nothing');
  assert.ok(!/00:aa/.test(one.bridgesHostTable.h), 'the search kept a non-matching host');
}
{
  // The summed rate column really sums: 0+50 beats 1+1.
  // Column 7 is RX / TX, and `data-identity` is the reliable anchor — the name
  // cell also carries a state badge, so `>a<` matches nothing.
  const s = JSON.parse(G.live('believability:sort-by-rate', () => liveRun(P({ bridges: [
    B({ name: 'a', rxMbps: 1, txMbps: 1 }), B({ id: '*2', name: 'b', rxMbps: 0, txMbps: 50 })] }),
    { clicks: [7, 7] })));
  const first = s.bridgesTable.h.indexOf('data-identity="b"');
  const second = s.bridgesTable.h.indexOf('data-identity="a"');
  assert.ok(first !== -1 && (second === -1 || first < second),
    'descending by rate did not put the busier bridge first');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('bridges-page-check: %d cases identical', checked);
