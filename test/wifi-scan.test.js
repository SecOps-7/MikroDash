'use strict';
// The WiFi frequency scan runner (src/wifiScan.js).
//
// This module takes a radio off the air, so almost every test here is about a
// way it could FAIL to put it back. The fleet has no spare AP to disrupt, so
// this file is the correctness story rather than a supplement to manual testing.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const EventEmitter = require('events');

const { createRegistry, parseRow, classifyTrap, freqToChannel } = require('../src/wifiScan');

// ── harness ──────────────────────────────────────────────────────────────────

// A clock we drive by hand, so a "30 second" scan takes no wall-clock time and
// the timer bookkeeping is directly observable.
function fakeClock() {
  let t = 0;
  const timers = [];
  return {
    now: () => t,
    live: () => timers.filter(x => !x.cleared).length,
    api: {
      setTimeout:  (fn, ms) => { const x = { fn, at: t + ms, cleared: false, repeat: 0 }; timers.push(x); return x; },
      setInterval: (fn, ms) => { const x = { fn, at: t + ms, cleared: false, repeat: ms }; timers.push(x); return x; },
      clearTimeout:  (x) => { if (x) x.cleared = true; },
      clearInterval: (x) => { if (x) x.cleared = true; },
    },
    advance(ms) {
      const end = t + ms;
      for (;;) {
        const due = timers.filter(x => !x.cleared && x.at <= end).sort((a, b) => a.at - b.at)[0];
        if (!due) break;
        t = due.at;
        if (due.repeat) due.at = t + due.repeat; else due.cleared = true;
        due.fn();
      }
      t = end;
    },
  };
}

function fakeRos() {
  const stream = new EventEmitter();
  stream.stopped = 0;
  stream.stop = () => { stream.stopped++; return Promise.resolve(); };
  return {
    connected: true,
    calls: [],
    streams: [stream],
    stream(words, cb) {
      this.calls.push({ words, cb });
      this.lastCb = cb;
      // A retry gets a fresh stream, mirroring the driver.
      if (this.calls.length > 1) {
        const s = new EventEmitter();
        s.stopped = 0; s.stop = () => { s.stopped++; return Promise.resolve(); };
        this.streams.push(s);
        return s;
      }
      return this.streams[0];
    },
  };
}

const IFACES = [
  { name: 'wifi1',  id: '*45', master: true,  capsmanManaged: false },
  { name: 'wifi2',  id: '*42', master: true,  capsmanManaged: false },
  { name: 'guest',  id: '*46', master: false, capsmanManaged: false },
  { name: 'capped', id: '*47', master: true,  capsmanManaged: true  },
];

function harness(over = {}) {
  const clock = fakeClock();
  const reg = createRegistry({ clock: clock.api, now: clock.now, flushMs: 100, hardStopGraceMs: 5000 });
  const ros = fakeRos();
  const events = [];
  const emit = (ev, d) => events.push({ ev, d });
  const startArgs = Object.assign({
    routerId: 'r1', ros, iface: 'wifi1', durationSec: 30,
    socketId: 's1', emit, interfaces: IFACES,
  }, over);
  return { clock, reg, ros, events, emit, startArgs,
           start: (o) => reg.start(Object.assign({}, startArgs, o)),
           last: (ev) => events.filter(e => e.ev === ev).pop() };
}

const row = (ch, over = {}) => Object.assign({
  channel: String(ch), networks: '2', load: '40', nf: '-95',
  'max-signal': '-60', 'min-signal': '-80',
}, over);

// ── row parsing ──────────────────────────────────────────────────────────────

test('a compound channel keeps its raw form and yields the leading MHz', () => {
  // /interface/wifi/monitor returns "2427/ax/Ce" on the live fleet, so the scan
  // reporting the same shape is entirely plausible and must not become NaN.
  const r = parseRow(row('5180/ax/Ceee'));
  assert.strictEqual(r.ch, 5180);
  assert.strictEqual(r.chRaw, '5180/ax/Ceee');
});

test('a row with no usable channel is dropped, not rendered as NaN', () => {
  // A bar at x=NaN silently vanishes, which looks like a channel that was never
  // scanned rather than one that failed to parse.
  assert.strictEqual(parseRow(row('garbage')), null);
  assert.strictEqual(parseRow({}), null);
  assert.strictEqual(parseRow(null), null);
});

