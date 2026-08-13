// The authorization truth table (issue #78).
//
// can() is the one place every permission decision is made, so its edges are
// worth pinning individually. The two that matter most are the ones that would
// fail silently AND permissively: a scoped grant must never satisfy a
// system-administration permission, and a missing target must never mean "yes".

const { test } = require('node:test');
const assert   = require('node:assert');
const fs       = require('node:fs');
const os       = require('node:os');
const path     = require('node:path');

process.env.DATA_DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'rbac-model-'));

const db      = require('../src/db');
const Routers = require('../src/routers');
const rbac    = require('../src/rbac');

db.open();
let _modern = true;
rbac.init({ isModern: () => _modern });

// Two routers: one in a site, one with no site at all. The site-less case is
// what catches a rule accidentally written as "everything inherits from a site".
const rIn  = Routers.add({ label: 'In Site', host: '10.0.0.1' });
const rOut = Routers.add({ label: 'No Site', host: '10.0.0.2' });
const SITE = 'site-berlin';
Routers.update(rIn.id, { siteId: SITE });

const sess = (userId) => ({ userId });

function reset() {
  for (const g of db.listGrants()) db.deleteGrant(g.id);
  for (const gr of db.listGroups()) db.deleteGroup(gr.id);
  rbac.bump();
}

function grant(principalId, role, scopeType, scopeId = '', principalType = 'user') {
  db.upsertGrant({ principalType, principalId, role, scopeType, scopeId });
  rbac.bump();
}

test('no grants means no access at all', () => {
  reset();
  assert.equal(rbac.can(sess('u1'), 'router:read', rIn.id), false);
  assert.equal(rbac.can(sess('u1'), 'system:principals'), false);
  assert.deepEqual(rbac.effectiveRouterIds(sess('u1')), []);
});

test('a global grant reaches every router, including site-less ones', () => {
  reset();
  grant('u1', 'viewer', 'global');
  assert.equal(rbac.can(sess('u1'), 'router:read', rIn.id), true);
  assert.equal(rbac.can(sess('u1'), 'router:read', rOut.id), true);
  assert.deepEqual(rbac.effectiveRouterIds(sess('u1')).sort(), [rIn.id, rOut.id].sort());
});

test("a site grant reaches only that site's routers", () => {
  reset();
  grant('u1', 'operator', 'site', SITE);
  assert.equal(rbac.can(sess('u1'), 'router:read', rIn.id), true);
  assert.equal(rbac.can(sess('u1'), 'router:read', rOut.id), false,
    'a site-less router must not be reachable through a site grant');
});

test('a router grant confers nothing site-wide', () => {
  // Otherwise granting someone one router in Berlin would quietly hand them
  // every other router in Berlin too.
  reset();
  grant('u1', 'admin', 'router', rIn.id);
  assert.equal(rbac.can(sess('u1'), 'router:manage', rIn.id), true);
  assert.equal(rbac.can(sess('u1'), 'router:read', { type: 'site', id: SITE }), false);
});

test('SYSTEM permissions are unreachable from any scoped grant', () => {
  // The security boundary the whole design rests on: without it, an admin over
  // one site could edit grants and promote themselves to global.
  reset();
  grant('u1', 'admin', 'site', SITE);
  grant('u2', 'admin', 'router', rIn.id);
  for (const perm of rbac.GLOBAL_ONLY) {
    assert.equal(rbac.can(sess('u1'), perm), false, `site admin must not hold ${perm}`);
    assert.equal(rbac.can(sess('u2'), perm), false, `router admin must not hold ${perm}`);
    // Passing a target must not open a side door either.
    assert.equal(rbac.can(sess('u1'), perm, rIn.id), false, `${perm} must ignore a target`);
  }
  reset();
  grant('u1', 'admin', 'global');
  for (const perm of rbac.GLOBAL_ONLY) {
    assert.equal(rbac.can(sess('u1'), perm), true, `global admin must hold ${perm}`);
  }
});

test('a scoped permission with no target is refused', () => {
  // The old model's "no restriction recorded means everything" fallthrough is
  // exactly this bug. Forgetting the id must deny, not allow.
  reset();
  grant('u1', 'admin', 'global');
  for (const missing of [undefined, null, '']) {
    assert.equal(rbac.can(sess('u1'), 'router:read', missing), false);
  }
});

test('roles carry the permissions they should, and no more', () => {
  reset();
  grant('viewer1',   'viewer',   'global');
  grant('operator1', 'operator', 'global');
  grant('admin1',    'admin',    'global');

  const has = (u, p) => rbac.can(sess(u), p, rIn.id);

  assert.equal(has('viewer1', 'router:read'), true);
  for (const p of ['router:ack', 'router:history', 'router:diagnose', 'router:manage', 'router:secrets']) {
    assert.equal(has('viewer1', p), false, `viewer must not hold ${p}`);
  }

  for (const p of ['router:read', 'router:ack', 'router:history', 'router:diagnose', 'router:write']) {
    assert.equal(has('operator1', p), true, `operator must hold ${p}`);
  }
  for (const p of ['router:manage', 'router:purge', 'router:secrets']) {
    assert.equal(has('operator1', p), false, `operator must not hold ${p}`);
  }

  for (const p of rbac.SCOPED) {
    assert.equal(has('admin1', p), true, `admin must hold ${p}`);
  }
});

