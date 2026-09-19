/**
 * THE MOBILE ROUTER SELECT ESCAPES WHAT THE OPERATOR TYPED (review loop,
 * 2026-09-19).
 *
 * `main.ts` built the select's options by concatenation with neither the label
 * nor the id escaped, while the desktop dropdown beside it escapes both. A label
 * is operator text; `<`, `"` and `&` in it broke the markup or injected it.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.router-select-entry.ts');
fs.writeFileSync(ENTRY, "export { selectOptionsHtml } from '../web/src/router-dropdown.js';\n");
const OUT = path.join(ROOT, 'testdata', '.router-select.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const { selectOptionsHtml } = require(OUT);
fs.rmSync(OUT, { force: true });

const html = selectOptionsHtml([
  { id: 'r"1', label: '<img src=x onerror=alert(1)> & "Lab"' },
  { id: 'r2', name: 'plain' },
]);
assert.ok(!html.includes('<img'), 'a label reached the markup as a tag: ' + html);
assert.ok(html.includes('&lt;img') && html.includes('&amp;') && html.includes('&quot;Lab&quot;'),
  'the label was not escaped: ' + html);
assert.ok(html.includes('value="r&quot;1"'), 'the id can break out of its attribute: ' + html);
// THE CONTROL: an ordinary name is drawn as it is.
assert.ok(html.includes('<option value="r2">plain</option>'), 'an ordinary router lost its option: ' + html);
console.log('ok  the mobile router select escapes its labels and ids');
