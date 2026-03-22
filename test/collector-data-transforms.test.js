const test = require('node:test');
const assert = require('node:assert/strict');

const TrafficCollector = require('../src/collectors/traffic');

test('traffic collector emits normalized socket and WAN payloads from a poll cycle', async () => {
  const socketEmits = [];
  const broadcastEmits = [];
  const state = {};
  const ros = {
    connected: true,
    on() {},
    write: async () => [{
      'rx-bits-per-second': '27.8kbps',
      'tx-bits-per-second': '1.5Mbps',
      running: 'true',
      disabled: 'false',
    }],
  };
  const io = {
    engine: { clientsCount: 1 },
    to(id) {
      return {
        emit(ev, data) {
          socketEmits.push({ id, ev, data });
        },
      };
    },
    emit(ev, data) {
      broadcastEmits.push({ ev, data });
    },
  };
  const collector = new TrafficCollector({ ros, io, defaultIf: 'wan', historyMinutes: 1, state });
  collector.subscriptions.set('socket-1', 'wan');

  await collector._pollInterface('wan');

  assert.equal(socketEmits.length, 1);
  assert.equal(socketEmits[0].ev, 'traffic:update');
  assert.equal(socketEmits[0].data.ifName, 'wan');
  assert.equal(socketEmits[0].data.rx_mbps, 0.028);
  assert.equal(socketEmits[0].data.tx_mbps, 1.5);
  assert.equal(socketEmits[0].data.running, true);
  assert.equal(socketEmits[0].data.disabled, false);

  assert.equal(broadcastEmits.length, 1);
  assert.equal(broadcastEmits[0].ev, 'wan:status');
  assert.equal(broadcastEmits[0].data.ifName, 'wan');
  assert.equal(broadcastEmits[0].data.running, true);

  const history = collector.hist.get('wan').toArray();
  assert.equal(history.length, 1);
  assert.equal(history[0].rx_mbps, 0.028);
  assert.equal(history[0].tx_mbps, 1.5);
  assert.equal(typeof state.lastTrafficTs, 'number');
  assert.equal(state.lastTrafficErr, null);
});

test('traffic collector treats missing or zero traffic fields as zero Mbps', async () => {
  const socketEmits = [];
  const ros = {
    connected: true,
    on() {},
    write: async () => [{
      'rx-bits-per-second': undefined,
      'tx-bits-per-second': '0',
      running: false,
      disabled: true,
    }],
  };
  const io = {
    engine: { clientsCount: 1 },
    to() {
      return {
        emit(ev, data) {
          socketEmits.push({ ev, data });
        },
      };
    },
    emit() {},
  };
  const collector = new TrafficCollector({ ros, io, defaultIf: 'wan', historyMinutes: 1, state: {} });
  collector.subscriptions.set('socket-1', 'wan');

  await collector._pollInterface('wan');

  assert.equal(socketEmits[0].data.rx_mbps, 0);
  assert.equal(socketEmits[0].data.tx_mbps, 0);
  assert.equal(socketEmits[0].data.running, false);
  assert.equal(socketEmits[0].data.disabled, true);
});

// --- System Collector ---
const SystemCollector = require('../src/collectors/system');

test('system collector parses CPU, memory, and HDD percentages', async () => {
  const emitted = [];
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const ros = {
    connected: true,
    on() {},
    write: async (cmd) => {
      if (cmd.includes('resource')) return [{ 'cpu-load': '42', 'total-memory': '1073741824', 'free-memory': '536870912', 'total-hdd-space': '134217728', 'free-hdd-space': '67108864', version: '7.16 (stable)', uptime: '3d12h', 'board-name': 'RB4011', 'cpu-count': '4', 'cpu-frequency': '1400' }];
      if (cmd.includes('health')) return [{ name: 'cpu-temperature', value: '47' }];
      if (cmd.includes('update')) return [{ 'latest-version': '7.17', status: 'New version is available' }];
      return [];
    },
  };
  const collector = new SystemCollector({ ros, io, pollMs: 5000, state: {} });
  await collector.tick();

  assert.equal(emitted.length, 1);
  const d = emitted[0].data;
  assert.equal(d.cpuLoad, 42);
  assert.equal(d.memPct, 50);
  assert.equal(d.hddPct, 50);
  assert.equal(d.tempC, 47);
  assert.equal(d.version, '7.16 (stable)');
  assert.equal(d.updateAvailable, true);
  assert.equal(d.latestVersion, '7.17');
  assert.equal(d.boardName, 'RB4011');
  assert.equal(d.cpuCount, 4);
});

test('system collector handles zero total memory without division by zero', async () => {
  const emitted = [];
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const ros = {
    connected: true,
    on() {},
    write: async () => [{}],
  };
  const collector = new SystemCollector({ ros, io, pollMs: 5000, state: {} });
  await collector.tick();

  const d = emitted[0].data;
  assert.equal(d.memPct, 0);
  assert.equal(d.hddPct, 0);
  assert.equal(d.cpuLoad, 0);
});

test('system collector returns null temperature when health data is missing (virtualized RouterOS)', async () => {
  const emitted = [];
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const ros = {
    connected: true,
    on() {},
    write: async (cmd) => {
      if (cmd.includes('resource')) return [{ 'cpu-load': '10', 'total-memory': '1000000', 'free-memory': '500000', version: '7.16' }];
      if (cmd.includes('health')) return [];
      if (cmd.includes('update')) return [];
      return [];
    },
  };
  const collector = new SystemCollector({ ros, io, pollMs: 5000, state: {} });
  await collector.tick();

  assert.equal(emitted[0].data.tempC, null);
});

test('system collector returns null temperature when health query fails entirely', async () => {
  const emitted = [];
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const ros = {
    connected: true,
    on() {},
    write: async (cmd) => {
      if (cmd.includes('resource')) return [{ 'cpu-load': '5', 'total-memory': '1000000', 'free-memory': '500000', version: '7.16' }];
      if (cmd.includes('health')) throw new Error('not supported on CHR');
      if (cmd.includes('update')) return [{ 'latest-version': '7.16', status: 'System is already up to date' }];
      return [];
    },
  };
  const collector = new SystemCollector({ ros, io, pollMs: 5000, state: {} });
  await collector.tick();

  assert.equal(emitted.length, 1);
  assert.equal(emitted[0].data.tempC, null);
  assert.equal(emitted[0].data.cpuLoad, 5);
});

test('system collector detects no update when versions match', async () => {
  const emitted = [];
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const ros = {
    connected: true,
    on() {},
    write: async (cmd) => {
      if (cmd.includes('resource')) return [{ version: '7.16 (stable)' }];
      if (cmd.includes('health')) return [];
      if (cmd.includes('update')) return [{ 'latest-version': '7.16', status: 'System is already up to date' }];
      return [];
    },
  };
  const collector = new SystemCollector({ ros, io, pollMs: 5000, state: {} });
  await collector.tick();

  assert.equal(emitted[0].data.updateAvailable, false);
});

test('system collector handles health items without temperature name', async () => {
  const emitted = [];
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const ros = {
    connected: true,
    on() {},
    write: async (cmd) => {
      if (cmd.includes('resource')) return [{ version: '7.16' }];
      if (cmd.includes('health')) return [{ name: 'voltage', value: '24' }, { name: 'fan-speed', value: '3500' }];
      if (cmd.includes('update')) return [];
      return [];
    },
  };
  const collector = new SystemCollector({ ros, io, pollMs: 5000, state: {} });
  await collector.tick();

  assert.equal(emitted[0].data.tempC, null);
});

// --- Connections Collector ---
const ConnectionsCollector = require('../src/collectors/connections');

test('connections collector counts protocols correctly including case-insensitive icmp', async () => {
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    write: async () => [
      { '.id': '*1', 'src-address': '192.168.1.10', 'dst-address': '1.1.1.1', protocol: 'tcp' },
      { '.id': '*2', 'src-address': '192.168.1.10', 'dst-address': '8.8.8.8', protocol: 'UDP' },
      { '.id': '*3', 'src-address': '192.168.1.10', 'dst-address': '9.9.9.9', protocol: 'icmpv6' },
      { '.id': '*4', 'src-address': '192.168.1.10', 'dst-address': '4.4.4.4', protocol: 'gre' },
    ],
  };
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new ConnectionsCollector({
    ros, io, pollMs: 5000, topN: 5, state: {},
    dhcpNetworks: { getLanCidrs: () => ['192.168.1.0/24'] },
    dhcpLeases: { getNameByIP: () => null, getNameByMAC: () => null },
    arp: { getByIP: () => null },
  });
  await collector.tick();

  const p = emitted[0].data.protoCounts;
  assert.equal(p.tcp, 1);
  assert.equal(p.udp, 1);
  assert.equal(p.icmp, 1);
  assert.equal(p.other, 1);
});

test('connections collector classifies LAN sources and WAN destinations using CIDRs', async () => {
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    write: async () => [
      { '.id': '*1', 'src-address': '192.168.1.10', 'dst-address': '1.1.1.1', protocol: 'tcp', 'dst-port': '443' },
      { '.id': '*2', 'src-address': '10.0.0.5', 'dst-address': '192.168.1.10', protocol: 'tcp', 'dst-port': '80' },
    ],
  };
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new ConnectionsCollector({
    ros, io, pollMs: 5000, topN: 10, state: {},
    dhcpNetworks: { getLanCidrs: () => ['192.168.1.0/24'] },
    dhcpLeases: { getNameByIP: () => null, getNameByMAC: () => null },
    arp: { getByIP: () => null },
  });
  await collector.tick();

  const d = emitted[0].data;
  assert.equal(d.topSources.length, 1);
  assert.equal(d.topSources[0].ip, '192.168.1.10');
  assert.equal(d.topSources[0].count, 1);
  assert.ok(d.topDestinations.length >= 1);
});

test('connections collector uses field fallback chain for src/dst/protocol', async () => {
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    write: async () => [
      { '.id': '*1', src: '192.168.1.10', dst: '1.1.1.1', 'ip-protocol': 'tcp', port: '443' },
    ],
  };
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new ConnectionsCollector({
    ros, io, pollMs: 5000, topN: 5, state: {},
    dhcpNetworks: { getLanCidrs: () => ['192.168.1.0/24'] },
    dhcpLeases: { getNameByIP: () => null, getNameByMAC: () => null },
    arp: { getByIP: () => null },
  });
  await collector.tick();

  const d = emitted[0].data;
  assert.equal(d.protoCounts.tcp, 1);
  assert.equal(d.topSources.length, 1);
});

test('connections collector tracks new connections since last poll', async () => {
  let callNum = 0;
  const responses = [
    [{ '.id': '*1', 'src-address': '192.168.1.10', 'dst-address': '1.1.1.1', protocol: 'tcp' }],
    [{ '.id': '*1', 'src-address': '192.168.1.10', 'dst-address': '1.1.1.1', protocol: 'tcp' },
     { '.id': '*2', 'src-address': '192.168.1.10', 'dst-address': '8.8.8.8', protocol: 'udp' }],
  ];
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    write: async () => responses[callNum++],
  };
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new ConnectionsCollector({
    ros, io, pollMs: 5000, topN: 5, state: {},
    dhcpNetworks: { getLanCidrs: () => ['192.168.1.0/24'] },
    dhcpLeases: { getNameByIP: () => null, getNameByMAC: () => null },
    arp: { getByIP: () => null },
  });

  await collector.tick();
  assert.equal(emitted[0].data.newSinceLast, 1);

  await collector.tick();
  assert.equal(emitted[1].data.newSinceLast, 1);
});

