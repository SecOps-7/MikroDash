'use strict';
// Users are granted access the same way groups are (issue #108, Phase 5b).
//
// Moving the Users card onto the grant editor exposed a destructive
// interaction: syncUserGrants() DELETES every grant a user holds and rebuilds
// them from the legacy role + allowedRouterIds pair. Both /api/users write
// routes called it unconditionally, so once access lives in grants, renaming a
// user would silently wipe everything an administrator had just built — a
// green-looking request that quietly removes access.
//
// These pin the destructive behaviour (so nobody "simplifies" the guard away)
// and the conditions under which it may run.

const { test } = require('node:test');
const assert   = require('node:assert');
const fs       = require('node:fs');
const os       = require('node:os');
const path     = require('node:path');

process.env.DATA_DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'roles-usergrants-'));

const db      = require('../src/db');
const Routers = require('../src/routers');
const rbac    = require('../src/rbac');

const INDEX_JS = fs.readFileSync(path.join(__dirname, '..', 'src', 'index.js'), 'utf8');

db.open();
rbac.init({ isModern: () => true });

const rtr = Routers.add({ label: 'UG', host: '10.6.0.1' });

// ── The hazard the guard exists for ──────────────────────────────────────────

test('syncUserGrants replaces every grant a user holds', () => {
  // Not a bug in itself — it is a projection, and a projection has to be
  // authoritative. It is only destructive when run against a user whose access
  // was granted directly, which is why the routes must not call it blindly.
  const u = { id: 'u-sync', username: 'sync', role: 'viewer', allowedRouterIds: [] };
  db.upsertGrant({ principalType: 'user', principalId: u.id, roleId: 'operator', scopeType: 'router', scopeId: rtr.id });
  assert.strictEqual(db.listGrants({ principalId: u.id }).length, 1);

  rbac.syncUserGrants(u);

  const after = db.listGrants({ principalId: u.id });
  assert.strictEqual(after.length, 1, 'the hand-made grant is gone, replaced by the projection');
  assert.strictEqual(after[0].scope_type, 'global', 'projected from allowedRouterIds: []');
  assert.strictEqual(after[0].role_id, 'readonly');

  for (const g of db.listGrants({ principalId: u.id })) db.deleteGrant(g.id);
});

test('both user write routes project only when the legacy fields were sent', () => {
  // The Users card no longer sends role or allowedRouterIds, so these guards
  // are what stop an ordinary username edit from wiping the user's access.
  const post = INDEX_JS.slice(INDEX_JS.indexOf("app.post('/api/users'"));
  assert.match(post.slice(0, 1800),
    /if \(role !== undefined \|\| allowedRouterIds !== undefined\) Rbac\.syncUserGrants\(user\);/,
    'POST /api/users must not project unconditionally');

  // Asserted as "the condition guards the call", not "the call is the next
  // statement" — an earlier version pinned the exact following line and broke
  // when the last-admin probe was inserted between them, which is a formatting
  // change, not a behavioural one.
  const put  = INDEX_JS.slice(INDEX_JS.indexOf("app.put('/api/users/:id'"));
  const body = put.slice(0, put.indexOf('res.json({ ok: true, user: updated })'));
  const cond = body.indexOf('if (updates.role !== undefined || updates.allowedRouterIds !== undefined)');
  assert.ok(cond > -1, 'PUT /api/users/:id must not project unconditionally');
  assert.ok(body.indexOf('Rbac.syncUserGrants(updated)') > cond,
    'the projection must sit inside that condition');
});

test('a new user created without legacy fields starts with no access at all', () => {
  // Deny-by-default: the account exists, and an administrator grants it
  // something explicitly. Projecting a default "viewer" would silently hand
  // every new account read of every router.
  const u = 'u-fresh';
  assert.deepStrictEqual(db.listGrants({ principalId: u }), []);
  assert.strictEqual(rbac.can({ userId: u }, 'router:read', rtr.id), false);
});

// ── The grant path itself ────────────────────────────────────────────────────

test('a user can hold different roles at different scopes', () => {
  // The whole reason for retiring the single-role dropdown: it could express
  // one role over all-or-listed routers, and nothing else.
  const u = 'u-multi';
  const site = 'site-ug';
  const inSite = Routers.add({ label: 'UG Site', host: '10.6.0.2' });
  Routers.update(inSite.id, { siteId: site });

  db.upsertGrant({ principalType: 'user', principalId: u, roleId: 'readonly', scopeType: 'global' });
  db.upsertGrant({ principalType: 'user', principalId: u, roleId: 'operator', scopeType: 'site', scopeId: site });
  rbac.bump();

  assert.strictEqual(rbac.can({ userId: u }, 'router:history', inSite.id), true,  'operator inside the site');
  assert.strictEqual(rbac.can({ userId: u }, 'router:history', rtr.id),    false, 'read only outside it');
  for (const g of db.listGrants({ principalId: u })) db.deleteGrant(g.id);
});

