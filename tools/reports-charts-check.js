'use strict';
/**
 * The REPORTS charts, live against ported — by their CONFIG, not their pixels.
 *
 * ── WHAT IS ACTUALLY COMPARABLE HERE ────────────────────────────────────────
 *
 * Every previous gate in this port has said "not the charts, those need
 * Chart.js". That was true of the drawing and false of everything that decides
 * it: both sides call `new Chart(canvas, config)`, and the config carries the
 * downsampling, the datasets, the labels, the axis scales and the colours. Chart
 * renders it; the port's job is to build it.
 *
 * So Chart is stubbed on BOTH sides to capture the config, and the configs are
 * compared. A stub is safe here for the reason it was not safe for `resRow`: it
 * produces nothing that reaches the comparison, it only records what it was
 * handed.
 *
 * ── FUNCTIONS IN THE CONFIG ARE CALLED, NOT SKIPPED ─────────────────────────
 *
 * Tick and tooltip callbacks are where the formatting lives — a Mbps suffix, a
 * percentage, an axis label. Serialising them as '[fn]' would compare two charts
 * that agree about having a callback and disagree about what it prints. They are
 * invoked with sample values and their OUTPUT is compared.
 *
 * WHAT IT CANNOT SEE: the drawing itself, canvas sizing, animation.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/reports-charts-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc, withDocument } = require('./lib/dom-shim.js');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/reports-charts-check.js --freeze
const G = L.golden('reports-charts-check');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT);

const iife = G.value('iife', () => L.region(src, {
  banner: '// ── Reports page',
  must: ['renderPingChart', 'renderTrafficChart', 'renderBandwidthChart'],
  mustNot: ['Queues page', 'backupsPage', 'dnsSettingsBody'],
}));

const COMPARED = ['rptPingChart', 'rptTrafficChart', 'rptBandwidthChart', 'rptShowCapacity'];
if (process.argv.includes('--ids')) { console.log(JSON.stringify(COMPARED)); process.exit(0); }
const IDS = G.value('IDS', () => L.idsFor(src, iife));

const ENTRY = path.join(ROOT, 'testdata', '.rc-entry.ts');
fs.writeFileSync(ENTRY, [
  "export { renderPingChart, renderTrafficChart, renderBandwidthChart }",
  "  from '../web/src/pages/reports-charts.js';",
  "export { setReportTimezone } from '../web/src/pages/reports.js';",
].join('\n') + '\n');
const OUT = path.join(ROOT, 'testdata', '.rc-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const TZ = 'UTC';

// Sample values fed to every callback found in a config. Chosen to exercise
// formatting rather than to be realistic: a zero, a fraction, a round number and
// a large one.
// Two shapes, because Chart calls these two ways: an axis TICK callback gets a
// number, a tooltip LABEL callback gets a context object. Feeding only numbers
// made every label callback throw on both sides — identical, and therefore
// "equal", while exercising none of the formatting it exists for.
const SAMPLES = [
  0, 0.5, 12, 1024.5,
  { dataset: { label: 'RX' }, parsed: { y: 12.5 } },
  { dataset: { label: 'TX' }, parsed: { y: 0 } },
];

/**
 * Serialise a Chart config, INVOKING functions rather than eliding them.
 * A callback that throws on a sample records the throw, which is itself a
 * comparable fact — both sides should throw on the same input or neither should.
 */
function freeze(v, seen) {
  seen = seen || new Set();
  if (typeof v === 'function') {
    const outs = SAMPLES.map((s) => {
      try {
        const r = v.call({ chart: { data: { datasets: [] } } }, s, 0, [{ value: s }]);
        return typeof r === 'object' ? JSON.stringify(r) : String(r);
      } catch (e) { return 'THREW:' + e.message; }
    });
    return { __fn: outs };
  }
  if (v && typeof v === 'object') {
    // CYCLE DETECTION TRACKS THE CURRENT PATH, not everything visited. A shared
    // object — Chart configs reuse one font descriptor across several axes — is
    // REPEATED, not circular, and marking it '__circular' on the second visit
    // produced a difference between two identical configs. Removed on the way
    // back up so a sibling sees it fresh.
    if (seen.has(v)) return '__circular';
    seen.add(v);
    const out = Array.isArray(v)
      ? v.map((x) => freeze(x, seen))
      : Object.keys(v).sort().reduce((o, k) => { o[k] = freeze(v[k], seen); return o; }, {});
    seen.delete(v);
    return out;
  }
  return v;
}

