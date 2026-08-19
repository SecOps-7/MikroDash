'use strict';
/**
 * Would this firewall rule cut MikroDash off from the router it manages?
 *
 * ── Where this sits among the other four ──────────────────────────────────
 *
 *   selfGuard    REFUSES, fails CLOSED   protects usernames on /user
 *   queueGuard   warns,   fails OPEN     would this queue throttle us
 *   wanGuard     warns,   fails OPEN     is the management path local or remote
 *   selfPath     warns,   fails OPEN     which interface are we reachable on
 *   fwGuard      warns,   fails OPEN     could this RULE block our session
 *
 * The others ask about topology. This one asks about MATCHING, which is the
 * whole of what a firewall does, and it is the hazard issue #97 names first: a
 * bad input-chain rule locks this app out of the router, and the fix is WinBox.
 *
 * WARN, NEVER REFUSE. `chain=input action=drop` as the last line of a properly
 * ordered chain is not a mistake — it is the correct end of every hardened
 * firewall, and from here it is indistinguishable from the same rule placed
 * first, which locks everyone out. Refusing it would make the page useless to
 * exactly the people who most need a firewall. So it describes the consequence
 * and lets somebody who can see the whole chain decide.
 *
 * FAIL OPEN. Two reads have to succeed for this to answer at all, and
 * `/user/active` is denied to the read-only API user the README recommends.
 * That is the COMMON case. Failing closed would block every firewall edit on
 * every correctly hardened router.
 *
 * ── What it deliberately does not model ───────────────────────────────────
 *
 * ORDER. Whether a rule takes effect depends on every rule above it, and
 * evaluating that means writing a firewall simulator whose bugs would be
 * invisible. So the question here is narrower and answerable: *could this rule
 * match our management traffic at all* — and, symmetrically, *does the accept
 * rule being removed currently match it*. Those two cover the ways a single
 * edit locks you out. They do not cover a reorder that changes which of two
 * existing rules wins.
 *
 * Also not modelled: address lists, `jump` targets, layer7, time windows,
 * connection state, and negated matches. Each would be a source of confident
 * wrong answers. One honest warning beats a fake-precise one — every false
 * alarm trains the operator to click through the one that mattered.
 */

const { isInCidrs } = require('../util/ip');

/** Actions that stop a packet reaching the router. */
const BLOCKING = new Set(['drop', 'reject', 'tarpit']);

/**
 * The chain in each table that sees traffic addressed TO the router.
 *
 * `forward` is traffic passing THROUGH and cannot touch our session, which is
 * why the great majority of firewall edits raise nothing here. Tables absent
 * from this map cannot block us at all: mangle has no dropping action, and NAT
 * is not a filter.
 */
const TO_ROUTER_CHAIN = Object.freeze({
  '/ip/firewall/filter': 'input',
  '/ip/firewall/raw':    'prerouting',
});

// ── Matching ─────────────────────────────────────────────────────────────────

/**
 * Does an address spec cover any of our addresses?
 *
 * Three-valued. `null` means UNDECIDABLE — a range (`10.0.0.1-10.0.0.5`), a
 * negation (`!10.0.0.0/8`), an address-list name, anything isInCidrs cannot
 * parse. The caller treats undecidable as a match, because for a BLOCKING rule
 * the safe direction is to ask rather than to stay quiet.
 */
function addressCovers(spec, addresses) {
  const s = String(spec == null ? '' : spec).trim();
  if (!s) return true;                       // no source match: everything, including us
  if (s.startsWith('!') || s.indexOf('-') !== -1) return null;
  const addrs = (addresses || []).filter(Boolean);
  if (!addrs.length) return null;
  if (addrs.some(a => isInCidrs(a, [s]))) return true;
  // A spec isInCidrs could parse, that simply does not contain us. If it could
  // not parse it at all we cannot tell, and undecidable is not "no".
  return isInCidrs(addrs[0], [s + '']) === false && /^[0-9a-fA-F:.\/]+$/.test(s) ? false : null;
}

