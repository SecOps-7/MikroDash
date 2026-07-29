'use strict';
// Regression tests for the 2026-07-26 code-review remediation batches:
// connectLoop listener containment, connections suspend/watchdog, traffic
// bindSocket idempotency, empty-table stream packets, ping restart-timer
// cleanup, router input validation, and credential ciphertext preservation.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const EventEmitter = require('events');
const os   = require('os');
const fs   = require('fs');
const path = require('path');

const ROS                  = require('../src/routeros/client');
const ConnectionsCollector = require('../src/collectors/connections');
const TrafficCollector     = require('../src/collectors/traffic');
const InterfaceStatusCollector = require('../src/collectors/interfaceStatus');
const TopTalkersCollector  = require('../src/collectors/talkers');
const WirelessCollector    = require('../src/collectors/wireless');
const PingCollector        = require('../src/collectors/ping');

function makeTmpDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'mikrodash-remed-'));
}

// io stub matching the buildRouterIo surface collectors rely on.
function stubIo(clients) {
  const io = {
    engine: { clientsCount: clients },
    emit() {},
    to() { return { emit() {}, to() { return { emit() {} }; } }; },
    on() {},
    sockets: { adapter: { rooms: { get() { return undefined; } } } },
  };
  return io;
}

// ros mock whose stream() returns an EventEmitter so tests can fire data/error.
function mockStreamRos() {
  const ros = new EventEmitter();
  ros.connected = true;
  ros.write = async () => [];
  ros.streams = [];
  ros.streamsByCmd = {};
  ros.stream = (words) => {
    const cmd = Array.isArray(words) ? words[0] : words;
    const s = new EventEmitter();
    s.stop = () => Promise.resolve();
    ros.streams.push(s);
    ros.streamsByCmd[cmd] = s;
    return s;
  };
  return ros;
}

// Patches setTimeout/clearTimeout + setInterval/clearInterval, recording timers.
async function withPatchedTimers(runTest) {
  const o = [global.setInterval, global.clearInterval, global.setTimeout, global.clearTimeout];
  const timers = [];
  global.setInterval  = (cb, ms) => { const t = { cb, ms, cleared: false, isInterval: true  }; timers.push(t); return t; };
  global.setTimeout   = (cb, ms) => { const t = { cb, ms, cleared: false, isInterval: false }; timers.push(t); return t; };
  global.clearInterval = (t) => { if (t && typeof t === 'object') t.cleared = true; };
  global.clearTimeout  = (t) => { if (t && typeof t === 'object') t.cleared = true; };
  try { await runTest(timers); }
  finally { [global.setInterval, global.clearInterval, global.setTimeout, global.clearTimeout] = o; }
}

// ── ROS client: router label is sanitised for log safety ────────────────────

test('routerLabel strips control characters so a label cannot forge log lines', () => {
  const ros = new ROS({ host: 'h', username: 'u', password: 'p' });
  ros.routerLabel = 'hAP AX3\n2026-01-01 00:00:00 [FAKE] forged line';
  assert.ok(!ros.routerLabel.includes('\n'), 'newline stripped');
  assert.equal(ros.routerLabel, 'hAP AX32026-01-01 00:00:00 [FAKE] forged line');
  ros.routerLabel = 'a\r\tb\x00c\x7f';
  assert.equal(ros.routerLabel, 'abc');
});

test('routerLabel strips % so it can never act as a format specifier', () => {
  const ros = new ROS({ host: 'h', username: 'u', password: 'p' });
  ros.routerLabel = 'router %s %d %j';
  assert.equal(ros.routerLabel, 'router s d j');
  assert.ok(!ros.routerLabel.includes('%'));
  // A legitimate label keeps its text, minus the percent sign.
  ros.routerLabel = 'Site 50% Backup';
  assert.equal(ros.routerLabel, 'Site 50 Backup');
});

test('routerLabel passes ordinary labels through unchanged and handles null', () => {
  const ros = new ROS({ host: 'h', username: 'u', password: 'p' });
  ros.routerLabel = 'Mikrotik hAP AX3';
  assert.equal(ros.routerLabel, 'Mikrotik hAP AX3');
  // Collectors do `ros.routerLabel ? ... : '[tag]'`, so an unset label must stay falsy.
  const fresh = new ROS({ host: 'h', username: 'u', password: 'p' });
  assert.ok(!fresh.routerLabel, 'unset label is falsy');
  ros.routerLabel = null;
  assert.equal(ros.routerLabel, '');
});