function chartStub(captured) {
  return function Chart(canvas, config) {
    captured.push({ id: canvas && canvas.id, config: freeze(config) });
    return { destroy() {}, update() {}, data: config && config.data, options: config && config.options };
  };
}

function liveRun(call, capacity) {
  const doc = makeDoc(IDS, {});
  doc.nodes.rptShowCapacity.checked = !!capacity;
  const captured = [];
  const ctx = {
    String, Array, Math, Number, Object, JSON, Date, parseInt, parseFloat, isFinite, isNaN,
    encodeURIComponent, document: doc,
    socket: { on() {}, emit() {} },
    setTimeout: (fn) => { fn(); return 0; }, clearTimeout: () => {},
    window: { location: { origin: 'http://x' } },
    fetch: () => Promise.resolve({ ok: true, json: () => Promise.resolve({}) }),
    Chart: chartStub(captured),
    MutationObserver: function () { return { observe() {}, disconnect() {} }; },
    ResizeObserver: function () { return { observe() {}, disconnect() {} }; },
    requestAnimationFrame: (fn) => { fn(); return 0; },
    localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
    __out: null,
  };
  vm.createContext(ctx);
  vm.runInContext([
    L.line(src, 'function esc('),
    L.whole(src, 'function fmtBytes('),
    L.whole(src, 'function fmtMbps('),
    L.whole(src, 'function fmtDataMB('),
    L.whole(src, 'function maxOf('),
    L.whole(src, 'function _sortRows('),
    L.whole(src, 'function _sortMul('),
    L.whole(src, 'function _renderSortHeader('),
    L.line(src, 'var _displayTimezone'),
    'function $(id){return document.getElementById(id);}',
    'function _debounce(fn){return fn;}',
    'function pageVisible(){return true;}',
    '(function () {' + iife +
      '\n__out = { renderPingChart: renderPingChart, renderTrafficChart: renderTrafficChart,' +
      ' renderBandwidthChart: renderBandwidthChart };\n})();',
  ].join('\n'), ctx);
  assert.ok(ctx.__out && ctx.__out.renderPingChart, 'the region did not publish its chart renderers');
  vm.runInContext('_displayTimezone = ' + JSON.stringify(TZ) + ';', ctx);
  call(ctx.__out, doc);
  return JSON.stringify(captured);
}

function portRun(call, capacity) {
  const doc = makeDoc(IDS, {});
  doc.nodes.rptShowCapacity.checked = !!capacity;
  const captured = [];
  const prevWin = globalThis.window;
  const prevChart = globalThis.Chart;
  globalThis.window = { location: { origin: 'http://x' } };
  globalThis.Chart = chartStub(captured);
  try {
    return withDocument(doc, () => {
      delete require.cache[require.resolve(OUT)];
      const mod = require(OUT);
      if (mod.setReportTimezone) mod.setReportTimezone(TZ);
      call(mod, doc);
      return JSON.stringify(captured);
    });
  } finally {
    globalThis.Chart = prevChart;
    if (prevWin === undefined) delete globalThis.window; else globalThis.window = prevWin;
  }
}

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (a === b) return;
  bad++;
  if (bad <= 3) {
    // Report the first PATH that differs rather than the first 600 characters:
    // a chart config is deep and the interesting difference is rarely near the
    // front.
    const walk = (x, y, p) => {
      if (JSON.stringify(x) === JSON.stringify(y)) return null;
      if (x && y && typeof x === 'object' && typeof y === 'object') {
        for (const k of new Set([...Object.keys(x), ...Object.keys(y)])) {
          const r = walk(x[k], y[k], p + '.' + k);
          if (r) return r;
        }
      }
      return p + '\n    live: ' + JSON.stringify(x) + '\n    port: ' + JSON.stringify(y);
    };
    shout('DIFF %s at %s', what, walk(JSON.parse(a), JSON.parse(b), ''));
  }
}

