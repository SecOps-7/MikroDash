const test = require('node:test');
const assert = require('node:assert/strict');
const EventEmitter = require('events');

const SystemCollector = require('../src/collectors/system');
const TrafficCollector = require('../src/collectors/traffic');
const LogsCollector = require('../src/collectors/logs');
const DhcpLeasesCollector = require('../src/collectors/dhcpLeases');
const WirelessCollector = require('../src/collectors/wireless');
const DhcpNetworksCollector = require('../src/collectors/dhcpNetworks');
const ConnectionsCollector = require('../src/collectors/connections');
const BandwidthCollector = require('../src/collectors/bandwidth');
const ROS = require('../src/routeros/client');

// Helper: create a mock ROS that is an EventEmitter (for on/emit lifecycle)
function mockROS(writeFn) {
  const ros = new EventEmitter();
  ros.setMaxListeners(30);
  ros.connected = true;
  ros.write = writeFn || (async () => []);
  return ros;
}

function mockConn({ onConnect, onClose } = {}) {
  const conn = new EventEmitter();
  conn.connect = async () => {
    if (onConnect) await onConnect(conn);
  };
  conn.close = () => {
    if (onClose) onClose(conn);
    conn.emit('close');
  };
  return conn;
}

async function withPatchedIntervals(runTest) {
  const originalSetInterval  = global.setInterval;
  const originalClearInterval = global.clearInterval;
  const timers = [];
  global.setInterval = (cb, ms) => {
    const timer = { cb, ms, cleared: false, isInterval: true };
    timers.push(timer);
    return timer;
  };
  global.clearInterval = (timer) => {
    if (timer) timer.cleared = true;
  };
  try {
    await runTest(timers);
  } finally {
    global.setInterval  = originalSetInterval;
    global.clearInterval = originalClearInterval;
  }
}

// Like withPatchedIntervals but also patches setTimeout/clearTimeout.
// Use for collectors that use the seamless-interval pattern (recursive setTimeout).
async function withPatchedTimers(runTest) {
  const originalSetInterval  = global.setInterval;
  const originalClearInterval = global.clearInterval;
  const originalSetTimeout   = global.setTimeout;
  const originalClearTimeout  = global.clearTimeout;
  const timers = [];
  global.setInterval = (cb, ms) => {
    const timer = { cb, ms, cleared: false, isInterval: true };
    timers.push(timer);
    return timer;
  };
  global.clearInterval = (timer) => {
    if (timer) timer.cleared = true;
  };
  global.setTimeout = (cb, ms) => {
    const timer = { cb, ms, cleared: false, isInterval: false };
    timers.push(timer);
    return timer;
  };
  global.clearTimeout = (timer) => {
    if (timer && typeof timer === 'object') timer.cleared = true;
  };
  try {
    await runTest(timers);
  } finally {
    global.setInterval  = originalSetInterval;
    global.clearInterval = originalClearInterval;
    global.setTimeout   = originalSetTimeout;
    global.clearTimeout  = originalClearTimeout;
  }
}

// --- DhcpNetworksCollector streaming lifecycle ---
const dhcpLeaseStub = { getActiveLeaseIPs: () => [], getAllLeaseIPs: () => [] };

function mockRosWithStream(writeFn) {
  const ros = new EventEmitter();
  ros.connected = true;
  ros.write = writeFn || (async () => []);
  ros.stream = (words) => {
    const s = { _key: words[0], stopped: false, stop() { this.stopped = true; } };
    s.on = () => s;
    return s;
  };
  return ros;
}

test('dhcpNetworks start() opens 4 streams and populates lanCidrs via initial fetch', async () => {
  const ros = mockRosWithStream(async (cmd) => {
    if (cmd.includes('network')) return [{ address: '192.168.1.0/24', gateway: '192.168.1.1', 'dns-server': '' }];
    return [];
  });
  const io = { emit() {} };
  const state = {};
  const collector = new DhcpNetworksCollector({ ros, io, pollMs: 60000, dhcpLeases: dhcpLeaseStub, state, wanIface: 'ether1' });

  await collector.start();

  assert.ok(collector.lanCidrs.length > 0, 'lanCidrs populated after start()');
  assert.ok(Object.values(collector._streams).some(s => s !== null), 'at least one stream opened');

  collector.stop();
});

test('dhcpNetworks stop() closes all streams', async () => {
  const stoppedKeys = [];
  const ros = new EventEmitter();
  ros.connected = true;
  ros.write = async () => [];
  ros.stream = (words) => {
    const key = words[0];
    const s = { key, stop() { stoppedKeys.push(this.key); } };
    s.on = () => s;
    return s;
  };
  const io = { emit() {} };
  const collector = new DhcpNetworksCollector({ ros, io, pollMs: 60000, dhcpLeases: dhcpLeaseStub, state: {}, wanIface: 'ether1' });

  await collector.start();
  collector.stop();

  assert.equal(stoppedKeys.length, 4, 'all 4 streams stopped');
});

test('dhcpNetworks stops streams on ROS close event', async () => {
  const stoppedKeys = [];
  const ros = new EventEmitter();
  ros.connected = true;
  ros.write = async () => [];
  ros.stream = (words) => {
    const key = words[0];
    const s = { key, stop() { stoppedKeys.push(this.key); } };
    s.on = () => s;
    return s;
  };
  const io = { emit() {} };
  const collector = new DhcpNetworksCollector({ ros, io, pollMs: 60000, dhcpLeases: dhcpLeaseStub, state: {}, wanIface: 'ether1' });

  await collector.start();
  const beforeClose = stoppedKeys.length;

  ros.emit('close');
  assert.ok(stoppedKeys.length > beforeClose, 'streams stopped on ROS close');
});

test('dhcpNetworks restarts streams on ROS connected event', async () => {
  let streamCount = 0;
  const ros = new EventEmitter();
  ros.connected = true;
  ros.write = async () => [];
  ros.stream = () => {
    streamCount++;
    const s = { stop() {} };
    s.on = () => s;
    return s;
  };
  const io = { emit() {} };
  const collector = new DhcpNetworksCollector({ ros, io, pollMs: 60000, dhcpLeases: dhcpLeaseStub, state: {}, wanIface: 'ether1' });

  await collector.start();
  const afterStart = streamCount;

  ros.emit('close');
  ros.emit('connected');
  assert.ok(streamCount > afterStart, 'streams re-opened after reconnect');

  collector.stop();
});

