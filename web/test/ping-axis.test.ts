/**
 * ONE SPIKE MUST NOT FLATTEN THE PING CARD.
 *
 * Chart.js scales `y` to the largest value it is handed, so a single 1500ms
 * reply among fifty 20ms ones drew every ordinary bar under a pixel tall and
 * the card went blank apart from the spike. `pingAxisMax` caps the axis at half
 * again the drawn window's 90th percentile.
 *
 * ── WHAT WOULD MAKE THIS TEST PASS DISHONESTLY ─────────────────────────────
 *
 * A function returning a constant. So the slow-link case is here as a control:
 * a window that is genuinely slow all the way through must scale UP, or the
 * cap has become a ceiling on what the card can report.
 *
 * And the call site is checked as well as the function, because the failure
 * being guarded against is "somebody computed the axis and never applied it" -
 * which leaves the chart looking exactly as it did before the fix.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.ping-axis-entry.ts');
fs.writeFileSync(ENTRY,
  "export { pingAxisMax, updatePingChart, PING_AXIS_FLOOR } from '../web/src/pages/dashboard-ping.js';\n");
const OUT = path.join(ROOT, 'testdata', '.ping-axis.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

// The module reaches for the document through its `el` helper.
(global as unknown as { document: unknown }).document = makeDoc([], { allowUnknown: ['*'] });
(global as unknown as { window: unknown }).window = { addEventListener: () => {} };
const mod = require(OUT);
fs.rmSync(OUT, { force: true });

interface Pt { ts: number; rtt: number | null; loss: number }
const { pingAxisMax, updatePingChart, PING_AXIS_FLOOR } = mod as {
  pingAxisMax(pts: Pt[]): number;
  updatePingChart(chart: unknown, history: Pt[]): void;
  PING_AXIS_FLOOR: number;
};

const pt = (rtt: number | null): Pt => ({ ts: 0, rtt, loss: 0 });
const many = (n: number, rtt: number | null): Pt[] => Array.from({ length: n }, () => pt(rtt));
const fakeChart = () => ({
  data: {
    labels: [] as string[],
    datasets: [{ data: [] as (number | null)[], backgroundColor: [] as string[] }],
  },
  options: { scales: { y: {} as { max?: number } } },
  update: () => {},
  destroy: () => {},
});

// ── a steady link keeps headroom, and clips nothing ─────────────────────────
{
  const axis = pingAxisMax(many(50, 20));
  assert.strictEqual(axis, 30, 'a steady 20ms link should scale to 30ms');
  assert.ok(axis > 20,
    'the axis sits ON the highest bar, so a steady link draws a row of flat-topped ' +
    'bars jammed against the ceiling - which reads as a fault');
}

// ── the case from the report: one spike does not move the scale ─────────────
{
  const steady = many(49, 20);
  const axis = pingAxisMax([...steady, pt(1500)]);
  assert.strictEqual(axis, pingAxisMax(steady),
    'a single 1500ms reply changed the axis, so every 20ms bar is a pixel tall again');
  assert.ok(1500 > axis, 'the spike is meant to run off the top of the chart');
}

// ── THE CONTROL: a genuinely slow link still scales up ──────────────────────
//
// Without this, `return 30` passes every assertion above.
{
  assert.strictEqual(pingAxisMax(many(50, 400)), 600,
    'a link that is slow all the way through must scale to show it; the cap is ' +
    'protection from one outlier, not a ceiling on what the card can report');
}

// ── a fifth of the window being slow IS the picture ─────────────────────────
{
  assert.ok(pingAxisMax([...many(40, 20), ...many(10, 800)]) > 800,
    'the 90th percentile tolerates a tenth of the window as outliers and no more - ' +
    'at a fifth, the slow replies are the reading and must be drawn');
}

// ── timeouts are not values ─────────────────────────────────────────────────
{
  assert.strictEqual(pingAxisMax(many(50, null)), PING_AXIS_FLOOR,
    'a window of nothing but timeouts has no scale of its own and takes the floor');
  assert.strictEqual(pingAxisMax([...many(25, 200), ...many(25, null)]), pingAxisMax(many(25, 200)),
    'timeouts took part in the percentile, so a lossy link draws on a different scale ' +
    'from a clean one at the same latency');
}

// ── the floor ───────────────────────────────────────────────────────────────
{
  assert.strictEqual(pingAxisMax(many(50, 1)), PING_AXIS_FLOOR,
    'a sub-millisecond LAN scaled to its own jitter, which fills the card and reads ' +
    'as a problem');
  assert.strictEqual(pingAxisMax([]), PING_AXIS_FLOOR, 'an empty window still needs a scale');
}

// ── THE CALL SITE: the axis is actually applied to the chart ────────────────
{
  const chart = fakeChart();
  updatePingChart(chart, [...many(49, 20), pt(1500)]);
  assert.strictEqual(chart.options.scales.y.max, 30,
    'updatePingChart did not write the axis maximum, so Chart.js is still scaling to ' +
    'the largest value it was given');
  // The bars themselves are untouched by the clip: the tooltip still reports the
  // real number, and so does the `max` figure in the card header.
  assert.strictEqual(chart.data.datasets[0]!.data[49], 1500,
    'the spike was clamped in the DATA rather than on the axis, which would make the ' +
    'tooltip lie about what the router measured');
}

// ── the axis follows the DRAWN window, not the whole buffer ─────────────────
//
// The chart draws the last fifty of a sixty-point history. A spike that has
// scrolled off the chart must stop owning the scale.
{
  const chart = fakeChart();
  updatePingChart(chart, [pt(1500), ...many(59, 20)]);
  assert.strictEqual(chart.options.scales.y.max, 30,
    'a spike that is no longer drawn still set the scale, so the card stays flattened ' +
    'for a minute after the spike has gone');
}

console.log('ping-axis: ok');
