/**
 * EACH GENERATED PAGE'S NAV ENTRY WEARS ITS OWN ICON (2026-09-18).
 *
 * `mountAreaNav` composed every area's entry with one placeholder table icon, so
 * sixteen pages looked alike in the collapsed nav. The icon is now declared with
 * the area in `internal/areas` and generated into `gen/areas.ts`; this drives the
 * real `mountAreaNav` and asserts that every entry it mounts renders ITS area's
 * icon, and that no two entries render the same picture. A regression to one
 * shared icon fails the second check even if the table still carries sixteen.
 *
 * The Go side (`TestEveryAreaHasItsOwnIcon`) holds the declarations; this holds
 * the wiring from declaration to nav entry.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.area-icons-entry.ts');
fs.writeFileSync(ENTRY,
  "export { mountAreaNav } from '../web/src/pages/area.js';\n" +
  "export { AREAS } from '../web/src/gen/areas.js';\n");
const OUT = path.join(ROOT, 'testdata', '.area-icons.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const mod = require(OUT);
fs.rmSync(OUT, { force: true });

// A document with every nav group body, and nothing else: mountAreaNav asks for
// an existing entry (none), the group, and creates an anchor per area.
const groups = {};
global.document = {
  querySelector(sel) {
    const g = /^#navgrp-(.+)$/.exec(sel);
    if (g) return (groups[g[1]] ||= { kids: [], appendChild(n) { this.kids.push(n); } });
    return null;
  },
  createElement() {
    return { attrs: {}, innerHTML: '', setAttribute(k, v) { this.attrs[k] = String(v); } };
  },
};

mod.mountAreaNav();
const entries = Object.values(groups).flatMap((g) => g.kids);
assert.ok(mod.AREAS.length > 0, 'no areas in the generated table; this check would pass on nothing');
assert.strictEqual(entries.length, mod.AREAS.length,
  'mounted ' + entries.length + ' nav entries for ' + mod.AREAS.length + ' areas');

const svgOf = (html) => (/<svg viewBox="0 0 24 24">(.*?)<\/svg>/.exec(html) || [])[1];
const seen = new Map();
for (const area of mod.AREAS) {
  const a = entries.find((e) => e.attrs['data-page'] === area.key);
  assert.ok(a, 'area ' + area.key + ' has no nav entry');
  assert.ok(area.icon && area.icon.length > 0, 'area ' + area.key + ' has no icon');
  const svg = svgOf(a.innerHTML);
  assert.strictEqual(svg, area.icon, 'the ' + area.key + ' nav entry does not render its own icon:\n' + a.innerHTML);
  assert.ok(!seen.has(svg), 'the ' + area.key + ' and ' + seen.get(svg) + ' nav entries render the same icon');
  seen.set(svg, area.key);
}
say('ok  ' + entries.length + ' generated nav entries, each wearing its own area\'s icon');
