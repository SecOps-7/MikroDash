'use strict';
/**
 * Backup history cases — what the LIVE queries return, for the Go port.
 *
 * ── IT SEEDS A DATABASE RATHER THAN READING ONE ─────────────────────────────
 *
 * The interesting rows are the ones a healthy fleet rarely produces: a run that
 * failed and stored nothing, a pair retention has pruned, a router with no
 * history at all. Those decide what the summary cards say, and a real /data has
 * few of them — besides belonging to one operator and not in a public repo.
 *
 * ── THE EDGES THAT MATTER ───────────────────────────────────────────────────
 *
 *   stem NULL          a run that happened and stored nothing. It still counts
 *                      as a RUN and must not count as STORED.
 *   pruned_at set      retention removed the files; the row stays so History can
 *                      explain the disappearance. Also not STORED, and its bytes
 *                      must leave the disk total.
 *   no rows at all     SUM over no rows is NULL in SQLite, not 0. A port scanning
 *                      that into a plain int64 errors; one defaulting wrongly
 *                      reports a fleet member as using negative disk.
 *   another router     every query is scoped, and a second router's rows must
 *                      never reach the first one's totals.
 *   lastOutcome        comes from the NEWEST row whatever it says, so a router
 *                      whose last run failed shows "failed" rather than the last
 *                      successful one.
 *
 * ── CONTAINER ONLY ──────────────────────────────────────────────────────────
 *
 * `src/db.js` requires better-sqlite3, which is native and installed only where
 * the app runs:
 *
 *   docker exec mikrodash rm -rf /tools /bkcases.json
 *   docker cp tools mikrodash:/tools
 *   docker exec -e MIKRODASH_SRC=/app -e BK_OUT=/bkcases.json \
 *     mikrodash node /tools/backup-history-cases.js
 *   docker cp mikrodash:/bkcases.json testdata/backup-history-cases.json
 *
 * DATA_DIR is pointed at a temp directory BEFORE src/db.js is required, because
 * that module resolves its path at load time. Getting that wrong would open the
 * real database and run migrations on it.
 */

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const OUT = process.env.BK_OUT || path.join(__dirname, '..', 'testdata', 'backup-history-cases.json');

// BEFORE the require, not after.
const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'mikrodash-bkcases-'));
process.env.DATA_DIR = TMP;

const ROOT = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const db = require(path.join(ROOT, 'src', 'db.js'));

const R = 'router-a';
const OTHER = 'router-b';
const EMPTY = 'router-empty';

const BASE = Date.parse('2026-01-01T00:00:00Z');
const HOUR = 3600 * 1000;
const DAY = 24 * HOUR;

// Rows chosen for the edges above, oldest first so `taken_at DESC` has work.
//
// CAMELCASE WITH AN `identity` SUB-OBJECT, because that is what `recordBackup`
// binds — and it SWALLOWS a bad shape, returning null and logging. The first
// version of this file used snake_case, inserted nothing, and said nothing; the
// assertion below is why that is now a failure rather than an empty corpus.
const ROWS = [
  { routerId: R, takenAt: BASE, outcome: 'changed', source: 'schedule',
    stem: '2026-01-01T000000', dir: '/data/config-backups/router-a',
    fingerprint: 'f1', rscBytes: 1000, backupBytes: 4000, ms: 5000,
    identity: { model: 'hAP ax^3', serial: 'S1', osVersion: '7.24' } },
  // This one gets pruned below: the row stays, the files go.
  { routerId: R, takenAt: BASE + DAY, outcome: 'changed', source: 'schedule',
    stem: '2026-01-02T000000', dir: '/data/config-backups/router-a',
    fingerprint: 'f2', rscBytes: 2000, backupBytes: 8000, ms: 5200,
    identity: { model: 'hAP ax^3', serial: 'S1', osVersion: '7.24' } },
  // Unchanged: a real run with a fingerprint and no pair.
  { routerId: R, takenAt: BASE + 2 * DAY, outcome: 'unchanged', source: 'schedule',
    fingerprint: 'f2', ms: 1200,
    identity: { model: 'hAP ax^3', serial: 'S1', osVersion: '7.24' } },
  // Manual run by a named human.
  { routerId: R, takenAt: BASE + 3 * DAY, outcome: 'changed', source: 'manual', actor: 'alice',
    stem: '2026-01-04T000000', dir: '/data/config-backups/router-a',
    fingerprint: 'f3', rscBytes: 1500, backupBytes: 6500, ms: 4800,
    identity: { model: 'hAP ax^3', serial: 'S1', osVersion: '7.25' } },
  // The NEWEST row failed — lastOutcome must say so.
  { routerId: R, takenAt: BASE + 4 * DAY, outcome: 'failed', source: 'schedule',
    ms: 900, error: 'no space left on device' },
  // A second router, to prove every query is scoped.
  { routerId: OTHER, takenAt: BASE + 10 * DAY, outcome: 'changed', source: 'schedule',
    stem: '2026-01-11T000000', dir: '/data/config-backups/router-b',
    fingerprint: 'g1', rscBytes: 999999, backupBytes: 999999, ms: 3000,
    identity: { model: 'cAP ax', serial: 'S2', osVersion: '7.24' } },
];

// `db.open()` FIRST. Every function in src/db.js begins `if (!_db) return ...`,
// so without it recordBackup returns null having logged nothing at all — the
// same silent-empty-corpus failure the assertion below exists to catch, reached
// by a different road.
db.open();

const ids = ROWS.map((r) => {
  const id = db.recordBackup(r);
  if (!id) {
    console.error('recordBackup returned null — it swallows a bad row shape, so ' +
                  'this would otherwise produce an empty corpus that passes.');
    process.exit(1);
  }
  return id;
});
// Retention removed the second pair's FILES. Done through the real function so
// whatever it sets is what the port has to read.
db.markBackupPruned(ids[1], BASE + 5 * DAY);

const answers = {
  listAll: db.listBackups(R, 200),
  listLimited: db.listBackups(R, 2),
  listZeroLimit: db.listBackups(R, 0),
  listOther: db.listBackups(OTHER, 200),
  listEmpty: db.listBackups(EMPTY, 200),
  stored: db.storedBackups(R),
  storedEmpty: db.storedBackups(EMPTY),
  lastRun: db.lastBackupRun(R),
  lastRunEmpty: db.lastBackupRun(EMPTY),
  summary: db.backupSummary(R),
  summaryOther: db.backupSummary(OTHER),
  summaryEmpty: db.backupSummary(EMPTY),
};
// getBackup by the id the first row actually received.
answers.getFirst = db.getBackup(ids[0]);
answers.getMissing = db.getBackup(999999);

db.close();

const out = JSON.stringify({ rows: ROWS, prunedIndex: 1, prunedAt: BASE + 5 * DAY, answers }, null, 2) + '\n';
if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== out) { console.error('backup-history-cases.json is stale'); process.exit(1); }
  console.log('backup-history-cases.json is up to date');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, out);
  console.log('wrote ' + OUT + ' — ' + ROWS.length + ' rows, ' +
              Object.keys(answers).length + ' answers; summary=' + JSON.stringify(answers.summary));
}
