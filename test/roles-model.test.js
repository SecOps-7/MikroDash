'use strict';
// Custom roles: union resolution and the page axis (issue #108, Phase 3).
//
// The old model resolved a user's grants by RANK — viewer < operator < admin —
// and kept one winning role per scope. Custom roles have no total order: a role
// granting Logs and a role granting DHCP, neither dominates the other. So
// resolution became a union of permission sets, and these pin the edges of that
// change plus the new page axis.
//
// The two that matter most are the ones that would fail permissively: no page
// may ever confer system administration, and a missing target must never mean
// "yes".

const { test } = require('node:test');
const assert   = require('node:assert');
const fs       = require('node:fs');
const os       = require('node:os');
const path     = require('node:path');

process.env.DATA_DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'roles-model-'));

const db      = require('../src/db');
const Pages   = require('../src/pages');
const Routers = require('../src/routers');
const rbac    = require('../src/rbac');

db.open();
let _modern = true;
rbac.init({ isModern: () => _modern });

const SITE   = 'site-model';
const inSite = Routers.add({ label: 'In Site', host: '10.8.0.1' });
const noSite = Routers.add({ label: 'No Site', host: '10.8.0.2' });
Routers.update(inSite.id, { siteId: SITE });

const sess = (userId) => ({ userId });
let _seq = 0;

/** A fresh custom role with the given matrix. */
function role(pages, name) {
  const r = db.createRole({ name: name || 'Role ' + (++_seq) });
  db.setRolePages(r.id, pages);
  rbac.bump();
  return r.id;
}

function grant(userId, roleId, scopeType = 'global', scopeId = '') {
  db.upsertGrant({ principalType: 'user', principalId: userId, roleId, scopeType, scopeId });
  rbac.bump();
}

function reset(userId) {
  for (const g of db.listGrants({ principalId: userId })) db.deleteGrant(g.id);
  rbac.bump();
}

// ── Union, where a rank cannot help ──────────────────────────────────────────

test('two roles neither of which dominates combine to the union of their pages', () => {
  // The case the old _stronger() could not express at all.
  const logs = role([{ page: 'logs', access: 'read' }]);
  const dhcp = role([{ page: 'dhcp', access: 'write' }]);
  const u = 'u-union';
  grant(u, logs);
  grant(u, dhcp, 'router', noSite.id);

  assert.strictEqual(rbac.canPage(sess(u), 'logs', 'read',  noSite.id), true);
  assert.strictEqual(rbac.canPage(sess(u), 'dhcp', 'write', noSite.id), true);
  // And neither role's absences leak in.
  assert.strictEqual(rbac.canPage(sess(u), 'vpn',  'read',  noSite.id), false);
  assert.strictEqual(rbac.canPage(sess(u), 'logs', 'write', noSite.id), false);
  reset(u);
});

test('a union across scopes applies only where each grant reaches', () => {
  const siteRole   = role([{ page: 'firewall', access: 'write' }]);
  const globalRole = role([{ page: 'logs', access: 'read' }]);
  const u = 'u-scoped-union';
  grant(u, globalRole);
  grant(u, siteRole, 'site', SITE);

  assert.strictEqual(rbac.canPage(sess(u), 'firewall', 'write', inSite.id), true);
  assert.strictEqual(rbac.canPage(sess(u), 'firewall', 'write', noSite.id), false, 'site grant stops at the site');
  assert.strictEqual(rbac.canPage(sess(u), 'logs', 'read', noSite.id), true, 'global reaches everywhere');
  reset(u);
});

test('a router-scoped grant confers nothing on a site target', () => {
  const r = role([{ page: 'logs', access: 'read' }]);
  const u = 'u-router-only';
  grant(u, r, 'router', inSite.id);
  assert.strictEqual(rbac.canPage(sess(u), 'logs', 'read', inSite.id), true);
  assert.strictEqual(rbac.canPage(sess(u), 'logs', 'read', { type: 'site', id: SITE }), false);
  reset(u);
});

test('an empty role is legal and grants nothing', () => {
  const empty = role([], 'Empty Role');
  const u = 'u-empty';
  grant(u, empty);
  assert.strictEqual(rbac.can(sess(u), 'router:read', noSite.id), false);
  for (const pg of Pages.KEYS) {
    assert.strictEqual(rbac.canPage(sess(u), pg, 'read', noSite.id), false, pg);
  }
  reset(u);
});

