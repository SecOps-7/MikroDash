/**
 * Topology and SwOS switches (#142): which neighbour is a stored switch, and
 * which merely looks like SwOS.
 *
 * The look is a heuristic (no SwOS neighbour has been captured): a MikroTik
 * device on a 1.x or 2.x version. What matters most is the negative: no
 * RouterOS neighbour, whose versions are 6.x and 7.x, may be offered as a
 * switch.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-swos-switches.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'swos-switches.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
const m = require(OUT);

let failed = 0;
function check(what, fn) {
  try { fn(); say('  ok   ' + what); }
  catch (e) { failed++; say('  FAIL ' + what + '\n       ' + e.message); }
}

say('topology: SwOS neighbours');

check('a stored switch is found by its MAC, either case, or its address', () => {
  m.setKnownSwitches([{ id: 's1', host: '198.51.100.20', mac: '02:00:00:00:00:1E' }]);
  assert.equal(m.switchForNeighbor('02:00:00:00:00:1e', '')?.id, 's1');
  assert.equal(m.switchForNeighbor('', '198.51.100.20')?.id, 's1');
  assert.equal(m.switchForNeighbor('02:00:00:00:00:99', '198.51.100.21'), null);
  assert.equal(m.switchForNeighbor('', ''), null, 'an empty MAC and address matched a switch');
});

check('a MikroTik device on SwOS versions looks like SwOS', () => {
  assert.ok(m.looksLikeSwOS('MikroTik', '2.21'));
  assert.ok(m.looksLikeSwOS('mikrotik', '1.17'));
});

check('RouterOS and non-MikroTik neighbours do not', () => {
  for (const [p, v] of [['MikroTik', '7.24.1 (stable)'], ['MikroTik', '6.49.10'], ['Cisco', '2.1'], ['', '2.21'], ['MikroTik', '']]) {
    assert.ok(!m.looksLikeSwOS(p, v), p + ' ' + v + ' was taken for SwOS');
  }
});

if (failed) { say('\n' + failed + ' failed'); process.exit(1); }
say('\nall passed');
