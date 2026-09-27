/**
 * THE TERMINAL NAV ITEM IS GATED BY THE ROLE, AND BY NOTHING ELSE.
 *
 * It briefly had a feature flag of its own, derived from an install-wide
 * `terminalEnabled` switch, and that switch is gone: a page key in this app is
 * a permission key, so "may this person use the Terminal" already had an
 * answer and the flag was a second mechanism for the one job.
 *
 * Which leaves one thing to prove, and it is worth proving on this page above
 * all others: the sweep actually VISITS it. The failure this file's sibling
 * records (nav-ai-agent.test.ts) is a correct expression that is never
 * evaluated - `ALL_NAV_PAGES` not carrying the key, so no role, toggle or gate
 * can hide the item however right the rest of the code is. That shipped once
 * for the AI Agent and was found in a browser, with the suite green.
 *
 * Driven through the real module for that reason.
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

const display = (k: string): string | undefined => navItems[k]?.style.display;

// ── THE SWEEP MUST VISIT IT AT ALL ────────────────────────────────────────
//
// The harness creates a nav item only when the sweep asks about that page, so
// an absent entry means it was never considered - which is indistinguishable,
// on screen, from "considered and left visible".
applyCaps({ pages: { dashboard: true, firewall: true } });
applyPageVisibility({});
assert.ok(navItems['terminal'],
  'the sweep never asked about terminal, so no role can hide it. It is missing ' +
  'from ALL_NAV_PAGES in testdata/pages-table.json.');
say('ok  the visibility sweep considers the Terminal page');

assert.strictEqual(display('terminal'), 'none',
  'a role without the Terminal page still saw the nav item');
say('ok  no Terminal permission hides it');

// BOTH DIRECTIONS. A sweep that hid it unconditionally would pass the assertion
// above and be just as wrong: nobody could ever be granted the page.
applyCaps({ pages: { dashboard: true, firewall: true, terminal: true } });
applyPageVisibility({});
assert.strictEqual(display('terminal'), '',
  'a role WITH the Terminal page did not get the nav item');
say('ok  the Terminal permission shows it');

// AND NO SETTING CAN OVERRIDE THE ROLE in either direction. The install-wide
// switch is gone; a payload still carrying one must change nothing.
applyPageVisibility({ terminalReady: false, terminalEnabled: false });
assert.strictEqual(display('terminal'), '',
  'a stale terminalEnabled/terminalReady in the payload hid a page the role grants');
say('ok  a leftover setting in the payload does not override the role');

// THE CONTROL. Firewall is granted and ungated, so it must stay visible
// throughout - without this, a sweep that hid everything would pass the rest.
assert.strictEqual(display('firewall'), '',
  'control: Firewall should be visible, so the assertions above measure the ' +
  'Terminal grant rather than a sweep that hides everything');
say('ok  control: an ungated page stays visible');

say('nav-terminal: all checks passed');
