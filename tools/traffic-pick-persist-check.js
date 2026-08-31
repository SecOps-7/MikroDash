'use strict';
/**
 * THE OPERATOR'S CHOSEN INTERFACE SURVIVES A RECONNECT.
 *
 * Upstream `d7548b0` (issue #119, second report): the operator picks ether2, a
 * socket reconnect happens, and they land back on the router's defaultIf. The
 * live fix records the pick and clears it at exactly ONE moment — a router
 * switch — never on connect.
 *
 * ---- WHY A GATE OF ITS OWN -------------------------------------------------
 *
 * Three gates already touch this area and NONE of them could catch the port
 * getting it wrong, which is why it was wrong:
 *
 *   traffic-pick-check.js     the RESTORE condition, as pure logic. Says nothing
 *                             about when the pick is cleared.
 *   reset-contract-audit.js   maps `_userPickedIf` -> `resetTraffic`. It records
 *                             the asymmetry IN A COMMENT and asserts nothing
 *                             about it, so a `resetTraffic()` on the connect
 *                             path is invisible to it.
 *   traffic-reset-check.js    which variables each moment clears — but the port
 *                             had ONE function for BOTH moments, so it could not
 *                             express a difference between them.
 *
 * The port cleared the pick on every reconnect for exactly that reason. Found by
 * reading the live repo's last two commits, not by any gate going red.
 *
 * ---- WHAT IT ASSERTS -------------------------------------------------------
 *
 * 1. LIVE: `_userPickedIf` is assigned '' at exactly one site, and that site is
 *    inside the router-switch handler, not a connect handler. If upstream ever
 *    clears it on connect too, this fails and the port's rule must be revisited
 *    rather than left as a stale copy.
 * 2. PORT: no `socket.on('connect')` handler anywhere reaches a function that
 *    clears `userPickedIf`.
 * 3. PORT: the router-switch path DOES clear it — otherwise "never cleared"
 *    would pass this gate while breaking the switch.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/traffic-pick-persist-check.js
 */

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const problems = [];

// ---- 1. the live rule, re-measured rather than assumed ---------------------
// PART 1 IS ENTIRELY A QUESTION ABOUT THE LIVE SOURCE — where does it clear
// `_userPickedIf`, and which socket handler owns that site. It establishes the
// premise the PORT's rule was copied from, and produces nothing part 2 consumes
// (checked: no identifier from this block is referenced below line 84).
//
// Once the reference is gone that premise is fixed and the question is
// unanswerable, so the block is guarded rather than frozen. Part 2, which checks
// the port, runs unconditionally and is where every mutation is caught.
const LIFT = require('./lib/lift.js');
const app = LIFT.liveSource(ROOT, path.join('public', 'app.js'));
const appLines = app.split('\n');
if (LIFT.hasReference(ROOT)) {
const clearSites = [];
appLines.forEach((l, i) => {
  // NOT the declaration. `var _userPickedIf = '';` matches a naive assignment
  // regex and is not a clear — it is where the variable comes from. Counting it
  // made this gate report two sites and name the wrong handler for one of them.
  if (/(?:^|[^.\w])_userPickedIf\s*=\s*''/.test(l) && !/\b(?:var|let|const)\s+_userPickedIf/.test(l)) {
    clearSites.push(i);
  }
});
if (clearSites.length !== 1) {
  problems.push(`live clears _userPickedIf at ${clearSites.length} sites (expected 1): lines ` +
    clearSites.map((i) => i + 1).join(', '));
}
for (const at of clearSites) {
  // The nearest enclosing handler, found by walking BACK to the closest
  // `socket.on('...')` — the same anchoring upstream's own test settled on,
  // because a fixed character window finds the wrong handler.
  let owner = null;
  for (let i = at; i >= 0 && i > at - 400; i--) {
    const m = appLines[i].match(/socket\.on\(\s*'([^']+)'/);
    if (m) { owner = m[1]; break; }
  }
  if (owner === 'connect') {
    problems.push(`live now clears _userPickedIf inside socket.on('connect') at line ${at + 1} — ` +
      'the port copies the OPPOSITE rule and must be revisited');
  }
  if (owner && !/switch/i.test(owner)) {
    problems.push(`live clears _userPickedIf inside socket.on('${owner}') at line ${at + 1}, ` +
      'which is neither a switch nor connect — the anchor has moved');
  }
}

}

