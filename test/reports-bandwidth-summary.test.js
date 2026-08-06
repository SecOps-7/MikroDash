'use strict';
// Tests for the report summary queries added for #62.
//
// The stat cards on the Reports page used to be reduced in the browser from
// whatever rows the API returned, which made them wrong in two independent
// ways: aggregated rows are averages, so a max across them is a peak of
// averages rather than a real peak; and the row queries are capped by LIMIT, so
// totals silently truncated on long ranges. These cover both, plus the
// nearest-rank percentile and the range/interface/router scoping.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs   = require('fs');
const os   = require('os');
const path = require('path');

// db.js resolves DATA_DIR at require time, so point it at a temp dir first.
const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'mikrodash-rpt-'));
process.env.DATA_DIR = TMP;
const db = require('../src/db');

const MIN  = 60000;
const DAY  = 86400000;
const BASE = 1785312000000;   // fixed epoch ms so bucket maths is deterministic
const RA = 'router-a', RB = 'router-b';
const IF1 = 'ether1',  IF2 = 'ether2';

function reset() { db.purge({}); }

// 100 one-minute samples, rx_mbps 1..100, all inside a single UTC day so that
// day aggregation collapses them to one bucket.
function seedRamp(routerId, iface, base) {
  for (let i = 0; i < 100; i++) {
    db.insertTrafficSample(routerId, iface, i + 1, (i + 1) / 10, base + i * MIN);
    db.insertBandwidthSample(routerId, iface, (i + 1) / 8, (i + 1) / 80, base + i * MIN);
  }
}

test('setup', () => {
  db.open();
  reset();
  seedRamp(RA, IF1, BASE);
  assert.equal(db.queryTrafficSummary(RA, IF1, 0, Date.now()).samples, 100);
});

test('traffic summary reports count, true max and mean', () => {
  const s = db.queryTrafficSummary(RA, IF1, 0, Date.now());
  assert.equal(s.samples, 100);
  assert.equal(s.rxMaxMbps, 100, 'max is the real maximum sample');
  assert.equal(s.txMaxMbps, 10);
  assert.equal(s.rxAvgMbps, 50.5, 'mean of 1..100');
});

test('95th percentile is nearest-rank, computed in SQL', () => {
  // ceil(100 * 95 / 100) - 1 = 94  ->  sorted[94] = 95
  const s = db.queryTrafficSummary(RA, IF1, 0, Date.now(), 95);
  assert.equal(s.rxP95Mbps, 95);
  // 50th of 1..100 -> ceil(50)-1 = 49 -> sorted[49] = 50
  assert.equal(db.queryTrafficSummary(RA, IF1, 0, Date.now(), 50).rxP95Mbps, 50);
});

test('percentile clamps at both ends instead of running off the array', () => {
  const lo = db.queryTrafficSummary(RA, IF1, 0, Date.now(), 1);
  const hi = db.queryTrafficSummary(RA, IF1, 0, Date.now(), 99);
  assert.equal(lo.rxP95Mbps, 1,  'p1 lands on the first sample, not offset -1');
  assert.equal(hi.rxP95Mbps, 99);
  // Out-of-range percentiles are clamped to 1..99 rather than throwing.
  assert.equal(db.queryTrafficSummary(RA, IF1, 0, Date.now(), 0).rxP95Mbps, 95,
    'falsy pct falls back to the 95 default');
  assert.equal(db.queryTrafficSummary(RA, IF1, 0, Date.now(), 999).rxP95Mbps, 99);
});

test('a single sample is its own percentile', () => {
  reset();
  db.insertTrafficSample(RA, IF1, 42, 4, BASE);
  const s = db.queryTrafficSummary(RA, IF1, 0, Date.now());
  assert.equal(s.samples, 1);
  assert.equal(s.rxP95Mbps, 42);
  assert.equal(s.rxMaxMbps, 42);
});

// The #62 regression. A short spike inside a day bucket is invisible to the
// aggregated rows, because those rows are averages — which is exactly what the
// stat cards used to reduce over.
test('summary sees a spike that day-aggregation averages away', () => {
  reset();
  for (let i = 0; i < 60; i++) db.insertTrafficSample(RA, IF1, 1, 1, BASE + i * MIN);
  db.insertTrafficSample(RA, IF1, 900, 90, BASE + 60 * MIN);   // one-minute spike

  const agg = db.queryTrafficSamplesAgg(RA, IF1, 0, Date.now(), 'day');
  assert.equal(agg.length, 1, 'all samples fall in one day bucket');
  const peakOfAverages = Math.max.apply(null, agg.map(r => r.rx_mbps));
  const summary = db.queryTrafficSummary(RA, IF1, 0, Date.now());

  assert.equal(summary.rxMaxMbps, 900, 'summary reports the real peak');
  assert.ok(peakOfAverages < 100, 'the averaged bucket hides it');
  assert.ok(summary.rxMaxMbps > peakOfAverages * 10,
    'this gap is the bug: ' + peakOfAverages.toFixed(1) + ' vs ' + summary.rxMaxMbps);
});