test('connections collector resolves names via DHCP leases then ARP fallback', async () => {
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    write: async () => [
      { '.id': '*1', 'src-address': '192.168.1.10', 'dst-address': '1.1.1.1', protocol: 'tcp' },
      { '.id': '*2', 'src-address': '192.168.1.11', 'dst-address': '1.1.1.1', protocol: 'tcp' },
      { '.id': '*3', 'src-address': '192.168.1.12', 'dst-address': '1.1.1.1', protocol: 'tcp' },
    ],
  };
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new ConnectionsCollector({
    ros, io, pollMs: 5000, topN: 10, state: {},
    dhcpNetworks: { getLanCidrs: () => ['192.168.1.0/24'] },
    dhcpLeases: {
      getNameByIP: (ip) => ip === '192.168.1.10' ? { name: 'laptop', mac: 'AA:BB:CC:DD:EE:FF' } : null,
      getNameByMAC: (mac) => mac === '11:22:33:44:55:66' ? { name: 'phone' } : null,
    },
    arp: {
      getByIP: (ip) => ip === '192.168.1.11' ? { mac: '11:22:33:44:55:66' } : null,
    },
  });
  await collector.tick();

  const sources = emitted[0].data.topSources;
  const byIp = Object.fromEntries(sources.map(s => [s.ip, s]));
  assert.equal(byIp['192.168.1.10'].name, 'laptop');
  assert.equal(byIp['192.168.1.11'].name, 'phone');
  assert.equal(byIp['192.168.1.12'].name, '192.168.1.12');
});

test('connections collector emits IPv6 destination keys, top ports, and geo aggregates', async () => {
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    write: async () => [
      { '.id': '*1', 'src-address': '192.168.1.10', 'dst-address': '2001:db8::1', protocol: 'tcp', 'dst-port': '443' },
      { '.id': '*2', 'src-address': '192.168.1.11', 'dst-address': '2001:db8::1', protocol: 'tcp', 'dst-port': '443' },
      { '.id': '*3', 'src-address': '192.168.1.12', 'dst-address': '198.51.100.2', protocol: 'udp', 'dst-port': '53' },
    ],
  };
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new ConnectionsCollector({
    ros, io, pollMs: 5000, topN: 10, state: {},
    geoLookup: (ip) => {
      if (ip === '2001:db8::1') return { country: 'ZZ', city: 'Lab City' };
      if (ip === '198.51.100.2') return { country: 'YY', city: 'Edge Town' };
      return null;
    },
    dhcpNetworks: { getLanCidrs: () => ['192.168.1.0/24'] },
    dhcpLeases: { getNameByIP: () => null, getNameByMAC: () => null },
    arp: { getByIP: () => null },
  });
  await collector.tick();

  const payload = emitted[0].data;
  assert.equal(payload.topDestinations[0].key, '[2001:db8::1]:443/tcp');
  assert.equal(payload.topDestinations[0].country, 'ZZ');
  assert.equal(payload.topDestinations[0].city, 'Lab City');
  assert.deepEqual(payload.topDestinations[0].proto, { tcp: 2, udp: 0, other: 0 });
  assert.deepEqual(payload.topPorts, [{ port: '443', count: 2 }, { port: '53', count: 1 }]);
  assert.deepEqual(payload.topCountries, [
    { cc: 'ZZ', city: 'Lab City', count: 2, proto: { tcp: 2, udp: 0, other: 0 }, orgs: [] },
    { cc: 'YY', city: 'Edge Town', count: 1, proto: { tcp: 0, udp: 1, other: 0 }, orgs: [] },
  ]);
});

test('connections collector caps work honestly by excluding truncated destinations from aggregates', async () => {
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    write: async () => [
      { '.id': '*1', 'src-address': '192.168.1.10', 'dst-address': '198.51.100.1', protocol: 'tcp', 'dst-port': '443' },
      { '.id': '*2', 'src-address': '192.168.1.11', 'dst-address': '198.51.100.2', protocol: 'udp', 'dst-port': '53' },
      { '.id': '*3', 'src-address': '192.168.1.12', 'dst-address': '198.51.100.3', protocol: 'tcp', 'dst-port': '80' },
    ],
  };
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new ConnectionsCollector({
    ros, io, pollMs: 5000, topN: 10, maxConns: 2, state: {},
    geoLookup: (ip) => ({ country: ip.endsWith('.3') ? 'TRUNC' : 'KEPT', city: ip }),
    dhcpNetworks: { getLanCidrs: () => ['192.168.1.0/24'] },
    dhcpLeases: { getNameByIP: () => null, getNameByMAC: () => null },
    arp: { getByIP: () => null },
  });
  await collector.tick();

  const payload = emitted[0].data;
  assert.equal(payload.processingCapped, true);
  assert.equal(payload.processed, 2);
  assert.ok(!payload.topDestinations.some(d => d.key.includes('198.51.100.3')));
  assert.ok(!payload.topCountries.some(c => c.cc === 'TRUNC'));
  assert.deepEqual(payload.topPorts, [{ port: '443', count: 1 }, { port: '53', count: 1 }]);
});

// --- Firewall Collector ---
const FirewallCollector = require('../src/collectors/firewall');

test('firewall collector calculates delta packets between polls', async () => {
  const emitted = [];
  let loadNum = 0;
  const ros = {
    connected: true,
    on() {},
    stream: (words, cb) => ({ stop() {} }),
    write: async (cmd) => {
      if (cmd.includes('filter')) return loadNum === 0
        ? [{ '.id': '*1', chain: 'forward', action: 'accept', packets: '100', bytes: '50000', disabled: 'false' }]
        : [{ '.id': '*1', chain: 'forward', action: 'accept', packets: '150', bytes: '75000', disabled: 'false' }];
      return []; // nat, mangle empty
    },
  };
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new FirewallCollector({ ros, io, pollMs: 10000, state: {}, topN: 10 });

  await collector._loadInitial();
  assert.equal(emitted[0].data.filter[0].deltaPackets, 0); // no previous
  loadNum++;

  await collector._loadInitial();
  assert.equal(emitted[1].data.filter[0].deltaPackets, 50); // 150 - 100
});

test('firewall collector clamps negative delta to zero on counter reset', async () => {
  const emitted = [];
  let loadNum = 0;
  const ros = {
    connected: true,
    on() {},
    stream: (words, cb) => ({ stop() {} }),
    write: async (cmd) => {
      if (cmd.includes('filter')) return loadNum === 0
        ? [{ '.id': '*1', chain: 'forward', action: 'accept', packets: '1000', bytes: '50000', disabled: 'false' }]
        : [{ '.id': '*1', chain: 'forward', action: 'accept', packets: '10', bytes: '500', disabled: 'false' }];
      return [];
    },
  };
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new FirewallCollector({ ros, io, pollMs: 10000, state: {}, topN: 10 });

  await collector._loadInitial();
  loadNum++;
  await collector._loadInitial();

  assert.equal(emitted[1].data.filter[0].deltaPackets, 0);
});

test('firewall collector filters out disabled rules', async () => {
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    stream: (words, cb) => ({ stop() {} }),
    write: async (cmd) => {
      if (cmd.includes('filter')) return [
        { '.id': '*1', chain: 'forward', action: 'accept', packets: '100', disabled: 'true' },
        { '.id': '*2', chain: 'forward', action: 'drop', packets: '50', disabled: 'false' },
        { '.id': '*3', chain: 'forward', action: 'log', packets: '25', disabled: true },
      ];
      return [];
    },
  };
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new FirewallCollector({ ros, io, pollMs: 10000, state: {}, topN: 10 });
  await collector._loadInitial();

  assert.equal(emitted[0].data.filter.length, 1);
  assert.equal(emitted[0].data.filter[0].id, '*2');
});

test('firewall collector prunes stale entries from prevCounts', async () => {
  const emitted = [];
  let loadNum = 0;
  const ros = {
    connected: true,
    on() {},
    stream: (words, cb) => ({ stop() {} }),
    write: async (cmd) => {
      if (cmd.includes('filter')) return loadNum === 0
        ? [{ '.id': '*1', packets: '100', disabled: 'false' }, { '.id': '*2', packets: '200', disabled: 'false' }]
        : [{ '.id': '*2', packets: '250', disabled: 'false' }];
      return [];
    },
  };
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new FirewallCollector({ ros, io, pollMs: 10000, state: {}, topN: 10 });

  await collector._loadInitial();
  assert.ok(collector.prevCounts.has('*1'));
  assert.ok(collector.prevCounts.has('*2'));
  loadNum++;

  await collector._loadInitial();
  assert.ok(!collector.prevCounts.has('*1'), 'stale *1 should be pruned');
  assert.ok(collector.prevCounts.has('*2'));
});

// --- Ping Collector ---
const PingCollector = require('../src/collectors/ping');

test('ping collector extracts RTT from summary avg-rtt field', async () => {
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    write: async () => [
      { status: 'replied', time: '3ms' },
      { status: 'replied', time: '5ms' },
      { status: 'replied', time: '4ms' },
      { 'avg-rtt': '4ms', sent: '3', received: '3' },
    ],
  };
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new PingCollector({ ros, io, pollMs: 10000, state: {}, target: '1.1.1.1' });
  await collector.tick();

  assert.equal(emitted[0].data.rtt, 4);
  assert.equal(emitted[0].data.loss, 0);
});

test('ping collector calculates loss percentage', async () => {
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    write: async () => [
      { status: 'replied', time: '3ms' },
      { 'avg-rtt': '3ms', sent: '3', received: '1' },
    ],
  };
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new PingCollector({ ros, io, pollMs: 10000, state: {}, target: '1.1.1.1' });
  await collector.tick();

  assert.equal(emitted[0].data.loss, 67);
});

test('ping collector returns null rtt and 100% loss on no replies', async () => {
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    write: async () => [],
  };
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new PingCollector({ ros, io, pollMs: 10000, state: {}, target: '1.1.1.1' });
  await collector.tick();

  assert.equal(emitted[0].data.rtt, null);
  assert.equal(emitted[0].data.loss, 100);
});

test('ping collector falls back to averaging individual reply times when no summary', async () => {
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    write: async () => [
      { status: 'replied', time: '10ms' },
      { status: 'replied', time: '20ms' },
    ],
  };
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new PingCollector({ ros, io, pollMs: 10000, state: {}, target: '1.1.1.1' });
  await collector.tick();

  assert.equal(emitted[0].data.rtt, 15);
  // PING_COUNT=2, both replied → 0% loss
  assert.equal(emitted[0].data.loss, 0);
});

