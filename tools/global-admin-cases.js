'use strict';
/**
 * `globalAdminUserIds()` — who still has administrator access.
 *
 * ── THIS IS THE QUERY BEHIND "THAT WOULD LEAVE NOBODY WITH ADMIN ACCESS" ────
 *
 * `Rbac.wouldOrphanGlobalAdmin` runs a proposed change inside a transaction,
 * asks this, and rolls back. Five different routes consult it — deleting a user,
 * a group, a grant, a role, and emptying a group's membership — and the live
 * comment calls the last "the least obvious" of them.
 *
 * Getting it WRONG IN EITHER DIRECTION is bad in a different way. Too few
 * admins and the app refuses a legitimate change, forever, with no way round it
 * short of editing the database. Too many and it hands out the last
 * administrator's access and locks everyone out.
 *
 * ── THE LIVE SQL IS LIFTED AND RUN, NOT RETYPED ─────────────────────────────
 *
 * `src/db.js` needs better-sqlite3, so the query TEXT is sliced out of
 * `globalAdminUserIds` and executed against a real SQLite built by REPLAYING the
 * live migrations — the same technique `tools/schema-audit.js` uses, on
 * `node:sqlite`, which is built in since Node 22. So the expected answers come
 * from the original query against the original schema.
 *
 * ── NOTHING HERE IS REAL ────────────────────────────────────────────────────
 *
 * Synthetic user, group and role ids.
 *
 *   node tools/global-admin-cases.js            write the corpus
 *   node tools/global-admin-cases.js --check    exit 1 if stale
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { DatabaseSync } = require('node:sqlite');

const ROOT = path.join(__dirname, '..');
const OUT = process.env.GLOBAL_ADMIN_OUT
  || path.join(ROOT, 'testdata', 'global-admin-cases.json');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));

const src = fs.readFileSync(path.join(LIVE, 'src', 'db.js'), 'utf8');

// ---- lift the query ------------------------------------------------------
const FN = 'function globalAdminUserIds() {';
assert.equal(src.split(FN).length - 1, 1, 'the globalAdminUserIds anchor is ambiguous');
const fnFrom = src.indexOf(FN);
const sqlOpen = src.indexOf('`', fnFrom);
const sqlClose = src.indexOf('`', sqlOpen + 1);
assert.ok(sqlOpen > 0 && sqlClose > sqlOpen, 'the query is not where its anchors say');
const QUERY = src.slice(sqlOpen + 1, sqlClose);
for (const marker of ['principal_type', 'group_members', 'builtin = 1', 'UNION', 'DISTINCT']) {
  assert.ok(QUERY.includes(marker),
    'the lifted query has no ' + marker + ' -- the slice is short, and this corpus would then '
    + 'record the answers of a simpler question than the app asks');
}

// ---- replay the migrations ------------------------------------------------
const MOPEN = 'const MIGRATIONS = [';
const mFrom = src.indexOf(MOPEN);
const mBody = src.slice(mFrom, src.indexOf('\n];', mFrom) + 3);
const ctx = { module: { exports: {} }, JSON, Date, String, Number, Math, Object, Array };
vm.createContext(ctx);
vm.runInContext(mBody + '\nmodule.exports = MIGRATIONS;', ctx);
const migrations = ctx.module.exports;
assert.ok(migrations.length >= 12, 'only ' + migrations.length + ' migrations were lifted');

function freshDB() {
  const db = new DatabaseSync(':memory:');
  for (const m of migrations) {
    m.up({
      exec: (s) => db.exec(s),
      prepare: (s) => {
        const st = db.prepare(s);
        return { run: (...a) => st.run(...a), get: (...a) => st.get(...a), all: (...a) => st.all(...a) };
      },
    });
  }
  return db;
}

// The migrations seed the builtin roles; confirm rather than assume, since every
// case below turns on `builtin = 1`.
{
  const probe = freshDB();
  const builtin = probe.prepare('SELECT id FROM roles WHERE builtin = 1').all().map((r) => r.id);
  assert.ok(builtin.length > 0,
    'no builtin roles exist after the migrations, so every case would answer "nobody" and '
    + 'this corpus would be uniformly empty');
  assert.ok(builtin.includes('administrator'),
    'the builtin roles are ' + builtin.join(', ') + ' -- this corpus names "administrator" '
    + 'explicitly, so a rename needs reading rather than patching');
  // MEASURED, NOT ASSUMED: exactly one role is builtin. The corpus below turns
  // on that -- a seeded `readonly` role exists and is NOT builtin, which is why
  // holding it globally does not count as administrator access.
  assert.deepEqual(builtin, ['administrator'],
    'the builtin set is now ' + builtin.join(', ') + '; the readonly case below was written '
    + 'when administrator was the only one, and needs rereading');
}

let grantSeq = 0;
function seed(db, { grants = [], members = [], roles = [] }) {
  for (const r of roles) {
    db.prepare('INSERT INTO roles (id, name, builtin, created_at) VALUES (?, ?, ?, 0)')
      .run(r.id, r.name, r.builtin ? 1 : 0);
  }
  for (const g of grants) {
    if (g.type === 'group') {
      db.prepare('INSERT OR IGNORE INTO groups (id, name, created_at) VALUES (?, ?, 0)')
        .run(g.id, 'Group ' + g.id);
    }
    db.prepare(`INSERT INTO grants
        (id, principal_type, principal_id, role_id, role, scope_type, scope_id, created_at)
      VALUES (?, ?, ?, ?, NULL, ?, ?, 0)`)
      .run('grant-' + (++grantSeq), g.type, g.id, g.role, g.scope, g.scopeId || '');
  }
  // THE GROUP ROWS THEMSELVES. `group_members.group_id` has a foreign key to
  // `groups`, and the migrations enable them — so a membership cannot be seeded
  // for a group that does not exist. Derived from the specs rather than listed,
  // because a case naming a group in one place and not the other would be a
  // fixture bug that looks like a query bug.
  const groupIds = new Set([
    ...grants.filter((g) => g.type === 'group').map((g) => g.id),
    ...members.map((m) => m.group),
  ]);
  for (const id of groupIds) {
    db.prepare('INSERT OR IGNORE INTO groups (id, name, created_at) VALUES (?, ?, 0)')
      .run(id, 'Group ' + id);
  }
  for (const m of members) {
    db.prepare('INSERT INTO group_members (group_id, user_id) VALUES (?, ?)')
      .run(m.group, m.user);
  }
}

// ---- the cases -----------------------------------------------------------
const CASES = {
  // Nobody at all.
  emptyInstall: {},

  // The plain case: a user holding a builtin role globally.
  aDirectGlobalAdmin: {
    grants: [{ type: 'user', id: 'u-1', role: 'administrator', scope: 'global' }],
  },

  // ── THE THREE THAT LOOK LIKE ADMINS AND ARE NOT ────────────────────────
  //
  // A CUSTOM role held globally. It may even confer every permission today, but
  // `builtin = 1` is what the query asks — a custom role can be edited to
  // confer nothing, so counting it would let the last real admin be removed.
  aGlobalCustomRoleIsNotAdmin: {
    roles: [{ id: 'nearly', name: 'Nearly', builtin: false }],
    grants: [{ type: 'user', id: 'u-1', role: 'nearly', scope: 'global' }],
  },
  // A BUILTIN role scoped to one router. Administrator of a router is not an
  // administrator of the app.
  aRouterScopedBuiltinIsNotAdmin: {
    grants: [{ type: 'user', id: 'u-1', role: 'administrator', scope: 'router', scopeId: 'r1' }],
  },
  // ...and to a site.
  aSiteScopedBuiltinIsNotAdmin: {
    grants: [{ type: 'user', id: 'u-1', role: 'administrator', scope: 'site', scopeId: 's1' }],
  },

  // ── THROUGH A GROUP, which is the half that is easy to miss ────────────
  aGroupGlobalAdminCountsItsMembers: {
    grants: [{ type: 'group', id: 'g-1', role: 'administrator', scope: 'global' }],
    members: [{ group: 'g-1', user: 'u-1' }, { group: 'g-1', user: 'u-2' }],
  },
  // A group with the grant and NO members leaves nobody.
  anEmptyAdminGroupIsNobody: {
    grants: [{ type: 'group', id: 'g-1', role: 'administrator', scope: 'global' }],
  },
  // Membership of a group that has no global grant confers nothing.
  aMemberOfANonAdminGroup: {
    grants: [{ type: 'group', id: 'g-1', role: 'administrator', scope: 'router', scopeId: 'r1' }],
    members: [{ group: 'g-1', user: 'u-1' }],
  },

  // DISTINCT: both routes to the same person is still one person. Without this
  // a port using UNION ALL passes every other case.
  bothRoutesCountOnce: {
    grants: [
      { type: 'user', id: 'u-1', role: 'administrator', scope: 'global' },
      { type: 'group', id: 'g-1', role: 'administrator', scope: 'global' },
    ],
    members: [{ group: 'g-1', user: 'u-1' }],
  },

  // A GLOBAL `readonly` GRANT IS NOT ADMINISTRATOR ACCESS — and the reason is
  // not that readonly confers little. It is that `readonly` IS NOT BUILTIN:
  // exactly one role is (`administrator`), asserted above. The query asks
  // "does anybody hold a BUILTIN role globally", so this turns entirely on the
  // flag and not on what the role can do.
  aGlobalReadonlyIsNotAdmin: {
    grants: [{ type: 'user', id: 'u-1', role: 'readonly', scope: 'global' }],
  },

  // A realistic mix, so the ordering and the set are both exercised.
  aMixedInstall: {
    roles: [{ id: 'custom', name: 'Custom', builtin: false }],
    grants: [
      { type: 'user', id: 'u-1', role: 'administrator', scope: 'global' },
      { type: 'user', id: 'u-2', role: 'custom', scope: 'global' },
      { type: 'user', id: 'u-3', role: 'administrator', scope: 'router', scopeId: 'r1' },
      { type: 'group', id: 'g-1', role: 'administrator', scope: 'global' },
    ],
    members: [{ group: 'g-1', user: 'u-4' }, { group: 'g-1', user: 'u-1' }],
  },
};

const cases = {};
for (const [name, spec] of Object.entries(CASES)) {
  const db = freshDB();
  seed(db, spec);
  const ids = db.prepare(QUERY).all().map((r) => r.uid);
  cases[name] = { ...spec, adminIds: ids.slice().sort() };
}

// ---- believability -------------------------------------------------------
{
  const a = (k) => cases[k].adminIds;
  assert.deepEqual(a('emptyInstall'), []);
  assert.deepEqual(a('aDirectGlobalAdmin'), ['u-1']);
  assert.deepEqual(a('aGlobalCustomRoleIsNotAdmin'), [],
    'a CUSTOM role held globally counted as administrator -- a role that can be edited to '
    + 'confer nothing would then keep the last real admin removable');
  assert.deepEqual(a('aRouterScopedBuiltinIsNotAdmin'), [],
    'administrator OF A ROUTER counted as administrator of the app');
  assert.deepEqual(a('aSiteScopedBuiltinIsNotAdmin'), []);
  assert.deepEqual(a('aGroupGlobalAdminCountsItsMembers'), ['u-1', 'u-2']);
  assert.deepEqual(a('anEmptyAdminGroupIsNobody'), [],
    'an EMPTY group with a global admin grant produced administrators, which is how the '
    + 'last one gets removed while the check says somebody is left');
  assert.deepEqual(a('aMemberOfANonAdminGroup'), []);
  assert.deepEqual(a('bothRoutesCountOnce'), ['u-1'],
    'the same person counted twice -- a port using UNION ALL passes every other case here');
  assert.deepEqual(a('aGlobalReadonlyIsNotAdmin'), [],
    'a global readonly grant counted as administrator access -- readonly is not a BUILTIN '
    + 'role, and the query turns on that flag');
  assert.deepEqual(a('aMixedInstall'), ['u-1', 'u-4']);

  // The corpus must contain both empty and non-empty answers, or a port
  // returning a constant would satisfy it.
  const all = Object.values(cases).map((c) => c.adminIds);
  assert.ok(all.some((x) => x.length === 0) && all.some((x) => x.length > 0),
    'every case answers the same way, so this corpus cannot tell a working query from a '
    + 'constant');
}

const json = JSON.stringify({
  note: 'Generated by tools/global-admin-cases.js by RUNNING the live SQL against the live '
    + 'schema. Do not edit.',
  query: QUERY.trim(),
  cases,
}, null, 2) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== json) {
    console.error('global-admin-cases.json is STALE — re-run without --check');
    process.exit(1);
  }
  console.log('global-admin-cases.json is current (' + Object.keys(cases).length + ' cases)');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, json);
  console.log('wrote ' + OUT + ' (' + Object.keys(cases).length + ' cases)');
}
