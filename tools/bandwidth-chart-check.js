#!/usr/bin/env node
'use strict';
/**
 * The Bandwidth page's compact chart, live against ported.
 *
 * ── IT IS NOT THE DASHBOARD CHART, AND THE DIFFERENCE IS ONE CONSTANT ───────
 *
 * `_syncBwChart` (app.js:6947) is the dashboard's `redrawChart` with the same
 * max, the same anchor and the same axis window — and a DIFFERENT set of points.
 * The dashboard seeds from `windowedPoints()` (app.js:774), which filters at
 * `ts >= cutoff`. This one walks back to `cutoff - 3000` and keeps three seconds
 * more, matching what its own keepalive prunes to.
 *
 * A port that reused the dashboard's helper here would look right, pass any test
 * written from the dashboard's behaviour, and show up as a one-frame flicker at
 * the left edge. So the corpus carries points that fall INSIDE that three-second
 * band and nowhere else — without them the two selections are identical and this
 * gate proves nothing.
 *
 * ── COMPARED AS DATA ────────────────────────────────────────────────────────
 *
 * The live function writes onto a Chart.js instance; the port returns what it
 * would write. So the live side is given a fake chart that records, and the two
 * are compared field for field: both datasets, the y max and both x extents.
 *
 * ---- AND THE CANVAS, ADDED 2026-08-25 --------------------------------------
 *
 * This gate compared the CONFIG and nothing else. `_bwChartCtx` was a `{}`
 * placeholder and the Chart stub threw its first argument away, so a port that
 * built the chart on the wrong element — or on none — produced an identical
 * config and passed. That is the hole `map-fs-check` records for the fullscreen
 * slots, in a second place: the payload was checked and the WIRING was not.
 *
 * Both sides now resolve the canvas the way the page does (the live side through
 * `$('bwTrafficChart')`, as its module scope does; the port by being MOUNTED and
 * given one sample, since both build the chart lazily on the first
 * `traffic:update`), and what reaches the constructor is compared by id.
 *
 * MUTATIONS (four, all killed, each with a named message):
 *   build the chart on the wrong element   1/28  "chart canvas live/port"
 *   pass no canvas at all                  1/28  ditto
 *   never construct the chart              1/28  "the PORT never constructed…"
 *   skip makeChart on the first sample     1/28  ditto
 *
 *   MIKRODASH_SRC=../MikroDash node tools/bandwidth-chart-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc } = require('./lib/dom-shim.js');

// Every id the bandwidth page looks up, so the mount below does not return early
// on a missing element. Kept beside `bandwidth-page-check`'s list deliberately:
// that gate covers the table, this one the chart, and both mount the same page.
const PAGE_IDS = ['bwTbody', 'bwStats', 'bwSearch', 'bwIface', 'bwScope', 'bwIpver', 'bwTopN',
  'bwThDevice', 'bwThDst', 'bwThRx', 'bwThTx', 'bwThTotal', 'bwThIface', 'bwThProto', 'bwThOrg',
  'bwTrafficChart', 'bwLiveRxNum', 'bwLiveRxUnit', 'bwLiveTxNum', 'bwLiveTxUnit'];

const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('bandwidth-chart-check');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource, which yields '' when the reference is gone rather
// than throwing ENOENT. What the gate lifts from it is frozen below.
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

const START = 'function _syncBwChart(animated) {';
const from = src.indexOf(START);
if (LIFT.hasReference(ROOT)) assert.ok(from > 0, '_syncBwChart has moved in app.js');
const END = '\n  }';
const to = src.indexOf(END, from);
// 2600, not 1600: ToDo #24's fix landed on 2026-08-25 and the function grew —
// the backward walk became a filter and picked up eight lines of comment
// explaining why the three-second slack is NOT part of the bug. A bound that
// tracks the function's length is a bound that fails on every edit, so it is
// generous and the `must` list below is what actually pins the content.
if (LIFT.hasReference(ROOT)) assert.ok(to > from && to - from < 2600, '_syncBwChart is not where its anchors say');
const syncSrc = G.value('syncSrc', () => src.slice(from, to + END.length));
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['syncSrc', syncSrc]]) {
  if (typeof __v !== 'string' || __v.length <= 4) {
    throw new Error('the recorded ' + __n + ' is empty — the golden is broken');
  }
}

// `- 3000` rather than `cutoff - 3000`: the slack moved from the COMPARISON into
// the cutoff itself. Pinning the old spelling would have made this gate demand
// the shape of a bug it had reported.
for (const must of ['- 3000', 'allPoints.filter', 'scales.x.min', 'scales.x.max', '_bwYMaxTarget']) {
  assert.ok(syncSrc.includes(must), 'the lifted function lost: ' + must);
}
for (const mustNot of ['_bwTick', 'requestAnimationFrame', '_updateBwStats']) {
  if (LIFT.hasReference(ROOT)) assert.ok(!syncSrc.includes(mustNot), 'the slice over-read and took in: ' + mustNot);
}

function lift(decl, name, must, mustNot, maxLen) {
  const at = src.indexOf(decl);
  if (LIFT.hasReference(ROOT)) assert.ok(at > 0, name + ' has moved in app.js');
  const end = src.indexOf('\n  }', at);
  if (LIFT.hasReference(ROOT)) assert.ok(end > at && end - at < maxLen, name + ' is not where its anchors say');
  const body = src.slice(at, end + 4);
  for (const m of must) assert.ok(body.includes(m), name + ' lost: ' + m);
  for (const m of mustNot) assert.ok(!body.includes(m), name + ' over-read and took in: ' + m);
  return body;
}
const makeSrc = G.value('makeSrc', () => lift('function _makeBwChart() {', '_makeBwChart',
  ['maxTicksLimit:4', 'beginAtZero:true', "size:10"], ['_syncBwChart', 'requestAnimationFrame'], 2600));
const tickSrc = G.value('tickSrc', () => lift('function _bwTick() {', '_bwTick',
  ['0.08', 'vl - 3000', 'scales.x.max'], ['_makeBwChart', '_syncBwChart'], 1400));
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['makeSrc', makeSrc], ['tickSrc', tickSrc]]) {
  if (typeof __v !== 'string' || __v.length <= 4) {
    throw new Error('the recorded ' + __n + ' is empty — the golden is broken');
  }
}

const ENTRY = path.join(ROOT, 'testdata', '.bwchart-entry.ts');
const OUT = path.join(ROOT, 'testdata', '.bwchart-port.cjs');
fs.writeFileSync(ENTRY, "export { bwSyncState, bwChartConfig, bwTickState, initBandwidthPage } " +
  "from '../web/src/pages/bandwidth';\n");
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const mod = require(OUT);
const { bwSyncState, bwChartConfig, bwTickState } = mod;

const RIGHT_BUFFER_MS = 1000;
const NOW = 1773567000000;

function liveSync(points, clock) {
  const ch = {
    data: { datasets: [{ data: [] }, { data: [] }] },
    options: { scales: { x: {}, y: {} } },
    updates: [],
    update(mode) { this.updates.push(mode); },
  };
  const ctx = {
    Math,
    Date: { now: () => NOW },
    RIGHT_BUFFER_MS, windowSecs: clock.windowSecs,
    allPoints: points.slice(),
    _bwChart: ch,
    _lastSampleTs: clock.lastSampleTs, _serverOffset: clock.serverOffset,
    _bwYMaxTarget: 0, _bwYMaxCurrent: 0,
  };
  vm.createContext(ctx);
  vm.runInContext(syncSrc + '\n_syncBwChart(false);', ctx);
  return {
    rx: ch.data.datasets[0].data, tx: ch.data.datasets[1].data,
    yMax: ch.options.scales.y.max, xMin: ch.options.scales.x.min, xMax: ch.options.scales.x.max,
  };
}

// OLDEST FIRST in every fixture. `allPoints` is append-ordered, and the live
// selection walks it from the END, so a newest-first fixture makes the live side
// break on its first step and report an empty chart — a difference invented by
// the harness rather than found in the code. Three cases were built that way and
// had to be corrected before the real divergence below could be believed.
const mk = (ts, rx, tx) => ({ ts, rx_mbps: rx, tx_mbps: tx });
const CLOCK = (o) => Object.assign({ lastSampleTs: NOW - 1000, serverOffset: 0, windowSecs: 60 }, o);

// The window edge, in milliseconds before NOW, for a 60s window:
//   inside            <= 61000
//   the 3s band       61000 .. 64000   <-- only these separate the two selections
//   dropped by both   >  64000
const CASES = {
  'a full window': [Array.from({ length: 30 }, (_, i) => mk(NOW - (29 - i) * 1000, i, 29 - i)), CLOCK()],
  'empty': [[], CLOCK({ lastSampleTs: 0 })],
  'one point, no sample yet': [[mk(NOW - 500, 3, 4)], CLOCK({ lastSampleTs: 0 })],
  'all zero rates': [Array.from({ length: 5 }, (_, i) => mk(NOW - (4 - i) * 1000, 0, 0)), CLOCK()],

  // ── THE THREE-SECOND BAND ────────────────────────────────────────────────
  // Each of these is kept by this chart and dropped by the dashboard's helper.
  'a point just inside the band (61.5s)': [[mk(NOW - 61500, 5, 6), mk(NOW - 1000, 1, 2)], CLOCK()],
  'a point at the far edge of the band (63.9s)': [[mk(NOW - 63900, 9, 9), mk(NOW - 1000, 1, 2)], CLOCK()],
  'a point just PAST the band (64.5s) is dropped by both':
    [[mk(NOW - 64500, 9, 9), mk(NOW - 1000, 1, 2)], CLOCK()],
  'a point exactly on the window edge (61s)': [[mk(NOW - 61000, 4, 4), mk(NOW - 1000, 1, 2)], CLOCK()],
  'the band holds the ONLY points': [[mk(NOW - 62000, 7, 3), mk(NOW - 63000, 2, 8)], CLOCK()],
  'the band holds the MAXIMUM': [[mk(NOW - 62000, 99, 0), mk(NOW - 1000, 1, 2)], CLOCK()],

  // Clock and window variations.
  'no sample yet, so the anchor is the last point':
    [[mk(NOW - 5000, 1, 1), mk(NOW - 2000, 2, 2)], CLOCK({ lastSampleTs: 0 })],
  'a positive server offset': [[mk(NOW - 3000, 1, 1)], CLOCK({ serverOffset: 250 })],
  'a negative server offset': [[mk(NOW - 3000, 1, 1)], CLOCK({ serverOffset: -300 })],
  'a wide window': [Array.from({ length: 10 }, (_, i) => mk(NOW - (9 - i) * 60000, i, i)), CLOCK({ windowSecs: 1800 })],
  'a narrow window': [Array.from({ length: 20 }, (_, i) => mk(NOW - (19 - i) * 1000, i, i)), CLOCK({ windowSecs: 10 })],
  // Out of order, which traffic:history can genuinely deliver after an NTP step.
  'non-monotonic timestamps': [[mk(NOW - 1000, 1, 1), mk(NOW - 70000, 5, 5), mk(NOW - 2000, 2, 2)], CLOCK()],
  'tx larger than rx': [[mk(NOW - 1000, 1, 50)], CLOCK()],
};

let bad = 0, checked = 0;
for (const [name, [points, clock]] of Object.entries(CASES)) {
  checked++;
  const a = liveSync(points, clock);
  const b = bwSyncState(points, NOW, clock, RIGHT_BUFFER_MS);
  const A = JSON.stringify(a), B = JSON.stringify(b);
  if (A !== B) {
    bad++;
    console.error('%s\n  live: %s\n  port: %s', name, A, B);
  }
}

// ── the config, compared as data with its callbacks CALLED ─────────────────
//
// A config is a literal handed to a constructor, so both sides are compared key
// for key. Functions cannot be compared as values — two identical closures are
// never equal — so each is invoked and its RESULT compared. A label callback
// that quietly dropped `fmtMbps` would otherwise pass, which is the same trap
// traffic-chart-check documents for the dashboard's config.
{
  const WIN = 60;
  // `fmtMbps` is LIFTED from app.js, not stubbed. A stub here would be compared
  // against the port's real one and report a difference that is the harness's,
  // not the port's — which is exactly what the first run of this block did.
  // FROZEN — the DEFINITION LINE, since `fmtMbps` is built from it by
  // `new Function`. Inside a block, so no module-scope pattern reached it.
  const fmtLine = G.value('the live fmtMbps definition', () => {
    const t = src.slice(src.indexOf('function fmtMbps('));
    return t.slice(0, t.indexOf('\n'));
  });
  if (!/^function fmtMbps\(/.test(fmtLine)) throw new Error('the recorded fmtMbps is not one');
  const fmtMbps = new Function(fmtLine + '\n return fmtMbps;')();

  // A Date that is BOTH constructible and has `now`: the config seeds its axis
  // from `Date.now()` and the tooltip title does `new Date(x)`. Replacing it
  // with `{ now }` alone made the title callback throw, which read as a port
  // defect until the harness was looked at.
  function PinnedDate(...a) { return a.length ? new Date(...a) : new Date(NOW); }
  PinnedDate.now = () => NOW;

  // THE CANVAS IS AN ARGUMENT, AND IT WAS NOT BEING COMPARED.
  //
  // `_bwChartCtx` was `{}` here — a placeholder — and the Chart stub threw its
  // first argument away, so this gate compared the CONFIG and nothing else. A
  // port that handed Chart the wrong element, or a stale one, or nothing,
  // produced an identical config and passed. That is the same hole
  // `map-fs-check` records for the fullscreen slots: the payload was checked and
  // the WIRING was not.
  //
  // Both sides now resolve it from a document the same way the page does, and
  // what reaches the constructor is compared by id.
  const doc = makeDoc(['bwTrafficChart'], {});
  doc.nodes.bwTrafficChart.id = 'bwTrafficChart';
  const ctx = {
    Math, Date: PinnedDate,
    document: doc,
    Chart: function (canvas, cfg) { ctx.captured = cfg; ctx.canvas = canvas; return { destroy() {} }; },
    windowSecs: WIN, RIGHT_BUFFER_MS,
    // Resolved through `$`, exactly as the live module scope does
    // (`var _bwChartCtx = $('bwTrafficChart')`), rather than stubbed.
    _bwChart: null, fmtMbps,
    captured: null, canvas: null,
  };
  vm.createContext(ctx);
  vm.runInContext('function $(id){return document.getElementById(id);}\n' +
    "var _bwChartCtx = $('bwTrafficChart');\n" + makeSrc + '\n_makeBwChart();', ctx);
  const live = ctx.captured;
  const port = bwChartConfig(NOW, WIN, RIGHT_BUFFER_MS);

  // ── AND THE PORT'S OWN WIRING, DRIVEN THROUGH THE PAGE ──────────────────
  //
  // `bwChartConfig` is exported and compared above, but the page is what decides
  // WHICH ELEMENT the config is attached to, and that is a separate question.
  // The port is mounted and given one sample — both sides build the chart lazily
  // on the first `traffic:update` when none exists — with a Chart stub that
  // records its first argument.
  const portCanvas = (() => {
    const pdoc = makeDoc(PAGE_IDS, {});
    pdoc.nodes.bwTrafficChart.id = 'bwTrafficChart';
    let seen = { called: false, canvas: null };
    const prev = { doc: globalThis.document, win: globalThis.window,
                   raf: globalThis.requestAnimationFrame, caf: globalThis.cancelAnimationFrame };
    // The keepalive books itself on the next frame. It is NEVER RUN here — this
    // block is about which element the chart is built on, and the keepalive has
    // its own block below that drives it frame by frame with an injected clock.
    // Returning 1 rather than calling back is what keeps those two separate.
    globalThis.requestAnimationFrame = () => 1;
    globalThis.cancelAnimationFrame = () => {};
    globalThis.document = pdoc;
    globalThis.window = {
      // The instance exposes the CONFIG's own `data` and `options`, which is what
      // Chart.js does — inventing an empty `options` here made `syncChart` throw
      // on `scales.y`, and that read as a port defect until the stub was looked
      // at. A shim thin enough to run is not thick enough to compare.
      Chart: function (canvas, cfg) {
        seen = { called: true, canvas };
        return { destroy() {}, update() {}, data: cfg.data, options: cfg.options };
      },
    };
    try {
      const handlers = {};
      mod.initBandwidthPage({ on: (ev, fn) => { handlers[ev] = fn; }, emit() {} },
        () => true);
      if (!handlers['traffic:update']) throw new Error('the port registered no traffic:update handler');
      handlers['traffic:update']({ ts: NOW, rx_mbps: 1, tx_mbps: 2 });
      return seen;
    } finally {
      for (const [k, g] of [['doc', 'document'], ['win', 'window'],
                            ['raf', 'requestAnimationFrame'], ['caf', 'cancelAnimationFrame']]) {
        if (prev[k] === undefined) delete globalThis[g]; else globalThis[g] = prev[k];
      }
    }
  })();

  checked++;
  const liveCanvasId = ctx.canvas && ctx.canvas.id;
  const portCanvasId = portCanvas.canvas && portCanvas.canvas.id;
  if (!portCanvas.called) {
    bad++;
    console.error('the PORT never constructed a chart on its first sample — ' +
      'the canvas it would use cannot be compared');
  } else if (liveCanvasId !== portCanvasId) {
    bad++;
    console.error('chart canvas\n  live: %s\n  port: %s', liveCanvasId, portCanvasId);
  }
  // BELIEVABILITY: if BOTH resolved to nothing the comparison above is two
  // `undefined`s agreeing, which is what the placeholder `{}` used to guarantee.
  assert.equal(liveCanvasId, 'bwTrafficChart',
    'the LIVE chart was not built on #bwTrafficChart — this comparison is vacuous');

  const callables = [
    ['tooltip title', (c) => c.options.plugins.tooltip.callbacks.title([{ parsed: { x: NOW } }])],
    ['tooltip label', (c) => c.options.plugins.tooltip.callbacks.label(
      { dataset: { label: 'RX' }, parsed: { y: 12.5 } })],
    ['y tick 0', (c) => c.options.scales.y.ticks.callback(0)],
    ['y tick 12.5', (c) => c.options.scales.y.ticks.callback(12.5)],
    ['y tick 1234', (c) => c.options.scales.y.ticks.callback(1234)],
  ];
  for (const [name, call] of callables) {
    checked++;
    let a, b;
    try { a = call(live); } catch (e) { a = 'THREW ' + e.message; }
    try { b = call(port); } catch (e) { b = 'THREW ' + e.message; }
    if (a !== b) { bad++; console.error('config %s\n  live: %s\n  port: %s', name, a, b); }
  }

  // Then the data half, with every function replaced by a marker so the
  // structure is comparable and a MISSING function is still visible.
  const shape = (o) => JSON.parse(JSON.stringify(o, (k, v) => (typeof v === 'function' ? '<fn>' : v)));
  checked++;
  const A = JSON.stringify(shape(live), null, 1), B = JSON.stringify(shape(port), null, 1);
  if (A !== B) {
    bad++;
    const al = A.split('\n'), bl = B.split('\n');
    const diff = al.filter((l, i) => bl[i] !== l).slice(0, 6);
    console.error('config shape differs, first lines:\n  live: %s\n  port: %s',
      diff.join(' | '), bl.filter((l, i) => al[i] !== l).slice(0, 6).join(' | '));
  }
}

// ── the keepalive, driven frame by frame ───────────────────────────────────
//
// It re-books itself every frame, so it is driven with an injected clock and a
// queue rather than a live rAF: the prune and the Y lerp are only observable
// across a SEQUENCE of frames at known times.
{
  const WIN = 60;
  // THE SEED MUST OUTRUN THE WINDOW, or the prune never fires and a mutation
  // removing it survives. The first version held 40 points across 40s inside a
  // 60s window — nothing was ever older than `vl - 3000`, so the case named
  // "frames spanning a prune" spanned none. Measured, not guessed: the mutation
  // survived until this line changed.
  //
  // 120 points at 1s covers NOW-119s..NOW, so roughly half sit outside a 60s
  // window and the prune has work on every frame.
  //
  // RX AND TX CARRY DIFFERENT VALUES for the same reason: with `y: i` on both, a
  // max that ignored tx agreed anyway. tx is made larger at one known point so
  // dropping it changes the answer.
  const seed = () => Array.from({ length: 120 }, (_, i) => ({ x: NOW - (119 - i) * 1000, y: i }));
  const seedTx = () => Array.from({ length: 120 }, (_, i) => ({ x: NOW - (119 - i) * 1000, y: i === 118 ? 999 : i / 2 }));
  for (const [name, frames, offset] of [
    ['ten frames at 16ms', 10, 0],
    ['frames spanning a prune', 8, 0],
    ['with a positive offset', 6, 250],
    ['with a negative offset', 6, -300],
  ]) {
    checked++;
    const liveRx = seed(), liveTx = seedTx();
    const portRx = seed(), portTx = seedTx();
    let liveY = 0, portY = 0;
    const liveOut = [], portOut = [];
    for (let f = 0; f < frames; f++) {
      const at = NOW + f * (name === 'frames spanning a prune' ? 900 : 16);
      const ch = {
        data: { datasets: [{ data: liveRx }, { data: liveTx }] },
        options: { scales: { x: {}, y: {} } }, update() {},
      };
      const ctx = {
        Math, Date: { now: () => at }, RIGHT_BUFFER_MS, windowSecs: WIN,
        _bwChart: ch, _lastSampleTs: NOW - 1000, _serverOffset: offset,
        _bwYMaxTarget: 0, _bwYMaxCurrent: liveY,
        _bwKeepaliveId: 1, requestAnimationFrame: () => 1,
        pageVisible: () => true,
      };
      vm.createContext(ctx);
      vm.runInContext(tickSrc + '\n_bwTick();', ctx);
      liveY = ctx._bwYMaxCurrent;
      liveOut.push([ch.options.scales.y.max, ch.options.scales.x.min, ch.options.scales.x.max, liveRx.length]);

      const st = bwTickState(portRx, portTx, portY, at, { serverOffset: offset, windowSecs: WIN }, RIGHT_BUFFER_MS);
      portY = st.yMax;
      portOut.push([st.yMax, st.xMin, st.xMax, portRx.length]);
    }
    const A = JSON.stringify(liveOut), B = JSON.stringify(portOut);
    if (A !== B) { bad++; console.error('keepalive %s\n  live: %s\n  port: %s', name, A, B); }
  }
}

// BELIEVABILITY: the corpus must contain a case the dashboard's own selection
// would get WRONG, or it cannot see a port that reused the wrong helper.
const band = [[mk(NOW - 62000, 7, 3)], CLOCK()];
const withBand = liveSync(band[0], band[1]);
if (!withBand.rx.length) {
  console.error('the three-second band case selected nothing — this gate cannot see the difference ' +
    'between the two point selections, which is the whole reason it exists');
  process.exit(1);
}

if (bad) {
  console.error('\nbandwidth-chart-check: %d of %d cases differ', bad, checked);
  process.exit(1);
}
console.log('bandwidth-chart-check: %d cases identical', checked);
