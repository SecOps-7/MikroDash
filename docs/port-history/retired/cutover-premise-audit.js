'use strict';
/**
 * The remaining cutover blockers rest on facts about the LIVE source. This
 * asserts those facts are still true.
 *
 * ---- THE DIRECTION NOBODY WATCHES -----------------------------------------
 *
 * Every blocker in PORT-QUEUE.md says "this cannot be done while Node runs
 * because <fact about src/>". Each fact was checked once, by hand, on a date.
 * Nothing fails when one stops being true — and a blocker that has quietly
 * become false is work deferred for no reason, which is the same shape as
 * `faSpectrum`'s deferral ("it lands with the other Chart.js work") outliving
 * the work it was waiting for.
 *
 * `tools/coexistence-audit.js` already covers the FILE-CACHE class — settings,
 * routers and users, all `if (_cache) return _cache;`. This covers the other
 * class: IN-MEMORY STATE TWO PROCESSES CANNOT SHARE.
 *
 * A failure here is good news. It means a blocker may be closeable, and the
 * entry has to be re-read rather than inherited.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/cutover-premise-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..', '..', '..');
const LIFT = require('../../../tools/lib/lift.js');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const read = (f) => LIFT.liveSource(ROOT, path.join('src', f));

const problems = [];
const held = [];

/** Assert a premise, naming the blocker it holds up. */
function premise(blocker, why, ok) {
  if (ok) { held.push(blocker); return; }
  problems.push(`${blocker}: ${why}`);
}

// ── Blocker 6 — the principal writes ────────────────────────────────────────
//
// Node's Rbac memoises per-user views and per-role definitions in Maps cleared
// only by its own bump(). A Go write to `grants` would leave Node honouring a
// revoked grant until it restarted, because nothing this side can advance that
// counter.
{
  const rbac = read('rbac.js');
  premise('principal writes (blocker 6)',
    '`_views` and `_defs` are no longer in-memory Maps — if they moved to the database, or '
    + 'gained a TTL, a Go write might become visible to Node and the blocker could close',
    /const\s+_views\s*=\s*new Map\(\)/.test(rbac) && /const\s+_defs\s*=\s*new Map\(\)/.test(rbac));

  premise('principal writes (blocker 6)',
    '`bump()` no longer clears both caches — the invalidation shape this blocker describes has '
    + 'changed and the entry must be re-read',
    /function bump\(\)\s*\{[^}]*_views\.clear\(\)[^}]*_defs\.clear\(\)[^}]*\}/.test(rbac));

  // A TIME-BASED EXPIRY WOULD CHANGE THE ANSWER. Not "would fix it" — a stale
  // grant honoured for sixty seconds is different from one honoured until
  // restart, and the entry says "no time-based expiry" as a fact.
  const expiry = /_views[\s\S]{0,200}(setTimeout|setInterval|Date\.now\(\)\s*-\s*\w*(ttl|TTL|expiry))/;
  premise('principal writes (blocker 6)',
    'something time-based now appears near `_views`; the entry states there is NO time-based '
    + 'expiry, and that is what makes a revoked grant survive until restart',
    !expiry.test(rbac));
}

// ── Blocker 5 — the notification transports ─────────────────────────────────
//
// Wiring them while Node runs would send every notification TWICE: both apps
// evaluate the same conditions against the same routers, and the cooldown is an
// in-memory Map rather than a shared row, so neither engine sees the other's
// sends. Unlike a reverted write, a duplicated Telegram message cannot be
// un-received.
{
  const alerter = read('alerter.js');
  premise('notification transports (blocker 5)',
    'the delivery cooldown is no longer a plain in-memory Map — if it moved to the shared '
    + 'database, two engines WOULD see each other\'s sends and the duplicate-send argument '
    + 'weakens',
    /cooldownMap\.get\(/.test(alerter) && /cooldownMap\.set\(/.test(alerter));

  premise('notification transports (blocker 5)',
    'the cooldown map is now read from or written to SQLite; the blocker assumes it is '
    + 'per-process',
    !/cooldownMap[\s\S]{0,300}(db\.|prepare\(|SELECT|INSERT)/i.test(alerter));
}

// ── The audit must have read something ──────────────────────────────────────
if (held.length + problems.length < 5) {
  problems.push(`only ${held.length + problems.length} premises were evaluated; there were 5 when `
    + 'this was written, so a check has been lost');
}

if (problems.length) {
  console.error('cutover-premise-audit FAILED — a blocker\'s premise may no longer hold:');
  for (const p of problems) console.error('  - ' + p);
  console.error('\n  This is not a regression. Re-read the entry in PORT-QUEUE.md: the work may '
    + 'now be closeable, or the reason may have changed.');
  process.exit(1);
}
console.log(`cutover-premise-audit: ${held.length} premises still hold across `
  + `${new Set(held).size} blockers`);