const FROM = 1756000000000;
const PG = (o) => Object.assign({ ts: FROM, rtt_ms: 12.5, loss_pct: 0 }, o);
const TR = (o) => Object.assign({ ts: FROM, iface: 'ether1', rx_mbps: 12.5, tx_mbps: 3.25 }, o);
const BW = (o) => Object.assign({ ts: FROM, iface: 'ether1', rx_mb: 125.5, tx_mb: 40.25 }, o);
const SUM = (o) => Object.assign({
  // THE REAL KEY NAMES. This fixture said `maxRx / maxTx / avgRx / avgTx /
  // p95Rx / p95Tx` until 2026-08-24 and NO implementation has ever read any of
  // them — the payload calls them `rxMaxMbps`, `rxAvgMbps`, `rxP95Mbps` and so
  // on. So every stat card rendered its em-dash branch on both sides, agreed,
  // and the gate reported identical while covering none of them. Found by
  // `tools/fixture-key-audit.js`, which exists because the same defect hid the
  // whole DHCP half of the WAN gate.
  trafficSamples: 1440, bandwidthSamples: 1200,
  rxAvgMbps: 20, txAvgMbps: 10,
  rxMaxMbps: 100, txMaxMbps: 50,
  rxP95Mbps: 80, txP95Mbps: 40,
  rxTotalMb: 4096, txTotalMb: 1024,
  rxMaxMb: 900, txMaxMb: 300,
  rxPeakPct: 10, txPeakPct: 50,
  capacityDownMbps: 1000, capacityUpMbps: 100,
}, o);

const series = (n, mk) => Array.from({ length: n }, (_, i) => mk(i));

const LIVE_CALL = {
  ping: (m, c) => m.renderPingChart(c.rows),
  traffic: (m, c, doc) => { doc.nodes.rptAggregate.value = c.agg || ''; return m.renderTrafficChart(c.rows, c.summary || null); },
  bandwidth: (m, c, doc) => { doc.nodes.rptAggregate.value = c.agg || ''; return m.renderBandwidthChart(c.rows); },
};
const PORT_CALL = {
  ping: (m, c) => m.renderPingChart(c.rows),
  traffic: (m, c) => m.renderTrafficChart(c.rows, c.summary || null, c.agg || ''),
  bandwidth: (m, c) => m.renderBandwidthChart(c.rows, c.agg || ''),
};
const C = (fn, rows, extra) => Object.assign({ fn, rows }, extra || {});

const CASES = {
  'ping: none': C('ping', []),
  'ping: one point': C('ping', [PG({})]),
  'ping: a null rtt': C('ping', [PG({ rtt_ms: null })]),
  'ping: total loss': C('ping', [PG({ rtt_ms: null, loss_pct: 100 })]),
  'ping: a few points': C('ping', series(10, (i) => PG({ ts: FROM + i * 60000, rtt_ms: i }))),
  // Downsampling: at most 300 points, every Nth kept.
  'ping: exactly 300 points': C('ping', series(300, (i) => PG({ ts: FROM + i * 60000, rtt_ms: i }))),
  'ping: 301 points downsamples': C('ping', series(301, (i) => PG({ ts: FROM + i * 60000, rtt_ms: i }))),
  'ping: 1000 points downsamples harder': C('ping', series(1000, (i) => PG({ ts: FROM + i * 60000, rtt_ms: i }))),
  'traffic: none': C('traffic', []),
  'traffic: one point': C('traffic', [TR({})], { summary: SUM({}) }),
  'traffic: no summary': C('traffic', [TR({})]),
  'traffic: a few points': C('traffic', series(10, (i) => TR({ ts: FROM + i * 60000, rx_mbps: i }))),
  'traffic: aggregated': C('traffic', series(10, (i) => TR({ ts: FROM + i * 60000 })), { summary: SUM({}), agg: 'hour' }),
  'traffic: 500 points downsamples': C('traffic', series(500, (i) => TR({ ts: FROM + i * 60000, rx_mbps: i })), { summary: SUM({}) }),
  'traffic: no capacity configured': C('traffic', [TR({})], { summary: SUM({ capacityDownMbps: 0, capacityUpMbps: 0 }) }),
  'bandwidth: none': C('bandwidth', []),
  'bandwidth: one point': C('bandwidth', [BW({})]),
  'bandwidth: a few points': C('bandwidth', series(10, (i) => BW({ ts: FROM + i * 3600000, rx_mb: i }))),
  'bandwidth: aggregated': C('bandwidth', series(10, (i) => BW({ ts: FROM + i * 3600000 })), { agg: 'hour' }),
  'bandwidth: 400 points': C('bandwidth', series(400, (i) => BW({ ts: FROM + i * 3600000, rx_mb: i }))),
  'bandwidth: zero volumes': C('bandwidth', [BW({ rx_mb: 0, tx_mb: 0 })]),
};

