const test = require('node:test');
const assert = require('node:assert/strict');
const EventEmitter = require('events');

const SystemCollector = require('../src/collectors/system');
const ArpCollector = require('../src/collectors/arp');
const TrafficCollector = require('../src/collectors/traffic');
const LogsCollector = require('../src/collectors/logs');
const DhcpLeasesCollector = require('../src/collectors/dhcpLeases');
const WirelessCollector = require('../src/collectors/wireless');
const DhcpNetworksCollector = require('../src/collectors/dhcpNetworks');
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
  const originalSetInterval = global.setInterval;
  const originalClearInterval = global.clearInterval;
  const timers = [];
  global.setInterval = (cb, ms) => {
    const timer = { cb, ms, cleared: false };
    timers.push(timer);
    return timer;
  };
  global.clearInterval = (timer) => {
    if (timer) timer.cleared = true;
  };

  try {
    await runTest(timers);
  } finally {
    global.setInterval = originalSetInterval;
    global.clearInterval = originalClearInterval;
  }
}

// --- Inflight guard and polling lifecycle ---

test('polling collector start skips overlapping interval ticks while one run is inflight', async () => {
  await withPatchedIntervals(async (timers) => {
    let tickCount = 0;
    let releaseTick;
    const pendingTick = new Promise((resolve) => { releaseTick = resolve; });
    const ros = mockROS(async () => [{}]);
    const collector = new ArpCollector({ ros, pollMs: 50000, state: {} });

    collector.tick = async function () {
      tickCount++;
      await pendingTick;
    };

    collector.start();
    assert.equal(tickCount, 1, 'start() should launch the first run immediately');

    await timers[0].cb();
    assert.equal(tickCount, 1, 'interval callback should no-op while the first run is inflight');

    releaseTick();
    await pendingTick;
    await Promise.resolve();
    await timers[0].cb();
    assert.equal(tickCount, 2, 'next interval should run after inflight state resets');
  });
});

test('polling collector start resets inflight state after a tick throws', async () => {
  await withPatchedIntervals(async (timers) => {
    const state = {};
    const ros = mockROS(async () => []);
    const io = { emit() {} };
    const collector = new SystemCollector({ ros, io, pollMs: 50000, state });
    let tickCount = 0;

    collector.tick = async function () {
      tickCount++;
      if (tickCount === 1) throw new Error('boom');
    };

    collector.start();
    await Promise.resolve();
    assert.equal(state.lastSystemErr, 'boom');

    await timers[0].cb();
    assert.equal(tickCount, 2, 'interval callback should run again after the failed start tick');
  });
});

test('polling collector stops timer on ROS close event', () => {
  return withPatchedIntervals(async () => {
    const ros = mockROS();
    const collector = new ArpCollector({ ros, pollMs: 30000, state: {} });
    collector.start();
    const timer = collector.timer;

    assert.ok(timer);
    ros.emit('close');
    assert.equal(timer.cleared, true);
    assert.equal(collector.timer, null);
  });
});

