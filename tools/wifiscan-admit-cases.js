'use strict';
/**
 * WHO IS ALLOWED TO START A FREQUENCY SCAN — the admission guard in
 * `src/wifiScan.js`.
 *
 * This is the one deliberately DISRUPTIVE command MikroDash issues. MikroTik's
 * own words about it, quoted in the live file's header:
 *
 *   "Running a frequency scan will disconnect all connected clients, or if the
 *    interface is in station mode, it will disconnect from the AP."
 *
 * Every check below exists because of that sentence, and the live header says so
 * outright: "the bounded duration, the wall-clock stop that does not trust the
 * router to honour it, one scan per router, and a fleet-wide cap so one operator
 * cannot walk a building disabling every AP in it."
 *
 * So the ORDER matters as much as the verdicts. A caller learns different things
 * from `busy` and `no-such-interface`, and the sequence decides which one they
 * get when both apply — an interface name that does not exist on a router
 * already scanning answers `busy`, because the running scan is the more useful
 * fact and the caller has no business enumerating interfaces through error codes.
 *
 * ---- WHY THIS IS DRIVEN THROUGH THE REAL start() --------------------------
 *
 * The guard is not a separate function: it is the first thirty lines of
 * `start()`, and there is no seam. So the module is loaded with a clock and a
 * `ros` stub, and `start` is called for real — every refusal returns before any
 * stream is opened, so a refused case touches nothing. The ONE case that is
 * admitted is included on purpose, to prove the guard can say yes; it is
 * immediately stopped.
 *
 *   docker exec -e MIKRODASH_SRC=/app mikrodash \\
 *     node /work/tools/wifiscan-admit-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const WifiScan = require(path.join(SRC, 'src', 'wifiScan.js'));

// A clock the cases drive, so a cooldown is a fact rather than a race.
let NOW = 1_000_000;
const timers = [];
const clock = {
  setTimeout: (fn) => { const t = { fn, unref() {} }; timers.push(t); return t; },
  clearTimeout: () => {},
  setInterval: (fn) => { const t = { fn, unref() {} }; timers.push(t); return t; },
  clearInterval: () => {},
};

/** A `ros` that never actually talks: every admitted scan is stopped at once. */
const ros = (connected = true) => ({
  connected,
  stream: () => ({ on() {}, cancel() {}, close() {} }),
});

const IFACES = [
  { name: 'wifi1', id: '*1', master: true, capsmanManaged: false },
  { name: 'wifi2-5GHz', id: '*2', master: true, capsmanManaged: false },
  { name: 'capsman-ap', id: '*3', master: true, capsmanManaged: true },
  { name: 'wifi1-guest', id: '*4', master: false, capsmanManaged: false },
  { name: 'no-id', id: '', master: true, capsmanManaged: false },
];

const base = (over = {}) => ({
  routerId: 'r1', ros: ros(), iface: 'wifi1', durationSec: 30,
  socketId: 'sock-1', emit() {}, interfaces: IFACES, ...over,
});

function makeRunner() {
  // `now` is its own option, separate from `clock` -- the module reads the wall
  // clock through it and schedules through the other, so a corpus that only
  // replaced one would still be racing the real clock.
  return WifiScan.createRegistry({
    clock, now: () => NOW, sanitize: (e) => String((e && e.message) || e),
  });
}

const cases = [];
function record(name, ctx, prep) {
  const runner = makeRunner();
  if (prep) prep(runner);
  const out = runner.start(ctx);
  // Never leave a scan running: an admitted one is stopped immediately, and the
  // corpus records only the VERDICT, never the scan.
  if (out.ok) runner.abortAllForRouter(ctx.routerId);
  cases.push({
    name,
    ctx: { iface: ctx.iface, durationSec: ctx.durationSec, socketId: ctx.socketId,
      hasRos: !!ctx.ros, connected: !!(ctx.ros && ctx.ros.connected),
      routerId: ctx.routerId, interfaces: ctx.interfaces === undefined ? undefined
        : ctx.interfaces === null ? null : ctx.interfaces.map((i) => i.name) },
    out: { ok: !!out.ok, code: out.code || null, message: out.message || null,
      iface: out.iface || null, hasRetryAt: out.retryAt !== undefined },
  });
}

