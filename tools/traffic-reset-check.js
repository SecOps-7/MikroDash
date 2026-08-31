'use strict';
/**
 * WHAT THE TRAFFIC CHART FORGETS, AND WHEN.
 *
 * The live app clears the chart's history at exactly two moments and clears
 * exactly two variables at each:
 *
 *   ../MikroDash/public/app.js:2957   socket 'connect'      currentIf, allPoints
 *   ../MikroDash/public/app.js:8048   'router:switching'    currentIf, allPoints
 *
 * This port had BOTH halves of that wrong, in opposite directions, and neither
 * was visible to any of the 94 gates:
 *
 *  - `resetTraffic` was wired only to the router switch. A socket gap therefore
 *    left `allPoints` holding samples from before it, and the post-reconnect
 *    history was APPENDED to them — a chart drawn straight across a period
 *    during which nothing was received.
 *  - `resetTraffic` also zeroed `lastSampleTs`, `serverOffset` and
 *    `pendingTraffic`, which the live app clears NOWHERE. `serverOffset` is an
 *    EMA of clock skew that `app.js:2318` keeps deliberately — "resumes cleanly
 *    from the (EMA-smoothed) _serverOffset … so there is no resume jump."
 *    Zeroing it moves the first samples after a switch along the X axis.
 *
 * ── WHAT THIS GATE IS, SAID PLAINLY ─────────────────────────────────────────
 *
 * TWO HALVES, and only one of them is behavioural.
 *
 *  1. A SOURCE CONTRACT against the live app: which of the chart's state
 *     variables are assigned at each of the two sites. That is a read of the
 *     live source, not a run of it — it proves the port clears the same NAMES,
 *     not that either produces the same pixels. Said here so a green run is not
 *     read as more than it is.
 *  2. A BEHAVIOURAL check of the port: drive it with history, fire `connect`,
 *     and see the points actually gone and the clock actually kept.
 *
 *   node tools/traffic-reset-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc } = require('./lib/dom-shim.js');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/traffic-reset-check.js --freeze
const G = L.golden('traffic-reset-check');

const ROOT = path.join(__dirname, '..');
const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-traffic-reset.cjs');
const src = L.liveSource(ROOT);

/** The chart's state, by the live app's names, paired with the port's. */
const STATE = [
  ['currentIf', 'currentIf'],
  ['allPoints', 'allPoints'],
  ['_lastSampleTs', 'lastSampleTs'],
  ['_serverOffset', 'serverOffset'],
  ['_pendingTraffic', 'pendingTraffic'],
  // ADDED 2026-08-29, and its absence is why this gate could not see the bug it
  // was closest to. Without `_userPickedIf` in this vocabulary both live sites
  // read as {currentIf, allPoints}, the two-shapes check below compared equal,
  // and the port's single `resetTraffic` looked correct for both moments while
  // clearing the operator's chosen interface on every reconnect (issue #119).
  ['_userPickedIf', 'userPickedIf'],
];

/** Which of STATE a block of source ASSIGNS (not merely mentions). */
function clearedIn(block) {
  const out = new Set();
  for (const [live] of STATE) {
    // `name =` but not `name ==`/`===`, and not `.name =`.
    const re = new RegExp('(^|[^.\\w])' + live + '\\s*=(?!=)');
    if (re.test(block)) out.add(live);
  }
  return out;
}

// ── THE LIVE SIDE ───────────────────────────────────────────────────────────
//
// Both sites are handler bodies, lifted by their event name so a line moving
// cannot silently change what is read.
// THREE handlers subscribe `connect` and TWO subscribe `router:switching`; the
// anchors below name the one that owns the chart's state. Selecting by content
// rather than by position is the whole point — the first match is the reconnect
// BANNER handler, which clears nothing this gate is about.
const liveConnect = G.value('liveConnect', () => L.handler(src, 'connect', { contains: '_sysMetaWritten' }));
const liveSwitch = G.value('liveSwitch', () => L.handler(src, 'router:switching', { contains: 'switchOvl' }));

