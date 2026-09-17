/**
 * THE AI AGENT NAV ITEM WAS NEVER SWEPT.
 *
 * `applyPageVisibility` gates that page on `aiReady` — derived server-side as
 * enabled AND an endpoint AND a model — so a page that can reach no model is not
 * offered. The branch was written, reviewed and shipped, and it never ran once:
 * the loop iterates `ALL_NAV_PAGES`, and `ai-agent` was not in it. Switching the
 * agent off, or clearing the endpoint, changed nothing in either direction.
 *
 * Reported from the running app on 2026-09-17, after the server side had been
 * verified live to broadcast `aiReady: false` correctly. Nothing was wrong with
 * the payload; the consumer never asked about that page.
 *
 * Driven through the real module, because the fault was precisely that a correct
 * expression was never evaluated — a test that called the expression directly
 * would have passed on the broken build.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const ENTRY = path.join(ROOT, 'testdata', '.nav-ai-entry.ts');
fs.writeFileSync(ENTRY, "export { initCaps, applyCaps, applyPageVisibility } from '../web/src/caps.js';\n");
const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'nav-ai.cjs');
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
// only `aiReady` can.
applyCaps({ pages: { dashboard: true, 'ai-agent': true, firewall: true } });

const display = (k: string): string | undefined => navItems[k]?.style.display;

// ── THE SWEEP MUST VISIT IT AT ALL ────────────────────────────────────────
//
// This is the assertion the original bug would have failed. The harness creates
// a nav item only when the sweep asks for that page, so an absent entry means
// the page was never considered — which is indistinguishable, on screen, from
// "considered and left visible".
applyPageVisibility({ aiReady: false });
assert.ok(navItems['ai-agent'],
  'the sweep never asked about ai-agent, so no setting can hide it. It is missing ' +
  'from ALL_NAV_PAGES in testdata/pages-table.json.');
say('ok  the visibility sweep considers the AI Agent page');

assert.strictEqual(display('ai-agent'), 'none',
  'aiReady false left the AI Agent nav item visible');
say('ok  aiReady false hides it');

// BOTH DIRECTIONS. A sweep that hid it unconditionally would pass the assertion
// above and be just as wrong: the page would never appear once configured.
applyPageVisibility({ aiReady: true });
assert.strictEqual(display('ai-agent'), '',
  'aiReady true did not bring the AI Agent nav item back');
say('ok  aiReady true shows it');

// ABSENT IS NOT READY. `aiReady` is derived and always sent; a payload without
// it means an older server or a projection that dropped it, and offering a page
// that cannot work is the failure this gate exists to prevent.
applyPageVisibility({ aiReady: undefined });
assert.strictEqual(display('ai-agent'), 'none',
  'an absent aiReady left the page visible; it must fail closed');
say('ok  an absent aiReady fails closed');

// THE CONTROL. Firewall is granted and ungated, so it must stay visible
// throughout — without this, a sweep that hid everything would pass all three.
assert.strictEqual(display('firewall'), '',
  'control: Firewall should be visible, so the assertions above measure aiReady ' +
  'rather than a sweep that hides everything');
say('ok  control: an ungated page stays visible');

say('nav-ai-agent: all checks passed');
