'use strict';
// Authorization policy (issue #78).
//
// src/db.js owns the rows; this file owns what they mean. Everything funnels
// through can(), so the vocabulary of permissions and the rule that a scoped
// grant can never confer system administration live in exactly one place
// instead of being re-remembered at two dozen call sites.
//
// The model is standard RBAC: a principal (a user, or a group of users) holds a
// role over a scope. Scopes are global, a site, or a single router.

const db      = require('./db');
const Users   = require('./users');
const Routers = require('./routers');
const Pages   = require('./pages');

// ── Vocabulary ───────────────────────────────────────────────────────────────

// Permissions that a site- or router-scoped grant can NEVER satisfy, however
// senior the role. This set IS the "system administration is global only"
// decision. Without it, granting someone admin over one site would let them
// edit grants — including their own — and the scope would stop being a boundary
// and become a default view.
const GLOBAL_ONLY = new Set([
  'system:principals', // users, groups, sites, grants
  'system:settings',   // settings, auth mode, notification channels, poll intervals
  'system:db',         // database stats and global purge
  'router:create',     // adding a router, setting the global active router
]);

// Scoped permissions, evaluated against a router or a site.
const SCOPED = new Set([
  'router:read',      // visible in lists, live data, dashboard/topology pages
  'router:ack',       // acknowledge alerts
  'router:history',   // historical reports and exports
  'router:diagnose',  // connection test, ping, firewall table selection
  'router:scan',      // wireless frequency scan — takes the radio off the air
  'router:write',     // RouterOS writes — reserved for issue #97, no call sites yet
  'router:manage',    // edit or delete a router, change its site
  'router:purge',     // purge that router's history
  'router:secrets',   // host, WAN IP, credential-adjacent fields
]);

// ── The page axis (issue #108) ───────────────────────────────────────────────
//
// A role is no longer one of three names with a hardcoded permission set; it is
// a row with a matrix of (page → read|write). These two tables project that
// matrix onto the action vocabulary above, so every existing requirePerm() call
// site keeps asking exactly the same question it asks today.
//
// Two axes rather than one, because the action permissions are not page-shaped
// and cannot be made so: router:read gates socket attachment and the router list
// for EVERY page, router:diagnose is reachable from two pages, and
// router:secrets is a field-level filter belonging to no page at all. A pure
// page:<name>:<access> vocabulary could not express any of those. Equally, the
// action vocabulary alone cannot express "read Logs but not DHCP", which is the
// feature. So: both, joined by this projection.
//
// Holding read on ANY page confers router:read — "this router is visible to you
// at all" is the precondition for a page, not a page in itself.
const READ_CONFERS = Object.freeze({
  reports: ['router:history'],   // historical reports and their CSV exports
});

const WRITE_CONFERS = Object.freeze({
  dashboard: ['router:ack'],                        // acknowledge alerts
  firewall:  ['router:diagnose'],                   // switch the shared firewall table
  wireless:  ['router:scan'],                       // frequency scan — disconnects every client on the radio
  routers:   ['router:manage'],                     // edit/delete a router, change its site
  settings:  ['system:settings', 'router:purge'],   // app settings; purge one router's history
});

// Any write row also confers router:write, which has no call sites yet — it is
// what issue #97 will gate RouterOS writes on. Conferring it now is what keeps
// the seeded Operator role exactly equal to today's, which holds it.
//
// Deliberately conferred by NO page: system:principals, system:db and
// router:create (stripped structurally below), and router:secrets, which is
// credential-adjacent and stays Administrator-only.
const WRITE_CONFERS_ALWAYS = Object.freeze(['router:write']);

const _ACCESS_RANK = Object.freeze({ read: 1, write: 2 });

const PERMISSIONS = [...GLOBAL_ONLY, ...SCOPED];

// Injected by init() rather than imported: asking settings for the auth mode
// from here would make the policy layer depend on the whole settings stack.
let _isModern = () => true;
function init({ isModern }) { if (typeof isModern === 'function') _isModern = isModern; }

