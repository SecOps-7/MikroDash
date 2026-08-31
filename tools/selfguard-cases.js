#!/usr/bin/env node
'use strict';
/**
 * Pin the lockout guard against the LIVE implementation.
 *
 * WHY THIS ONE ESPECIALLY. Every other guard in this app warns; this one
 * refuses, and what it refuses is the thing that cannot be undone from inside
 * the app. If it wrongly allows, somebody drives to a site with a serial cable.
 * There is no fixture for it — it is pure logic over rows — so the only way to
 * know the port agrees with the original is to run both.
 *
 * The cases are synthetic and the ANSWERS are not: this runs the live
 * `selfGuard` over each scenario and records what it returns.
 * internal/guard/selfguard_test.go replays the same inputs through the port and
 * compares. Neither implementation is asked about itself.
 *
 * The scenarios are chosen for the rules the module's own header calls out —
 * both sides of the target/value split, the fail-closed path, and the
 * case-insensitivity that makes it over-match rather than under-match.
 *
 *   node tools/selfguard-cases.js            # write testdata/selfguard-cases.json
 *   node tools/selfguard-cases.js --check    # fail if it is stale
 */

const fs   = require('node:fs');
const path = require('node:path');

const ROOT = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT  = process.env.SELFGUARD_OUT || path.join(__dirname, '..', 'testdata', 'selfguard-cases.json');

const G = require(path.join(ROOT, 'src', 'routeros', 'selfGuard.js'));
for (const fn of ['resolveSelf', 'checkUser', 'checkGroup', 'checkSession']) {
  if (typeof G[fn] !== 'function') {
    console.error('src/routeros/selfGuard.js no longer exports ' + fn + ' — this generator was ' +
                  'pinning a function that has moved. Find it before regenerating.');
    process.exit(1);
  }
}

// One router's tables. `mikrodash` is us; everything else belongs to somebody
// else. Entirely invented — no router has these accounts.
const USERS = [
  { name: 'admin',     group: 'full' },
  { name: 'mikrodash', group: 'mikrodash-api' },
  { name: 'alice',     group: 'read' },
];
const ACTIVE = [
  { name: 'mikrodash', group: 'mikrodash-api', via: 'api' },
  { name: 'admin',     group: 'full',          via: 'winbox' },
];

const SCENARIOS = [
  { name: 'resolved from /user/active',
    userRows: USERS, activeRows: ACTIVE, usernames: ['mikrodash'] },

  // /user/active is empty — RADIUS, or an API user that cannot read it — so the
  // group has to come from /user instead.
  { name: 'resolved from /user when active is empty',
    userRows: USERS, activeRows: [], usernames: ['mikrodash'] },

  // Both names protected: the live session and whatever routers.json holds.
  { name: 'two usernames, both protected',
    userRows: USERS, activeRows: ACTIVE, usernames: ['mikrodash', 'admin'] },

  // Nothing identifies us — every check must refuse.
  { name: 'unresolvable: our name is on neither table',
    userRows: USERS, activeRows: ACTIVE, usernames: ['nobody'] },
  { name: 'unresolvable: no usernames given at all',
    userRows: USERS, activeRows: ACTIVE, usernames: [] },

  // Case and whitespace: RouterOS names are case-sensitive, the guard is not.
  { name: 'case and whitespace still match',
    userRows: USERS, activeRows: ACTIVE, usernames: ['  MikroDash  '] },
];