test('a grant cannot even be written for a role that does not exist', () => {
  // The foreign key refuses it, so the dangling state is unreachable through
  // the API — a stronger guarantee than resolving it safely afterwards.
  assert.throws(
    () => db.upsertGrant({ principalType: 'user', principalId: 'u-ghost', roleId: 'no-such-role', scopeType: 'global' }),
    /FOREIGN KEY/i);
});

test('and if one existed anyway, it would confer nothing', () => {
  // Defence in depth. foreign_keys is per-connection, so a hand-edited database
  // or a future migration bug could still produce this row; the resolver must
  // fail closed rather than throw or, worse, treat it as a wildcard.
  const u = 'u-ghost';
  const raw = new (require('better-sqlite3'))(path.join(process.env.DATA_DIR, 'mikrodash.db'));
  raw.pragma('foreign_keys = OFF');
  raw.prepare(`INSERT INTO grants (id, principal_type, principal_id, role_id, role, scope_type, scope_id, created_at)
               VALUES ('ghost', 'user', ?, 'no-such-role', 'viewer', 'global', '', 1)`).run(u);
  raw.close();
  rbac.bump();

  assert.strictEqual(rbac.can(sess(u), 'router:read', noSite.id), false);
  assert.strictEqual(rbac.canPage(sess(u), 'logs', 'read', noSite.id), false);
  reset(u);
});

// ── The escalation firewall ──────────────────────────────────────────────────

test('no page, at any access level, confers system administration', () => {
  // Iterates the whole matrix. This is the decision "system administration is
  // Administrator-only" expressed as a property of the resolver rather than of
  // getting READ_CONFERS/WRITE_CONFERS right.
  const forbidden = ['system:principals', 'system:db', 'router:create'];
  for (const pg of Pages.KEYS) {
    for (const access of ['read', 'write']) {
      const u = `u-esc-${pg}-${access}`;
      grant(u, role([{ page: pg, access }], `Esc ${pg} ${access}`));
      for (const perm of forbidden) {
        assert.strictEqual(rbac.can(sess(u), perm), false, `${pg}:${access} must not confer ${perm}`);
      }
      reset(u);
    }
  }
});

test('settings write is the one global-only permission a custom role may hold', () => {
  const r = role([{ page: 'settings', access: 'write' }], 'Settings Writer');
  const u = 'u-settings';
  grant(u, r);
  assert.strictEqual(rbac.can(sess(u), 'system:settings'),   true);
  assert.strictEqual(rbac.can(sess(u), 'system:principals'), false);
  assert.strictEqual(rbac.can(sess(u), 'system:db'),         false);
  assert.strictEqual(rbac.can(sess(u), 'router:create'),     false);
  reset(u);
});

test('settings write held at site scope reaches nothing — global-only means global', () => {
  const r = role([{ page: 'settings', access: 'write' }], 'Site Settings Writer');
  const u = 'u-settings-site';
  grant(u, r, 'site', SITE);
  assert.strictEqual(rbac.can(sess(u), 'system:settings'), false);
  reset(u);
});

test('no page confers router:secrets — credential-adjacent stays Administrator-only', () => {
  for (const pg of Pages.KEYS) {
    const u = 'u-secrets-' + pg;
    grant(u, role([{ page: pg, access: 'write' }], `Secrets ${pg}`));
    assert.strictEqual(rbac.can(sess(u), 'router:secrets', noSite.id), false, pg);
    reset(u);
  }
});

// ── Administrator is structural ──────────────────────────────────────────────

test('Administrator satisfies permissions and pages it has no row for', () => {
  const u = 'u-admin';
  grant(u, 'administrator');
  assert.deepStrictEqual(db.rolePages('administrator'), [], 'no rows at all');
  for (const perm of rbac.PERMISSIONS) {
    assert.strictEqual(rbac.can(sess(u), perm, noSite.id), true, perm);
  }
  for (const pg of Pages.KEYS) {
    assert.strictEqual(rbac.canPage(sess(u), pg, 'write', noSite.id), true, pg);
  }
  reset(u);
});

// ── canPage edges ────────────────────────────────────────────────────────────