// --- Streaming collector lifecycle ---

test('logs collector starts stream on start and restarts on reconnect', () => {
  let streamCalls = 0;
  let stopCalls = 0;
  const ros = mockROS();
  ros.stream = (words, cb) => {
    streamCalls++;
    return { stop() { stopCalls++; } };
  };
  const collector = new LogsCollector({ ros, io: { emit() {} }, state: {} });
  collector.start();

  assert.equal(streamCalls, 1, 'stream started on start()');

  ros.emit('close');
  assert.equal(stopCalls, 1, 'stream stopped on close');
  assert.equal(collector.stream, null);

  ros.emit('connected');
  assert.equal(streamCalls, 2, 'stream restarted on reconnect');
});

test('logs collector records stream errors and schedules a restart', async () => {
  const ros = mockROS();
  let capturedCb;
  let streamCalls = 0;
  ros.stream = (words, cb) => {
    streamCalls++;
    capturedCb = cb;
    return { stop() {} };
  };
  const state = {};
  const collector = new LogsCollector({ ros, io: { emit() {} }, state, _restartDelayMs: 50 });
  collector.start();

  assert.ok(collector.stream, 'stream should be active');

  capturedCb(new Error('connection lost'), null);
  assert.equal(streamCalls, 1, 'restart is delayed, not immediate');
  assert.equal(collector.stream, null, 'old stream should be stopped');
  assert.match(state.lastLogsErr, /connection lost/);

  // Fast-forward the restart delay
  await new Promise(r => setTimeout(r, 100));
  assert.equal(streamCalls, 2, 'collector should create a replacement stream after delay');
  assert.ok(collector.stream, 'replacement stream should stay active');
});

test('logs collector restarts stream after callback error while ROS remains connected', async () => {
  const ros = mockROS();
  let streamCalls = 0;
  let stopCalls = 0;
  const callbacks = [];
  ros.stream = (words, cb) => {
    streamCalls++;
    callbacks.push(cb);
    return {
      stop() {
        stopCalls++;
      },
    };
  };
  const collector = new LogsCollector({ ros, io: { emit() {} }, state: {}, _restartDelayMs: 50 });
  collector.start();

  callbacks[0](new Error('connection lost'), null);

  assert.equal(stopCalls, 1, 'stale stream should be stopped before restart');
  assert.equal(streamCalls, 1, 'restart is delayed, not immediate');

  // Fast-forward the restart delay
  await new Promise(r => setTimeout(r, 100));
  assert.equal(streamCalls, 2, 'stream should restart after delay');
  assert.ok(collector.stream, 'replacement stream should be active');
});

test('dhcp leases collector loads initial data and starts stream', async () => {
  let writeCalls = 0;
  let streamCalls = 0;
  const ros = mockROS(async () => {
    writeCalls++;
    return [{ address: '192.168.1.10', 'mac-address': 'AA:BB', comment: 'test' }];
  });
  ros.stream = (words, cb) => {
    streamCalls++;
    return { stop() {} };
  };
  const collector = new DhcpLeasesCollector({ ros, io: { emit() {} }, pollMs: 15000, state: {} });
  await collector.start();

  assert.equal(writeCalls, 1, 'initial /print called');
  assert.equal(streamCalls, 1, 'listen stream started');
  assert.equal(collector.getNameByIP('192.168.1.10').name, 'test');
});

test('dhcp leases collector restarts stream after callback error and preserves seen devices', async () => {
  const emitted = [];
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  let streamCalls = 0;
  let stopCalls = 0;
  const callbacks = [];
  const ros = mockROS(async () => [
    { address: '192.168.1.10', 'mac-address': 'AA:BB', comment: 'laptop' },
  ]);
  ros.stream = (words, cb) => {
    streamCalls++;
    callbacks.push(cb);
    return {
      stop() {
        stopCalls++;
      },
    };
  };
  const collector = new DhcpLeasesCollector({ ros, io, pollMs: 15000, state: {}, _restartDelayMs: 50 });
  await collector.start();

  callbacks[0](new Error('listen lost'), null);
  assert.equal(stopCalls, 1, 'failed stream should be stopped before restart');
  assert.equal(streamCalls, 1, 'restart is delayed, not immediate');

  // Fast-forward the restart delay
  await new Promise(r => setTimeout(r, 100));
  assert.equal(streamCalls, 2, 'stream should restart after delay');

  callbacks[1](null, { address: '192.168.1.10', 'mac-address': 'AA:BB', comment: 'laptop' });
  assert.equal(collector.getNameByIP('192.168.1.10').name, 'laptop');
  assert.equal(emitted.filter(e => e.ev === 'device:new').length, 1, 'device:new should remain deduplicated');
});

test('dhcp leases collector emits device:new only once per MAC across initial load and stream updates', async () => {
  const emitted = [];
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  let streamHandler;
  const ros = mockROS(async () => [
    { address: '192.168.1.10', 'mac-address': 'AA:BB', comment: 'laptop' },
  ]);
  ros.stream = (words, cb) => {
    streamHandler = cb;
    return { stop() {} };
  };
  const collector = new DhcpLeasesCollector({ ros, io, pollMs: 15000, state: {} });
  await collector.start();
  streamHandler(null, { address: '192.168.1.10', 'mac-address': 'AA:BB', comment: 'laptop' });

  const deviceNew = emitted.filter(e => e.ev === 'device:new');
  assert.equal(deviceNew.length, 1, 'device:new should only fire once per MAC');
});

// --- RouterOS client resilience ---