// ── Cached per-user view ─────────────────────────────────────────────────────
// Resolving a user's grants is two indexed queries, but the auth middleware runs
// on every request, so the result is memoised behind a generation counter.
// Anything that can change an answer bumps the generation — including a router
// changing site, which is easy to forget because it looks like router config
// rather than authorization.

let _gen = 0;
const _views = new Map(); // userId → { gen, global: Set, bySite: Map<id,Set>, byRouter: Map<id,Set> }
const _defs  = new Map(); // roleId → { gen, builtin, perms: Set, pages: Map<page, access> }

/** Invalidate every cached view and role definition. Cheap, and correctness beats precision here. */
function bump() { _gen++; _views.clear(); _defs.clear(); }

/**
 * A role's resolved permission set and page matrix, memoised on the same
 * generation counter as _views. One lookup per ROLE, not per user — a fleet
 * sharing three roles costs three lookups per generation, not one per request.
 */
function _roleDef(roleId) {
  const hit = _defs.get(roleId);
  if (hit && hit.gen === _gen) return hit;

  const def = { gen: _gen, builtin: false, perms: new Set(), pages: new Map() };
  const row = db.getRole(roleId);
  // A grant referencing a role that no longer exists confers nothing. ON DELETE
  // RESTRICT should make that unreachable; failing closed anyway costs nothing.
  if (!row) { _defs.set(roleId, def); return def; }

  if (row.builtin) {
    // Administrator's reach is structural, not table-driven. A permission or a
    // page added in a later release is covered with no data migration — which
    // is what "allows access to everything as it currently does" has to mean.
    def.builtin = true;
    for (const p of PERMISSIONS) def.perms.add(p);
    for (const pg of Pages.KEYS) def.pages.set(pg, 'write');
  } else {
    for (const r of db.rolePages(roleId)) {
      def.pages.set(r.page, r.access);
      def.perms.add('router:read');
      for (const p of READ_CONFERS[r.page] || []) def.perms.add(p);
      if (r.access === 'write') {
        for (const p of WRITE_CONFERS_ALWAYS) def.perms.add(p);
        for (const p of WRITE_CONFERS[r.page] || []) def.perms.add(p);
      }
    }
    // Structural escalation firewall. No page may ever confer principal, database
    // or router-creation authority, whatever the projection tables above say.
    // This is what makes "system administration is Administrator-only" a property
    // of the resolver rather than of getting a table right. system:settings is the
    // single exception, and being in GLOBAL_ONLY it is still only satisfiable by a
    // grant held at global scope.
    for (const p of [...def.perms]) {
      if (GLOBAL_ONLY.has(p) && p !== 'system:settings') def.perms.delete(p);
    }
  }

  _defs.set(roleId, def);
  return def;
}

function _addTo(map, key, roleId) {
  let s = map.get(key);
  if (!s) { s = new Set(); map.set(key, s); }
  s.add(roleId);
}

/**
 * Every role a user holds, indexed by scope.
 *
 * This accumulates SETS rather than collapsing to one winning role. Custom roles
 * have no total order — one may grant read on Logs, another write on DHCP, and
 * neither dominates — so the old rank-based _stronger() could not express the
 * combination. Resolution is a union of permission sets, which is what additive
 * RBAC means and what #78's "most permissive wins" always intended; with three
 * ranked roles it merely happened to collapse into a maximum.
 */
function viewFor(userId) {
  const cached = _views.get(userId);
  if (cached && cached.gen === _gen) return cached;

  const view = { gen: _gen, global: new Set(), bySite: new Map(), byRouter: new Map() };
  for (const g of db.grantsForUser(userId)) {
    if (g.scope_type === 'global')      view.global.add(g.role_id);
    else if (g.scope_type === 'site')   _addTo(view.bySite,   g.scope_id, g.role_id);
    else if (g.scope_type === 'router') _addTo(view.byRouter, g.scope_id, g.role_id);
  }
  _views.set(userId, view);
  return view;
}

