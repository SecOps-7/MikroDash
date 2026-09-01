// Moved from `tools/fetch-guard-check.js` when the port-parity harness was retired.
//
// The body is VERBATIM apart from its imports and the path to the repository
// root: this test drives the port's OWN TypeScript and asserts what it does, so
// nothing in it referred to the implementation this app replaced.
/**
 * A 401 must send the browser to the login page.
 *
 * ---- THE DEFECT ------------------------------------------------------------
 *
 * `public/app.js` wraps `window.fetch` as the FIRST thing it does: a 401 in
 * modern auth mode redirects to `/login`, a 403 re-resolves permissions. This
 * port had no equivalent at all.
 *
 * What that cost, reported by the operator on 2026-08-28: with the SPA already
 * open, the server restarted, every in-memory session died, and every request
 * afterwards answered 401. The page sat there — NO LOGIN SCREEN, nothing
 * working. The document was never re-requested, so the server's own 302 on
 * `/next/` never came into it, and `main.ts` catches the `loadRouters()` failure
 * and logs "no routers are readable by this account", which reads as a
 * permissions problem rather than a dead session.
 *
 * ---- WHAT THIS PINS --------------------------------------------------------
 *
 * The behaviour, by DRIVING the wrapper — a source check would only prove the
 * file exists. Four cases, and the two that are easy to get wrong are the ones
 * that must NOT redirect:
 *
 *   401 + modern  -> /login
 *   403 + modern  -> refreshCaps, throttled
 *   401 + none    -> nothing (there is no login page to send anyone to)
 *   200           -> nothing, and the response is passed through unchanged
 *
 * Plus the ORDER, which is the half a behaviour test cannot see: the guard must
 * be installed before anything else in `main()`, because a request made before
 * the wrapper is a request whose 401 nobody sees.
 *
 *   node tools/fetch-guard-check.js
 */

import fs from 'node:fs';
import path from 'node:path';
import { execFileSync } from 'node:child_process';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const OUT = path.join(ROOT, 'testdata', '.fetchguard-port.cjs');
const problems = [];

execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'fetch-guard.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

// ---- Drive it --------------------------------------------------------------
function run(status, mode) {
  const calls = { redirects: [], refreshes: 0 };
  const g = globalThis;
  const savedFetch = g.fetch;
  const savedWindow = g.window;
  const savedMode = g._authMode;

  const responses = [];
  g.fetch = () => Promise.resolve({ status });
  g.window = { location: { set href(v) { calls.redirects.push(v); } } };
  g._authMode = mode;

  delete require.cache[require.resolve(OUT)];
  const m = require(OUT);
  m.installFetchGuard(() => { calls.refreshes++; });

  return g.fetch('/api/anything').then((res) => {
    responses.push(res);
    return g.fetch('/api/anything');           // second call, for the throttle
  }).then(() => {
    g.fetch = savedFetch; g.window = savedWindow; g._authMode = savedMode;
    return { ...calls, passedThrough: responses[0] && responses[0].status === status };
  });
}