test('ROS client connectLoop retries failures and resets backoff after a successful reconnect', { timeout: 1000 }, async () => {
  const ros = new ROS({});
  const events = [];
  ros.on('error', () => events.push('error'));
  ros.on('connected', () => events.push('connected'));
  ros.on('close', () => events.push('close'));

  let attempt = 0;
  ros._buildConn = () => {
    attempt++;
    if (attempt === 1) {
      return mockConn({
        onConnect: async () => { throw new Error('boom'); },
      });
    }
    return mockConn({
      onConnect: async (conn) => {
        process.nextTick(() => conn.emit('close'));
      },
    });
  };

  const sleeps = [];
  ros._sleep = async (ms) => {
    sleeps.push(ms);
    if (sleeps.length === 2) ros.stop();
  };

  await ros.connectLoop();

  assert.deepEqual(sleeps, [2000, 2000]);
  assert.deepEqual(events.slice(0, 3), ['error', 'connected', 'close']);
  assert.equal(ros.connected, false);
});

test('ROS client connectLoop does not schedule another retry after stop is requested', { timeout: 1000 }, async () => {
  const ros = new ROS({});
  ros._buildConn = () => mockConn({
    onConnect: async (conn) => {
      process.nextTick(() => conn.emit('close'));
    },
  });

  let sleepCalls = 0;
  ros._sleep = async () => {
    sleepCalls++;
  };
  ros.on('close', () => ros.stop());

  await ros.connectLoop();

  assert.equal(ros._stopping, true);
  assert.equal(sleepCalls, 0);
});

test('ROS client write rejects when not connected', async () => {
  const ros = new ROS({});
  ros.connected = false;
  await assert.rejects(ros.write('/test'), /Not connected/);
});

test('ROS client stream throws when not connected', () => {
  const ros = new ROS({});
  ros.connected = false;
  assert.throws(() => ros.stream(['/test'], () => {}), /Not connected/);
});

test('ROS client write normalizes null result to empty array', async () => {
  const ros = new ROS({});
  ros.connected = true;
  ros.conn = {
    write: async () => null,
    close() {},
  };

  const result = await ros.write('/test', [], 1000);
  assert.deepEqual(result, []);
});

// --- Error handling and system collector resilience ---


test('system collector still emits data when package/update query fails', async () => {
  const emitted = [];
  const fakeStream = new EventEmitter();
  fakeStream.stop = () => Promise.resolve();
  const ros = mockROS(async (cmd) => {
    if (cmd.includes('update') || cmd.includes('package')) throw new Error('no such command');
    return [];
  });
  ros.stream = () => fakeStream;
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); } };
  await withPatchedIntervals(async () => {
    const collector = new SystemCollector({ ros, io, pollMs: 5000, state: {} });
    collector.start();
    fakeStream.emit('data', { 'cpu-load': '25', 'total-memory': '1000000', 'free-memory': '750000', version: '7.16' });
    assert.equal(emitted.length, 1);
    assert.equal(emitted[0].data.cpuLoad, 25);
    assert.equal(emitted[0].data.updateAvailable, false);
    assert.equal(emitted[0].data.latestVersion, '');
    collector.stop();
  });
});

test('system collector skips tick when ros is not connected', async () => {
  const emitted = [];
  const ros = mockROS(async () => []);
  ros.connected = false;
  ros.stream = () => ({ stop() {}, on() {} });
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); } };
  await withPatchedIntervals(async () => {
    const collector = new SystemCollector({ ros, io, pollMs: 5000, state: {} });
    collector.start();
    assert.equal(emitted.length, 0);
    collector.stop();
  });
});

test('traffic collector rejects interface selection before whitelist is loaded', () => {
  const ros = { connected: true, on() {} };
  const io = { to() { return { emit() {} }; }, emit() {} };
  const collector = new TrafficCollector({ ros, io, defaultIf: 'wan', historyMinutes: 1, state: {} });

  const result = collector._normalizeIfName('ether1');
  assert.equal(result, null);
});

test('traffic collector rejects control characters and oversized names', () => {
  const ros = { connected: true, on() {} };
  const io = { to() { return { emit() {} }; }, emit() {} };
  const collector = new TrafficCollector({ ros, io, defaultIf: 'wan', historyMinutes: 1, state: {} });
  collector.setAvailableInterfaces(['ether1', 'wan']);

  assert.equal(collector._normalizeIfName('ether1'), 'ether1');
  assert.equal(collector._normalizeIfName(''), null);
  assert.equal(collector._normalizeIfName('   '), null);
  assert.equal(collector._normalizeIfName('a'.repeat(129)), null);
  assert.equal(collector._normalizeIfName('eth\ner1'), null);
  assert.equal(collector._normalizeIfName('eth\0er1'), null);
  assert.equal(collector._normalizeIfName('bogus'), null);
  assert.equal(collector._normalizeIfName(123), null);
  assert.equal(collector._normalizeIfName(null), null);
});

// --- Wireless API detection ---

test('wireless collector detects wifi API mode and locks in', () => {
  const ros = mockROS();
  ros.stream = () => { const s = { stop() {} }; s.on = () => s; return s; };
  const io = { emit() {} };
  const collector = new WirelessCollector({
    ros, io, pollMs: 5000, state: {},
    dhcpLeases: { getNameByMAC: () => null },
    arp: { getByMAC: () => null },
  });

  assert.equal(collector.mode, null);
  collector._onBatch('wifi', [{ 'mac-address': 'AA:BB', signal: '-50', interface: 'wifi1' }]);
  assert.equal(collector.mode, 'wifi');
});

test('wireless collector falls back to legacy API when wifi returns empty batch', () => {
  let wirelessStarted = false;
  const ros = mockROS();
  ros.stream = (words) => {
    if (words[0].includes('/interface/wireless/')) wirelessStarted = true;
    const s = { stop() {} }; s.on = () => s; return s;
  };
  const io = { emit() {} };
  const collector = new WirelessCollector({
    ros, io, pollMs: 5000, state: {},
    dhcpLeases: { getNameByMAC: () => null },
    arp: { getByMAC: () => null },
  });

  collector._onBatch('wifi', []);
  assert.equal(collector.mode, 'wireless');
  assert.ok(wirelessStarted, 'wireless stream started after empty wifi batch');
});