/** True if any of these roles confers the permission. */
function _anyConfers(roleIds, permission) {
  if (!roleIds) return false;
  for (const id of roleIds) if (_roleDef(id).perms.has(permission)) return true;
  return false;
}

/**
 * The role sets that apply to a target, or null if the target does not exist.
 * Factored so can() and canPage() walk scope identically and cannot drift apart.
 */
function _roleSetsInScope(view, t) {
  const out = [view.global];
  if (t.type === 'site') {
    const s = view.bySite.get(t.id);
    if (s) out.push(s);
    return out;
  }
  // A router inherits its site's grant. A router-scoped grant never confers
  // anything site-wide, which is why the site branch above does not consult
  // byRouter.
  const router = Routers.getById(t.id);
  if (!router) return null;
  if (router.siteId) {
    const s = view.bySite.get(router.siteId);
    if (s) out.push(s);
  }
  const r = view.byRouter.get(t.id);
  if (r) out.push(r);
  return out;
}

// ── The authorization function ───────────────────────────────────────────────

/**
 * @param {object|null} session   req.authSession, or socket.request._authSession
 * @param {string} permission     one of PERMISSIONS
 * @param {string|{type:'router'|'site',id:string}} [target]
 * @returns {boolean}
 */
function can(session, permission, target) {
  // In 'none' auth mode there is no identity and every request is implicitly
  // admin. This is the ONLY copy of that short circuit — it used to be repeated
  // in _requireAdmin, _routerPermitted and _scopeRouterId, three places to
  // forget it independently.
  if (!_isModern()) return true;
  if (!session || !session.userId) return false;

  const view = viewFor(session.userId);

  if (GLOBAL_ONLY.has(permission)) {
    // Deliberately ignores `target`: only roles held at global scope are
    // consulted, so no site or router grant can reach these — not even one
    // holding Administrator.
    return _anyConfers(view.global, permission);
  }

  if (!SCOPED.has(permission)) return false; // unknown permission: deny

  // Fail closed when the caller forgot the target. The old model's
  // `allowedRouterIds.length === 0` fallthrough meant "no restriction recorded"
  // granted everything; this is the same shape of bug, refused up front.
  if (target === null || target === undefined || target === '') return false;

  const t = (typeof target === 'string') ? { type: 'router', id: target } : target;
  if (!t || !t.id) return false;

  const sets = _roleSetsInScope(view, t);
  if (!sets) return false; // unknown router
  for (const ids of sets) if (_anyConfers(ids, permission)) return true;
  return false;
}

/**
 * Whether a session may see (or act on) a page for a target.
 *
 * @param {object|null} session
 * @param {string} page      one of Pages.KEYS
 * @param {'read'|'write'} access
 * @param {string|{type,id}} target   fail-closed if absent, exactly like can()
 *
 * Deliberately does NOT consult the install-wide Settings.pageX toggle. That is
 * a statement about the deployment, not about identity; the two are combined at
 * the enforcement points so each stays separately testable.
 */
function canPage(session, page, access = 'read', target) {
  if (!_isModern()) return true;
  if (!session || !session.userId) return false;
  if (!Pages.BY_KEY[page]) return false;           // unknown page: deny
  const need = _ACCESS_RANK[access] || 0;
  if (!need) return false;                          // unknown access level: deny
  if (target === null || target === undefined || target === '') return false;

  const t = (typeof target === 'string') ? { type: 'router', id: target } : target;
  if (!t || !t.id) return false;

  const sets = _roleSetsInScope(viewFor(session.userId), t);
  if (!sets) return false;
  for (const ids of sets) {
    for (const id of ids) {
      const held = _roleDef(id).pages.get(page);
      if (held && _ACCESS_RANK[held] >= need) return true;
    }
  }
  return false;
}

/**
 * Router ids the session can exercise `permission` on. Always a concrete array,
 * never a '*' sentinel — routers number in the dozens, and a concrete list
 * permanently removes the "empty means everything" ambiguity that the old
 * allowedRouterIds model turned on.
 */
