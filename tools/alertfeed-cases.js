'use strict';
/**
 * THE ALERT FEED — `queryOpenAlerts` and `queryRecentAlerts`, run for real.
 *
 * The notification bell reads both. `internal/db/alerts.go` records the rule
 * that kept them unported — "a read nothing calls is a read nothing gates" — and
 * this file is how that rule is honoured rather than waived: the queries land
 * WITH a corpus that drives them, so there is no window in which they exist and
 * nothing exercises them.
 *
 * ---- THE TWO ARE NOT THE SAME QUERY WITH A FLAG ---------------------------
 *
 *   open     `resolved_at IS NULL`, newest FIRED first.
 *   recent   `resolved_at IS NOT NULL AND resolved_at >= ?`, newest RESOLVED
 *            first.
 *
 * "Recent" means RESOLVED — an alert that is still open is never in it, however
 * recently it fired. A port that read "recent" as "lately" would put every open
 * alert in both lists and the bell would double-count.
 *
 * They also sort on DIFFERENT COLUMNS, which only shows up when the two orders
 * disagree: an alert that fired early and resolved late outranks one that fired
 * late and resolved early. The seed has exactly that pair, and a believability
 * assertion refuses to pass if sorting by the wrong column would give the same
 * answer.
 *
 * ---- AND THE LIMIT IS `limit || N` ----------------------------------------
 *
 * A limit of ZERO is falsy, so it takes the default — 200 for open, 50 for
 * recent — rather than returning nothing. A port passing the zero through
 * returns an empty feed and the bell silently shows nothing.
 *
 * ── CONTAINER ONLY ─────────────────────────────────────────────────────────
 *
 * `src/db.js` requires better-sqlite3, which is native and installed only where
 * the app runs:
 *
 *   docker exec -e MIKRODASH_SRC=/app mikrodash node /work/tools/alertfeed-cases.js
 *
 * DATA_DIR is pointed at a temp directory BEFORE src/db.js is required, because
 * that module resolves its path at load time. Getting that wrong would open the
 * real database and run migrations on it.
 */

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const OUT = process.env.ALERTFEED_OUT || path.join(ROOT, 'testdata', 'alertfeed-cases.json');

// BEFORE the require, not after.
const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'mikrodash-alertfeed-'));
process.env.DATA_DIR = TMP;

const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const db = require(path.join(SRC, 'src', 'db.js'));

const R = 'router-a';
const OTHER = 'router-b';

// A fixed base instant so the file is identical on every run.
const BASE = Date.parse('2026-01-01T00:00:00Z');
const MIN = 60 * 1000;
const HOUR = 60 * MIN;

/**
 * The rows, chosen for the edges rather than for volume.
 *
 * The pair that matters most is `early-fired-late-resolved` and
 * `late-fired-early-resolved`: they order one way by `fired_at` and the other by
 * `resolved_at`, so a port sorting the recent feed on the wrong column produces
 * a list that looks plausible and is backwards.
 */
const ROWS = [
  // ---- open (resolved_at NULL) ----
  { router: R, type: 'cpu', subject: 'CPU', detail: 'high', fired: BASE + 1 * HOUR, resolved: null },
  { router: R, type: 'link', subject: 'ether1', detail: 'down', fired: BASE + 3 * HOUR, resolved: null },
  { router: R, type: 'ping', subject: '198.51.100.1', detail: 'loss', fired: BASE + 2 * HOUR, resolved: null },
  // An open alert on ANOTHER router must never appear in this router's feed.
  { router: OTHER, type: 'cpu', subject: 'CPU', detail: 'high', fired: BASE + 4 * HOUR, resolved: null },

  // ---- resolved ----
  // THE ORDER-DISAGREEING PAIR.
  { router: R, type: 'early-fired-late-resolved', subject: 'a', detail: '',
    fired: BASE + 1 * MIN, resolved: BASE + 10 * HOUR },
  { router: R, type: 'late-fired-early-resolved', subject: 'b', detail: '',
    fired: BASE + 9 * HOUR, resolved: BASE + 9 * HOUR + MIN },
  // Straddling the `since` boundary used below: one exactly ON it, one before.
  { router: R, type: 'exactly-at-since', subject: 'c', detail: '',
    fired: BASE, resolved: BASE + 5 * HOUR },
  { router: R, type: 'before-since', subject: 'd', detail: '',
    fired: BASE, resolved: BASE + 5 * HOUR - 1 },
  // Resolved on another router.
  { router: OTHER, type: 'other-resolved', subject: 'e', detail: '',
    fired: BASE, resolved: BASE + 6 * HOUR },
  // Null subject and detail: the columns are nullable and the bell renders them.
  { router: R, type: 'nulls', subject: null, detail: null,
    fired: BASE + 7 * HOUR, resolved: BASE + 8 * HOUR },
];