test('ping collector maintains bounded history', async () => {
  const ros = {
    connected: true,
    on() {},
    write: async () => [{ 'avg-rtt': '5ms', sent: '3', received: '3' }],
  };
  const io = { emit() {} };
  const collector = new PingCollector({ ros, io, pollMs: 10000, state: {}, target: '1.1.1.1' });

  for (let i = 0; i < 65; i++) await collector.tick();

  assert.equal(collector.history.toArray().length, 60);
  const h = collector.getHistory();
  assert.equal(h.target, '1.1.1.1');
  assert.equal(h.history.length, 60);
});

// --- Top Talkers Collector ---
const TopTalkersCollector = require('../src/collectors/talkers');

test('talkers collector calculates throughput rate between polls', async () => {
  const emitted = [];
  let callNum = 0;
  const ros = {
    connected: true,
    on() {},
    write: async () => [
      { 'mac-address': 'AA:BB:CC:DD:EE:FF', name: 'laptop', 'bytes-up': callNum === 0 ? '0' : '125000', 'bytes-down': callNum === 0 ? '0' : '250000' },
    ],
  };
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); callNum++; } };
  const collector = new TopTalkersCollector({ ros, io, pollMs: 3000, state: {}, topN: 5 });

  await collector.tick();
  assert.equal(emitted[0].data.devices[0].tx_mbps, 0);
  assert.equal(emitted[0].data.devices[0].rx_mbps, 0);

  // Simulate time passing with fixed timestamp to avoid timing flakiness
  const prev = collector.prev.get('AA:BB:CC:DD:EE:FF');
  const fixedNow = Date.now();
  prev.ts = fixedNow - 1000; // exactly 1 second ago
  prev.up = 0;
  prev.down = 0;
  const origDateNow = Date.now;
  Date.now = () => fixedNow;
  try {
    await collector.tick();
  } finally {
    Date.now = origDateNow;
  }
  // tx = (125000 * 8) / 1 / 1_000_000 = 1.0 Mbps
  // rx = (250000 * 8) / 1 / 1_000_000 = 2.0 Mbps
  assert.equal(emitted[1].data.devices[0].tx_mbps, 1);
  assert.equal(emitted[1].data.devices[0].rx_mbps, 2);
});

test('talkers collector returns zero rate on counter reset', async () => {
  const emitted = [];
  let callNum = 0;
  const ros = {
    connected: true,
    on() {},
    write: async () => [
      { 'mac-address': 'AA:BB:CC:DD:EE:FF', name: 'laptop', 'bytes-up': callNum === 0 ? '1000000' : '100', 'bytes-down': callNum === 0 ? '2000000' : '50' },
    ],
  };
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); callNum++; } };
  const collector = new TopTalkersCollector({ ros, io, pollMs: 3000, state: {}, topN: 5 });

  await collector.tick();
  await collector.tick();

  // On counter reset both rates are 0 — same as tick 1, so fp suppresses the
  // second emit. Assert via lastPayload which is always updated regardless.
  assert.equal(collector.lastPayload.devices[0].tx_mbps, 0);
  assert.equal(collector.lastPayload.devices[0].rx_mbps, 0);
});

test('talkers collector prunes stale devices', async () => {
  let callNum = 0;
  const responses = [
    [{ 'mac-address': 'AA:BB', name: 'a', 'bytes-up': '100', 'bytes-down': '200' },
     { 'mac-address': 'CC:DD', name: 'b', 'bytes-up': '300', 'bytes-down': '400' }],
    [{ 'mac-address': 'AA:BB', name: 'a', 'bytes-up': '200', 'bytes-down': '300' }],
  ];
  const ros = {
    connected: true,
    on() {},
    write: async () => responses[callNum++],
  };
  const io = { engine: { clientsCount: 1 }, emit() {} };
  const collector = new TopTalkersCollector({ ros, io, pollMs: 3000, state: {}, topN: 5 });

  await collector.tick();
  assert.ok(collector.prev.has('CC:DD'));

  await collector.tick();
  assert.ok(!collector.prev.has('CC:DD'), 'stale device CC:DD should be pruned');
});

// --- VPN Collector ---
const VpnCollector = require('../src/collectors/vpn');

test('vpn collector resolves peer name with fallback chain', async () => {
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    stream: (words, cb) => ({ stop() {} }),
    write: async () => [
      { 'public-key': 'AAAA', name: 'myphone', comment: 'backup', 'allowed-address': '10.0.0.2/32', 'last-handshake': '1m30s', 'rx-bytes': '0', 'tx-bytes': '0' },
      { 'public-key': 'BBBB', name: '', comment: 'server', 'allowed-address': '10.0.0.3/32', 'last-handshake': 'never', 'rx-bytes': '0', 'tx-bytes': '0' },
      { 'public-key': 'CCCC', name: '', comment: '', 'allowed-address': '10.0.0.4/32', 'last-handshake': '', 'rx-bytes': '0', 'tx-bytes': '0' },
      { 'public-key': 'DDDDEEEEFFFFGGGG1234567890', name: '', comment: '', 'allowed-address': '', 'last-handshake': '5s', 'rx-bytes': '0', 'tx-bytes': '0' },
      { 'last-handshake': '10s', 'rx-bytes': '0', 'tx-bytes': '0' },
    ],
  };
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new VpnCollector({ ros, io, pollMs: 10000, state: {} });
  await collector._loadInitial();

  const t = emitted[0].data.tunnels;
  assert.equal(t[0].name, 'myphone');
  assert.equal(t[1].name, 'server');
  assert.equal(t[2].name, '10.0.0.4/32');
  assert.equal(t[3].name, 'DDDDEEEEFFFFGGGG' + '\u2026');
  assert.equal(t[4].name, '?');
});

test('vpn collector detects connected vs idle state', async () => {
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    stream: (words, cb) => ({ stop() {} }),
    write: async () => [
      { 'public-key': 'A', 'last-handshake': '30s', 'rx-bytes': '0', 'tx-bytes': '0' },
      { 'public-key': 'B', 'last-handshake': 'never', 'rx-bytes': '0', 'tx-bytes': '0' },
      { 'public-key': 'C', 'last-handshake': '', 'rx-bytes': '0', 'tx-bytes': '0' },
    ],
  };
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new VpnCollector({ ros, io, pollMs: 10000, state: {} });
  await collector._loadInitial();

  const t = emitted[0].data.tunnels;
  assert.equal(t[0].state, 'connected');
  assert.equal(t[1].state, 'idle');
  assert.equal(t[2].state, 'idle');
});

test('vpn collector calculates rates between polls and prunes stale peers', async () => {
  const emitted = [];
  let loadNum = 0;
  const responses = [
    [
      { 'public-key': 'A', name: 'phone', 'last-handshake': '10s', 'rx-bytes': '1000', 'tx-bytes': '2000' },
      { 'public-key': 'B', name: 'tablet', 'last-handshake': 'never', 'rx-bytes': '500', 'tx-bytes': '500' },
    ],
    [
      { 'public-key': 'A', name: 'phone', 'last-handshake': '5s', 'rx-bytes': '3000', 'tx-bytes': '5000' },
    ],
  ];
  const ros = {
    connected: true,
    on() {},
    stream: (words, cb) => ({ stop() {} }),
    write: async () => responses[loadNum++],
  };
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new VpnCollector({ ros, io, pollMs: 10000, state: {} });

  await collector._loadInitial();
  const prev = collector._prev.get('A');
  const fixedNow = Date.now();
  prev.ts = fixedNow - 1000;
  prev.rx = 1000;
  prev.tx = 2000;
  const origDateNow = Date.now;
  Date.now = () => fixedNow;
  try {
    await collector._loadInitial();
  } finally {
    Date.now = origDateNow;
  }

  assert.equal(emitted[1].data.tunnels.length, 1);
  assert.equal(emitted[1].data.tunnels[0].name, 'phone');
  assert.equal(emitted[1].data.tunnels[0].rxRate, 2000);
  assert.equal(emitted[1].data.tunnels[0].txRate, 3000);
  assert.ok(!collector._prev.has('B'), 'stale peer should be pruned from previous counters');
});

// --- Wireless Collector ---
const WirelessCollector = require('../src/collectors/wireless');

test('wireless collector detects band from RouterOS band field', async () => {
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    cfg: {},
    write: async () => [
      { 'mac-address': 'AA:BB', interface: 'wifi1', band: '5ghz-n/ac/ax', signal: '-50' },
      { 'mac-address': 'CC:DD', interface: 'wifi3', band: '6ghz-ax',       signal: '-60' },
      { 'mac-address': 'EE:FF', interface: 'wlan0', band: '2ghz-b/g/n',    signal: '-70' },
      { 'mac-address': '11:22', interface: 'wlan0', band: '5ghz-ax',       signal: '-55' },
    ],
  };
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new WirelessCollector({
    ros, io, pollMs: 5000, state: {},
    dhcpLeases: { getNameByMAC: () => null },
    arp: { getByMAC: () => null },
  });
  await collector.tick();

  const clients = emitted[0].data.clients;
  const byMac = Object.fromEntries(clients.map(c => [c.mac, c]));
  assert.equal(byMac['AA:BB'].band, '5GHz');
  assert.equal(byMac['CC:DD'].band, '6GHz');
  assert.equal(byMac['EE:FF'].band, '2.4GHz');
  assert.equal(byMac['11:22'].band, '5GHz');
});

test('wireless collector sorts clients by signal strength descending', async () => {
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    cfg: {},
    write: async () => [
      { 'mac-address': 'AA:BB', signal: '-70', interface: 'wifi1' },
      { 'mac-address': 'CC:DD', signal: '-40', interface: 'wifi1' },
      { 'mac-address': 'EE:FF', signal: '-55', interface: 'wifi1' },
    ],
  };
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new WirelessCollector({
    ros, io, pollMs: 5000, state: {},
    dhcpLeases: { getNameByMAC: () => null },
    arp: { getByMAC: () => null },
  });
  await collector.tick();

  const macs = emitted[0].data.clients.map(c => c.mac);
  assert.deepEqual(macs, ['CC:DD', 'EE:FF', 'AA:BB']);
});

test('wireless collector enriches payloads with DHCP names, ARP IPs, and suppresses transient empty results', async () => {
  const emitted = [];
  let callNum = 0;
  const ros = {
    connected: true,
    on() {},
    cfg: {},
    write: async () => callNum++ === 0
      ? [{ 'mac-address': 'AA:BB', signal: '-55', interface: 'wifi1', 'tx-rate': 'HE-MCS 11 80MHz', ssid: 'Office' }]
      : [],
  };
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new WirelessCollector({
    ros, io, pollMs: 5000, state: {},
    dhcpLeases: { getNameByMAC: (mac) => mac === 'AA:BB' ? { name: 'Laptop' } : null },
    arp: { getByMAC: (mac) => mac === 'AA:BB' ? { ip: '192.168.1.20' } : null },
  });

  await collector.tick();                     // tick 1: returns client — emits
  assert.equal(emitted.length, 1);
  assert.equal(emitted[0].data.clients[0].name, 'Laptop');
  assert.equal(emitted[0].data.clients[0].ip, '192.168.1.20');
  assert.equal(emitted[0].data.clients[0].ssid, 'Office');
  assert.equal(emitted[0].data.mode, 'wifi');

  await collector.tick();                     // tick 2: empty — suppressed (emptyTicks=1)
  assert.equal(emitted.length, 1);
  await collector.tick();                     // tick 3: empty — suppressed (emptyTicks=2)
  assert.equal(emitted.length, 1);
  await collector.tick();                     // tick 4: empty — authoritative (emptyTicks=3), emits []
  assert.equal(emitted.length, 2);
  assert.deepEqual(emitted[1].data.clients, []);
  assert.equal(emitted[1].data.mode, 'wifi');
});

