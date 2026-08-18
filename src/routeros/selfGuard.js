'use strict';
/**
 * The lockout guard for the Router Users page.
 *
 * MikroDash logs into every router it manages as an ordinary RouterOS user. The
 * Router Users page can edit RouterOS users. So the page can, in about ten
 * different ways, cut the dashboard off from the device it is managing — and
 * unlike every other write in this app, the failure is unrecoverable from
 * inside the app: once the login is broken, the fix is WinBox.
 *
 * That is why the decision lives in one pure module rather than inline in six
 * handlers. Every refusal is one function, testable without a router.
 *
 * ── What counts as fatal ──────────────────────────────────────────────────
 *
 *   remove / disable the account        no login
 *   rename it                           routers.json still holds the old name
 *   change its password                 routers.json still holds the old one
 *   expire its password                 next login is forced to change it
 *   move it to another group            the new group may lack `api` or `read`
 *   set an address restriction          may exclude MikroDash's source address
 *   set an inactivity timeout           disconnects on a timer
 *   edit / rename / remove ITS GROUP    dropping `api` or `read` disconnects it
 *   end its /user/active session        pointless churn; it just reconnects
 *
 * ── Two rules, not one ────────────────────────────────────────────────────
 *
 * The obvious rule protects the TARGET: our account and our group are never a
 * valid thing to act on. The second rule protects the VALUE: no other user may
 * be moved INTO our group, and no new user may be created with our name. That
 * one is not lockout, it is silent privilege escalation through a screen that
 * reads as low-stakes — a viewer with page-write could otherwise mint themself
 * an account in the group that holds `policy`.
 *
 * ── Deliberately blunt ────────────────────────────────────────────────────
 *
 * Editing our group's policy is refused outright rather than "refused if it
 * drops api or read". Deciding which policy edits are survivable means parsing
 * RouterOS's negation syntax and reasoning about implicit defaults — a second
 * guard, with its own bugs, protecting the first. The cost is that legitimate
 * edits to the MikroDash group must be made in WinBox, which the UI says.
 *
 * Likewise an `address` restriction is refused without asking whether the CIDR
 * would still admit us: MikroDash's source address as the router sees it is not
 * knowable from here (NAT, multi-homing, the container bridge).
 *
 * ── Fail closed ───────────────────────────────────────────────────────────
 *
 * If we cannot identify ourselves on this router at all, every write is
 * refused. Allowing everything because we cannot tell what is ours would be
 * exactly the accident this module exists to prevent.
 *
 * RouterOS has its own backstops — a user cannot grant policies it does not
 * itself hold, and the last full-access user cannot be removed. Those are
 * defence in depth. Nothing here relies on them.
 */

const _key = (v) => String(v == null ? '' : v).trim().toLowerCase();

/**
 * Work out which accounts and groups belong to MikroDash itself.
 *
 * MULTIPLE NAMES. `collectionFingerprint` (src/collection.js) does not cover
 * credentials, so `PUT /api/routers/:id` does not rebuild the session when the
 * username changes: the live connection can be logged in as one name while
 * routers.json holds another, indefinitely. Both are protected. Over-protecting
 * an account that is no longer ours is a nuisance; under-protecting the live one
 * costs somebody a site visit.
 *
 * THE GROUP COMES FROM /user/active. That row names the group the session which
 * actually authenticated landed in — true even when the /user row is absent
 * (RADIUS) or hidden from this API user. /user is only the fallback.
 *
 * Names are compared case-insensitively and trimmed. RouterOS names are
 * case-sensitive, so this over-matches a hypothetical second `mikrodash`; that
 * is the direction to err in.
 */
function resolveSelf(userRows, activeRows, usernames) {
  const names = new Set();
  for (const n of usernames || []) if (_key(n)) names.add(_key(n));

  const groups = new Set();
  let source = null;

  for (const r of activeRows || []) {
    if (r && r.name && names.has(_key(r.name)) && r.group) {
      groups.add(_key(r.group));
      source = 'active';
    }
  }
  if (!groups.size) {
    for (const r of userRows || []) {
      if (r && r.name && names.has(_key(r.name)) && r.group) {
        groups.add(_key(r.group));
        source = 'user';
      }
    }
  }

  return {
    names:  Array.from(names),
    groups: Array.from(groups),
    resolved: !!groups.size,
    source,
  };
}

const _ok = { ok: true,  code: null, detail: null };
const _no = (code, detail) => ({ ok: false, code, detail: detail || null });

function isSelfUser(self, name)   { return !!self && self.names.indexOf(_key(name))   !== -1; }
function isSelfGroup(self, group) { return !!self && self.groups.indexOf(_key(group)) !== -1; }

/**
 * A user action: add, set, remove, or any of the verbs that amount to set
 * (enable, disable, reset the password).
 *
 * `target` is the row as freshly read from the router — never the row the
 * browser sent, and never one from the collector's last payload. `values` is
 * what the action would write.
 */
function checkUser(self, { verb, target, values }) {
  if (!self || !self.resolved) return _no('self-unresolved');

  // Target side. `target` is absent on add, which is the value-side case below.
  if (target && isSelfUser(self, target.name)) {
    return _no('protected-account', target.name);
  }

  const v = values || {};
  // Value side: creating or renaming into our name, or moving anybody into our
  // group. Both are refused for every user, including ones that are not ours.
  if (v.name !== undefined && isSelfUser(self, v.name)) {
    return _no('protected-name-value', String(v.name));
  }
  if (v.group !== undefined && isSelfGroup(self, v.group)) {
    return _no('protected-group-value', String(v.group));
  }

  if (verb === 'remove' && !target) return _no('bad-request');
  return _ok;
}

/** A group action: add, set (including rename and policy edits), or remove. */
function checkGroup(self, { verb, target, values }) {
  if (!self || !self.resolved) return _no('self-unresolved');

  if (target && isSelfGroup(self, target.name)) {
    return _no('protected-group', target.name);
  }
  const v = values || {};
  // Renaming another group ONTO our group's name would make the protected name
  // ambiguous on the next read. RouterOS rejects the duplicate itself; the
  // refusal should still be ours, and legible.
  if (v.name !== undefined && isSelfGroup(self, v.name)) {
    return _no('protected-group-value', String(v.name));
  }

  if (verb === 'remove' && !target) return _no('bad-request');
  return _ok;
}

/**
 * Ending an active session.
 *
 * Every row whose name is ours is refused, whatever the `via` — MikroDash keeps
 * several logins open per router (the dashboard session, plus one each for
 * alerts and the routers overview), and they are all equally ours.
 */
function checkSession(self, { target }) {
  if (!self || !self.resolved) return _no('self-unresolved');
  if (!target) return _no('bad-request');
  if (isSelfUser(self, target.name)) return _no('protected-account', target.name);
  return _ok;
}

module.exports = {
  resolveSelf, checkUser, checkGroup, checkSession, isSelfUser, isSelfGroup,
};
