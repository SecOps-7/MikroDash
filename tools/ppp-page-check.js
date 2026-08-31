'use strict';
/**
 * The PPP page, live against ported. Built on `tools/lib/lift.js`.
 *
 * ── WHAT THIS PAGE ADDS ─────────────────────────────────────────────────────
 *
 * `rxRate === null` is "no measurement window yet", tested by IDENTITY and
 * rendered as a dash carrying that reason. A zero rate is a real reading and
 * renders as a rate. The same distinction repeats in the summary, where
 * `totalRxRate` is passed through a `toMbps` that maps null to null rather than
 * to 0 — so the two branches must not be collapsed at either end.
 *
 * The empty state is TWO different sentences: a router with PPP configured and
 * nobody connected is told where sessions will appear; a router with no PPP
 * service at all is told that instead. `available` decides, and getting it
 * backwards would send someone hunting for a client that was never going to
 * arrive.
 *
 * The config table is built from ARRAYS, not objects, and the row builder blanks
 * a falsy cell to an em dash — so a profile whose `rateLimit` is empty falls
 * through to `remoteAddress` before that dash is reached.
 *
 * WHAT IT CANNOT SEE: layout, focus.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/ppp-page-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc, withDocument } = require('./lib/dom-shim.js');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/ppp-page-check.js --freeze
const G = L.golden('ppp-page-check');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT);

const iife = G.value('iife', () => L.region(src, {
  banner: '/* ── PPP page',
  must: ['pppServerTable', 'No active PPP sessions'],
  mustNot: ['DNS page', 'VLANs page', 'WAN page', 'backupsPage', 'qSimpleTable'],
}));
const IDS = G.value('IDS', () => L.idsFor(src, iife));

// Declare what this gate provides, for `tools/element-coverage-audit.js`. Placed
// BEFORE the bundle step so asking costs nothing: a text scan cannot see ids
// derived at runtime, and guessing at them is what the audit exists to stop.
if (process.argv.includes('--ids')) { console.log(JSON.stringify(IDS)); process.exit(0); }

const FILE_ELS = G.value('FILE_ELS', () => L.fileScopeEls(src, iife));

const ENTRY = path.join(ROOT, 'testdata', '.ppp-entry.ts');
fs.writeFileSync(ENTRY, "export { initPppPage } from '../web/src/pages/ppp.js';\n");
const OUT = path.join(ROOT, 'testdata', '.ppp-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const snap = (doc) => {
  const n = doc.nodes;
  const g = (id) => (n[id] ? { h: n[id].innerHTML, t: n[id].textContent, c: n[id].className } : null);
  const out = {};
  for (const id of IDS.slice().sort()) out[id] = g(id);
  return JSON.stringify(out);
};

function clickHeaders(doc, clicks) {
  for (const i of clicks || []) {
    const cells = doc.nodes.pppThead.querySelectorAll('th');
    if (!cells[i]) throw new Error('no header cell at index ' + i);
    cells[i].click();
  }
}

function liveRun(payload, opts) {
  const o = opts || {};
  const doc = makeDoc(IDS, {});
  if (o.query) doc.nodes.pppSearch.value = o.query;
  const handlers = {};
  const ctx = {
    String, Array, Math, Number, Object, JSON, parseInt, parseFloat, isFinite,
    document: doc,
    socket: { on: (ev, fn) => { handlers[ev] = fn; }, emit() {} },
    setTimeout: () => 0, clearTimeout: () => {}, window: {},
  };
  vm.createContext(ctx);
  vm.runInContext([
    L.line(src, 'function esc('),
    L.whole(src, 'function fmtMbps('),
    L.whole(src, 'function fmtBytes('),
    L.line(src, 'function parseUptime('),
    L.whole(src, 'function _sortMul('),
    L.whole(src, 'function _renderSortHeader('),
    'function $(id){return document.getElementById(id);}',
    'function pageVisible(){return true;}',
    'function _debounce(fn){return fn;}',
    L.declare(FILE_ELS),
    '(function () {' + iife + '})();',
  ].join('\n'), ctx);
  if (!handlers['ppp:update']) {
    throw new Error('the live page registered no handler; ids it wanted and this gate does not ' +
      'provide: ' + ([...doc.unknown].join(', ') || 'none'));
  }
  handlers['ppp:update'](payload);
  clickHeaders(doc, o.clicks);
  return snap(doc);
}

function portRun(payload, opts) {
  const o = opts || {};
  const doc = makeDoc(IDS, {});
  if (o.query) doc.nodes.pppSearch.value = o.query;
  const handlers = {};
  const prevWin = globalThis.window;
  globalThis.window = {};
  try {
    return withDocument(doc, () => {
      delete require.cache[require.resolve(OUT)];
      require(OUT).initPppPage({ on: (ev, fn) => { handlers[ev] = fn; }, emit() {} }, () => true);
      if (!handlers['ppp:update']) throw new Error('the port registered no ppp:update handler');
      handlers['ppp:update'](payload);
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

const S = (o) => Object.assign({
  id: '*1', name: 'dialin', service: 'pppoe', address: '10.1.1.2',
  callerId: '02:00:00:00:00:01', uptime: '2h30m', rxRate: 125000, txRate: 250000,
  rx: 1048576, tx: 2097152,
}, o);
const SRV = (o) => Object.assign({
  serviceName: 'pppoe-in', interface: 'ether2', maxSessions: 10, disabled: false,
}, o);
const PROF = (o) => Object.assign({
  name: 'default', localAddress: '10.1.1.1', rateLimit: '10M/10M',
  remoteAddress: 'pool1', onlyOne: 'yes',
}, o);
const P = (o) => Object.assign({
  sessions: [], servers: [], profiles: [], byService: {},
  totalRxRate: 0, totalTxRate: 0, available: true,
}, o);

const CASES = {
  'no sessions, PPP available': [P({}), {}],
  // THE OTHER EMPTY STATE — a different sentence, decided by `available`.
  'no sessions, PPP NOT available': [P({ available: false }), {}],
  'one session': [P({ sessions: [S({})] }), {}],
  'several sessions': [P({ sessions: [S({}), S({ id: '*2', name: 'other' })] }), {}],
  // null is "no measurement window yet", zero is a reading.
  'a session with null rates': [P({ sessions: [S({ rxRate: null, txRate: null })] }), {}],
  'a session with zero rates': [P({ sessions: [S({ rxRate: 0, txRate: 0 })] }), {}],
  'rx null but tx a number': [P({ sessions: [S({ rxRate: null })] }), {}],
  // Session fields.
  'no service falls back to PPP': [P({ sessions: [S({ service: '' })] }), {}],
  'no address': [P({ sessions: [S({ address: '' })] }), {}],
  'no caller id': [P({ sessions: [S({ callerId: '' })] }), {}],
  'no uptime': [P({ sessions: [S({ uptime: '' })] }), {}],
  'zero counters': [P({ sessions: [S({ rx: 0, tx: 0 })] }), {}],
  // Search.
  'search by name': [P({ sessions: [S({}), S({ id: '*2', name: 'zzz' })] }), { query: 'dialin' }],
  'search by address': [P({ sessions: [S({})] }), { query: '10.1.1' }],
  'search by caller id': [P({ sessions: [S({})] }), { query: '02:00' }],
  'search matching nothing': [P({ sessions: [S({})] }), { query: 'nope' }],
  'search is trimmed and lowercased': [P({ sessions: [S({})] }), { query: '  DIALIN  ' }],
  // The summary.
  'byService counts': [P({ sessions: [S({})], byService: { pppoe: 2, l2tp: 1 } }), {}],
  'no byService key': [P({ sessions: [S({})], byService: undefined }), {}],
  'an empty byService': [P({ sessions: [S({})], byService: {} }), {}],
  'null totals are dashes': [P({ totalRxRate: null, totalTxRate: null }), {}],
  'zero totals are rates': [P({ totalRxRate: 0, totalTxRate: 0 }), {}],
  'rx total null, tx a number': [P({ totalRxRate: null, totalTxRate: 125000 }), {}],
  // The config table — arrays, and a cascade of fallbacks.
  'one server': [P({ servers: [SRV({})] }), {}],
  'an unnamed server': [P({ servers: [SRV({ serviceName: '' })] }), {}],
  'a disabled server': [P({ servers: [SRV({ disabled: true })] }), {}],
  'a server with no session cap': [P({ servers: [SRV({ maxSessions: 0 })] }), {}],
  'one profile': [P({ profiles: [PROF({})] }), {}],
  'a profile with no rate limit falls back to the pool': [P({ profiles: [PROF({ rateLimit: '' })] }), {}],
  'a profile with neither rate limit nor pool': [P({ profiles: [PROF({ rateLimit: '', remoteAddress: '' })] }), {}],
  'a profile with no onlyOne': [P({ profiles: [PROF({ onlyOne: '' })] }), {}],
  'servers and profiles together': [P({ servers: [SRV({})], profiles: [PROF({})] }), {}],
  'no servers or profiles': [P({ servers: [], profiles: [] }), {}],
  // Escaping.
  'markup in a session name': [P({ sessions: [S({ name: '<img src=x>' })] }), {}],
  'a quote in a caller id': [P({ sessions: [S({ callerId: 'a"b' })] }), {}],
  'markup in a server name': [P({ servers: [SRV({ serviceName: '<b>x</b>' })] }), {}],
  // Sorting.
  'sorted by the first column': [P({ sessions: [S({ id: '*1', name: 'z' }), S({ id: '*2', name: 'a' })] }), { clicks: [0] }],
  'first column descending': [P({ sessions: [S({ id: '*1', name: 'z' }), S({ id: '*2', name: 'a' })] }), { clicks: [0, 0] }],
  'a sort survives a search': [P({ sessions: [S({ id: '*1', name: 'zdial' }), S({ id: '*2', name: 'adial' })] }), { query: 'dial', clicks: [0, 0] }],
};

for (const [name, [payload, opts]] of Object.entries(CASES)) {
  let a, b;
  try { a = G.live(name, () => liveRun(payload, opts)); } catch (e) { shout('LIVE THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  try { b = portRun(payload, opts); } catch (e) { shout('PORT THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  cmp(name, a, b);
}

// ── believability ──────────────────────────────────────────────────────────
{
  const s = JSON.parse(G.live('auto:7', () => liveRun(P({ sessions: [S({})], servers: [SRV({})], profiles: [PROF({})],
    byService: { pppoe: 1 } }), {})));
  assert.match(s.pppTable.h, /dialin/, 'the live session table rendered no row');
  assert.match(s.pppServerTable.h, /pppoe-in/, 'the config table rendered nothing');
  assert.match(s.pppServerTable.h, /default/, 'the profile row is missing');
  assert.match(s.pppThead.h, /<th/, 'the sort header rendered nothing');
  assert.equal(s.pppBadge.t, '1', 'the badge is ' + s.pppBadge.t);
  assert.match(s.pppBadge.c, /active-blue/, 'a non-empty table left the badge inactive');
  assert.match(s.pppSumServices.t, /pppoe 1/, 'the service summary is ' + s.pppSumServices.t);
}
{
  // The two empty states really are different sentences.
  const yes = JSON.parse(G.live('auto:6', () => liveRun(P({ available: true }), {}))).pppTable.h;
  const no = JSON.parse(G.live('auto:5', () => liveRun(P({ available: false }), {}))).pppTable.h;
  assert.match(yes, /No active PPP sessions/, 'the available-but-idle empty state is wrong');
  assert.match(no, /no PPP service configured/, 'the unavailable empty state is wrong');
  assert.notEqual(yes, no, 'both empty states rendered the same sentence');
}
{
  // null is not zero, at BOTH ends.
  const nul = JSON.parse(G.live('auto:4', () => liveRun(P({ sessions: [S({ rxRate: null })] }), {}))).pppTable.h;
  const zero = JSON.parse(G.live('auto:3', () => liveRun(P({ sessions: [S({ rxRate: 0 })] }), {}))).pppTable.h;
  assert.match(nul, /No measurement window yet/, 'a null rate lost its explanation');
  assert.notEqual(nul, zero, 'a null rate rendered the same as a zero one');
  const sNul = JSON.parse(G.live('auto:2', () => liveRun(P({ totalRxRate: null }), {}))).pppSumRx.h;
  const sZero = JSON.parse(G.live('auto:1', () => liveRun(P({ totalRxRate: 0 }), {}))).pppSumRx.h;
  assert.notEqual(sNul, sZero, 'a null TOTAL rendered the same as a zero one');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('ppp-page-check: %d cases identical', checked);