test('absent numbers are null rather than NaN, and flags are real booleans', () => {
  const r = parseRow({ channel: '2412', primary: 'true' });
  assert.strictEqual(r.maxSig, null);
  assert.strictEqual(r.nets, null);
  assert.strictEqual(r.primary, true);
  assert.strictEqual(r.secondary, false);
});

test('frequencies map to the channel numbers operators talk in', () => {
  assert.strictEqual(freqToChannel(2412), 1);
  assert.strictEqual(freqToChannel(2437), 6);
  assert.strictEqual(freqToChannel(2472), 13);
  assert.strictEqual(freqToChannel(2484), 14, 'Japan, and not on the /5 grid');
  assert.strictEqual(freqToChannel(5180), 36);
  assert.strictEqual(freqToChannel(5745), 149);
  // 6GHz reuses 5GHz numbering from its own base, so it must be matched first.
  assert.strictEqual(freqToChannel(5955), 1);
  assert.strictEqual(freqToChannel(6175), 45);
});

test('a frequency outside the known bands has no channel number', () => {
  // Better an honest "5905 MHz" with no number than a plausible-looking wrong
  // one; an operator acts on a channel number.
  for (const f of [5905, 2413, 100, 9999, 0, -5, null, undefined, NaN, '2412']) {
    assert.strictEqual(freqToChannel(f), null, String(f));
  }
});

test('a parsed row carries its channel number alongside the frequency', () => {
  assert.strictEqual(parseRow(row(5180)).chNum, 36);
  assert.strictEqual(parseRow(row(5905)).chNum, null, 'unknown band still yields a row');
  assert.strictEqual(parseRow(row(5905)).ch, 5905);
});

test('trap text maps to codes the browser can phrase', () => {
  assert.strictEqual(classifyTrap('not enough privileges'),      'permission-denied');
  assert.strictEqual(classifyTrap('no such command prefix'),     'unsupported-stack');
  assert.strictEqual(classifyTrap('no such item (4)'),           'no-such-interface');
  assert.strictEqual(classifyTrap('unknown parameter duration'), 'bad-parameter');
  assert.strictEqual(classifyTrap('something else entirely'),    'router-error');
});

// ── the command on the wire ──────────────────────────────────────────────────

test('the scan is issued as a stream with a null callback', () => {
  const h = harness();
  assert.strictEqual(h.start().ok, true);

  const { words, cb } = h.ros.calls[0];
  assert.strictEqual(words[0], '/interface/wifi/frequency-scan');
  // =.id=, not =number=. The manual documents `number`; the binary API rejects
  // it with "missing =.id=".
  assert.ok(words.includes('=.id=*45'), 'addressed by RouterOS id: ' + JSON.stringify(words));
  // Without a proplist RouterOS answers every freeze-frame with a bare !empty
  // and never sends a row. This one line is the difference between 25 seconds of
  // silence and 234 rows.
  assert.ok(words.some(w => w.startsWith('=.proplist=')), 'explicit proplist required');
  assert.ok(words.includes('=duration=00:00:30'), 'hh:mm:ss, unambiguous across builds');
  assert.ok(words.includes('=freeze-frame-interval=00:00:01'));

  // Rows arrive through the CALLBACK. On a streaming channel node-routeros
  // emits !re as 'stream' and 'data' never fires, so a 'data' listener receives
  // nothing at all for this command.
  assert.strictEqual(typeof cb, 'function', 'callback form, or no rows ever arrive');
});

// ── coalescing ───────────────────────────────────────────────────────────────

test('repeated rows for one channel collapse to the latest', () => {
  // A 10s scan at a 1s freeze-frame visits every channel several times. Without
  // keying on channel the table grows a duplicate bar per sweep.
  const h = harness();
  h.start();
  h.ros.lastCb(null, row(2412, { load: '10' }));
  h.ros.lastCb(null, row(2412, { load: '55' }));
  h.ros.lastCb(null, row(2437, { load: '20' }));
  h.clock.advance(100);

  const rows = h.last('wifiscan:rows').d.rows;
  assert.strictEqual(rows.length, 2, 'two channels, not three rows');
  assert.strictEqual(rows.find(r => r.ch === 2412).load, 55, 'latest wins');
});