function effectiveRouterIds(session, permission = 'router:read') {
  const all = Routers.loadAll().map(r => r.id);
  if (!_isModern()) return all.slice().sort();
  if (!session || !session.userId) return [];
  return all.filter(id => can(session, permission, id)).sort();
}

/** Site ids containing at least one router this session may read. */
function visibleSiteIds(session) {
  const ids = new Set();
  for (const id of effectiveRouterIds(session, 'router:read')) {
    const r = Routers.getById(id);
    if (r && r.siteId) ids.add(r.siteId);
  }
  return ids;
}

// ── Express middleware ───────────────────────────────────────────────────────

/**
 * Record a refused write.
 *
 * The refusal happens HERE, in middleware, before the route handler ever runs —
 * so a handler that records its own audit row cannot see it. Doing it once at
 * the guard covers every route uniformly, including ones added later.
 *
 * Only mutating methods: a GET refused is a permission working normally and
 * would bury the writes that matter under routine noise.
 *
 * Required lazily to keep the dependency one-way at load time; audit needs db,
 * db needs nothing, and rbac is used by both.
 */
function _auditDenied(req, permission) {
  if (!/^(POST|PUT|PATCH|DELETE)$/.test(req.method || '')) return;
  try {
    require('./audit').fromReq(req).denied({
      action: 'access.denied',
      targetType: 'route',
      targetName: req.method + ' ' + (req.baseUrl || '') + (req.route ? req.route.path : (req.path || '')),
      extra: { permission },
    });
  } catch (_) { /* a refusal must still be a refusal if bookkeeping fails */ }
}

function requireGlobalAdmin(req, res, next) {
  if (can(req.authSession, 'system:principals')) return next();
  _auditDenied(req, 'system:principals');
  return res.status(403).json({ ok: false, error: 'Administrator access required' });
}

/**
 * @param {string} permission
 * @param {function} [targetFn]  (req) => routerId | {type,id} | null
 */
function requirePerm(permission, targetFn) {
  return function (req, res, next) {
    const target = typeof targetFn === 'function' ? targetFn(req) : undefined;
    if (can(req.authSession, permission, target)) return next();
    _auditDenied(req, permission);
    return res.status(403).json({ ok: false, error: 'Not permitted' });
  };
}

/**
 * Express guard for a page permission, mirroring requirePerm.
 *
 * Note this does NOT apply the install-wide page toggle — a route is an API,
 * not a nav item, and turning a page off in Settings is a UI decision that
 * should not start returning 403 to something already holding the data.
 */
function requirePage(page, access, targetFn) {
  return (req, res, next) => {
    const target = targetFn ? targetFn(req) : undefined;
    if (canPage(req.authSession, page, access, target)) return next();
    _auditDenied(req, 'page:' + page + ':' + access);
    res.status(403).json({ ok: false, error: 'Not permitted' });
  };
}

/** True if the session may read this page on at least one router it can see. */
function canPageAnywhere(session, page, access = 'read') {
  if (!_isModern()) return true;
  for (const rid of effectiveRouterIds(session, 'router:read')) {
    if (canPage(session, page, access, rid)) return true;
  }
  return false;
}

const fromQuery = (key) => (req) => req.query  && req.query[key];
const fromParam = (key) => (req) => req.params && req.params[key];
const fromBody  = (key) => (req) => req.body   && req.body[key];

// ── Last-administrator protection ────────────────────────────────────────────

class _Probe extends Error {}

/**
 * Would applying `mutate` leave nobody holding admin at global scope?
 *
 * Users.adminCount() cannot answer this any more: it counts user records, and a
 * global admin grant can be held by a group whose membership it cannot see (and
 * an empty group confers nothing). The five ways to orphan the last
 * administrator — dropping the grant, emptying the group that holds it,
 * deleting that group, deleting that user, demoting yourself — all reduce to
 * this one question, asked inside a rolled-back transaction so the check cannot
 * drift from the operation it guards.
 */