test('aggregate rows carry a MAX column alongside the average', () => {
  reset();
  for (let i = 0; i < 60; i++) db.insertTrafficSample(RA, IF1, 1, 1, BASE + i * MIN);
  db.insertTrafficSample(RA, IF1, 900, 90, BASE + 60 * MIN);
  const row = db.queryTrafficSamplesAgg(RA, IF1, 0, Date.now(), 'day')[0];
  assert.equal(row.rx_max_mbps, 900, 'new column exposes the true peak');
  assert.ok(row.rx_mbps < 100, 'existing average column keeps its meaning');

  db.insertBandwidthSample(RA, IF1, 5,   1, BASE);
  db.insertBandwidthSample(RA, IF1, 500, 1, BASE + MIN);
  const brow = db.queryBandwidthSamplesAgg(RA, IF1, 0, Date.now(), 'day')[0];
  assert.equal(brow.rx_max_mb, 500);
  assert.equal(brow.rx_mb, 505, 'existing sum column keeps its meaning');
});

// Totals used to be summed in the browser over rows already capped by LIMIT, so
// they under-reported on long ranges. The summary sums in SQL instead, so it is
// independent of the cap. Exercised with an explicit small limit rather than by
// seeding 100k rows, which would add seconds to the suite for the same property;
// the real-world default cap is checked against the live database separately.
test('totals are not truncated by the row LIMIT', () => {
  reset();
  const N  = 200;
  const TO = BASE + N * MIN;
  for (let i = 0; i < N; i++) db.insertBandwidthSample(RA, IF1, 1, 0.5, BASE + i * MIN);

  const capped  = db.queryBandwidthSamples(RA, IF1, 0, TO, 50);   // stand-in for LIMIT 100000
  const summary = db.queryBandwidthSummary(RA, IF1, 0, TO);
  assert.equal(capped.length, 50, 'row query respects its limit, as designed');
  assert.equal(summary.samples, N, 'summary counts every row regardless');
  assert.equal(summary.rxTotalMb, N, 'total covers rows beyond the cap');

  const truncated = capped.reduce((a, r) => a + r.rx_mb, 0);
  assert.equal(truncated, 50, 'reducing the returned rows is what used to happen');
  assert.ok(summary.rxTotalMb > truncated,
    'browser-side total was short by ' + (summary.rxTotalMb - truncated));
});

test('range filter applies to count, max and percentile alike', () => {
  reset();
  seedRamp(RA, IF1, BASE);                                     // 1..100
  db.insertTrafficSample(RA, IF1, 5000, 500, BASE + 30 * DAY); // far outside

  const s = db.queryTrafficSummary(RA, IF1, BASE, BASE + 99 * MIN);
  assert.equal(s.samples, 100, 'out-of-range row excluded from the count');
  assert.equal(s.rxMaxMbps, 100, 'and from the max');
  assert.equal(s.rxP95Mbps, 95, 'and from the percentile, which runs its own query');
});

test('summaries are scoped to one interface and one router', () => {
  reset();
  seedRamp(RA, IF1, BASE);
  db.insertTrafficSample(RA, IF2, 9999, 999, BASE);
  db.insertTrafficSample(RB, IF1, 8888, 888, BASE);
  db.insertBandwidthSample(RA, IF2, 9999, 999, BASE);
  db.insertBandwidthSample(RB, IF1, 8888, 888, BASE);

  assert.equal(db.queryTrafficSummary(RA, IF1, 0, Date.now()).rxMaxMbps, 100);
  assert.equal(db.queryBandwidthSummary(RA, IF1, 0, Date.now()).rxMaxMb, 100 / 8);
  assert.equal(db.queryTrafficSummary(RA, IF2, 0, Date.now()).rxMaxMbps, 9999);
  assert.equal(db.queryTrafficSummary(RB, IF1, 0, Date.now()).rxMaxMbps, 8888);
});

test('bandwidth summary totals and peak match the seed', () => {
  reset();
  db.insertBandwidthSample(RA, IF1, 10, 2, BASE);
  db.insertBandwidthSample(RA, IF1, 30, 4, BASE + MIN);
  db.insertBandwidthSample(RA, IF1, 60, 6, BASE + 2 * MIN);
  const s = db.queryBandwidthSummary(RA, IF1, 0, Date.now());
  assert.equal(s.samples, 3);
  assert.equal(s.rxTotalMb, 100);
  assert.equal(s.txTotalMb, 12);
  assert.equal(s.rxMaxMb, 60);
});

test('empty selections return a zero shape rather than throwing', () => {
  reset();
  const t = db.queryTrafficSummary(RA, 'no-such-iface', 0, Date.now());
  assert.equal(t.samples, 0);
  assert.equal(t.rxMaxMbps, null);
  assert.equal(t.rxP95Mbps, null, 'percentile must not run against a zero count');
  const b = db.queryBandwidthSummary(RA, 'no-such-iface', 0, Date.now());
  assert.equal(b.samples, 0);
  assert.equal(b.rxTotalMb, 0);
});

test('teardown', () => {
  db.close();
  fs.rmSync(TMP, { recursive: true, force: true });
});
