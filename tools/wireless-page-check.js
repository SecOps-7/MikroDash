'use strict';
/**
 * The WIRELESS page, live against ported.
 *
 * ── WHY THIS PAGE NEXT ──────────────────────────────────────────────────────
 *
 * Part 95 closed the sort-header gap for DNS and Queues but left one piece
 * uncompared and said so: `c.cls`, the per-column class `_renderSortHeader`
 * passes through. Neither of those pages sets it. THIS header does, and the live
 * comment says why — "wl-col-* classes are passed through because the matching
 * td carries them", pairing `wl-col-iface` and `wl-col-uptime` on the th with
 * the same class on its td. Dropping it silently breaks that pairing, and until
 * now no gate could see it.
 *
 * The header also carries the other case those pages lacked: columns with a NULL
 * key — Interface and Band are unsortable on purpose, because the table is
 * GROUPED by interface and Band is a derived label.
 *
 * WHAT IT CANNOT SEE: layout, focus, the SSID colour assignment's visual result
 * (the markup is compared, the rendering is not), and `wl-idle`, which the live
 * renderer hard-codes to false.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/wireless-page-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc, withDocument } = require('./lib/dom-shim.js');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource. This gate already carried the empty-source guard in
// its slicing helper, but the READ itself still used fs.readFileSync and died of
// ENOENT before reaching it — the guard's own comment described behaviour the
// code did not have.
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('wireless-page-check');
// FROZEN AS ONE JOINED PROGRAM. These seven lifters were called INLINE inside the
// `vm.runInContext` array — the form no pattern matches, and the reason to freeze
// the JOINED RESULT rather than chase each call: it covers every lift inside it
// whatever shape each one has.
let LIVE_HELPERS;
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));
const lines = src.split('\n');

// FROZEN — the region is EXECUTED, so the source is what must survive. A
// multi-line IIFE whose body also asserts, which is why it needed doing by hand.
const iife = G.value('the lifted wireless IIFE', () => {
  const at = lines.findIndex((l) => l.includes('function renderWireless()'));
  assert.ok(at > 0, 'no renderWireless in app.js');
  let open = -1;
  for (let j = at; j >= 0; j--) if (/^\(function\s*\(\s*\)\s*\{/.test(lines[j])) { open = j; break; }
  assert.ok(open >= 0, 'renderWireless is not inside an IIFE');
  let close = -1;
  for (let j = at; j < lines.length; j++) if (/^\}\)\(\);|^\}\(\)\);/.test(lines[j])) { close = j; break; }
  assert.ok(close > open, 'the wireless IIFE never closes at column 0');
  return lines.slice(open + 1, close).join('\n');
});
assert.ok(iife.includes('renderWireless'), 'the lifted region is not the wireless page');
assert.ok(iife.includes('wl-col-iface'), 'the lifted region lost the column classes this gate exists for');
// Inclusion cannot bound a slice — these are what prove it stopped in time.
for (const foreign of ['Queues page', 'backupsPage', 'DNS page', 'dnsSettingsBody', 'qSimpleTable']) {
  assert.ok(!iife.includes(foreign), 'the lifted wireless region reaches into another page (' + foreign + ')');
}

const grab = (decl) => { const i = src.indexOf(decl); return src.slice(i, src.indexOf('\n', i)); };
// BRACE-MATCHED, not "up to the first line starting with }". The naive version
// truncates any function containing a nested block that closes at column 0's
// indentation, and the result is a syntax error miles from the cause — this file
// spent a run on `Unexpected token '}'` before the extractor was fixed.
function whole(decl) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent; without this the helper throws at module scope before a frozen
  // output can be served. Harmless because the live half is never entered
  // once the output is frozen.
  if (src === '') return '';
  const n = src.split(decl).length - 1;
  assert.equal(n, 1, 'AMBIGUOUS anchor (' + n + '): ' + decl);
  const i = src.indexOf(decl);
  const open = src.indexOf('{', i);
  let depth = 0;
  for (let j = open; j < src.length; j++) {
    if (src[j] === '{') depth++;
    else if (src[j] === '}') { depth--; if (!depth) return src.slice(i, j + 1); }
  }
  throw new Error('unbalanced body for ' + decl);
}

const ENTRY = path.join(ROOT, 'testdata', '.wl-entry.ts');
fs.writeFileSync(ENTRY, "export { initWirelessPage } from '../web/src/pages/wireless.js';\n");
const OUT = path.join(ROOT, 'testdata', '.wl-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

// ── THE ELEMENTS THIS REGION NEEDS ──────────────────────────────────────────
//
// Three different spellings, and scanning for `$('…')` inside the region finds
// only the first:
//
//   1. `$('wlSsidList')` — inside the region.
//   2. `wlThead` — passed as a STRING ARGUMENT to `_renderSortHeader`. Missing
//      this is what left the Queues headers written-but-unwired.
//   3. `wirelessTable`, `wirelessTabBadge` — resolved at FILE scope in app.js
//      (`var wirelessTable = $('wirelessTable');`) and merely REFERENCED here.
//
// The third kind is derived from app.js rather than hand-listed, because
// discovering them one crash at a time is how the last two runs were spent.
// FROZEN. Derived from the live source and CONSUMED by the comparison below, so
// it is a lifted value rather than a question — guarding it would leave the list
// empty and every case below vacuous. The assertion then validates the RECORDING
// and stays live, which is what it was for.
const FILE_SCOPE_ELS = G.value('FILE_SCOPE_ELS', () =>
  [...src.matchAll(/^var\s+([A-Za-z_][\w]*)\s*=\s*\$\('([A-Za-z0-9_-]+)'\);/gm)]
    .filter(([, name]) => new RegExp('\\b' + name + '\\b').test(iife))
    .map(([, name, id]) => ({ name, id })));
assert.ok(FILE_SCOPE_ELS.length, 'no file-scope element vars matched — the extraction has broken');

const IDS = [...new Set([
  'wlThead',
  ...FILE_SCOPE_ELS.map((e) => e.id),
  ...[...iife.matchAll(/\$\('([A-Za-z0-9_-]+)'\)/g)].map((m) => m[1]),
])];

// What this gate actually COMPARES, for `element-coverage-audit`. Declared
// rather than guessed — and it is exactly the snapshot's list below, no more:
// over-claiming here was caught once already, when `reports-tables-check`
// declared all 66 of its region ids and compared twelve.
//
// `wifiSortBtns` is deliberately ABSENT — it is an input, not an output, so
// there is nothing for a DOM comparison to hold. `wirelessTabBadge` WAS absent
// for the same reason the audit reported it uncovered: the snapshot did not read
// it. It does now, class included, because the badge carries a colour as well as
// a number and a port that got the count right and the colour wrong would look
// correct in every text comparison.
// ── THE SORT BUTTON STRIP ──────────────────────────────────────────────────
//
// Three buttons beside the table header, and they are NOT the header: pressing
// one sets the column AND resets the direction to that column's default, where
// the header toggles. Both write the same state, so the header indicator follows
// a button press and vice versa — which is the part worth comparing, and which
// nothing drove until 2026-08-25.
const SORT_BTNS = ['name', 'signal', 'uptime'];
const BTN_DOM = {
  elementQuery: { wifiSortBtns: { '.wl-sort-btn': SORT_BTNS } },
  queryAttr: { '.wl-sort-btn': 'data-sort' },
};

const COVERS = [
  'wirelessTable', 'wlThead', 'wlSsidList', 'ndWirelessCount',
  'wlBand24', 'wlBand5', 'wlBand6', 'wlBandNum24', 'wlBandNum5', 'wlBandNum6',
  'wlBandRow6', 'wirelessTabBadge', 'wifiSortBtns',
];
if (process.argv.includes('--ids')) { console.log(JSON.stringify(COVERS)); process.exit(0); }

const snap = (doc) => {
  const n = doc.nodes;
  const g = (id) => (n[id] ? { h: n[id].innerHTML, t: n[id].textContent } : null);
  return JSON.stringify({
    table: g('wirelessTable'), thead: g('wlThead'), ssids: g('wlSsidList'),
    count: g('ndWirelessCount'),
    bands: ['wlBand24', 'wlBand5', 'wlBand6', 'wlBandNum24', 'wlBandNum5', 'wlBandNum6'].map(g),
    band6row: n.wlBandRow6 ? n.wlBandRow6.style.display : null,
    // The badge's TEXT AND CLASS: it turns blue only when somebody is connected,
    // so the colour is data and not decoration.
    tabBadge: n.wirelessTabBadge
      ? [n.wirelessTabBadge.textContent, n.wirelessTabBadge.className] : null,
    // WHICH SORT BUTTON IS LIT. It follows the same state the header writes, so
    // a header click must move it too — and a button press must move the
    // header's indicator, which is already in `thead` above.
    sortBtns: doc.nodes.wifiSortBtns.querySelectorAll('.wl-sort-btn')
      .map((b) => [b.getAttribute('data-sort'), b.classList.contains('active')]),
  });
};

/** Press sort BUTTONS, through the strip's delegated listener. */
function pressSortBtns(doc, keys) {
  for (const k of keys || []) {
    const btn = doc.nodes.wifiSortBtns.querySelectorAll('.wl-sort-btn')
      .find((b) => b.getAttribute('data-sort') === k);
    if (!btn) throw new Error('no sort button for ' + k);
    btn.closest = (sel) => (sel === '.wl-sort-btn' ? btn : null);
    doc.nodes.wifiSortBtns.fire('click', { target: btn });
  }
}

