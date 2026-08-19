'use strict';
/**
 * Where this router sees MikroDash connecting from.
 *
 * `/user/active` carries a source address per logged-in session, which is the
 * ROUTER'S OWN VIEW of us — past any NAT, past the container bridge, past
 * whatever the host thinks its address is. Nothing else available to this
 * process can answer that question.
 *
 * Two features need it, for opposite-looking reasons:
 *
 *   queueGuard   would a new simple queue throttle our own management traffic?
 *   wanGuard     would renewing this lease cut the path we manage through?
 *
 * It lives here rather than in either of them because both are guards and
 * neither owns the other. Note that selfGuard.js — the Router Users lockout
 * guard — deliberately does NOT use this: it runs in contexts where there is no
 * session to ask about, which is exactly why it concluded the address was
 * unknowable and had to protect by name instead.
 */

/**
 * MikroDash holds several logins per router at once (the dashboard session, the
 * alerter, the routers overview), and they need not share a source address, so
 * every distinct one is collected. Names are compared trimmed and lowercased —
 * over-matching is the safe direction for a guard.
 */
function resolveSelfAddresses(activeRows, usernames) {
  const names = new Set(
    (usernames || []).map(n => String(n == null ? '' : n).trim().toLowerCase()).filter(Boolean));
  const addresses = [];
  for (const r of activeRows || []) {
    if (!r || !r.name || !r.address) continue;
    if (!names.has(String(r.name).trim().toLowerCase())) continue;
    const a = String(r.address).trim();
    if (a && addresses.indexOf(a) === -1) addresses.push(a);
  }
  return { addresses, resolved: addresses.length > 0 };
}

module.exports = { resolveSelfAddresses };