for (const capacity of [false, true]) {
  const tag = capacity ? ' [capacity shown]' : '';
  for (const [name, c] of Object.entries(CASES)) {
    let a, b;
    try { a = G.live(name, () => liveRun((m, d) => LIVE_CALL[c.fn](m, c, d), capacity)); }
    catch (e) { shout('LIVE THREW on %s%s: %s', name, tag, e.message); bad++; checked++; continue; }
    try { b = portRun((m, d) => PORT_CALL[c.fn](m, c, d), capacity); }
    catch (e) { shout('PORT THREW on %s%s: %s', name, tag, e.message); bad++; checked++; continue; }
    cmp(name + tag, a, b);
  }
}

// ── believability ──────────────────────────────────────────────────────────
{
  const s = JSON.parse(G.live('auto:4', () => liveRun((m) => m.renderPingChart([PG({}), PG({ ts: FROM + 60000 })]), false)));
  assert.equal(s.length, 1, 'the live ping chart was not constructed: ' + s.length);
  assert.equal(s[0].id, 'rptPingChart', 'the chart went to the wrong canvas: ' + s[0].id);
  assert.ok(s[0].config.data, 'the chart config carries no data');
  assert.ok(JSON.stringify(s[0].config).length > 200, 'the captured config is suspiciously small');
}
{
  // Downsampling really happens, and 300 is the boundary.
  const at = JSON.parse(G.live('auto:3', () => liveRun((m) => m.renderPingChart(series(300, (i) => PG({ ts: FROM + i * 60000 }))), false)));
  const over = JSON.parse(G.live('auto:2', () => liveRun((m) => m.renderPingChart(series(600, (i) => PG({ ts: FROM + i * 60000 }))), false)));
  const n = (c) => JSON.stringify(c[0].config.data.labels || []).length;
  assert.ok(n(at) > 0, '300 points produced no labels');
  assert.ok(n(over) <= n(at) * 1.2,
    '600 points were not downsampled — the cap is not holding');
}
{
  // A callback's OUTPUT is compared, not merely its presence — and the assertion
  // has to be pointed at a chart that HAS one. The ping and traffic charts carry
  // no callbacks; the BANDWIDTH chart carries two, a tooltip label and an axis
  // tick. Asserting on the ping chart was the assertion being wrong, not the
  // code, and it is worth keeping the check rather than dropping it: `freeze()`
  // silently eliding functions would compare two charts that agree about having
  // a formatter and disagree about what it prints.
  const s = JSON.parse(G.live('auto:1', () => liveRun((m) => m.renderBandwidthChart([BW({}), BW({ ts: FROM + 3600000 })]), false)));
  const text = JSON.stringify(s[0].config);
  assert.match(text, /__fn/, 'no callback was captured — freeze() is eliding functions');
  assert.ok(!/"__fn":\[\]/.test(text), 'a callback was captured but never invoked');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('reports-charts-check: %d cases identical', checked);
