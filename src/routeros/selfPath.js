'use strict';
/**
 * Which interface carries MikroDash's own management traffic — the L2 guard.
 *
 * ── Where this sits among the other four ──────────────────────────────────
 *
 *   selfGuard    REFUSES, fails CLOSED   protects usernames on /user
 *   queueGuard   warns,   fails OPEN     would this queue throttle us
 *   wanGuard     warns,   fails OPEN     is the management path local or remote
 *   selfPath     warns,   fails OPEN     which INTERFACE are we reachable on
 *   fwGuard      warns,   fails OPEN     could a firewall rule block our session
 *
 * This one is the L2 question, and it is the one a VLAN or bridge-port edit
 * needs. `wanGuard` can already tell you that you are managing a router from
 * off-subnet; it cannot tell you that the port you are about to pull out of a
 * bridge is the port your packets arrive on.
 *
 * WARN, NEVER REFUSE. Pulling a port, disabling a VLAN, or renaming a bridge
 * are all ordinary things to do, and the one time they are catastrophic is
 * indistinguishable, from here, from the many times they are routine. Refusing
 * would make the Bridges page useless in order to prevent a mistake that a
 * sentence can prevent instead.
 *
 * FAIL OPEN. Two things have to be readable for this to answer at all —
 * `/user/active` and `/ip/address` — and `/user/active` is denied to the
 * read-only API user the README tells people to create. That is the COMMON
 * case. Failing closed would block every VLAN edit on every correctly hardened
 * router in order to guard against a mistake on the others.
 *
 * ── What it deliberately does not model ───────────────────────────────────
 *
 * Only the address the router sees us arriving on. Not the operator's own
 * browser path, not the route the reply takes, not bonding or failover. If
 * MikroDash reaches the router over a second link that survives the edit, this
 * still warns — over-warning on the L2 question is the safe direction, and the
 * warning names the address and the interface so the reader can judge it.
 */

const { resolveSelfAddresses } = require('./selfAddress');
const { isInCidrs } = require('../util/ip');

/**
 * The interfaces our management address sits behind.
 *
 * `addressRows` is `/ip/address/print`. An address is matched by SUBNET
 * containment, not equality: the router holds `10.0.0.1/24` and sees us at
 * `10.0.0.5`, so the question is which configured prefix contains us.
 *
 * Both `interface` and `actual-interface` are collected. They differ when an
 * address is configured on something RouterOS resolves elsewhere, and a guard
 * that knew only one of the two names would miss an edit aimed at the other.
 *
 * Returns `{ resolved: false }` when it cannot tell — the caller must read that
 * as "no warning", never as "no risk".
 */
function resolveManagementInterfaces({ activeRows, usernames, addressRows, selfAddresses }) {
  const self = selfAddresses || resolveSelfAddresses(activeRows, usernames);
  if (!self.resolved) return { resolved: false, interfaces: [], address: null, addresses: [] };

  const interfaces = [];
  let matched = null;

  for (const addr of self.addresses) {
    for (const row of addressRows || []) {
      if (!row || !row.address) continue;
      if (!isInCidrs(addr, [String(row.address)])) continue;
      matched = matched || addr;
      for (const name of [row.interface, row['actual-interface']]) {
        const n = String(name || '').trim();
        if (n && interfaces.indexOf(n) === -1) interfaces.push(n);
      }
    }
  }

  // We know where the router sees us from, but no configured prefix contains
  // it — so we arrive over a route rather than off a connected subnet, and no
  // single interface here is "the" management interface. That is wanGuard's
  // question, not this one.
  if (!interfaces.length)
    return { resolved: false, interfaces: [], address: self.addresses[0] || null, addresses: self.addresses };

  // `addresses` is every address the router sees us from, not just the one that
  // matched a prefix. fwGuard matches a rule against all of them: MikroDash
  // holds several logins per router and they need not share a source address.
  return { resolved: true, interfaces, address: matched, addresses: self.addresses };
}

/**
 * A stable identity for the exact inputs a verdict came from.
 *
 * Recomputed from a fresh read on the retry, so an acknowledgement cannot be
 * carried from one row to another or replayed against a different write. Same
 * idea as queueGuard's, applied to interface names.
 */
function _fingerprint(action, targets, path) {
  return JSON.stringify([
    String(action || ''),
    targets.slice().sort(),
    path.interfaces.slice().sort(),
    String(path.address || ''),
  ]);
}

const _none = () => ({ level: 'none', code: null, detail: null, fingerprint: null });

/**
 * Would this edit touch the interface we are reachable on?
 *
 * `targets` are the interface names the row is about — a resource declares
 * which of its fields those are, in `guardInterfaceFields`. `action` is
 * 'update' or 'delete'; both warn, because disabling a port and removing it cut
 * the same link.
 *
 * Names are compared trimmed and lowercased. RouterOS is case sensitive here,
 * so this over-matches slightly — the safe direction for a warning.
 */
function checkInterfaceEdit({ path, targets, action }) {
  if (!path || !path.resolved) return _none();            // fail open
  const want = (targets || [])
    .map(t => String(t == null ? '' : t).trim())
    .filter(Boolean);
  if (!want.length) return _none();

  const mine = new Set(path.interfaces.map(n => n.toLowerCase()));
  const hit = want.find(t => mine.has(t.toLowerCase()));
  if (!hit) return _none();

  return {
    level: 'warn',
    code: 'self-cutoff',
    detail: {
      interface: hit,
      address: path.address,
      action: action === 'delete' ? 'delete' : 'update',
    },
    fingerprint: _fingerprint(action, want, path),
  };
}

module.exports = { resolveManagementInterfaces, checkInterfaceEdit };
