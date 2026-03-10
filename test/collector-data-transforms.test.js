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
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
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
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
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
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
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
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
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
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
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
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
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
    { cc: 'ZZ', city: 'Lab City', count: 2, proto: { tcp: 2, udp: 0, other: 0 } },
    { cc: 'YY', city: 'Edge Town', count: 1, proto: { tcp: 0, udp: 1, other: 0 } },
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
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
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
  let tickNum = 0;
  const ros = {
    connected: true,
    on() {},
    write: async (cmd) => {
      if (cmd.includes('filter')) return tickNum === 0
        ? [{ '.id': '*1', chain: 'forward', action: 'accept', packets: '100', bytes: '50000', disabled: 'false' }]
        : [{ '.id': '*1', chain: 'forward', action: 'accept', packets: '150', bytes: '75000', disabled: 'false' }];
      return []; // nat, mangle empty
    },
  };
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new FirewallCollector({ ros, io, pollMs: 10000, state: {}, topN: 10 });

  await collector.tick();
  assert.equal(emitted[0].data.filter[0].deltaPackets, 0); // no previous
  tickNum++;

  await collector.tick();
  assert.equal(emitted[1].data.filter[0].deltaPackets, 50); // 150 - 100
});

test('firewall collector clamps negative delta to zero on counter reset', async () => {
  const emitted = [];
  let tickNum = 0;
  const ros = {
    connected: true,
    on() {},
    write: async (cmd) => {
      if (cmd.includes('filter')) return tickNum === 0
        ? [{ '.id': '*1', chain: 'forward', action: 'accept', packets: '1000', bytes: '50000', disabled: 'false' }]
        : [{ '.id': '*1', chain: 'forward', action: 'accept', packets: '10', bytes: '500', disabled: 'false' }];
      return [];
    },
  };
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new FirewallCollector({ ros, io, pollMs: 10000, state: {}, topN: 10 });

  await collector.tick();
  tickNum++;
  await collector.tick();

  assert.equal(emitted[1].data.filter[0].deltaPackets, 0);
});

test('firewall collector filters out disabled rules', async () => {
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    write: async (cmd) => {
      if (cmd.includes('filter')) return [
        { '.id': '*1', chain: 'forward', action: 'accept', packets: '100', disabled: 'true' },
        { '.id': '*2', chain: 'forward', action: 'drop', packets: '50', disabled: 'false' },
        { '.id': '*3', chain: 'forward', action: 'log', packets: '25', disabled: true },
      ];
      return [];
    },
  };
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new FirewallCollector({ ros, io, pollMs: 10000, state: {}, topN: 10 });
  await collector.tick();

  assert.equal(emitted[0].data.filter.length, 1);
  assert.equal(emitted[0].data.filter[0].id, '*2');
});