test('rows are flushed on a timer, not once per sentence', () => {
  const h = harness();
  h.start();
  const s = h.ros.streams[0];
  for (let i = 0; i < 20; i++) h.ros.lastCb(null, row(2412 + i * 5));
  assert.strictEqual(h.events.filter(e => e.ev === 'wifiscan:rows').length, 0, 'nothing yet');
  h.clock.advance(100);
  assert.strictEqual(h.events.filter(e => e.ev === 'wifiscan:rows').length, 1, 'one frame for twenty rows');
});

// ── termination ──────────────────────────────────────────────────────────────

test('a scan that ends by itself is not also cancelled', () => {
  // RStream.stop() opens a NEW channel to write /cancel with a stale tag. Doing
  // that to a device that has just finished scanning is a pointless extra write.
  const h = harness();
  h.start();
  h.ros.lastCb(null, row(2412));
  const s = h.ros.streams[0];
  s.emit('done');

  assert.strictEqual(h.last('wifiscan:done').d.reason, 'complete');
  assert.strictEqual(s.stopped, 0, 'stop() not called on natural completion');
  assert.strictEqual(h.reg.size(), 0, 'registry entry released');
});

test('a router that ignores duration is stopped by the dashboard', () => {
  // This is the normal path on the wifi stack, not an edge case: a live 7.23.3
  // hAP AX3 keeps streaming freeze-frames past =duration=, so our wall-clock
  // stop is what ends every scan. It reports 'complete' for that reason —
  // labelling it a timeout would warn the user about every successful scan.
  const h = harness();
  h.start();
  const s = h.ros.streams[0];
  h.ros.lastCb(null, row(2412));

  h.clock.advance(30_000 + 5000 + 1);
  const done = h.last('wifiscan:done').d;
  assert.strictEqual(done.reason, 'complete');
  assert.strictEqual(done.rows.length, 1, 'the rows collected are still valid results');
  assert.strictEqual(s.stopped, 1, 'and the radio is released');
  assert.strictEqual(h.reg.size(), 0);
});

test('a done arriving after the hard stop is ignored', () => {
  const h = harness();
  h.start();
  h.clock.advance(40_000);
  const after = h.events.length;
  h.ros.streams[0].emit('done');
  assert.strictEqual(h.events.length, after, 'no second terminal frame');
});

test('every terminal path releases the registry entry and its timers', () => {
  for (const finish of [
    (h) => h.ros.streams[0].emit('done'),
    (h) => h.clock.advance(40_000),   // past the 30s duration + 5s grace
    (h) => h.reg.abort('r1'),
    (h) => h.reg.abortByOwner('s1'),
    (h) => h.reg.abortAllForRouter('r1'),
    (h) => { h.ros.connected = false; h.clock.advance(100); },
    (h) => h.ros.streams[0].emit('error', new Error('boom')),
  ]) {
    const h = harness();
    h.start();
    finish(h);
    assert.strictEqual(h.reg.size(), 0, 'registry drained');
    assert.strictEqual(h.clock.live(), 0, 'no timer left running');
    assert.ok(h.last('wifiscan:done'), 'the browser was told it ended');
  }
});

test('a router that goes away mid-scan ends the scan', () => {
  // Otherwise the entry sits until the hard stop, blocking a retry against a
  // router that has already rebooted.
  const h = harness();
  h.start();
  h.ros.connected = false;
  h.clock.advance(100);
  assert.strictEqual(h.last('wifiscan:done').d.reason, 'disconnected');
});

// ── the duration retry ───────────────────────────────────────────────────────

test('a build that rejects duration is retried once without it', () => {
  const h = harness();
  h.start();
  h.ros.streams[0].emit('error', new Error('unknown parameter duration'));

  assert.strictEqual(h.ros.calls.length, 2, 'retried');
  assert.ok(!h.ros.calls[1].words.some(w => w.startsWith('=duration=')),
    'second attempt drops duration and leans on the wall-clock stop');
  assert.ok(!h.last('wifiscan:done'), 'and the scan is still running');
});

test('a second duration rejection is fatal rather than a retry loop', () => {
  const h = harness();
  h.start();
  h.ros.streams[0].emit('error', new Error('unknown parameter duration'));
  h.ros.streams[1].emit('error', new Error('unknown parameter duration'));

  assert.strictEqual(h.ros.calls.length, 2, 'no third attempt');
  assert.strictEqual(h.last('wifiscan:done').d.reason, 'error');
});