test('ROS stream supports command, params array, callback without dropping words', () => {
  const ros = new ROS({});
  ros.connected = true;
  let forwardedWords = null;
  let forwardedCb = null;
  ros.conn = {
    stream(words, cb) {
      forwardedWords = words;
      forwardedCb = cb;
      return { stop() {} };
    },
  };
  const cb = () => {};
  ros.stream('/interface/monitor-traffic', ['=interface=wan', '=interval=1'], cb);
  assert.deepEqual(forwardedWords, ['/interface/monitor-traffic', '=interface=wan', '=interval=1']);
  assert.equal(forwardedCb, cb);
});

// ── ROS client: listener exceptions must not kill connectLoop ────────────────

test('a throwing connectionError listener does not kill connectLoop', async () => {
  const ros = new ROS({ host: 'h', username: 'u', password: 'p' });
  let attempts = 0;
  ros._buildConn = () => {
    attempts++;
    const conn = new EventEmitter();
    conn.connect = async () => { throw new Error('refused'); };
    conn.close = () => {};
    return conn;
  };
  ros.on('connectionError', () => { throw new Error('listener boom'); });
  ros._sleep = async () => { if (attempts >= 3) ros._stopping = true; };
  await assert.doesNotReject(ros.connectLoop());
  assert.ok(attempts >= 3, 'loop kept retrying after listener threw');
});

test('a throwing close listener does not crash the conn close path', async () => {
  const ros = new ROS({ host: 'h', username: 'u', password: 'p' });
  let conn;
  ros._buildConn = () => {
    conn = new EventEmitter();
    conn.connect = async () => {};
    conn.close = () => {};
    return conn;
  };
  ros.on('close', () => { throw new Error('close listener boom'); });
  const loop = ros.connectLoop();
  await new Promise(r => setImmediate(r));
  assert.doesNotThrow(() => conn.emit('close'));
  ros.stop();
  await loop;
});

// ── Connections: suspend/resume/watchdog ─────────────────────────────────────

function makeConns(ros, io) {
  return new ConnectionsCollector({
    ros, io, pollMs: 3000, topN: 5, maxConns: 1000,
    dhcpNetworks: {}, dhcpLeases: {}, arp: {}, state: {},
    connTableCache: { deposit() {}, latestWithTs() { return { rows: [], ts: 0 }; }, invalidate() {} },
    geoOrgCache: { geo: new Map(), org: new Map() },
    streamMode: true,
  });
}

test('connections resume() does not open the stream with zero clients', () => {
  const ros = mockStreamRos();
  const io  = stubIo(0);
  const c   = makeConns(ros, io);
  c.start();
  c.resume();
  assert.equal(c._stream, null, 'no viewers → stream stays closed');
  c.stop();
});

test('connections resume() opens the stream when viewers exist', () => {
  const ros = mockStreamRos();
  const io  = stubIo(1);
  const c   = makeConns(ros, io);
  c.start();
  c.resume();
  assert.ok(c._stream, 'viewers present → stream opens');
  c.stop();
});

test('connections watchdog recovers a dead stream', async () => {
  await withPatchedTimers(async (timers) => {
    const ros = mockStreamRos();
    const io  = stubIo(1);
    const c   = makeConns(ros, io);
    c.start();
    c.resume();
    assert.ok(c._stream);
    // Simulate silent stream death where the 3 s restart never landed.
    c._stream = null;
    c._restarting = false;
    const watchdog = timers.find(t => t.isInterval && !t.cleared);
    assert.ok(watchdog, 'watchdog interval armed');
    watchdog.cb();
    assert.ok(c._stream, 'watchdog reopened the missing stream');
    c.stop();
  });
});

test('connections suspend() blocks the pending error-restart', async () => {
  await withPatchedTimers(async (timers) => {
    const ros = mockStreamRos();
    const io  = stubIo(1);
    const c   = makeConns(ros, io);
    c.start();
    c.resume();
    ros.streams[0].emit('error', new Error('stream blew up'));
    const restart = timers.find(t => !t.isInterval && t.ms === 3000);
    assert.ok(restart, 'error scheduled a 3 s restart');
    c.suspend();
    assert.ok(restart.cleared, 'suspend cancelled the pending restart');
    c.stop();
  });
});

// ── Traffic: bindSocket idempotency and stop() release ───────────────────────