// --- Logs Collector ---
const LogsCollector = require('../src/collectors/logs');

test('logs collector emits severity-classified entries from stream callbacks and drops empty messages', () => {
  const emitted = [];
  let streamHandler;
  const ros = {
    connected: true,
    on() {},
    stream(words, cb) {
      streamHandler = cb;
      return { stop() {} };
    },
  };
  const state = {};
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new LogsCollector({ ros, io, state });
  collector.start();

  streamHandler(null, { message: 'test log', topics: 'system,error', time: '12:00:00' });
  assert.equal(emitted.length, 1);
  assert.equal(emitted[0].ev, 'logs:new');
  assert.equal(emitted[0].data.severity, 'error');
  assert.equal(emitted[0].data.message, 'test log');
  assert.equal(emitted[0].data.time, '12:00:00');
  assert.equal(state.lastLogsErr, null);

  streamHandler(null, { topics: 'system' });
  assert.equal(emitted.length, 1);
  streamHandler(null, null);
  assert.equal(emitted.length, 1);
});

// --- DHCP Leases Collector ---
const DhcpLeasesCollector = require('../src/collectors/dhcpLeases');

test('dhcp leases collector resolves name with comment > hostname > empty fallback', async () => {
  let streamHandler;
  const ros = {
    connected: true,
    on() {},
    write: async () => [
      { address: '192.168.1.10', 'mac-address': 'AA:BB', comment: '  MyLaptop  ', 'host-name': 'generic-host' },
      { address: '192.168.1.11', 'mac-address': 'CC:DD', comment: '', 'host-name': 'phone' },
    ],
    stream(words, cb) {
      streamHandler = cb;
      return { stop() {} };
    },
  };
  const collector = new DhcpLeasesCollector({ ros, io: { emit() {} }, pollMs: 15000, state: {} });

  await collector.start();
  streamHandler(null, { address: '192.168.1.12', 'mac-address': 'EE:FF', comment: '   ', 'host-name': '  ' });

  assert.equal(collector.getNameByIP('192.168.1.10').name, 'MyLaptop');
  assert.equal(collector.getNameByIP('192.168.1.11').name, 'phone');
  assert.equal(collector.getNameByIP('192.168.1.12').name, '');
});

test('dhcp leases collector filters active leases after initial load and streamed updates', async () => {
  let streamHandler;
  const ros = {
    connected: true,
    on() {},
    write: async () => [
      { address: '192.168.1.1', 'mac-address': 'A1', status: 'bound' },
      { address: '192.168.1.2', 'mac-address': 'A2', status: 'offered' },
    ],
    stream(words, cb) {
      streamHandler = cb;
      return { stop() {} };
    },
  };
  const collector = new DhcpLeasesCollector({ ros, io: { emit() {} }, pollMs: 15000, state: {} });
  await collector.start();
  streamHandler(null, { address: '192.168.1.3', 'mac-address': 'A3', status: '' });
  streamHandler(null, { address: '192.168.1.4', 'mac-address': 'A4', status: 'expired' });

  const active = collector.getActiveLeaseIPs();
  assert.ok(active.includes('192.168.1.1'));
  assert.ok(active.includes('192.168.1.2'));
  assert.ok(active.includes('192.168.1.3'));
  assert.ok(!active.includes('192.168.1.4'));
});

// --- Interface Status Collector ---
const InterfaceStatusCollector = require('../src/collectors/interfaceStatus');

test('interface status collector normalizes booleans and computes Mbps', async () => {
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    stream: (words, cb) => ({ stop() {} }),
    write: async (cmd) => {
      if (cmd.includes('interface')) return [
        { name: 'ether1', type: 'ether', running: 'true', disabled: 'false', 'rx-byte': '1000000', 'tx-byte': '500000', 'rx-bits-per-second': '15000000', 'tx-bits-per-second': '8500000' },
        { name: 'ether2', type: 'ether', running: true, disabled: false, 'rx-bits-per-second': '0', 'tx-bits-per-second': '0' },
      ];
      if (cmd.includes('address')) return [
        { interface: 'ether1', address: '192.168.1.1/24' },
        { interface: 'ether1', address: '10.0.0.1/24' },
      ];
      return [];
    },
  };
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new InterfaceStatusCollector({ ros, io, pollMs: 5000, state: {} });
  await collector._loadInitial();

  const ifaces = emitted[0].data.interfaces;
  assert.equal(ifaces[0].running, true);
  assert.equal(ifaces[0].disabled, false);
  assert.equal(ifaces[0].rxMbps, 15);
  assert.equal(ifaces[0].txMbps, 8.5);
  assert.deepEqual(ifaces[0].ips, ['192.168.1.1/24', '10.0.0.1/24']);
  assert.equal(ifaces[1].running, true);
  assert.equal(ifaces[1].rxMbps, 0);
});

test('interface status collector clamps malformed throughput fields to zero', async () => {
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    stream: (words, cb) => ({ stop() {} }),
    write: async (cmd) => {
      if (cmd.includes('interface')) return [
        { name: 'ether1', running: 'true', disabled: 'false', 'rx-bits-per-second': 'bad-data', 'tx-bits-per-second': '', 'rx-byte': 'oops', 'tx-byte': null },
      ];
      if (cmd.includes('address')) return [];
      return [];
    },
  };
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new InterfaceStatusCollector({ ros, io, pollMs: 5000, state: {} });
  await collector._loadInitial();

  const iface = emitted[0].data.interfaces[0];
  assert.equal(iface.rxBytes, 0);
  assert.equal(iface.txBytes, 0);
  assert.equal(iface.rxMbps, 0);
  assert.equal(iface.txMbps, 0);
});

// --- ARP Collector ---
const ArpCollector = require('../src/collectors/arp');

test('arp collector builds bidirectional lookup maps and skips incomplete entries', async () => {
  const ros = {
    connected: true,
    on() {},
    stream: (words, cb) => ({ stop() {} }),
    write: async () => [
      { address: '192.168.1.10', 'mac-address': 'AA:BB:CC:DD:EE:FF', interface: 'bridge' },
      { address: '192.168.1.11' },
      { 'mac-address': 'CC:DD:EE:FF:00:11' },
      { address: '192.168.1.12', 'mac-address': '11:22:33:44:55:66' },
    ],
  };
  const collector = new ArpCollector({ ros, pollMs: 30000, state: {} });
  await collector._loadInitial();

  const byIp = collector.getByIP('192.168.1.10');
  assert.equal(byIp.mac, 'AA:BB:CC:DD:EE:FF');
  assert.equal(byIp.iface, 'bridge');

  const byMac = collector.getByMAC('AA:BB:CC:DD:EE:FF');
  assert.equal(byMac.ip, '192.168.1.10');

  assert.equal(collector.getByIP('192.168.1.11'), null);
  assert.equal(collector.getByMAC('CC:DD:EE:FF:00:11'), null);
  assert.equal(collector.getByIP('192.168.1.12').mac, '11:22:33:44:55:66');
});

test('arp collector replaces stale snapshot entries on each poll', async () => {
  let loadNum = 0;
  const ros = {
    connected: true,
    on() {},
    stream: (words, cb) => ({ stop() {} }),
    write: async () => loadNum++ === 0
      ? [{ address: '192.168.1.10', 'mac-address': 'AA:BB', interface: 'bridge' }]
      : [{ address: '192.168.1.11', 'mac-address': 'CC:DD', interface: 'bridge' }],
  };
  const collector = new ArpCollector({ ros, pollMs: 30000, state: {} });

  await collector._loadInitial();
  await collector._loadInitial();

  assert.equal(collector.getByIP('192.168.1.10'), null);
  assert.equal(collector.getByMAC('AA:BB'), null);
  assert.equal(collector.getByIP('192.168.1.11').mac, 'CC:DD');
});

// --- DHCP Networks Collector ---
const DhcpNetworksCollector = require('../src/collectors/dhcpNetworks');

test('dhcp networks collector counts leases per CIDR and extracts WAN IP', async () => {
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    write: async (cmd) => {
      if (cmd.includes('network')) return [
        { address: '192.168.1.0/24', gateway: '192.168.1.1', 'dns-server': '1.1.1.1' },
        { address: '10.0.0.0/24', gateway: '10.0.0.1' },
      ];
      if (cmd.includes('address')) return [
        { interface: 'WAN1', address: '203.0.113.5/30' },
        { interface: 'bridge', address: '192.168.1.1/24' },
      ];
      return [];
    },
  };
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const leases = {
    getActiveLeaseIPs: () => ['192.168.1.10', '192.168.1.11', '10.0.0.5'], getAllLeaseIPs: () => ['192.168.1.10', '192.168.1.11', '10.0.0.5'],
  };
  const collector = new DhcpNetworksCollector({ ros, io, pollMs: 15000, dhcpLeases: leases, state: {}, wanIface: 'WAN1' });
  await collector.tick();

  const d = emitted[0].data;
  assert.deepEqual(d.lanCidrs, ['192.168.1.0/24', '10.0.0.0/24']);
  assert.equal(d.wanIp, '203.0.113.5/30');
  assert.equal(d.networks[0].leaseCount, 2);
  assert.equal(d.networks[1].leaseCount, 1);
});

test('dhcp networks collector handles one query failing gracefully', async () => {
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    write: async (cmd) => {
      if (cmd.includes('network')) throw new Error('timeout');
      if (cmd.includes('address')) return [{ interface: 'WAN1', address: '1.2.3.4/30' }];
      return [];
    },
  };
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new DhcpNetworksCollector({ ros, io, pollMs: 15000, dhcpLeases: { getActiveLeaseIPs: () => [], getAllLeaseIPs: () => [] }, state: {}, wanIface: 'WAN1' });
  await collector.tick();

  assert.equal(emitted[0].data.networks.length, 0);
  assert.equal(emitted[0].data.wanIp, '1.2.3.4/30');
});

test('dhcp networks collector clears WAN IP when the configured WAN interface is absent', async () => {
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    write: async (cmd) => {
      if (cmd.includes('network')) return [{ address: '192.168.1.0/24', gateway: '192.168.1.1' }];
      if (cmd.includes('address')) return [{ interface: 'bridge', address: '192.168.1.1/24' }];
      return [];
    },
  };
  const state = { lastWanIp: '203.0.113.5/30' };
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new DhcpNetworksCollector({ ros, io, pollMs: 15000, dhcpLeases: { getActiveLeaseIPs: () => [], getAllLeaseIPs: () => [] }, state, wanIface: 'WAN1' });
  await collector.tick();

  assert.equal(emitted[0].data.wanIp, '');
  assert.equal(state.lastWanIp, '');
});

