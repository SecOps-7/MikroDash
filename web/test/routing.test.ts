// The address bar, and the three ways it can lie.
//
// `web/src/routing.ts` is pure apart from `window.history` and
// `window.location`, so this stubs both and asserts what it writes. That is the
// whole point of keeping the history logic in one small module: the interesting
// behaviour is testable without mounting the app.

import test from 'node:test';
import assert from 'node:assert';
import path from 'node:path';
import { execFileSync } from 'node:child_process';

const REPO = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const OUT = path.join(REPO, 'web', 'test-out', 'routing.bundle.cjs');

execFileSync(path.join(REPO, 'web', 'node_modules', '.bin', 'esbuild'), [
  path.join(REPO, 'web', 'src', 'routing.ts'),
  '--bundle', '--format=cjs', '--platform=node', '--log-level=error', '--outfile=' + OUT,
], { cwd: REPO });

/** A stand-in for the browser's history and location. */
function browser(pathname: string) {
  const writes: Array<{ how: string; to: string }> = [];
  const listeners: Array<() => void> = [];
  (globalThis as any).window = {
    location: { get pathname() { return pathname; } },
    history: {
      pushState: (_s: unknown, _t: string, to: string) => { writes.push({ how: 'push', to }); pathname = to; },
      replaceState: (_s: unknown, _t: string, to: string) => { writes.push({ how: 'replace', to }); pathname = to; },
    },
    addEventListener: (ev: string, fn: () => void) => { if (ev === 'popstate') listeners.push(fn); },
  };
  return {
    writes,
    go: (to: string) => { pathname = to; listeners.forEach((f) => f()); },
    at: () => pathname,
  };
}

test('a navigation adds one history entry, and only one', () => {
  const b = browser('/logs');
  const r = require(OUT);
  r.sync('firewall', 'push');
  assert.deepStrictEqual(b.writes, [{ how: 'push', to: '/firewall' }]);
});

test('re-showing the page already open writes nothing', () => {
  // select() re-runs showPage with the SAME page on every socket reconnect. If
  // that pushed, a flaky link would fill the back button with duplicates.
  const b = browser('/firewall');
  const r = require(OUT);
  r.sync('firewall', 'push');
  r.sync('firewall', 'push');
  assert.deepStrictEqual(b.writes, [], 'a reconnect must not touch history');
});

test('a correction replaces rather than pushes', () => {
  // Booting at a page the operator may not see: pushing would leave the
  // forbidden page as a back target and bouncing between the two.
  const b = browser('/settings');
  const r = require(OUT);
  r.sync('dashboard', 'replace');
  assert.deepStrictEqual(b.writes, [{ how: 'replace', to: '/home' }]);
});

test('the dashboard is served at /home, not /dashboard', () => {
  const b = browser('/');
  const r = require(OUT);
  r.sync('dashboard', 'push');
  assert.strictEqual(b.at(), '/home');
  assert.strictEqual(r.pageForURL('/home'), 'dashboard');
  assert.strictEqual(r.pageForURL('/dashboard'), '', 'one page, one path');
});

test('a deep link decides the first page, and an unknown one falls back', () => {
  const known = new Set(['dashboard', 'logs', 'firewall']);
  browser('/logs');
  let r = require(OUT);
  assert.strictEqual(r.initialPage(known, 'dashboard'), 'logs');

  browser('/nothing-here');
  assert.strictEqual(r.initialPage(known, 'dashboard'), 'dashboard');

  // A real page this BUILD cannot render -- a link from a newer version, or a
  // rolling deploy. Landing home beats rendering nothing.
  browser('/reports');
  assert.strictEqual(r.initialPage(known, 'dashboard'), 'dashboard');
});

test('back and forward navigate, and never re-fire the page already shown', () => {
  // THE DOUBLE-FIRE HAZARD, asserted directly. `mikrodash:pagechange` carries no
  // "did it change" guard and ~20 page modules re-run their entry logic on it,
  // so a popstate landing on the current page must call nothing at all.
  const b = browser('/logs');
  const r = require(OUT);
  const seen: string[] = [];
  let current = 'logs';
  r.initRouting((key: string) => { seen.push(key); current = key; }, () => current);

  b.go('/firewall');
  assert.deepStrictEqual(seen, ['firewall'], 'back should navigate once');

  b.go('/firewall');
  assert.deepStrictEqual(seen, ['firewall'], 'a popstate to the page already shown must do nothing');

  b.go('/nothing-here');
  assert.deepStrictEqual(seen, ['firewall'], 'a path naming no page must not navigate');
});

test('skip never writes, because the browser already moved', () => {
  const b = browser('/logs');
  const r = require(OUT);
  r.sync('firewall', 'skip');
  assert.deepStrictEqual(b.writes, []);
});