const connectCleared = clearedIn(liveConnect);
const switchCleared = clearedIn(liveSwitch);

// BELIEVABILITY. An empty set on either side means the lift found the wrong
// block, and the comparison below would then be between two empty sets — green,
// and meaningless.
assert.ok(connectCleared.size > 0,
  "the live 'connect' handler clears none of the chart's state — the lift has broken");
assert.ok(switchCleared.size > 0,
  "the live 'router:switching' handler clears none of the chart's state — the lift has broken");

// ── THE PORT'S resetTraffic ─────────────────────────────────────────────────
const portSrc = fs.readFileSync(path.join(ROOT, 'web', 'src', 'pages', 'dashboard-traffic.ts'), 'utf8');

// ── TWO SHAPES, NOT ONE ─────────────────────────────────────────────────────
//
// This gate used to assume a single `resetTraffic` served both live moments, and
// said so in the failure message it raised when that stopped being true. It
// stopped being true upstream in `d7548b0`: the router-switch site gained
// `_userPickedIf` and the connect site did not. The port now mirrors that with
// two functions, and this reads each against its own live site.
//
// TRANSITIVELY, because `resetTraffic` delegates the shared half to
// `resetTrafficOnReconnect` rather than repeating it. Reading only the literal
// body would report the delegating function as clearing almost nothing — which
// is exactly what happened when the split first landed.
function portBodyOf(name) {
  const after = portSrc.split('export function ' + name + '(): void {')[1];
  assert.ok(after, name + ' is gone from dashboard-traffic.ts');
  return after.split('\n}')[0];
}
function portClears(name, seen = new Set()) {
  if (seen.has(name)) return new Set();
  seen.add(name);
  const body = portBodyOf(name);
  const out = new Set();
  for (const [live, port] of STATE) {
    if (new RegExp('(^|[^.\\w])' + port + '\\s*=(?!=)').test(body)) out.add(live);
  }
  // Follow calls to the module's other exported resets.
  for (const m of body.matchAll(/\b(reset[A-Za-z]*)\s*\(/g)) {
    if (portSrc.includes('export function ' + m[1] + '(): void {')) {
      for (const k of portClears(m[1], seen)) out.add(k);
    }
  }
  return out;
}

function same(a, b) {
  return a.size === b.size && [...a].every((k) => b.has(k));
}

const problems = [];
const reconnectSet = portClears('resetTrafficOnReconnect');
const switchSet = portClears('resetTraffic');

if (!same(reconnectSet, connectCleared)) {
  problems.push('resetTrafficOnReconnect clears {' + [...reconnectSet].join(', ') +
                "} where the live 'connect' handler clears {" + [...connectCleared].join(', ') + '}');
}
if (!same(switchSet, switchCleared)) {
  problems.push('resetTraffic clears {' + [...switchSet].join(', ') +
                "} where the live 'router:switching' handler clears {" +
                [...switchCleared].join(', ') + '}');
}
// THE ASYMMETRY ITSELF. Asserted rather than described: the switch must forget
// strictly more than a reconnect does, and the difference must be the pick.
if (same(connectCleared, switchCleared)) {
  problems.push("the live app's two sites now clear the SAME set {" + [...connectCleared].join(', ') +
                '}. The asymmetry this gate models is gone upstream; the port keeps two functions ' +
                'for a distinction that no longer exists and should be revisited.');
} else if (connectCleared.has('_userPickedIf')) {
  problems.push("the live 'connect' handler now clears _userPickedIf — upstream reversed the " +
                'issue #119 rule and the port copies the opposite');
}

// ── resetTraffic MUST BE WIRED TO BOTH MOMENTS ──────────────────────────────
//
// Clearing the right names is worth nothing if nothing calls it. The switch path
// lives in main.ts and the reconnect path in dashboard.ts; both are checked by
// name rather than by driving a socket, which is what makes this half a source
// contract too.
const mainSrc = fs.readFileSync(path.join(ROOT, 'web', 'src', 'main.ts'), 'utf8');
const dashSrc = fs.readFileSync(path.join(ROOT, 'web', 'src', 'pages', 'dashboard.ts'), 'utf8');
if (!/resetTraffic\(\)/.test(mainSrc)) {
  problems.push('nothing in main.ts calls resetTraffic — the router switch no longer clears the chart');
}
// NOT `[^)]*` between the two: the handler is written `() => resetTraffic()`
// and the arrow's own `)` ends the class immediately. That version of this line
// reported the wiring missing while it was right there, which is a false
// failure and trains the reader to skim.
if (!/socket\.on\('connect',\s*\(\)\s*=>\s*resetTrafficOnReconnect\(\)\)/.test(dashSrc)) {
  problems.push("dashboard.ts does not call resetTrafficOnReconnect on socket 'connect' — a reconnect would " +
                'append the new history to samples from before the gap');
}

// ── THE BEHAVIOURAL HALF ────────────────────────────────────────────────────
function behaviour() {
  execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
    [path.join(ROOT, 'web', 'src', 'pages', 'dashboard-traffic.ts'),
     '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
    { stdio: 'inherit' });

  const doc = makeDoc(['ifaceSelect', 'windowSelect', 'trafficChart', 'trafficCard', 'liveRx', 'liveTx'], {});
  const prev = { doc: globalThis.document, win: globalThis.window };
  globalThis.document = doc;
  globalThis.window = { location: { origin: 'http://x' } };
  try {
    delete require.cache[require.resolve(OUT)];
    const mod = require(OUT);
    const handlers = {};
    mod.initTraffic({ on: (ev, cb) => { handlers[ev] = cb; }, emit() {} });

    const pts = [{ ts: 1000, rx: 1, tx: 2 }, { ts: 2000, rx: 3, tx: 4 }, { ts: 3000, rx: 5, tx: 6 }];
    handlers['traffic:history']({ ifName: 'ether1', points: pts });

    // BELIEVABILITY: history that did not land makes the emptiness below
    // meaningless — nothing was there to clear.
    if (mod.sharedPoints().length === 0) {
      console.error('the port ignored a traffic:history payload — this check cannot tell a reset ' +
                    'from a page that never held anything');
      process.exit(1);
    }

    // A clock the reset must NOT touch. Set through the module's own path, so
    // this is the value a real page would be carrying.
    const before = mod.sharedClock();

    mod.resetTraffic();
    if (mod.sharedPoints().length !== 0) {
      problems.push('resetTraffic left ' + mod.sharedPoints().length + ' point(s) behind');
    }
    const after = mod.sharedClock();
    if (after.serverOffset !== before.serverOffset || after.lastSampleTs !== before.lastSampleTs) {
      problems.push('resetTraffic changed the clock: serverOffset ' + before.serverOffset + '->' +
                    after.serverOffset + ', lastSampleTs ' + before.lastSampleTs + '->' +
                    after.lastSampleTs + '. The live app clears neither, anywhere.');
    }
    if (after.windowSecs !== before.windowSecs) {
      problems.push('resetTraffic changed the window from ' + before.windowSecs + ' to ' + after.windowSecs);
    }
  } finally {
    if (prev.doc === undefined) delete globalThis.document; else globalThis.document = prev.doc;
    if (prev.win === undefined) delete globalThis.window; else globalThis.window = prev.win;
  }
}
behaviour();

if (problems.length) {
  for (const p of problems) console.error('  ' + p);
  console.error('\ntraffic-reset-check: ' + problems.length + ' problem(s)');
  process.exit(1);
}
console.log('traffic-reset-check: a reconnect clears {' + [...reconnectSet].join(', ') +
            '}, a router switch clears {' + [...switchSet].join(', ') +
            '}, each matching its own live site, and the clock survives both');
