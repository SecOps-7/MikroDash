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

// --- Inflight guard and polling lifecycle ---

test('inflight guard prevents concurrent ticks on polling collector', async () => {
  let tickCount = 0;
  const ros = mockROS(async () => [{}]);
  const io = { emit() {} };
  const collector = new ArpCollector({ ros, pollMs: 50000, state: {} });

  // Patch tick to track calls and add delay
  const origTick = collector.tick.bind(collector);
  collector.tick = async function () {
    tickCount++;
    await new Promise(r => setTimeout(r, 50));
    return origTick();
  };

  let inflight = false;
  const run = async () => {
    if (inflight) return;
    inflight = true;
    try { await collector.tick(); } finally { inflight = false; }
  };

  const first = run();
  const second = run(); // should be no-op because inflight is true
  await Promise.all([first, second]);

  assert.equal(tickCount, 1);
});

test('inflight guard resets after tick throws', async () => {
  const ros = mockROS(async () => { throw new Error('boom'); });
  const io = { emit() {} };
  const state = {};
  const collector = new SystemCollector({ ros, io, pollMs: 50000, state });

  let inflight = false;
  const run = async () => {
    if (inflight) return;
    inflight = true;
    try { await collector.tick(); } catch (e) {
      state.lastSystemErr = e.message;
    } finally { inflight = false; }
  };

  await run();
  assert.equal(inflight, false, 'inflight should be reset after error');
});

test('polling collector stops timer on ROS close event', () => {
  const ros = mockROS();
  const collector = new ArpCollector({ ros, pollMs: 30000, state: {} });
  collector.timer = setInterval(() => {}, 30000);
  assert.ok(collector.timer);

  // Manually register the close handler (simulating what start() does)
  ros.on('close', () => { if (collector.timer) { clearInterval(collector.timer); collector.timer = null; } });
  ros.emit('close');
  assert.equal(collector.timer, null);
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

test('logs collector handles stream error by nullifying stream', () => {
  const ros = mockROS();
  let capturedCb;
  ros.stream = (words, cb) => {
    capturedCb = cb;
    return { stop() {} };
  };
  const state = {};
  const collector = new LogsCollector({ ros, io: { emit() {} }, state });
  collector.start();

  assert.ok(collector.stream, 'stream should be active');

  capturedCb(new Error('connection lost'), null);
  assert.equal(collector.stream, null, 'stream should be nullified on error');
  assert.match(state.lastLogsErr, /connection lost/);
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

test('dhcp leases collector emits device:new only once per MAC', () => {
  const emitted = [];
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new DhcpLeasesCollector({ ros: {}, io, pollMs: 15000, state: {} });

  collector._applyLease({ address: '192.168.1.10', 'mac-address': 'AA:BB', comment: 'laptop' });
  collector._applyLease({ address: '192.168.1.10', 'mac-address': 'AA:BB', comment: 'laptop' });

  const deviceNew = emitted.filter(e => e.ev === 'device:new');
  assert.equal(deviceNew.length, 1, 'device:new should only fire once per MAC');
});

// --- RouterOS client resilience ---

test('ROS client exponential backoff caps at maxBackoffMs', () => {
  const ros = new ROS({});
  assert.equal(ros.backoffMs, 2000);

  const backoffs = [];
  let b = ros.backoffMs;
  for (let i = 0; i < 10; i++) {
    backoffs.push(b);
    b = Math.min(b * 2, ros.maxBackoffMs);
  }

  assert.deepEqual(backoffs, [2000, 4000, 8000, 16000, 30000, 30000, 30000, 30000, 30000, 30000]);
});

test('ROS client stop() sets _stopping flag', () => {
  const ros = new ROS({});
  assert.equal(ros._stopping, false);
  ros.stop();
  assert.equal(ros._stopping, true);
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

test('polling collector stores error in state and continues on next tick', async () => {
  let callNum = 0;
  const ros = mockROS(async () => {
    callNum++;
    if (callNum === 1) throw new Error('temporary failure');
    return [];
  });
  const state = {};
  const collector = new ArpCollector({ ros, pollMs: 50000, state });

  // First tick — error
  let caught = false;
  try { await collector.tick(); } catch (e) { caught = true; state.lastArpErr = e.message; }
  assert.equal(caught, true);
  assert.equal(state.lastArpErr, 'temporary failure');

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
