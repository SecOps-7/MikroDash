#!/usr/bin/env node
'use strict';
/**
 * Pin the Routers background pool's membership and lifecycle against the live
 * `src/overviewSessions.js`.
 *
 * ── THE REAL MODULE IS DRIVEN, WITH ONLY ITS EDGES FAKED ────────────────────
 *
 * Same technique as `tools/connectivity-cases.js`: the ROS client, the three
 * collectors, Settings and the error classifier are replaced through
 * `require.cache`, and `syncSessions` / `suspend` / `resume` are then called for
 * real. The membership rule and the lifecycle guards are the live ones.
 *
 * ── WHAT THE CORPUS HAS TO SEPARATE ─────────────────────────────────────────
 *
 * `syncSessions` has two loops and they are not symmetrical: the teardown loop
 * fires when a router is EXCLUDED **or** GONE, and the build loop skips when a
 * router is EXCLUDED **or** ALREADY TRACKED. A port that used one condition for
 * both agrees on every simple case and diverges precisely when a tracked router
 * becomes excluded — which is what happens the moment somebody opens a router's
 * page, i.e. constantly.
 *
 * The lifecycle guards fail silently in the other direction. A `destroyed`
 * session that starts its collectors on a late `connected` event polls a removed
 * router for ever, and a `resume()` that ignores `connected` starts collectors
 * against a dead socket. Neither shows up as an error.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/overview-pool-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const Module = require('module');
const { EventEmitter } = require('events');

const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT = path.join(__dirname, '..', 'testdata', 'overview-pool-cases.json');
const CHECK = process.argv.includes('--check');
const srcDir = path.join(SRC, 'src');

function stub(rel, exports) {
  const file = require.resolve(rel, { paths: [srcDir] });
  const m = new Module(file, null);
  m.filename = file; m.loaded = true; m.exports = exports;
  require.cache[file] = m;
  return file;
}

// Every collector records start/stop rather than touching a router.
const collectorLog = [];
function fakeCollector(name) {
  return class {
    // `routerLabel` is what overviewSessions puts on the client
    // (`router.label || router.host`), so it identifies which session this
    // collector belongs to without the fake needing to be told.
    constructor(o) { this.opts = o; this.lastPayload = null; this._id = (o.ros || {}).routerLabel; }
    start() { collectorLog.push([this._id, name, 'start']); }
    stop() { collectorLog.push([this._id, name, 'stop']); }
  };
}
let lastRos = null;
class FakeROS extends EventEmitter {
  constructor(opts) { super(); this.opts = opts; lastRos = this; }
  connectLoop() { return new Promise(() => {}); }
  stop() {}
}
stub('./routeros/client', FakeROS);
stub('./collectors/system', fakeCollector('system'));
stub('./collectors/interfaceStatus', fakeCollector('ifStatus'));
stub('./collectors/dhcpLeases', fakeCollector('dhcpLeases'));
stub('./collectors/nullCollector', {
  makeNullCollector: (n) => ({ _null: n, lastPayload: null, start() {}, stop() {} }),
});
stub('./routeros/classifyError', {
  classifyRosError: (e) => (String(e && e.message) === 'known'
    ? { reason: 'Authentication failed', classified: true }
    : { reason: String(e && e.message), classified: false }),
});
stub('./settings', { load: () => ({}) });
stub('./collection', {
  resolveCollection: () => ({
    poll: { system: 2000, ifStatus: 5000, ifaces: 60000, dhcpLeases: 30000 },
    stream: { system: false, ifStatus: false, dhcpLeases: false },
    enabled: { ifStatus: true },
  }),
});

const pool = require(path.join(srcDir, 'overviewSessions.js'));

const R = (id) => ({ id, label: 'r' + id, host: '198.51.100.1', port: 8728, username: 'u', password: 'p' });
const rosOf = new Map();

/** Drive one scenario and record what the pool concluded. */
function run(steps) {
  pool.stopAll();
  rosOf.clear();
  collectorLog.length = 0;
  pool.resume();     // every scenario starts un-suspended
  const trace = [];
  for (const [op, a, b] of steps) {
    if (op === 'sync') {
      const before = new Set(pool.getSummaries().map((s) => s.routerId));
      lastRos = null;
      pool.syncSessions(a.map(R), new Set(b || []));
      const after = pool.getSummaries().map((s) => s.routerId).sort();
      // Which ROS clients were built during THIS sync — one per new session.
      for (const id of after) if (!before.has(id) && lastRos) rosOf.set(id, lastRos);
      trace.push(['sync', after]);
    } else if (op === 'connect' || op === 'close' || op === 'error') {
      const ros = rosOf.get(a);
      if (ros) {
        if (op === 'connect') ros.emit('connected');
        else if (op === 'close') ros.emit('close');
        else ros.emit('connectionError', new Error(b || 'boom'));
      }
      const s = pool.getSummaries().find((x) => x.routerId === a) || null;
      trace.push([op, a, s ? [s.connected, s.lastError] : null]);
    } else if (op === 'suspend') { pool.suspend(); trace.push(['suspend']); }
    else if (op === 'resume') { pool.resume(); trace.push(['resume']); }
    else throw new Error('unknown step ' + op);
  }
  return { trace, collectors: collectorLog.slice() };
}

