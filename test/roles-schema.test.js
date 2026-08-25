'use strict';
// Migration v7: roles become rows (issue #108, Phase 2).
//
// The migration's job is to change the representation of a role WITHOUT
// changing what anyone can do. The highest-risk failure is silent: a seeded
// role that grants one page more than its predecessor widens every existing
// grant on upgrade, and a role that grants one page fewer locks people out.
// Both look like a clean migration. So the seed matrices are pinned exactly,
// and the legacy→role_id mapping is exercised against a real v6 database.

const { test } = require('node:test');
const assert   = require('node:assert');
const fs       = require('node:fs');
const os       = require('node:os');
const path     = require('node:path');
const BetterSqlite = require('better-sqlite3');

process.env.DATA_DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'roles-schema-'));
const DB_FILE = path.join(process.env.DATA_DIR, 'mikrodash.db');

const db = require('../src/db');
db.open();

// ── Seeding ──────────────────────────────────────────────────────────────────

test('the three seed roles exist with stable ids', () => {
  // Ids are literals, not UUIDs, so the migration's CASE is deterministic and
  // a downgrade/re-upgrade lands on the same rows.
  const byId = Object.fromEntries(db.listRoles().map(r => [r.id, r]));
  assert.deepStrictEqual(Object.keys(byId).sort(), ['administrator', 'operator', 'readonly']);
  assert.strictEqual(byId.administrator.builtin, 1);
  assert.strictEqual(byId.operator.builtin, 0);
  assert.strictEqual(byId.readonly.builtin, 0);
});

test('Administrator has no page rows — its reach is structural', () => {
  // If admin were table-driven, adding a 15th page later would silently not
  // grant it. builtin=1 is what makes "everything, including things that do not
  // exist yet" true without a data migration.
  assert.deepStrictEqual(db.rolePages('administrator'), []);
});

test('Read Only reproduces today\'s viewer exactly, and grants no reports', () => {
  // The seeding bug this pins: viewer holds router:read and NOTHING else. A
  // reports row confers router:history, so granting it here would hand every
  // existing viewer historical reports and CSV exports on upgrade.
  const rows = db.rolePages('readonly');
  // 'devices' AND 'routers': the Routers page became Devices (#117) and
  // migration 15 copies each row to the new key while LEAVING the old one. The
  // stale row is inert here — the registry has no 'routers' page, so canPage()
  // never looks it up — and it exists so a binary rolled back to before the
  // rename still finds the grant. The pair collapses on its own, because
  // setRolePages() deletes and re-inserts a role's rows on the next edit.
  assert.deepStrictEqual(rows.map(r => r.page).sort(), [
    'bandwidth', 'connections', 'dashboard', 'devices', 'dhcp', 'firewall',
    'interfaces', 'logs', 'routers', 'routing', 'topology', 'vpn', 'wireless',
  ]);
  assert.ok(rows.every(r => r.access === 'read'), 'viewer has no write action today');
  assert.ok(!rows.some(r => r.page === 'reports'), 'reports would confer router:history');
  assert.ok(!rows.some(r => r.page === 'settings'), 'viewer cannot manage settings');
});

test('Operator reproduces today\'s operator exactly', () => {
  // operator holds router:read, ack, history, diagnose. Those map to: reports
  // read (history), dashboard write (ack), firewall write (diagnose) — and
  // notably NOT routers write, which would be router:manage.
  const access = Object.fromEntries(db.rolePages('operator').map(r => [r.page, r.access]));
  assert.strictEqual(access.reports,   'read',  'router:history');
  assert.strictEqual(access.dashboard, 'write', 'router:ack');
  assert.strictEqual(access.firewall,  'write', 'router:diagnose');
  assert.strictEqual(access.routers,   'read',  'operator has no router:manage');
  assert.strictEqual(access.settings,  undefined, 'operator cannot manage settings');
  const writes = Object.entries(access).filter(([, a]) => a === 'write').map(([p]) => p).sort();
  assert.deepStrictEqual(writes, ['dashboard', 'firewall']);
});

// ── Constraints ──────────────────────────────────────────────────────────────