// ═══════════════════════════════════════════════════════════════════════════
// --- Routing Collector ---
// ═══════════════════════════════════════════════════════════════════════════
const RoutingCollector = require('../src/collectors/routing');
function makeRoutingRos({ printRows = [], sessionRows = [], peerCfgRows = [] } = {}) {
  return {
    connected: true,
    on() {},
    write: async (cmd) => {
      if (cmd.includes('/routing/bgp/session')) return sessionRows;
      if (cmd.includes('/routing/bgp/peer'))    return peerCfgRows;
      if (cmd.includes('/ip/route'))            return printRows;
      return [];
    },
    stream: (words, cb) => ({ stop() {} }),
  };
}

// ── start() happy path ───────────────────────────────────────────────────────

test('routing collector start() emits correct payload with routes and BGP sessions', async () => {
  const emitted = [];
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const state = {};
  const ros = makeRoutingRos({
    printRows: [
      { '.id': '*1', 'dst-address': '0.0.0.0/0',     gateway: '10.0.0.1', distance: '1',  '.flags': 'AS' },
      { '.id': '*2', 'dst-address': '192.168.1.0/24', gateway: 'bridge',   distance: '0',  '.flags': 'AC' },
    ],
    sessionRows: [{
      name: 'peer1', 'remote.address': '10.0.0.1', 'remote.as': '65001',
      state: 'established', uptime: '1h', 'prefix-count': '100',
      'updates-sent': '10', 'updates-received': '20',
    }],
    peerCfgRows: [{ 'remote.address': '10.0.0.1', comment: 'Transit A' }],
  });

  const collector = new RoutingCollector({ ros, io, pollMs: 10000, state });
  await collector.start();

  const d = emitted[emitted.length - 1].data;
  assert.equal(d.peers.length, 1);
  assert.equal(d.peers[0].state, 'established');
  assert.equal(d.peers[0].prefixes, 100);
  assert.equal(d.peers[0].description, 'Transit A');
  assert.equal(d.routes.length, 1);
  assert.equal(d.routes[0].dst, '0.0.0.0/0');
  assert.equal(d.routes[0].type, 'static');
  assert.equal(d.routeCounts.total, 2);
  assert.equal(d.routeCounts.static, 1);
  assert.equal(d.routeCounts.connect, 1);
  assert.equal(d.summary.established, 1);
  assert.equal(d.pollMs, 0, 'pollMs must be 0 for streamed collector');
  assert.ok(state.lastRoutingTs > 0);
  assert.equal(state.lastRoutingErr, null);
});

// ── _applySessionDelta / _buildPeers ─────────────────────────────────────────

test('routing collector BGP session state change triggers emit and is reflected in peers', async () => {
  const emitted = [];
  let bgpCb;
  const ros = {
    connected: true, on() {},
    write: async () => [],
    stream: (words, cb) => {
      if (words[0] && words[0].includes('bgp')) bgpCb = cb;
      return { stop() {} };
    },
  };
  const collector = new RoutingCollector({ ros, io: { emit(ev, d) { emitted.push({ ev, data: d }); } }, pollMs: 10000, state: {} });
  await collector.start();
  const countBefore = emitted.length;

  bgpCb(null, { name: 'p1', 'remote.address': '10.0.0.1', 'remote.as': '65001', state: 'established', 'prefix-count': '50' });
  await new Promise(r => setTimeout(r, 10));

  assert.ok(emitted.length > countBefore, 'state change triggers emit');
  const d = emitted[emitted.length - 1].data;
  assert.equal(d.peers[0].state, 'established');
  assert.equal(d.peers[0].prefixes, 50);
});

test('routing collector BGP keepalive-only update is suppressed by fingerprint', async () => {
  const emitted = [];
  let bgpCb;
  const ros = {
    connected: true, on() {},
    write: async () => [],
    stream: (words, cb) => {
      if (words[0] && words[0].includes('bgp')) bgpCb = cb;
      return { stop() {} };
    },
  };
  const collector = new RoutingCollector({ ros, io: { emit(ev, d) { emitted.push(d); } }, pollMs: 10000, state: {} });
  await collector.start();

  // First event — sets the fingerprint baseline
  bgpCb(null, { name: 'p1', 'remote.address': '10.0.0.1', state: 'established', 'prefix-count': '50', uptime: '1h' });
  await new Promise(r => setTimeout(r, 10));
  const countAfterFirst = emitted.length;

  // Second event — only uptime changed (keepalive), state/prefixes identical
  bgpCb(null, { name: 'p1', 'remote.address': '10.0.0.1', state: 'established', 'prefix-count': '50', uptime: '1h10m' });
  await new Promise(r => setTimeout(r, 10));

  assert.equal(emitted.length, countAfterFirst, 'keepalive-only update must be suppressed');
});

test('routing collector BGP prefix count change is not suppressed', async () => {
  const emitted = [];
  let bgpCb;
  const ros = {
    connected: true, on() {},
    write: async () => [],
    stream: (words, cb) => {
      if (words[0] && words[0].includes('bgp')) bgpCb = cb;
      return { stop() {} };
    },
  };
  const collector = new RoutingCollector({ ros, io: { emit(ev, d) { emitted.push(d); } }, pollMs: 10000, state: {} });
  await collector.start();

  bgpCb(null, { name: 'p1', 'remote.address': '10.0.0.1', state: 'established', 'prefix-count': '50', uptime: '1h' });
  await new Promise(r => setTimeout(r, 10));
  const countAfterFirst = emitted.length;

  // Prefix count changes — must emit
  bgpCb(null, { name: 'p1', 'remote.address': '10.0.0.1', state: 'established', 'prefix-count': '75', uptime: '1h10m' });
  await new Promise(r => setTimeout(r, 10));

  assert.ok(emitted.length > countAfterFirst, 'prefix count change must trigger emit');
  assert.equal(emitted[emitted.length - 1].peers[0].prefixes, 75);
});

test('routing collector BGP peer removed via .dead=true clears session', async () => {
  const emitted = [];
  let bgpCb;
  const ros = {
    connected: true, on() {},
    write: async () => [],
    stream: (words, cb) => {
      if (words[0] && words[0].includes('bgp')) bgpCb = cb;
      return { stop() {} };
    },
  };
  const collector = new RoutingCollector({ ros, io: { emit(ev, d) { emitted.push(d); } }, pollMs: 10000, state: {} });
  await collector.start();

  bgpCb(null, { name: 'p1', 'remote.address': '10.0.0.1', state: 'established', 'prefix-count': '50' });
  await new Promise(r => setTimeout(r, 10));
  assert.equal(collector._sessions.size, 1);

  bgpCb(null, { name: 'p1', 'remote.address': '10.0.0.1', '.dead': 'true' });
  await new Promise(r => setTimeout(r, 10));
  assert.equal(collector._sessions.size, 0, 'session removed on .dead=true');
});

// ── Route stream delta ────────────────────────────────────────────────────────

test('routing collector route stream delta adds new route', async () => {
  const emitted = [];
  const collector = new RoutingCollector({ ros: makeRoutingRos(), io: { emit(ev, d) { emitted.push(d); } }, pollMs: 10000, state: {} });
  await collector._loadRoutes();
  collector._applyRouteDelta({ '.id': '*5', 'dst-address': '10.0.0.0/8', gateway: '1.2.3.1', distance: '1', '.flags': 'AS' });
  collector._emit(null);
  assert.equal(emitted[0].routes.length, 1);
  assert.equal(emitted[0].routes[0].dst, '10.0.0.0/8');
});

test('routing collector route stream delta deletes route via .dead=true', async () => {
  const emitted = [];
  const ros = makeRoutingRos({
    printRows: [
      { '.id': '*1', 'dst-address': '0.0.0.0/0',  gateway: '1.2.3.1', distance: '1', '.flags': 'AS' },
      { '.id': '*2', 'dst-address': '10.0.0.0/8', gateway: '1.2.3.1', distance: '1', '.flags': 'AS' },
    ],
  });
  const collector = new RoutingCollector({ ros, io: { emit(ev, d) { emitted.push(d); } }, pollMs: 10000, state: {} });
  await collector._loadRoutes();
  collector._applyRouteDelta({ '.id': '*1', '.dead': 'true' });
  collector._emit(null);
  assert.equal(collector._routes.size, 1);
  assert.equal(emitted[0].routes[0].dst, '10.0.0.0/8');
});

test('routing collector route stream partial row merges with stored raw', async () => {
  const collector = new RoutingCollector({ ros: makeRoutingRos(), io: { emit() {} }, pollMs: 10000, state: {} });
  collector._routes.set('*1', collector._mapRoute({ '.id': '*1', 'dst-address': '0.0.0.0/0', gateway: '1.2.3.1', distance: '1', '.flags': 'AS', comment: 'orig' }));
  collector._applyRouteDelta({ '.id': '*1', distance: '5' });
  const r = collector._routes.get('*1');
  assert.equal(r.distance, 5);
  assert.equal(r.gateway, '1.2.3.1');
  assert.equal(r.comment, 'orig');
});

// ── _emit(null) reuses last peers ─────────────────────────────────────────────

test('routing collector _emit(null) reuses last known peers from lastPayload', async () => {
  const emitted = [];
  const ros = makeRoutingRos({
    sessionRows: [{ name: 'p1', 'remote.address': '10.0.0.1', 'remote.as': '65001', state: 'established', 'prefix-count': '50' }],
  });
  const collector = new RoutingCollector({ ros, io: { emit(ev, d) { emitted.push(d); } }, pollMs: 10000, state: {} });
  await collector.start();

  // Route stream event fires — reuses BGP peers from lastPayload
  collector._applyRouteDelta({ '.id': '*1', 'dst-address': '1.0.0.0/8', gateway: '10.0.0.1', distance: '1', '.flags': 'AS' });
  collector._emit(null);

  assert.equal(emitted[emitted.length - 1].peers.length, 1, 'last known peers reused');
});

test('routing collector _emit(null) before any peers returns empty array', async () => {
  const emitted = [];
  const collector = new RoutingCollector({ ros: makeRoutingRos(), io: { emit(ev, d) { emitted.push(d); } }, pollMs: 10000, state: {} });
  collector._emit(null);
  assert.deepEqual(emitted[0].peers, []);
});

// ── Flag inference / type classification ──────────────────────────────────────