test('polling collector restarts timer on ROS connected event', () => {
  const ros = mockROS(async () => []);
  const collector = new ArpCollector({ ros, pollMs: 30000, state: {} });
  collector.start();

  ros.emit('close');
  assert.equal(collector.timer, null);

  ros.emit('connected');
  assert.ok(collector.timer, 'timer should be restored after reconnect');

  clearInterval(collector.timer);
  collector.timer = null;
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
  const collector = new LogsCollector({ ros, io: { emit() {} }, state });
  collector.start();

  assert.ok(collector.stream, 'stream should be active');

  capturedCb(new Error('connection lost'), null);
  assert.equal(streamCalls, 1, 'restart is delayed, not immediate');
  assert.equal(collector.stream, null, 'old stream should be stopped');
  assert.match(state.lastLogsErr, /connection lost/);

  // Fast-forward the 2s restart delay
  await new Promise(r => setTimeout(r, 2100));
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
  const collector = new LogsCollector({ ros, io: { emit() {} }, state: {} });
  collector.start();

  callbacks[0](new Error('connection lost'), null);

  assert.equal(stopCalls, 1, 'stale stream should be stopped before restart');
  assert.equal(streamCalls, 1, 'restart is delayed, not immediate');

  // Fast-forward the 2s restart delay
  await new Promise(r => setTimeout(r, 2100));
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
  const collector = new DhcpLeasesCollector({ ros, io, pollMs: 15000, state: {} });
  await collector.start();

  callbacks[0](new Error('listen lost'), null);
  assert.equal(stopCalls, 1, 'failed stream should be stopped before restart');
  assert.equal(streamCalls, 1, 'restart is delayed, not immediate');

  // Fast-forward the 2s restart delay
  await new Promise(r => setTimeout(r, 2100));
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

test('polling collector throws on error and succeeds on next tick', async () => {
  let callNum = 0;
  const ros = mockROS(async () => {
    callNum++;
    if (callNum === 1) throw new Error('temporary failure');
    return [];
  });
  const state = {};
  const collector = new ArpCollector({ ros, pollMs: 50000, state });

  // First tick — error propagates
  await assert.rejects(() => collector.tick(), { message: 'temporary failure' });

  // Second tick — success
  await collector.tick();
  assert.equal(state.lastArpTs > 0, true);
});

test('system collector still emits data when package/update query fails', async () => {
  const emitted = [];
  const ros = mockROS(async (cmd) => {
    if (cmd.includes('resource')) return [{ 'cpu-load': '25', 'total-memory': '1000000', 'free-memory': '750000', version: '7.16' }];
    if (cmd.includes('health')) return [];
    if (cmd.includes('update')) throw new Error('no such command');
    return [];
  });
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new SystemCollector({ ros, io, pollMs: 5000, state: {} });
  await collector.tick();

  assert.equal(emitted.length, 1);
  assert.equal(emitted[0].data.cpuLoad, 25);
  assert.equal(emitted[0].data.updateAvailable, false);
  assert.equal(emitted[0].data.latestVersion, '');
});

test('system collector skips tick when ros is not connected', async () => {
  const emitted = [];
  const ros = mockROS(async () => { assert.fail('should not be called'); });
  ros.connected = false;
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new SystemCollector({ ros, io, pollMs: 5000, state: {} });
  await collector.tick();

  assert.equal(emitted.length, 0);
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

test('wireless collector detects wifi API mode and locks in', async () => {
  const ros = mockROS(async (cmd) => {
    if (cmd.includes('/interface/wifi/')) return [{ 'mac-address': 'AA:BB', signal: '-50', interface: 'wifi1' }];
    return [];
  });
  ros.cfg = {};
  const io = { emit() {} };
  const collector = new WirelessCollector({
    ros, io, pollMs: 5000, state: {},
    dhcpLeases: { getNameByMAC: () => null },
    arp: { getByMAC: () => null },
  });

  assert.equal(collector.mode, null);
  await collector.tick();
  assert.equal(collector.mode, 'wifi');
});

test('wireless collector falls back to legacy API when wifi API fails', async () => {
  const ros = mockROS(async (cmd) => {
    if (cmd.includes('/interface/wifi/')) throw new Error('no such command');
    if (cmd.includes('/interface/wireless/')) return [{ 'mac-address': 'CC:DD', signal: '-60' }];
    return [];
  });
  ros.cfg = {};
  const io = { emit() {} };
  const collector = new WirelessCollector({
    ros, io, pollMs: 5000, state: {},
    dhcpLeases: { getNameByMAC: () => null },
    arp: { getByMAC: () => null },
  });

  await collector.tick();
  assert.equal(collector.mode, 'wireless');
});

test('wireless collector resets mode on reconnect', () => {
  const ros = mockROS(async () => []);
  ros.cfg = {};
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

  clearInterval(collector.timer);
  collector.timer = null;
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
  const collector = new DhcpNetworksCollector({ ros, io, pollMs: 15000, dhcpLeases: { getActiveLeaseIPs: () => [] }, state: {} });
  await collector.tick();

  assert.deepEqual(collector.getLanCidrs(), ['192.168.1.0/24']);
});
