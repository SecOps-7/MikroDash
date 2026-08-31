'use strict';
/**
 * The SPECTRUM CHART's two pure decisions, lifted out of `public/app.js`.
 *
 * ---- WHY NOW -------------------------------------------------------------
 *
 * `faSpectrum` is the last non-cutover gap `tools/wiring-audit.js` records on a
 * ported page. Its entry deferred it — "it lands with the other Chart.js work
 * rather than being half-drawn here" — and THAT WORK HAS LANDED: routing,
 * bandwidth, reports, the dashboard traffic and ping charts and the routing
 * donut are all ported, with three chart gates over them. The deferral's premise
 * expired, which is the shape this project keeps finding.
 *
 * ---- WHAT IS PURE HERE, AND WHAT IS NOT ----------------------------------
 *
 * The chart is a Chart.js bar plot. Most of it is configuration — colours,
 * fonts, axis titles — which is presentation and belongs with the canvas. Two
 * pieces are DECISIONS and can be pinned without a browser:
 *
 *   the TOOLTIP body   which of six measurements a channel row contributes, in
 *                      what order, with what labels and units, and the marker on
 *                      the radio's own channel
 *   the BAND GEOMETRY  where the current-channel band is drawn and how wide,
 *                      including the fallback when the bar element is missing
 *
 * The band's fallback is the interesting half: it takes the width from the BAR
 * rather than the category, because "Chart.js insets bars within their category,
 * so a category-wide band would sit visibly proud of them" — and when there is
 * no bar it falls back to the category spacing, clamped to [10, 44], with 18 as
 * the answer for a single-column chart where there is no spacing to measure.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/fa-spectrum-cases.js
 *   MIKRODASH_SRC=../MikroDash node tools/fa-spectrum-cases.js --check
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const OUT = path.join(ROOT, 'testdata', 'fa-spectrum-cases.json');

const app = fs.readFileSync(path.join(LIVE, 'public', 'app.js'), 'utf8');
const lines = app.split('\n');

// ---- The tooltip body ------------------------------------------------------
const tipStart = lines.findIndex((l) => l.includes('afterBody: function (c) {')
  && lines.slice(0, 1).length >= 0);
if (tipStart < 0) throw new Error('anchor lost: the spectrum tooltip afterBody');
const tipEnd = lines.findIndex((l, i) => i > tipStart && l.includes('return out;'));
if (tipEnd < 0) throw new Error('anchor lost: the afterBody `return out;`');
const tipBody = lines.slice(tipStart + 1, tipEnd + 1).join('\n');
if (!tipBody.includes('this radio')) {
  throw new Error('the tooltip slice lost the current-radio marker — the anchors drifted');
}
// Six pushes plus the marker; fewer means the slice is wrong.
if ((tipBody.match(/out\.push\(/g) || []).length < 6) {
  throw new Error('the tooltip slice has fewer than six pushes');
}

const tooltipFor = vm.runInNewContext(
  `(function (r, currentChannelMhz) {
     var _state = { currentChannelMhz: currentChannelMhz };
     var c = [{ dataIndex: 0 }];
     var _rows = [r];
     ${tipBody}
   })`, Object.create(null), { filename: 'app.js#faSpectrum.afterBody' });

// ---- The band geometry -----------------------------------------------------
const bandStart = lines.findIndex((l) => l.includes("id: 'faChannelBand'"));
if (bandStart < 0) throw new Error("anchor lost: id: 'faChannelBand'");
const geomStart = lines.findIndex((l, i) => i > bandStart && l.includes('var x = el ? el.x'));
if (geomStart < 0) throw new Error('anchor lost: the band x/width computation');
const geom = lines.slice(geomStart, geomStart + 4).join('\n');
if (!geom.includes('Math.max(10') || !geom.includes('44')) {
  throw new Error('the band slice lost its clamp');
}
const bandFor = vm.runInNewContext(
  `(function (el, labelCount, pixel0, pixel1, idx) {
     var chart = { data: { labels: new Array(labelCount) } };
     var xs = { getPixelForValue: function (v) { return v === 0 ? pixel0 : (v === 1 ? pixel1 : v * 10); } };
     ${geom}
     return { x: x, w: w };
   })`, Object.create(null), { filename: 'app.js#faChannelBand' });

// ---- Cases -----------------------------------------------------------------
const R = (o) => Object.assign({ ch: 2412, chNum: 1 }, o);
const TOOLTIP = [
  ['every measurement present, and not the radio\'s own channel',
    R({ load: 44, nets: 3, nf: -98, maxSig: -55, minSig: -80 }), 5180],
  ['the radio\'s own channel gets the marker',
    R({ load: 10, nets: 1, nf: -95, maxSig: -60, minSig: -70 }), 2412],
  ['a row with nothing measured contributes nothing',
    R({}), 5180],
  ['zero is a measurement, not an absence',
    R({ load: 0, nets: 0, nf: 0, maxSig: 0, minSig: 0 }), 5180],
  ['nulls are skipped one by one, and order is preserved',
    R({ load: null, nets: 2, nf: null, maxSig: -50, minSig: null }), 5180],
];
const BAND = [
  ['the bar element gives both x and width', { x: 120, width: 22 }, 8, 40, 60, 3],
  ['no bar: the width comes from the category spacing', null, 8, 40, 60, 3],
  ['no bar and ONE column: the spacing cannot be measured, so 18', null, 1, 40, 60, 0],
  ['the spacing is clamped UP to 10', null, 8, 40, 42, 3],
  ['the spacing is clamped DOWN to 44', null, 8, 0, 400, 3],
];

const out = {
  tooltip: TOOLTIP.map(([why, row, current]) => ({
    why, row, currentChannelMhz: current, lines: tooltipFor(row, current),
  })),
  band: BAND.map(([why, el, labelCount, p0, p1, idx]) => ({
    why, el, labelCount, pixel0: p0, pixel1: p1, idx, ...bandFor(el, labelCount, p0, p1, idx),
  })),
};

// ---- Believability ---------------------------------------------------------
const tipBy = Object.fromEntries(out.tooltip.map((c) => [c.why, c]));
const bandBy = Object.fromEntries(out.band.map((c) => [c.why, c]));

if (tipBy['every measurement present, and not the radio\'s own channel'].lines.length !== 5) {
  throw new Error('a fully-measured row did not produce five lines');
}
if (tipBy['a row with nothing measured contributes nothing'].lines.length !== 0) {
  throw new Error('an empty row produced lines');
}
// ZERO IS A MEASUREMENT. The live test is `!= null`, so 0 must survive — a port
// using a truthiness check drops every one of them.
if (tipBy['zero is a measurement, not an absence'].lines.length !== 5) {
  throw new Error('zero values were dropped; the live test is `!= null`, not truthiness');
}
if (!tipBy['the radio\'s own channel gets the marker'].lines.some((l) => l.includes('this radio'))) {
  throw new Error('the current channel did not get its marker');
}
if (tipBy['every measurement present, and not the radio\'s own channel'].lines
  .some((l) => l.includes('this radio'))) {
  throw new Error('a channel that is not the radio\'s own got the marker');
}
// The band's three fallback arms must differ, or the clamp is not exercised.
const widths = out.band.map((b) => b.w);
if (new Set(widths).size < 4) {
  throw new Error(`the band cases produce only ${new Set(widths).size} distinct widths: `
    + widths.join(', '));
}
if (bandBy['the bar element gives both x and width'].w !== 22) {
  throw new Error('the bar element did not supply the width');
}
if (bandBy['no bar and ONE column: the spacing cannot be measured, so 18'].w !== 18) {
  throw new Error('the single-column fallback is not 18');
}
if (bandBy['the spacing is clamped UP to 10'].w !== 10) throw new Error('no clamp at 10');
if (bandBy['the spacing is clamped DOWN to 44'].w !== 44) throw new Error('no clamp at 44');

const json = JSON.stringify(
  { generated_from: 'public/app.js faSpectrum tooltip + faChannelBand', ...out }, null, 2) + '\n';
if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== json) {
    console.error('STALE: testdata/fa-spectrum-cases.json - re-run tools/fa-spectrum-cases.js');
    process.exit(1);
  }
  console.log(`fa-spectrum-cases: up to date (${out.tooltip.length} tooltip, ${out.band.length} band)`);
} else {
  fs.writeFileSync(OUT, json);
  console.log(`wrote ${OUT} (${out.tooltip.length} tooltip cases, ${out.band.length} band cases)`);
}