const SINCE = BASE + 5 * HOUR;

// OPENED FIRST, and it returns the handle.
//
// Every write in src/db.js begins `if (!_db) return`, so without this the seed
// would insert nothing, the queries would answer nothing, and the corpus would
// record ten empty results as though they were the answer. Found by the direct
// handle failing with "no such table" — the file did not exist at all.
const handle = db.open();
assert.ok(handle, 'db.open() returned no handle');

// The rows go in through the LIVE insert, so the column set and its coercions
// are the app's — `subject || null` turns an empty string into a null, which is
// exactly the sort of thing a hand-written INSERT would get wrong.
for (const r of ROWS) {
  db.insertAlertEvent(r.router, r.type, r.subject, r.detail);
}

// THE TIMESTAMPS ARE SET DIRECTLY, on the handle db.open() just returned.
//
// Both are stamped `Date.now()` by the live code — `insertAlertEvent` sets
// `fired_at` and `resolveAlertEvent` sets `resolved_at` — so going through
// either would make this corpus different on every run and `--check` would never
// pass. The rows need chosen instants.
for (const r of ROWS) {
  const n = handle.prepare(
    'UPDATE alert_events SET fired_at = ?, resolved_at = ? WHERE router_id = ? AND alert_type = ?'
  ).run(r.fired, r.resolved, r.router, r.type).changes;
  assert.equal(n, 1, 'expected exactly one row for ' + r.type + ', updated ' + n);
}

const strip = (rows) => rows.map((r) => ({
  alert_type: r.alert_type, subject: r.subject, detail: r.detail,
  fired_at: r.fired_at, resolved_at: r.resolved_at,
  acknowledged_at: r.acknowledged_at, acknowledged_by: r.acknowledged_by,
}));

const cases = {
  openDefault: { router: R, limit: null, rows: strip(db.queryOpenAlerts(R, null)) },
  openLimit2: { router: R, limit: 2, rows: strip(db.queryOpenAlerts(R, 2)) },
  // A limit of ZERO takes the DEFAULT, because `limit || 200` is falsy on 0.
  openLimitZero: { router: R, limit: 0, rows: strip(db.queryOpenAlerts(R, 0)) },
  openOtherRouter: { router: OTHER, limit: null, rows: strip(db.queryOpenAlerts(OTHER, null)) },
  openUnknownRouter: { router: 'nope', limit: null, rows: strip(db.queryOpenAlerts('nope', null)) },

  recentDefault: { router: R, since: SINCE, limit: null,
    rows: strip(db.queryRecentAlerts(R, SINCE, null)) },
  recentSinceZero: { router: R, since: 0, limit: null,
    rows: strip(db.queryRecentAlerts(R, 0, null)) },
  recentSinceNull: { router: R, since: null, limit: null,
    rows: strip(db.queryRecentAlerts(R, null, null)) },
  recentLimit1: { router: R, since: 0, limit: 1, rows: strip(db.queryRecentAlerts(R, 0, 1)) },
  recentLimitZero: { router: R, since: 0, limit: 0, rows: strip(db.queryRecentAlerts(R, 0, 0)) },
  recentOtherRouter: { router: OTHER, since: 0, limit: null,
    rows: strip(db.queryRecentAlerts(OTHER, 0, null)) },
};

