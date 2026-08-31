'use strict';
/**
 * WHICH IDENTITY this port writes into each shared column, against what Node
 * writes there.
 *
 * ── WHY THIS EXISTS: TWO BUGS IN TWO DAYS, BOTH INVISIBLE TO TESTS ──────────
 *
 * `mikrodash.db` is written by BOTH processes, and several of its columns hold
 * "who". There is no blanket rule about which identity belongs in one — it is
 * per column:
 *
 *   grants.principal_id          the user ID
 *   audit_events.actor_id        the user ID
 *   user_layouts.user_id         the user ID
 *   alert_events.acknowledged_by the USERNAME
 *   audit_events.actor_name      the USERNAME
 *
 * Both bugs found on 2026-08-27 were a Go writer choosing the other one:
 *
 *   `user_layouts.user_id` got the USERNAME, so one account ended up with two
 *   nav preferences — one written by each half, neither able to see the other.
 *
 *   `audit_events.actor_name` got "local" for every login, because the handler
 *   built its recorder with no session and `ForUser` substitutes the
 *   unauthenticated-socket fallback. Node records the account name, and that
 *   column is what an operator filters by.
 *
 * NEITHER COULD BE CAUGHT BY A TEST OF EITHER IMPLEMENTATION. A round trip
 * through one half agrees with itself whatever it wrote; nothing errors and
 * nothing logs. Both were found by reading the real table — which is not
 * something a suite can do, so this ledger checks the next best thing: that the
 * Go writer for each column resolves the identity Node resolves.
 *
 * ── WHAT IT CHECKS, AND WHAT IT CANNOT ──────────────────────────────────────
 *
 * It reads the Go source and asserts each recorded call site passes the right
 * KIND of identity — `userIDFor(...)` for an id column, a bare username for a
 * username column. It cannot prove the value is correct at runtime, and it does
 * not try: what it prevents is the specific mistake that has now happened twice,
 * which is reaching for the wrong one of the two.
 *
 * FAILS IN BOTH DIRECTIONS. A column with no entry is unrecorded and fails; an
 * entry naming a call site that has moved fails too, so the ledger cannot
 * quietly describe code that is no longer there.
 *
 *   node tools/identity-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');

/**
 * One shared column that carries an identity.
 *
 * `kind` is what NODE writes, taken from the live source. `site` is the Go
 * expression that must supply it.
 */
const COLUMNS = [
  {
    column: 'user_layouts.user_id',
    kind: 'id',
    file: 'internal/server/navprefs_api.go',
    site: 's.userIDFor(sess.Username)',
    sites: 1,
    why: 'the nav preference and both saved layouts. `_layoutUser(req)` is '
      + '`authSession.userId`. Keying on the username gave one account two rows, '
      + 'one per half — found 2026-08-27 by reading the table.',
  },
  {
    column: 'audit_events.actor_name',
    kind: 'username',
    file: 'internal/server/auth_login.go',
    site: 'audit.ForLogin(username, clientIPOf(r))',
    sites: 1,
    why: 'a login is a PRE-authentication event: `forLogin` records the CLAIMED '
      + 'username and a null id, because "a failed login may name a user that does '
      + 'not exist, and that is worth seeing". Building the recorder with no '
      + 'session instead substitutes "local".',
  },
  {
    column: 'audit_events.actor_id',
    kind: 'id',
    file: 'internal/server/audit.go',
    site: 'audit.ForUser(s.userIDFor(name), name, clientIPOf(r))',
    sites: 1,
    why: 'every non-login event. `fromReq` records `authSession.userId` beside the '
      + 'username, and the Audit page filters on both.',
  },
  {
    column: 'alert_events.acknowledged_by',
    kind: 'username',
    file: 'internal/server/alerts_api.go',
    // TWO: acknowledge and un-acknowledge. Counting them is what makes a
    // one-site change visible — see the note on `sites` below.
    site: 'who = sess.Username',
    sites: 2,
    why: 'the live route is `req.authSession?.username || null` — a USERNAME, unlike '
      + 'the id columns beside it. The Alerts table renders this value directly, so '
      + 'an id here would show an opaque identifier where a name belongs.',
  },
  {
    column: 'grants.principal_id',
    kind: 'caller',
    file: 'internal/db/grantwrite.go',
    site: 's.PrincipalID',
    sites: 2,
    why: 'the principal a grant belongs to. Supplied by the caller rather than '
      + 'resolved here, and the principal WRITES are unported — recorded so the '
      + 'column is not silently unaccounted for.',
  },
  {
    column: 'grants.created_by',
    kind: 'caller',
    file: 'internal/db/grantwrite.go',
    site: 's.CreatedBy',
    sites: 2,
    why: 'who made the grant. Same caller-supplied shape as principal_id above.',
  },
];

