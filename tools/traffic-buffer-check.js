'use strict';
/**
 * The traffic chart's buffer and clock, live against ported.
 *
 * ── THE LIVE SIDE IS LIFTED, NOT RESTATED ───────────────────────────────────
 *
 * Every formula here is arithmetic, which is exactly the kind of code a port
 * "obviously" reproduces and quietly does not: an EMA weight of 0.1 against
 * 0.01, a `-3000` slack against `-30000`, `>` against `>=`. So the expected
 * values are produced by RUNNING the live expressions, pulled out of
 * `public/app.js` by anchored slices, rather than by me writing down what I
 * believe they are. Two of these differ from what I would have written.
 *
 * ── THE CLOCK IS DRIVEN, NOT SAMPLED ────────────────────────────────────────
 *
 * `Date.now()` appears inside three of these formulas, so a naive harness would
 * compare two runs taken microseconds apart and call a drifting clock a
 * difference. Both sides are given the SAME injected `now`, and the live slices
 * are evaluated in a context whose `Date.now` is that fixed value.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/traffic-buffer-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource. This gate already carried the empty-source guard in
// its slicing helper, but the READ itself still used fs.readFileSync and died of
// ENOENT before reaching it — the guard's own comment described behaviour the
// code did not have.
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('traffic-buffer-check');
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

function slice(decl, close, name) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent, and this then threw `cannot find <name>` at module scope — before
  // any frozen output could be served. An empty slice is harmless because the
  // live half is never entered when the output is frozen.
  if (src === '') return '';
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find ' + name + ' — it has moved or been rewritten');
  const j = src.indexOf(close, i);
  if (j === -1) throw new Error(name + ' is never closed');
  return src.slice(i, j + close.length);
}
function lineWith(needle, name) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent; without this the helper throws at module scope before a frozen
  // output can be served. Harmless because the live half is never entered
  // once the output is frozen.
  if (src === '') return '';
  const i = src.indexOf(needle);
  if (i === -1) throw new Error('cannot find ' + name + ' (' + needle + ')');
  const from = src.lastIndexOf('\n', i) + 1;
  return src.slice(from, src.indexOf('\n', i)).trim();
}

// FROZEN with the six lines below it, and for the same reason: `liveCtx`
// executes it.
const windowedSrc = G.value('windowedPoints source',
  () => slice('function windowedPoints()', '\n}', 'windowedPoints'));
assert.ok(/function windowedPoints/.test(windowedSrc),
  'the recorded windowedPoints source is not a function');
// The cap, the EMA weights and the slacks, taken from the live lines themselves
// so a change over there fails here instead of drifting silently.
// FROZEN, NOT GUARDED. These six are lifted VALUES — the live lines themselves —
// and `liveCtx` executes them, so guarding would leave the live half running
// empty source rather than not running at all.
const capLine = G.value('MAX_CLIENT_POINTS line', () => lineWith('var MAX_CLIENT_POINTS', 'MAX_CLIENT_POINTS'));
const offsetLine = G.value('server-offset EMA line', () => lineWith('_serverOffset=_serverOffset?', 'the server-offset EMA'));
const anchorLine = G.value('anchor formula line', () => lineWith('var anchor=_lastSampleTs?', 'the anchor formula'));
const pruneLine = G.value('prune slack line', () => lineWith('while(rd.length>0&&rd[0].x<vl-3000)', 'the prune slack'));
const yMaxLine = G.value('y-max EMA line', () => lineWith('_yMaxCurrent+=(_yMaxTarget-_yMaxCurrent)*0.08', 'the y-max EMA'));
const redrawLine = G.value('redraw gap rule line', () => lineWith('if(!rx.length||p.ts-rx[rx.length-1].x>2000)', 'the redraw gap rule'));
// NOT guarded: with the lines frozen this validates the RECORDING, which is
// exactly what should stay checkable without a reference.
assert.match(capLine, /1800/, 'MAX_CLIENT_POINTS is no longer 1800');
for (const [nm, ln] of [['offset', offsetLine], ['anchor', anchorLine], ['prune', pruneLine],
  ['yMax', yMaxLine], ['redraw', redrawLine]]) {
  assert.ok(ln && ln.length > 8, 'the recorded ' + nm + ' line is empty — the golden is broken');
}

const ENTRY = path.join(ROOT, 'testdata', '.trafbuf-entry.ts');
fs.writeFileSync(ENTRY, "export * from '../web/src/pages/dashboard-traffic-buffer.js';\n");
const OUT = path.join(ROOT, 'testdata', '.trafbuf-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const port = require(OUT);

const RIGHT_BUFFER_MS = 1500;
function liveCtx(now) {
  const ctx = {
    Math, Date: { now: () => now },
    RIGHT_BUFFER_MS, windowSecs: 60, allPoints: [],
    _lastSampleTs: 0, _serverOffset: 0, _yMaxTarget: 0, _yMaxCurrent: 0,
    MAX_CLIENT_POINTS: 0,
  };
  vm.createContext(ctx);
  vm.runInContext([
    capLine, windowedSrc,
    'function __offset(prev,raw){ var _serverOffset=prev, _rawOffset=raw; ' + offsetLine + '; return _serverOffset; }',
    'function __anchor(lastTs,off,pts){ var _lastSampleTs=lastTs, _serverOffset=off; ' + anchorLine + '; return anchor; }',
    // NOTE: `pruneLine` already carries its own loop body — appending another
    // `{rd.shift();td.shift();}` here shifted one extra point unconditionally
    // and made the live side appear to prune points exactly ON the boundary.
    'function __prune(rd,td,vl){ ' + pruneLine + ' var newMax=0;' +
      'for(var i=0;i<rd.length;i++)if(rd[i].y>newMax)newMax=rd[i].y;' +
      'for(var i=0;i<td.length;i++)if(td[i].y>newMax)newMax=td[i].y; return newMax; }',
    'function __ymax(cur,target){ var _yMaxCurrent=cur, _yMaxTarget=target||1; ' + yMaxLine + '; return _yMaxCurrent; }',
    'var __rc=false; function redrawChart(){ __rc=true; }',
    'function __redrawInner(rx,ts){ var p={ts:ts}; ' + redrawLine + ' }',
    'function __redraw(rx,ts){ __rc=false; __redrawInner(rx,ts); return __rc; }',
    'function __push(sample){ allPoints.push({ts:sample.ts,rx_mbps:sample.rx_mbps,tx_mbps:sample.tx_mbps});' +
      ' if(allPoints.length>MAX_CLIENT_POINTS)allPoints.shift(); return allPoints; }',
  ].join('\n'), ctx);
  return ctx;
}
assert.equal(liveCtx(0).MAX_CLIENT_POINTS, port.MAX_CLIENT_POINTS, 'MAX_CLIENT_POINTS differs');

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (JSON.stringify(a) === JSON.stringify(b)) return;
  bad++;
  if (bad <= 5) console.error('DIFF %s\n  live: %j\n  port: %j', what, a, b);
}

// ── windowedPoints, against a clock both sides share ───────────────────────
const NOW = 1773567000000;
const mk = (ts, rx, tx) => ({ ts, rx_mbps: rx, tx_mbps: tx });
for (const windowSecs of [10, 60, 300]) {
  for (const spread of [1000, 30000, 600000]) {
    const pts = Array.from({ length: 40 }, (_, i) => mk(NOW - (39 - i) * spread, i, i * 2));
    const ctx = liveCtx(NOW);
    ctx.allPoints = pts.slice();
    ctx.windowSecs = windowSecs;
    cmp('windowedPoints(win=' + windowSecs + ',spread=' + spread + ')',
      ctx.windowedPoints(), port.windowedPoints(pts, NOW, windowSecs, RIGHT_BUFFER_MS));
  }
}
{ // the edges: empty, one point, everything too old, everything in range
  for (const pts of [[], [mk(NOW, 1, 1)], [mk(NOW - 9e8, 1, 1)], [mk(NOW - 1, 1, 1), mk(NOW, 2, 2)]]) {
    const ctx = liveCtx(NOW); ctx.allPoints = pts.slice(); ctx.windowSecs = 60;
    cmp('windowedPoints(edge ' + pts.length + ')', ctx.windowedPoints(),
      port.windowedPoints(pts, NOW, 60, RIGHT_BUFFER_MS));
  }
}
{
  // NON-MONOTONIC buffers. `windowedPoints` walks BACKWARDS and BREAKS at the
  // first point older than the cutoff — it does not filter — so one stale point
  // in the middle truncates everything before it. A `filter` rewrite is
  // identical on every ordered buffer and differs only here, which is exactly
  // why a mutation to `filter` SURVIVED until these cases existed.
  //
  // The port's own comment claims this behaviour; a claim a gate cannot see is
  // a comment, not a contract. Reachable: the buffer is fed by pushed samples
  // AND by a wholesale history load, and a router whose clock steps backwards
  // emits a lower ts than the sample before it.
  const cutoff = NOW - 60000 - RIGHT_BUFFER_MS;
  const cases = {
    'one stale point in the middle': [mk(cutoff + 1000, 1, 1), mk(cutoff - 5000, 2, 2), mk(cutoff + 3000, 3, 3)],
    'a clock step backwards at the end': [mk(NOW - 2000, 1, 1), mk(NOW - 1000, 2, 2), mk(cutoff - 1, 3, 3)],
    'fully reversed': [mk(NOW, 1, 1), mk(NOW - 20000, 2, 2), mk(NOW - 90000, 3, 3)],
  };
  for (const [name, pts] of Object.entries(cases)) {
    const ctx = liveCtx(NOW); ctx.allPoints = pts.slice(); ctx.windowSecs = 60;
    cmp('windowedPoints(' + name + ')', ctx.windowedPoints(),
      port.windowedPoints(pts, NOW, 60, RIGHT_BUFFER_MS));
  }
}
{ // EXACTLY on the cutoff: the live test is `<`, so a point AT the cutoff stays.
  const ctx = liveCtx(NOW); ctx.windowSecs = 60;
  const cutoff = NOW - 60000 - RIGHT_BUFFER_MS;
  const pts = [mk(cutoff - 1, 0, 0), mk(cutoff, 1, 1), mk(cutoff + 1, 2, 2)];
  ctx.allPoints = pts.slice();
  cmp('windowedPoints(exactly on the cutoff)', ctx.windowedPoints(),
    port.windowedPoints(pts, NOW, 60, RIGHT_BUFFER_MS));
}

// ── the offset EMA, including the falsy re-seed ────────────────────────────
for (const [prev, raw] of [[0, 500], [0, -500], [500, 500], [500, 1500], [1000, -1000],
  [0, 0], [-0.0001, 5], [1e-9, 5], [123.456, -78.9]]) {
  cmp('smoothOffset(' + prev + ',' + raw + ')',
    liveCtx(NOW).__offset(prev, raw), port.smoothOffset(prev, raw));
}

// ── the anchor, in all three of its branches ───────────────────────────────
for (const lastTs of [0, NOW - 5000]) {
  for (const off of [0, 250, -250]) {
    for (const pts of [[], [mk(NOW - 3000, 1, 1)], [mk(NOW - 9000, 1, 1), mk(NOW - 3000, 2, 2)]]) {
      const ctx = liveCtx(NOW);
      ctx.allPoints = pts.slice();
      cmp('anchorMs(' + lastTs + ',' + off + ',' + pts.length + ')',
        ctx.__anchor(lastTs, off, pts), port.anchorMs(lastTs, off, NOW, pts));
    }
  }
}

// ── axisWindow follows the anchor ──────────────────────────────────────────
for (const windowSecs of [10, 60, 300]) {
  const anchor = NOW + 250;
  const want = { min: anchor - windowSecs * 1000 - RIGHT_BUFFER_MS, max: anchor - RIGHT_BUFFER_MS };
  cmp('axisWindow(' + windowSecs + ')', want, port.axisWindow(anchor, windowSecs, RIGHT_BUFFER_MS));
}

// ── prune + max, RX and TX shifted together ────────────────────────────────
for (const slack of [0, 2999, 3000, 3001, 10000]) {
  const vl = NOW - 60000;
  const rx = [{ x: vl - slack, y: 5 }, { x: vl + 1000, y: 9 }, { x: vl + 2000, y: 3 }];
  const tx = [{ x: vl - slack, y: 50 }, { x: vl + 1000, y: 1 }, { x: vl + 2000, y: 2 }];
  const lrx = rx.map((p) => ({ ...p })), ltx = tx.map((p) => ({ ...p }));
  const prx = rx.map((p) => ({ ...p })), ptx = tx.map((p) => ({ ...p }));
  const lm = liveCtx(NOW).__prune(lrx, ltx, vl);
  const pm = port.pruneAndMax(prx, ptx, vl);
  cmp('pruneAndMax(slack=' + slack + ') max', lm, pm);
  cmp('pruneAndMax(slack=' + slack + ') rx', lrx, prx);
  cmp('pruneAndMax(slack=' + slack + ') tx', ltx, ptx);
}
{ // everything pruned, and an all-zero set
  const vl = NOW;
  for (const pts of [[], [{ x: vl - 99999, y: 7 }], [{ x: vl, y: 0 }, { x: vl + 1, y: 0 }]]) {
    const lrx = pts.map((p) => ({ ...p })), ltx = pts.map((p) => ({ ...p }));
    const prx = pts.map((p) => ({ ...p })), ptx = pts.map((p) => ({ ...p }));
    cmp('pruneAndMax(edge ' + pts.length + ')',
      [liveCtx(NOW).__prune(lrx, ltx, vl), lrx], [port.pruneAndMax(prx, ptx, vl), prx]);
  }
}

// ── the y-max EMA, including the || 1 floor ────────────────────────────────
for (const [cur, target] of [[0, 0], [0, 100], [100, 0], [100, 100], [1, 0.5], [50, 51], [0.08, 0]]) {
  cmp('smoothMax(' + cur + ',' + target + ')',
    liveCtx(NOW).__ymax(cur, target), port.smoothMax(cur, target));
}

// ── the redraw gap rule, on both sides of 2000 and ON it ───────────────────
for (const gap of [0, 1, 1999, 2000, 2001, 5000, -1]) {
  const rx = [{ x: NOW - 10000, y: 1 }, { x: NOW, y: 2 }];
  cmp('needsFullRedraw(gap=' + gap + ')',
    liveCtx(NOW).__redraw(rx, NOW + gap), port.needsFullRedraw(rx, NOW + gap));
}
cmp('needsFullRedraw(empty)', liveCtx(NOW).__redraw([], NOW), port.needsFullRedraw([], NOW));

// ── pushSample and the cap ─────────────────────────────────────────────────
{
  const cap = port.MAX_CLIENT_POINTS;
  const ctx = liveCtx(NOW);
  const mine = [];
  for (let i = 0; i < cap + 5; i++) {
    const s = mk(NOW + i, i, i * 2);
    ctx.__push(s);
    port.pushSample(mine, s);
  }
  cmp('pushSample(cap) length', ctx.allPoints.length, mine.length);
  cmp('pushSample(cap) first', ctx.allPoints[0], mine[0]);
  cmp('pushSample(cap) last', ctx.allPoints[ctx.allPoints.length - 1], mine[mine.length - 1]);
  // Extra keys on the sample must NOT be carried into the buffer: the live push
  // copies three named fields.
  const ctx2 = liveCtx(NOW), mine2 = [];
  const fat = Object.assign(mk(NOW, 1, 2), { ifName: 'ether1', extra: 'x' });
  ctx2.__push(fat); port.pushSample(mine2, fat);
  cmp('pushSample drops extra keys', ctx2.allPoints, mine2);
}

fs.rmSync(OUT, { force: true });
if (bad) { console.error('\n%d of %d comparisons differ', bad, checked); process.exit(1); }
console.log('traffic-buffer-check: %d comparisons identical', checked);
