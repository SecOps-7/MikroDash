'use strict';
/**
 * The Dashboard's arc gauge, live against ported.
 *
 * ── STRING EQUALITY IS THE WHOLE GATE, AND IT CAN BE ────────────────────────
 *
 * `gauge` is pure — label, percentage, class in; an SVG string out — and every
 * coordinate it emits goes through `_v`, which fixes two decimals. So the two
 * implementations either produce the same characters or they do not, and there
 * is no formatting tail to argue about. Nothing else on the System card is
 * checkable this exactly, which is why it was ported and gated on its own.
 *
 * ── THE CORPUS IS BUILT AROUND THE THRESHOLDS, NOT AROUND ROUND NUMBERS ─────
 *
 * Two independent ternaries switch at 75 and 90 — one picks the colour ramp and
 * one picks the text class — and both use `>`, not `>=`. So 75 is NOT warn and
 * 90 is NOT crit, and a port that reached for the more natural `>=` would be
 * wrong on exactly two inputs out of a hundred and right on all the rest. Each
 * boundary is therefore probed on both sides and ON it.
 *
 * ── AND ON A CLASS THE TABLE DOES NOT HOLD ──────────────────────────────────
 *
 * `cols` falls back to the cpu ramp for an unknown class. That is reachable:
 * the caller passes 'hdd' for storage, and any future caller passing anything
 * else lands here rather than crashing on undefined. Pinned, because a port
 * that let it throw would blank the card instead of drawing a blue gauge.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/gauge-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/gauge-check.js --freeze
const G = L.golden('gauge-check');
const src = L.liveSource(ROOT, path.join('public', 'app.js'));

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

// `gauge` ends at the first `\n}` after its declaration because its body has no
// nested function — the loop and the ternaries all sit inside. Asserted below
// rather than assumed: a slice that lost the return would compare two empty
// strings and pass.
const gaugeSrc = slice('function gauge(label, pct, cls)', '\n}', 'gauge');
// GUARDED ON THE REFERENCE. This validates the LIFT, which is meaningless when
// there is nothing to lift from: without `../MikroDash` the slice is '' and this
// fires for a reason that has nothing to do with the gauge.
if (L.hasReference(ROOT) &&
    (!gaugeSrc.includes('gauge-arc-wrap') || !gaugeSrc.includes('paths.join'))) {
  throw new Error('the gauge slice lost its return — it has grown a nested function');
}
const helperSrc = ['function _rotPt(', 'function _lp(', 'function _v(']
  .map((d) => slice(d, '\n}', d)).join('\n');
const escSrc = slice('function esc(', '\n}', 'esc');

const ENTRY = path.join(ROOT, 'testdata', '.gauge-entry.ts');
fs.writeFileSync(ENTRY, "export { gauge } from '../web/src/pages/dashboard-gauge.js';\n");
const OUT = path.join(ROOT, 'testdata', '.gauge-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const ctx = { Math, parseInt, String };
vm.createContext(ctx);
vm.runInContext(escSrc + '\n' + helperSrc + '\n' + gaugeSrc, ctx);
const port = require(OUT);

// Every percentage that matters, and a couple that do not, so a port keyed to
// the corpus rather than to the formula has nowhere to hide.
const PCTS = [0, 1, 12, 37, 50, 74, 75, 76, 89, 90, 91, 99, 100,
  // NOT integers. `litSegs` rounds and the text prints the number as given, so
  // a fractional load — which `cpuLoad` never is today, but `memPct` could
  // become — must round the same way and print the same digits on both sides.
  74.9, 75.5, 90.5, 3.5, 2.5];
const CLASSES = ['cpu', 'mem', 'hdd',
  // Reachable through the thresholds even though no caller passes them.
  'warn', 'crit',
  // The fallback: not in the table at all.
  'storage', ''];
// The label is escaped, so one that needs escaping is in the corpus. A gauge
// label is `'CPU'`, `'RAM'` or `'Storage'` today — but esc() is in the live
// path and a port that dropped it would only be caught by a label like this.
const LABELS = ['CPU', 'RAM', 'Storage', 'A & B <b>', "O'Brien \"x\"", ''];

let checked = 0, bad = 0;
for (const label of LABELS) {
  for (const pct of PCTS) {
    for (const cls of CLASSES) {
      const want = G.live(label + '|' + pct + '|' + cls, () => ctx.gauge(label, pct, cls));
      const got = port.gauge(label, pct, cls);
      checked++;
      if (want === got) continue;
      bad++;
      if (bad <= 3) {
        let at = 0;
        while (at < want.length && want[at] === got[at]) at++;
        console.error('DIFF gauge(%j, %s, %j) at char %d:\n  live: %s\n  port: %s',
          label, pct, cls, at, want.slice(Math.max(0, at - 40), at + 60),
          got.slice(Math.max(0, at - 40), at + 60));
      }
    }
  }
}

// A gauge that renders nothing would compare equal to another that renders
// nothing. The live output is checked for substance before the run is believed.
const sample = G.live('believability:sample', () => ctx.gauge('CPU', 42, 'cpu'));
const segs = (sample.match(/<path /g) || []).length;
if (segs !== 28) throw new Error('the live gauge drew ' + segs + ' segments, want 28');

fs.rmSync(OUT, { force: true });
if (bad) {
  console.error('\n%d of %d gauges differ', bad, checked);
  process.exit(1);
}
console.log('gauge-check: %d gauges identical (%d labels × %d percentages × %d classes)',
  checked, LABELS.length, PCTS.length, CLASSES.length);