// Every check runs against every scenario, so each rule is exercised in the
// resolved AND the unresolved state.
const USER_CHECKS = [
  { kind: 'user', verb: 'set',    target: { name: 'mikrodash' }, values: { comment: 'x' } },
  { kind: 'user', verb: 'remove', target: { name: 'mikrodash' } },
  { kind: 'user', verb: 'set',    target: { name: 'MIKRODASH' }, values: {} },
  { kind: 'user', verb: 'set',    target: { name: 'alice' },     values: { comment: 'fine' } },
  // Value side: renaming somebody INTO our name, or moving them into our group.
  { kind: 'user', verb: 'set',    target: { name: 'alice' },     values: { name: 'mikrodash' } },
  { kind: 'user', verb: 'set',    target: { name: 'alice' },     values: { group: 'mikrodash-api' } },
  { kind: 'user', verb: 'add',    target: null,                  values: { name: 'mikrodash', group: 'read' } },
  { kind: 'user', verb: 'add',    target: null,                  values: { name: 'bob', group: 'mikrodash-api' } },
  { kind: 'user', verb: 'add',    target: null,                  values: { name: 'bob', group: 'read' } },
  // An empty name IS a value being written, and is not the same as no name key.
  { kind: 'user', verb: 'set',    target: { name: 'alice' },     values: { name: '' } },
  { kind: 'user', verb: 'remove', target: null },
];

const GROUP_CHECKS = [
  { kind: 'group', verb: 'set',    target: { name: 'mikrodash-api' }, values: { policy: 'read,api' } },
  { kind: 'group', verb: 'remove', target: { name: 'mikrodash-api' } },
  { kind: 'group', verb: 'set',    target: { name: 'read' },          values: { policy: 'read' } },
  { kind: 'group', verb: 'set',    target: { name: 'read' },          values: { name: 'mikrodash-api' } },
  { kind: 'group', verb: 'add',    target: null,                      values: { name: 'mikrodash-api' } },
  { kind: 'group', verb: 'add',    target: null,                      values: { name: 'newgroup' } },
  { kind: 'group', verb: 'remove', target: null },
];

const SESSION_CHECKS = [
  { kind: 'session', target: { name: 'mikrodash', via: 'api' } },
  { kind: 'session', target: { name: 'mikrodash', via: 'winbox' } },
  { kind: 'session', target: { name: 'admin', via: 'winbox' } },
  { kind: 'session', target: null },
];

function run() {
  return SCENARIOS.map((s) => {
    const self = G.resolveSelf(s.userRows, s.activeRows, s.usernames);
    const checks = [...USER_CHECKS, ...GROUP_CHECKS, ...SESSION_CHECKS].map((c) => {
      let want;
      if (c.kind === 'user')       want = G.checkUser(self, c);
      else if (c.kind === 'group') want = G.checkGroup(self, c);
      else                         want = G.checkSession(self, c);
      return {
        kind: c.kind, verb: c.verb || '',
        target: c.target || null,
        values: c.values || null,
        // The keys the action actually carries. Go cannot tell an absent field
        // from an empty one in a plain map, and the original's checks turn on
        // exactly that distinction (`!== undefined`).
        valueKeys: c.values ? Object.keys(c.values) : [],
        want: {
          ok: !!want.ok,
          code: want.code || '',
          detail: want.detail == null ? '' : String(want.detail),
        },
      };
    });
    return {
      name: s.name,
      userRows: s.userRows, activeRows: s.activeRows, usernames: s.usernames,
      self: {
        names: self.names, groups: self.groups,
        resolved: !!self.resolved, source: self.source || '',
      },
      checks,
    };
  });
}

function main() {
  const check = process.argv.includes('--check');
  const body = JSON.stringify({ cases: run() }, null, 2) + '\n';

  if (check) {
    const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : null;
    if (cur !== body) {
      console.error('selfguard cases are stale — run: node tools/selfguard-cases.js');
      process.exit(1);
    }
    console.log('selfguard cases up to date (' + JSON.parse(body).cases.length + ' scenarios)');
    return;
  }
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, body);
  const parsed = JSON.parse(body);
  const checks = parsed.cases.reduce((n, c) => n + c.checks.length, 0);
  console.log('wrote ' + path.relative(path.join(__dirname, '..'), OUT) +
              ' (' + parsed.cases.length + ' scenarios, ' + checks + ' decisions)');
}

main();
