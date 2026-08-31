'use strict';
/**
 * The restore decision from upstream `d7548b0`, port against live.
 *
 * The live condition is three tests inside the `traffic:history` handler; the
 * port extracts them into `shouldRestorePick` so both can be driven without a
 * DOM or a socket. The live one is lifted by content anchor and evaluated in a
 * `vm` with a fake options list.
 *
 * ---- WHY THIS EXISTS ------------------------------------------------------
 *
 * `tools/reset-contract-audit.js` catches the CLEAR — it noticed the upstream
 * change by failing on a live variable it could not map. It says nothing about
 * the restore, which is the subtle half: the guard on the pick still being
 * selectable is what keeps the restore from fighting the auto-switch that moves
 * off a downed interface.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/traffic-pick-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const OUT = path.join(ROOT, 'testdata', '.traffic-pick-port.cjs');

// ---- Lift the live condition ----------------------------------------------
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('traffic-pick-check');
const app = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

// THE LIFT AND ITS ANCHOR CHECKS, ONLY WHERE THERE IS A SOURCE TO LIFT FROM.
// Both ask the live source a question — is the condition still at this anchor,
// does it still test the options list — and neither is answerable without it.
// `liveDecides` is from here on called only inside a frozen closure.
let liveDecides = () => { throw new Error('liveDecides called without a reference'); };
if (LIFT.hasReference(ROOT)) {
  const lines = app.split('\n');
  const at = lines.findIndex((l) => l.includes('if (_userPickedIf && data.ifName !== _userPickedIf &&'));
  if (at < 0) throw new Error('anchor lost: the traffic:history restore condition');
  const cond = lines.slice(at, at + 2).join('\n')
    .replace(/^\s*if \(/, '').replace(/\) \{\s*$/, '');
  if (!cond.includes('ifaceSelect.options')) {
    throw new Error('the lifted condition does not test the options list — the anchors drifted');
  }
  liveDecides = vm.runInNewContext(
    `(function (arriving, picked, options) {
       var _userPickedIf = picked;
       var data = { ifName: arriving };
       var ifaceSelect = { options: options.map(function (v) { return { value: v }; }) };
       return !!(${cond});
     })`, Object.create(null), { filename: 'app.js#trafficRestore' });
}

// ---- The port --------------------------------------------------------------
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'dashboard-traffic.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
const m = require(OUT);

const CASES = [
  ['no pick at all', 'ether1', '', ['ether1', 'ether2']],
  ['the pick is what arrived', 'ether2', 'ether2', ['ether1', 'ether2']],
  ['the default arrived and the pick is selectable', 'ether1', 'ether2', ['ether1', 'ether2']],
  // THE GUARD. The picked interface went down and left the options; the restore
  // must stay quiet or it fights the auto-switch every tick.
  ['the pick has left the list', 'ether1', 'ether2', ['ether1', 'ether3']],
  ['the pick returns to the list', 'ether1', 'ether2', ['ether1', 'ether2', 'ether3']],
  ['an empty options list', 'ether1', 'ether2', []],
  ['arriving name is undefined', undefined, 'ether2', ['ether1', 'ether2']],
];

const problems = [];
let restores = 0;
for (const [why, arriving, picked, options] of CASES) {
  const live = G.live(why, () => liveDecides(arriving, picked, options));
  const port = m.shouldRestorePick(arriving, picked, options);
  if (live) restores++;
  if (live !== port) {
    problems.push(`${why}: port ${port}, live ${live}`);
  }
}

// The corpus must exercise BOTH answers, or it agrees with a function that
// always returns the same thing.
if (restores === 0) problems.push('no case restores; the corpus cannot tell the decision from `false`');
if (restores === CASES.length) problems.push('every case restores; the guards are not exercised');

// ── THE THIRD PROPERTY: RECORDED ONLY WHEN THE OPERATOR ACTS ───────────────
//
// The live test asserts `_rebuildIfaceSelect` never mentions `_userPickedIf`:
// "the app coping with a downed interface is not the operator making a choice",
// and remembering the auto-switch would let one flap permanently rewrite what
// they asked for.
//
// THE PORT'S GUARANTEE IS STRONGER, and this pins that it stays that way.
// `userPickedIf` is module-private to `dashboard-traffic.ts` — the auto-switch
// lives in `pages/interfaces.ts` and CANNOT reach it, where the live app has a
// file-level `var` that anything could assign. Exporting it, or writing it from
// a second place, would give away a guarantee the type system is currently
// making for free.
{
  const mod = fs.readFileSync(
    path.join(ROOT, 'web', 'src', 'pages', 'dashboard-traffic.ts'), 'utf8');
  const body = mod.replace(/\/\*[\s\S]*?\*\//g, ' ').replace(/\/\/[^\n]*/g, ' ');

  if (/export\s+(let|const|var)\s+userPickedIf/.test(body)) {
    problems.push('userPickedIf is EXPORTED. It is module-private on purpose: that is what stops '
      + 'the auto-switch in pages/interfaces.ts from recording a choice the operator did not make.');
  }
  // Two writes: the clear in resetTraffic, and the change listener. Any third is
  // a place the operator did not act.
  const writes = (body.match(/\buserPickedIf\s*=/g) || []).length - 1; // minus the declaration
  if (writes !== 2) {
    problems.push(`userPickedIf is assigned in ${writes} places besides its declaration; there `
      + 'are exactly two — the clear in resetTraffic and the change listener. A third is the app '
      + 'coping being mistaken for the operator choosing.');
  }
  // And no other module may mention it at all.
  for (const f of fs.readdirSync(path.join(ROOT, 'web', 'src', 'pages'))) {
    if (!f.endsWith('.ts') || f === 'dashboard-traffic.ts') continue;
    const other = fs.readFileSync(path.join(ROOT, 'web', 'src', 'pages', f), 'utf8')
      .replace(/\/\*[\s\S]*?\*\//g, ' ').replace(/\/\/[^\n]*/g, ' ');
    if (/\buserPickedIf\b/.test(other)) {
      problems.push(`pages/${f} mentions userPickedIf; only dashboard-traffic.ts may.`);
    }
  }
}

fs.rmSync(OUT, { force: true });
if (problems.length) {
  console.error('traffic-pick-check FAILED:');
  for (const p of problems) console.error('  - ' + p);
  process.exit(1);
}
console.log(`traffic-pick-check: ${CASES.length} cases agree with the live restore condition `
  + `(${restores} restore, ${CASES.length - restores} do not)`);
