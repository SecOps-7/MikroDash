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

// ── One open alert per (router, type, subject) ───────────────────────────────
//
// The bell kept showing new "update available" entries for a router whose alert
// had already been acknowledged. The evaluator keeps edge-detection state in
// memory, and dropEvaluator() wipes it on a router switch, a session rebuild
// and — most often — an idle teardown, when nobody has had the router's page
// open for a while. The rebuilt evaluator has no memory of having reported the
// condition, so it reports it again.
//
// hasOpenAlert() moves that memory into the database, where it survives all
// three, and covers every alert type rather than the one that was noticed.

test('an alert already open is reported as open', () => {
  assert.equal(db.hasOpenAlert('r-dup', 'routeros_update', null), false,
    'nothing open to begin with');
  db.insertAlertEvent('r-dup', 'routeros_update', null, 'RouterOS 7.24 is available');
  assert.equal(db.hasOpenAlert('r-dup', 'routeros_update', null), true);
});

test('acknowledging does not reopen the door', () => {
  // "Dismiss" in the bell acknowledges; it does not resolve. An acknowledged but
  // unresolved alert must still count as open, or dismissing it achieves nothing
  // — which was exactly the reported symptom.
  const id = db.insertAlertEvent('r-ack', 'routeros_update', null, 'update');
  db.acknowledgeAlert(id, 'someone');
  assert.equal(db.hasOpenAlert('r-ack', 'routeros_update', null), true,
    'acknowledged is not resolved');
});

test('once resolved it is no longer open, and may fire again', () => {
  // The rule is about duplicates, not about never alerting twice: a router that
  // updates and later has another update available must alert again.
  db.insertAlertEvent('r-cycle', 'routeros_update', null, 'update');
  db.resolveAlertEvent('r-cycle', 'routeros_update', null);
  assert.equal(db.hasOpenAlert('r-cycle', 'routeros_update', null), false);
});

test('the same type on different subjects and routers stays separate', () => {
  // ether5 being down says nothing about ether6, and one router says nothing
  // about another. Matching on type alone would swallow real alerts.
  db.insertAlertEvent('r-a', 'interface_down', 'ether5', 'down');
  assert.equal(db.hasOpenAlert('r-a', 'interface_down', 'ether5'), true);
  assert.equal(db.hasOpenAlert('r-a', 'interface_down', 'ether6'), false,
    'a different subject is a different alert');
  assert.equal(db.hasOpenAlert('r-b', 'interface_down', 'ether5'), false,
    'a different router is a different alert');
});

test('with no database open the answer is "not open", not a throw', () => {
  // Callers then behave as they did before: say something rather than nothing.
  // Suppressing alerts because the database is unavailable would be the worst
  // possible failure mode for an alerting system.
  db.close();
  assert.equal(db.hasOpenAlert('r-a', 'interface_down', 'ether5'), false);
  db.open();
});

// ── Clearing a router's alerts ───────────────────────────────────────────────
//
// The Routers page counts alerts that are unresolved. An alert whose condition
// went away without the evaluator ever seeing it clear — the router was removed
// and re-added, the threshold was changed, the collector was not running at the
// moment it recovered — stays unresolved forever, and the router reads
// "Alerting" with nothing wrong. "Clear all" in the bell is the way out.
//
// It used to only acknowledge, which empties the bell and changes the count not
// at all. That is why these tests assert on countOpenAlertsByRouter rather than
// on the bell: the bell was never the thing that was broken.

test('clearing resolves every open alert for the router', () => {
  db.insertAlertEvent('r-clr', 'routeros_update', null, 'RouterOS 7.24 is available');
  db.insertAlertEvent('r-clr', 'interface_down', 'ether1', 'ether1 went down');
  assert.equal(db.countOpenAlertsByRouter()['r-clr'], 2);

  const ids = db.resolveAllAlerts('r-clr', 'operator');
  assert.equal(ids.length, 2, 'returns the affected ids, so the change can be broadcast');
  assert.equal(db.countOpenAlertsByRouter()['r-clr'], undefined,
    'the router drops out of the Alerting count entirely');
});

test('clearing keeps the history rather than deleting it', () => {
  // The whole reason this resolves instead of deleting. Reports and the CSV
  // export must still show that the alert happened.
  db.insertAlertEvent('r-hist', 'high_cpu', null, 'CPU at 95%');
  db.resolveAllAlerts('r-hist', 'operator');

  const rows = db.queryRecentAlerts('r-hist', 0, 50);
  assert.equal(rows.length, 1, 'the row survives');
  assert.equal(rows[0].detail, 'CPU at 95%');
  assert.ok(rows[0].resolved_at > 0, 'and is stamped resolved');
});

test('clearing records who did it', () => {
  // Otherwise a person clearing the list is indistinguishable in Reports from
  // the evaluator having resolved it on its own.
  db.insertAlertEvent('r-who', 'ping_loss', '1.1.1.1', 'loss 100%');
  db.resolveAllAlerts('r-who', 'operator-1');
  assert.equal(db.queryRecentAlerts('r-who', 0, 50)[0].acknowledged_by, 'operator-1');
});

test('clearing does not steal an acknowledgement someone else made', () => {
  const id = db.insertAlertEvent('r-ack2', 'interface_down', 'ether3', 'down');
  db.acknowledgeAlert(id, 'first-responder');
  db.resolveAllAlerts('r-ack2', 'someone-else');
  const row = db.queryRecentAlerts('r-ack2', 0, 50)[0];
  assert.equal(row.acknowledged_by, 'first-responder', 'the original name stands');
  assert.ok(row.resolved_at > 0, 'but it is still resolved');
});

test('clearing one router leaves every other router alone', () => {
  // The button is scoped to the active router. Clearing the fleet by accident
  // would destroy exactly the signal the Routers page exists to show.
  db.insertAlertEvent('r-mine',  'high_cpu', null, 'CPU at 95%');
  db.insertAlertEvent('r-yours', 'high_cpu', null, 'CPU at 95%');
  db.resolveAllAlerts('r-mine', 'operator');
  const counts = db.countOpenAlertsByRouter();
  assert.equal(counts['r-mine'], undefined);
  assert.equal(counts['r-yours'], 1, 'the other router still reports its alert');
});

test('clearing does not re-stamp alerts that were already resolved', () => {
  // Re-stamping would move an outage that ended last week to today, which is
  // the one thing that would make the history actively misleading.
  db.insertAlertEvent('r-twice', 'ping_loss', '8.8.8.8', 'loss 100%');
  db.resolveAllAlerts('r-twice', 'operator');
  const firstAt = db.queryRecentAlerts('r-twice', 0, 50)[0].resolved_at;

  db.insertAlertEvent('r-twice', 'high_cpu', null, 'CPU at 95%');
  db.resolveAllAlerts('r-twice', 'operator');

  const ping = db.queryRecentAlerts('r-twice', 0, 50)
    .find(r => r.alert_type === 'ping_loss');
  assert.equal(ping.resolved_at, firstAt, 'the earlier resolution time is untouched');
});

test('clearing with nothing open reports nothing cleared', () => {
  // The endpoint only broadcasts when ids come back, so an empty array is what
  // stops a second click from telling every other browser something happened.
  assert.deepEqual(db.resolveAllAlerts('r-empty', 'operator'), []);
});

test('clearing with no database open returns nothing rather than throwing', () => {
  db.close();
  assert.deepEqual(db.resolveAllAlerts('r-clr', 'operator'), []);
  db.open();
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

