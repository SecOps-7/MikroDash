'use strict';
// Self-service account surface: what a user may see about themselves, and the
// guards on the routes that let them act on themselves.
//
// The thing most likely to go wrong here is not the resolver but the guards: the
// routes sit directly below a block where every single handler carries
// Rbac.requireGlobalAdmin, and copying that one line down would lock the feature
// to administrators — the one audience that does not need it. That is asserted
// explicitly rather than left to review.

const { test } = require('node:test');
const assert   = require('node:assert');
const fs       = require('node:fs');
const os       = require('node:os');
const path     = require('node:path');

process.env.DATA_DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'account-access-'));

const db      = require('../src/db');
const Routers = require('../src/routers');
const rbac    = require('../src/rbac');

db.open();
rbac.init({ isModern: () => true });

const rA = Routers.add({ label: 'Router A', host: '10.0.0.1' });
const rB = Routers.add({ label: 'Router B', host: '10.0.0.2' });
const site = db.createSite({ name: 'Berlin' });
Routers.update(rA.id, { siteId: site.id });

function reset() {
  for (const g of db.listGrants()) db.deleteGrant(g.id);
  for (const gr of db.listGroups()) db.deleteGroup(gr.id);
  rbac.bump();
}

function grant(principalId, roleId, scopeType, scopeId = '', principalType = 'user') {
  db.upsertGrant({ principalType, principalId, role: 'viewer', roleId, scopeType, scopeId });
  rbac.bump();
}

// ── accessSummaryFor ─────────────────────────────────────────────────────────

test('a user with no grants gets empty lists rather than an error', () => {
  reset();
  assert.deepEqual(rbac.accessSummaryFor('u1'), { global: [], sites: [], routers: [] });
});

test('a missing user id resolves to nothing, never to everything', () => {
  reset();
  assert.deepEqual(rbac.accessSummaryFor(''),        { global: [], sites: [], routers: [] });
  assert.deepEqual(rbac.accessSummaryFor(undefined), { global: [], sites: [], routers: [] });
});

test('roles resolve to names at each scope', () => {
  reset();
  grant('u1', 'administrator', 'global');
  grant('u1', 'operator', 'site', site.id);
  grant('u1', 'readonly', 'router', rB.id);

  const a = rbac.accessSummaryFor('u1');
  assert.deepEqual(a.global, ['Administrator']);
  assert.equal(a.sites.length, 1);
  assert.equal(a.sites[0].siteName, 'Berlin');
  assert.deepEqual(a.sites[0].roles, ['Operator']);
  assert.equal(a.routers.length, 1);
  assert.equal(a.routers[0].routerLabel, 'Router B');
  assert.deepEqual(a.routers[0].roles, ['Read Only']);
});

test('a role held only through a group still appears', () => {
  reset();
  const g = db.createGroup({ name: 'NOC' });
  db.setGroupMembers(g.id, ['u2']);
  grant(g.id, 'operator', 'router', rA.id, 'group');

  const a = rbac.accessSummaryFor('u2');
  assert.equal(a.routers.length, 1, 'the legacy allowedRouterIds field cannot express this');
  assert.deepEqual(a.routers[0].roles, ['Operator']);
});

test('this answers only for the user asked about', () => {
  reset();
  grant('u1', 'administrator', 'global');
  assert.deepEqual(rbac.accessSummaryFor('u2'), { global: [], sites: [], routers: [] },
    "one user's access must never leak into another's summary");
});

test('a grant pointing at a deleted router is dropped, not rendered as null', () => {
  reset();
  const doomed = Routers.add({ label: 'Doomed', host: '10.0.0.9' });
  grant('u1', 'readonly', 'router', doomed.id);
  assert.equal(rbac.accessSummaryFor('u1').routers.length, 1);

  Routers.remove(doomed.id);
  rbac.bump();
  assert.deepEqual(rbac.accessSummaryFor('u1').routers, [],
    'a stale grant must not surface as a nameless row');
});

// ── Route guards (source assertions) ─────────────────────────────────────────
// src/index.js calls server.listen() at require time, so no test in this repo
// exercises routes over HTTP. These read the source instead.

const INDEX_JS = fs.readFileSync(path.join(__dirname, '..', 'src', 'index.js'), 'utf8');

test('the account routes exist and are gated on identity, not administration', () => {
  const paths = [
    "app.get('/api/account/access'",
    "app.get('/api/account/sessions'",
    "app.post('/api/account/password'",
    "app.post('/api/account/sessions/revoke-others'",
  ];
  for (const p of paths) {
    const at = INDEX_JS.indexOf(p);
    assert.ok(at > -1, 'missing route: ' + p);
    // The registration line, up to the handler body.
    const line = INDEX_JS.slice(at, INDEX_JS.indexOf('{', at));
    assert.ok(!/requireGlobalAdmin/.test(line),
      p + ' must NOT be admin-gated — that would hide the feature from everyone who needs it');
    assert.ok(/_requireAccount/.test(line), p + ' must be gated on _requireAccount');
    assert.ok(/Limiter/.test(line), p + ' must be rate limited');
  }
});

test('the account guard asks for an identity, and nothing more', () => {
  const at   = INDEX_JS.indexOf('function _requireAccount');
  const body = INDEX_JS.slice(at, INDEX_JS.indexOf('\n}', at));
  assert.ok(/req\.authSession/.test(body) && /userId/.test(body));
  assert.ok(!/Rbac\.can|requireGlobalAdmin/.test(body),
    'authorization for the routers a user may be told about belongs to Rbac at send time, not here');
});

test('the password route verifies the current password and enforces a floor', () => {
  const at   = INDEX_JS.indexOf("app.post('/api/account/password'");
  const body = INDEX_JS.slice(at, at + 2500);
  assert.ok(/verifyPassword/.test(body), 'a password change must prove the current one');
  assert.ok(/length < 4/.test(body), 'the same floor POST /api/users applies');
  assert.ok(/deleteSessionsForUser/.test(body), 'other sessions go with a password change');
  // The caller keeps their own session, or a security action signs them out.
  assert.ok(/deleteSessionsForUser\(req\.authSession\.userId, token\)/.test(body),
    'the current token must be passed as the exception');
});

test('the sessions route never ships a session token to the browser', () => {
  const at   = INDEX_JS.indexOf("app.get('/api/account/sessions'");
  const body = INDEX_JS.slice(at, INDEX_JS.indexOf('});', at));
  assert.ok(/createdAt/.test(body) && /current/.test(body), 'rows describe when and which');
  assert.ok(!/token:\s*s\.token/.test(body),
    'the token is the credential itself — projecting it into the response would hand it out');
});