test('wireless collector resets mode on reconnect and does not auto-start streams', () => {
  const ros = mockROS();
  ros.stream = () => { const s = { stop() {} }; s.on = () => s; return s; };
  const io = { emit() {} };
  const collector = new WirelessCollector({
    ros, io, pollMs: 5000, state: {},
    dhcpLeases: { getNameByMAC: () => null },
    arp: { getByMAC: () => null },
  });

  collector.mode = 'wifi';
  collector.start();
  ros.emit('connected');
  assert.equal(collector.mode, null, 'mode should reset on reconnect');
  // Streams must NOT auto-start — resume() is called externally by _updateWirelessStreams()
  assert.ok(Object.values(collector._streams).every(s => s === null), 'streams stay stopped after reconnect');
  // After resume() is called externally, streams restart
  collector.resume();
  assert.ok(collector._streams.wifi !== null, 'wifi stream starts after resume()');

  collector.stop();
});

test('wireless _probeCAPsMAN sets _capsmanAvailable=true when API responds', async () => {
  const ros = { connected: true, on() {}, cfg: {}, write: async () => [] };
  const io  = { emit() {} };
  const collector = new WirelessCollector({ ros, io, pollMs: 5000, state: {}, dhcpLeases: null, arp: null });

  assert.equal(collector._capsmanAvailable, false, 'false before probe');
  await collector._probeCAPsMAN();
  assert.equal(collector._capsmanAvailable, true, 'true after successful probe');
});

test('wireless _probeCAPsMAN sets _capsmanAvailable=false on unknown-command error', async () => {
  const ros = {
    connected: true, on() {}, cfg: {},
    write: async (cmd) => {
      if (cmd.includes('/caps-man/')) throw new Error('unknown command');
      return [];
    },
  };
  const io  = { emit() {} };
  const collector = new WirelessCollector({ ros, io, pollMs: 5000, state: {}, dhcpLeases: null, arp: null });

  await collector._probeCAPsMAN();
  assert.equal(collector._capsmanAvailable, false, 'false when router rejects the path');
});

test('dhcp networks collector deduplicates LAN CIDRs', async () => {
  const ros = mockROS(async (cmd) => {
    if (cmd.includes('network')) return [
      { address: '192.168.1.0/24', gateway: '192.168.1.1' },
      { address: '192.168.1.0/24', gateway: '192.168.1.1' },
    ];
    if (cmd.includes('address')) return [];
    return [];
  });
  const io = { emit() {} };
  const collector = new DhcpNetworksCollector({ ros, io, pollMs: 15000, dhcpLeases: { getActiveLeaseIPs: () => [], getAllLeaseIPs: () => [] }, state: {} });
  await collector._fetchOnce();

  assert.deepEqual(collector.getLanCidrs(), ['192.168.1.0/24']);
});

// ═══════════════════════════════════════════════════════════════════════════
// --- Routing Collector lifecycle ---
// ═══════════════════════════════════════════════════════════════════════════
const RoutingCollector = require('../src/collectors/routing');

// Helper: minimal io mock that supports the to(room).emit() pattern used by routing.
function routingIo(collector) {
  return { emit() {}, to(room) { return { emit() {} }; } };
}

test('routing collector resume() opens both route and BGP session streams', async () => {
  return withPatchedIntervals(async () => {
    let routeStreamOpened = false;
    let bgpStreamOpened   = false;
    let printCalled       = false;

    const ros = mockROS(async (cmd) => {
      if (cmd.includes('/ip/route')) printCalled = true;
      return [];
    });
    ros.stream = (words, cb) => {
      if (words[0].includes('/ip/route'))    routeStreamOpened = true;
      if (words[0].includes('bgp/session'))  bgpStreamOpened   = true;
      return { stop() {} };
    };

    const collector = new RoutingCollector({ ros, io: routingIo(), pollMs: 10000, state: {} });
    collector.start(); // no-op — listeners registered in constructor
    await collector.resume();

    assert.ok(printCalled,       '/ip/route/print called on resume');
    assert.ok(routeStreamOpened, '/ip/route/listen stream opened');
    assert.ok(bgpStreamOpened,   '/routing/bgp/session/listen stream opened');
    assert.equal(collector.timer, null, 'no poll timer — fully streamed');
  });
});

test('routing collector stop() clears all streams and timers', async () => {
  return withPatchedIntervals(async () => {
    const ros = mockROS(async () => []);
    ros.stream = (w, cb) => ({ stop() {} });
    const collector = new RoutingCollector({ ros, io: routingIo(), pollMs: 10000, state: {} });
    await collector.resume();

    assert.equal(collector.timer, null, 'timer is null — no poll loop');
    assert.doesNotThrow(() => collector.stop(), 'stop() must not throw');
    assert.equal(collector.timer,        null, 'timer still null after stop');
    assert.equal(collector._routeStream, null, 'route stream cleared by stop()');
    assert.equal(collector._bgpStream,   null, 'BGP stream cleared by stop()');
  });
});

test('routing collector stops all streams on ROS close event', async () => {
  return withPatchedIntervals(async () => {
    let routeStopCalled = false;
    let bgpStopCalled   = false;
    const ros = mockROS(async () => []);
    ros.stream = (words, cb) => ({
      stop() {
        if (words[0].includes('/ip/route')) routeStopCalled = true;
        if (words[0].includes('bgp'))       bgpStopCalled   = true;
      },
    });

    const collector = new RoutingCollector({ ros, io: routingIo(), pollMs: 10000, state: {} });
    await collector.resume();

    ros.emit('close');

    assert.ok(routeStopCalled, 'route stream stopped on close');
    assert.ok(bgpStopCalled,   'BGP stream stopped on close');
    assert.equal(collector._routeStream, null);
    assert.equal(collector._bgpStream,   null);
  });
});