test('a role still referenced by a grant cannot be deleted', () => {
  // ON DELETE RESTRICT, so this holds even if a future route forgets to check.
  db.upsertGrant({ principalType: 'user', principalId: 'u-restrict', roleId: 'operator', scopeType: 'global' });
  assert.strictEqual(db.countGrantsForRole('operator'), 1);
  assert.throws(() => db.deleteRole('operator'), /FOREIGN KEY/i);

  for (const g of db.listGrants({ principalId: 'u-restrict' })) db.deleteGrant(g.id);
  assert.strictEqual(db.countGrantsForRole('operator'), 0);
});

test('the builtin role cannot be deleted even with no grants', () => {
  assert.strictEqual(db.countGrantsForRole('administrator'), 0);
  assert.strictEqual(db.deleteRole('administrator'), false);
  assert.ok(db.getRole('administrator'), 'still there');
});

test('deleting a role cascades its page rows', () => {
  const r = db.createRole({ name: 'Cascade Me' });
  db.setRolePages(r.id, [{ page: 'logs', access: 'read' }]);
  assert.strictEqual(db.rolePages(r.id).length, 1);
  assert.strictEqual(db.deleteRole(r.id), true);
  assert.deepStrictEqual(db.rolePages(r.id), []);
});

test('access is read or write — never a third spelling of "no access"', () => {
  const r = db.createRole({ name: 'Check Access' });
  // The engine refuses it...
  const raw = new BetterSqlite(DB_FILE);
  assert.throws(
    () => raw.prepare('INSERT INTO role_pages (role_id,page,access) VALUES (?,?,?)').run(r.id, 'logs', 'none'),
    /CHECK constraint failed/i);
  raw.close();
  // ...and setRolePages drops it rather than storing something unreadable.
  db.setRolePages(r.id, [{ page: 'logs', access: 'none' }, { page: 'vpn', access: 'read' }]);
  assert.deepStrictEqual(db.rolePages(r.id), [{ page: 'vpn', access: 'read' }]);
  db.deleteRole(r.id);
});

test('setRolePages replaces the whole matrix rather than merging', () => {
  const r = db.createRole({ name: 'Replace Me' });
  db.setRolePages(r.id, [{ page: 'logs', access: 'read' }, { page: 'vpn', access: 'write' }]);
  db.setRolePages(r.id, [{ page: 'dhcp', access: 'read' }]);
  assert.deepStrictEqual(db.rolePages(r.id), [{ page: 'dhcp', access: 'read' }]);
  db.deleteRole(r.id);
});

test('role names are unique, case-insensitively', () => {
  const r = db.createRole({ name: 'Night Shift' });
  assert.throws(() => db.createRole({ name: 'night shift' }), /UNIQUE/i);
  db.deleteRole(r.id);
});

test('updateRole cannot promote a custom role to builtin', () => {
  const r = db.createRole({ name: 'Wannabe Admin' });
  db.updateRole(r.id, { name: 'Renamed', description: 'x', builtin: 1, id: 'administrator' });
  const after = db.getRole(r.id);
  assert.strictEqual(after.builtin, 0, 'builtin is not a mutable field');
  assert.strictEqual(after.id, r.id, 'id is not a mutable field');
  assert.strictEqual(after.name, 'Renamed');
  db.deleteRole(r.id);
});

// ── Grants bridge ────────────────────────────────────────────────────────────

test('upsertGrant accepts a role id and keeps the legacy mirror consistent', () => {
  const g = db.upsertGrant({ principalType: 'user', principalId: 'u1', roleId: 'administrator', scopeType: 'global' });
  assert.strictEqual(g.role_id, 'administrator');
  assert.strictEqual(g.role, 'admin', 'mirror for a downgraded binary');
  db.deleteGrant(g.id);
});

test('upsertGrant still accepts the legacy role name', () => {
  // Phase 2 changes no caller: rbac.syncUserGrants and the grants routes keep
  // passing `role`, and the id is derived.
  for (const [legacy, id] of [['admin', 'administrator'], ['operator', 'operator'], ['viewer', 'readonly']]) {
    const g = db.upsertGrant({ principalType: 'user', principalId: 'u-' + legacy, role: legacy, scopeType: 'global' });
    assert.strictEqual(g.role_id, id, legacy);
    assert.strictEqual(g.role, legacy);
    db.deleteGrant(g.id);
  }
});