// ---- BELIEVABILITY -------------------------------------------------------
{
  const types = (k) => cases[k].rows.map((r) => r.alert_type);

  // Open: three on this router, newest FIRED first, and nothing resolved.
  assert.deepEqual(types('openDefault'), ['link', 'ping', 'cpu'],
    'the open feed is not ordered by fired_at DESC');
  for (const r of cases.openDefault.rows) {
    assert.equal(r.resolved_at, null, 'a resolved alert appeared in the OPEN feed');
  }
  assert.deepEqual(types('openOtherRouter'), ['cpu'], 'the other router sees the wrong rows');
  assert.deepEqual(types('openUnknownRouter'), [],
    'an unknown router got somebody else\'s alerts');

  // The limit, and the falsy zero.
  assert.equal(cases.openLimit2.rows.length, 2, 'the open limit is ignored');
  assert.equal(cases.openLimitZero.rows.length, cases.openDefault.rows.length,
    'a limit of 0 returned nothing — `limit || 200` makes zero the DEFAULT, and a port '
    + 'passing it through would leave the bell silently empty');

  // Recent: RESOLVED only, newest RESOLVED first.
  for (const r of cases.recentSinceZero.rows) {
    assert.ok(r.resolved_at != null, 'an OPEN alert appeared in the RECENT feed');
  }
  assert.deepEqual(types('recentSinceZero'),
    ['early-fired-late-resolved', 'late-fired-early-resolved', 'nulls',
      'exactly-at-since', 'before-since'],
    'the recent feed is not ordered by resolved_at DESC');
  // The order-disagreeing pair proves the sort column.
  const byFired = cases.recentSinceZero.rows.slice().sort((a, b) => b.fired_at - a.fired_at)
    .map((r) => r.alert_type);
  assert.notDeepEqual(types('recentSinceZero'), byFired,
    'sorting by fired_at gives the same answer here, so the seed cannot tell the two apart');

  // The `since` boundary is INCLUSIVE.
  assert.ok(types('recentDefault').includes('exactly-at-since'),
    'a row resolved exactly at `since` was excluded — the comparison is >=');
  assert.ok(!types('recentDefault').includes('before-since'),
    'a row resolved one millisecond before `since` was included');

  assert.deepEqual(types('recentSinceNull'), types('recentSinceZero'),
    'a null `since` is not being treated as 0');
  assert.equal(cases.recentLimit1.rows.length, 1, 'the recent limit is ignored');
  assert.equal(cases.recentLimitZero.rows.length, cases.recentSinceZero.rows.length,
    'a recent limit of 0 returned nothing rather than taking the default of 50');
  assert.deepEqual(types('recentOtherRouter'), ['other-resolved'],
    'the other router sees the wrong resolved rows');

  // Nullable columns survive as nulls rather than empty strings.
  const nulls = cases.recentSinceZero.rows.find((r) => r.alert_type === 'nulls');
  assert.equal(nulls.subject, null, 'a null subject came back as something else');
  assert.equal(nulls.detail, null, 'a null detail came back as something else');
  // Nothing is acknowledged in this seed, so both columns must be null
  // everywhere — a port defaulting them to a zero timestamp would show every
  // alert as already seen.
  for (const c of Object.values(cases)) {
    for (const r of c.rows) {
      assert.equal(r.acknowledged_at, null, 'an unacknowledged alert has a timestamp');
      assert.equal(r.acknowledged_by, null, 'an unacknowledged alert has an acknowledger');
    }
  }
}

const payload = JSON.stringify({
  note: 'GENERATED by tools/alertfeed-cases.js from the live src/db.js. Do not edit.',
  seed: ROWS, since: SINCE, cases,
}, null, 1) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== payload) {
    console.error('alertfeed-cases: testdata/alertfeed-cases.json is stale — re-run without --check');
    process.exit(1);
  }
  console.log('alertfeed-cases: up to date');
} else {
  fs.writeFileSync(OUT, payload);
  console.log('alertfeed-cases: wrote %d seed rows and %d query cases',
    ROWS.length, Object.keys(cases).length);
}
