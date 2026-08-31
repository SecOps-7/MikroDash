'use strict';
/**
 * The app must become VISIBLE after a login.
 *
 * ---- THE DEFECT ------------------------------------------------------------
 *
 * `web/public/preflight.js` sets `documentElement.style.opacity = '0'` when
 * `sessionStorage.justLoggedIn` is set — the flag `login.js` writes on a
 * successful sign-in — so the app does not flash its default colours between the
 * redirect and the first paint. The live app fades it back in. THIS PORT DID
 * NOT, so after logging in the entire application rendered correctly and was
 * invisible. A plain reload showed it, because preflight only hides when the
 * flag is set, which makes it look intermittent.
 *
 * Reported by the operator on 2026-08-28 as "an empty page", after the login
 * screen and the root redirect had both been fixed. It is the FOURTH piece of
 * `public/app.js`'s global setup this port was missing — after the fetch guard,
 * the session verifier and the visibility recheck — and it was found by the
 * operator rather than by me, because I stopped reading that section of app.js
 * after the first two.
 *
 * ---- WHY A GATE, FOR THREE LINES ------------------------------------------
 *
 * Because the failure is total and silent. Nothing errors, nothing logs, every
 * request is 200, and the page is perfect and unseeable. There is no smaller
 * defect that costs more to diagnose.
 *
 *   node tools/login-fade-check.js
 */

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');
const problems = [];

// ---- The premise: preflight really does hide the document ------------------
// `web/src/entry/preflight.ts`, not `web/public/preflight.js`. The public copy was the
// LIVE REPO'S FILE, shipped verbatim, and was deleted on 2026-08-28 when the
// operator asked that the port "stand on its own without any lingering JS from
// the live repo". `tools/preflight-check.js` is what proved the port equivalent
// before the copy went; this gate asks the different question of whether the
// hide it performs still has a matching restore in `main.ts`.
const preflight = fs.readFileSync(
  path.join(ROOT, 'web', 'src', 'entry', 'preflight.ts'), 'utf8');
if (!/opacity\s*=\s*'0'/.test(preflight)) {
  problems.push('preflight.js no longer sets opacity to 0. If the hide is gone the restore below '
    + 'is dead code and this check is asserting nothing — delete both together, deliberately.');
}
if (!/justLoggedIn/.test(preflight)) {
  problems.push('preflight.js no longer keys the hide on justLoggedIn; the restore must follow it');
}

// ---- And login.js sets the flag the whole thing turns on -------------------
const login = fs.readFileSync(path.join(ROOT, 'web', 'src', 'entry', 'login.ts'), 'utf8');
if (!/sessionStorage\.setItem\(\s*'justLoggedIn'/.test(login)) {
  problems.push('login.js no longer sets justLoggedIn, so the hide never fires — again, that makes '
    + 'the restore dead code rather than wrong');
}

// ---- The restore itself ----------------------------------------------------
const main = fs.readFileSync(path.join(ROOT, 'web', 'src', 'main.ts'), 'utf8');
const body = main.replace(/\/\*[\s\S]*?\*\//g, ' ').replace(/\/\/[^\n]*/g, ' ');

if (!/sessionStorage\.getItem\(\s*'justLoggedIn'\s*\)/.test(body)) {
  problems.push('main.ts never reads justLoggedIn. After a login the app renders correctly and is '
    + 'INVISIBLE — every request 200, nothing logged, nothing to see.');
}
if (!/documentElement\.style\.opacity\s*=\s*'1'/.test(body)) {
  problems.push('main.ts never restores opacity to 1');
}
if (!/sessionStorage\.removeItem\(\s*'justLoggedIn'\s*\)/.test(body)) {
  problems.push('the flag is not cleared, so the fade would replay on every later load of the tab');
}

// ---- AND IT MUST BE EARLY ---------------------------------------------------
//
// Anything that throws before the restore leaves the operator looking at
// nothing, unable to tell a crashed app from a blank one. The live app puts it
// near the top of its file for exactly that reason.
const at = body.indexOf('async function main(): Promise<void> {');
if (at < 0) {
  problems.push('anchor lost: async function main() in main.ts');
} else {
  const inner = body.slice(at);
  const fadeAt = inner.indexOf("sessionStorage.getItem('justLoggedIn')");
  const awaitAt = inner.search(/\bawait\b|loadRouters\(|initCaps\(/);
  if (fadeAt >= 0 && awaitAt >= 0 && awaitAt < fadeAt) {
    problems.push('the opacity restore runs AFTER work that can throw or await. Anything failing '
      + 'first leaves the page invisible, which is indistinguishable from a crash.');
  }
}

if (problems.length) {
  console.error('login-fade-check FAILED:');
  for (const p of problems) console.error('  - ' + p);
  process.exit(1);
}
console.log('login-fade-check: preflight hides on justLoggedIn, login.js sets it, and main() '
  + 'clears the flag and restores opacity before anything that can throw');