test('traffic bindSocket attaches listeners once per socket; unbind/stop release them', () => {
  const ros = new EventEmitter();
  ros.connected = false; // no stream needed for this test
  const t = new TrafficCollector({ ros, io: stubIo(0), defaultIf: 'ether1', historyMinutes: 1, pollMs: 1000, state: {} });

  const sock = new EventEmitter();
  sock.id = 's1';
  t.bindSocket(sock);
  t.bindSocket(sock);
  t.bindSocket(sock);
  assert.equal(sock.listenerCount('traffic:select'), 1, 'no listener stacking across re-binds');
  assert.equal(sock.listenerCount('disconnect'), 1);

  t.unbindSocket(sock);
  assert.equal(sock.listenerCount('traffic:select'), 0, 'unbindSocket detaches');
  assert.equal(t.subscriptions.size, 0);

  t.bindSocket(sock);
  t.stop();
  assert.equal(sock.listenerCount('traffic:select'), 0, 'stop() releases socket listeners');
  assert.equal(t.subscriptions.size, 0);
  assert.equal(t._boundSockets.size, 0);
});

test('traffic watchdog restarts a silently stalled stream', async () => {
  await withPatchedTimers(async (timers) => {
    const ros = mockStreamRos();
    const t = new TrafficCollector({ ros, io: stubIo(0), defaultIf: 'wan', historyMinutes: 1, pollMs: 1000, state: {} });
    t.start();
    const first = t._allStream;
    t._streamStartTs = Date.now() - 11000;
    t._lastDataTs = 0;
    const watchdog = timers.find(timer => timer.isInterval && timer.ms === 5000 && !timer.cleared);
    assert.ok(watchdog, 'traffic watchdog armed');
    watchdog.cb();
    assert.notEqual(t._allStream, first, 'stalled stream replaced');
    t.stop();
  });
});

test('traffic stream accepts numeric zero counters as a healthy idle sample', () => {
  const ros = mockStreamRos();
  const state = {};
  const t = new TrafficCollector({ ros, io: stubIo(0), defaultIf: 'wan', historyMinutes: 1, pollMs: 1000, state });
  t.start();
  t._allStream.emit('data', { 'rx-bits-per-second': 0, 'tx-bits-per-second': 0 });
  assert.ok(state.lastTrafficTs > 0, 'idle zero-rate packet advances freshness');
  t.stop();
});

test('interface rate poll records errors without advancing freshness', async () => {
  const state = { lastIfStatusTs: 123 };
  const ros = new EventEmitter();
  ros.connected = true;
  ros.write = async () => { throw new Error('monitor failed'); };
  const collector = new InterfaceStatusCollector({ ros, io: stubIo(1), pollMs: 1000, state, streamMode: false });
  collector._ifaces.set('wan', { name: 'wan', disabled: false });
  await collector._pollRatesOnce();
  assert.equal(state.lastIfStatusTs, 123, 'failed poll does not look fresh');
  assert.match(state.lastIfStatusErr, /monitor failed/);
});

// ── Empty-table stream packets ───────────────────────────────────────────────

test('talkers clears the device list when the stream reports an empty table', () => {
  const ros = mockStreamRos();
  const tk  = new TopTalkersCollector({ ros, io: stubIo(1), pollMs: 3000, state: {}, topN: 5, streamMode: true });
  tk._startStream();
  const s = ros.streams[0];
  s.emit('data', { 'mac-address': 'AA:BB:CC:DD:EE:FF', name: 'kid', 'rate-up': '100', 'rate-down': '200' });
  assert.equal(tk._devicesNext.size, 1);
  s.emit('data', []); // RStream debounce-empty packet: table is now empty
  assert.equal(tk._devicesNext.size, 0, 'empty packet cleared pending devices');
  tk._stopStream();
});

test('wireless latches legacy fallback when the wifi table is empty (not just on error)', () => {
  const ros = mockStreamRos();
  const w   = new WirelessCollector({
    ros, io: stubIo(1), pollMs: 30000, state: {},
    dhcpLeases: { getNameByMAC: () => null }, arp: {},
  });
  w._startStream('wifi');
  const wifiStream = ros.streamsByCmd['/interface/wifi/registration-table/print'];
  assert.ok(wifiStream);
  wifiStream.emit('data', []); // empty first batch
  assert.equal(w.mode, 'wireless', 'empty wifi table latched legacy mode');
  assert.ok(ros.streamsByCmd['/interface/wireless/registration-table/print'], 'legacy stream opened');
  w.stop();
});

// ── Ping: pending error-restart cancelled by stop() ──────────────────────────

