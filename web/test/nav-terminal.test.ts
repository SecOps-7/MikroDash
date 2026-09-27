/**
 * THE TERMINAL NAV ITEM WAS OFFERED WITH THE FEATURE SWITCHED OFF.
 *
 * `terminalEnabled` is off until an install turns it on, and the server derives
 * `terminalReady` from it and sends it with the page settings. The payload was
 * right from the first build. `applyPageVisibility` simply never asked about
 * that page: `byFeature` named `ai-agent` and nothing else, so the nav item was
 * visible on an install where every line would be refused.
 *
 * Found in a browser on 2026-09-27 with the whole suite green, which is the
 * shape this file exists to stop repeating - the sibling next door
 * (nav-ai-agent.test.ts) records the same failure one page over, where the
 * expression was correct and never evaluated.
 *
 * Driven through the real module for that reason: a test that called the
 * expression directly would have passed on the broken build.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const ENTRY = path.join(ROOT, 'testdata', '.nav-term-entry.ts');
fs.writeFileSync(ENTRY, "export { initCaps, applyCaps, applyPageVisibility } from '../web/src/caps.js';\n");
const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'nav-term.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const navItems: Record<string, { style: Record<string, string> }> = {};
global.document = {
  querySelectorAll: (sel: string) => {
    const m = /^\.nav-item\[data-page="([^"]+)"\]$/.exec(sel);
    if (!m) return [];
    if (!navItems[m[1]!]) navItems[m[1]!] = { style: {} };
    return [navItems[m[1]!]];
  },
  querySelector: () => null,
  getElementById: () => null,
  addEventListener: () => {},
  body: { classList: { add() {}, remove() {}, toggle() {} } },
} as never;
global.window = { addEventListener: () => {} } as never;
global.fetch = (() => new Promise(() => {})) as never;

const { initCaps, applyCaps, applyPageVisibility } = require(OUT);

initCaps({ current: () => 'dashboard', go: () => {}, serves: () => true });
// A role that grants everything, so ROLE never explains a hidden nav item and
// only `terminalReady` can.
applyCaps({ pages: { dashboard: true, terminal: true, firewall: true } });

const display = (k: string): string | undefined => navItems[k]?.style.display;

// THE SWEEP MUST VISIT IT AT ALL. The harness creates a nav item only when the
// sweep asks about that page, so an absent entry means it was never considered -
// indistinguishable on screen from "considered and left visible".
applyPageVisibility({ terminalReady: false });
assert.ok(navItems['terminal'],
  'the sweep never asked about terminal, so no setting can hide it. It is missing ' +
  'from ALL_NAV_PAGES in testdata/pages-table.json.');
say('ok  the visibility sweep considers the Terminal page');

assert.strictEqual(display('terminal'), 'none',
  'terminalReady false left the Terminal nav item visible - the page would offer an ' +
  'input that only ever refuses');
say('ok  terminalReady false hides it');

// BOTH DIRECTIONS. A sweep that hid it unconditionally would pass the assertion
// above and be just as wrong: the page would never appear once switched on.
applyPageVisibility({ terminalReady: true });
assert.strictEqual(display('terminal'), '',
  'terminalReady true did not bring the Terminal nav item back');
say('ok  terminalReady true shows it');

// ABSENT IS NOT READY. `terminalReady` is derived and always sent; a payload
// without it means an older server or a projection that dropped it, and this is
// the page where offering it anyway is least acceptable.
applyPageVisibility({ terminalReady: undefined });
assert.strictEqual(display('terminal'), 'none',
  'an absent terminalReady left the page visible; it must fail closed');
say('ok  an absent terminalReady fails closed');

// AND IT MUST NOT GATE ANYTHING ELSE. The two feature terms are ANDed in one
// expression, so a mistake there could hide every page but the terminal.
applyPageVisibility({ terminalReady: true, aiReady: true });
assert.strictEqual(display('firewall'), '',
  'control: Firewall should be visible, so the assertions above measure terminalReady ' +
  'rather than a sweep that hides everything');
say('ok  control: an ungated page stays visible');

say('nav-terminal: all checks passed');
