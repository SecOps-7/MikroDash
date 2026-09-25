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
 *   3. nothing else writes the count, so it cannot drift back to a page the
 *      card does not subscribe to.
 *
 * ── RE-AIMED 2026-09-25, WHEN THE CARD WAS REPLACED ────────────────────────
 *
 * The count used to be written straight into `#ndWiredCount`. The new card
 * owns its own text node and is fed through `netFlowUpdate`, so property 2 is
 * now about the ROUTE rather than the element, and property 3 scans for the new
 * id. What issue #132 was about - the count filling on first paint, from an
 * event every browser receives - is unchanged and still checked.
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

// ── 2. it reaches the card through netFlowUpdate, and never throws ─────────
//
// `netFlowUpdate` is a no-op until the card mounts, and this handler runs on an
// event every browser receives - including browsers that are not on the
// Dashboard and have never built the SVG. Calling it before the mount must be
// silent rather than fatal, which is the property worth pinning: a throw here
// would take down the `ifstatus:names` handler for every page.
{
  const src = fs.readFileSync(
    path.join(ROOT, 'web', 'src', 'pages', 'dashboard-netflow.ts'), 'utf8');
  assert.ok(/netFlowUpdate\(\{\s*wired:\s*\{\s*clients:\s*wiredCount\(/.test(src),
    'renderWiredCount no longer feeds the card through netFlowUpdate');
  assert.ok(!src.includes('ndWiredCount'),
    'the old element id is still referenced - the card that owned it is gone');

  // Called with no card mounted, which is the common case. A document is
  // needed because the unmounted path writes the static text nodes rather than
  // dropping the payload - which is the behaviour that keeps the numbers
  // readable where the animation cannot run.
  const nodes = { 'nf-cnt-wired': { textContent: '0' } };
  global.document = { getElementById: (id) => nodes[id] || null };
  N.renderWiredCount({ interfaces: [{ type: 'ether', running: true, disabled: false }] });
  assert.strictEqual(nodes['nf-cnt-wired'].textContent, '1',
    'the unmounted path did not write the count into the card');
  N.renderWiredCount(undefined);
  assert.strictEqual(nodes['nf-cnt-wired'].textContent, '0',
    'an empty payload did not reset the count');
  say('ok  the count is pushed through netFlowUpdate, and still renders unmounted');
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
      if (e.name.endsWith('.ts') && fs.readFileSync(p, 'utf8').includes("nf-cnt-wired")) {
        writers.push(path.relative(ROOT, p));
      }
    }
  };
  walk(path.join(ROOT, 'web', 'src'));
  assert.deepStrictEqual(writers, [path.join('web', 'src', 'pages', 'dashboard-card-netflow.ts')],
    'the counter text node is written outside the Network Flow card: ' + writers.join(', '));
  say('ok  only the Network Flow module writes the Wired count, from ifstatus:names');
}
