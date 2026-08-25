'use strict';
/**
 * What the Backups table shows, and what it must never stop recording.
 *
 * A scheduled run that finds the configuration identical stores no pair, so
 * every one of those rows offered nothing to restore. On a stable router with a
 * daily schedule they arrive one a day and push the real restore points out of
 * the list. The table now shows only the newest of them.
 *
 * The row itself still has to be written, and that is the half worth guarding:
 * lastBackupRun() reads the newest run of ANY outcome and is what gates
 * isDue(). Stop recording unchanged runs and a stable router never advances its
 * last-run time, so it re-exports on every scheduler tick, forever.
 *
 * Separate file rather than an addition to backups.test.js because db.js
 * resolves DATA_DIR at REQUIRE time, while that suite sets it in test.before().
 * Requiring the database over there would open the live /data instead.
 */

const test = require('node:test');
const assert = require('node:assert');
const fs = require('fs');
const os = require('os');
const path = require('path');

const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'bkhist-'));
process.env.DATA_DIR = TMP;
const db = require('../src/db');

db.open();

test.after(() => {
  try { db.close(); } catch (_) {}
  fs.rmSync(TMP, { recursive: true, force: true });
});

const T0 = 1700000000000;
let seq = 0;
const record = (routerId, outcome, stem) => db.recordBackup({
  routerId, takenAt: T0 + (seq++ * 60000), outcome,
  stem: stem || null, fingerprint: 'fp-' + outcome,
});

test('only the newest unchanged run reaches the table', () => {
  // A realistic history: a backup, a stretch of quiet days, a failure, another
  // backup, then more quiet days.
  record('r1', 'changed', 'stem-a');
  record('r1', 'unchanged');
  record('r1', 'unchanged');
  record('r1', 'failed');
  record('r1', 'changed', 'stem-b');
  record('r1', 'unchanged');
  record('r1', 'unchanged');
  const newestUnchanged = record('r1', 'unchanged');

  const rows = db.listBackups('r1', 200);
  const unchanged = rows.filter(r => r.outcome === 'unchanged');

  assert.equal(unchanged.length, 1, 'a run of no-ops collapses to one line');
  assert.equal(unchanged[0].id, newestUnchanged, 'and it is the most recent one');

  // The inverse, so this cannot pass by filtering everything away: nothing that
  // is a restore point, or a problem, may be hidden.
  assert.equal(rows.filter(r => r.outcome === 'changed').length, 2,
    'both restore points must still be listed');
  assert.equal(rows.filter(r => r.outcome === 'failed').length, 1,
    'a failure is never hidden');
});

test('but every unchanged run is still recorded, or the schedule breaks', () => {
  // The load-bearing half. isDue() gates on lastBackupRun(), which reads the
  // newest run whatever its outcome, so an unchanged run has to move it
  // forward. If this ever regresses, a router whose configuration is stable
  // re-exports on every scheduler tick instead of once a day.
  const before = db.lastBackupRun('r1');
  const id = record('r1', 'unchanged');
  const after = db.lastBackupRun('r1');

  assert.ok(id, 'the run must still be written to config_backups');
  assert.ok(after > before, 'and it must advance the last-run time');
  assert.equal(db.getBackup(id).outcome, 'unchanged',
    'the row is filtered from the view, not from the table');
});

test('an unchanged run still carries the fingerprint forward', () => {
  // The other reader of these rows. latestFingerprint() is the drift baseline,
  // and it takes the newest row that HAS a fingerprint, so a run that found no
  // change must contribute one or a later failure would read as drift.
  assert.equal(db.latestFingerprint('r1'), 'fp-unchanged');
});

test('a router with nothing but unchanged runs still shows one', () => {
  // The edge the filter could plausibly get wrong: no changed row to anchor on.
  record('r2', 'unchanged');
  record('r2', 'unchanged');
  const rows = db.listBackups('r2', 200);
  assert.equal(rows.length, 1, 'one line, not none and not two');
  assert.equal(rows[0].outcome, 'unchanged');
});

test('a router with no runs at all lists nothing', () => {
  assert.deepEqual(db.listBackups('r-never', 200), []);
});

test('the filter does not reach across routers', () => {
  // The subquery picks the newest unchanged run; it must be scoped to the same
  // router, or one router's quiet day would suppress another's.
  const r1 = db.listBackups('r1', 200);
  assert.ok(r1.length > 0);
  assert.ok(r1.every(r => r.router_id === 'r1'), 'no other router may appear');
  assert.equal(db.listBackups('r2', 200).filter(r => r.outcome === 'unchanged').length, 1,
    'r2 keeps its own newest unchanged run');
});
