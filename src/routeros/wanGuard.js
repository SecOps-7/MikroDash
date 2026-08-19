'use strict';
/**
 * The self-cutoff warning for the WAN page.
 *
 * Renewing or releasing a DHCP lease drops that uplink for a few seconds. If
 * MikroDash manages the router THROUGH that uplink, the same click drops the
 * dashboard — and unlike a bad queue, you cannot undo it from the row that
 * caused it, because the row is no longer reachable.
 *
 * ── Same posture as queueGuard, and for the same reasons ──────────────────
 *
 * It WARNS, NEVER REFUSES. A remote admin with a stuck lease is exactly the
 * person who most needs to renew it; refusing would remove the feature at the
 * moment it matters. The warning names what will happen and lets them decide.
 *
 * It FAILS OPEN. If the source address cannot be resolved — `/user/active` is
 * denied to a read-only API user, which is the common case — no warning is
 * produced and the action proceeds. Failing closed would prompt on every
 * routine renew on the majority of installs, and a prompt nobody can dismiss
 * meaningfully is one they learn to click through.
 *
 * Do not read the filename as "guard = refuse". selfGuard.js refuses; this and
 * queueGuard.js warn.
 *
 * ── How "am I behind this WAN" is decided ─────────────────────────────────
 *
 * `/user/active` gives the source address the ROUTER sees us from — past NAT,
 * past the container bridge (see src/routeros/selfAddress.js). If that address
 * falls inside one of the router's own connected subnets, our packets never
 * traverse a WAN and no lease action can strand us. If it does not, our traffic
 * arrives over a route, and for a remotely-managed router that is the default
 * route.
 *
 * So the WAN carrying the ACTIVE default route is the lifeline, and it is the
 * only one worth warning about: releasing some other uplink cannot break the
 * path our packets are already taking.
 *
 * Containment uses isInCidrs from src/util/ip.js, which handles IPv4 and IPv6
 * and is already the app's answer to this question in connections.js and
 * bandwidth.js. queueGuard's cidrContains is deliberately NOT used — it is
 * IPv4-only and three-valued, which is right for judging a queue target typed
 * by a human and wrong for matching a real address against real subnets.
 */

const { isInCidrs } = require('../util/ip');

/**
 * Is MikroDash on one of this router's directly attached networks?
 *
 * `connectedCidrs` is every address the router holds, from /ip/address.
 *
 * If ANY of our session addresses is off-subnet we report remote, not local.
 * We hold several sessions per router and they need not share a path; the one
 * that would be cut is the one that matters, so the answer errs toward warning.
 */
function resolveManagementPath({ selfAddresses, connectedCidrs }) {
  const addrs = (selfAddresses && selfAddresses.addresses) || [];
  const cidrs = (connectedCidrs || []).filter(Boolean);
  if (!addrs.length) return { resolved: false, local: false, address: null };
  // With no connected subnets to compare against we know nothing, so say so
  // rather than concluding "not local" and warning on every action.
  if (!cidrs.length) return { resolved: false, local: false, address: null };

  const offSubnet = addrs.find(a => !isInCidrs(a, cidrs));
  return offSubnet
    ? { resolved: true, local: false, address: offSubnet }
    : { resolved: true, local: true,  address: addrs[0] };
}

/**
 * A stable identity for the inputs this verdict came from.
 *
 * Echoed back by the browser to acknowledge the warning, and recomputed from a
 * fresh read on the retry — so an acknowledgement cannot be replayed against a
 * different interface, or survive our path changing underneath it.
 */
function _fingerprint(targetWan, address, activeDefaultWan) {
  return JSON.stringify([String(targetWan || ''), String(address || ''), String(activeDefaultWan || '')]);
}

const _none = () => ({ level: 'none', code: null, detail: null, fingerprint: null });

/**
 * Would renewing or releasing this lease cut our own management path?
 *
 * `activeDefaultWan` is the interface carrying the active default route, or ''
 * when that cannot be determined — in which case any WAN might be the lifeline
 * and the warning applies to all of them.
 */
function checkLeaseAction({ path, targetWan, activeDefaultWan }) {
  if (!path || !path.resolved) return _none();     // fail open
  if (path.local) return _none();                  // cannot cut a directly attached session

  // Remote, but touching an uplink that is not carrying our traffic. Return
  // packets to us follow the active default route; another WAN's lease is not
  // on that path.
  if (activeDefaultWan && String(targetWan) !== String(activeDefaultWan)) return _none();

  return {
    level: 'warn',
    code: 'self-cutoff',
    detail: {
      address: path.address,
      wan: String(targetWan || ''),
      // False when we are warning because we could not identify the active
      // default route, rather than because this is demonstrably the one.
      certain: !!activeDefaultWan,
    },
    fingerprint: _fingerprint(targetWan, path.address, activeDefaultWan),
  };
}

module.exports = { resolveManagementPath, checkLeaseAction };
