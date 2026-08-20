'use strict';
// The seeded roles must mean EXACTLY what the hardcoded ones did (issue #108, Phase 3).
//
// This is the highest-value file in the change. Phase 3 replaced a static
// ROLE_PERMS object with a projection from role_pages, and a mistake there is
// invisible: a role that confers one permission too many silently widens every
// existing grant on upgrade, and one too few silently locks people out. Both
// look like a clean migration and a green suite.
//
// So the OLD table is inlined below as a fixture. If someone later "tidies"
// READ_CONFERS / WRITE_CONFERS or edits the v7 seed, this fails loudly instead
// of quietly redefining what every deployed grant means.

const { test } = require('node:test');
const assert   = require('node:assert');
const fs       = require('node:fs');
const os       = require('node:os');
const path     = require('node:path');

process.env.DATA_DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'roles-equiv-'));

const db      = require('../src/db');
const Routers = require('../src/routers');
const rbac    = require('../src/rbac');

db.open();
rbac.init({ isModern: () => true });

// ── The pre-#108 table, verbatim from src/rbac.js:44-54 ──────────────────────
const GLOBAL_ONLY_PERMS = ['system:principals', 'system:settings', 'system:db', 'router:create'];
const OLD_ROLE_PERMS = {
  viewer: new Set(['router:read']),
  operator: new Set([
    'router:read', 'router:ack', 'router:history', 'router:diagnose', 'router:write',
  ]),
  admin: new Set([
    'router:read', 'router:ack', 'router:history', 'router:diagnose', 'router:write',
    'router:manage', 'router:purge', 'router:secrets',
    // Added after #108. Administrator is builtin=1, so its reach is structural
    // rather than a stored matrix — every permission added later lands here with
    // no data migration, which is the whole point of seeding it that way. A new
    // permission appearing in this set is therefore expected; one appearing in
    // viewer or operator below would be the bug.
    'router:scan',
    'router:schedule',
    ...GLOBAL_ONLY_PERMS,
  ]),
};
// What the v7 migration seeded each legacy role as.
const SEED_ID = { viewer: 'readonly', operator: 'operator', admin: 'administrator' };

const SITE = 'site-equiv';
const inSite = Routers.add({ label: 'In Site', host: '10.9.0.1' });
const noSite = Routers.add({ label: 'No Site', host: '10.9.0.2' });
Routers.update(inSite.id, { siteId: SITE });

const sess = (userId) => ({ userId });

function only(userId, roleId, scopeType, scopeId = '') {
  for (const g of db.listGrants({ principalId: userId })) db.deleteGrant(g.id);
  db.upsertGrant({ principalType: 'user', principalId: userId, roleId, scopeType, scopeId });
  rbac.bump();
}

// ── Scoped permissions ───────────────────────────────────────────────────────

for (const legacy of ['viewer', 'operator', 'admin']) {
  test(`${legacy} → ${SEED_ID[legacy]}: scoped permissions are unchanged at global scope`, () => {
    const u = 'u-global-' + legacy;
    only(u, SEED_ID[legacy], 'global');
    for (const perm of rbac.SCOPED) {
      assert.strictEqual(rbac.can(sess(u), perm, noSite.id), OLD_ROLE_PERMS[legacy].has(perm),
        `${legacy} / ${perm}`);
    }
  });

  test(`${legacy} → ${SEED_ID[legacy]}: scoped permissions are unchanged at site scope`, () => {
    const u = 'u-site-' + legacy;
    only(u, SEED_ID[legacy], 'site', SITE);
    for (const perm of rbac.SCOPED) {
      // The router inside the site gets exactly the old answer...
      assert.strictEqual(rbac.can(sess(u), perm, inSite.id), OLD_ROLE_PERMS[legacy].has(perm),
        `${legacy} / ${perm} / in site`);
      // ...and the router outside it gets nothing, as before.
      assert.strictEqual(rbac.can(sess(u), perm, noSite.id), false,
        `${legacy} / ${perm} / outside site`);
    }
  });

  test(`${legacy} → ${SEED_ID[legacy]}: scoped permissions are unchanged at router scope`, () => {
    const u = 'u-router-' + legacy;
    only(u, SEED_ID[legacy], 'router', noSite.id);
    for (const perm of rbac.SCOPED) {
      assert.strictEqual(rbac.can(sess(u), perm, noSite.id), OLD_ROLE_PERMS[legacy].has(perm),
        `${legacy} / ${perm} / granted router`);
      assert.strictEqual(rbac.can(sess(u), perm, inSite.id), false,
        `${legacy} / ${perm} / other router`);
    }
  });
}

