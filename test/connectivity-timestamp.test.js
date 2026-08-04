'use strict';
// Regression tests for #99: a debounced offline event must record the moment the
// disconnect was observed, not the moment the debounce expired. Getting this
// wrong makes every outage look shorter than it was by connDownThresholdSec.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs   = require('fs');
const os   = require('os');
const path = require('path');

const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'mikrodash-conn-'));
process.env.DATA_DIR = TMP;
const db = require('../src/db');

// db-writer is the layer the collectors call, so exercise the pass-through too.
const dbWriterPath = require.resolve('../src/db-writer');
delete require.cache[dbWriterPath];
const dbWriter = require('../src/db-writer');

const RID = 'router-99';

function events() {
  return db.queryConnectivityEvents(RID, 0, Date.now() + 60000, 100);
}

test('setup', () => {
  db.open();
  db.purge({});
});

test('insertConnectivityEvent honours an explicit timestamp', () => {
  db.purge({});
  const when = Date.now() - 5 * 60000;   // five minutes ago
  db.insertConnectivityEvent(RID, false, when);
  const rows = events();
  assert.equal(rows.length, 1);
  assert.equal(rows[0].ts, when, 'stores the supplied time, not now');
});

test('insertConnectivityEvent still defaults to now when no timestamp is given', () => {
  db.purge({});
  const before = Date.now();
  db.insertConnectivityEvent(RID, true);
  const rows = events();
  assert.equal(rows.length, 1);
  assert.ok(rows[0].ts >= before && rows[0].ts <= Date.now(),
    'immediate callers are unaffected by the new parameter');
});

test('recordConnectivity passes the timestamp through db-writer', () => {
  db.purge({});
  const when = Date.now() - 90000;
  dbWriter.recordConnectivity(RID, false, when);
  assert.equal(events()[0].ts, when);
});

test('recordConnectivity without a timestamp still stamps now', () => {
  db.purge({});
  const before = Date.now();
  dbWriter.recordConnectivity(RID, true);
  const ts = events()[0].ts;
  assert.ok(ts >= before && ts <= Date.now());
});

// The bug itself: the write happens inside a setTimeout, so "now" at write time
// is threshMs later than the disconnect. This reproduces the shape and shows the
// stored time is the observed one.
test('debounced offline records the disconnect time, not the declaration time', async () => {
  db.purge({});
  const THRESH = 120;                       // stand-in for connDownThresholdSec
  const downAt = Date.now();                // disconnect observed here
  await new Promise(r => setTimeout(r, THRESH));
  dbWriter.recordConnectivity(RID, false, downAt);   // declared here

  const offlineTs = events()[0].ts;
  assert.equal(offlineTs, downAt, 'stored time is when the router actually dropped');
  assert.ok(Date.now() - offlineTs >= THRESH,
    'and is measurably earlier than the moment we declared it');
});

test('downtime is no longer under-reported by the debounce window', () => {
  db.purge({});
  const THRESH   = 30000;                 // default connDownThresholdSec
  const downAt   = Date.now() - 300000;   // outage began 5 minutes ago
  const backUpAt = Date.now();            // recovered now
  dbWriter.recordConnectivity(RID, false, downAt);
  dbWriter.recordConnectivity(RID, true, backUpAt);

  const rows = events().sort((a, b) => a.ts - b.ts);
  const measured = rows[1].ts - rows[0].ts;
  assert.equal(measured, 300000, 'full 5 minutes, not 4m30s');

  // What the old behaviour produced, for contrast.
  const oldStyle = backUpAt - (downAt + THRESH);
  assert.equal(oldStyle, 270000);
  assert.ok(measured > oldStyle, 'the fix reports a longer, correct outage');
});

test('teardown', () => {
  db.close();
  fs.rmSync(TMP, { recursive: true, force: true });
});
