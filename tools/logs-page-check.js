'use strict';
/**
 * The LOGS page, live against ported.
 *
 * ── ANOTHER CARD GATE THAT LOOKED LIKE PAGE COVERAGE ────────────────────────
 *
 * `fwlogs-cards-check.js` provides `dc-logs` — the dashboard card. The PAGE
 * writes `logs`, the four severity count badges, and the pause/clear controls,
 * and none of that was compared. Same shape as the VPN page, found the same way:
 * by asking what a passing gate actually provides rather than what its name
 * suggests.
 *
 * ── DRIVING THE CONTROLS ────────────────────────────────────────────────────
 *
 * Filter and severity live behind ELEMENT listeners on both sides — a search
 * box's `input`, a select's `change` — so the shim now records element-level
 * listeners and `node.fire(ev)` dispatches them. Reaching into a closure to set
 * `logFilter` instead would compare this harness against an implementation
 * rather than two implementations against each other.
 *
 * ── TWO HANDLERS FOR ONE EVENT ──────────────────────────────────────────────
 *
 * `logs:history` is subscribed twice in app.js — once here and once by the
 * dashboard card — so the lift selects by CONTENT. `lift.handler()` refuses the
 * bare anchor rather than silently taking the first, which would have compared
 * the card's renderer against this page's port module.
 *
 * WHAT IT CANNOT SEE: scroll position (`autoScroll` sets `scrollTop`, which no
 * shim can measure meaningfully), layout, focus.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/logs-page-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc, withDocument } = require('./lib/dom-shim.js');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/logs-page-check.js --freeze
const G = L.golden('logs-page-check');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT);

// FROZEN AT THE LIFT. The asserts below check the handlers still carry their
// cap; with the reference absent the lifters return stubs, so unfrozen values
// make those asserts fire for a reason unrelated to the handlers.
const HISTORY = G.value('HISTORY', () => L.handler(src, 'logs:history', { contains: 'logBuffer' }));
const NEW = G.value('NEW', () => L.handler(src, 'logs:new', { contains: 'logBuffer' }));
assert.ok(HISTORY.includes('MAX_LOG_LINES'), 'the history handler lost its cap');
assert.ok(NEW.includes('logBuffer.shift'), 'the new-line handler lost its cap');

const IDS = ['logs', 'logSearch', 'logSeverity', 'toggleScroll', 'clearLogs',
  'logCountError', 'logCountWarning', 'logCountInfo', 'logCountDebug'];

const ENTRY = path.join(ROOT, 'testdata', '.logs-entry.ts');
fs.writeFileSync(ENTRY, "export { initLogsPage } from '../web/src/pages/logs.js';\n");
const OUT = path.join(ROOT, 'testdata', '.logs-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const snap = (doc) => {
  const n = doc.nodes;
  const g = (id) => (n[id] ? { h: n[id].innerHTML, t: n[id].textContent,
    c: [...(n[id].classList._s || [])].sort().join(' ') } : null);
  const out = {};
  for (const id of IDS.slice().sort()) out[id] = g(id);
  return JSON.stringify(out);
};

// A script is a list of [op, arg]. Both sides run the same one.
function drive(doc, fire, script) {
  for (const [op, arg] of script) {
    if (op === 'history') fire('logs:history', arg);
    else if (op === 'new') fire('logs:new', arg);
    else if (op === 'search') { doc.nodes.logSearch.value = arg; doc.nodes.logSearch.fire('input'); }
    else if (op === 'severity') { doc.nodes.logSeverity.value = arg; doc.nodes.logSeverity.fire('change'); }
    else if (op === 'toggle') doc.nodes.toggleScroll.fire('click');
    else if (op === 'clear') doc.nodes.clearLogs.fire('click');
    else throw new Error('unknown op ' + op);
  }
}

function liveRun(script) {
  const doc = makeDoc(IDS, {});
  const handlers = {};
  const ctx = {
    String, Array, Math, Number, Object, JSON, parseInt, isFinite,
    document: doc,
    socket: { on: (ev, fn) => { handlers[ev] = fn; }, emit() {} },
    setTimeout: (fn) => { fn(); return 0; }, clearTimeout: () => {}, window: {},
  };
  vm.createContext(ctx);
  vm.runInContext([
    L.line(src, 'function esc('),
    'function $(id){return document.getElementById(id);}',
    'function _debounce(fn){return fn;}',
    L.declare(L.fileScopeEls(src, HISTORY + NEW + 'logsEl logSearch logSeverity toggleScroll clearLogs')),
    'var logBuffer=[],MAX_LOG_LINES=2000;',
    L.whole(src, 'var logCountEls={'),
    L.whole(src, 'function updateLogCounts('),
    L.line(src, 'function topicClass('),
    L.line(src, 'function sevClass('),
    L.line(src, 'function buildLogHtml('),
    'var autoScroll = true, logFilter = "", logLevel = "";',
    L.whole(src, 'function flushLogs('),
    'socket.on("logs:history", function(data){' + HISTORY + '});',
    'socket.on("logs:new", function(line){' + NEW + '});',
    L.line(src, "logSearch.addEventListener('input'"),
    L.line(src, "logSeverity.addEventListener('change'"),
    L.line(src, "toggleScroll.addEventListener('click'"),
    L.line(src, "clearLogs.addEventListener('click'"),
    'updateLogCounts();',
  ].join('\n'), ctx);
  drive(doc, (ev, arg) => {
    if (!handlers[ev]) throw new Error('nothing subscribes ' + ev);
    handlers[ev](arg);
  }, script);
  return snap(doc);
}

function portRun(script) {
  const doc = makeDoc(IDS, {});
  const handlers = {};
  // THE DEBOUNCE MUST BE NEUTRALISED ON BOTH SIDES, not one. The live lift is
  // given `_debounce(fn){return fn;}`, so its search listener runs immediately;
  // the port calls the real `debounce`, which defers through setTimeout. With
  // only one side neutralised the port simply never applied the filter and five
  // cases "differed" — the harness, not the port. Running the timer immediately
  // here is the same neutralisation expressed the only way it can be from
  // outside the module.
  const prevWin = globalThis.window;
  const prevST = globalThis.setTimeout;
  globalThis.window = {};
  globalThis.setTimeout = ((fn) => { fn(); return 0; });
  try {
    return withDocument(doc, () => {
      delete require.cache[require.resolve(OUT)];
      require(OUT).initLogsPage({ on: (ev, fn) => { handlers[ev] = fn; }, emit() {} }, () => true);
      drive(doc, (ev, arg) => {
        if (!handlers[ev]) throw new Error('the port does not subscribe ' + ev);
        handlers[ev](arg);
      }, script);
      return snap(doc);
    });
  } finally {
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
      if (x !== y) shout('DIFF %s [%s]\n  live: %s\n  port: %s', what, k, x, y);
    }
  }
}

const E = (o) => Object.assign({
  time: '12:00:00', topics: 'system,info', severity: 'info', message: 'started',
}, o);
const many = (n, o) => Array.from({ length: n }, (_, i) => E(Object.assign({ message: 'line' + i }, o)));

const CASES = {
  'no history': [[['history', []]]],
  'one line': [[['history', [E({})]]]],
  'several lines': [[['history', [E({}), E({ message: 'second' })]]]],
  // ToDo #17's two payload shapes, both still accepted.
  'a bare array history': [[['history', [E({})]]]],
  'an { entries } history': [[['history', { entries: [E({})] }]]],
  'an empty entries object': [[['history', { entries: [] }]]],
  'a history with no entries key': [[['history', {}]]],
  // The four severities drive both the line class and the count badges.
  'an error line': [[['history', [E({ severity: 'error' })]]]],
  'a warning line': [[['history', [E({ severity: 'warning' })]]]],
  'a debug line': [[['history', [E({ severity: 'debug' })]]]],
  'an unknown severity': [[['history', [E({ severity: 'critical' })]]]],
  'one of each severity': [[['history', [
    E({ severity: 'error' }), E({ severity: 'warning' }),
    E({ severity: 'info' }), E({ severity: 'debug' })]]]],
  // The badge labels pluralise only for error and warning.
  'one error is singular': [[['history', [E({ severity: 'error' })]]]],
  'two errors are plural': [[['history', [E({ severity: 'error' }), E({ severity: 'error' })]]]],
  'one warning is singular': [[['history', [E({ severity: 'warning' })]]]],
  'two warnings are plural': [[['history', [E({ severity: 'warning' }), E({ severity: 'warning' })]]]],
  'two info lines do NOT pluralise': [[['history', [E({}), E({})]]]],
  // Topic classes.
  'a firewall topic': [[['history', [E({ topics: 'firewall,info' })]]]],
  'a forward topic also reads as firewall': [[['history', [E({ topics: 'forward' })]]]],
  'a dhcp topic': [[['history', [E({ topics: 'dhcp,info' })]]]],
  'a wireless topic': [[['history', [E({ topics: 'wireless' })]]]],
  'a wifi topic': [[['history', [E({ topics: 'wifi' })]]]],
  'a wlan topic': [[['history', [E({ topics: 'wlan' })]]]],
  'a system topic': [[['history', [E({ topics: 'system' })]]]],
  'an unmatched topic': [[['history', [E({ topics: 'script' })]]]],
  'topic matching is case-insensitive': [[['history', [E({ topics: 'FIREWALL' })]]]],
  // Streaming.
  'a new line after history': [[['history', [E({})]], ['new', E({ message: 'live' })]]],
  'a new line with no history': [[['new', E({ message: 'first' })]]],
  'several new lines': [[['new', E({ message: 'a' })], ['new', E({ message: 'b' })]]],
  // Filters, driven through the real controls.
  'a search filter': [[['history', [E({ message: 'alpha' }), E({ message: 'beta' })]], ['search', 'alpha']]],
  'a search matching nothing': [[['history', [E({})]], ['search', 'zzz']]],
  'a search is lowercased': [[['history', [E({ message: 'Alpha' })]], ['search', 'ALPHA']]],
  'a search matches the topic': [[['history', [E({ topics: 'dhcp' })]], ['search', 'dhcp']]],
  'a search matches the time': [[['history', [E({ time: '13:45:01' })]], ['search', '13:45']]],
  'a severity filter': [[['history', [E({ severity: 'error' }), E({})]], ['severity', 'error']]],
  'a severity filter matching nothing': [[['history', [E({})]], ['severity', 'debug']]],
  'severity and search together': [[['history', [
    E({ severity: 'error', message: 'alpha' }), E({ severity: 'error', message: 'beta' })]],
    ['severity', 'error'], ['search', 'alpha']]],
  'clearing the severity filter': [[['history', [E({ severity: 'error' }), E({})]],
    ['severity', 'error'], ['severity', '']]],
  'a new line that the filter excludes': [[['history', [E({})]], ['search', 'alpha'],
    ['new', E({ message: 'beta' })]]],
  'a new line that the filter includes': [[['history', [E({})]], ['search', 'alpha'],
    ['new', E({ message: 'alpha two' })]]],
  // Controls.
  'pause toggles the label': [[['history', [E({})]], ['toggle']]],
  'pause then resume': [[['history', [E({})]], ['toggle'], ['toggle']]],
  'clear empties the view': [[['history', [E({}), E({})]], ['clear']]],
  'clear then a new line': [[['history', [E({})]], ['clear'], ['new', E({ message: 'after' })]]],
  // The 2000-line cap, on both paths.
  'history over the cap keeps the newest': [[['history', many(2005)]]],
  'history exactly at the cap': [[['history', many(2000)]]],
  'streaming past the cap': [[['history', many(2000)], ['new', E({ message: 'newest' })]]],
  // Escaping.
  'markup in a message': [[['history', [E({ message: '<img src=x>' })]]]],
  'a quote in a topic': [[['history', [E({ topics: 'a"b' })]]]],
  'an ampersand in a message': [[['history', [E({ message: 'a & b' })]]]],
};

for (const [name, [script]] of Object.entries(CASES)) {
  let a, b;
  try { a = G.live(name, () => liveRun(script)); } catch (e) { shout('LIVE THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  try { b = portRun(script); } catch (e) { shout('PORT THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  cmp(name, a, b);
}

// ── believability ──────────────────────────────────────────────────────────
{
  const s = JSON.parse(G.live('believability:severity', () => liveRun([['history', [E({ severity: 'error' }), E({})]]])));
  assert.match(s.logs.h, /log-line/, 'the live page rendered no log lines');
  assert.match(s.logs.h, /log-error/, 'the error severity class is missing');
  assert.equal(s.logCountError.t, '1 error', 'the error badge is ' + s.logCountError.t);
  assert.equal(s.logCountInfo.t, '1 info', 'the info badge is ' + s.logCountInfo.t);
}
{
  // The filter really removes lines.
  const all = JSON.parse(G.live('believability:search-all', () => liveRun([['history', [E({ message: 'alpha' }), E({ message: 'beta' })]]])));
  const one = JSON.parse(G.live('believability:search-one', () => liveRun([['history', [E({ message: 'alpha' }), E({ message: 'beta' })]], ['search', 'alpha']])));
  assert.ok(all.logs.h.length > one.logs.h.length, 'the search filter removed nothing');
  assert.ok(!/beta/.test(one.logs.h), 'the search filter kept a non-matching line');
  // …and the COUNTS are of the buffer, not the view: filtering must not
  // renumber the badges, or the operator loses sight of what the router said.
  assert.equal(all.logCountInfo.t, one.logCountInfo.t,
    'the severity badges follow the filter — they count the buffer: ' + one.logCountInfo.t);
}
{
  const capped = JSON.parse(G.live('believability:cap', () => liveRun([['history', many(2005)]])));
  assert.ok(!/line0</.test(capped.logs.h), 'the oldest line survived the 2000-line cap');
  assert.match(capped.logs.h, /line2004/, 'the newest line was dropped by the cap');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('logs-page-check: %d cases identical', checked);
