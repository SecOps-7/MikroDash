'use strict';
/**
 * THE STATUS IS RECORDED EVEN WHEN IT IS NOT PAINTED.
 *
 * `updateRouterStatusBadge` refuses to paint a DISABLED row (upstream `d7529e0`).
 * There are two ways to implement that and only one is right:
 *
 *   RIGHT  record the status, skip the paint. Re-enabling the router re-renders
 *          from `routerStatus` and shows the state it actually had.
 *   WRONG  skip both. Re-enabling shows an em dash until the next status event,
 *          which for a healthy router can be a whole poll interval away.
 *
 * ---- WHY THIS IS NOT IN settings-routers-check.js -------------------------
 *
 * That gate drives `updateRouterStatusBadge` and reads badge text. It is handed
 * the status map; it does not own it. So it would pass unchanged against a port
 * that stopped recording entirely — the failure is one module up, in `main.ts`,
 * where `router:status` writes `routerStatus[id]` and then paints.
 *
 * The live-repo agent found this by re-enabling a router in a browser and
 * watching for an intermediate dash — the only observable that distinguishes the
 * two implementations, and one a screenshot of the disabled state cannot show.
 * This is the source-level equivalent, because the port has no browser gate.
 *
 * ---- WHAT IT ASSERTS ------------------------------------------------------
 *
 * In the `router:status` handler: the write to `routerStatus` happens, is
 * UNCONDITIONAL, and comes BEFORE the paint call. Order matters — recording
 * after a call that can return early is the same bug wearing a different hat.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/status-record-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');
const MAIN = path.join(ROOT, 'web', 'src', 'main.ts');
const src = fs.readFileSync(MAIN, 'utf8');
const problems = [];

// ── THE HANDLER THAT OWNS THE CONCERN, NOT THE FIRST ONE NAMED ────────────
//
// main.ts subscribes `router:status` TWICE: once room-scoped for the session
// banner, once fleet-wide for the Settings table. `indexOf` finds the banner
// one, whose body mentions neither `routerStatus` nor the badge — so the first
// version of this audit reported both properties missing and would have sent
// somebody looking for a bug that was not there.
//
// Selected by CONTENT: the handler whose body writes `routerStatus`. Exactly the
// mistake the live-repo agent hit with four `router:switching` handlers, and the
// same remedy.
const bodies = [];
for (let i = src.indexOf("socket.on('router:status'"); i >= 0;
     i = src.indexOf("socket.on('router:status'", i + 1)) {
  const rest = src.slice(i);
  const endRel = rest.indexOf('\n  });');
  bodies.push(endRel < 0 ? rest.slice(0, 2000) : rest.slice(0, endRel));
}
if (!bodies.length) {
  console.error("status-record-audit: no socket.on('router:status') in main.ts — " +
                'this audit is measuring nothing');
  process.exit(1);
}
const owning = bodies.filter((b) => /routerStatus\[/.test(b));
if (owning.length !== 1) {
  console.error('status-record-audit: %d of %d router:status handlers write routerStatus; ' +
                'expected exactly one to own it', owning.length, bodies.length);
  process.exit(1);
}
const body = owning[0];

const record = body.search(/routerStatus\[[^\]]+\]\s*=/);
const paint = body.search(/updateRouterStatusBadge\s*\(/);

if (record < 0) {
  problems.push('the router:status handler never writes routerStatus[...]. A disabled ' +
    'row would then re-render as an em dash after being re-enabled, because nothing ' +
    'remembered the state it had while it was not being painted.');
}
if (paint < 0) {
  problems.push('the router:status handler never calls updateRouterStatusBadge — the ' +
    'Settings table would keep whatever status it was rendered with.');
}
if (record >= 0 && paint >= 0 && record > paint) {
  problems.push('routerStatus is written AFTER updateRouterStatusBadge. The paint call ' +
    'returns early for a disabled row, and a record placed after it is a record that ' +
    'does not happen — the same defect with the statements swapped.');
}

// UNCONDITIONAL. A record inside an `if` is a record with a way not to happen.
if (record >= 0) {
  const line = body.slice(body.lastIndexOf('\n', record) + 1, body.indexOf('\n', record));
  if (/^\s*(if|\}\s*else)\b/.test(line) || /\?\s*[^:]*:/.test(line)) {
    problems.push('routerStatus is written conditionally (`' + line.trim() + '`). ' +
      'Whether the state is remembered must not depend on whether it is displayed.');
  }
}

if (problems.length) {
  console.error('status-record-audit: %d problem(s)\n', problems.length);
  for (const p of problems) console.error('  - %s', p);
  process.exit(1);
}
console.log('status-record-audit: router:status records before it paints, unconditionally');