test('routing collector resumes streams after ROS reconnect via resume()', async () => {
  // Routing is page-aware: the connected event calls suspend() to clear state;
  // index.js calls _updateRoutingStreams() → resume() when the Routing page is open.
  return withPatchedIntervals(async () => {
    let routeStreamCount = 0;
    let bgpStreamCount   = 0;
    let printCount       = 0;

    const ros = mockROS(async (cmd) => {
      if (cmd.includes('/ip/route')) printCount++;
      return [];
    });
    ros.stream = (words, cb) => {
      if (words[0].includes('/ip/route')) routeStreamCount++;
      if (words[0].includes('bgp'))       bgpStreamCount++;
      return { stop() {} };
    };

    const collector = new RoutingCollector({ ros, io: routingIo(), pollMs: 10000, state: {} });
    await collector.resume();

    assert.equal(routeStreamCount, 1, 'route stream opened once on resume');
    assert.equal(bgpStreamCount,   1, 'BGP stream opened once on resume');
    assert.equal(printCount,       1, '/ip/route/print called once on resume');

    ros.emit('close');
    await new Promise(r => setTimeout(r, 10));
    ros.emit('connected'); // triggers suspend() — state cleared, streams stopped
    await new Promise(r => setTimeout(r, 10));
    // Simulate index.js calling _updateRoutingStreams() → resume() for open page
    await collector.resume();

    assert.equal(routeStreamCount, 2, 'route stream reopened after resume');
    assert.equal(bgpStreamCount,   2, 'BGP stream reopened after resume');
    assert.equal(printCount,       2, '/ip/route/print reloaded after resume');
  });
});

test('routing collector does not accumulate listeners across multiple reconnects', async () => {
  // Listeners registered once in constructor — count must stay at 1.
  return withPatchedIntervals(async () => {
    const ros = mockROS(async () => []);
    ros.stream = (w, cb) => ({ stop() {} });
    const collector = new RoutingCollector({ ros, io: routingIo(), pollMs: 10000, state: {} });
    collector.start(); // no-op

    const counts = [];
    for (let i = 0; i < 4; i++) {
      ros.emit('close');
      await new Promise(r => setTimeout(r, 10));
      ros.emit('connected');
      await new Promise(r => setTimeout(r, 20));
      counts.push(ros.listenerCount('connected'));
    }

    assert.deepEqual(counts, [1, 1, 1, 1], 'connected listener count must stay at 1');
  });
});

test('routing collector route stream error triggers reload and restart after 3s delay', async () => {
  let streamCount = 0;
  let printCount  = 0;
  const callbacks = [];
  const ros = mockROS(async (cmd) => { if (cmd.includes('/ip/route')) printCount++; return []; });
  ros.stream = (words, cb) => {
    if (words[0].includes('/ip/route')) { streamCount++; callbacks.push(cb); }
    return { stop() {} };
  };

  const collector = new RoutingCollector({ ros, io: routingIo(), pollMs: 10000, state: {}, _restartDelayMs: 50 });
  await collector.resume();

  assert.equal(streamCount, 1);
  assert.equal(printCount,  1);

  callbacks[0](new Error('stream dropped'), null);
  assert.equal(collector._routeStream, null, 'stream nulled on error');
  assert.equal(streamCount, 1, 'restart is delayed');

  await new Promise(r => setTimeout(r, 100));
  assert.equal(printCount,  2, 'route table reloaded after stream error');
  assert.equal(streamCount, 2, 'new route stream started after reload');

  collector.stop();
});

test('routing collector BGP session stream error triggers reload and restart after 3s delay', async () => {
  let bgpStreamCount = 0;
  const bgpCallbacks = [];
  const ros = mockROS(async () => []);
  ros.stream = (words, cb) => {
    if (words[0].includes('bgp')) { bgpStreamCount++; bgpCallbacks.push(cb); }
    return { stop() {} };
  };

  const collector = new RoutingCollector({ ros, io: routingIo(), pollMs: 10000, state: {}, _restartDelayMs: 50 });
  await collector.resume();
  assert.equal(bgpStreamCount, 1);

  bgpCallbacks[0](new Error('bgp stream dropped'), null);
  assert.equal(collector._bgpStream, null);
  assert.equal(bgpStreamCount, 1, 'restart is delayed');

  await new Promise(r => setTimeout(r, 100));
  assert.equal(bgpStreamCount, 2, 'BGP stream restarted after error');

  collector.stop();
});

test('routing collector BGP stream unavailable on v6/no-BGP — route stream still runs', async () => {
  return withPatchedIntervals(async () => {
    let routeStreamOpened = false;
    const ros = mockROS(async () => []);
    ros.stream = (words, cb) => {
      if (words[0].includes('/ip/route')) { routeStreamOpened = true; return { stop() {} }; }
      if (words[0].includes('bgp'))       throw new Error('no such command');
      return { stop() {} };
    };

    const collector = new RoutingCollector({ ros, io: routingIo(), pollMs: 10000, state: {} });
    await collector.resume(); // must not throw

    assert.ok(routeStreamOpened,              'route stream opens despite BGP unavailable');
    assert.equal(collector._bgpStream, null,  'bgpStream null when endpoint unavailable');
  });
});

test('routing collector heartbeat re-emits last payload every 60s', async () => {
  return withPatchedIntervals(async (timers) => {
    const emitted = [];
    const rooms = new Map([['page-routing', { size: 1 }]]);
    const io = {
      emit(ev, data) { emitted.push({ ev, data }); },
      to(room) { return { to() { return this; }, emit: (ev, data) => emitted.push({ ev, data }) }; },
      sockets: { adapter: { rooms } },
    };
    const ros = mockROS(async () => []);
    ros.stream = (w, cb) => ({ stop() {} });

    const collector = new RoutingCollector({ ros, io, pollMs: 10000, state: {} });
    await collector.resume();
    const countBefore = emitted.length;

    // Find the heartbeat timer (pollMs interval)
    const heartbeatTimer = timers.find(t => !t.cleared);
    assert.ok(heartbeatTimer, 'heartbeat timer exists');
    heartbeatTimer.cb();
    assert.equal(emitted.length, countBefore + 1, 'heartbeat emits routing:update');
    assert.equal(emitted[emitted.length - 1].ev, 'routing:update');
  });
});

// ═══════════════════════════════════════════════════════════════════════════
// --- ConnectionsCollector lifecycle ---
// ═══════════════════════════════════════════════════════════════════════════