/** Does a RouterOS port spec — `443`, `80,443`, `1000-2000` — include `port`? */
function portCovers(spec, port) {
  const s = String(spec == null ? '' : spec).trim();
  if (!s) return true;                       // no port match: every port, including ours
  for (const part of s.split(',')) {
    const p = part.trim();
    if (!p) continue;
    const range = p.split('-');
    if (range.length === 2) {
      const lo = Number(range[0]), hi = Number(range[1]);
      if (Number.isFinite(lo) && Number.isFinite(hi) && port >= lo && port <= hi) return true;
    } else if (Number(p) === port) return true;
  }
  return false;
}

/**
 * Could this rule match the traffic that keeps MikroDash connected?
 *
 * Every clause has to hold. An empty field matches everything, which is why a
 * bare `chain=input action=drop` is the loudest case here — it matches on all
 * four counts.
 */
function matchesUs(rule, ctx) {
  if (addressCovers(rule.srcAddress, ctx.addresses) === false) return false;

  const proto = String(rule.protocol || '').trim().toLowerCase();
  if (proto && proto !== 'tcp') return false;              // the API is TCP

  if (!portCovers(rule.dstPort, ctx.apiPort)) return false;

  const inIf = String(rule.inInterface || '').trim().toLowerCase();
  if (inIf && !(ctx.interfaces || []).some(i => String(i).toLowerCase() === inIf)) return false;

  return true;
}

// ── Verdicts ─────────────────────────────────────────────────────────────────

function _fingerprint(what, menu, rule, ctx) {
  return JSON.stringify([
    String(what || ''), String(menu || ''),
    String(rule.chain || ''), String(rule.action || ''),
    String(rule.srcAddress || ''), String(rule.dstAddress || ''),
    String(rule.protocol || ''), String(rule.dstPort || ''), String(rule.inInterface || ''),
    (ctx.addresses || []).slice().sort(), ctx.apiPort,
  ]);
}

const _none = () => ({ level: 'none', code: null, detail: null, fingerprint: null });
const _off  = (v) => v === true || v === 'yes' || v === 'true';

/**
 * `ctx`    { resolved, addresses, interfaces, apiPort }
 * `menu`   the RouterOS table, e.g. '/ip/firewall/filter'
 * `values` the rule as it will be after the write
 * `before` the freshly-read row in resource field names, or null on create
 * `what`   'create' | 'update' | 'delete' | 'enable' | 'disable' | 'move'
 */
function checkRule({ ctx, menu, values, before, what }) {
  if (!ctx || !ctx.resolved) return _none();          // fail open
  const chain = TO_ROUTER_CHAIN[menu];
  if (!chain) return _none();                         // mangle and NAT cannot block us

  const removing = what === 'delete' || what === 'disable';
  // On removal the rule of interest is the one already there; otherwise it is
  // the one about to exist.
  const rule = removing ? (before || {}) : (values || {});
  if (String(rule.chain || '').toLowerCase() !== chain) return _none();
  if (!matchesUs(rule, ctx)) return _none();

  const act = String(rule.action || '').toLowerCase();
  const where = { chain, action: act, what,
                  address: (ctx.addresses || [])[0] || null,
                  interface: (ctx.interfaces || [])[0] || null,
                  port: ctx.apiPort };

  // 1. A rule that would stop our packets — created, edited into existence,
  //    enabled, or moved somewhere it may now win. A rule left disabled blocks
  //    nothing, so it only counts when it is on, or being switched on.
  if (!removing && BLOCKING.has(act) && !(_off(rule.disabled) && what !== 'enable')) {
    return { level: 'warn', code: 'self-lockout',
             detail: Object.assign({ kind: 'block' }, where),
             fingerprint: _fingerprint(what, menu, rule, ctx) };
  }

  // 2. The other half of #97's "an existing accept no longer matches": the rule
  //    letting us in is the one being taken away or moved.
  if ((removing || what === 'move') && act === 'accept' && !_off(before && before.disabled)) {
    return { level: 'warn', code: 'self-lockout',
             detail: Object.assign({ kind: 'accept-removed' }, where),
             fingerprint: _fingerprint(what, menu, rule, ctx) };
  }

  return _none();
}

module.exports = {
  checkRule, matchesUs, addressCovers, portCovers,
  BLOCKING, TO_ROUTER_CHAIN,
};