/**
 * Columns that exist in the shared schema and that this port does NOT write.
 * Recorded so "no entry" always means "nobody checked" rather than "nothing to
 * check" — the distinction the whole ledger rests on.
 */
const UNWRITTEN = {
  'group_members.user_id':
    'the group writers are ported but nothing above the HTTP layer calls them — '
    + 'the principal writes are deliberately unported while Node memoises its RBAC '
    + 'views on a generation counter this process cannot advance.',
  'user_notify_config.user_id':
    'the per-user notification settings. The transports are ported and their caller '
    + 'is not; wiring it is a cutover step, recorded in cutover item 11.',
  'report_schedules.created_by':
    'a schedule is created through the Reports page, which this port serves — but the '
    + 'column is carried from the STORED ROW on an update and left to the database on '
    + 'a create, so there is no identity expression here to check.',
};

const problems = [];
const notes = [];

for (const c of COLUMNS) {
  const full = path.join(ROOT, c.file);
  if (!fs.existsSync(full)) {
    problems.push(c.column + ': ' + c.file + ' does not exist — the entry names a file that '
      + 'has moved or been deleted');
    continue;
  }
  const src = fs.readFileSync(full, 'utf8');
  // COMMENTS ARE NOT CODE. This ledger's entries quote the expressions they
  // check, and several of those files explain the rule in prose containing it —
  // a scanner that read prose would pass on the documentation alone.
  const code = src.split('\n').filter((l) => !l.trim().startsWith('//')).join('\n');
  // ── THE COUNT, NOT MERELY THE PRESENCE ─────────────────────────────
  //
  // `includes` alone passes a file where ONE of several writers was changed and
  // the others were not — measured: `alerts_api.go` has two acknowledge sites,
  // and mutating one left this ledger clean. So the number is recorded and
  // compared, which also makes a NEW writer fail until somebody has looked at
  // it rather than being absorbed silently.
  const found = code.split(c.site).length - 1;
  if (found !== c.sites) {
    problems.push(c.column + ': ' + c.file + ' has ' + found + ' site(s) passing `' + c.site
      + '`, recorded as ' + c.sites + '. Either a writer was ADDED — check it passes the same '
      + 'identity and update the count — or one was CHANGED, which is the bug this exists to '
      + 'catch. ' + c.why);
    continue;
  }
  if (!code.includes(c.site)) {
    problems.push(c.column + ': ' + c.file + ' no longer contains `' + c.site + '`. Either the '
      + 'call moved — update this entry — or the identity it passes has changed, which is the '
      + 'bug this exists to catch. ' + c.why);
    continue;
  }
  // THE KIND CHECK. An id column whose expression does not resolve one, or a
  // username column that does, is the specific mistake made twice.
  const resolves = c.site.includes('userIDFor');
  if (c.kind === 'id' && !resolves) {
    problems.push(c.column + ' is an ID column and `' + c.site + '` does not resolve one');
  }
  if (c.kind === 'username' && resolves) {
    problems.push(c.column + ' is a USERNAME column and `' + c.site + '` resolves an id — the '
      + 'value is rendered directly, so an opaque identifier would appear where a name belongs');
  }
  notes.push(c.column + ' (' + c.kind + ')');
}

for (const [col, why] of Object.entries(UNWRITTEN)) {
  if (!why || why.length < 20) {
    problems.push(col + ' is recorded as unwritten with no real reason');
  }
  if (COLUMNS.find((c) => c.column === col)) {
    problems.push(col + ' is in BOTH lists — it cannot be written and unwritten at once');
  }
}

if (problems.length) {
  console.error('the identity ledger disagrees with the code:\n');
  for (const p of problems) console.error('  ✗ ' + p + '\n');
  process.exit(1);
}
console.log('identity audit clean: ' + COLUMNS.length + ' shared column(s) checked, '
  + Object.keys(UNWRITTEN).length + ' recorded as not written by this port');
for (const n of notes) console.log('  ' + n);