test('grants union to the most permissive, per scope', () => {
  reset();
  grant('u1', 'viewer', 'global');
  grant('u1', 'admin',  'site', SITE);
  assert.equal(rbac.can(sess('u1'), 'router:manage', rIn.id),  true,  'admin on the site wins there');
  assert.equal(rbac.can(sess('u1'), 'router:manage', rOut.id), false, 'but not elsewhere');
  assert.equal(rbac.can(sess('u1'), 'router:read',   rOut.id), true,  'the global viewer grant still applies');
});

test('group membership grants, and an empty group grants nothing', () => {
  reset();
  const g = db.createGroup({ name: 'Berlin Admins' });
  grant(g.id, 'admin', 'site', SITE, 'group');
  assert.equal(rbac.can(sess('u1'), 'router:manage', rIn.id), false, 'not a member yet');

  db.setGroupMembers(g.id, ['u1']);
  rbac.bump();
  assert.equal(rbac.can(sess('u1'), 'router:manage', rIn.id), true, 'membership confers the grant');

  db.setGroupMembers(g.id, []);
  rbac.bump();
  assert.equal(rbac.can(sess('u1'), 'router:manage', rIn.id), false, 'an empty group confers nothing');
});

test('deleting a group takes its grants and memberships with it', () => {
  reset();
  const g = db.createGroup({ name: 'Temp' });
  db.setGroupMembers(g.id, ['u1']);
  grant(g.id, 'admin', 'global', '', 'group');
  assert.equal(db.listGrants().length, 1);

  db.deleteGroup(g.id);
  rbac.bump();
  assert.equal(db.listGrants().length, 0, "the group's grants must not outlive it");
  assert.equal(db.getGroupMembers(g.id).length, 0, 'memberships must cascade');
  assert.equal(rbac.can(sess('u1'), 'system:principals'), false);
});

test('one principal cannot hold two grants on the same scope', () => {
  // SQLite treats NULLs as distinct in a UNIQUE index, so storing NULL for the
  // global scope_id would let this happen silently and leave two rows to
  // reconcile. '' is stored instead, which the constraint can actually see.
  reset();
  grant('u1', 'viewer', 'global');
  grant('u1', 'admin',  'global');
  const rows = db.listGrants({ principalId: 'u1' });
  assert.equal(rows.length, 1, 'the second grant must replace, not stack');
  assert.equal(rows[0].role, 'admin');
});

test('authMode none is implicitly admin, with or without a session', () => {
  reset();
  _modern = false;
  try {
    assert.equal(rbac.can(null, 'system:principals'), true);
    assert.equal(rbac.can(null, 'router:manage', rIn.id), true);
    assert.equal(rbac.effectiveRouterIds(null).length, 2, 'every router is visible');
  } finally { _modern = true; }
});

test('an unknown permission is denied rather than ignored', () => {
  reset();
  grant('u1', 'admin', 'global');
  assert.equal(rbac.can(sess('u1'), 'router:launch-missiles', rIn.id), false);
});

test('a grant on a router that no longer exists confers nothing', () => {
  reset();
  grant('u1', 'admin', 'router', 'deleted-router-id');
  assert.equal(rbac.can(sess('u1'), 'router:read', 'deleted-router-id'), false);
});

test('effectiveRouterIds is a concrete list, never a wildcard', () => {
  reset();
  grant('u1', 'admin', 'global');
  const ids = rbac.effectiveRouterIds(sess('u1'), 'router:manage');
  assert.ok(Array.isArray(ids));
  assert.ok(!ids.includes('*'), 'a sentinel would reintroduce the "empty means all" ambiguity');
  assert.equal(ids.length, 2);
});

test('capsFor never leaks another principal into the payload', () => {
  reset();
  grant('u1', 'operator', 'site', SITE);
  grant('u2', 'admin', 'global');            // somebody else's access
  const caps = rbac.capsFor(sess('u1'));
  assert.equal(caps.managePrincipals, false);
  assert.deepEqual(caps.routers.readable, [rIn.id]);
  assert.deepEqual(caps.routers.manageable, [], 'an operator cannot manage');
  assert.ok(!JSON.stringify(caps).includes('u2'),
    "another principal must not appear in a caller's capability payload");
});

test('wouldOrphanGlobalAdmin sees group-held grants and rolls back', () => {
  reset();
  const g = db.createGroup({ name: 'Admins' });
  db.setGroupMembers(g.id, ['u1']);
  grant(g.id, 'admin', 'global', '', 'group');

  // A count of user records cannot see this: the grant belongs to the group.
  assert.deepEqual(db.globalAdminUserIds(), ['u1']);

  const before = db.listGrants().length;
  const orphan = rbac.wouldOrphanGlobalAdmin(() => db.setGroupMembers(g.id, []));
  assert.equal(orphan, true, 'emptying the only admin group orphans the install');
  assert.equal(db.listGrants().length, before, 'the probe must not commit');
  assert.deepEqual(db.getGroupMembers(g.id), ['u1'], 'membership must survive the probe');
});