test('a privilege trap is reported as such, not as a generic failure', () => {
  const h = harness();
  h.start();
  h.ros.streams[0].emit('error', new Error('not enough privileges (9)'));
  assert.strictEqual(h.last('wifiscan:error').d.code, 'permission-denied');
});

// ── concurrency ──────────────────────────────────────────────────────────────

test('one scan per router, and the refusal says what is running', () => {
  // Not one per interface: two radios scanning at once on a small board is the
  // load #105 exists to avoid, and the blast radius doubles.
  const h = harness();
  h.start();
  const second = h.start({ iface: 'wifi2', socketId: 's2' });
  assert.strictEqual(second.ok, false);
  assert.strictEqual(second.code, 'busy');
  assert.strictEqual(second.iface, 'wifi1', 'names the scan already running');
  assert.strictEqual(h.ros.calls.length, 1, 'the router was not asked twice');
});

test('a different router may scan concurrently, up to the fleet cap', () => {
  // One operator must not be able to walk a fleet disabling every AP in it.
  const h = harness();
  assert.strictEqual(h.start({ routerId: 'r1', ros: fakeRos() }).ok, true);
  assert.strictEqual(h.start({ routerId: 'r2', ros: fakeRos() }).ok, true);
  assert.strictEqual(h.start({ routerId: 'r3', ros: fakeRos() }).ok, true);
  const fourth = h.start({ routerId: 'r4', ros: fakeRos() });
  assert.strictEqual(fourth.ok, false);
  assert.strictEqual(fourth.code, 'fleet-busy');
});

test('a socket must wait before scanning again', () => {
  const h = harness();
  h.start();
  h.ros.streams[0].emit('done');
  const again = h.start({ ros: fakeRos() });
  assert.strictEqual(again.ok, false);
  assert.strictEqual(again.code, 'cooldown');

  h.clock.advance(10_001);
  assert.strictEqual(h.start({ ros: fakeRos() }).ok, true);
});

// ── validation, all before the router is touched ─────────────────────────────

test('bad input is refused without contacting the router', () => {
  for (const [over, code] of [
    [{ iface: '' },                       'bad-request'],
    [{ iface: 'a'.repeat(65) },           'bad-request'],
    [{ iface: '=x' },                     'bad-request'],
    [{ iface: 'wifi1;reboot' },           'bad-request'],
    [{ iface: '../etc' },                 'bad-request'],
    [{ iface: null },                     'bad-request'],
    [{ durationSec: 0 },                  'bad-request'],
    [{ durationSec: 15 },                 'bad-request'],
    [{ durationSec: '30' },               'bad-request'],
    // Withdrawn deliberately: the first rows did not arrive until 7.3s on a live
    // router, so these took the radio off the air and returned almost nothing.
    [{ durationSec: 5 },                  'bad-request'],
    [{ durationSec: 10 },                 'bad-request'],
    [{ iface: 'nope' },                   'no-such-interface'],
    [{ iface: 'capped' },                 'capsman-managed'],
    [{ iface: 'guest' },                  'not-a-radio'],
    [{ interfaces: null },                'unavailable'],
  ]) {
    const h = harness();
    const r = h.start(over);
    assert.strictEqual(r.ok, false, JSON.stringify(over));
    assert.strictEqual(r.code, code, JSON.stringify(over));
    assert.strictEqual(h.ros.calls.length, 0, 'router untouched for ' + JSON.stringify(over));
  }
});

test('a virtual AP cannot be scanned', () => {
  // The live fleet reports 12 wifi interfaces of which only 4 are radios; the
  // rest are virtual APs with no radio of their own.
  const h = harness();
  assert.strictEqual(h.start({ iface: 'guest' }).code, 'not-a-radio');
});

test('an offline router is refused before the stream is opened', () => {
  const h = harness();
  h.ros.connected = false;
  assert.strictEqual(h.start().code, 'router-offline');
  assert.strictEqual(h.ros.calls.length, 0);
});

// ── state replay ─────────────────────────────────────────────────────────────

test('a scan in progress can be described to a browser that just opened', () => {
  const h = harness();
  h.start();
  h.ros.lastCb(null, row(2412));
  const st = h.reg.stateFor('r1');
  assert.strictEqual(st.scanning, true);
  assert.strictEqual(st.iface, 'wifi1');
  assert.strictEqual(st.rows.length, 1);
  assert.strictEqual(h.reg.stateFor('nope'), null);
});