function wouldOrphanGlobalAdmin(mutate) {
  const handle = db.open();
  let orphaned = false;
  try {
    handle.transaction(() => {
      mutate();
      orphaned = db.globalAdminUserIds().length === 0;
      throw new _Probe(); // always unwind: this is a question, not a change
    })();
  } catch (e) {
    if (!(e instanceof _Probe)) throw e;
  }
  return orphaned;
}

// ── Capabilities for the browser ─────────────────────────────────────────────

/**
 * What this session may do, resolved to plain booleans and id lists. The raw
 * grant graph is deliberately NOT sent — it would disclose other principals'
 * access to anyone who opened devtools.
 */
/**
 * This user's own access, resolved to names, for showing them what they hold.
 *
 * capsFor() below deliberately ships booleans and id lists and never names,
 * because it must not disclose how anyone ELSE's access is composed. This is a
 * different question with a different answer: it is viewFor(userId) — already
 * scoped to exactly one person — projected onto the names of the roles, sites
 * and routers that person holds. Nothing here is reachable for another user, so
 * it discloses nothing capsFor() was protecting.
 *
 * Roles held through a group appear automatically: grantsForUser already unions
 * group grants, which is the whole reason the legacy allowedRouterIds field
 * cannot answer this.
 */
function accessSummaryFor(userId) {
  const empty = { global: [], sites: [], routers: [] };
  if (!userId) return empty;
  const view = viewFor(userId);

  // A role, site or router can be deleted while a stale grant row survives until
  // the next sweep, so every lookup can miss. Drop those rather than rendering
  // "null" at somebody.
  const roleNames = (ids) => [...ids]
    .map(id => { const r = db.getRole(id); return r ? r.name : null; })
    .filter(Boolean)
    .sort();

  return {
    global: roleNames(view.global),
    sites: [...view.bySite].map(([siteId, roles]) => {
      const s = db.getSite(siteId);
      return { siteId, siteName: s ? s.name : null, roles: roleNames(roles) };
    }).filter(x => x.siteName),
    routers: [...view.byRouter].map(([routerId, roles]) => {
      const r = Routers.getById(routerId);
      return { routerId, routerLabel: r ? (r.label || r.host) : null, roles: roleNames(roles) };
    }).filter(x => x.routerLabel),
  };
}

function capsFor(session) {
  return {
    managePrincipals: can(session, 'system:principals'),
    manageSettings:   can(session, 'system:settings'),
    manageDb:         can(session, 'system:db'),
    createRouters:    can(session, 'router:create'),
    // Page access, unioned across every readable router: the nav is not
    // per-router, so this is what the first paint needs. The per-router answer
    // is authoritative and arrives over the socket. Still never the raw grant
    // graph, which would disclose every other principal's access.
    pages: (() => {
      const out  = {};
      const rids = effectiveRouterIds(session, 'router:read');
      for (const pg of Pages.KEYS) {
        let best = null;
        for (const rid of rids) {
          if (canPage(session, pg, 'write', rid)) { best = 'write'; break; }
          if (canPage(session, pg, 'read',  rid)) best = 'read';
        }
        if (best) out[pg] = best;
      }
      return out;
    })(),
    routers: {
      readable:    effectiveRouterIds(session, 'router:read'),
      manageable:  effectiveRouterIds(session, 'router:manage'),
      history:     effectiveRouterIds(session, 'router:history'),
      ackable:     effectiveRouterIds(session, 'router:ack'),
      diagnosable: effectiveRouterIds(session, 'router:diagnose'),
      scannable:   effectiveRouterIds(session, 'router:scan'),
    },
  };
}

// ── One-time migration from role + allowedRouterIds ──────────────────────────

/**
 * Convert the legacy per-user model into grants.
 *
 * The dangerous part is a semantic inversion: `allowedRouterIds: []` (or an
 * absent field) meant UNRESTRICTED — every router. The grant model is
 * deny-by-default. Reading [] as "no routers" would silently lock every user out
 * of everything, so it maps to a GLOBAL grant instead.
 *
 * Idempotent by construction: it does nothing once any grant exists.
 */
