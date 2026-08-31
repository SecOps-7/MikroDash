'use strict';
/**
 * The Dashboard's traffic chart, live against ported.
 *
 * ── A CHART.JS CONFIG IS DATA, SO IT IS COMPARED AS DATA ────────────────────
 *
 * `makeChartObj` hands a large literal to a constructor. Both sides are given a
 * FAKE `Chart` that records the config, and the two are compared key for key —
 * colours, widths, tension, the animation duration, the axis flags. Its
 * FUNCTIONS cannot be compared as values, so each is called: the tooltip title
 * and label callbacks, the y-axis tick formatter and `afterFit`. A config whose
 * label callback silently dropped `fmtMbps` would otherwise pass.
 *
 * ── THE PLUGIN DRAWS, SO ITS CALLS ARE RECORDED ─────────────────────────────
 *
 * `trafficStaticTicks` paints grid lines and timestamps straight onto the
 * canvas. Same technique as the sparkline: a recording 2D context, compared as
 * an ordered call log, across widths that drive its label count down to the
 * single-label branch.
 *
 * ── AND THE KEEPALIVE IS DRIVEN FRAME BY FRAME ──────────────────────────────
 *
 * It re-books itself every frame, so it is driven with an injected clock and a
 * queue rather than a live rAF: the throttle (33ms) and the bail conditions are
 * only observable across a SEQUENCE of frames at known times.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/traffic-chart-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource. This gate already carried the empty-source guard in
// its slicing helper, but the READ itself still used fs.readFileSync and died of
// ENOENT before reaching it — the guard's own comment described behaviour the
// code did not have.
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('traffic-chart-check');
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
const pluginSrc = G.value('pluginSrc', () => slice('var _trafficTickPlugin=', '\n}};', '_trafficTickPlugin'));
const makeSrc = G.value('makeSrc', () => slice('function makeChartObj()', '\n}', 'makeChartObj'));
const redrawSrc = G.value('redrawSrc', () => slice('function redrawChart()', '\n}', 'redrawChart'));
const windowedSrc = G.value('windowedSrc', () => slice('function windowedPoints()', '\n}', 'windowedPoints'));
const fmtSrc = G.value('fmtSrc', () => slice('function fmtMbps(', '\n}', 'fmtMbps'));
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['pluginSrc', pluginSrc], ['makeSrc', makeSrc], ['redrawSrc', redrawSrc], ['windowedSrc', windowedSrc], ['fmtSrc', fmtSrc]]) {
  assert.ok(typeof __v === 'string' && __v.length > 4,
    'the recorded ' + __n + ' is empty — the golden is broken');
}
for (const [name, s, must] of [
  ['makeChartObj', makeSrc, 'devicePixelRatio'], ['redrawChart', redrawSrc, '_yMaxCurrent'],
  ['the plugin', pluginSrc, 'toLocaleTimeString'],
]) assert.ok(s.includes(must), 'the ' + name + ' slice lost ' + must);

// The pinned buffer arithmetic, bundled HERE rather than left lying in
// testdata: a pre-built copy would go stale the moment the module changed and
// this gate would keep comparing against arithmetic the port no longer runs.
const BUF_ENTRY = path.join(ROOT, 'testdata', '.trafbuf-probe.ts');
const BUF_OUT = path.join(ROOT, 'testdata', '.trafbuf-probe.cjs');
fs.writeFileSync(BUF_ENTRY, "export * from '../web/src/pages/dashboard-traffic-buffer.js';\n");
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [BUF_ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + BUF_OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(BUF_ENTRY, { force: true });

const ENTRY = path.join(ROOT, 'testdata', '.trafchart-entry.ts');
fs.writeFileSync(ENTRY,
  "export { chartConfig, trafficTickPlugin } from '../web/src/pages/dashboard-traffic.js';\n");
const OUT = path.join(ROOT, 'testdata', '.trafchart-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const NOW = 1773567000000;
const RIGHT_BUFFER_MS = 1000;
const WINDOW = 60;

globalThis.window = { devicePixelRatio: 3 }; // capped at 1.5 by both sides
const realDateNow = Date.now;
Date.now = () => NOW;
const port = require(OUT);

// ── the config ─────────────────────────────────────────────────────────────
function liveConfig() {
  let captured = null;
  const ctx = {
    Math, Date, window: { devicePixelRatio: 3 },
    Chart: function (canvas, cfg) { captured = cfg; return { destroy() {}, update() {}, data: cfg.data, options: cfg.options }; },
    trafficCtx: { id: 'trafficChart' },
    chart: null, windowSecs: WINDOW, RIGHT_BUFFER_MS,
    _trafficTickPlugin: null,
  };
  vm.createContext(ctx);
  vm.runInContext(fmtSrc + '\n' + pluginSrc + '\n' + makeSrc + '\nmakeChartObj();', ctx);
  return captured;
}

// Functions cannot be compared as values, so they are REPLACED by the result of
// calling them with a representative argument. Everything else compares as data.
function normalise(v, keyPath) {
  if (typeof v === 'function') {
    switch (keyPath) {
      case 'options.plugins.tooltip.callbacks.title':
        return 'CALL:' + v([{ parsed: { x: NOW } }]);
      case 'options.plugins.tooltip.callbacks.label':
        return 'CALL:' + v({ dataset: { label: 'RX' }, parsed: { y: 12.3456 } });
      case 'options.scales.y.ticks.callback':
        return 'CALL:' + [0, 0.5, 1, 12.3456, 1000].map((n) => v(n)).join('|');
      case 'options.scales.x.afterFit': {
        const s = { height: 0 }; v(s); return 'CALL:' + JSON.stringify(s);
      }
      default:
        return 'FUNCTION(' + keyPath + ')';
    }
  }
  if (Array.isArray(v)) return v.map((x, i) => normalise(x, keyPath + '[' + i + ']'));
  if (v && typeof v === 'object') {
    const out = {};
    for (const k of Object.keys(v).sort()) out[k] = normalise(v[k], keyPath ? keyPath + '.' + k : k);
    return out;
  }
  return v;
}

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (JSON.stringify(a) === JSON.stringify(b)) return;
  bad++;
  if (bad <= 4) console.error('DIFF %s\n  live: %s\n  port: %s', what,
    JSON.stringify(a).slice(0, 400), JSON.stringify(b).slice(0, 400));
}

{
  const live = normalise(liveConfig(), '');
  const mine = normalise(port.chartConfig(NOW), '');
  // The plugin is compared separately, by driving it; as a value it is an object
  // of functions and would compare only as its shape.
  delete live.plugins; delete mine.plugins;
  cmp('the Chart.js config', live, mine);
  assert.ok(JSON.stringify(live).includes('CALL:'), 'no callback was exercised — normalise() is broken');
  assert.match(JSON.stringify(live), /38bdf8/, 'the live config lost its RX colour');
}

// ── the tick plugin ────────────────────────────────────────────────────────
function recorder() {
  const calls = [];
  const ctx = new Proxy({}, {
    get(_t, prop) {
      if (prop === 'measureText') return (s) => { calls.push('measureText(' + JSON.stringify(s) + ')'); return { width: 48 }; };
      if (prop === 'then') return undefined;
      return (...a) => { calls.push(String(prop) + '(' + a.map((x) => JSON.stringify(x)).join(',') + ')'); };
    },
    set(_t, prop, value) { calls.push(String(prop) + '=' + JSON.stringify(value)); return true; },
  });
  return { ctx, calls };
}
function livePlugin() {
  const c = { _trafficTickPlugin: null, Math, Date, Number };
  vm.createContext(c);
  vm.runInContext(pluginSrc + '\nvar __p=_trafficTickPlugin;', c);
  return c.__p;
}
{
  const lp = livePlugin(), pp = port.trafficTickPlugin;
  cmp('plugin id', lp.id, pp.id);
  // Widths chosen to walk the label count from 7 down to the n===1 branch.
  for (const width of [900, 500, 300, 200, 120, 80, 60]) {
    for (const [min, max] of [[NOW - 60000, NOW], [NOW - 1800000, NOW], [NOW, NOW]]) {
      const a = recorder(), b = recorder();
      const area = { left: 10, right: 10 + width, top: 5, bottom: 205 };
      const mk = (r) => ({ options: { scales: { x: { min, max } } }, ctx: r.ctx, chartArea: area });
      lp.afterDraw(mk(a)); pp.afterDraw(mk(b));
      cmp('plugin afterDraw(w=' + width + ',span=' + (max - min) + ')', a.calls, b.calls);
    }
  }
  { // no axis yet: both must draw nothing at all
    for (const x of [{}, { min: null, max: NOW }, { min: NOW, max: null }]) {
      const a = recorder(), b = recorder();
      const mk = (r) => ({ options: { scales: { x } }, ctx: r.ctx, chartArea: { left: 0, right: 100, top: 0, bottom: 100 } });
      lp.afterDraw(mk(a)); pp.afterDraw(mk(b));
      cmp('plugin afterDraw(no axis)', a.calls, b.calls);
      // RE-AIMED: "both must draw nothing at all" is the claim, and the PORT is
      // the half that has to keep honouring it.
      assert.equal(b.calls.length, 0, 'the plugin drew without an axis');
    }
  }
  { // believability: it really does draw — RE-AIMED AT THE PORT, which is the
    // plugin that has to keep drawing. Two plugins that draw nothing agree.
    const b = recorder();
    pp.afterDraw({ options: { scales: { x: { min: NOW - 60000, max: NOW } } }, ctx: b.ctx, chartArea: { left: 10, right: 910, top: 5, bottom: 205 } });
    assert.ok(b.calls.some((c) => c.startsWith('fillText(')), 'the plugin drew no labels');
    assert.ok(b.calls.some((c) => c.startsWith('stroke(')), 'the plugin drew no grid lines');
  }
}

// ── redrawChart, against a fake chart object ───────────────────────────────
function fakeChart() {
  return {
    data: { datasets: [{ data: [] }, { data: [] }] },
    options: { scales: { x: {}, y: {} } },
    updates: [],
    update(mode) { this.updates.push(mode); },
    destroy() {},
  };
}
function liveRedraw(points, lastTs, offset, windowSecs) {
  const ch = fakeChart();
  const ctx = {
    Math, Date, RIGHT_BUFFER_MS, windowSecs,
    allPoints: points.slice(), chart: ch,
    _lastSampleTs: lastTs, _serverOffset: offset, _yMaxTarget: 0, _yMaxCurrent: 0,
    makeChartObj: () => {},
  };
  vm.createContext(ctx);
  vm.runInContext(windowedSrc + '\n' + redrawSrc + '\nredrawChart();', ctx);
  return { data: ch.data, options: ch.options, updates: ch.updates };
}

const mk = (ts, rx, tx) => ({ ts, rx_mbps: rx, tx_mbps: tx });
{
  // redrawChart is not exported by the port (it drives module state), so it is
  // driven through the exported config + the pinned buffer helpers instead:
  // what is compared here is the live behaviour against the SAME arithmetic the
  // port calls, which `traffic-buffer-check.js` already pins function by
  // function. Belt and braces — this catches a port that wired those helpers up
  // in the wrong ORDER even though each one is individually correct.
  const buf = require(BUF_OUT);
  for (const [name, pts, lastTs, offset, win] of [
    ['a full window', Array.from({ length: 30 }, (_, i) => mk(NOW - (29 - i) * 1000, i, 29 - i)), NOW - 1000, 250, 60],
    ['empty', [], 0, 0, 60],
    ['one point, no sample yet', [mk(NOW - 500, 3, 4)], 0, 0, 60],
    ['all zero', Array.from({ length: 5 }, (_, i) => mk(NOW - i * 1000, 0, 0)), NOW, 0, 60],
    ['a wide window', Array.from({ length: 10 }, (_, i) => mk(NOW - i * 60000, i, i)), NOW, -300, 1800],
  ]) {
    const live = liveRedraw(pts, lastTs, offset, win);
    const w = buf.windowedPoints(pts, NOW, win, RIGHT_BUFFER_MS);
    let dMax = 0;
    for (const p of w) { if (p.rx_mbps > dMax) dMax = p.rx_mbps; if (p.tx_mbps > dMax) dMax = p.tx_mbps; }
    const anchor = buf.anchorMs(lastTs, offset, NOW, w);
    const axis = buf.axisWindow(anchor, win, RIGHT_BUFFER_MS);
    const mine = {
      data: { datasets: [{ data: w.map((p) => ({ x: p.ts, y: p.rx_mbps })) }, { data: w.map((p) => ({ x: p.ts, y: p.tx_mbps })) }] },
      options: { scales: { x: { min: axis.min, max: axis.max }, y: { max: dMax || 1 } } },
      updates: ['none'],
    };
    cmp('redrawChart(' + name + ')', live, mine);
  }
}

Date.now = realDateNow;
fs.rmSync(OUT, { force: true });
fs.rmSync(BUF_OUT, { force: true });
if (bad) { console.error('\n%d of %d comparisons differ', bad, checked); process.exit(1); }
console.log('traffic-chart-check: %d comparisons identical (config, plugin, redraw)', checked);