test('routing collector keeps active routes with no .flags via IP-gateway inference', async () => {
  const emitted = [];
  const ros = makeRoutingRos({
    printRows: [
      { '.id': '*1', 'dst-address': '0.0.0.0/0',      gateway: '192.168.88.1', distance: '1' },
      { '.id': '*2', 'dst-address': '172.16.0.0/12',   gateway: '10.0.0.1',    distance: '1', '.flags': 'Xs' },
      { '.id': '*3', 'dst-address': '192.168.88.0/24', gateway: 'bridge',       distance: '0' },
    ],
  });
  const collector = new RoutingCollector({ ros, io: { emit(ev, d) { emitted.push(d); } }, pollMs: 10000, state: {} });
  await collector._loadRoutes();
  collector._emit(null);

  const dsts = emitted[0].routes.map(r => r.dst);
  assert.ok(dsts.includes('0.0.0.0/0'),     'IP-gateway route kept');
  assert.ok(dsts.includes('172.16.0.0/12'), 'disabled route kept');
  assert.ok(!dsts.includes('192.168.88.0/24'), 'interface-name gateway excluded');
});

test('routing collector excludes interface-name-gateway routes consistently across ticks', async () => {
  const emitted = [];
  let tick = 0;
  const ros = {
    connected: true, on() {},
    write: async (cmd) => {
      if (!cmd.includes('/ip/route')) return [];
      return tick++ === 0
        ? [{ '.id': '*1', 'dst-address': '192.168.1.0/24', gateway: 'bridge', distance: '0' }]
        : [{ '.id': '*1', 'dst-address': '192.168.1.0/24', gateway: 'bridge', distance: '0', '.flags': 'AC' }];
    },
    stream: (w, cb) => ({ stop() {} }),
  };
  const collector = new RoutingCollector({ ros, io: { emit(ev, d) { emitted.push(d); } }, pollMs: 10000, state: {} });
  await collector._loadRoutes(); collector._emit(null);
  await collector._loadRoutes(); collector._emit(null);
  assert.equal(emitted[0].routes.length, 0, 'tick 1 (no .flags): interface route excluded');
  assert.equal(emitted[1].routes.length, 0, 'tick 2 (.flags=AC): interface route excluded');
});

// ── Route counts ──────────────────────────────────────────────────────────────

test('routing collector counts all route protocol types correctly', async () => {
  const emitted = [];
  const collector = new RoutingCollector({ ros: makeRoutingRos(), io: { emit(ev, d) { emitted.push(d); } }, pollMs: 10000, state: {} });
  [
    { '.id': '*1', 'dst-address': '0.0.0.0/0',     gateway: '1.2.3.1', distance: '1',   '.flags': 'AS'  },
    { '.id': '*2', 'dst-address': '10.0.0.0/8',    gateway: '1.2.3.1', distance: '20',  '.flags': 'Ab'  },
    { '.id': '*3', 'dst-address': '172.16.0.0/12',  gateway: '1.2.3.1', distance: '20',  '.flags': 'Ab'  },
    { '.id': '*4', 'dst-address': '192.168.0.0/24', gateway: 'bridge',  distance: '0',   '.flags': 'AC'  },
    { '.id': '*5', 'dst-address': '192.168.2.0/24', gateway: '10.1.0.1', distance: '110', '.flags': 'Ao' },
  ].forEach(r => collector._routes.set(r['.id'], collector._mapRoute(r)));
  collector._emit(null);

  const c = emitted[0].routeCounts;
  assert.equal(c.total,   5);
  assert.equal(c.static,  1);
  assert.equal(c.bgp,     2);
  assert.equal(c.connect, 1);
  assert.equal(c.ospf,    1);
});

// ── Empty / malformed data ────────────────────────────────────────────────────

test('routing collector emits empty payload without crash when router has no data', async () => {
  const emitted = [];
  const state = {};
  const collector = new RoutingCollector({ ros: makeRoutingRos(), io: { emit(ev, d) { emitted.push(d); } }, pollMs: 10000, state });
  await collector.start();
  const d = emitted[emitted.length - 1];
  assert.deepEqual(d.peers, []);
  assert.deepEqual(d.routes, []);
  assert.equal(d.routeCounts.total, 0);
  assert.ok(state.lastRoutingTs > 0);
  assert.equal(state.lastRoutingErr, null);
});

test('routing collector malformed numeric fields clamped to 0', async () => {
  const emitted = [];
  const ros = makeRoutingRos({
    printRows:   [{ '.id': '*1', 'dst-address': '1.2.3.0/24', gateway: '1.2.3.1', distance: 'bad', '.flags': 'AS' }],
    sessionRows: [{ name: 'bad', 'remote.address': '10.0.0.1', 'remote.as': 'notanumber', state: 'established', 'prefix-count': 'bad', 'updates-sent': null }],
  });
  const collector = new RoutingCollector({ ros, io: { emit(ev, d) { emitted.push(d); } }, pollMs: 10000, state: {} });
  await collector.start();
  const d = emitted[emitted.length - 1];
  assert.equal(d.routes[0].distance, 0);
  assert.equal(d.peers[0].remoteAs, 0);
  assert.equal(d.peers[0].prefixes, 0);
  assert.equal(d.peers[0].updatesSent, 0);
});

// ── Helpers ───────────────────────────────────────────────────────────────────

test('routing collector parses RouterOS uptime formats correctly', () => {
  const c = new RoutingCollector({ ros: { on() {} }, io: { emit() {} }, pollMs: 10000, state: {} });
  assert.equal(c._parseUptime('1d2h3m4s'), 86400 + 7200 + 180 + 4);
  assert.equal(c._parseUptime('12:34:56'), 12 * 3600 + 34 * 60 + 56);
  assert.equal(c._parseUptime('30m'), 1800);
  assert.equal(c._parseUptime(''), 0);
  assert.equal(c._parseUptime(null), 0);
});

test('routing collector classifies peers by ASN and description', () => {
  const c = new RoutingCollector({ ros: { on() {} }, io: { emit() {} }, pollMs: 10000, state: {} });
  assert.equal(c._classifyPeer(65001, '', ''), 'private');
  assert.equal(c._classifyPeer(4200000001, '', ''), 'private');
  assert.equal(c._classifyPeer(13335, 'ix peering', ''), 'ix');
  assert.equal(c._classifyPeer(1299, 'transit', ''), 'upstream');
});

test('routing collector normalises BGP state strings', async () => {
  const emitted = [];
  const ros = makeRoutingRos({
    sessionRows: [
      { name: 'a', 'remote.address': '10.0.0.1', state: 'Established' },
      { name: 'b', 'remote.address': '10.0.0.2', state: 'Active' },
      { name: 'c', 'remote.address': '10.0.0.3', state: '', established: 'true' },
      { name: 'd', 'remote.address': '10.0.0.4', state: 'idle' },
    ],
  });
  const collector = new RoutingCollector({ ros, io: { emit(ev, d) { emitted.push(d); } }, pollMs: 10000, state: {} });
  await collector.start();
  const states = emitted[emitted.length - 1].peers.map(p => p.state);
  assert.equal(states[0], 'established');
  assert.equal(states[1], 'active');
  assert.equal(states[2], 'established');
  assert.equal(states[3], 'idle');
});

test('routing collector ghost sessions with no address and no name are excluded', async () => {
  const emitted = [];
  const ros = makeRoutingRos({
    sessionRows: [
      { name: 'real', 'remote.address': '10.0.0.1', 'remote.as': '65001', state: 'established' },
      { name: '',     'remote.address': '', state: 'idle' },
      { name: '?',    'remote.address': '', state: 'idle' },
    ],
  });
  const collector = new RoutingCollector({ ros, io: { emit(ev, d) { emitted.push(d); } }, pollMs: 10000, state: {} });
  await collector.start();
  assert.equal(emitted[emitted.length - 1].peers.length, 1);
  assert.equal(emitted[emitted.length - 1].peers[0].name, 'real');
});

test('routing collector legacy bgp/peer/print used when session endpoint returns empty', async () => {
  const emitted = [];
  const ros = {
    connected: true, on() {},
    write: async (cmd) => {
      if (cmd.includes('/routing/bgp/session')) return [];
      if (cmd.includes('/routing/bgp/peer'))    return [{ name: 'legacy', 'remote-address': '10.1.0.1', 'remote-as': '65002', state: 'established', 'prefix-count': '50' }];
      return [];
    },
    stream: (w, cb) => ({ stop() {} }),
  };
  const collector = new RoutingCollector({ ros, io: { emit(ev, d) { emitted.push(d); } }, pollMs: 10000, state: {} });
  await collector.start();
  const d = emitted[emitted.length - 1];
  assert.equal(d.peers.length, 1);
  assert.equal(d.peers[0].remoteAs, 65002);
});

test('routing collector sets pollMs=0 to signal stream-based delivery', async () => {
  const emitted = [];
  const collector = new RoutingCollector({ ros: makeRoutingRos(), io: { emit(ev, d) { emitted.push(d); } }, pollMs: 15000, state: {} });
  await collector.start();
  assert.equal(emitted[0].pollMs, 0);
});

// ── Wireless: proplist removal fixes single-client bug ───────────────────────

test('wireless collector returns all clients without =.proplist= restriction', async () => {
  const WirelessCollector = require('../src/collectors/wireless');
  const emitted = [];
  const ros = {
    connected: true, cfg: {}, on() {},
    write: async (cmd) => {
      if (cmd.includes('/interface/wifi/')) return [
        { 'mac-address': 'AA:01', 'signal-strength': '-55', interface: 'wifi1', band: '5ghz', uptime: '1h' },
        { 'mac-address': 'AA:02', 'signal-strength': '-65', interface: 'wifi1', band: '5ghz', uptime: '30m' },
        { 'mac-address': 'AA:03', 'signal-strength': '-70', interface: 'wifi2', band: '2.4ghz', uptime: '15m' },
      ];
      return [];
    },
  };
  const collector = new WirelessCollector({
    ros, io: { engine: { clientsCount: 1 }, emit(ev, d) { emitted.push({ ev, data: d }); } }, pollMs: 5000, state: {},
    dhcpLeases: { getNameByMAC: () => null },
    arp: { getByMAC: () => null },
  });
  await collector.tick();
  assert.equal(emitted[0].data.clients.length, 3, 'all 3 clients present');
  assert.equal(collector.mode, 'wifi');
});

test('wireless collector write call contains no =.proplist= parameter', async () => {
  const WirelessCollector = require('../src/collectors/wireless');
  const writeCalls = [];
  const ros = {
    connected: true, cfg: {}, on() {},
    write: async (cmd, params) => { writeCalls.push({ cmd, params: params || [] }); return []; },
  };
  const collector = new WirelessCollector({
    ros, io: { engine: { clientsCount: 1 }, emit() {} }, pollMs: 5000, state: {},
    dhcpLeases: { getNameByMAC: () => null },
    arp: { getByMAC: () => null },
  });
  await collector.tick();
  for (const call of writeCalls) {
    const hasProplst = call.params.some(p => String(p).includes('.proplist'));
    assert.ok(!hasProplst, `no .proplist in write params for ${call.cmd}`);
  }
});

