/**
 * The gap held open at the right edge of a live chart.
 *
 * ── WHAT WENT WRONG WITH A FIXED ONE ───────────────────────────────────────
 *
 * `RIGHT_BUFFER_MS` is documented as "one sample interval", which is true when
 * a sample arrives every second. A router in STREAM mode does not keep that
 * promise: measured on the operator's hAP ax^3 (2026-09-23), 55 points over 63
 * seconds with a mean gap of 1,170 ms — 22 near 1,000, 27 near 1,250, four near
 * 1,500 and one of 2,000.
 *
 * With a fixed 1,000 the axis edge runs ahead of the newest sample whenever one
 * is late, and the line falls short of the frame. Reported as "the right side
 * sometimes is lagging behind and creates a visible gap between where the lines
 * start drawing and the right edge".
 *
 * ── AND WHY A HIGH QUANTILE, NOT AN AVERAGE ────────────────────────────────
 *
 * The newest sample's age runs from nothing, just after one lands, to a whole
 * interval, just before the next. A buffer of the MEAN interval is therefore
 * too small about half the time: the hole would appear half as often rather
 * than stop appearing. That is the mistake this file exists to prevent, and it
 * is the one a casual reading of "track the interval" would make.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-right-buffer.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'dashboard-traffic-buffer.ts'),
    '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

const m = require(OUT);

/** Points spaced by the given gaps, in order. */
function pointsWithGaps(gaps) {
  let t = 1790000000000;
  const out = [{ ts: t, rx_mbps: 1, tx_mbps: 1 }];
  gaps.forEach((g) => { t += g; out.push({ ts: t, rx_mbps: 1, tx_mbps: 1 }); });
  return out;
}

let failed = 0;
function check(name, fn) {
  try {
    fn();
    say('  ok  ' + name);
  } catch (e) {
    failed += 1;
    say('  FAIL ' + name + '\n       ' + (e && e.message));
  }
}

check('a steady one-second stream keeps the documented gap', () => {
  const b = m.rightBufferFor(pointsWithGaps([1000, 1000, 1000, 1000, 1000, 1000]));
  assert.equal(b, m.RIGHT_BUFFER_MS,
    'a chart already arriving every second should not be pushed further into the past');
});

check('a slower stream widens the gap to cover it', () => {
  // The operator's measured distribution.
  const b = m.rightBufferFor(pointsWithGaps(
    [1000, 1250, 1000, 1250, 1250, 1000, 1500, 1250, 1000, 2000]));
  assert.ok(b > m.RIGHT_BUFFER_MS,
    'the gap stayed at one second on a stream that does not deliver every second: ' + b);
  assert.ok(b >= 1500,
    'the gap does not cover the ordinary jitter, so the line still falls short: ' + b);
});

// THE MEAN WOULD BE TOO SMALL. Its own check, because "track the interval" is
// the instruction and the mean is the obvious wrong reading of it.
check('the gap exceeds the mean interval', () => {
  const gaps = [1000, 1250, 1000, 1250, 1250, 1000, 1500, 1250, 1000, 2000];
  const mean = gaps.reduce((a, g) => a + g, 0) / gaps.length;
  const b = m.rightBufferFor(pointsWithGaps(gaps));
  assert.ok(b > mean,
    'the gap is at or below the mean interval (' + Math.round(mean) + 'ms), so the '
    + 'newest sample is older than the edge about half the time: ' + b);
});

// A SINGLE STALL MUST NOT DRAG THE CHART INTO THE PAST for the next twenty
// samples, which is what a plain maximum would do.
check('one stall does not move the edge far back', () => {
  const b = m.rightBufferFor(pointsWithGaps(
    [1000, 1000, 1000, 9000, 1000, 1000, 1000, 1000, 1000, 1000]));
  assert.ok(b <= 2500, 'a single 9s stall dragged the edge to ' + b + 'ms');
});

check('too few samples fall back to the documented gap', () => {
  assert.equal(m.rightBufferFor([]), m.RIGHT_BUFFER_MS);
  assert.equal(m.rightBufferFor(pointsWithGaps([1000])), m.RIGHT_BUFFER_MS);
});

// OUT-OF-ORDER SAMPLES ARE IGNORED rather than counted as a negative gap: the
// timestamps are the ROUTER's, and one stepping backwards after NTP corrects a
// drifted clock is the case this module's own window filter was fixed for.
check('a backwards timestamp does not corrupt the gap', () => {
  const pts = pointsWithGaps([1000, 1000, 1000, 1000, 1000]);
  pts[3].ts -= 5000;
  const b = m.rightBufferFor(pts);
  assert.ok(b >= m.RIGHT_BUFFER_MS && b <= 2500, 'a backwards step produced ' + b);
});

if (failed) { say('\n' + failed + ' failed'); process.exit(1); }
say('\nall passed');
