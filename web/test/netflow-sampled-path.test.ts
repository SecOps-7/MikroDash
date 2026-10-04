/**
 * THE NETWORK FLOW CURVES ARE SAMPLED ONCE (2026-10-04).
 *
 * The card's loop asked the browser for `getPointAtLength` for every dot of
 * every particle at 60 frames a second, which measured at 336 ms of every
 * second the Dashboard was open. Each lane is now sampled once at mount and a
 * dot's position read from the table, so this holds the table to the curve it
 * replaces, and the source to not calling the browser per frame.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.nfsample-entry.ts');
fs.writeFileSync(ENTRY, "export * from '../web/src/pages/dashboard-card-netflow.js';\n");
const OUT = path.join(ROOT, 'testdata', '.nfsample.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const mod = require(OUT);

// A quarter circle of radius 100, parameterised by arc length: a curve whose
// true point at any length is known, so the error is measured, not assumed.
const R = 100;
const LEN = Math.PI * R / 2;
let calls = 0;
const arc = {
  getPointAtLength(s: number) {
    calls++;
    const a = s / R;
    return { x: R * Math.cos(a), y: R * Math.sin(a) };
  },
};

const pts: Float32Array = mod.samplePath(arc, LEN);
assert.strictEqual(calls, Math.ceil(LEN) + 1, 'one browser call per unit of length, once');

// Under a twentieth of a unit anywhere along it: far below a pixel.
let worst = 0;
for (let s = 0; s <= LEN; s += 0.37) {
  const got = mod.pointOn(pts, LEN, s);
  const want = arc.getPointAtLength(s);
  worst = Math.max(worst, Math.hypot(got.x - want.x, got.y - want.y));
}
assert.ok(worst < 0.05, `the sampled curve strays ${worst.toFixed(3)} units from the real one`);

// The ends are exact, and a length beyond them clamps rather than reading past
// the table.
const end = arc.getPointAtLength(LEN);
const atEnd = mod.pointOn(pts, LEN, LEN);
assert.ok(Math.hypot(atEnd.x - end.x, atEnd.y - end.y) < 1e-3, 'the last sample is the end of the path');
assert.deepStrictEqual(mod.pointOn(pts, LEN, -5), mod.pointOn(pts, LEN, 0), 'before the start clamps to the start');
const past = mod.pointOn(pts, LEN, LEN + 50);
assert.ok(Number.isFinite(past.x) && Number.isFinite(past.y), 'past the end clamps, never NaN');

// The frame loop reads the table, never the browser.
const src = fs.readFileSync(path.join(ROOT, 'web', 'src', 'pages', 'dashboard-card-netflow.ts'), 'utf8');
const loop = src.slice(src.indexOf('function frame('), src.indexOf('function update('));
assert.ok(loop.length > 100, 'the frame loop was not found');
assert.ok(!/getPointAtLength/.test(loop), 'the frame loop asks the browser for points again');
assert.ok(/pointOn\(l\.pts, l\.len/.test(loop), 'the frame loop no longer reads the sampled table');
assert.ok(/if \(t - last < FRAME_MS\) return;/.test(loop), 'the frame loop is no longer held to 30 fps');

fs.rmSync(OUT, { force: true });
console.log('netflow-sampled-path: ok');
