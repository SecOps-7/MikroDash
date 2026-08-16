'use strict';
// Tests for the historical data cleanup feature (#77): countPurge/purge scoping
// by router, data type and age; the preview matching the delete exactly; VACUUM
// actually shrinking the file; and stats() reporting size and per-router rows.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs   = require('fs');
const os   = require('os');
const path = require('path');

// db.js resolves DATA_DIR at require time, so point it at a temp dir first.
const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'mikrodash-db-'));
process.env.DATA_DIR = TMP;
const db = require('../src/db');

const DAY = 86400000;
const R1 = 'router-one';
const R2 = 'router-two';

// Seed a fixed spread of ages so age filters have something unambiguous to cut:
// one row per store, per router, for each entry in daysAgo.
//
// Only ping/traffic/bandwidth take a caller-supplied timestamp.
// insertConnectivityEvent stamps Date.now() itself, so those rows are always
// "now" and are deliberately excluded from the age arithmetic below.
const AGED_STORES = 3;
function seed(daysAgo) {
  const now = Date.now();
  for (const rid of [R1, R2]) {
    for (const d of daysAgo) {
      const ts = now - d * DAY;
      db.insertPingSample(rid, '1.1.1.1', 10, 0, ts);
      db.insertTrafficSample(rid, 'ether1', 1, 2, ts);
      db.insertBandwidthSample(rid, 'ether1', 3, 4, ts);
      db.insertConnectivityEvent(rid, 1);
    }
  }
}

function reset(daysAgo) {
  db.purge({});                       // no routerId, no types, no age = everything
  seed(daysAgo);
}

test('setup: open db and seed', () => {
  db.open();
  reset([0, 2, 10, 45, 200]);
  // 5 ages x 2 routers x 4 stores = 40 rows.
  assert.equal(db.stats().total, 40);
});

test('stats reports size, per-type and per-router breakdowns', () => {
  const s = db.stats();
  assert.ok(s.bytes > 0, 'reports a non-zero file size');
  assert.deepEqual(Object.keys(s.byType).sort(), ['bandwidth', 'events', 'ping', 'traffic']);
  assert.equal(s.byType.ping, 10);
  assert.equal(s.byType.events, 10, 'events counts connectivity + alert rows together');
  assert.equal(s.byRouter.length, 2);
  assert.equal(s.byRouter[0].rows, 20);
  assert.ok(s.oldestTs > 0 && s.oldestTs < Date.now(), 'oldestTs is epoch ms in the past');
});

test('countPurge matches exactly what purge deletes', () => {
  reset([0, 2, 10, 45, 200]);
  const opts = { routerId: R1, types: ['ping', 'traffic'], olderThanMs: 30 * DAY };
  const predicted = db.countPurge(opts);
  const actual = db.purge(opts);
  assert.equal(actual.deleted, predicted.total,
    'the preview count is the same predicate as the delete');
  assert.equal(predicted.total, 4, 'ages 45 and 200, across ping + traffic');
});

test('purge scoped to one router leaves the other untouched', () => {
  reset([0, 2, 10, 45, 200]);
  db.purge({ routerId: R1 });
  const s = db.stats();
  assert.equal(s.byRouter.length, 1);
  assert.equal(s.byRouter[0].routerId, R2);
  assert.equal(s.total, 20, 'only R2 rows remain');
});

test('purge scoped to data types leaves other types untouched', () => {
  reset([0, 2, 10, 45, 200]);
  db.purge({ types: ['ping'] });
  const s = db.stats();
  assert.equal(s.byType.ping, 0);
  assert.equal(s.byType.traffic, 10);
  assert.equal(s.byType.bandwidth, 10);
  assert.equal(s.byType.events, 10);
});