test('a custom role mirrors as the least privilege, never as admin', () => {
  // A v6 binary reading this row must not conclude the holder is an admin.
  const r = db.createRole({ name: 'Custom Mirror' });
  const g = db.upsertGrant({ principalType: 'user', principalId: 'u2', roleId: r.id, scopeType: 'global' });
  assert.strictEqual(g.role_id, r.id);
  assert.strictEqual(g.role, 'viewer');
  db.deleteGrant(g.id);
  db.deleteRole(r.id);
});

test('upserting the same scope replaces the role instead of stacking', () => {
  const a = db.upsertGrant({ principalType: 'user', principalId: 'u3', roleId: 'readonly', scopeType: 'global' });
  const b = db.upsertGrant({ principalType: 'user', principalId: 'u3', roleId: 'operator', scopeType: 'global' });
  assert.strictEqual(a.id, b.id, 'same row');
  assert.strictEqual(b.role_id, 'operator');
  assert.strictEqual(db.listGrants({ principalId: 'u3' }).length, 1);
  db.deleteGrant(b.id);
});

test('the rebuilt table kept the global/scope_id coupling', () => {
  // A constraint carried across from v5 — a global grant must have an empty
  // scope_id, or UNIQUE could be bypassed.
  const raw = new BetterSqlite(DB_FILE);
  assert.throws(
    () => raw.prepare(`INSERT INTO grants (id,principal_type,principal_id,role_id,role,scope_type,scope_id,created_at)
                       VALUES ('x','user','u4','readonly','viewer','global','not-empty',1)`).run(),
    /CHECK constraint failed/i);
  raw.close();
});

test('globalAdminUserIds is defined by builtin, not by a role name', () => {
  // Renaming Administrator must not change who counts as one.
  const g = db.upsertGrant({ principalType: 'user', principalId: 'boss', roleId: 'administrator', scopeType: 'global' });
  db.updateRole('administrator', { name: 'Supreme Leader' });
  assert.deepStrictEqual(db.globalAdminUserIds(), ['boss']);
  db.updateRole('administrator', { name: 'Administrator' });
  db.deleteGrant(g.id);
});

// ── The upgrade path, against a real v6 database ─────────────────────────────

test('a v6 database upgrades, mapping every legacy role and keeping the mirror', () => {
  // Downgrade the live file to v6 shape, then let db.open() run v7 for real —
  // exercising the migration rather than a re-implementation of it.
  db.close();
  const raw = new BetterSqlite(DB_FILE);
  raw.exec(`
    DROP TABLE grants;
    DROP TABLE role_pages;
    DROP TABLE roles;
    CREATE TABLE grants (
      id             TEXT PRIMARY KEY,
      principal_type TEXT NOT NULL CHECK (principal_type IN ('user','group')),
      principal_id   TEXT NOT NULL,
      role           TEXT NOT NULL CHECK (role IN ('viewer','operator','admin')),
      scope_type     TEXT NOT NULL CHECK (scope_type IN ('global','site','router')),
      scope_id       TEXT NOT NULL DEFAULT '',
      created_at     INTEGER NOT NULL,
      created_by     TEXT,
      CHECK ((scope_type =  'global' AND scope_id =  '')
          OR (scope_type <> 'global' AND scope_id <> '')),
      UNIQUE (principal_type, principal_id, scope_type, scope_id)
    );
    INSERT INTO grants VALUES
      ('g1','user', 'alice','admin',   'global','',       100, NULL),
      ('g2','user', 'bob',  'operator','site',  'berlin', 101, NULL),
      ('g3','user', 'carol','viewer',  'router','rtr-1',  102, NULL),
      ('g4','group','noc',  'operator','global','',       103, 'alice');
    -- Both, not just v7: a real v6 database has neither row, and leaving v8
    -- marked applied would rebuild the table without its role_id default.
    DELETE FROM schema_version WHERE version IN (7, 8);
  `);
  raw.close();

  db.open(); // runs v7 only

  const byId = Object.fromEntries(db.listGrants().map(g => [g.id, g]));
  assert.strictEqual(byId.g1.role_id, 'administrator');
  assert.strictEqual(byId.g2.role_id, 'operator');
  assert.strictEqual(byId.g3.role_id, 'readonly');
  assert.strictEqual(byId.g4.role_id, 'operator');

  // Legacy strings survive untouched for a downgraded binary.
  assert.deepStrictEqual(
    ['g1', 'g2', 'g3', 'g4'].map(k => byId[k].role),
    ['admin', 'operator', 'viewer', 'operator']);

  // Scope is carried across verbatim — the migration must not touch it.
  assert.strictEqual(byId.g2.scope_type, 'site');
  assert.strictEqual(byId.g2.scope_id, 'berlin');
  assert.strictEqual(byId.g3.scope_id, 'rtr-1');
  assert.strictEqual(byId.g4.principal_type, 'group');
  assert.strictEqual(byId.g4.created_by, 'alice');

  // And the seeds came back with it.
  assert.strictEqual(db.listRoles().length, 3);
  assert.strictEqual(db.rolePages('readonly').length, 12);

  // Group-held admin is still invisible here (noc holds operator), and alice's
  // direct grant is found — the guard that stops the last admin being orphaned.
  assert.deepStrictEqual(db.globalAdminUserIds(), ['alice']);
});

