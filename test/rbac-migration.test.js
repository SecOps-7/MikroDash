// Migration from the legacy role + allowedRouterIds model (issue #78).
//
// This is the highest-risk part of the change, because the semantics INVERT.
// `allowedRouterIds: []` — and an absent field — used to mean UNRESTRICTED, i.e.
// every router. The grant model is deny-by-default. Read [] as "no routers" and
// every existing user silently loses everything, which is a data-shaped outage
// rather than a crash: nothing throws, people just cannot see their network.
//
// The three tests marked * are the ones that pin that inversion.

const { test } = require('node:test');
const assert   = require('node:assert');
const fs       = require('node:fs');
const os       = require('node:os');
const path     = require('node:path');

const DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'rbac-mig-'));
process.env.DATA_DIR = DIR;

const db      = require('../src/db');
const Routers = require('../src/routers');
const rbac    = require('../src/rbac');
const Users   = require('../src/users');

db.open();
rbac.init({ isModern: () => true });

const rA = Routers.add({ label: 'A', host: '10.0.0.1' });
const rB = Routers.add({ label: 'B', host: '10.0.0.2' });

// Write users.json directly: createUser now refuses unknown roles, and one case
// under test is precisely a record carrying a role that is no longer accepted.
// A legacy file can contain one; the migration has to cope.
function seedUsers(users) {
  fs.writeFileSync(path.join(DIR, 'users.json'), JSON.stringify(users, null, 2));
  Users.invalidateCache();
  for (const g of db.listGrants()) db.deleteGrant(g.id);
  rbac.bump();
}

const user = (over) => Object.assign({
  id: 'u-' + Math.random().toString(36).slice(2, 10),
  username: 'someone',
  passwordHash: 'x', salt: 'y',
  role: 'viewer',
  allowedRouterIds: [],
  createdAt: 1755000000000,
}, over);

const grantsFor = (id) => db.listGrants({ principalId: id })
  .map(g => g.role + '@' + g.scope_type + (g.scope_id ? ':' + g.scope_id : ''))
  .sort();

test('* an admin with an EMPTY allowedRouterIds becomes a GLOBAL admin', () => {
  // [] meant "all routers". Mapping it to no grants would lock the only
  // administrator out of their own install.
  const u = user({ role: 'admin', allowedRouterIds: [] });
  seedUsers([u]);
  rbac.migrateFromLegacy();
  assert.deepEqual(grantsFor(u.id), ['admin@global']);
  assert.equal(rbac.can({ userId: u.id }, 'router:read', rA.id), true);
  assert.equal(rbac.can({ userId: u.id }, 'system:principals'), true);
});

test('* a viewer with an EMPTY allowedRouterIds becomes a GLOBAL viewer', () => {
  const u = user({ role: 'viewer', allowedRouterIds: [] });
  seedUsers([u]);
  rbac.migrateFromLegacy();
  assert.deepEqual(grantsFor(u.id), ['viewer@global']);
  assert.equal(rbac.can({ userId: u.id }, 'router:read', rB.id), true);
});

test('* an ABSENT allowedRouterIds behaves identically to an empty one', () => {
  // Records written before the field existed simply do not have it. Every
  // enforcement site treats absent and [] the same today; so must this.
  const u = user({ role: 'admin' });
  delete u.allowedRouterIds;
  seedUsers([u]);
  rbac.migrateFromLegacy();
  assert.deepEqual(grantsFor(u.id), ['admin@global']);
});

test('a restricted user gets router-scoped grants and nothing else', () => {
  const u = user({ role: 'viewer', allowedRouterIds: [rA.id] });
  seedUsers([u]);
  rbac.migrateFromLegacy();
  assert.deepEqual(grantsFor(u.id), ['viewer@router:' + rA.id]);
  assert.equal(rbac.can({ userId: u.id }, 'router:read', rA.id), true);
  assert.equal(rbac.can({ userId: u.id }, 'router:read', rB.id), false,
    'a restriction must survive the migration, not be widened');
});

