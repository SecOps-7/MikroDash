/**
 * ONE DIFF RENDERER (Config Management, 2026-09-21).
 *
 * Backups and Config Management draw a line diff with the same function,
 * web/src/diffview.ts. A configuration line can hold anything a router took,
 * so it is escaped; an added line is numbered on the new side, a removed one on
 * the old, and context on the old.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.diffview-entry.ts');
fs.writeFileSync(ENTRY, "export { hunksHTML } from '../web/src/diffview.js';\n");
const OUT = path.join(ROOT, 'testdata', '.diffview.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const mod = require(OUT);
fs.rmSync(OUT, { force: true });

const html = mod.hunksHTML([{ aStart: 3, aCount: 2, bStart: 3, bCount: 2, lines: [
  { op: ' ', text: '/ip dns', aLine: 3, bLine: 3 },
  { op: '-', text: 'set servers=192.0.2.1', aLine: 4, bLine: 0 },
  { op: '+', text: 'set comment="<img src=x onerror=alert(1)>"', aLine: 0, bLine: 4 },
] }]);

assert.ok(html.includes('@@ -3,2 +3,2 @@'), 'the hunk header');
assert.ok(!html.includes('<img'), 'a configuration line reached the markup unescaped');
assert.ok(html.includes('&lt;img'), 'the escaped line is shown');
const rows = [...html.matchAll(/<div class="bk-line ([^"]*)"><span class="bk-ln">(\d*)<\/span>/g)]
  .map((m) => [m[1].trim(), m[2]]);
assert.deepStrictEqual(rows, [['', '3'], ['bk-del', '4'], ['bk-add', '4']],
  'context and removed lines carry the old number, an added line the new');
assert.strictEqual(mod.hunksHTML([]), '', 'no hunks, no markup');
console.log('diffview: ok');