// Minimal stubs for dependencies that ConnectionsCollector requires
function makeConnsDeps(rosOverrides = {}) {
  const ros = new EventEmitter();
  ros.setMaxListeners(30);
  ros.connected = true;
  ros.write = async () => [];
  Object.assign(ros, rosOverrides);

  const dhcpLeases = {
    getNameByIP: () => null,
    getNameByMAC: () => null,
  };
  const arp = { getByIP: () => null };
  const dhcpNetworks = { getLanCidrs: () => [] };
  // io.sockets.adapter.rooms is accessed by _processRows / _runFallbackTick
  // when checking page-connections room membership — must not be undefined.
  const io = {
    engine: { clientsCount: 1 },
    emit() {},
    to() { return { emit() {} }; },
    sockets: { adapter: { rooms: new Map() } },
  };
  const state = {};
  return { ros, dhcpLeases, arp, dhcpNetworks, io, state };
}

test('ConnectionsCollector has a stop() method that clears the timer', () => {
  return withPatchedIntervals(async () => {
    const { ros, dhcpLeases, arp, dhcpNetworks, io, state } = makeConnsDeps();
    const collector = new ConnectionsCollector({
      ros, io, pollMs: 30000, topN: 5, dhcpNetworks, dhcpLeases, arp, state,
    });
    try {
      collector.start();
      assert.ok(collector._watchdogTimer, 'watchdog timer set after start()');

      collector.stop();
      assert.equal(collector._watchdogTimer, null, 'stop() must null the watchdog timer');
    } finally {
      collector.stop();
    }
  });
});

test('ConnectionsCollector stop() is idempotent — safe to call when already stopped', () => {
  return withPatchedIntervals(async () => {
    const { ros, dhcpLeases, arp, dhcpNetworks, io, state } = makeConnsDeps();
    const collector = new ConnectionsCollector({
      ros, io, pollMs: 30000, topN: 5, dhcpNetworks, dhcpLeases, arp, state,
    });
    try {
      assert.doesNotThrow(() => collector.stop(), 'stop() before start() must not throw');
      collector.start();
      collector.stop();
      assert.doesNotThrow(() => collector.stop(), 'double stop() must not throw');
    } finally {
      collector.stop();
    }
  });
});

test('ConnectionsCollector stops on ROS close event via stop()', () => {
  return withPatchedIntervals(async () => {
    const { ros, dhcpLeases, arp, dhcpNetworks, io, state } = makeConnsDeps();
    const collector = new ConnectionsCollector({
      ros, io, pollMs: 30000, topN: 5, dhcpNetworks, dhcpLeases, arp, state,
    });
    try {
      collector.start();
      assert.ok(collector._watchdogTimer, 'watchdog timer set after start()');

      ros.emit('close');
      assert.equal(collector._watchdogTimer, null, 'close event must clear watchdog timer');
    } finally {
      collector.stop();
    }
  });
});

test('ConnectionsCollector restarts on ROS connected event', () => {
  return withPatchedIntervals(async () => {
    const { ros, dhcpLeases, arp, dhcpNetworks, io, state } = makeConnsDeps();
    const collector = new ConnectionsCollector({
      ros, io, pollMs: 30000, topN: 5, dhcpNetworks, dhcpLeases, arp, state,
    });
    try {
      collector.start();
      ros.emit('close');
      assert.equal(collector._watchdogTimer, null, 'watchdog timer cleared on close');

      ros.emit('connected');
      assert.ok(collector._watchdogTimer, 'watchdog timer restored after connected event');
    } finally {
      collector.stop();
    }
  });
});

test('ConnectionsCollector does not accumulate listeners across reconnects', () => {
  return withPatchedIntervals(async () => {
    const { ros, dhcpLeases, arp, dhcpNetworks, io, state } = makeConnsDeps();
    new ConnectionsCollector({
      ros, io, pollMs: 30000, topN: 5, dhcpNetworks, dhcpLeases, arp, state,
    });

    // Listeners are registered once in the constructor — reconnect cycles
    // must not push additional listeners onto the ROS emitter.
    const counts = [];
    for (let i = 0; i < 4; i++) {
      ros.emit('close');
      ros.emit('connected');
      counts.push(ros.listenerCount('connected'));
    }

    assert.deepEqual(counts, [1, 1, 1, 1], 'connected listener count must stay at 1');
  });
});

test('ConnectionsCollector does not double-start when connected fires while already running', () => {
  return withPatchedIntervals(async (timers) => {
    const { ros, dhcpLeases, arp, dhcpNetworks, io, state } = makeConnsDeps();
    const collector = new ConnectionsCollector({
      ros, io, pollMs: 30000, topN: 5, dhcpNetworks, dhcpLeases, arp, state,
    });
    try {
      collector.start();
      assert.ok(collector._watchdogTimer, 'watchdog timer set after start()');

      // Simulate connected firing while already running — stop() then start(),
      // so there is still exactly one active watchdog interval (setInterval).
      ros.emit('connected');

      assert.ok(collector._watchdogTimer, 'watchdog timer still active after connected');
      assert.equal(timers.filter(t => t.isInterval && !t.cleared).length, 1, 'exactly one active watchdog interval after reconnect');
    } finally {
      collector.stop();
    }
  });
});


// ═══════════════════════════════════════════════════════════════════════════
// --- BandwidthCollector lifecycle ---
// ═══════════════════════════════════════════════════════════════════════════

function makeBandwidthDeps(rosOverrides = {}) {
  const ros = new EventEmitter();
  ros.setMaxListeners(30);
  ros.connected = true;
  ros.write = async () => [];
  Object.assign(ros, rosOverrides);

  const dhcpLeases = { getNameByIP: () => null, getNameByMAC: () => null };
  const arp = { getByIP: () => null };
  const dhcpNetworks = { getLanCidrs: () => [] };
  const ifStatus = { lastPayload: null };
  const _toChain = { emit() {} };
  _toChain.to = () => _toChain;
  const io = { engine: { clientsCount: 1 }, emit() {}, to: () => _toChain };
  const state = {};
  return { ros, dhcpLeases, arp, dhcpNetworks, ifStatus, io, state };
}