// ── Global-only permissions ──────────────────────────────────────────────────

for (const legacy of ['viewer', 'operator', 'admin']) {
  test(`${legacy} → ${SEED_ID[legacy]}: global-only permissions are unchanged`, () => {
    const u = 'u-go-' + legacy;
    only(u, SEED_ID[legacy], 'global');
    for (const perm of GLOBAL_ONLY_PERMS) {
      assert.strictEqual(rbac.can(sess(u), perm), OLD_ROLE_PERMS[legacy].has(perm),
        `${legacy} / ${perm}`);
    }
  });

  test(`${legacy} → ${SEED_ID[legacy]}: a scoped grant still reaches no global-only permission`, () => {
    // The security boundary: even Administrator held over one site cannot
    // manage principals. This held before #108 and must still hold.
    for (const [scopeType, scopeId] of [['site', SITE], ['router', noSite.id]]) {
      const u = `u-go-${legacy}-${scopeType}`;
      only(u, SEED_ID[legacy], scopeType, scopeId);
      for (const perm of GLOBAL_ONLY_PERMS) {
        assert.strictEqual(rbac.can(sess(u), perm), false, `${legacy} / ${perm} / ${scopeType}`);
      }
    }
  });
}

// ── The union, which used to be a maximum ────────────────────────────────────

test('two seeded roles on one scope combine to the more permissive, as before', () => {
  // Under the old model _stronger() picked operator over viewer by rank. Under
  // the union the answer is the same, because operator's set is a superset —
  // which is precisely why the rank could be deleted without changing anything.
  const u = 'u-union';
  for (const g of db.listGrants({ principalId: u })) db.deleteGrant(g.id);
  db.upsertGrant({ principalType: 'user', principalId: u, roleId: 'readonly', scopeType: 'global' });
  db.upsertGrant({ principalType: 'user', principalId: u, roleId: 'operator', scopeType: 'site', scopeId: SITE });
  rbac.bump();

  for (const perm of rbac.SCOPED) {
    assert.strictEqual(rbac.can(sess(u), perm, inSite.id), OLD_ROLE_PERMS.operator.has(perm),
      'in-site router sees the union: ' + perm);
    assert.strictEqual(rbac.can(sess(u), perm, noSite.id), OLD_ROLE_PERMS.viewer.has(perm),
      'outside router sees only the global grant: ' + perm);
  }
});

test('the seeded page matrices are what produce that equivalence', () => {
  // Documents the derivation, so a failure above points at the cause: reports
  // read is the only thing conferring router:history, and the two write rows
  // are the only things conferring ack and diagnose.
  const ro = Object.fromEntries(db.rolePages('readonly').map(r => [r.page, r.access]));
  const op = Object.fromEntries(db.rolePages('operator').map(r => [r.page, r.access]));
  assert.strictEqual(ro.reports, undefined, 'viewer has no router:history');
  assert.strictEqual(op.reports, 'read',    'operator has router:history');
  assert.strictEqual(op.dashboard, 'write', 'operator has router:ack');
  assert.strictEqual(op.firewall,  'write', 'operator has router:diagnose');
  assert.deepStrictEqual(db.rolePages('administrator'), [], 'admin is structural');
});