function clickHeaders(doc, clicks) {
  for (const i of clicks || []) {
    const cells = doc.nodes.wlThead.querySelectorAll('th');
    if (!cells[i]) throw new Error('no header cell at index ' + i);
    cells[i].click();
  }
}

LIVE_HELPERS = G.value('the lifted live helpers', () => [
  grab('function esc('),
  whole('function _renderSortHeader('),
  whole('function signalBars('),
  whole('function sigQuality('),
  whole('function parseTxRate('),
  whole('function parseTxRateNum('),
  whole('function bandBadge('),
].join('\n'));
if (!LIVE_HELPERS || LIVE_HELPERS.length < 200) {
  throw new Error('the recorded live helpers are empty — the golden is broken');
}

function liveRun(payload, clicks, btns) {
  const doc = makeDoc([...IDS, 'wifiSortBtns'], BTN_DOM);
  const handlers = {};
  const ctx = {
    String, Array, Math, Number, Object, JSON, parseInt, parseFloat, isFinite,
    document: doc,
    socket: { on: (ev, fn) => { handlers[ev] = fn; }, emit() {} },
    setTimeout: () => 0, clearTimeout: () => {},
    // The region PUBLISHES helpers onto `window` for other pages (the dashboard
    // wireless card reads `_bandBadge` and `_ssidColours`) and reads
    // `_VIEW_PRESETS`. A bare object is enough; the publishing is behaviour this
    // gate does not compare, but its ABSENCE crashes the lift.
    window: {},
  };
  vm.createContext(ctx);
  vm.runInContext([
    LIVE_HELPERS,
    'function $(id){return document.getElementById(id);}',
    FILE_SCOPE_ELS.map((e) => 'var ' + e.name + ' = $("' + e.id + '");').join('\n'),
    'function pageVisible(){return true;}',
    '(function(){' + iife + '})();',
  ].join('\n'), ctx);
  if (!handlers['wireless:update']) {
    throw new Error('the live page registered no handler; ids it wanted and this gate does not ' +
      'provide: ' + ([...doc.unknown].join(', ') || 'none'));
  }
  handlers['wireless:update'](payload);
  clickHeaders(doc, clicks);
  pressSortBtns(doc, btns);
  return snap(doc);
}

