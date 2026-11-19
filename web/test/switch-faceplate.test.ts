/**
 * The switch modal's Ports card draws the faceplate as the switch has it
 * (#142): one row on a small switch, two stacked rows (odd ports on top) on a
 * switch with stacked jacks, cages drawn as cages, each block of one medium
 * where the port order puts it.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-switch-faceplate.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'devices-modal-switch.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
global.document = { getElementById: () => null, querySelector: () => null, querySelectorAll: () => [], addEventListener: () => {},
  documentElement: { getAttribute: () => null }, body: { classList: { contains: () => false } } };
global.window = { addEventListener: () => {}, matchMedia: () => ({ matches: false, addEventListener: () => {} }) };
global.getComputedStyle = () => ({ getPropertyValue: () => '' });
const m = require(OUT);

let failed = 0;
function check(what, fn) {
  try { fn(); say('  ok   ' + what); }
  catch (e) { failed++; say('  FAIL ' + what + '\n       ' + e.message); }
}

/** `copper` jacks then `cages` cages (or the reverse when cagesFirst). */
function layout(copper, cages, cagesFirst = false) {
  const c = Array.from({ length: copper }, () => ({ cage: false }));
  const s = Array.from({ length: cages }, () => ({ cage: true }));
  return cagesFirst ? [...s, ...c] : [...c, ...s];
}
const shape = (groups) => groups.map((g) => (g.cage ? 'S' : 'C') + ':' + g.rows.map((r) => r.length).join('/'));

say('switch modal: the faceplate');

check('a small switch is one row: CSS610 8 jacks + 2 cages, CSS106 5 + 1', () => {
  assert.deepStrictEqual(shape(m.switchFaceplate(layout(8, 2))), ['C:8', 'S:2']);
  assert.deepStrictEqual(shape(m.switchFaceplate(layout(5, 1))), ['C:5', 'S:1']);
});

check('stacked jacks are two rows, odd ports on top, and the cages stack too', () => {
  const g = m.switchFaceplate(layout(16, 2));
  assert.deepStrictEqual(shape(g), ['C:8/8', 'S:1/1'], 'CSS318');
  assert.deepStrictEqual(g[0].rows[0].slice(0, 3), [0, 2, 4], 'the top row is ports 1, 3, 5...');
  assert.deepStrictEqual(g[0].rows[1].slice(0, 3), [1, 3, 5], 'the bottom row is ports 2, 4, 6...');
  assert.deepStrictEqual(g[1].rows, [[16], [17]], 'the two cages stack, 17 over 18');
  assert.deepStrictEqual(shape(m.switchFaceplate(layout(24, 2))), ['C:12/12', 'S:1/1'], 'CSS326');
});

check('a cage-first switch keeps the panel order: CRS317 16 cages then the management jack', () => {
  assert.deepStrictEqual(shape(m.switchFaceplate(layout(1, 16, true))), ['S:8/8', 'C:1']);
});

check('the card draws every port once, cages as cages, labelled by number', () => {
  const ports = layout(8, 2).map((p, i) => ({ ...p, name: 'p' + (i + 1), up: i !== 3, enabled: true,
    speed: '1G', fullDuplex: true, rxBps: 0, txBps: 0, rxErrors: 0, txErrors: 0, sfp: null, poe: null }));
  const html = m.switchFaceplateHtml({ connected: true, switch: { ports } });
  assert.ok(html.includes('<h3>Ports</h3>'), 'the card is not titled Ports');
  assert.equal((html.match(/class="if-port-item"/g) || []).length, 10);
  assert.equal((html.match(/data-state="down"/g) || []).length, 1, 'port 4 is down');
  assert.ok(html.includes('>10</span>'), 'the last port is not labelled 10');
  assert.equal((html.match(/class="swp-row"/g) || []).length, 2, 'one row per block');
});

check('the table card is renamed Ports list', () => {
  const html = m.switchPortsHtml({ connected: true, switch: { ports: [] } });
  assert.ok(html.includes('<h3>Ports list</h3>'));
});

if (failed) { say('\n' + failed + ' failed'); process.exit(1); }
say('\nall passed');