(async () => {
  const modern401 = await run(401, 'modern');
  if (modern401.redirects[0] !== '/login') {
    problems.push(`401 in modern mode redirected to ${JSON.stringify(modern401.redirects[0])}, `
      + 'want /login — this is the case the operator hit');
  }

  const modern403 = await run(403, 'modern');
  if (modern403.refreshes !== 1) {
    problems.push(`403 in modern mode called refreshCaps ${modern403.refreshes} times across two `
      + 'requests; want exactly 1 — one denied page fires several requests and each must not '
      + 'trigger its own re-resolve');
  }
  if (modern403.redirects.length) {
    problems.push('403 redirected to the login page. It means "still signed in, but no longer '
      + 'permitted", and a redirect would misreport that as a session problem.');
  }

  // AUTH MODE none: there is no login page, and a redirect would loop.
  const none401 = await run(401, 'none');
  if (none401.redirects.length) {
    problems.push('401 redirected with authMode "none", where there is no login page to send '
      + 'anybody to and the redirect would loop');
  }

  const ok = await run(200, 'modern');
  if (ok.redirects.length || ok.refreshes) {
    problems.push('a 200 triggered a redirect or a refresh');
  }
  if (!ok.passedThrough) {
    problems.push('the response is not passed through unchanged; the guard adds a reaction, it '
      + 'does not swallow one');
  }

  // ---- The ORDER, which the behaviour above cannot see ---------------------
  const main = fs.readFileSync(path.join(ROOT, 'web', 'src', 'main.ts'), 'utf8');
  const at = main.indexOf('async function main(): Promise<void> {');
  if (at < 0) {
    problems.push('anchor lost: async function main() in main.ts');
  } else {
    const body = main.slice(at).replace(/\/\*[\s\S]*?\*\//g, ' ').replace(/\/\/[^\n]*/g, ' ');
    // THE FIRST STATEMENT, not merely an early one.
    //
    // "Before anything that fetches today" passed when the call was moved down
    // to sit just above `initCaps` — nothing between the two happens to fetch,
    // so the behaviour was unchanged and the mutation SURVIVED. That is a
    // weaker guarantee than the live app's, which installs the wrapper before
    // any code at all: it holds only until someone adds a fetch above it.
    //
    // So the property asserted is positional and absolute.
    const open = body.indexOf('{');
    const inner = body.slice(open + 1);
    const firstStatement = inner
      .split('\n')
      .map((l) => l.trim())
      .find((l) => l.length > 0);
    if (!firstStatement || !firstStatement.startsWith('installFetchGuard(')) {
      problems.push('installFetchGuard is not the FIRST statement of main() — it is '
        + JSON.stringify(String(firstStatement).slice(0, 60)) + '. The live app wraps fetch before '
        + 'any code runs; anything less holds only until someone adds a fetch above it.');
    }
  }

  // ---- The REFUSED HANDSHAKE path -----------------------------------------
  //
  // Separate from everything above, because the fetch guard cannot see it: the
  // WebSocket upgrade is auth-gated, so a dead session refuses every attempt,
  // `open` never fires, and no fetch is involved at all.
  async function verify(statusBody, mode, { fetchFails = false } = {}) {
    const g = globalThis;
    const saved = { fetch: g.fetch, window: g.window, mode: g._authMode };
    const redirects = [];
    let calls = 0;
    g.fetch = () => { calls++; return fetchFails
      ? Promise.reject(new Error('unreachable'))
      : Promise.resolve({ json: () => Promise.resolve(statusBody) }); };
    g.window = { location: { set href(v) { redirects.push(v); } } };
    g._authMode = mode;
    delete require.cache[require.resolve(OUT)];
    const m = require(OUT);
    m.verifySessionAfterFailure();
    m.verifySessionAfterFailure();          // second call, for the throttle
    await new Promise((r) => setTimeout(r, 20));
    g.fetch = saved.fetch; g.window = saved.window; g._authMode = saved.mode;
    return { redirects, calls };
  }

  const gone = await verify({ session: null }, 'modern');
  if (gone.redirects[0] !== '/login') {
    problems.push('a refused handshake with NO session did not redirect to /login — this is the '
      + 'case that leaves a tab on an empty dashboard until somebody reloads by hand');
  }
  if (gone.calls !== 1) {
    problems.push(`the session check ran ${gone.calls} times across two failures; reconnect `
      + 'attempts are frequent and each must not cost a request');
  }

  const alive = await verify({ session: { username: 'someone' } }, 'modern');
  if (alive.redirects.length) {
    problems.push('a refused handshake redirected while the session was still ALIVE — the server '
      + 'being briefly unreachable is not a session problem');
  }

  const down = await verify(null, 'modern', { fetchFails: true });
  if (down.redirects.length) {
    problems.push('the session check redirected when /api/auth/status itself failed. That means '
      + 'the SERVER is unreachable, which is what the reconnect banner is for.');
  }

  const noneMode = await verify({ session: null }, 'none');
  if (noneMode.redirects.length || noneMode.calls) {
    problems.push('the session check ran with authMode "none", where there is no login page');
  }

  fs.rmSync(OUT, { force: true });
  if (problems.length) {
    console.error('fetch-guard-check FAILED:');
    for (const p of problems) console.error('  - ' + p);
    process.exit(1);
  }
  console.log('fetch-guard-check: 401 redirects, 403 re-resolves once, authMode none is left '
    + 'alone, 200 passes through, and the guard is installed before anything fetches');
})();