test('an unrecognised role migrates as viewer, not admin', () => {
  // createUser used to coerce anything non-'viewer' to 'admin', so such records
  // exist in the wild. Every runtime check was `role !== 'admin'`, so they
  // BEHAVED as viewers — migrating them as admin would be a privilege
  // escalation performed by an upgrade.
  const u = user({ role: 'superuser', allowedRouterIds: [] });
  seedUsers([u]);
  rbac.migrateFromLegacy();
  assert.deepEqual(grantsFor(u.id), ['viewer@global']);
});

test('a router id that no longer exists is dropped', () => {
  // Deliberately a viewer. A restricted ADMIN would also trip the zero-lockout
  // guard below and pick up a global grant, which would muddle what this test
  // is measuring.
  const u = user({ role: 'viewer', allowedRouterIds: [rA.id, 'ghost-router'] });
  seedUsers([u]);
  rbac.migrateFromLegacy();
  assert.deepEqual(grantsFor(u.id), ['viewer@router:' + rA.id]);
});

test('migrating a non-empty user list always leaves a global administrator', () => {
  // The zero-lockout guard. A restricted admin can create users today because
  // _requireAdmin has no router scoping; under the new model they cannot. An
  // install whose only admins were restricted would end up with nobody able to
  // administer anything, so they are promoted rather than stranded.
  const u = user({ role: 'admin', allowedRouterIds: [rA.id] });
  seedUsers([u]);
  rbac.migrateFromLegacy();
  assert.deepEqual(db.globalAdminUserIds(), [u.id],
    'the only administrator must still be able to administer');
  assert.equal(rbac.can({ userId: u.id }, 'system:principals'), true);
});

test('a viewer-only install is not handed an administrator it never had', () => {
  const u = user({ role: 'viewer', allowedRouterIds: [rA.id] });
  seedUsers([u]);
  rbac.migrateFromLegacy();
  assert.deepEqual(db.globalAdminUserIds(), [],
    'the guard promotes existing admins, it does not invent one');
});

test('migration is idempotent', () => {
  const u = user({ role: 'admin', allowedRouterIds: [] });
  seedUsers([u]);
  const first = rbac.migrateFromLegacy();
  const after = db.listGrants().map(g => g.principal_id + '|' + g.role + '|' + g.scope_type).sort();

  const second = rbac.migrateFromLegacy();
  assert.equal(first.skipped, false);
  assert.equal(second.skipped, true, 'a second run must be a no-op');
  assert.deepEqual(
    db.listGrants().map(g => g.principal_id + '|' + g.role + '|' + g.scope_type).sort(),
    after, 're-running must not duplicate or alter grants');
});

test('an empty user list produces no grants and does not throw', () => {
  seedUsers([]);
  const r = rbac.migrateFromLegacy();
  assert.equal(r.migrated, 0);
  assert.equal(db.listGrants().length, 0);
});

test('several users migrate independently', () => {
  const admin  = user({ username: 'admin',  role: 'admin',  allowedRouterIds: [] });
  const scoped = user({ username: 'scoped', role: 'viewer', allowedRouterIds: [rB.id] });
  seedUsers([admin, scoped]);
  rbac.migrateFromLegacy();

  assert.deepEqual(grantsFor(admin.id),  ['admin@global']);
  assert.deepEqual(grantsFor(scoped.id), ['viewer@router:' + rB.id]);
  assert.equal(rbac.can({ userId: scoped.id }, 'router:read', rA.id), false);
  assert.equal(rbac.can({ userId: scoped.id }, 'system:principals'), false);
});

test('the migration does not rewrite users.json', () => {
  // users.json keeps its shape so that rolling back to an older image still
  // authenticates. If it were rewritten into something an old binary could not
  // parse, that binary would see zero users and re-open the unauthenticated
  // first-run setup route.
  const u = user({ role: 'admin', allowedRouterIds: [] });
  seedUsers([u]);
  const before = fs.readFileSync(path.join(DIR, 'users.json'), 'utf8');
  rbac.migrateFromLegacy();
  assert.equal(fs.readFileSync(path.join(DIR, 'users.json'), 'utf8'), before,
    'users.json must be left byte-identical');
  assert.ok(Array.isArray(JSON.parse(before)), 'and must remain a bare array');
});
