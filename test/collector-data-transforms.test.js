const test = require('node:test');
const assert = require('node:assert/strict');

const { parseBps, bpsToMbps } = require('../src/collectors/traffic');

test('parseBps handles raw integer strings from RouterOS binary API', () => {
  assert.equal(parseBps('27800'), 27800);
  assert.equal(parseBps('1500000'), 1500000);
  assert.equal(parseBps('0'), 0);
});

test('parseBps handles kbps/Mbps/Gbps suffixed values', () => {
  assert.equal(parseBps('27.8kbps'), 27800);
  assert.equal(parseBps('27.8Kbps'), 27800);
  assert.equal(parseBps('1.5Mbps'), 1500000);
  assert.equal(parseBps('1.5mbps'), 1500000);
  assert.equal(parseBps('2.1Gbps'), 2100000000);
  assert.equal(parseBps('2.1gbps'), 2100000000);
});

test('parseBps handles plain bps suffix and edge cases', () => {
  assert.equal(parseBps('500bps'), 500);
  assert.equal(parseBps(undefined), 0);
  assert.equal(parseBps(null), 0);
  assert.equal(parseBps(''), 0);
});

test('bpsToMbps converts and rounds to 3 decimal places', () => {
  assert.equal(bpsToMbps(27800), 0.028);
  assert.equal(bpsToMbps(1500000), 1.5);
  assert.equal(bpsToMbps(0), 0);
  assert.equal(bpsToMbps(undefined), 0);
  assert.equal(bpsToMbps(null), 0);
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