// ---- the refusals, in the order the guard applies them --------------------
record('no routerId', base({ routerId: '' }));
record('no ros', base({ ros: null }));
record('interface is not a string', base({ iface: 42 }));
record('interface is empty', base({ iface: '' }));
record('interface has a slash', base({ iface: 'wifi1/../etc' }));
record('interface has a quote', base({ iface: 'wifi1"' }));
record('interface has a newline', base({ iface: 'wifi1\nX' }));
record('interface is 64 characters', base({ iface: 'a'.repeat(64) }));
record('interface is 65 characters', base({ iface: 'a'.repeat(65) }));
record('duration is not offered', base({ durationSec: 15 }));
record('duration is a string', base({ durationSec: '30' }));
record('duration is zero', base({ durationSec: 0 }));
record('duration 30 is offered', base({ durationSec: 30 }));
record('duration 120 is offered', base({ durationSec: 120 }));
record('interfaces not yet known', base({ interfaces: null }));
record('interfaces undefined', base({ interfaces: undefined }));
record('interface not in the catalogue', base({ iface: 'wifi9' }));
record('interface has no id', base({ iface: 'no-id' }));
record('interface is capsman managed', base({ iface: 'capsman-ap' }));
record('interface is not a master radio', base({ iface: 'wifi1-guest' }));
record('router is offline', base({ ros: ros(false) }));

// ---- the stateful ones ----------------------------------------------------
record('a scan is already running on this router', base(), (r) => {
  r.start(base({ iface: 'wifi2-5GHz' }));
});
record('the fleet cap is reached', base({ routerId: 'r4' }), (r) => {
  r.start(base({ routerId: 'r1' }));
  r.start(base({ routerId: 'r2' }));
  r.start(base({ routerId: 'r3' }));
});
record('the fleet is one short of the cap', base({ routerId: 'r3' }), (r) => {
  r.start(base({ routerId: 'r1' }));
  r.start(base({ routerId: 'r2' }));
});

// ---- BELIEVABILITY -------------------------------------------------------
{
  const by = Object.fromEntries(cases.map((c) => [c.name, c.out]));

  // The guard must be able to say YES, or every verdict below is vacuous.
  assert.equal(by['duration 30 is offered'].ok, true,
    'a valid request was refused — every other case here proves nothing');
  assert.equal(by['duration 120 is offered'].ok, true, '120s is an offered duration');
  assert.equal(by['the fleet is one short of the cap'].ok, true,
    'the third concurrent scan was refused — the cap is off by one');

  // ...and it must refuse each thing for its OWN reason.
  const codes = new Set(cases.filter((c) => !c.out.ok).map((c) => c.out.code));
  for (const want of ['unavailable', 'bad-request', 'busy', 'fleet-busy',
                      'no-such-interface', 'capsman-managed', 'not-a-radio', 'router-offline']) {
    assert.ok(codes.has(want), 'no case produced the code ' + want);
  }

  assert.equal(by['interface is 64 characters'].code, 'no-such-interface',
    'a 64-character name failed the PATTERN — the limit is 64 inclusive, and it should '
    + 'get as far as the catalogue lookup');
  assert.equal(by['interface is 65 characters'].code, 'bad-request',
    'a 65-character name was accepted by the pattern');
  assert.equal(by['duration is a string'].code, 'bad-request',
    '"30" was accepted — the live check is `DURATIONS.includes`, which is strict');

  // Order: a busy router answers `busy` even when the interface is nonsense.
  assert.equal(by['a scan is already running on this router'].code, 'busy',
    'a router already scanning did not answer busy');
  assert.ok(by['a scan is already running on this router'].iface,
    'the busy answer does not say which interface is being scanned');
}

const OUT = path.join(ROOT, 'testdata', 'wifiscan-admit-cases.json');
const payload = JSON.stringify({
  note: 'GENERATED by tools/wifiscan-admit-cases.js from the live src/wifiScan.js. Do not edit.',
  fleetCap: WifiScan.FLEET_CAP,
  // COOLDOWN_MS is NOT exported by the live module, so it cannot be lifted the
  // way the other three are. The Go side carries its own constant and this
  // corpus does not claim to pin it -- recorded as a gap rather than a number
  // typed here that would look pinned and not be.
  cooldownMsIsNotExported: true,
  durations: WifiScan.DURATIONS, maxChannels: WifiScan.MAX_CHANNELS,
  cases,
}, null, 1) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== payload) {
    console.error('wifiscan-admit-cases: testdata/wifiscan-admit-cases.json is stale — re-run without --check');
    process.exit(1);
  }
  console.log('wifiscan-admit-cases: up to date (' + cases.length + ' cases)');
} else {
  fs.writeFileSync(OUT, payload);
  console.log('wifiscan-admit-cases: wrote ' + cases.length + ' admission cases');
}
