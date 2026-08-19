'use strict';
/**
 * The self-throttle warning for the Queues page — and the rate-string parsing
 * that both the collector and the warning depend on.
 *
 * ── THIS IS NOT selfGuard ─────────────────────────────────────────────────
 *
 * It sits beside `selfGuard.js` and inverts it in both directions that matter.
 * Read this before assuming the sibling's rules apply:
 *
 *   selfGuard REFUSES.        queueGuard only ever WARNS.
 *   selfGuard FAILS CLOSED.   queueGuard FAILS OPEN.
 *
 * Both inversions are deliberate.
 *
 * WARN, NEVER REFUSE. selfGuard refuses because breaking the login is
 * unrecoverable from inside this app — the fix is WinBox. A queue that throttles
 * the dashboard is recoverable from the very row that created it, seconds later,
 * right here. And `target=10.0.0.0/24 max-limit=50M/50M` on the LAN that happens
 * to contain the dashboard is the single most ordinary queue anyone writes.
 * Refusing it would make the feature useless, which is a worse failure than the
 * one being prevented.
 *
 * FAIL OPEN. If we cannot work out our own address on this router, no warning is
 * produced and the write proceeds. `/user/active` being denied to the API user is
 * the COMMON case, not an edge one — the documented monitoring group denies
 * `policy` (see src/collectors/rosusers.js). Failing closed would block queue
 * creation on exactly those routers in order to prevent a slow dashboard.
 *
 * ── Deliberately blunt ────────────────────────────────────────────────────
 *
 * The warning does not reason about queue ORDER (first match wins, and modelling
 * that would be a second guard with its own bugs), nor `direction`, `time`
 * windows, or `dst-address` narrowing. One honest warning beats a fake-precise
 * one: every false alarm here trains the operator to click through the warning
 * that mattered.
 *
 * Queue TREES are not checked at all. A tree has no `target` — it matches
 * packet-marks under a parent — so it cannot be aimed at our address. That
 * removes half the surface for free.
 */

/** Below this, a queue covering our own address is worth mentioning. */
const SELF_THROTTLE_FLOOR_BPS = 1000000;

// Moved out when wanGuard became a second caller: two guards need to know where
// the router sees us from, and neither owns the other. Re-exported below so
// existing callers and tests are unaffected.
const { resolveSelfAddresses } = require('./selfAddress');

/**
 * Parse a RouterOS rate to bits per second.
 *
 * Over the API the router answers in raw bps ("15000000"), but it ACCEPTS the
 * CLI's suffixed form ("15M"), and an operator typing into the form will use
 * suffixes. Both are handled, so the same function reads a router response and
 * validates a browser submission.
 *
 * Returns null for absent/unparseable. Note that 0 is NOT null: RouterOS reads
 * an unlimited queue back as "0/0" rather than omitting the field, so zero means
 * "explicitly unlimited" and null means "no value at all". The page renders
 * those differently.
 */
function parseRate(raw) {
  if (raw === 0) return 0;
  if (raw === null || raw === undefined || raw === '') return null;
  const s = String(raw).trim();
  const m = /^(\d+(?:\.\d+)?)\s*([kKmMgG]?)$/.exec(s);
  if (!m) return null;
  const mult = { '': 1, k: 1e3, K: 1e3, m: 1e6, M: 1e6, g: 1e9, G: 1e9 }[m[2]];
  return Math.round(parseFloat(m[1]) * mult);
}

/**
 * Split a simple queue's `upload/download` pair.
 *
 * Simple queues express every limit and counter as a pair; queue trees use a
 * single value for the same fields. Passing a single value here yields the same
 * number in both halves, which is why tree callers read `.up` only.
 */
function parsePair(raw) {
  if (raw === null || raw === undefined || raw === '') return { up: null, down: null };
  const parts = String(raw).split('/');
  const up   = parseRate(parts[0]);
  const down = parts.length > 1 ? parseRate(parts[1]) : up;
  return { up, down };
}

// ── Address arithmetic ───────────────────────────────────────────────────────

/** Dotted quad to a 32-bit unsigned int, or null if it is not one. */
function _v4ToInt(ip) {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(String(ip || '').trim());
  if (!m) return null;
  let n = 0;
  for (let i = 1; i <= 4; i++) {
    const o = Number(m[i]);
    if (o > 255) return null;
    n = (n * 256) + o;
  }
  return n >>> 0;
}

