'use strict';
/**
 * The Frequency Analyser legend's two callbacks, live against ported.
 *
 * ── WHY THESE NEEDED THEIR OWN GATE ────────────────────────────────────────
 *
 * `tools/fa-chart-check.js` compares `spectrumConfig` key for key, and it
 * SUPPLIES ITS OWN STUBS for these two — which is correct for what that gate
 * asks (does the config wire them into the right places) and means the real
 * implementations were never compared to anything. They did not exist until
 * 2026-08-29, and the config had been passing for a day without them.
 *
 * ── BOTH ARE ONE DECISION EACH, AND BOTH ARE ABOUT THE SAME MISSING DATASET ─
 *
 *   generateLabels  the band is drawn by a plugin, so the legend — built from
 *                   DATASETS — has no item for it. Appended by hand, or the one
 *                   mark people ask about is the one nothing explains.
 *   onClick         that appended item has no `datasetIndex`, and the default
 *                   handler would throw toggling a dataset that does not exist.
 *                   Clicking the entry that explains the band would break the
 *                   chart.
 *
 * `Chart` is faked on both sides, so this compares the PORT'S OWN calls into it:
 * what it asks the defaults for, what it appends, and whether it delegates.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/fa-legend-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/fa-legend-check.js --freeze
const G = L.golden('fa-legend-check');
const src = L.liveSource(ROOT, path.join('public', 'app.js'));

function slice(decl, close, name) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent, and this then threw `cannot find <name>` at module scope — before
  // any frozen output could be served. An empty slice is harmless because the
  // live half is never entered when the output is frozen.
  if (src === '') return '';
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find ' + name + ' — it has moved or been rewritten');
  const j = src.indexOf(close, i);
  if (j === -1) throw new Error(name + ' is never closed');
  return src.slice(i, j + close.length);
}

// The two callbacks, lifted from inside the `makeChart` config object. They have
// no function boundary of their own, so each is anchored on its property name.
// The closing anchor stops at the brace and NOT the comma that follows it. Both
// are object properties, so the slice would otherwise end `},` and
// `this.f = function(){...},;` does not parse — which is what the first run
// reported, from `new Script`.
const genSrc = slice('              generateLabels: function (chart) {', '\n              }',
  'generateLabels');
const clickSrc = slice('            onClick: function (e, item, legend) {', '\n            }',
  'the legend onClick');

const OUT = path.join(ROOT, 'testdata', '.falegend-port.cjs');
const ENTRY = path.join(ROOT, 'testdata', '.falegend-entry.ts');
fs.writeFileSync(ENTRY, "export * from '../web/src/pages/wireless-fa.js';\n");
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

// ── A recording Chart ───────────────────────────────────────────────────────
//
// The DEFAULTS are what both sides reach for, so the fake records every call:
// what was asked of `generateLabels`, and whether `onClick` was delegated at all.

function makeChartFake(log) {
  return {
    defaults: {
      plugins: {
        legend: {
          labels: {
            generateLabels(chart) {
              log.push(['generateLabels', chart && chart.tag]);
              return [
                { text: 'Signal power', datasetIndex: 0 },
                { text: 'Noise floor', datasetIndex: 1 },
              ];
            },
          },
          onClick(e, item, legend) {
            log.push(['default onClick', e && e.tag, item && item.text,
                      legend && legend.tag, this === legend]);
          },
        },
      },
    },
  };
}

const CHART_ARG = { tag: 'the-chart' };

function runLive(kind, arg) {
  const log = [];
  const ctx = { Chart: makeChartFake(log) };
  vm.createContext(ctx);
  if (kind === 'labels') {
    vm.runInContext('this.f = ' + genSrc.replace('generateLabels: ', '') + ';', ctx);
    return { out: ctx.f(arg), log };
  }
  vm.runInContext('this.f = ' + clickSrc.replace('onClick: ', '') + ';', ctx);
  // `this` is the legend in Chart.js's own invocation.
  return { out: ctx.f.call(arg.legend, arg.e, arg.item, arg.legend), log };
}

function runPort(kind, arg) {
  const log = [];
  const prevChart = globalThis.Chart;
  globalThis.Chart = makeChartFake(log);
  try {
    delete require.cache[require.resolve(OUT)];
    const m = require(OUT);
    if (kind === 'labels') return { out: m.faLegendLabels(arg), log };
    return { out: m.faLegendClick(arg.e, arg.item, arg.legend), log };
  } finally {
    globalThis.Chart = prevChart;
  }
}

const CASES = [
  { why: 'the band item is appended after the defaults', kind: 'labels', arg: CHART_ARG },
  {
    why: 'a click on a real dataset item is delegated to the default',
    kind: 'click',
    arg: { e: { tag: 'evt' }, item: { text: 'Signal power', datasetIndex: 0 },
           legend: { tag: 'the-legend' } },
  },
  {
    why: 'a click on the SECOND dataset is delegated too',
    kind: 'click',
    arg: { e: { tag: 'evt' }, item: { text: 'Noise floor', datasetIndex: 1 },
           legend: { tag: 'the-legend' } },
  },
  {
    why: 'a click on the BAND item is swallowed — it has no datasetIndex',
    kind: 'click',
    arg: { e: { tag: 'evt' }, item: { text: 'Active Channel' },
           legend: { tag: 'the-legend' } },
  },
  {
    why: 'datasetIndex 0 is not confused with absent',
    kind: 'click',
    arg: { e: { tag: 'evt' }, item: { text: 'Signal power', datasetIndex: 0 },
           legend: { tag: 'the-legend' } },
  },
];

let bad = 0;
let delegated = 0, swallowed = 0;
for (const c of CASES) {
  const live = G.live(G.seq(), () => runLive(c.kind, c.arg));
  const port = runPort(c.kind, c.arg);
  if (c.kind === 'click') {
    if (live.log.length) delegated++; else swallowed++;
  }
  const a = JSON.stringify({ out: live.out, log: live.log }, null, 1);
  const b = JSON.stringify({ out: port.out, log: port.log }, null, 1);
  if (a === b) { console.log('  ok  ' + c.why); continue; }
  bad++;
  console.log('  DIFF  ' + c.why);
  console.log('        live: ' + a.replace(/\n\s*/g, ' '));
  console.log('        port: ' + b.replace(/\n\s*/g, ' '));
}

fs.rmSync(OUT, { force: true });

// BELIEVABILITY. A corpus that only ever delegates would pass against a handler
// with no guard, and one that only ever swallows against a handler that does
// nothing at all.
if (delegated === 0 || swallowed === 0) {
  console.log('\nthe click cases delegated ' + delegated + ' and swallowed ' + swallowed +
              ' — they do not exercise both sides of the guard');
  process.exit(1);
}

console.log('\n' + CASES.length + ' cases, ' + delegated + ' delegated and ' +
            swallowed + ' swallowed');
if (bad) { console.log(bad + ' FAILED'); process.exit(1); }
console.log('all agree');