/**
 * Project one user's `role` + `allowedRouterIds` onto their grants.
 *
 * Grants are authoritative once enforcement is on, so the existing Users form —
 * which still submits a role and a router list — would otherwise silently stop
 * granting anything at all. This keeps that form working, and it is the same
 * mapping the migration uses, so the two cannot drift apart.
 *
 * Replaces the user's grants wholesale: a router removed from the list has to
 * lose its grant, not keep it.
 */
function syncUserGrants(user) {
  if (!user || !user.id) return 0;
  const role = user.role === 'admin'    ? 'admin'
             : user.role === 'operator' ? 'operator'
             : 'viewer';
  const liveRouterIds = new Set(Routers.loadAll().map(r => r.id));
  const ids = Array.isArray(user.allowedRouterIds) ? user.allowedRouterIds.filter(Boolean) : [];

  db.deleteGrantsForPrincipal('user', user.id);
  let made = 0;
  if (!ids.length) {
    // Empty means unrestricted — see the inversion note on migrateFromLegacy.
    db.upsertGrant({ principalType: 'user', principalId: user.id, role, scopeType: 'global' });
    made++;
  } else {
    for (const rid of ids) {
      if (!liveRouterIds.has(String(rid))) {
        console.warn('%s', `[rbac] dropping ${user.username}'s access to unknown router ${rid}`);
        continue;
      }
      db.upsertGrant({ principalType: 'user', principalId: user.id, role, scopeType: 'router', scopeId: String(rid) });
      made++;
    }
  }
  bump();
  return made;
}

function migrateFromLegacy() {
  if (db.listGrants().length) return { migrated: 0, skipped: true };

  // Sync, deliberately: listUsers() returns a Promise, and reading .length off
  // one yields undefined — the migration would report "0 users" and silently do
  // nothing, which is the worst possible failure for this particular function.
  const users = Users.listUsersSync();
  if (!users.length) return { migrated: 0, skipped: false };

  let made = 0, adminsSeen = 0;

  for (const u of users) {
    // Unknown roles map to viewer. That matches the RUNTIME behaviour of the old
    // code — every check was `role !== 'admin'` — even though createUser would
    // have written 'admin'. Least privilege is the right way to be wrong.
    if (u.role !== 'admin' && u.role !== 'viewer') {
      console.warn('%s', `[rbac] user ${u.username} had unrecognised role "${u.role}" — migrating as viewer`);
    }
    if (u.role === 'admin') adminsSeen++;
    made += syncUserGrants(u);
  }

  // Zero-lockout guard. A restricted admin can create users today, because
  // _requireAdmin never had router scoping. Under the new model they cannot, so
  // an install whose only admins were restricted would end up with nobody able
  // to administer anything. Promote them rather than stranding the deployment.
  if (db.globalAdminUserIds().length === 0 && adminsSeen > 0) {
    for (const u of users) {
      if (u.role !== 'admin') continue;
      db.upsertGrant({ principalType: 'user', principalId: u.id, role: 'admin', scopeType: 'global' });
      made++;
    }
    console.warn('%s', '[rbac] no unrestricted administrator existed; promoted every legacy admin to global scope');
  }

  console.log('%s', `[rbac] migrated ${users.length} user(s) to ${made} grant(s)`);
  bump();
  return { migrated: made, skipped: false };
}

module.exports = {
  init, can, viewFor, bump,
  effectiveRouterIds, visibleSiteIds,
  requireGlobalAdmin, requirePerm, fromQuery, fromParam, fromBody,
  wouldOrphanGlobalAdmin, capsFor, accessSummaryFor, migrateFromLegacy, syncUserGrants,
  canPage, requirePage, canPageAnywhere,
  PERMISSIONS, GLOBAL_ONLY, SCOPED, READ_CONFERS, WRITE_CONFERS,
};
