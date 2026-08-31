'use strict';
/**
 * The spectrum chart's CONFIG, port against live, key for key.
 *
 * The last non-cutover gap `wiring-audit` recorded on a ported page. Its two
 * pure decisions — the tooltip body and the band geometry — landed earlier and
 * are pinned by `tools/fa-spectrum-check.js`. This is the configuration itself.
 *
 * ---- SAME TECHNIQUE AS THE TRAFFIC CHART ----------------------------------
 *
 * `makeChart` hands a large literal to a constructor. Both sides are given a
 * FAKE `Chart` that records the config, and the two are compared key for key.
 * Function-valued options cannot be compared as values, so each is CALLED with a
 * representative argument and the result compared instead — a tooltip callback
 * that silently stopped reading the row would otherwise pass.
 *
 *   node tools/fa-chart-check.js
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
const G = LIFT.golden('fa-chart-check');
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
const makeSrc = G.value('makeSrc', () => slice('function makeChart() {', '\n  }', 'makeChart'));
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['makeSrc', makeSrc]]) {
  assert.ok(typeof __v === 'string' && __v.length > 4,
    'the recorded ' + __n + ' is empty — the golden is broken');
}
if (LIFT.hasReference(ROOT)) assert.ok(makeSrc.includes('faChannelBand'), 'the makeChart slice lost its plugin');
if (LIFT.hasReference(ROOT)) assert.ok(makeSrc.includes('Active Channel'), 'the makeChart slice lost the legend item');
if (LIFT.hasReference(ROOT)) assert.ok(makeSrc.includes('FA_FLOOR_DBM'), 'the makeChart slice lost the y-axis floor');

// ---- The rows both sides describe ------------------------------------------
const ROWS = [
  { ch: 2412, chNum: 1, load: 44, nets: 3, nf: -98, maxSig: -55, minSig: -80 },
  { ch: 2437, chNum: 6, load: 0, nets: 0, nf: -99, maxSig: null, minSig: null },
  { ch: 5180, chNum: 36, load: 12, nets: 1, nf: -95, maxSig: -61, minSig: -70 },
];
const CURRENT = 2412;

function liveConfig() {
  let captured = null;
  const ctx = {
    Math, Date,
    Chart: Object.assign(
      function (canvas, cfg) { captured = cfg; return { destroy() {} }; },
      { defaults: { plugins: { legend: {
        labels: { generateLabels: () => [{ text: 'Signal power', datasetIndex: 0 },
                                         { text: 'Noise floor', datasetIndex: 1 }] },
        onClick: function () { ctx.__defaultClicked = true; },
      } } } }),
    FA_FLOOR_DBM: -100,
    faChannelBand: { id: 'faChannelBand' },
    _rows: ROWS,
    _state: { currentChannelMhz: CURRENT },
    _chart: null,
    $: () => ({ id: 'faSpectrum' }),
    __defaultClicked: false,
  };
  vm.createContext(ctx);
  vm.runInContext(makeSrc + '\nmakeChart();', ctx);
  return { cfg: captured, ctx };
}

// ---- The port ---------------------------------------------------------------
const ENTRY = path.join(ROOT, 'testdata', '.fachart-entry.ts');
const OUT = path.join(ROOT, 'testdata', '.fachart-port.cjs');
fs.writeFileSync(ENTRY, "export * from '../web/src/pages/wireless-fa.js';\n");
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const port = require(OUT);

let portDefaultClicked = false;
const portCfg = port.spectrumConfig({
  rows: () => ROWS,
  currentChannelMhz: () => CURRENT,
  legendLabels: () => [{ text: 'Signal power', datasetIndex: 0 },
                       { text: 'Noise floor', datasetIndex: 1 }, port.FA_BAND_LEGEND],
  legendClick: (e, item) => { if (item.datasetIndex !== undefined) portDefaultClicked = true; },
});

// ---- Compare ----------------------------------------------------------------
// A tooltip index chosen so the row HAS measurements; index 1 has none and is
// used separately, because "returns nothing" and "was never called" look the
// same otherwise.
const TIP = [{ dataIndex: 0 }];
const TIP_EMPTY = [{ dataIndex: 1 }];

function normalise(v, keyPath) {
  if (typeof v === 'function') {
    switch (keyPath) {
      case 'options.plugins.tooltip.callbacks.title':
        return 'CALL:' + v(TIP) + '|' + v(TIP_EMPTY);
      case 'options.plugins.tooltip.callbacks.label':
        return 'CALL:' + JSON.stringify(v({}));
      case 'options.plugins.tooltip.callbacks.afterBody':
        return 'CALL:' + JSON.stringify(v(TIP)) + '|' + JSON.stringify(v(TIP_EMPTY));
      case 'options.plugins.legend.labels.generateLabels':
        return 'CALL:' + JSON.stringify(v({}));
      case 'options.plugins.legend.onClick':
        return 'FUNCTION(compared separately)';
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

const { cfg: live, ctx } = liveConfig();
const problems = [];

// The plugin list is a reference on the live side and is wired by the caller
// here, so it is compared by NAME rather than identity.
const livePlugins = (live.plugins || []).map((p) => p && p.id);
if (livePlugins.join(',') !== 'faChannelBand') {
  problems.push(`the live config registers plugins [${livePlugins}]; the port wires the band `
    + 'plugin at construction and this check assumes exactly one');
}
delete live.plugins;

const a = normalise(live, '');
const b = normalise(portCfg, '');
const aj = JSON.stringify(a, null, 1);
const bj = JSON.stringify(b, null, 1);
if (aj !== bj) {
  const al = aj.split('\n'), bl = bj.split('\n');
  for (let i = 0; i < Math.max(al.length, bl.length); i++) {
    if (al[i] !== bl[i]) {
      problems.push(`the configs differ near line ${i + 1}:\n    live: ${al[i]}\n    port: ${bl[i]}`);
      break;
    }
  }
}

// ── THE LEGEND CLICK GUARD, compared by BEHAVIOUR ───────────────────────────
//
// The appended band item has no datasetIndex, and the default handler would
// throw toggling a dataset that does not exist. Both sides must ignore it and
// both must forward a real one.
live.options.plugins.legend.onClick.call({}, {}, { datasetIndex: 0 }, {});
portCfg.options.plugins.legend.onClick({}, { datasetIndex: 0 }, {});
if (!ctx.__defaultClicked || !portDefaultClicked) {
  problems.push('a legend item WITH a datasetIndex was not forwarded to the default handler '
    + `(live ${ctx.__defaultClicked}, port ${portDefaultClicked})`);
}
ctx.__defaultClicked = false; portDefaultClicked = false;
live.options.plugins.legend.onClick.call({}, {}, { text: 'Active Channel' }, {});
portCfg.options.plugins.legend.onClick({}, { text: 'Active Channel' }, {});
if (ctx.__defaultClicked || portDefaultClicked) {
  problems.push('the appended band item WAS forwarded to the default handler; it has no dataset '
    + `to toggle and the default would throw (live ${ctx.__defaultClicked}, port ${portDefaultClicked})`);
}

// ── THE DATA REBUILD, against the live renderChart ──────────────────────────
//
// `renderChart` is what puts the rows on the chart, and its floating-bar rule is
// the part a port gets wrong: a channel where nothing was detected gets NO BAR
// (`null`) rather than a fabricated one at the floor, which would be
// indistinguishable from a very weak signal.
{
  // FROZEN, not guarded: `renderChart` is EXECUTED below, so the source is what
  // has to survive. The assertion then validates the RECORDING and stays live.
  const renderSrc = G.value('renderChart source',
    () => slice('function renderChart() {', '\n  }', 'renderChart'));
  assert.ok(renderSrc.includes('FA_FLOOR_DBM'), 'the renderChart slice lost the bar base');
  const chart = { data: { labels: [], datasets: [{}, {}] }, update() {} };
  const ctx2 = {
    _chart: chart, _rows: ROWS, FA_FLOOR_DBM: -100,
    congestionColour: (load) => 'C(' + load + ')',
  };
  vm.createContext(ctx2);
  vm.runInContext(renderSrc + '\nrenderChart();', ctx2);

  const got = port.spectrumData(ROWS);
  const cmp = [
    ['labels', chart.data.labels, got.labels],
    ['signal', chart.data.datasets[0].data, got.signal],
    ['noise', chart.data.datasets[1].data, got.noise],
  ];
  for (const [what, live_, port_] of cmp) {
    if (JSON.stringify(live_) !== JSON.stringify(port_)) {
      problems.push(`the ${what} data differs\n    live: ${JSON.stringify(live_)}`
        + `\n    port: ${JSON.stringify(port_)}`);
    }
  }
  // The colours come from `congestionColour`, which is already gated by
  // fa-dialog-check; what matters here is that ONE is produced per row, in order.
  if (got.colours.length !== ROWS.length) {
    problems.push(`the port produced ${got.colours.length} bar colours for ${ROWS.length} rows`);
  }
  // AND THE ROW WITH NO SIGNAL MUST BE null, not a floor-anchored bar.
  if (got.signal[1] !== null) {
    problems.push('a channel where nothing was detected got a bar: '
      + JSON.stringify(got.signal[1]) + ' — it must be null, or an undetected channel is drawn '
      + 'the same as a very weak one');
  }
}

fs.rmSync(OUT, { force: true });
if (problems.length) {
  console.error('fa-chart-check FAILED:');
  for (const p of problems) console.error('  - ' + p);
  process.exit(1);
}
console.log('fa-chart-check: the spectrum config agrees with the live one key for key, including '
  + 'the tooltip callbacks and the legend click guard');
