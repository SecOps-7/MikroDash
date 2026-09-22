/**
 * THE WAN PAGE'S FLOW SANKEY, ITS ARITHMETIC (2026-09-22).
 *
 * this router ══▶ each uplink ══▶ Internet, one ribbon per uplink as thick as
 * its share of the WAN traffic now, split into an upload and a download lane.
 * Pinned, each with a control where one can fail silently:
 *   - the totals are the page's own Throughput sum (null counts as 0), shares
 *     add to 100 and are 0 (not NaN) when nothing moves, and ECMP counts the
 *     active default routes;
 *   - ribbons follow the page's order, stack without gaps at the ends, fill
 *     the stack exactly, and are as thick as their share, with a floor so an
 *     idle uplink is still drawn;
 *   - each ribbon's two lanes split it by upload and download, and an idle one
 *     splits evenly;
 *   - every uplink node gets room, and nodes never overlap;
 *   - particles go faster the busier a lane is, never blur, and stop at 0;
 *   - the ribbon path is closed and starts and ends where it was asked to.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.wanflow-entry.ts');
fs.writeFileSync(ENTRY, "export * from '../web/src/pages/wan-flow-layout.js';\n");
const OUT = path.join(ROOT, 'testdata', '.wanflow.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const F = require(OUT);
fs.rmSync(OUT, { force: true });

const wan = (name: string, rx: number | null, tx: number | null, active = true, isTunnel = false) =>
  ({ name, rxMbps: rx, txMbps: tx, routeActive: active, isTunnel });
const near = (a: number, b: number, msg: string) => assert.ok(Math.abs(a - b) < 1e-6, `${msg}: ${a} vs ${b}`);

// ── TOTALS, SHARES, ECMP (the hAP AX3's own shape) ────────────────────────
const flow = F.flowOf([wan('WAN1', 6, 2), wan('WG-SA', 1.5, 0.5, true, true),
  wan('WG-DE1', null, 0, true, true), wan('WG-DE2', 0, 0, false, true)]);
assert.strictEqual(flow.rx, 7.5, 'the Rx total is not the page\'s Throughput sum (null counts as 0)');
assert.strictEqual(flow.tx, 2.5);
assert.strictEqual(flow.ecmp, 3, 'three active default routes are not ECMP ×3');
near(flow.rows.reduce((a: number, r: { share: number }) => a + r.share, 0), 100, 'shares do not add to 100');
assert.strictEqual(flow.rows[0].share, 80, 'WAN1 carries 8 of 10');
assert.strictEqual(flow.rows[2].rx, null, 'an unreported rate became a number');
assert.ok(F.flowOf([wan('A', 0, 0), wan('B', null, null)]).rows.every((r: { share: number }) => r.share === 0),
  'nothing moving gave a share other than 0 (NaN?)');

// ── RIBBONS ───────────────────────────────────────────────────────────────
const L = F.layout(flow.rows, 1200);
assert.deepStrictEqual(L.bands.map((b: { name: string }) => b.name), ['WAN1', 'WG-SA', 'WG-DE1', 'WG-DE2'],
  'ribbons are not in the page\'s order');
let y = L.stackTop;
for (const b of L.bands) {
  near(b.y0, y, `${b.name} does not start where the ribbon above it ends`);
  y += b.h;
}
near(y, L.stackTop + L.stackH, 'the ribbons do not fill the stack exactly');
const spare = L.stackH - L.bands.length * F.MIN_BAND;
near(L.bands[0].h, F.MIN_BAND + 0.8 * spare, 'WAN1\'s ribbon is not 80% of the flow');
assert.strictEqual(L.bands[3].h, F.MIN_BAND, 'an idle uplink is not drawn at the floor');
assert.ok(L.bands[3].h > 0, 'control: an idle uplink vanished');

// ── LANES: upload over download ───────────────────────────────────────────
const w1 = L.bands[0];
near(w1.tx.h, w1.h * 2 / 8, 'the upload lane is not 2 of WAN1\'s 8');
near(w1.rx.top, w1.tx.h, 'the download lane does not start below the upload lane');
near(w1.tx.h + w1.rx.h, w1.h, 'the lanes do not fill their ribbon');
near(L.bands[3].tx.h, L.bands[3].h / 2, 'an idle ribbon is not split evenly');

// ── NODES: room for each, and no overlap ──────────────────────────────────
const nodeTop = (b: { y1: number; hn: number }) => b.y1 + b.hn / 2 - F.NODE_H / 2;
for (let i = 1; i < L.bands.length; i++) {
  assert.ok(nodeTop(L.bands[i]) >= nodeTop(L.bands[i - 1]) + F.NODE_H,
    `the ${L.bands[i].name} node overlaps the one above it`);
}
// A ribbon enters INSIDE its node, however busy: WAN1's is taller than a
// node at the ends, and must narrow to fit.
assert.ok(L.bands[0].h > F.NODE_H, 'control: WAN1\'s ribbon is not taller than a node here, so the next check proves nothing');
for (const b of L.bands) {
  assert.ok(b.y1 >= nodeTop(b) && b.y1 + b.hn <= nodeTop(b) + F.NODE_H, `${b.name}'s ribbon spills out of its node`);
  assert.ok(b.hn <= b.h, `${b.name}'s ribbon widens into its node`);
}
assert.ok(nodeTop(L.bands[0]) >= 0 && nodeTop(L.bands[3]) + F.NODE_H <= L.height, 'a node is outside the drawing');
assert.ok(L.xRouter < L.xNodeL && L.xNodeL < L.xNodeR && L.xNodeR < L.xNet, 'the columns are out of order');
const narrow = F.layout(flow.rows, 360);
assert.ok(narrow.xNodeR < narrow.xNet && narrow.xNodeL > narrow.xRouter, 'a phone-width layout overlaps its columns');

// ── PARTICLES ─────────────────────────────────────────────────────────────
assert.strictEqual(F.particleSpeed(0), 0, 'an idle lane has moving particles');
assert.strictEqual(F.particleSpeed(null), 0);
assert.ok(F.particleSpeed(1) < F.particleSpeed(100) && F.particleSpeed(100) < F.particleSpeed(1000),
  'a busier lane is not faster');
assert.ok(F.particleSpeed(1e6) <= 260, 'a very busy lane is a blur');
assert.ok(F.particleSpeed(0.001) > 0, 'control: a trickle does not move');

// ── EASING SETTLES ────────────────────────────────────────────────────────
let v = 0;
for (let i = 0; i < 200; i++) v = F.ease(v, 50);
assert.strictEqual(v, 50, 'easing never settles');

// ── THE RIBBON PATH ───────────────────────────────────────────────────────
const d = F.ribbon(10, 20, 110, 60, 8);
assert.ok(d.startsWith('M10.0,20.0C') && d.endsWith('Z'), `the ribbon is not a closed path from its start: ${d}`);
assert.ok(d.includes(' 110.0,60.0L110.0,68.0'), 'the ribbon does not reach its end at its thickness');
assert.ok(F.ribbon(10, 20, 110, 60, 8, 3).includes('L110.0,63.0'), 'a ribbon does not narrow to its end thickness');

// ── THE PAGE'S OWN RATE FORMAT ────────────────────────────────────────────
assert.strictEqual(F.fmtMb(0.88), '880 kb/s');
assert.strictEqual(F.fmtMb(12.34), '12.3 Mb/s');
assert.strictEqual(F.fmtMb(1500), '1.50 Gb/s');

console.log('wan-flow: all checks passed');