test('GET /api/users joins grants so the card can render real access', () => {
  assert.match(INDEX_JS, /principalType: 'user', principalId: u\.id/,
    '/api/users must join grants the way /api/groups does');
});

test('POST /api/grants takes a role id and validates it against the roles table', () => {
  const block = INDEX_JS.slice(INDEX_JS.indexOf("app.post('/api/grants'"));
  assert.match(block.slice(0, 1200), /const roleId = b\.roleId \|\|/, 'roleId is the current form');
  assert.match(block.slice(0, 1200), /!db\.getRole\(roleId\)/, 'and it must name a real role');
});

test('deleting the last administrator grant is still refused', () => {
  // Now reachable from the Users card, which previously could not remove a
  // grant at all — the guard has to hold on that path too.
  const block = INDEX_JS.slice(INDEX_JS.indexOf("app.delete('/api/grants/:id'"));
  assert.match(block.slice(0, 800), /Rbac\.wouldOrphanGlobalAdmin/);
});

// ── Live propagation ─────────────────────────────────────────────────────────

test('every permission mutation tells connected browsers to re-resolve', () => {
  // Editing a role used to leave open sessions on stale permissions until
  // reload, which reads as the feature not working.
  const bumps = (INDEX_JS.match(/Rbac\.bump\(\);/g) || []).length;
  const notes = (INDEX_JS.match(/Rbac\.bump\(\); _broadcastPermsChanged\(\);/g) || []).length;
  assert.strictEqual(bumps, notes, 'every Rbac.bump() in a route must also broadcast');
  assert.ok(notes >= 14, 'expected the full set of mutation routes, got ' + notes);
});

// ── Phase 6: the legacy fields decide nothing ────────────────────────────────

test('can() decides from grants alone, never from the user record', () => {
  // role and allowedRouterIds survive as downgrade mirrors only. They cannot
  // express a grant held through a group, so an administrator-via-group would
  // read as a viewer — which is why nothing may consult them.
  // Comments are stripped first: both functions *document* the legacy model at
  // length, and matching prose would make this assert the opposite of what it
  // means to.
  const strip   = (s) => s.replace(/\/\/[^\n]*/g, '').replace(/\/\*[\s\S]*?\*\//g, '');
  const RBAC_JS = fs.readFileSync(path.join(__dirname, '..', 'src', 'rbac.js'), 'utf8');

  const decide = strip(RBAC_JS.slice(RBAC_JS.indexOf('function can('), RBAC_JS.indexOf('function canPage(')));
  assert.ok(!/allowedRouterIds/.test(decide), 'can() must not read allowedRouterIds');
  assert.ok(!/\.role\b/.test(decide),         'can() must not read a role field');

  const viewFor = strip(RBAC_JS.slice(RBAC_JS.indexOf('function viewFor('), RBAC_JS.indexOf('function _anyConfers(')));
  assert.ok(/g\.role_id/.test(viewFor),       'viewFor resolves by role_id');
  assert.ok(!/g\.role\b(?!_)/.test(viewFor),  'and never by the legacy mirror');
});

test('the legacy pair is confined to the projection and the session mirror', () => {
  // Anywhere else would be a decision waiting to be made on stale data.
  const INDEX = INDEX_JS.replace(/\/\/[^\n]*/g, '').replace(/\/\*[\s\S]*?\*\//g, '');
  for (const m of INDEX.match(/[^\n]*allowedRouterIds[^\n]*/g) || []) {
    const ok = /session\.allowedRouterIds|SessionStore\.createSession|Users\.createUser|req\.body|updates\.allowedRouterIds|allowedRouterIds !== undefined/.test(m);
    assert.ok(ok, 'unexpected use of allowedRouterIds: ' + m.trim());
  }
});

test('the first administrator of a fresh install is granted explicitly', () => {
  // Both bootstrap paths run outside POST /api/users, so they do not get the
  // conditional projection — without an explicit grant a brand-new install
  // would come up with an admin account that every guard refuses.
  const setup = INDEX_JS.slice(INDEX_JS.indexOf("app.post('/api/users/setup'"));
  assert.match(setup.slice(0, 1600), /Rbac\.syncUserGrants\(user\)/,
    'first-run setup must grant the account it creates');

  const migrate = INDEX_JS.slice(INDEX_JS.indexOf('basic credentials migrated') - 1200);
  assert.match(migrate.slice(0, 1200), /Rbac\.syncUserGrants\(user\)/,
    'the dash-password migration must grant the account it creates');
});

test('the nudge carries no permissions of its own', () => {
  // The client re-asks and the server re-resolves, so a forged socket frame
  // cannot widen anyone's access.
  assert.match(INDEX_JS, /io\.emit\('perms:changed', \{\}\)/);
});