test('BandwidthCollector has a stop() method that clears the timer', () => {
  return withPatchedTimers(async () => {
    const { ros, dhcpLeases, arp, dhcpNetworks, ifStatus, io, state } = makeBandwidthDeps();
    const collector = new BandwidthCollector({
      ros, io, pollMs: 3000, dhcpNetworks, dhcpLeases, arp, ifStatus, state,
    });

    collector.start();
    assert.ok(collector.timer, 'timer set after start()');

    collector.stop();
    assert.equal(collector.timer, null, 'stop() must null the timer');
  });
});

test('BandwidthCollector stop() is idempotent', () => {
  return withPatchedTimers(async () => {
    const { ros, dhcpLeases, arp, dhcpNetworks, ifStatus, io, state } = makeBandwidthDeps();
    const collector = new BandwidthCollector({
      ros, io, pollMs: 3000, dhcpNetworks, dhcpLeases, arp, ifStatus, state,
    });

    assert.doesNotThrow(() => collector.stop(), 'stop() before start() must not throw');
    collector.start();
    collector.stop();
    assert.doesNotThrow(() => collector.stop(), 'double stop() must not throw');
    assert.equal(collector.timer, null);
  });
});

test('BandwidthCollector stops on ROS close event', () => {
  return withPatchedTimers(async () => {
    const { ros, dhcpLeases, arp, dhcpNetworks, ifStatus, io, state } = makeBandwidthDeps();
    const collector = new BandwidthCollector({
      ros, io, pollMs: 3000, dhcpNetworks, dhcpLeases, arp, ifStatus, state,
    });

    collector.start();
    assert.ok(collector.timer, 'timer set after start()');

    ros.emit('close');
    assert.equal(collector.timer, null, 'close event must clear timer');
  });
});

test('BandwidthCollector restarts and clears caches on ROS connected event', () => {
  return withPatchedTimers(async () => {
    const { ros, dhcpLeases, arp, dhcpNetworks, ifStatus, io, state } = makeBandwidthDeps();
    const collector = new BandwidthCollector({
      ros, io, pollMs: 3000, dhcpNetworks, dhcpLeases, arp, ifStatus, state,
    });

    // Seed caches to verify they are cleared on reconnect
    collector._prev.set('fake-id', { origBytes: 100, replBytes: 50, ts: Date.now() });
    collector._geoCache.set('1.2.3.4', { country: 'US', city: '' });
    collector._orgCache.set('1.2.3.4', 'Google');
    collector._ifaceCache.set('192.168.1.10', 'bridge');

    collector.start();
    ros.emit('close');
    ros.emit('connected');

    assert.equal(collector._prev.size, 0,      '_prev cleared on reconnect');
    assert.equal(collector._geoCache.size, 0,  '_geoCache cleared on reconnect');
    assert.equal(collector._orgCache.size, 0,  '_orgCache cleared on reconnect');
    assert.equal(collector._ifaceCache.size, 0,'_ifaceCache cleared on reconnect');
    assert.ok(collector.timer, 'timer restored after connected event');

    collector.stop();
  });
});

test('BandwidthCollector does not accumulate listeners across reconnects', () => {
  return withPatchedTimers(async () => {
    const { ros, dhcpLeases, arp, dhcpNetworks, ifStatus, io, state } = makeBandwidthDeps();
    new BandwidthCollector({
      ros, io, pollMs: 3000, dhcpNetworks, dhcpLeases, arp, ifStatus, state,
    });

    const counts = [];
    for (let i = 0; i < 4; i++) {
      ros.emit('close');
      ros.emit('connected');
      counts.push(ros.listenerCount('connected'));
    }

    assert.deepEqual(counts, [1, 1, 1, 1], 'connected listener count must stay at 1');
  });
});

test('BandwidthCollector does not double-start when connected fires while already running', () => {
  return withPatchedTimers(async (timers) => {
    const { ros, dhcpLeases, arp, dhcpNetworks, ifStatus, io, state } = makeBandwidthDeps();
    const collector = new BandwidthCollector({
      ros, io, pollMs: 3000, dhcpNetworks, dhcpLeases, arp, ifStatus, state,
    });

    collector.start();
    assert.ok(collector.timer, 'timer set after start()');

    // Simulate connected firing while already running (e.g. a reconnect).
    // The _started flag is true, so the handler stops the old timer and
    // starts a fresh one — never two concurrent timers.
    ros.emit('connected');

    assert.ok(collector.timer, 'a timer is still active after connected');
    assert.equal(timers.filter(t => !t.cleared).length, 1, 'exactly one active timer after reconnect');

    collector.stop();
  });
});

test('BandwidthCollector updates state timestamps and clears error on success', async () => {
  const { ros, dhcpLeases, arp, dhcpNetworks, ifStatus, io, state } = makeBandwidthDeps();
  // tick() exits early when snapshot timestamp hasn't changed; provide a cache with a real ts
  const connTableCache = { latestWithTs: () => ({ rows: [], ts: Date.now() }) };
  const collector = new BandwidthCollector({
    ros, io, pollMs: 3000, dhcpNetworks, dhcpLeases, arp, ifStatus, state, connTableCache,
  });

  state.lastBandwidthErr = 'stale error';
  await collector.tick();

  assert.ok(state.lastBandwidthTs > 0, 'lastBandwidthTs updated on success');
  assert.equal(state.lastBandwidthErr, null, 'lastBandwidthErr cleared on success');
});

test('BandwidthCollector records error in state on tick failure', async () => {
  const { ros, dhcpLeases, arp, dhcpNetworks, ifStatus, io, state } = makeBandwidthDeps();
  // Make the cache throw so tick() propagates an error that start()'s run() wrapper records
  const connTableCache = { latestWithTs: () => { throw new Error('cache read error'); } };
  const collector = new BandwidthCollector({
    ros, io, pollMs: 3000, dhcpNetworks, dhcpLeases, arp, ifStatus, state, connTableCache,
  });

  return withPatchedTimers(async () => {
    collector.start();
    // tick() throws synchronously before any await — one microtask turn is enough
    await Promise.resolve();
    await Promise.resolve();
    assert.match(state.lastBandwidthErr, /cache read error/, 'error recorded in state on tick failure');
    collector.stop();
  });
});

