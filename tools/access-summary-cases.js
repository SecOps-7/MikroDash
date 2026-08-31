'use strict';
/**
 * `Rbac.accessSummaryFor(userId)` — what `GET /api/account/access` answers.
 *
 * ── WHY IT NEEDS A CORPUS AND NOT A READING ─────────────────────────────────
 *
 * The happy path is four lines and obvious. What is not obvious is what it does
 * with a grant whose target has GONE, and the live comment is explicit that this
 * is deliberate: "A role, site or router can be deleted while a stale grant row
 * survives until the next sweep, so every lookup can miss. Drop those rather
 * than rendering 'null' at somebody."
 *
 * Three different drops, and they are not the same shape:
 *
 *   a deleted ROLE   drops the NAME from a list, leaving the scope in place
 *   a deleted SITE   drops the whole ROW
 *   a deleted ROUTER drops the whole ROW
 *
 * A port that dropped rows for a missing role, or kept a row with a null name,
 * would pass any test written from the happy path — and the second would render
 * the word "null" in an operator's account modal.
 *
 * ── LIFTED, BECAUSE rbac.js CANNOT BE REQUIRED ON THE HOST ──────────────────
 *
 * `rbac.js` requires `db.js`, which requires `better-sqlite3` — a native module
 * built only inside the app container. So the three functions are lifted and
 * evaluated in a `vm` with `db` and `Routers` stubbed: the layer BELOW the
 * decision, which is the rule this repo's other lifts follow. The decision
 * itself is the live code, character for character.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/access-summary-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const src = fs.readFileSync(path.join(LIVE, 'src', 'rbac.js'), 'utf8');

function liftFn(decl) {
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find ' + decl + ' — rbac.js has been rewritten');
  let depth = 0;
  for (let j = src.indexOf('{', i); j < src.length; j++) {
    if (src[j] === '{') depth++;
    else if (src[j] === '}') { depth--; if (!depth) return src.slice(i, j + 1); }
  }
  throw new Error('unbalanced ' + decl);
}

// MARKER ASSERTIONS. A lift that lost the filter would record "nothing is ever
// dropped" as the live behaviour, which is the one thing this corpus exists to
// contradict.
const body = [liftFn('function _addTo(map, key, roleId) {'),
  liftFn('function viewFor(userId) {'),
  liftFn('function accessSummaryFor(userId) {')].join('\n');
for (const marker of ['db.getRole', 'db.getSite', 'Routers.getById',
  'filter(x => x.siteName)', 'filter(x => x.routerLabel)', '.filter(Boolean)']) {
  assert.ok(body.includes(marker),
    'the lifted accessSummaryFor has no ' + marker + ' — the slice is wrong, or the live '
    + 'function no longer drops what it used to');
}

// ── THE WORLD THE STUBS DESCRIBE ────────────────────────────────────────────
//
// Everything below the decision, and nothing above it. `grantsForUser` already
// resolves group membership in the live db.js, so the stub returns the grants a
// user effectively holds — how they were reached is not this function's
// question.
const ROLES = { 'role-admin': 'Administrator', 'role-ops': 'Operator', 'role-ro': 'Read only' };
const SITES = { 'site-hq': 'Head Office', 'site-dc': 'Data Centre' };
const ROUTERS = {
  'rtr-1': { label: 'Edge Router', host: '10.0.0.1' },
  // NO LABEL. The live fallback is `r.label || r.host`, so this row survives
  // and shows the host — a port using `r.label` alone would DROP it, which is
  // the same bug as rendering null but in the other direction.
  'rtr-2': { label: '', host: '10.0.0.2' },
};

function run(grants) {
  const ctx = {
    Map, Set, Array, Object, String, JSON,
    _gen: 0,
    _views: new Map(),
    db: {
      grantsForUser: () => grants,
      getRole: (id) => (ROLES[id] ? { id, name: ROLES[id] } : null),
      getSite: (id) => (SITES[id] ? { id, name: SITES[id] } : null),
    },
    Routers: { getById: (id) => ROUTERS[id] || null },
    module: { exports: {} },
  };
  vm.createContext(ctx);
  vm.runInContext(body + '\nmodule.exports = accessSummaryFor;', ctx);
  return ctx.module.exports('u-1');
}

const g = (scopeType, scopeID, roleID) =>
  ({ scope_type: scopeType, scope_id: scopeID, role_id: roleID });

const CASES = {
  noGrants: [],
  oneGlobalRole: [g('global', null, 'role-admin')],
  // SORTED BY NAME, not by id and not by grant order.
  twoGlobalRolesSort: [g('global', null, 'role-ops'), g('global', null, 'role-admin')],
  oneSite: [g('site', 'site-hq', 'role-ops')],
  twoSites: [g('site', 'site-dc', 'role-ro'), g('site', 'site-hq', 'role-ops')],
  oneRouter: [g('router', 'rtr-1', 'role-ro')],
  // A ROUTER WITH NO LABEL falls back to its host and is KEPT.
  routerWithNoLabel: [g('router', 'rtr-2', 'role-ro')],
  everything: [g('global', null, 'role-admin'), g('site', 'site-hq', 'role-ops'),
    g('router', 'rtr-1', 'role-ro')],
  // TWO ROLES AT ONE SCOPE collapse into one row with two names.
  twoRolesOneSite: [g('site', 'site-hq', 'role-ops'), g('site', 'site-hq', 'role-ro')],
  // The same role twice at one scope is a Set — one name, not two.
  sameRoleTwiceAtOneSite: [g('site', 'site-hq', 'role-ops'), g('site', 'site-hq', 'role-ops')],

  // ── THE THREE DROPS ──────────────────────────────────────────────────
  deletedRoleGlobal: [g('global', null, 'role-gone')],
  // The scope SURVIVES with one fewer name, rather than the row disappearing.
  deletedRoleAmongLive: [g('site', 'site-hq', 'role-ops'), g('site', 'site-hq', 'role-gone')],
  deletedSite: [g('site', 'site-gone', 'role-ops')],
  deletedRouter: [g('router', 'rtr-gone', 'role-ops')],
  // A deleted site beside a live one: one row, not two and not none.
  deletedSiteAmongLive: [g('site', 'site-gone', 'role-ops'), g('site', 'site-hq', 'role-ro')],
  // EVERY role at a live site is gone. The row keeps its name and an EMPTY role
  // list — the filter is on `siteName`, not on the roles — which is a state a
  // port collapsing empty rows would lose.
  siteWhoseRolesAreAllGone: [g('site', 'site-hq', 'role-gone')],

  // An unknown scope_type is ignored by viewFor's if/else chain.
  unknownScopeType: [g('elsewhere', 'x', 'role-admin')],
  // ...AND IT NAMES A REAL SITE, which is what makes the case discriminating.
  // With a scope id nothing recognises, a port that mis-filed unknown types
  // into `bySite` produces a row for a site that does not exist and drops it
  // again — the same answer by accident. Pointing it at a LIVE site is the only
  // way the mis-filing becomes visible.
  unknownScopeTypeNamingARealSite: [g('elsewhere', 'site-hq', 'role-ops')],
};

const cases = {};
for (const [name, grants] of Object.entries(CASES)) cases[name] = { grants, out: run(grants) };

// ── BELIEVABILITY ───────────────────────────────────────────────────────────
{
  const o = (n) => cases[n].out;
  assert.deepEqual(o('noGrants'), { global: [], sites: [], routers: [] });
  assert.deepEqual(o('oneGlobalRole').global, ['Administrator']);
  assert.deepEqual(o('twoGlobalRolesSort').global, ['Administrator', 'Operator'],
    'the role names are not sorted by NAME');
  assert.equal(o('oneSite').sites.length, 1);
  assert.equal(o('oneSite').sites[0].siteName, 'Head Office');
  assert.equal(o('routerWithNoLabel').routers[0].routerLabel, '10.0.0.2',
    'a router with no label did not fall back to its host -- the live expression is '
    + '`r.label || r.host`, and a port reading label alone DROPS the row');
  assert.deepEqual(o('twoRolesOneSite').sites[0].roles, ['Operator', 'Read only']);
  assert.deepEqual(o('sameRoleTwiceAtOneSite').sites[0].roles, ['Operator'],
    'the same role granted twice at one scope produced two names -- viewFor collects into a Set');

  // The drops, each asserted for its OWN shape.
  assert.deepEqual(o('deletedRoleGlobal').global, [],
    'a deleted ROLE left something in the global list -- it must drop the NAME');
  assert.deepEqual(o('deletedRoleAmongLive').sites[0].roles, ['Operator'],
    'a deleted role beside a live one did not drop cleanly');
  assert.equal(o('deletedSite').sites.length, 0,
    'a deleted SITE kept its row -- the whole row goes, or the modal renders null');
  assert.equal(o('deletedRouter').routers.length, 0,
    'a deleted ROUTER kept its row');
  assert.equal(o('deletedSiteAmongLive').sites.length, 1,
    'a deleted site beside a live one took the live one with it, or kept itself');
  assert.equal(o('deletedSiteAmongLive').sites[0].siteName, 'Head Office');

  // THE ASYMMETRY, asserted as an asymmetry. A site whose roles have ALL been
  // deleted keeps its row with an empty list, where a deleted site loses the
  // row entirely — two different answers to "everything here is gone".
  const allGone = o('siteWhoseRolesAreAllGone');
  assert.equal(allGone.sites.length, 1,
    'a site whose roles were all deleted lost its ROW. The filter is on siteName, not on the '
    + 'roles, so the row survives with an empty list -- and a port that collapsed it would '
    + 'silently stop showing that the grant exists');
  assert.deepEqual(allGone.sites[0].roles, []);

  assert.deepEqual(o('unknownScopeType'), { global: [], sites: [], routers: [] });
  assert.deepEqual(o('unknownScopeTypeNamingARealSite'), { global: [], sites: [], routers: [] },
    'an unknown scope_type naming a REAL site produced a row -- viewFor\'s if/else chain has no '
    + 'else, so anything that is not global, site or router is ignored entirely');

  // Believability across the set: something is kept and something is dropped, or
  // every assertion above could pass against a stub.
  const kept = Object.values(cases).filter((c) =>
    c.out.global.length || c.out.sites.length || c.out.routers.length).length;
  assert.ok(kept > 0 && kept < Object.keys(cases).length,
    'every case has the same answer, so this corpus proves nothing');
}

const OUT = path.join(ROOT, 'testdata', 'access-summary-cases.json');
const text = JSON.stringify({ roles: ROLES, sites: SITES, routers: ROUTERS, cases }, null, 2) + '\n';
if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== text) {
    console.error('testdata/access-summary-cases.json is stale — run: '
      + 'node tools/access-summary-cases.js');
    process.exit(1);
  }
  console.log('access-summary-cases.json is current');
} else {
  fs.writeFileSync(OUT, text);
  console.log('wrote ' + OUT + ' (' + Object.keys(cases).length + ' grant sets)');
}