// ---- 2. the port: nothing on the connect path clears the pick --------------
const SRC = path.join(ROOT, 'web', 'src');
function walk(dir, out) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) walk(p, out);
    else if (e.name.endsWith('.ts')) out.push(p);
  }
  return out;
}
const files = walk(SRC, []);

// Which port functions clear the pick, measured from their bodies.
const clearers = new Set();
for (const f of files) {
  const body = fs.readFileSync(f, 'utf8');
  const re = /export function (\w+)\s*\([^)]*\)\s*:\s*void\s*\{([\s\S]*?)\n\}/g;
  for (const m of body.matchAll(re)) {
    // Same exclusion as the live side: a declaration is not a clear.
    if (/(?:^|[^.\w])userPickedIf\s*=\s*''/.test(m[2]) &&
        !/\b(?:var|let|const)\s+userPickedIf/.test(m[2])) clearers.add(m[1]);
  }
}
if (clearers.size === 0) {
  problems.push('no port function clears userPickedIf — the router switch would carry a stale pick');
}

// Transitive: a function that CALLS a clearer is a clearer too.
for (let pass = 0; pass < 5; pass++) {
  for (const f of files) {
    const body = fs.readFileSync(f, 'utf8');
    const re = /export function (\w+)\s*\([^)]*\)\s*:\s*void\s*\{([\s\S]*?)\n\}/g;
    for (const m of body.matchAll(re)) {
      if (clearers.has(m[1])) continue;
      for (const c of clearers) {
        if (new RegExp(`\\b${c}\\s*\\(`).test(m[2])) { clearers.add(m[1]); break; }
      }
    }
  }
}

for (const f of files) {
  const body = fs.readFileSync(f, 'utf8');
  const rel = path.relative(ROOT, f).split(path.sep).join('/');
  for (const m of body.matchAll(/socket\.on\(\s*'connect'\s*,([\s\S]{0,400}?)\)\s*;/g)) {
    for (const c of clearers) {
      if (new RegExp(`\\b${c}\\s*\\(`).test(m[1])) {
        problems.push(`${rel}: a socket.on('connect') handler calls ${c}(), which clears ` +
          "userPickedIf. A reconnect is the same operator on the same router; their chosen " +
          'interface must survive it (upstream d7548b0, issue #119).');
      }
    }
  }
}

// ---- 3. the switch path really does clear it -------------------------------
const mainTs = fs.readFileSync(path.join(SRC, 'main.ts'), 'utf8');
let switchClears = false;
for (const c of clearers) {
  if (new RegExp(`\\b${c}\\s*\\(`).test(mainTs)) switchClears = true;
}
if (!switchClears) {
  problems.push('main.ts (the router-switch path) calls nothing that clears userPickedIf — ' +
    'a switch would carry the previous router\'s interface name across');
}

if (problems.length) {
  console.error('traffic-pick-persist: %d problem(s)\n', problems.length);
  for (const p of problems) console.error('  - %s', p);
  process.exit(1);
}
console.log('traffic-pick-persist: the pick survives a reconnect and is cleared on a router switch ' +
  // SAY WHICH HALF ACTUALLY RAN. "live clear site verified" printed whether or
  // not the guarded block executed, which is how a gate ends up reporting a
  // check it skipped.
  `(${clearers.size} clearing function(s), ` +
  (LIFT.hasReference(ROOT) ? 'live clear site verified' : 'live clear site NOT checked — no reference') + ')');
