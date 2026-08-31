#!/usr/bin/env node
'use strict';

// Pin the connectivity-event state machine against the LIVE implementation.
//
// ── WHY THIS DRIVES THE REAL MODULE ────────────────────────────────────────
//
// `recordConnectivity` itself is three lines (src/db-writer.js:134). All of the
// value is in WHEN it is called, and that lives in `src/alertSessions.js` —
// entangled with a RouterOS client, an alerter, Settings and six collectors.
//
// The temptation is to reconstruct those rules in the generator. That is the
// trap this project has already paid for twice ("a stub is a rewrite"): a
// generator that re-implements the logic tests the reimplementation, and both
// sides agree while both are wrong.
//
// So the real module is required, with only its EDGES faked. The seam is real
// and the live code says so itself (alertSessions.js:97): a status-only session
// — `alertsEnabled: false` — constructs NO collectors, "since the ROS connection
// events alone provide Online/Offline state". So the state machine is driven
// exactly as production drives it: `ros.emit('connected')` / `'close'` in, rows
// out. Nothing about the transition rules is stubbed.
//
// ── THE CLOCK IS FAKE, AND THAT IS THE POINT ───────────────────────────────
//
// `downAt` is captured BEFORE the debounce timer and used INSIDE it, so the
// whole defect this rule fixes (#99) is invisible unless the two moments differ.
// Real timers would make the gap a few milliseconds of jitter and the assertion
// meaningless. A manual clock makes it 30_000 exactly, so a port that recorded
// the FIRING time instead of the OBSERVED time fails loudly.

const path = require('path');
const fs   = require('fs');
const Module = require('module');
const { EventEmitter } = require('events');

const SRC  = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT  = path.join(__dirname, '..', 'testdata', 'connectivity-cases.json');
const CHECK = process.argv.includes('--check');

const BASE = 1773567000000; // fixed epoch; nothing here may read the wall clock

// ── the fake edges ─────────────────────────────────────────────────────────
const srcDir = path.join(SRC, 'src');
function stub(rel, exports) {
  const file = require.resolve(rel, { paths: [srcDir] });
  const m = new Module(file, null);
  m.filename = file; m.loaded = true; m.exports = exports;
  require.cache[file] = m;
  return file;
}

// The recorder. This is the OUTPUT under test.
let rows = [], emits = [], clock = BASE;

let lastRos = null;
class FakeROS extends EventEmitter {
  constructor(opts) { super(); this.opts = opts; lastRos = this; }
  // connectLoop is called at the end of _buildSession and must not actually
  // dial. A never-settling promise mirrors "still trying" without a router.
  connectLoop() { return new Promise(() => {}); }
  // _stopSession calls stop(); a missing method here would abort the run
  // rather than silently skip teardown, which is how it was found.
  stop() {}
  close() {}
  destroy() {}
}

stub('./routeros/client', FakeROS);
stub('./alerter', {
  // Recorded but not asserted: alerts are a separate subsystem with its own
  // port. What matters here is that firing one never writes a row.
  fireConnectivityAlert() {},
});
stub('./settings', { load: () => ({}) });
stub('./db-writer', {
  recordConnectivity(routerId, connected, ts) {
    // Recorded EXACTLY as called. `ts` undefined is not the same fact as
    // `ts === clock`: the first says "the caller had no observed time and the
    // writer will default to now", the second says "the caller observed this".
    // db-writer.js:134 defaults with `ts || Date.now()`, so the ROW is the
    // same — but the port must reproduce the caller, not the default.
    rows.push({
      routerId, connected: !!connected,
      tsArg: ts === undefined ? null : ts,
      rowTs: ts === undefined ? clock : ts,
      atClock: clock,
    });
  },
});

// ── the fake clock and timer queue ─────────────────────────────────────────
let timers = [], nextTimerId = 1;
Date.now = () => clock;
global.setTimeout = (fn, ms) => {
  const t = { id: nextTimerId++, at: clock + (ms || 0), fn };
  timers.push(t);
  return t.id;
};
global.clearTimeout = (id) => { timers = timers.filter(t => t.id !== id); };

function advance(ms) {
  const target = clock + ms;
  for (;;) {
    const due = timers.filter(t => t.at <= target).sort((a, b) => a.at - b.at)[0];
    if (!due) break;
    timers = timers.filter(t => t !== due);
    clock = due.at;
    due.fn();
  }
  clock = target;
}

const _log = console.log;
console.log = () => {};   // the live 'connected' handler logs a line per connect
const sessions = require(path.join(srcDir, 'alertSessions.js'));
sessions.init({ emit: (ev, d) => emits.push({ ev, ...d, atClock: clock }) });