// The ROS clients are keyed by construction order, so tag each one as it is
// built: the fake records the router it was given.
const origBuild = FakeROS.prototype.constructor;
void origBuild;

const SCENARIOS = {
  'one router, nothing excluded': [['sync', ['a'], []]],
  'a router excluded from the start is never built': [['sync', ['a', 'b'], ['b']]],
  'a TRACKED router becoming excluded is torn down':
    [['sync', ['a', 'b'], []], ['sync', ['a', 'b'], ['a']]],
  'a router removed from the fleet is torn down':
    [['sync', ['a', 'b'], []], ['sync', ['b'], []]],
  'a router both removed AND excluded':
    [['sync', ['a', 'b'], []], ['sync', ['b'], ['a']]],
  'an excluded router returning is rebuilt':
    [['sync', ['a'], ['a']], ['sync', ['a'], []]],
  'syncing the same fleet twice builds nothing new':
    [['sync', ['a', 'b'], []], ['sync', ['a', 'b'], []]],
  'everything excluded': [['sync', ['a', 'b'], ['a', 'b']]],
  'an empty fleet': [['sync', [], []]],
  'an empty fleet tears down everything':
    [['sync', ['a', 'b'], []], ['sync', [], []]],

  // ── lifecycle ────────────────────────────────────────────────────────────
  'connecting starts the three collectors': [['sync', ['a'], []], ['connect', 'a']],
  'a close marks it down': [['sync', ['a'], []], ['connect', 'a'], ['close', 'a']],
  'a CLASSIFIED error is shown': [['sync', ['a'], []], ['error', 'a', 'known']],
  'an UNCLASSIFIED error is generalised': [['sync', ['a'], []], ['error', 'a', 'raw driver text']],
  'connecting after an error clears it':
    [['sync', ['a'], []], ['error', 'a', 'known'], ['connect', 'a']],
  'suspend then connect does NOT start collectors':
    [['sync', ['a'], []], ['suspend'], ['connect', 'a']],
  'resume starts a CONNECTED session':
    [['sync', ['a'], []], ['connect', 'a'], ['suspend'], ['resume']],
  'resume does NOT start a session that never connected':
    [['sync', ['a'], []], ['suspend'], ['resume']],
  'resume does NOT start a session whose link is down':
    [['sync', ['a'], []], ['connect', 'a'], ['close', 'a'], ['suspend'], ['resume']],
  // A CLOSE MUST NOT CLEAR THE ERROR. No case reached here until a mutation
  // clearing `lastError` on close survived: every existing case either closed
  // without an error or errored without closing. The page shows the reason
  // WHILE the router is down, so clearing it on the close that follows would
  // blank exactly the message it exists for.
  'an error then a close keeps the reason':
    [['sync', ['a'], []], ['error', 'a', 'known'], ['close', 'a']],
  'a connect, an error, then a close':
    [['sync', ['a'], []], ['connect', 'a'], ['error', 'a', 'known'], ['close', 'a']],
  'a torn-down session ignores a late connect':
    [['sync', ['a'], []], ['sync', [], []], ['connect', 'a']],
};

const cases = Object.entries(SCENARIOS).map(([name, steps]) => ({ name, steps, ...run(steps) }));

// BELIEVABILITY. The membership half must show both a build and a teardown, and
// the lifecycle half must show collectors both started and not started — a
// corpus of one outcome cannot see a rule that always answers the same way.
const anyStart = cases.some((c) => c.collectors.some((x) => x[2] === 'start'));
// A case that CONNECTS and starts nothing. `collectors` also records stops, so
// the test is on 'start' specifically — the first version checked the whole log
// being empty and fired on a corpus that did contain the case it was looking
// for, because a suspend had logged a stop beside it.
const anyNoStart = cases.some((c) => c.steps.some((s) => s[0] === 'connect')
  && !c.collectors.some((x) => x[2] === 'start'));
if (!anyStart) throw new Error('no case starts a collector');
if (!anyNoStart) throw new Error('no case connects WITHOUT starting collectors — the suspend and destroyed guards are unexercised');
const sizes = new Set(cases.flatMap((c) => c.trace.filter((t) => t[0] === 'sync').map((t) => t[1].length)));
if (sizes.size < 3) throw new Error('the membership cases barely vary');

const text = JSON.stringify({ generatedFrom: 'src/overviewSessions.js', cases }, null, 2) + '\n';
if (CHECK) {
  const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (cur !== text) { console.error('overview-pool-cases.json is STALE — run: node tools/overview-pool-cases.js'); process.exit(1); }
  console.log(`overview-pool-cases.json up to date (${cases.length} scenarios)`);
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, text);
  console.log(`wrote ${cases.length} scenarios -> ${path.relative(process.cwd(), OUT)}`);
}