test('firewall collector prunes stale entries from prevCounts', async () => {
  const emitted = [];
  let tickNum = 0;
  const ros = {
    connected: true,
    on() {},
    write: async (cmd) => {
      if (cmd.includes('filter')) return tickNum === 0
        ? [{ '.id': '*1', packets: '100', disabled: 'false' }, { '.id': '*2', packets: '200', disabled: 'false' }]
        : [{ '.id': '*2', packets: '250', disabled: 'false' }];
      return [];
    },
  };
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new FirewallCollector({ ros, io, pollMs: 10000, state: {}, topN: 10 });

  await collector.tick();
  assert.ok(collector.prevCounts.has('*1'));
  assert.ok(collector.prevCounts.has('*2'));
  tickNum++;

  await collector.tick();
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
  assert.equal(emitted[0].data.loss, 33);
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

  assert.equal(collector.history.length, 60);
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
  const io = { emit(ev, data) { emitted.push({ ev, data }); callNum++; } };
  const collector = new TopTalkersCollector({ ros, io, pollMs: 3000, state: {}, topN: 5 });

  await collector.tick();
  assert.equal(emitted[0].data.devices[0].tx_mbps, 0);
  assert.equal(emitted[0].data.devices[0].rx_mbps, 0);

  // Simulate time passing
  const prev = collector.prev.get('AA:BB:CC:DD:EE:FF');
  prev.ts = Date.now() - 1000; // 1 second ago
  prev.up = 0;
  prev.down = 0;

  await collector.tick();
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
  const io = { emit(ev, data) { emitted.push({ ev, data }); callNum++; } };
  const collector = new TopTalkersCollector({ ros, io, pollMs: 3000, state: {}, topN: 5 });

  await collector.tick();
  await collector.tick();

  assert.equal(emitted[1].data.devices[0].tx_mbps, 0);
  assert.equal(emitted[1].data.devices[0].rx_mbps, 0);
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
  const io = { emit() {} };
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
    write: async () => [
      { 'public-key': 'AAAA', name: 'myphone', comment: 'backup', 'allowed-address': '10.0.0.2/32', 'last-handshake': '1m30s', 'rx-bytes': '0', 'tx-bytes': '0' },
      { 'public-key': 'BBBB', name: '', comment: 'server', 'allowed-address': '10.0.0.3/32', 'last-handshake': 'never', 'rx-bytes': '0', 'tx-bytes': '0' },
      { 'public-key': 'CCCC', name: '', comment: '', 'allowed-address': '10.0.0.4/32', 'last-handshake': '', 'rx-bytes': '0', 'tx-bytes': '0' },
      { 'public-key': 'DDDDEEEEFFFFGGGG1234567890', name: '', comment: '', 'allowed-address': '', 'last-handshake': '5s', 'rx-bytes': '0', 'tx-bytes': '0' },
      { 'last-handshake': '10s', 'rx-bytes': '0', 'tx-bytes': '0' },
    ],
  };
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new VpnCollector({ ros, io, pollMs: 10000, state: {} });
  await collector.tick();

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
    write: async () => [
      { 'public-key': 'A', 'last-handshake': '30s', 'rx-bytes': '0', 'tx-bytes': '0' },
      { 'public-key': 'B', 'last-handshake': 'never', 'rx-bytes': '0', 'tx-bytes': '0' },
      { 'public-key': 'C', 'last-handshake': '', 'rx-bytes': '0', 'tx-bytes': '0' },
    ],
  };
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new VpnCollector({ ros, io, pollMs: 10000, state: {} });
  await collector.tick();

  const t = emitted[0].data.tunnels;
  assert.equal(t[0].state, 'connected');
  assert.equal(t[1].state, 'idle');
  assert.equal(t[2].state, 'idle');
});

test('vpn collector calculates rates between polls and prunes stale peers', async () => {
  const emitted = [];
  let callNum = 0;
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
    write: async () => responses[callNum++],
  };
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new VpnCollector({ ros, io, pollMs: 10000, state: {} });

  await collector.tick();
  const prev = collector._prev.get('A');
  prev.ts = Date.now() - 1000;
  prev.rx = 1000;
  prev.tx = 2000;

  await collector.tick();

  assert.equal(emitted[1].data.tunnels.length, 1);
  assert.equal(emitted[1].data.tunnels[0].name, 'phone');
  assert.equal(emitted[1].data.tunnels[0].rxRate, 2000);
  assert.equal(emitted[1].data.tunnels[0].txRate, 3000);
  assert.ok(!collector._prev.has('B'), 'stale peer should be pruned from previous counters');
});

// --- Wireless Collector ---
const WirelessCollector = require('../src/collectors/wireless');

test('wireless collector detects band from interface name and tx-rate', async () => {
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    cfg: {},
    write: async () => [
      { 'mac-address': 'AA:BB', interface: 'wifi1', 'tx-rate': '', signal: '-50' },
      { 'mac-address': 'CC:DD', interface: 'wifi3', 'tx-rate': '', signal: '-60' },
      { 'mac-address': 'EE:FF', interface: 'wlan0', 'tx-rate': '54Mbps', signal: '-70' },
      { 'mac-address': '11:22', interface: 'wlan0', 'tx-rate': 'HE-MCS 11 80MHz', signal: '-55' },
    ],
  };
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
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
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new WirelessCollector({
    ros, io, pollMs: 5000, state: {},
    dhcpLeases: { getNameByMAC: () => null },
    arp: { getByMAC: () => null },
  });
  await collector.tick();

  const macs = emitted[0].data.clients.map(c => c.mac);
  assert.deepEqual(macs, ['CC:DD', 'EE:FF', 'AA:BB']);
});

