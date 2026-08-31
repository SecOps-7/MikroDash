'use strict';
/**
 * The Dashboard's latency block, live against ported.
 *
 * ── THE CLASSES ARE THE OUTPUT, NOT JUST THE NUMBERS ────────────────────────
 *
 * Every figure carries a CSS class chosen by a threshold, and the class is what
 * a viewer actually reads at a glance — green, amber, red. A port that showed
 * the right number in the wrong colour would pass any check that compared only
 * `textContent`, so the DOM comparison includes `className` and `title`.
 *
 * ── AND THE CHART IS COMPARED AS ITS DATA ───────────────────────────────────
 *
 * A Chart.js bar chart has no DOM. Both sides get a fake `Chart` that records
 * the config it was constructed with and the arrays written to it, and those are
 * compared — including the per-bar COLOURS, which is where a timeout shows as
 * grey rather than red.
 *
 * ── LOSS HAS ITS OWN SCALE ──────────────────────────────────────────────────
 *
 * Zero is ok, under 50% warn, the rest bad — nothing to do with the rtt
 * thresholds, so 49% loss is the same colour as a 60ms round trip. Both
 * boundaries are probed on each side and ON them.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/ping-card-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource. This gate already carried the empty-source guard in
// its slicing helper, but the READ itself still used fs.readFileSync and died of
// ENOENT before reaching it — the guard's own comment described behaviour the
// code did not have.
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('ping-card-check');
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

function slice(decl, close, name) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent; without this the helper throws at module scope before a frozen
  // output can be served. Harmless because the live half is never entered
  // once the output is frozen.
  if (src === '') return '';
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find ' + name);
  const j = src.indexOf(close, i);
  if (j === -1) throw new Error(name + ' is never closed');
  return src.slice(i, j + close.length);
}
function braceBody(from) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent; without this the helper throws at module scope before a frozen
  // output can be served. Harmless because the live half is never entered
  // once the output is frozen.
  if (src === '') return '';
  const open = src.indexOf('{', from);
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === '{') depth++;
    else if (src[i] === '}') { depth--; if (!depth) return src.slice(open + 1, i); }
  }
  throw new Error('unbalanced body');
}

const rttClassSrc = G.value('rttClassSrc', () => slice('function rttClass(', '\n}', 'rttClass'));
const pingColorSrc = G.value('pingColorSrc', () => slice('function pingColor(', '\n}', 'pingColor'));
const makeSrc = G.value('makeSrc', () => slice('function makePingChart(', '\n}', 'makePingChart'));
const updSrc = G.value('updSrc', () => slice('function updatePingChart(', '\n}', 'updatePingChart'));
const renderSrc = G.value('renderSrc', () => slice('function renderPingUI(', '\n}', 'renderPingUI'));
const histBody = G.value('histBody', () => braceBody(src.indexOf("socket.on('ping:history'")));
// The SECOND `ping:update` handler. The first, ~100 lines earlier, only resets
// the networksCard stale timer — the port covers that through `stale.ts` and its
// generated tables, not here. Slicing the first one silently compared this card
// against a handler that renders nothing, which the assertion below caught.
const updBody = G.value('updBody', () => braceBody(src.indexOf("socket.on('ping:update'", src.indexOf("socket.on('ping:history'"))));
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['rttClassSrc', rttClassSrc], ['pingColorSrc', pingColorSrc], ['makeSrc', makeSrc], ['updSrc', updSrc], ['renderSrc', renderSrc], ['histBody', histBody], ['updBody', updBody]]) {
  assert.ok(typeof __v === 'string' && __v.length > 4,
    'the recorded ' + __n + ' is empty — the golden is broken');
}
if (LIFT.hasReference(ROOT)) assert.match(renderSrc, /ping-val/, 'the renderPingUI slice lost its classes');
assert.match(updBody, /permissionDenied/, 'the ping:update slice lost its refusal branch');

const ENTRY = path.join(ROOT, 'testdata', '.pingcard-entry.ts');
fs.writeFileSync(ENTRY,
  "export { onPingUpdate, onPingHistory, resetPing, rttClass, pingColor, pingChartConfig } " +
  "from '../web/src/pages/dashboard-ping.js';\n");
const OUT = path.join(ROOT, 'testdata', '.pingcard-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const IDS = ['ndPingRtt', 'ndPingLoss', 'ndPingMin', 'ndPingMax', 'pingTargetLabel'];

function makeDom() {
  const byId = new Map();
  const chartCalls = [];
  const mk = (id) => {
    const n = {
      id, className: '', title: '', style: {},
      set textContent(v) { n._t = String(v); },
      get textContent() { return n._t === undefined ? '' : n._t; },
    };
    byId.set(id, n);
    return n;
  };
  for (const id of IDS) mk(id);
  mk('pingChartNet');
  const chart = {
    data: { labels: [], datasets: [{ data: [], backgroundColor: [] }] },
    update: (m) => chartCalls.push('update(' + m + ')'),
    destroy: () => chartCalls.push('destroy()'),
  };
  return { byId, chart, chartCalls, made: [] };
}
function snapshot(d) {
  const out = { chart: { labels: d.chart.data.labels.slice(), data: d.chart.data.datasets[0].data.slice(), colors: d.chart.data.datasets[0].backgroundColor.slice() }, calls: d.chartCalls.slice(), made: d.made.length };
  for (const id of IDS) {
    const n = d.byId.get(id);
    out[id] = { text: n.textContent, cls: n.className, title: n.title };
  }
  return JSON.stringify(out);
}

function liveSide() {
  const d = makeDom();
  const ctx = {
    Math, JSON, String, Number, Date: { now: () => 1700000000000 },
    document: { getElementById: (id) => d.byId.get(id) || null },
    Chart: function (canvas, cfg) { d.made.push(cfg); return d.chart; },
    pingHistory: [], MAX_PING_HIST: 60, pingChartNet: null,
    $: (id) => d.byId.get(id) || null,
  };
  vm.createContext(ctx);
  vm.runInContext([
    rttClassSrc, pingColorSrc, makeSrc, updSrc, renderSrc,
    'function __hist(data){' + histBody + '}',
    'function __upd(data){' + updBody + '}',
  ].join('\n'), ctx);
  return { d, hist: (x) => ctx.__hist(x), upd: (x) => ctx.__upd(x), ctx };
}
function portSide() {
  const d = makeDom();
  globalThis.document = { getElementById: (id) => d.byId.get(id) || null };
  globalThis.Chart = function (canvas, cfg) { d.made.push(cfg); return d.chart; };
  delete require.cache[require.resolve(OUT)];
  const m = require(OUT);
  m.resetPing();
  return { d, hist: m.onPingHistory, upd: m.onPingUpdate, m };
}

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (a === b) return;
  bad++;
  if (bad <= 4) {
    const A = JSON.parse(a), B = JSON.parse(b);
    shout('DIFF %s', what);
    for (const k of Object.keys(A)) {
      if (JSON.stringify(A[k]) !== JSON.stringify(B[k])) {
        shout('  %s\n    live: %s\n    port: %s', k, JSON.stringify(A[k]), JSON.stringify(B[k]));
      }
    }
  }
}

const U = (o) => Object.assign({ target: '1.1.1.1', rtt: 12, loss: 0, minRtt: 9, maxRtt: 20, ts: 1 }, o);
const SCRIPTS = {
  'one update': [['upd', U({})]],
  'rtt on each side of 50': [['upd', U({ rtt: 49 })], ['upd', U({ rtt: 50 })], ['upd', U({ rtt: 51 })]],
  'rtt on each side of 150': [['upd', U({ rtt: 149 })], ['upd', U({ rtt: 150 })], ['upd', U({ rtt: 151 })]],
  'a timeout (null rtt)': [['upd', U({ rtt: null, loss: 100 })]],
  'loss at 0, 1, 49, 50, 100': [
    ['upd', U({ loss: 0 })], ['upd', U({ loss: 1 })], ['upd', U({ loss: 49 })],
    ['upd', U({ loss: 50 })], ['upd', U({ loss: 100 })],
  ],
  'min and max absent': [['upd', U({ minRtt: null, maxRtt: null })]],
  'min and max on the thresholds': [['upd', U({ minRtt: 50, maxRtt: 150 })]],
  'a fractional rtt': [['upd', U({ rtt: 0.783 })], ['upd', U({ rtt: 1.438 })]],
  'PERMISSION DENIED': [['upd', { permissionDenied: true, ts: 1 }]],
  'denied then recovered': [
    ['upd', { permissionDenied: true, ts: 1 }], ['upd', U({ rtt: 10 })],
  ],
  'disabled is ignored entirely': [['upd', U({ rtt: 99 })], ['upd', { enabled: false, rtt: 1 }]],
  'the target label follows the payload': [
    ['upd', U({ target: '8.8.8.8' })], ['upd', U({ target: '' })], ['upd', U({ target: '9.9.9.9' })],
  ],
  'history seeds the chart': [
    ['hist', { target: '1.1.1.1', minRtt: 5, maxRtt: 40, history: [
      { ts: 1, rtt: 10, loss: 0 }, { ts: 2, rtt: null, loss: 10 }, { ts: 3, rtt: 200, loss: 5 },
    ] }],
  ],
  'an EMPTY history renders nothing': [['hist', { target: 'x', history: [] }]],
  // The target guard on the HISTORY path, which is a second copy of the same
  // line: a mutation to it survived while only the update path was exercised.
  'a history with no target keeps the old label': [
    ['upd', U({ target: '8.8.8.8' })],
    ['hist', { target: '', history: [{ ts: 1, rtt: 5, loss: 0 }] }],
    ['hist', { history: [{ ts: 2, rtt: 6, loss: 0 }] }],
  ],
  'history then live updates': [
    ['hist', { target: '1.1.1.1', minRtt: 5, maxRtt: 40, history: [{ ts: 1, rtt: 10, loss: 0 }] }],
    ['upd', U({ rtt: 60 })], ['upd', U({ rtt: 300 })],
  ],
  'the buffer caps at 60 and the chart draws 50': [
    ['hist', { target: 'x', history: Array.from({ length: 80 }, (_, i) => ({ ts: i, rtt: i, loss: 0 })) }],
    ...Array.from({ length: 5 }, (_, i) => [['upd', U({ rtt: 500 + i })]]).flat(),
  ],
};

for (const [name, script] of Object.entries(SCRIPTS)) {
  const L = liveSide(), P = portSide();
  script.forEach(([op, arg], i) => {
    if (op === 'upd') { L.upd(arg); P.upd(arg); } else { L.hist(arg); P.hist(arg); }
    cmp(name + ' after step ' + (i + 1) + ' (' + op + ')', snapshot(L.d), snapshot(P.d));
  });
}

// ── the two threshold functions on their own ───────────────────────────────
{
  const L = liveSide(), P = portSide();
  for (const v of [null, undefined, 0, 0.1, 1, 49, 49.9, 50, 50.1, 149, 150, 151, 1000, -1]) {
    cmp('rttClass(' + v + ')', JSON.stringify(L.ctx.rttClass(v)), JSON.stringify(P.m.rttClass(v)));
    cmp('pingColor(' + v + ')', JSON.stringify(L.ctx.pingColor(v)), JSON.stringify(P.m.pingColor(v)));
  }
}

// ── the chart CONFIG, and its callbacks ────────────────────────────────────
{
  const L = liveSide(), P = portSide();
  L.upd(U({})); P.upd(U({}));
  assert.equal(L.d.made.length, 1, 'the live side built no chart');
  const norm = (cfg) => {
    const seen = JSON.parse(JSON.stringify(cfg, (k, v) => (typeof v === 'function' ? 'FN' : v)));
    // The two callbacks that format what a viewer reads.
    seen.options.plugins.tooltip.callbacks.label =
      [null, 0, 12, 12.5].map((raw) => cfg.options.plugins.tooltip.callbacks.label({ raw })).join('|');
    seen.options.scales.y.ticks.callback =
      [0, 5, 100].map((v) => cfg.options.scales.y.ticks.callback(v)).join('|');
    return JSON.stringify(seen);
  };
  cmp('the ping chart config', norm(L.d.made[0]), norm(P.d.made[0]));
  assert.match(norm(L.d.made[0]), /timeout/, 'the live tooltip lost its timeout label');
}

// ── believability ──────────────────────────────────────────────────────────
{
  const L = liveSide();
  L.upd(U({ rtt: 200, loss: 60 }));
  const s = JSON.parse(snapshot(L.d));
  assert.equal(s.ndPingRtt.text, '200', 'the live card did not render the rtt');
  assert.equal(s.ndPingRtt.cls, 'ping-val ping-bad', 'the live rtt class is ' + s.ndPingRtt.cls);
  assert.equal(s.ndPingLoss.cls, 'ping-val ping-bad', 'the live loss class is ' + s.ndPingLoss.cls);
  assert.ok(s.chart.colors.length > 0, 'the live chart drew no bars');
  const D = liveSide();
  D.upd({ permissionDenied: true, ts: 1 });
  const t = JSON.parse(snapshot(D.d));
  assert.equal(t.ndPingLoss.text, 'N/A', 'the live refusal did not render N/A');
  assert.match(t.ndPingLoss.title, /test.*policy/i, 'the live refusal lost its explanation');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d comparisons differ', bad, checked); process.exit(1); }
say('ping-card-check: %d comparisons identical', checked);
