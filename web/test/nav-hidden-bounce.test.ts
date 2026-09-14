/**
 * MOVING OFF A PAGE THAT BECOMES HIDDEN.
 *
 * Two faults in `applyPageVisibility`, both pinned here:
 *
 *  1. It moved inside the nav loop, so when the current page came before every
 *     visible page it fell back to 'dashboard', even when the role denied
 *     Dashboard. It must go to the first page still reachable.
 *  2. It navigated with the default 'push', leaving the hidden page one Back
 *     away. A correction replaces the history entry.
 *
 * Driven through the real module: `initCaps` for the host, `applyCaps` for a role
 * that grants only Interfaces while the viewer is standing on Dashboard.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const ENTRY = path.join(ROOT, 'testdata', '.nav-bounce-entry.ts');
fs.writeFileSync(ENTRY, "export { initCaps, applyCaps } from '../web/src/caps.js';\n");
const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'nav-bounce.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

// One nav item per page the selector asks for, created on first ask.
const navItems = {};
global.document = {
  querySelectorAll: (sel) => {
    const m = /^\.nav-item\[data-page="([^"]+)"\]$/.exec(sel);
    if (!m) return [];
    if (!navItems[m[1]]) navItems[m[1]] = { style: {} };
    return [navItems[m[1]]];
  },
  querySelector: () => null,
  getElementById: () => null,
  addEventListener: () => {},
  body: { classList: { add() {}, remove() {}, toggle() {} } },
};
global.window = { addEventListener: () => {} };
// initCaps asks the server for the auth status; this answer never arrives, so
// only what the test applies decides the nav.
global.fetch = () => new Promise(() => {});

const { initCaps, applyCaps } = require(OUT);

const moves = [];
initCaps({
  current: () => 'dashboard',
  go: (page, mode) => { moves.push([page, mode]); },
  serves: () => true,
});

// A role that grants Interfaces and nothing else. Dashboard is first in the nav
// and is where the viewer stands.
applyCaps({ pages: { interfaces: true } });

assert.strictEqual(navItems.dashboard && navItems.dashboard.style.display, 'none',
  'setup: Dashboard should be hidden for a role that does not grant it');
assert.strictEqual(moves.length, 1, 'expected one move off the hidden page, got ' +
  JSON.stringify(moves));
assert.strictEqual(moves[0][0], 'interfaces',
  'the viewer was sent to ' + moves[0][0] + ', not to Interfaces, the only page the ' +
  'role allows. Deciding inside the nav loop fell back to Dashboard, which is hidden too.');
say('ok  a hidden current page moves to the first page still reachable');

assert.strictEqual(moves[0][1], 'replace',
  'the move used ' + moves[0][1] + '; a correction must replace the history entry, ' +
  'or Back returns to the page that was just hidden');
say('ok  the move replaces the history entry');