test('write implies read, and read does not imply write', () => {
  const u = 'u-implies';
  grant(u, role([{ page: 'vpn', access: 'write' }, { page: 'logs', access: 'read' }], 'Implies'));
  assert.strictEqual(rbac.canPage(sess(u), 'vpn',  'read',  noSite.id), true);
  assert.strictEqual(rbac.canPage(sess(u), 'vpn',  'write', noSite.id), true);
  assert.strictEqual(rbac.canPage(sess(u), 'logs', 'read',  noSite.id), true);
  assert.strictEqual(rbac.canPage(sess(u), 'logs', 'write', noSite.id), false);
  reset(u);
});

test('canPage fails closed on a missing target, an unknown page and an unknown level', () => {
  const u = 'u-closed';
  grant(u, 'administrator');
  assert.strictEqual(rbac.canPage(sess(u), 'logs', 'read'),             false, 'no target');
  assert.strictEqual(rbac.canPage(sess(u), 'logs', 'read', ''),         false, 'empty target');
  assert.strictEqual(rbac.canPage(sess(u), 'logs', 'read', null),       false, 'null target');
  assert.strictEqual(rbac.canPage(sess(u), 'nope', 'read', noSite.id),  false, 'unknown page');
  assert.strictEqual(rbac.canPage(sess(u), 'logs', 'admin', noSite.id), false, 'unknown access level');
  assert.strictEqual(rbac.canPage(sess(u), 'logs', 'read', 'no-such-router'), false, 'unknown router');
  reset(u);
});

test('canPage denies without a session, and allows everything when auth is off', () => {
  assert.strictEqual(rbac.canPage(null, 'logs', 'read', noSite.id), false);
  assert.strictEqual(rbac.canPage({},   'logs', 'read', noSite.id), false);
  _modern = false;
  assert.strictEqual(rbac.canPage(null, 'logs', 'write', noSite.id), true, 'authMode none is implicitly admin');
  _modern = true;
});

// ── Memoisation ──────────────────────────────────────────────────────────────

test('editing a role changes the answer, but only after bump()', () => {
  // Role definitions are cached on the same generation counter as user views.
  // A write to roles/role_pages that forgets to bump is silent and permanent
  // until restart — the new easy-to-forget hazard this change introduces.
  const r = role([{ page: 'logs', access: 'read' }], 'Mutable');
  const u = 'u-memo';
  grant(u, r);
  assert.strictEqual(rbac.canPage(sess(u), 'dhcp', 'read', noSite.id), false);

  db.setRolePages(r, [{ page: 'logs', access: 'read' }, { page: 'dhcp', access: 'read' }]);
  assert.strictEqual(rbac.canPage(sess(u), 'dhcp', 'read', noSite.id), false, 'still cached');

  rbac.bump();
  assert.strictEqual(rbac.canPage(sess(u), 'dhcp', 'read', noSite.id), true, 'visible after bump');
  reset(u);
});

// ── capsFor ──────────────────────────────────────────────────────────────────

test('capsFor reports page access without disclosing the grant graph', () => {
  const u = 'u-caps';
  grant(u, role([{ page: 'logs', access: 'read' }, { page: 'firewall', access: 'write' }], 'Caps Role'));
  const caps = rbac.capsFor(sess(u));

  assert.strictEqual(caps.pages.logs, 'read');
  assert.strictEqual(caps.pages.firewall, 'write');
  assert.strictEqual(caps.pages.vpn, undefined, 'pages with no access are absent, not false');
  assert.strictEqual(caps.managePrincipals, false);

  const asText = JSON.stringify(caps);
  assert.ok(!asText.includes('Caps Role'), 'no role names');
  assert.ok(!asText.includes('u-caps'),    'no principal ids');
  reset(u);
});

test('capsFor unions page access across readable routers', () => {
  // The nav is not per-router, so the first paint needs the union; the
  // per-router answer is authoritative and arrives over the socket.
  const u = 'u-caps-union';
  grant(u, role([{ page: 'logs', access: 'read' }], 'Caps A'), 'router', noSite.id);
  grant(u, role([{ page: 'vpn',  access: 'write' }], 'Caps B'), 'router', inSite.id);
  const caps = rbac.capsFor(sess(u));
  assert.strictEqual(caps.pages.logs, 'read');
  assert.strictEqual(caps.pages.vpn,  'write');
  reset(u);
});