test('ping stop() cancels the pending stream error-restart timer', async () => {
  await withPatchedTimers(async (timers) => {
    const ros = mockStreamRos();
    const p   = new PingCollector({ ros, io: stubIo(1), pollMs: 5000, state: {}, target: '1.1.1.1', streamMode: true });
    p._startStream();
    ros.streams[0].emit('error', new Error('stream died'));
    const restart = timers.find(t => !t.isInterval && t.ms === 3000);
    assert.ok(restart, 'error scheduled a 3 s restart');
    p.stop();
    assert.ok(restart.cleared, 'stop() cancelled the pending restart');
  });
});

// ── Routers: input validation + ciphertext preservation ──────────────────────

function freshRouters(tmpDir) {
  process.env.DATA_DIR = tmpDir;
  delete require.cache[require.resolve('../src/routers')];
  delete require.cache[require.resolve('../src/settings')];
  return require('../src/routers');
}

test('routers add()/update() reject invalid pingTarget and defaultIf', () => {
  const tmp = makeTmpDir();
  try {
    const R = freshRouters(tmp);
    assert.throws(() => R.add({ host: '192.168.1.1', pingTarget: 'not-an-ip' }), /pingTarget/);
    assert.throws(() => R.add({ host: '192.168.1.1', defaultIf: 'bad if!' }), /defaultIf/);
    const ok = R.add({ host: '192.168.1.1', pingTarget: '8.8.8.8', defaultIf: 'ether1', password: 'pw' });
    assert.ok(ok.id);
    assert.throws(() => R.update(ok.id, { pingTarget: 'still-not-an-ip' }), /pingTarget/);
    assert.equal(R.getById(ok.id).pingTarget, '8.8.8.8', 'failed update left the router unchanged');
  } finally {
    delete process.env.DATA_DIR;
  }
});

test('routers: undecryptable password ciphertext survives unrelated edits', () => {
  const tmp = makeTmpDir();
  try {
    let R = freshRouters(tmp);
    const r = R.add({ host: '192.168.1.1', password: 'supersecret' });
    const file = path.join(tmp, 'routers.json');

    // Corrupt the stored ciphertext (simulates key mismatch after rotation).
    const onDisk = JSON.parse(fs.readFileSync(file, 'utf8'));
    onDisk[0].password = 'bm90LWRlY3J5cHRhYmxl'; // valid base64, wrong format/key
    fs.writeFileSync(file, JSON.stringify(onDisk));

    R = freshRouters(tmp); // re-read from disk with empty caches
    assert.equal(R.getById(r.id).password, '', 'undecryptable password reads as empty');

    R.update(r.id, { label: 'renamed' }); // unrelated edit
    const after = JSON.parse(fs.readFileSync(file, 'utf8'));
    assert.equal(after[0].password, 'bm90LWRlY3J5cHRhYmxl', 'original ciphertext preserved on save');

    R.update(r.id, { password: 'newsecret' }); // explicit new password wins
    const replaced = JSON.parse(fs.readFileSync(file, 'utf8'));
    assert.notEqual(replaced[0].password, 'bm90LWRlY3J5cHRhYmxl');
    R.invalidateCache();
    assert.equal(R.getById(r.id).password, 'newsecret');
  } finally {
    delete process.env.DATA_DIR;
  }
});

// ── Settings: ciphertext preservation ────────────────────────────────────────

function freshSettings(tmpDir) {
  process.env.DATA_DIR = tmpDir;
  delete require.cache[require.resolve('../src/settings')];
  return require('../src/settings');
}

test('settings: undecryptable credential ciphertext survives unrelated saves', () => {
  const tmp = makeTmpDir();
  try {
    let S = freshSettings(tmp);
    S.save({ telegramBotToken: 'tok-123' });
    const file = path.join(tmp, 'settings.json');

    const onDisk = JSON.parse(fs.readFileSync(file, 'utf8'));
    onDisk.telegramBotToken = 'bm90LWRlY3J5cHRhYmxl';
    fs.writeFileSync(file, JSON.stringify(onDisk));

    S = freshSettings(tmp);
    assert.equal(S.load().telegramBotToken, '', 'undecryptable token reads as empty');

    S.save({ pollSystem: 3000 }); // unrelated save
    const after = JSON.parse(fs.readFileSync(file, 'utf8'));
    assert.equal(after.telegramBotToken, 'bm90LWRlY3J5cHRhYmxl', 'ciphertext preserved');

    S.save({ telegramBotToken: 'new-tok' }); // explicit update wins
    S = freshSettings(tmp);
    assert.equal(S.load().telegramBotToken, 'new-tok');
  } finally {
    delete process.env.DATA_DIR;
  }
});