test('a rolled-back binary can still write grants', () => {
  // v7 left role_id NOT NULL with no default, so a v6 binary — which knows
  // nothing about the column — got "NOT NULL constraint failed" on every grant
  // write. It could log in and then not manage a single user. v8 gives the
  // column a default so that write lands on least privilege instead.
  const raw = new BetterSqlite(DB_FILE);
  raw.prepare(`INSERT INTO grants (id, principal_type, principal_id, role, scope_type, scope_id, created_at)
               VALUES ('legacy-write', 'user', 'u-rollback', 'admin', 'global', '', 1)`).run();
  raw.close();

  const g = db.listGrants({ principalId: 'u-rollback' })[0];
  assert.ok(g, 'the insert succeeded');
  assert.strictEqual(g.role_id, 'readonly',
    'least privilege, so a downgrade cannot silently widen anyone');
  assert.strictEqual(g.role, 'admin', 'the legacy mirror carries what the old binary meant');
  db.deleteGrant(g.id);
});

test('the v8 rebuild kept every constraint from v7', () => {
  const raw = new BetterSqlite(DB_FILE);
  // The global/scope_id coupling...
  assert.throws(() => raw.prepare(`INSERT INTO grants (id,principal_type,principal_id,role_id,scope_type,scope_id,created_at)
                                   VALUES ('c1','user','u','readonly','global','not-empty',1)`).run(),
    /CHECK constraint failed/i);
  // ...the principal vocabulary...
  assert.throws(() => raw.prepare(`INSERT INTO grants (id,principal_type,principal_id,role_id,scope_type,scope_id,created_at)
                                   VALUES ('c2','robot','u','readonly','global','',1)`).run(),
    /CHECK constraint failed/i);
  // ...and the foreign key to roles.
  assert.throws(() => raw.prepare(`INSERT INTO grants (id,principal_type,principal_id,role_id,scope_type,scope_id,created_at)
                                   VALUES ('c3','user','u','no-such-role','global','',1)`).run(),
    /FOREIGN KEY/i);
  raw.close();

  // And RESTRICT still protects a role that is in use.
  const g = db.upsertGrant({ principalType: 'user', principalId: 'u-v8', roleId: 'operator', scopeType: 'global' });
  assert.throws(() => db.deleteRole('operator'), /FOREIGN KEY/i);
  db.deleteGrant(g.id);
});

test('re-running the migration is a no-op', () => {
  // Version-guarded, so a second open() must not double-seed or re-map.
  const before = { roles: db.listRoles().length, grants: db.listGrants().length };
  db.close();
  db.open();
  assert.strictEqual(db.listRoles().length, before.roles);
  assert.strictEqual(db.listGrants().length, before.grants);
  assert.strictEqual(db.rolePages('operator').length, 13);
});