test('wireless collector resolves name on second tick when DHCP loads after first tick', async () => {
  // Regression: names are empty on the first tick because DHCP hasn't loaded yet.
  // The collector schedules a 500ms retry that re-resolves names from the already-
  // fetched client list WITHOUT making a second RouterOS API call (some firmware
  // builds return only partial results when queried soon after startup).
  const emitted = [];
  let writeCalls = 0;
  let leasesReady = false;
  const ros = {
    connected: true, cfg: {}, on() {},
    write: async () => {
      writeCalls++;
      return [
        { 'mac-address': 'AA:BB', signal: '-50', interface: 'wifi1' },
        { 'mac-address': 'CC:DD', signal: '-60', interface: 'wifi1' },
      ];
    },
  };
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); } };
  const dhcpLeases = {
    getNameByMAC: (mac) => {
      if (!leasesReady) return null;
      if (mac === 'AA:BB') return { name: 'Laptop' };
      if (mac === 'CC:DD') return { name: 'Phone' };
      return null;
    },
  };
  const collector = new WirelessCollector({
    ros, io, pollMs: 30000, state: {},
    dhcpLeases,
    arp: { getByMAC: () => null },
  });

  // First tick — DHCP not ready, both clients have empty names
  await collector.tick();
  const writeCallsAfterTick1 = writeCalls;
  assert.equal(emitted.length, 1, 'first tick emits');
  assert.equal(emitted[0].data.clients.length, 2, 'all clients present on first emit');
  assert.equal(emitted[0].data.clients[0].name, '', 'names empty before DHCP loads');
  assert.ok(collector._retryTimer, 'retry timer scheduled');

  // DHCP now available — retry fires within 500ms without a new API call
  leasesReady = true;
  await new Promise(r => setTimeout(r, 600));

  assert.equal(writeCalls, writeCallsAfterTick1, 'retry must not make a second RouterOS API call');
  assert.equal(emitted.length, 2, 'retry emits updated names');
  assert.equal(emitted[1].data.clients.length, 2, 'all clients still present after retry');
  assert.equal(emitted[1].data.clients[0].name, 'Laptop', 'first client name resolved');
  assert.equal(emitted[1].data.clients[1].name, 'Phone', 'second client name resolved');
  assert.equal(collector._retryTimer, null, 'retry stops once all names resolved');
});

// ═══════════════════════════════════════════════════════════════════════════
// --- Routing Collector ---
// ═══════════════════════════════════════════════════════════════════════════
function makeRoutingRos({ printRows = [], sessionRows = [], peerCfgRows = [] } = {}) {
  return {
    connected: true,
    on() {},
    write: async (cmd) => {
      if (cmd.includes('/routing/bgp/session')) return sessionRows;
      if (cmd.includes('/routing/bgp/peer'))    return peerCfgRows;
      if (cmd.includes('/ip/route'))            return printRows;
      return [];
    },
    stream: (words, cb) => ({ stop() {} }),
  };
}

// ── Initial load happy path ───────────────────────────────────────────────────

test('routing collector start() emits correct payload with routes and BGP sessions', async () => {
  const emitted = [];
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const state = {};
  const ros = makeRoutingRos({
    printRows: [
      { '.id': '*1', 'dst-address': '0.0.0.0/0',     gateway: '10.0.0.1', distance: '1',  '.flags': 'AS' },
      { '.id': '*2', 'dst-address': '192.168.1.0/24', gateway: 'bridge',   distance: '0',  '.flags': 'AC' },
    ],
    sessionRows: [{
      name: 'peer1', 'remote.address': '10.0.0.1', 'remote.as': '65001',
      state: 'established', uptime: '1h', 'prefix-count': '100',
      'updates-sent': '10', 'updates-received': '20',
    }],
    peerCfgRows: [{ 'remote.address': '10.0.0.1', comment: 'Transit A' }],
  });

  const collector = new RoutingCollector({ ros, io, pollMs: 10000, state });
  await collector.start();

  const d = emitted[emitted.length - 1].data;
  assert.equal(d.peers.length, 1);
  assert.equal(d.peers[0].state, 'established');
  assert.equal(d.peers[0].prefixes, 100);
  assert.equal(d.peers[0].description, 'Transit A');
  assert.equal(d.routes.length, 1);
  assert.equal(d.routes[0].dst, '0.0.0.0/0');
  assert.equal(d.routes[0].type, 'static');
  assert.equal(d.routeCounts.total, 2);
  assert.equal(d.routeCounts.static, 1);
  assert.equal(d.routeCounts.connect, 1);
  assert.equal(d.summary.established, 1);
  assert.equal(d.pollMs, 0, 'pollMs must be 0 for streamed collector');
  assert.ok(state.lastRoutingTs > 0);
  assert.equal(state.lastRoutingErr, null);
});

// ── Route stream delta: add, update, delete ───────────────────────────────────

test('routing collector route stream delta adds new route', async () => {
  const emitted = [];
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new RoutingCollector({ ros: makeRoutingRos(), io, pollMs: 10000, state: {} });
  await collector._loadRoutes();

  collector._applyRouteDelta({ '.id': '*5', 'dst-address': '10.0.0.0/8', gateway: '1.2.3.1', distance: '1', '.flags': 'AS' });
  collector._emit(null);

  assert.equal(emitted[emitted.length - 1].data.routes.length, 1);
  assert.equal(emitted[emitted.length - 1].data.routes[0].dst, '10.0.0.0/8');
});

test('routing collector route stream delta updates existing route in place', async () => {
  const emitted = [];
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const ros = makeRoutingRos({
    printRows: [{ '.id': '*1', 'dst-address': '0.0.0.0/0', gateway: '1.2.3.1', distance: '1', '.flags': 'AS' }],
  });
  const collector = new RoutingCollector({ ros, io, pollMs: 10000, state: {} });
  await collector._loadRoutes();

  collector._applyRouteDelta({ '.id': '*1', gateway: '9.9.9.9', distance: '1', '.flags': 'AS' });
  collector._emit(null);

  assert.equal(emitted[emitted.length - 1].data.routes[0].gateway, '9.9.9.9');
});

test('routing collector route stream delta removes route on .dead=true', async () => {
  const emitted = [];
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const ros = makeRoutingRos({
    printRows: [
      { '.id': '*1', 'dst-address': '0.0.0.0/0',  gateway: '1.2.3.1', distance: '1', '.flags': 'AS' },
      { '.id': '*2', 'dst-address': '10.0.0.0/8', gateway: '1.2.3.1', distance: '1', '.flags': 'AS' },
    ],
  });
  const collector = new RoutingCollector({ ros, io, pollMs: 10000, state: {} });
  await collector._loadRoutes();

  collector._applyRouteDelta({ '.id': '*1', '.dead': 'true' });
  collector._emit(null);

  assert.equal(collector._routes.size, 1);
  assert.equal(emitted[emitted.length - 1].data.routes[0].dst, '10.0.0.0/8');
});

test('routing collector route stream delta merges partial row with stored raw', async () => {
  const emitted = [];
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const ros = makeRoutingRos({
    printRows: [{ '.id': '*1', 'dst-address': '0.0.0.0/0', gateway: '1.2.3.1', distance: '1', '.flags': 'AS', comment: 'original' }],
  });
  const collector = new RoutingCollector({ ros, io, pollMs: 10000, state: {} });
  await collector._loadRoutes();

  collector._applyRouteDelta({ '.id': '*1', distance: '5' });
  collector._emit(null);

  const r = emitted[emitted.length - 1].data.routes[0];
  assert.equal(r.distance, 5,          'distance updated');
  assert.equal(r.gateway, '1.2.3.1',   'gateway preserved');
  assert.equal(r.comment, 'original',  'comment preserved');
});

// ── BGP session stream ────────────────────────────────────────────────────────

test('routing collector BGP session stream state change triggers emit', async () => {
  const emitted = [];
  let bgpCb;
  const ros = {
    connected: true, on() {},
    write: async () => [],
    stream: (words, cb) => {
      if (words[0].includes('bgp')) bgpCb = cb;
      return { stop() {} };
    },
  };
  const collector = new RoutingCollector({ ros, io: { emit(ev, d) { emitted.push(d); } }, pollMs: 10000, state: {} });
  await collector.start();
  const countBefore = emitted.length;

  bgpCb(null, { name: 'p1', 'remote.address': '10.0.0.1', 'remote.as': '65001', state: 'established', 'prefix-count': '50' });
  await new Promise(r => setTimeout(r, 20));

  assert.ok(emitted.length > countBefore, 'state change triggers emit');
  assert.equal(emitted[emitted.length - 1].peers.length, 1);
  assert.equal(emitted[emitted.length - 1].peers[0].state, 'established');
});

test('routing collector BGP session stream keepalive-only update suppressed', async () => {
  const emitted = [];
  let bgpCb;
  const ros = {
    connected: true, on() {},
    write: async () => [],
    stream: (words, cb) => {
      if (words[0].includes('bgp')) bgpCb = cb;
      return { stop() {} };
    },
  };
  const collector = new RoutingCollector({ ros, io: { emit(ev, d) { emitted.push(d); } }, pollMs: 10000, state: {} });
  await collector.start();

  // First event — establishes fingerprint
  bgpCb(null, { name: 'p1', 'remote.address': '10.0.0.1', state: 'established', 'prefix-count': '50', uptime: '1h' });
  await new Promise(r => setTimeout(r, 20));
  const countAfterFirst = emitted.length;

  // Keepalive — only uptime changed, state/prefixes/error identical
  bgpCb(null, { name: 'p1', 'remote.address': '10.0.0.1', state: 'established', 'prefix-count': '50', uptime: '1h30m' });
  await new Promise(r => setTimeout(r, 20));

  assert.equal(emitted.length, countAfterFirst, 'keepalive-only update must be suppressed');
});

test('routing collector BGP session stream prefix change triggers emit', async () => {
  const emitted = [];
  let bgpCb;
  const ros = {
    connected: true, on() {},
    write: async () => [],
    stream: (words, cb) => {
      if (words[0].includes('bgp')) bgpCb = cb;
      return { stop() {} };
    },
  };
  const collector = new RoutingCollector({ ros, io: { emit(ev, d) { emitted.push(d); } }, pollMs: 10000, state: {} });
  await collector.start();

  bgpCb(null, { name: 'p1', 'remote.address': '10.0.0.1', state: 'established', 'prefix-count': '50' });
  await new Promise(r => setTimeout(r, 20));
  const countAfterFirst = emitted.length;

  // Prefix count changed — must trigger emit
  bgpCb(null, { name: 'p1', 'remote.address': '10.0.0.1', state: 'established', 'prefix-count': '75' });
  await new Promise(r => setTimeout(r, 20));

  assert.ok(emitted.length > countAfterFirst, 'prefix count change triggers emit');
  assert.equal(emitted[emitted.length - 1].peers[0].prefixes, 75);
});

test('routing collector BGP session stream delete removes peer', async () => {
  const emitted = [];
  let bgpCb;
  const ros = {
    connected: true, on() {},
    write: async () => [],
    stream: (words, cb) => {
      if (words[0].includes('bgp')) bgpCb = cb;
      return { stop() {} };
    },
  };
  const collector = new RoutingCollector({ ros, io: { emit(ev, d) { emitted.push(d); } }, pollMs: 10000, state: {} });
  await collector.start();

  bgpCb(null, { name: 'p1', 'remote.address': '10.0.0.1', state: 'established', 'prefix-count': '50' });
  await new Promise(r => setTimeout(r, 20));
  assert.equal(collector._sessions.size, 1);

  bgpCb(null, { name: 'p1', 'remote.address': '10.0.0.1', '.dead': 'true' });
  await new Promise(r => setTimeout(r, 20));
  assert.equal(collector._sessions.size, 0);
});

