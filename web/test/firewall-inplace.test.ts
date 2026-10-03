/**
 * THE FIREWALL TABLE'S IN-PLACE PATH PATCHES COUNTERS, AND ONLY COUNTERS.
 *
 * It used to check only that the ids were the same and in the same order, so a
 * rule edited in Winbox - same id, same place - had its counters rewritten and
 * the rest of the row left showing the old rule (operator report, 2026-10-03).
 * `onlyCountersDiffer` is the decision; any rendered field that moved sends the
 * table to a full redraw.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const OUT = path.join(ROOT, 'testdata', '.fw-inplace.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'firewall.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
const { onlyCountersDiffer } = require(OUT);

const rule = (over = {}) => ({ id: '*1', chain: 'input', action: 'accept', comment: 'lan', srcAddress: '',
  dstAddress: '', protocol: '', dstPort: '', inInterface: '', packets: 10, bytes: 100, deltaPackets: 0,
  disabled: false, dynamic: false, ...over });
const two = [rule(), rule({ id: '*2', comment: 'wan' })];

assert.strictEqual(onlyCountersDiffer(two, [rule({ packets: 12, bytes: 300, deltaPackets: 2 }), two[1]]), true,
  'the control: a counter tick is patched in place');
for (const [what, changed] of [
  ['action', { action: 'drop' }], ['comment', { comment: 'lan, now blocked' }], ['disabled', { disabled: true }],
  ['source address', { srcAddress: '192.0.2.0/24' }], ['port', { dstPort: '22' }], ['interface', { inInterface: 'ether1' }],
]) {
  assert.strictEqual(onlyCountersDiffer(two, [rule(changed), two[1]]), false, 'an edited ' + what + ' was patched in place');
}
assert.strictEqual(onlyCountersDiffer(two, [two[1], two[0]]), false, 'a reorder was patched in place');
assert.strictEqual(onlyCountersDiffer(two, [two[0]]), false, 'a delete was patched in place');
assert.strictEqual(onlyCountersDiffer(two, [two[0], rule({ id: '*3' })]), false, 'a replaced rule was patched in place');
console.log('ok  only a counter change takes the in-place path; any edit, move or delete redraws');

fs.rmSync(OUT, { force: true });
console.log('firewall-inplace: all checks passed');