function portRun(payload, clicks, btns) {
  const doc = makeDoc([...IDS, 'wifiSortBtns'], BTN_DOM);
  const handlers = {};
  // The port publishes the same helpers onto `window` that the live region does,
  // so both sides need one. Restored afterwards rather than left behind.
  const prevWin = globalThis.window;
  globalThis.window = {};
  try {
  return withDocument(doc, () => {
    delete require.cache[require.resolve(OUT)];
    require(OUT).initWirelessPage({ on: (ev, fn) => { handlers[ev] = fn; }, emit() {} }, () => true);
    if (!handlers['wireless:update']) throw new Error('the port registered no wireless:update handler');
    handlers['wireless:update'](payload);
    clickHeaders(doc, clicks);
    pressSortBtns(doc, btns);
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

const C = (o) => Object.assign({
  name: 'laptop', mac: '02:00:00:00:00:01', ip: '198.51.100.10', iface: '5GHz WiFi',
  ssid: 'net', band: '5ghz-ax', signal: '-55', txRate: '866.7Mbps', rxRate: '780Mbps',
  uptime: '1h2m3s', source: 'wifi',
}, o);
// Taken from `renderSsids` rather than guessed: `ifaces` is an ARRAY that is
// joined into a title attribute, and `bands` is an array of badges. My first
// fixture had singular `iface`/`band` and every SSID case crashed on `.join`.
const S = (o) => Object.assign({
  ssid: 'net', ifaces: ['5GHz WiFi'], bands: ['5ghz-ax'], clients: 1,
  disabled: false, running: true,
}, o);
const P = (o) => Object.assign({ clients: [], ssids: [] }, o);

const CASES = {
  'no clients': [P({}), []],
  'one client': [P({ clients: [C({})] }), []],
  'two on one interface': [P({ clients: [C({}), C({ mac: '02:00:00:00:00:02', name: 'phone' })] }), []],
  'two interfaces group separately': [P({ clients: [
    C({}), C({ mac: '02:00:00:00:00:02', iface: '2.4GHz WiFi', band: '2ghz-n' })] }), []],
  // The CAP badge keys on `c.source === 'capsman'`, NOT a `capsman` flag — my
  // first fixture set the wrong field, so this case passed while testing
  // nothing. It also needs TWO interfaces, because the group header only renders
  // when there is more than one group to tell apart.
  'a capsman client (two groups, so the header shows)': [P({ clients: [
    C({ source: 'capsman' }),
    C({ mac: '02:00:00:00:00:02', iface: '2.4GHz WiFi', band: '2ghz-n' })] }), []],
  'a capsman client alone renders NO group header': [P({ clients: [C({ source: 'capsman' })] }), []],
  'two groups, neither capsman': [P({ clients: [
    C({}), C({ mac: '02:00:00:00:00:02', iface: '2.4GHz WiFi' })] }), []],
  'a client with no iface groups as unknown': [P({ clients: [
    C({ iface: '' }), C({ mac: '02:00:00:00:00:02', iface: 'ether-wifi' })] }), []],
  // Row fields, each absent in turn.
  'a client with no name falls back to the mac': [P({ clients: [C({ name: '' })] }), []],
  'a client with no ip': [P({ clients: [C({ ip: '' })] }), []],
  'a client with no iface': [P({ clients: [C({ iface: '' })] }), []],
  'a client with no uptime': [P({ clients: [C({ uptime: '' })] }), []],
  'a client with no rxRate': [P({ clients: [C({ rxRate: '' })] }), []],
  'a client with no band': [P({ clients: [C({ band: '' })] }), []],
  // Signal, across the bar thresholds.
  'signal -30': [P({ clients: [C({ signal: '-30' })] }), []],
  'signal -55': [P({ clients: [C({ signal: '-55' })] }), []],
  'signal -68': [P({ clients: [C({ signal: '-68' })] }), []],
  'signal -75': [P({ clients: [C({ signal: '-75' })] }), []],
  'signal -90': [P({ clients: [C({ signal: '-90' })] }), []],
  'signal unparseable': [P({ clients: [C({ signal: 'n/a' })] }), []],
  'signal empty': [P({ clients: [C({ signal: '' })] }), []],
  // Rates.
  'a plain Mbps rate': [P({ clients: [C({ txRate: '300Mbps' })] }), []],
  'a rate with a stream suffix': [P({ clients: [C({ txRate: '866.7Mbps-80MHz/2S' })] }), []],
  'a Gbps rate': [P({ clients: [C({ txRate: '1.2Gbps' })] }), []],
  'an unparseable rate': [P({ clients: [C({ txRate: 'wat' })] }), []],
  // Escaping.
  'markup in a client name': [P({ clients: [C({ name: '<img src=x>' })] }), []],
  'a quote in an ssid': [P({ clients: [C({ ssid: 'a"b' })], ssids: [S({ ssid: 'a"b' })] }), []],
  'markup in an interface name': [P({ clients: [C({ iface: '<b>if</b>' })] }), []],
  // SSID list.
  'no ssids': [P({ clients: [C({})], ssids: [] }), []],
  'one ssid': [P({ clients: [C({})], ssids: [S({})] }), []],
  'several ssids': [P({ clients: [C({})], ssids: [S({}), S({ ssid: 'guest', ifaces: ['2.4GHz WiFi'], bands: ['2ghz-n'] })] }), []],
  'an ssid with no clients': [P({ ssids: [S({ clients: 0 })] }), []],
  'a disabled ssid keeps the muted treatment': [P({ ssids: [S({ disabled: true })] }), []],
  'an ssid that is not running': [P({ ssids: [S({ running: false })] }), []],
  'an ssid on several interfaces': [P({ ssids: [S({ ifaces: ['5GHz WiFi', '2.4GHz WiFi'] })] }), []],
  'an ssid with no bands': [P({ ssids: [S({ bands: [] })] }), []],
  'an ssid with several bands': [P({ ssids: [S({ bands: ['2ghz-n', '5ghz-ax'] })] }), []],
  // The empty state distinguishes "none" from "managed by CAPsMAN", and says so
  // because reading as "none" would send someone hunting.
  'no ssids, none managed elsewhere': [P({ ssids: [], ssidsManagedElsewhere: 0 }), []],
  'no ssids, ONE radio managed by CAPsMAN': [P({ ssids: [], ssidsManagedElsewhere: 1 }), []],
  'no ssids, THREE radios managed by CAPsMAN': [P({ ssids: [], ssidsManagedElsewhere: 3 }), []],
  // ── THE SORT HEADER, which is why this page was chosen ────────────────────
  'default sort (signal, descending)': [P({ clients: [
    C({ mac: '02:00:00:00:00:01', name: 'a', signal: '-70', txRate: '100Mbps', uptime: '1m' }),
    C({ mac: '02:00:00:00:00:02', name: 'b', signal: '-40', txRate: '800Mbps', uptime: '3h' }),
    C({ mac: '02:00:00:00:00:03', name: 'c', signal: '-55', txRate: '400Mbps', uptime: '10m' })] }), []],
  'sorted by device name': [P({ clients: [
    C({ mac: '02:00:00:00:00:01', name: 'zed', signal: '-70' }),
    C({ mac: '02:00:00:00:00:02', name: 'amy', signal: '-40' })] }), [0]],
  'device name descending': [P({ clients: [
    C({ mac: '02:00:00:00:00:01', name: 'zed', signal: '-70' }),
    C({ mac: '02:00:00:00:00:02', name: 'amy', signal: '-40' })] }), [0, 0]],
  // Interface (1) and Band (2) carry a NULL key — clicking must do NOTHING.
  // Neither DNS nor Queues could test this: DNS has no null-key column and
  // Queues has no keys at all.
  'clicking Interface does nothing (null key)': [P({ clients: [
    C({ mac: '02:00:00:00:00:01', name: 'zed', signal: '-70' }),
    C({ mac: '02:00:00:00:00:02', name: 'amy', signal: '-40' })] }), [1]],
  'clicking Band does nothing (null key)': [P({ clients: [
    C({ mac: '02:00:00:00:00:01', name: 'zed', signal: '-70' }),
    C({ mac: '02:00:00:00:00:02', name: 'amy', signal: '-40' })] }), [2]],
  'sorted by signal explicitly': [P({ clients: [
    C({ mac: '02:00:00:00:00:01', signal: '-70' }),
    C({ mac: '02:00:00:00:00:02', signal: '-40' })] }), [3]],
  'sorted by tx rate': [P({ clients: [
    C({ mac: '02:00:00:00:00:01', txRate: '100Mbps' }),
    C({ mac: '02:00:00:00:00:02', txRate: '800Mbps' })] }), [4]],
  'sorted by uptime': [P({ clients: [
    C({ mac: '02:00:00:00:00:01', uptime: '5m' }),
    C({ mac: '02:00:00:00:00:02', uptime: '2h' })] }), [5]],
  'name descending, then uptime resets to its default': [P({ clients: [
    C({ mac: '02:00:00:00:00:01', uptime: '5m', name: 'zed' }),
    C({ mac: '02:00:00:00:00:02', uptime: '2h', name: 'amy' })] }), [0, 5]],
  // ── THE SORT BUTTONS ─────────────────────────────────────────────────────
  //
  // Pressing one sets the column AND resets the direction to that column's
  // default; the HEADER toggles instead. Both write the same state, so each
  // must move the other's indicator — which is why the header markup is in the
  // same snapshot as the lit button.
  'press the name button': [P({ clients: [C({}), C({ mac: '02:00:00:00:00:02', name: 'b' })] }), [], ['name']],
  'press signal': [P({ clients: [C({}), C({ mac: '02:00:00:00:00:02', name: 'b' })] }), [], ['signal']],
  'press uptime': [P({ clients: [C({}), C({ mac: '02:00:00:00:00:02', name: 'b' })] }), [], ['uptime']],
  // Pressing the SAME button twice must not toggle — that is the header's job,
  // and a button that toggled would reverse a list the operator just sorted.
  'press the same button twice': [P({ clients: [C({}), C({ mac: '02:00:00:00:00:02', name: 'b' })] }),
    [], ['name', 'name']],
  'press two different buttons': [P({ clients: [C({}), C({ mac: '02:00:00:00:00:02', name: 'b' })] }),
    [], ['uptime', 'name']],
  // A HEADER click then a BUTTON press: the button must reset the direction the
  // header had toggled.
  'header toggle, then a button': [P({ clients: [C({}), C({ mac: '02:00:00:00:00:02', name: 'b' })] }),
    [1], ['name']],
  'button, then a header toggle': [P({ clients: [C({}), C({ mac: '02:00:00:00:00:02', name: 'b' })] }),
    [], ['uptime']],

};

for (const [name, [payload, clicks, btns]] of Object.entries(CASES)) {
  let a, b;
  try { a = liveRun(payload, clicks, btns); } catch (e) { shout('LIVE THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  try { b = portRun(payload, clicks, btns); } catch (e) { shout('PORT THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  cmp(name, a, b);
}

// ── believability ──────────────────────────────────────────────────────────
{
  // TWO interfaces: the group header renders only when there is more than one
  // group, which is correct — a single-radio router does not need telling which
  // radio its clients are on. The first version of this assertion used one
  // client and failed, and the ASSERTION was what was wrong.
  const s = JSON.parse(liveRun(P({
    clients: [C({}), C({ mac: '02:00:00:00:00:02', iface: '2.4GHz WiFi' })], ssids: [S({})] }), []));
  assert.match(s.table.h, /wl-group-label/, 'the live table rendered no group header');
  assert.match(s.table.h, /02:00:00:00:00:01/, 'the client row is missing');
  const one = JSON.parse(liveRun(P({ clients: [C({})] }), []));
  assert.ok(!/wl-group-label/.test(one.table.h),
    'a single interface rendered a group header it does not need');
  assert.match(s.thead.h, /<th/, 'the sort header rendered nothing');
  // The whole reason this page was chosen.
  assert.match(s.thead.h, /wl-col-iface/, 'the per-column class is not reaching the header');
  assert.match(s.thead.h, /wl-col-uptime/, 'the uptime column class is not reaching the header');
  assert.match(s.table.h, /class="wl-col-iface"/, 'the matching td class is missing — the pairing is broken');
}
{
  const s = JSON.parse(liveRun(P({}), []));
  assert.match(s.table.h, /No wireless clients/, 'the empty state did not render');
}
{
  // A null-key column must render WITHOUT the pointer affordance, and clicking
  // it must not reorder anything.
  const rows = [C({ mac: '02:00:00:00:00:01', name: 'zed', signal: '-70' }),
                C({ mac: '02:00:00:00:00:02', name: 'amy', signal: '-40' })];
  const before = JSON.parse(liveRun(P({ clients: rows }), []));
  const after = JSON.parse(liveRun(P({ clients: rows }), [1]));
  assert.equal(before.table.h, after.table.h, 'clicking a NULL-key column reordered the table');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('wireless-page-check: %d cases identical', checked);
