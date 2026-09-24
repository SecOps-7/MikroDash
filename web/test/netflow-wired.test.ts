/**
 * The Network Flow card's Wired count fills without visiting another page
 * (issue #132).
 *
 * The count was written only by the Interfaces page's `ifstatus:update` handler,
 * an event the card's `wireless` room never receives, so it stayed "-" after
 * sign-in until Interfaces or Topology had been opened. Three properties:
 *
 *   1. the count is running, enabled `ether` interfaces and nothing else;
 *   2. the Dashboard draws it from `ifstatus:names`, which every browser gets;
 *   3. nothing else writes `#ndWiredCount`, so it cannot drift back to a page
 *      the card does not subscribe to.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-netflow-wired.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'dashboard-netflow.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
const N = require(OUT);

// ── 1. what is counted ─────────────────────────────────────────────────────
{
  const ifaces = [
    { name: 'ether1', type: 'ether', running: true, disabled: false },
    { name: 'ether2', type: 'ether', running: true, disabled: false },
    { name: 'ether3', type: 'ether', running: false, disabled: false },
    { name: 'ether4', type: 'ether', running: true, disabled: true },
    { name: 'bridge', type: 'bridge', running: true, disabled: false },
    { name: 'vlan10', type: 'vlan', running: true, disabled: false },
    { name: 'wifi1', type: 'wifi', running: true, disabled: false },
  ];
  assert.strictEqual(N.wiredCount(ifaces), 2, 'only running, enabled ether ports count');
  assert.strictEqual(N.wiredCount([]), 0);
  assert.strictEqual(N.wiredCount(null), 0);
  say('ok  the count is running, enabled ether ports');
}

// ── 2. it is written into the card ─────────────────────────────────────────
{
  const node = { textContent: '-' };
  global.document = { getElementById: (id) => (id === 'ndWiredCount' ? node : null) };
  N.renderWiredCount({ interfaces: [{ type: 'ether', running: true, disabled: false }] });
  assert.strictEqual(node.textContent, '1', 'the Wired count was not written');
  N.renderWiredCount(undefined);
  assert.strictEqual(node.textContent, '0', 'an empty payload did not reset the count');
  say('ok  the count is written into #ndWiredCount');
}

// ── 3. the Dashboard owns it, from ifstatus:names ──────────────────────────
{
  const dash = fs.readFileSync(path.join(ROOT, 'web', 'src', 'pages', 'dashboard.ts'), 'utf8');
  assert.ok(/socket\.on\('ifstatus:names',\s*\(d\)\s*=>\s*renderWiredCount\(d\)\)/.test(dash),
    'dashboard.ts does not draw the Wired count from ifstatus:names');

  const writers = [];
  const walk = (dir) => {
    for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
      const p = path.join(dir, e.name);
      if (e.isDirectory()) { if (e.name !== 'gen') walk(p); continue; }
      if (e.name.endsWith('.ts') && fs.readFileSync(p, 'utf8').includes("'ndWiredCount'")) {
        writers.push(path.relative(ROOT, p));
      }
    }
  };
  walk(path.join(ROOT, 'web', 'src'));
  assert.deepStrictEqual(writers, [path.join('web', 'src', 'pages', 'dashboard-netflow.ts')],
    'ndWiredCount is referenced outside the Network Flow module: ' + writers.join(', '));
  say('ok  only the Network Flow module writes the Wired count, from ifstatus:names');
}