/**
 * Does `cidr` contain `ip`?
 *
 * Three-valued on purpose. `null` means UNDECIDABLE — an interface-name target,
 * an IPv6/IPv4 mismatch, or anything unparseable — and an undecidable answer
 * must not be recorded as `false`, because the two have different meanings to a
 * reader even though both end in "no warning". Keeping them apart is what lets a
 * test prove which branch it exercised.
 */
function cidrContains(cidr, ip) {
  const raw = String(cidr || '').trim();
  if (!raw) return null;
  const slash = raw.split('/');
  const netPart = slash[0], bitsPart = slash[1];

  // IPv6 on either side: not attempted. Simple queues can hold v6 targets, and
  // getting v6 containment subtly wrong is worse than declining to answer.
  if (netPart.indexOf(':') !== -1 || String(ip).indexOf(':') !== -1) return null;

  const net  = _v4ToInt(netPart);
  const addr = _v4ToInt(ip);
  // An interface name ('WAN1', 'bridge') lands here and decides nothing.
  if (net === null || addr === null) return null;

  const bits = bitsPart === undefined ? 32 : Number(bitsPart);
  if (!Number.isInteger(bits) || bits < 0 || bits > 32) return null;
  if (bits === 0) return true;                       // 0.0.0.0/0 contains everything
  const mask = (0xFFFFFFFF << (32 - bits)) >>> 0;
  return ((net & mask) >>> 0) === ((addr & mask) >>> 0);
}

/**
 * A stable identity for the exact inputs a verdict came from.
 *
 * The browser echoes this back to acknowledge the warning. Recomputing it from a
 * fresh read means an acknowledgement cannot be carried from a mild queue to a
 * harsher one, or replayed against a different write — the same idea as
 * round-tripping a row's name, applied to a decision instead of a row.
 */
function _fingerprint(target, maxLimit, addresses) {
  return JSON.stringify([String(target || ''), maxLimit.up, maxLimit.down, addresses.slice().sort()]);
}

const _none = () => ({ level: 'none', code: null, detail: null, fingerprint: null });

/**
 * Would this simple queue throttle MikroDash's own connection?
 *
 * `values` is what the write would set: { target, maxLimit, disabled }.
 * `before` is the freshly-read row being edited, or null on create.
 *
 * On an edit the answer is only "warn" when the change makes things WORSE —
 * newly enabled, newly covering us, or a lower cap than before. Without that, a
 * comment-only edit on a long-standing throttling queue prompts every single
 * time, which is precisely how a warning becomes furniture.
 */
function checkSimpleQueue({ selfAddresses, values, before, floorBps }) {
  const addresses = (selfAddresses && selfAddresses.addresses) || [];
  if (!addresses.length) return _none();                    // fail open
  const v = values || {};
  if (v.disabled) return _none();                           // not in force

  const maxLimit = (v.maxLimit && typeof v.maxLimit === 'object') ? v.maxLimit : parsePair(v.maxLimit);
  const floor    = Number.isFinite(floorBps) ? floorBps : SELF_THROTTLE_FLOOR_BPS;

  // 0 means explicitly unlimited, which throttles nothing.
  const capped = [maxLimit.up, maxLimit.down].filter(n => typeof n === 'number' && n > 0 && n < floor);
  if (!capped.length) return _none();

  const hit = addresses.find(a => cidrContains(v.target, a) === true);
  if (!hit) return _none();

  if (before) {
    const beforeMax = (before.maxLimit && typeof before.maxLimit === 'object')
      ? before.maxLimit : parsePair(before.maxLimit);
    const wasCovering = cidrContains(before.target, hit) === true;
    const wasEnabled  = !before.disabled;
    const lower = (a, b) => (typeof a === 'number' && a > 0) &&
                            (typeof b !== 'number' || b <= 0 || a < b);
    const gotWorse = !wasEnabled || !wasCovering ||
                     lower(maxLimit.up, beforeMax.up) || lower(maxLimit.down, beforeMax.down);
    if (!gotWorse) return _none();
  }

  return {
    level: 'warn',
    code:  'self-throttle',
    detail: { address: hit, target: String(v.target || ''), maxLimit },
    fingerprint: _fingerprint(v.target, maxLimit, addresses),
  };
}

module.exports = {
  parseRate, parsePair, cidrContains, resolveSelfAddresses, checkSimpleQueue,
  SELF_THROTTLE_FLOOR_BPS,
};
