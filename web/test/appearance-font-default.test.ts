/**
 * THE DEFAULT INTERFACE FONT (2026-09-21): Oxanium, which replaced Syne.
 *
 * applyFont stores the font on every load, so every browser that ever opened
 * MikroDash holds 'syne' whether or not anyone chose it. Three properties:
 *
 *   1. a new browser gets Oxanium;
 *   2. a browser holding the old default 'syne' moves to Oxanium, once;
 *   3. after that, a browser that chooses Syne keeps it.
 */

import path from 'node:path';
import fs from 'node:fs';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const out = path.join(ROOT, 'web', 'dist', '_compare', 'appearance-font.cjs');
fs.mkdirSync(path.dirname(out), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web/src/appearance.ts'), '--bundle', '--format=cjs', '--platform=node',
   '--outfile=' + out, '--log-level=warning'], { stdio: 'inherit' });

let store: Record<string, string> = {};
const g = globalThis as Record<string, unknown>;
g.localStorage = {
  getItem: (k: string) => (k in store ? store[k] : null),
  setItem: (k: string, v: string) => { store[k] = String(v); },
  removeItem: (k: string) => { delete store[k]; },
};
const props: Record<string, string> = {};
const attrs: Record<string, string> = {};
const docEl = {
  style: { setProperty: (k: string, v: string) => { props[k] = v; }, removeProperty: (k: string) => { delete props[k]; } },
  setAttribute: (k: string, v: string) => { attrs[k] = v; },
  getAttribute: (k: string) => attrs[k] ?? null,
  removeAttribute: (k: string) => { delete attrs[k]; },
};
g.document = {
  documentElement: docEl,
  getElementById: () => null,
  querySelector: () => null,
  querySelectorAll: () => [],
};
g.window = g;

// eslint-disable-next-line @typescript-eslint/no-var-requires
const A = require(out);

function boot(saved: Record<string, string>): { font: string | null; ui: string } {
  store = { ...saved };
  A.initAppearance();
  return { font: store.mikrodash_font ?? null, ui: props['--font-ui'] ?? '' };
}

let r = boot({});
assert.strictEqual(r.font, 'oxanium', 'a new browser did not get Oxanium');
assert.ok(r.ui.includes('Oxanium'), 'the interface font variable is not Oxanium: ' + r.ui);

r = boot({ mikrodash_font: 'syne' });
assert.strictEqual(r.font, 'oxanium', 'a browser holding the old default stayed on Syne');

r = boot({ mikrodash_font: 'syne', mikrodash_font_default: 'oxanium' });
assert.strictEqual(r.font, 'syne', 'a choice of Syne made after the change was overwritten');
assert.ok(r.ui.includes('Syne'), 'the chosen Syne is not what the page uses');

r = boot({ mikrodash_font: 'inter' });
assert.strictEqual(r.font, 'inter', 'a browser that chose another font lost it');

r = boot({ mikrodash_font: 'no-such-font' });
assert.strictEqual(r.font, 'oxanium', 'an unknown font did not fall back to the default');

console.log('appearance-font-default: all checks passed');
