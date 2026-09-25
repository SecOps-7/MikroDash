/**
 * AN ABSENT FIELD MEANS UNCHANGED, NOT ZERO (2026-09-25).
 *
 * ── THE BUG THIS EXISTS FOR ────────────────────────────────────────────────
 *
 * The Network Flow card is fed by four writers that each know one thing: the
 * wired count from `ifstatus:names`, the wireless count from `wireless:update`,
 * the WAN address from `lan:wan`, and the rates from `ifstatus:update` and
 * `traffic:update`. So a perfectly ordinary update is `{ wired: { clients: 5 } }`
 * with no rates in it at all.
 *
 * Reading those missing rates as zero wiped the lane. `ifstatus:names` lands
 * just after `ifstatus:update`, so the wired rate was computed from the real
 * per-port sums and then zeroed a moment later, once a second, for ever. The
 * operator reported it as "why do I see less movement from the wired box" - and
 * measurement showed the wired ports were carrying 1.06 Mb/s against wireless's
 * 0.42. The busier lane was the dead one.
 *
 * `loadFraction` is the pure half and is what this drives: it is the function
 * that turns a rate into the 0..1 the animation reads, and the one place the
 * capacity scaling can be asserted rather than eyeballed.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.nfpartial-entry.ts');
fs.writeFileSync(ENTRY, "export * from '../web/src/pages/dashboard-card-netflow.js';\n");
const OUT = path.join(ROOT, 'testdata', '.nfpartial.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const mod = require(OUT);

// ── THE SOURCE RULE, which is what actually stops the regression ──────────
//
// The guard is inside a closure the bundle does not export, so this asserts the
// shape rather than driving it. A behavioural test would need the whole SVG;
// this pins the one line whose absence caused the bug.
{
  const src = fs.readFileSync(
    path.join(ROOT, 'web', 'src', 'pages', 'dashboard-card-netflow.ts'), 'utf8');
  assert.ok(/if \(x\.down != null \|\| x\.up != null\) \{/.test(src),
    'the rate block is no longer guarded on the fields being PRESENT - a partial ' +
    'update carrying only `clients` will zero the lane again');
  assert.ok(/x\.clients != null/.test(src),
    'the client count is no longer guarded on being present either');
}

// ── loadFraction: the scale itself ────────────────────────────────────────
{
  const f = mod.loadFraction;
  // Relative to capacity, which is the whole point: the same rate is busy on a
  // small line and idle on a large one.
  assert.strictEqual(f(50e6, 50), 1, '50 Mb/s on a 50 Mb line is saturated');
  assert.strictEqual(f(50e6, 1000), .05, 'the same rate on a gigabit line is 5%');
  assert.strictEqual(f(2e9, 1000), 1, 'more than the line can carry clamps to 1');

  // ── UNKNOWN IS NOT SATURATED ────────────────────────────────────────────
  //
  // A router with no capacity recorded would divide by zero. Answering 0 shows
  // it as idle, which is the safe direction: showing every unconfigured router
  // as permanently saturated would be worse and is the obvious wrong turn.
  assert.strictEqual(f(50e6, 0), 0, 'no capacity must read as unknown, not full');
  assert.strictEqual(f(undefined, 1000), 0, 'an absent rate is not a rate');
  assert.strictEqual(f(0, 1000), 0);
  assert.strictEqual(f(-5, 1000), 0, 'a negative rate is not negative load');
}

fs.rmSync(OUT, { force: true });
console.log('netflow-partial-update: ok');