// ═══════════════════════════════════════════════════════════════════════════
// --- Idle-gate and dirty-check fingerprint (new in optimisation pass) ---
// ═══════════════════════════════════════════════════════════════════════════

test('idle-gated collector skips tick when no browser clients connected', async () => {
  const { ros, dhcpLeases, arp, dhcpNetworks, state } = makeConnsDeps();
  const io = {
    engine: { clientsCount: 0 },
    emit() { assert.fail('must not emit when idle'); },
    to() { return { emit() {} }; },
    sockets: { adapter: { rooms: new Map() } },
  };
  const collector = new ConnectionsCollector({
    ros, io, pollMs: 30000, topN: 5, dhcpNetworks, dhcpLeases, arp, state,
  });
  // tick() must return without emitting or calling ros.write
  await collector.tick();
  // If we reach here without assert.fail firing, the idle-gate worked
});

test('idle-gated collector resumes when a client connects', async () => {
  const emitted = [];
  const { ros, dhcpLeases, arp, dhcpNetworks, state } = makeConnsDeps();
  const io = {
    engine: { clientsCount: 0 },
    emit(ev, d) { emitted.push({ ev, d }); },
    to() { return { emit() {} }; },
    sockets: { adapter: { rooms: new Map() } },
  };
  const collector = new ConnectionsCollector({
    ros, io, pollMs: 30000, topN: 5, dhcpNetworks, dhcpLeases, arp, state,
  });

  await collector.tick();
  assert.equal(emitted.length, 0, 'no emit while idle');

  io.engine.clientsCount = 1;
  await collector.tick();
  assert.equal(emitted.length, 1, 'emits once a client is connected');
});

test('dirty-check suppresses emit when connections data is unchanged', async () => {
  const emitted = [];
  const { ros, dhcpLeases, arp, dhcpNetworks, state } = makeConnsDeps({
    write: async () => [
      { '.id': '*1', 'src-address': '192.168.1.10', 'dst-address': '1.1.1.1', protocol: 'tcp', 'dst-port': '443' },
    ],
  });
  const io = {
    engine: { clientsCount: 1 },
    emit(ev, d) { emitted.push({ ev, d }); },
    to() { return { emit() {} }; },
    sockets: { adapter: { rooms: new Map() } },
  };
  const collector = new ConnectionsCollector({
    ros, io, pollMs: 30000, topN: 5, dhcpNetworks, dhcpLeases, arp, state,
  });

  await collector.tick();
  assert.equal(emitted.length, 1, 'first tick always emits');

  await collector.tick();
  assert.equal(emitted.length, 1, 'second tick suppressed — data unchanged');

  // lastPayload is still updated even when emit is suppressed
  assert.ok(collector.lastPayload, 'lastPayload set');
});

test('dirty-check emits when connections data changes', async () => {
  const emitted = [];
  let srcIp = '192.168.1.10';
  const { dhcpLeases, arp, state } = makeConnsDeps();
  // Must supply a real LAN CIDR so the source IP is counted in topSources
  const dhcpNetworks = { getLanCidrs: () => ['192.168.1.0/24'] };
  const ros = new (require('events').EventEmitter)();
  ros.setMaxListeners(30);
  ros.connected = true;
  ros.write = async () => [
    { '.id': '*1', 'src-address': srcIp, 'dst-address': '1.1.1.1', protocol: 'tcp', 'dst-port': '443' },
  ];
  const io = {
    engine: { clientsCount: 1 },
    emit(ev, d) { emitted.push({ ev, d }); },
    to() { return { emit() {} }; },
    sockets: { adapter: { rooms: new Map() } },
  };
  const collector = new ConnectionsCollector({
    ros, io, pollMs: 30000, topN: 5, dhcpNetworks, dhcpLeases, arp, state,
  });

  await collector.tick();
  assert.equal(emitted.length, 1, 'first tick emits');

  // New source IP changes topSources fingerprint → second emit fires.
  // Reset lastPayload so tick() doesn't short-circuit (it defers to the stream
  // when lastPayload is set; in tests we simulate a fresh poll cycle instead).
  srcIp = '192.168.1.99';
  collector.lastPayload = null;
  await collector.tick();
  assert.equal(emitted.length, 2, 'emits again when source IP changes');
});

// ── stop() coverage for newly-added stop() methods ───────────────────────────

test('PingCollector stop() clears stream', () => {
  return withPatchedIntervals(async () => {
    const PingCollector = require('../src/collectors/ping');
    const fakeStream = new EventEmitter();
    fakeStream.stop = () => Promise.resolve();
    const ros = mockROS(async () => []);
    ros.stream = () => fakeStream;
    const io = { emit() {}, on() {} };
    const collector = new PingCollector({ ros, io, pollMs: 10000, state: {}, target: '1.1.1.1' });
    collector.start();
    assert.ok(collector._stream, 'stream set after start');
    collector.stop();
    assert.equal(collector._stream, null, 'stream cleared by stop()');
  });
});

test('SystemCollector stop() clears health timer', () => {
  return withPatchedIntervals(async () => {
    const fakeStream = new EventEmitter();
    fakeStream.stop = () => Promise.resolve();
    const ros = mockROS(async () => []);
    ros.stream = () => fakeStream;
    const io = { engine: { clientsCount: 1 }, emit() {} };
    const collector = new SystemCollector({ ros, io, pollMs: 10000, state: {} });
    collector.start();
    assert.ok(collector._healthTimer, 'health timer set after start');
    collector.stop();
    assert.equal(collector._healthTimer, null, 'health timer cleared by stop()');
  });
});

test('WirelessCollector stop() clears retryTimer', () => {
  const ros = { connected: true, on() {}, stream: () => ({ stop() {}, on() {} }) };
  const io = { emit() {} };
  const collector = new WirelessCollector({
    ros, io, pollMs: 10000, state: {},
    dhcpLeases: { getNameByMAC: () => null },
    arp: { getByMAC: () => null },
  });
  collector._retryTimer = setTimeout(() => {}, 100000);
  collector.stop();
  assert.equal(collector._retryTimer, null, '_retryTimer cleared by stop()');
});