// ── scenarios ──────────────────────────────────────────────────────────────
// `steps` are driven against the real session. `thresh` is
// connDownThresholdSec, whose DEFAULT is 30 and whose 0 is a distinct branch.
const SCENARIOS = [
  { name: 'cold start, first connect records up',
    thresh: 30, steps: [['connect']] },

  { name: 'reconnect with no intervening down writes NOTHING',
    note: 'alertSessions.js:140 — unconditional writes on every reconnect inflate uptime for a flapping link',
    thresh: 30, steps: [['connect'], ['connect'], ['connect']] },

  { name: 'a genuine down->up transition writes both rows',
    thresh: 30, steps: [['connect'], ['close'], ['advance', 30000], ['connect']] },

  { name: 'the debounced outage records the OBSERVED time, not the firing time',
    note: 'alertSessions.js:~193 (#99) — downAt is captured before the timer and used inside it',
    thresh: 30, steps: [['connect'], ['close'], ['advance', 30000]] },

  { name: 'a flap INSIDE the debounce window writes nothing at all',
    thresh: 30, steps: [['connect'], ['close'], ['advance', 10000], ['connect'], ['advance', 60000]] },

  { name: 'a second close during the debounce does not re-arm or double-record',
    thresh: 30, steps: [['connect'], ['close'], ['advance', 5000], ['close'], ['advance', 5000], ['close'], ['advance', 30000]] },

  { name: 'COLD-START disconnect records immediately, bypassing the debounce',
    note: 'alertSessions.js:168 — _prevConnected === null takes the immediate branch',
    thresh: 30, steps: [['close'], ['advance', 60000]] },

  { name: 'cold-start disconnect then connect writes the up row',
    thresh: 30, steps: [['close'], ['advance', 1000], ['connect']] },

  { name: 'threshold 0 records immediately with no timer',
    thresh: 0, steps: [['connect'], ['close'], ['advance', 1000]] },

  { name: 'threshold 0, repeated closes DO record a row each time',
    note: 'named the other way round first, and the corpus disagreed: the '
        + '_prevConnected !== false guard is on the ALERT only. The row is '
        + 'unguarded, so a router flapping with threshold 0 writes one DOWN per '
        + 'close. Reproduced deliberately — this is live behaviour, not a bug '
        + 'to fix in a port.',
    thresh: 0, steps: [['connect'], ['close'], ['close'], ['advance', 1000]] },

  { name: 'threshold 0 full cycle down/up/down',
    thresh: 0, steps: [['connect'], ['close'], ['connect'], ['close'], ['advance', 1000]] },

  { name: 'connectionError is a disconnect exactly like close',
    thresh: 30, steps: [['connect'], ['error'], ['advance', 30000]] },

  { name: 'connect CANCELS a pending debounce timer',
    thresh: 30, steps: [['connect'], ['close'], ['advance', 29999], ['connect'], ['advance', 120000]] },

  { name: 'the debounce fires exactly AT the threshold, not after it',
    thresh: 30, steps: [['connect'], ['close'], ['advance', 29999], ['advance', 1]] },

  { name: 'a long outage records once, not once per elapsed threshold',
    thresh: 30, steps: [['connect'], ['close'], ['advance', 600000]] },

  { name: 'repeated full cycles each record a transition pair',
    thresh: 30, steps: [['connect'], ['close'], ['advance', 30000], ['connect'],
                        ['close'], ['advance', 30000], ['connect']] },

  { name: 'a custom threshold is honoured, not rounded to the default',
    thresh: 5, steps: [['connect'], ['close'], ['advance', 4999], ['advance', 1]] },

  { name: 'a long custom threshold does not fire at the default 30s',
    thresh: 120, steps: [['connect'], ['close'], ['advance', 30000], ['advance', 90000]] },
];

let routerSeq = 0;
const cases = SCENARIOS.map((sc) => {
  rows = []; emits = []; clock = BASE; timers = [];
  const id = `r${++routerSeq}`;
  // A fresh router id per scenario, so the module's own _sessions/_statusMap
  // never carry state between cases.
  // _buildSession constructs exactly one ROS client, so the most recently
  // constructed one IS this scenario's. Asserted rather than assumed.
  lastRos = null;
  sessions.syncSessions([], null, null);   // tear down anything left by the previous case
  sessions.syncSessions([{
    id, label: `Router ${routerSeq}`, host: '198.51.100.1', port: 8728,
    alertsEnabled: false, connDownThresholdSec: sc.thresh,
  }], null, null);
  if (!lastRos) throw new Error(`no ROS client was constructed for ${sc.name}`);
  const ros = lastRos;
  rows = []; emits = [];
  for (const [op, arg] of sc.steps) {
    if (op === 'connect')      ros.emit('connected');
    else if (op === 'close')   ros.emit('close');
    else if (op === 'error')   ros.emit('connectionError', new Error('x'));
    else if (op === 'advance') advance(arg);
    else throw new Error(`unknown step ${op}`);
  }
  return {
    name: sc.name, note: sc.note || null,
    thresholdSec: sc.thresh,
    steps: sc.steps,
    // Relative to BASE so the corpus carries no absolute wall-clock time.
    rows: rows.map(r => ({
      connected: r.connected,
      tsArg:  r.tsArg  === null ? null : r.tsArg - BASE,
      rowTs:  r.rowTs - BASE,
      atClock: r.atClock - BASE,
    })),
    statusEmits: emits.map(e => ({ connected: e.connected, atClock: e.atClock - BASE })),
  };
});

const payload = { generatedFrom: 'src/alertSessions.js + src/db-writer.js', base: BASE, cases };
const text = JSON.stringify(payload, null, 2) + '\n';

console.log = _log;

if (CHECK) {
  const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (cur !== text) { console.error('connectivity-cases.json is STALE — run: node tools/connectivity-cases.js'); process.exit(1); }
  console.log(`connectivity-cases.json up to date (${cases.length} cases)`);
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, text);
  console.log(`wrote ${cases.length} cases -> ${path.relative(process.cwd(), OUT)}`);
}