// ── _emit(null) reuses last peers ─────────────────────────────────────────────

test('routing collector _emit(null) reuses last known peers from lastPayload', async () => {
  const emitted = [];
  const ros = makeRoutingRos({
    sessionRows: [{ name: 'p1', 'remote.address': '10.0.0.1', 'remote.as': '65001', state: 'established', 'prefix-count': '50' }],
  });
  const collector = new RoutingCollector({ ros, io: { emit(ev, d) { emitted.push(d); } }, pollMs: 10000, state: {} });
  await collector.start();

  collector._applyRouteDelta({ '.id': '*1', 'dst-address': '1.0.0.0/8', gateway: '10.0.0.1', distance: '1', '.flags': 'AS' });
  collector._emit(null);

  const d = emitted[emitted.length - 1];
  assert.equal(d.peers.length, 1, 'last known peers reused on route stream event');
});

// ── BGP stream unavailable — graceful fallback ────────────────────────────────

test('routing collector falls back gracefully when BGP session stream unavailable', async () => {
  const ros = {
    connected: true, on() {},
    write: async () => [],
    stream: (words, cb) => {
      if (words[0].includes('bgp')) throw new Error('no such command');
      return { stop() {} };
    },
  };
  const collector = new RoutingCollector({ ros, io: { emit() {} }, pollMs: 10000, state: {} });
  await collector.start(); // must not throw
  assert.equal(collector._bgpStream, null, 'bgpStream null when endpoint unavailable');
  assert.ok(collector._routeStream !== null, 'route stream still active');
});

// ── Legacy ROS v6 fallback on initial load ────────────────────────────────────

test('routing collector falls back to bgp/peer/print when session/print returns empty', async () => {
  const emitted = [];
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const ros = {
    connected: true, on() {},
    write: async (cmd) => {
      if (cmd.includes('/routing/bgp/session')) return [];
      if (cmd.includes('/routing/bgp/peer'))    return [{ name: 'legacy', 'remote-address': '10.1.0.1', 'remote-as': '65002', state: 'established', 'prefix-count': '50' }];
      return [];
    },
    stream: (words, cb) => ({ stop() {} }),
  };
  const collector = new RoutingCollector({ ros, io, pollMs: 10000, state: {} });
  await collector.start();

  const d = emitted[emitted.length - 1].data;
  assert.equal(d.peers.length, 1);
  assert.equal(d.peers[0].remoteAs, 65002);
});

// ── Flag inference: active routes with no .flags ──────────────────────────────

test('routing collector keeps active routes when RouterOS omits .flags', async () => {
  const emitted = [];
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const ros = makeRoutingRos({
    printRows: [
      { '.id': '*1', 'dst-address': '0.0.0.0/0',      gateway: '192.168.88.1', distance: '1' },
      { '.id': '*2', 'dst-address': '172.16.0.0/12',   gateway: '10.0.0.1',    distance: '1', '.flags': 'Xs' },
      { '.id': '*3', 'dst-address': '192.168.88.0/24', gateway: 'bridge',       distance: '0' },
    ],
  });
  const collector = new RoutingCollector({ ros, io, pollMs: 10000, state: {} });
  await collector._loadRoutes();
  collector._emit(null);

  const dsts = emitted[emitted.length - 1].data.routes.map(r => r.dst);
  assert.ok(dsts.includes('0.0.0.0/0'),     'active static route kept via IP-gateway inference');
  assert.ok(dsts.includes('172.16.0.0/12'), 'disabled static route kept');
  assert.ok(!dsts.includes('192.168.88.0/24'), 'interface-name gateway excluded');
});

test('routing collector excludes interface-name-gateway routes from inference', async () => {
  const emitted = [];
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const ros = makeRoutingRos({
    printRows: [
      { '.id': '*1', 'dst-address': '192.168.1.0/24', gateway: 'bridge', distance: '0' },
      { '.id': '*2', 'dst-address': '10.0.0.0/24',    gateway: 'ether1', distance: '0' },
      { '.id': '*3', 'dst-address': '0.0.0.0/0',      gateway: '192.168.1.1', distance: '1' },
    ],
  });
  const collector = new RoutingCollector({ ros, io, pollMs: 10000, state: {} });
  await collector._loadRoutes();
  collector._emit(null);

  assert.equal(emitted[emitted.length - 1].data.routes.length, 1);
  assert.equal(emitted[emitted.length - 1].data.routes[0].dst, '0.0.0.0/0');
});

// ── Empty / malformed data ────────────────────────────────────────────────────

test('routing collector emits empty payload without crash when router has no data', async () => {
  const emitted = [];
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const state = {};
  const collector = new RoutingCollector({ ros: makeRoutingRos(), io, pollMs: 10000, state });
  await collector.start();

  const d = emitted[emitted.length - 1].data;
  assert.deepEqual(d.peers, []);
  assert.deepEqual(d.routes, []);
  assert.equal(d.routeCounts.total, 0);
  assert.ok(state.lastRoutingTs > 0);
  assert.equal(state.lastRoutingErr, null);
});

test('routing collector clamps malformed numeric fields to 0', async () => {
  const emitted = [];
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const ros = makeRoutingRos({
    printRows:   [{ '.id': '*1', 'dst-address': '1.2.3.0/24', gateway: '1.2.3.1', distance: 'bad', '.flags': 'AS' }],
    sessionRows: [{ name: 'bad', 'remote.address': '10.0.0.1', 'remote.as': 'notanumber', state: 'established', 'prefix-count': 'bad', 'updates-sent': null }],
  });
  const collector = new RoutingCollector({ ros, io, pollMs: 10000, state: {} });
  await collector.start();

  const d = emitted[emitted.length - 1].data;
  assert.equal(d.routes[0].distance,    0);
  assert.equal(d.peers[0].remoteAs,     0);
  assert.equal(d.peers[0].prefixes,     0);
  assert.equal(d.peers[0].updatesSent,  0);
});

// ── Route counts ──────────────────────────────────────────────────────────────

test('routing collector counts route types correctly from mixed routes', async () => {
  const emitted = [];
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const ros = makeRoutingRos({
    printRows: [
      { '.id': '*1', 'dst-address': '0.0.0.0/0',     gateway: '1.2.3.1', distance: '1',   '.flags': 'AS'  },
      { '.id': '*2', 'dst-address': '10.0.0.0/8',    gateway: '1.2.3.1', distance: '20',  '.flags': 'Ab'  },
      { '.id': '*3', 'dst-address': '172.16.0.0/12', gateway: '10.0.0.1', distance: '20', '.flags': 'Ab'  },
      { '.id': '*4', 'dst-address': '192.168.0.0/24', gateway: 'bridge', distance: '0',   '.flags': 'AC'  },
      { '.id': '*5', 'dst-address': '192.168.2.0/24', gateway: '10.1.0.1', distance: '110','.flags': 'Ao' },
    ],
  });
  const collector = new RoutingCollector({ ros, io, pollMs: 10000, state: {} });
  await collector._loadRoutes();
  collector._emit(null);

  const c = emitted[emitted.length - 1].data.routeCounts;
  assert.equal(c.total, 5);
  assert.equal(c.static, 1);
  assert.equal(c.bgp, 2);
  assert.equal(c.connect, 1);
  assert.equal(c.ospf, 1);
});

// ── Helpers ───────────────────────────────────────────────────────────────────

test('routing collector parses RouterOS uptime formats correctly', () => {
  const c = new RoutingCollector({ ros: { on() {} }, io: { emit() {} }, pollMs: 10000, state: {} });
  assert.equal(c._parseUptime('1d2h3m4s'), 86400 + 7200 + 180 + 4);
  assert.equal(c._parseUptime('12:34:56'), 12 * 3600 + 34 * 60 + 56);
  assert.equal(c._parseUptime('30m'),  1800);
  assert.equal(c._parseUptime(''),     0);
  assert.equal(c._parseUptime(null),   0);
});

test('routing collector classifies peers by ASN and description', () => {
  const c = new RoutingCollector({ ros: { on() {} }, io: { emit() {} }, pollMs: 10000, state: {} });
  assert.equal(c._classifyPeer(65001, '', ''),          'private');
  assert.equal(c._classifyPeer(4200000001, '', ''),     'private');
  assert.equal(c._classifyPeer(13335, 'ix peering', ''), 'ix');
  assert.equal(c._classifyPeer(1299, 'transit', ''),    'upstream');
});

test('routing collector normalises BGP state strings consistently', async () => {
  const emitted = [];
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const ros = makeRoutingRos({
    sessionRows: [
      { name: 'a', 'remote.address': '10.0.0.1', state: 'Established' },
      { name: 'b', 'remote.address': '10.0.0.2', state: 'Active' },
      { name: 'c', 'remote.address': '10.0.0.3', state: '', established: 'true' },
      { name: 'd', 'remote.address': '10.0.0.4', state: 'idle' },
    ],
  });
  const collector = new RoutingCollector({ ros, io, pollMs: 10000, state: {} });
  await collector.start();

  const states = emitted[emitted.length - 1].data.peers.map(p => p.state);
  assert.equal(states[0], 'established');
  assert.equal(states[1], 'active');
  assert.equal(states[2], 'established');
  assert.equal(states[3], 'idle');
});

test('routing collector accumulates prefix history across BGP stream events', async () => {
  const emitted = [];
  let bgpCb;
  const ros = {
    connected: true, on() {},
    write: async () => [],
    stream: (words, cb) => {
      if (words[0].includes('bgp')) bgpCb = cb;
      return { stop() {} };
    },
  };
  const collector = new RoutingCollector({ ros, io: { emit(ev, d) { emitted.push(d); } }, pollMs: 10000, state: {} });
  await collector.start();

  bgpCb(null, { name: 'p1', 'remote.address': '10.0.0.1', state: 'established', 'prefix-count': '100' });
  await new Promise(r => setTimeout(r, 20));
  bgpCb(null, { name: 'p1', 'remote.address': '10.0.0.1', state: 'established', 'prefix-count': '110' });
  await new Promise(r => setTimeout(r, 20));
  bgpCb(null, { name: 'p1', 'remote.address': '10.0.0.1', state: 'established', 'prefix-count': '120' });
  await new Promise(r => setTimeout(r, 20));

  const history = emitted[emitted.length - 1].peers[0].prefixHistory;
  assert.deepEqual(history, [100, 110, 120]);
});

test('routing collector sets pollMs=0 in payload to signal stream-based delivery', async () => {
  const emitted = [];
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new RoutingCollector({ ros: makeRoutingRos(), io, pollMs: 15000, state: {} });
  await collector._loadRoutes();
  collector._emit(null);
  assert.equal(emitted[emitted.length - 1].data.pollMs, 0);
});