test('wireless collector enriches payloads with DHCP names, ARP IPs, and emits empty refreshes', async () => {
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
  const io = { emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new WirelessCollector({
    ros, io, pollMs: 5000, state: {},
    dhcpLeases: { getNameByMAC: (mac) => mac === 'AA:BB' ? { name: 'Laptop' } : null },
    arp: { getByMAC: (mac) => mac === 'AA:BB' ? { ip: '192.168.1.20' } : null },
  });

  await collector.tick();
  await collector.tick();

  assert.equal(emitted[0].data.clients[0].name, 'Laptop');
  assert.equal(emitted[0].data.clients[0].ip, '192.168.1.20');
  assert.equal(emitted[0].data.clients[0].ssid, 'Office');
  assert.equal(emitted[0].data.mode, 'wifi');
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

test('dhcp leases collector resolves name with comment > hostname > empty fallback', () => {
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

  return collector.start().then(() => {
    streamHandler(null, { address: '192.168.1.12', 'mac-address': 'EE:FF', comment: '   ', 'host-name': '  ' });

  assert.equal(collector.getNameByIP('192.168.1.10').name, 'MyLaptop');
  assert.equal(collector.getNameByIP('192.168.1.11').name, 'phone');
  assert.equal(collector.getNameByIP('192.168.1.12').name, '');
  });
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
  await collector.tick();

  const ifaces = emitted[0].data.interfaces;
  assert.equal(ifaces[0].running, true);
  assert.equal(ifaces[0].disabled, false);
  assert.equal(ifaces[0].rxMbps, 15);
  assert.equal(ifaces[0].txMbps, 8.5);
  assert.deepEqual(ifaces[0].ips, ['192.168.1.1/24', '10.0.0.1/24']);
  assert.equal(ifaces[1].running, true);
  assert.equal(ifaces[1].rxMbps, 0);
});

// --- ARP Collector ---
const ArpCollector = require('../src/collectors/arp');

test('arp collector builds bidirectional lookup maps and skips incomplete entries', async () => {
  const ros = {
    connected: true,
    on() {},
    write: async () => [
      { address: '192.168.1.10', 'mac-address': 'AA:BB:CC:DD:EE:FF', interface: 'bridge' },
      { address: '192.168.1.11' },
      { 'mac-address': 'CC:DD:EE:FF:00:11' },
      { address: '192.168.1.12', 'mac-address': '11:22:33:44:55:66' },
    ],
  };
  const collector = new ArpCollector({ ros, pollMs: 30000, state: {} });
  await collector.tick();

  const byIp = collector.getByIP('192.168.1.10');
  assert.equal(byIp.mac, 'AA:BB:CC:DD:EE:FF');
  assert.equal(byIp.iface, 'bridge');

  const byMac = collector.getByMAC('AA:BB:CC:DD:EE:FF');
  assert.equal(byMac.ip, '192.168.1.10');

  assert.equal(collector.getByIP('192.168.1.11'), undefined);
  assert.equal(collector.getByMAC('CC:DD:EE:FF:00:11'), null);
  assert.equal(collector.getByIP('192.168.1.12').mac, '11:22:33:44:55:66');
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
    getActiveLeaseIPs: () => ['192.168.1.10', '192.168.1.11', '10.0.0.5'],
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
  const collector = new DhcpNetworksCollector({ ros, io, pollMs: 15000, dhcpLeases: { getActiveLeaseIPs: () => [] }, state: {}, wanIface: 'WAN1' });
  await collector.tick();

  assert.equal(emitted[0].data.networks.length, 0);
  assert.equal(emitted[0].data.wanIp, '1.2.3.4/30');
});