test('age filter keeps anything newer than the cutoff', () => {
  reset([0, 2, 10, 45, 200]);
  db.purge({ types: ['ping', 'traffic', 'bandwidth'], olderThanMs: 30 * DAY });
  const s = db.stats();
  // Ages 0, 2 and 10 survive: 3 ages x 2 routers, per aged store.
  const survivorsPerStore = 3 * 2;
  assert.equal(s.byType.ping, survivorsPerStore);
  assert.equal(s.byType.traffic, survivorsPerStore);
  assert.equal(s.byType.bandwidth, survivorsPerStore);
  assert.equal(s.total, survivorsPerStore * AGED_STORES + 10,
    'plus the 10 connectivity rows, which are stamped at insert time');
});

test('olderThanMs of 0 deletes regardless of age', () => {
  reset([0, 2, 10, 45, 200]);
  db.purge({ olderThanMs: 0 });
  assert.equal(db.stats().total, 0);
});

test('unknown data types are ignored rather than deleting everything', () => {
  reset([0, 2]);
  const before = db.stats().total;
  const res = db.purge({ types: ['not-a-type'] });
  assert.equal(res.deleted, 0);
  assert.equal(db.stats().total, before, 'a bogus type must not fall through to "all"');
});

test('countPurge on a router with no rows reports zero', () => {
  reset([0, 2]);
  assert.equal(db.countPurge({ routerId: 'no-such-router' }).total, 0);
});

test('vacuum shrinks the file after a large delete', () => {
  reset([]);
  // Enough rows that freed pages are measurable rather than lost to rounding.
  const now = Date.now();
  for (let i = 0; i < 20000; i++) db.insertTrafficSample(R1, 'ether1', 1, 2, now - i * 1000);
  const grown = db.stats().bytes;
  db.purge({});
  const v = db.vacuum();
  assert.ok(v.before >= grown * 0.5,
    'deleting rows alone does not return the space to disk');
  assert.ok(v.after < v.before, 'vacuum reclaims space');
  assert.ok(db.stats().bytes < grown, 'file is smaller than it was when full');
});

// ── Open-alert counts for the Routers page summary ──────────────────────────
// One grouped query feeds the "Alerting" card, refreshed every two seconds for
// every router a session can see. The thing worth pinning is that it counts only
// what is still open: a resolved alert must stop being reported, or the card
// would climb all day and never come back down.

test('countOpenAlertsByRouter counts only unresolved alerts, grouped by router', () => {
  for (const row of db.queryOpenAlerts(R1, 500)) db.resolveAlertEvent(R1, row.alert_type, row.subject);
  for (const row of db.queryOpenAlerts(R2, 500)) db.resolveAlertEvent(R2, row.alert_type, row.subject);
  assert.deepEqual(db.countOpenAlertsByRouter(), {}, 'nothing open to begin with');

  db.insertAlertEvent(R1, 'interface_down', 'ether1', 'ether1 went down');
  db.insertAlertEvent(R1, 'high_cpu', null, 'CPU at 95%');
  db.insertAlertEvent(R2, 'ping_loss', '1.1.1.1', 'loss 100%');

  assert.deepEqual(db.countOpenAlertsByRouter(), { [R1]: 2, [R2]: 1 });

  // Resolving one must decrement, not merely stop growing.
  db.resolveAlertEvent(R1, 'interface_down', 'ether1');
  assert.deepEqual(db.countOpenAlertsByRouter(), { [R1]: 1, [R2]: 1 });

  // A router with nothing open drops out entirely rather than reporting 0, so
  // the caller decides what "no alerts" looks like.
  db.resolveAlertEvent(R2, 'ping_loss', '1.1.1.1');
  const counts = db.countOpenAlertsByRouter();
  assert.equal(counts[R2], undefined, 'a router with nothing open is absent');
  assert.equal(counts[R1], 1);
});

test('teardown: close db', () => {
  db.close();
  fs.rmSync(TMP, { recursive: true, force: true });
});

test('countOpenAlertsByRouter fails soft with no database open', () => {
  // Same convention as every other accessor: an empty shape, never a throw. The
  // Routers page must still render if the database could not be opened.
  assert.deepEqual(db.countOpenAlertsByRouter(), {});
});
