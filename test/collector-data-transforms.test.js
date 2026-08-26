const test = require('node:test');
const assert = require('node:assert/strict');

// Stops every collector these tests construct once the file finishes; without
// it their timers keep the test process alive. See the helper for why that
// made the reported test count unstable.
const { track } = require('./helpers/collector-cleanup');
const TrafficCollector = track(require('../src/collectors/traffic'));

test('traffic collector emits normalized socket and WAN payloads from a poll cycle', () => {
  const socketEmits = [];
  const broadcastEmits = [];
  const state = {};
  const ros = { connected: true, on() {} };
  const fakeSocket = { emit(ev, data) { socketEmits.push({ ev, data }); } };
  const io = {
    engine: { clientsCount: 1 },
    emit(ev, data) { broadcastEmits.push({ ev, data }); },
  };
  const collector = new TrafficCollector({ ros, io, defaultIf: 'wan', historyMinutes: 1, state });
  collector.subscriptions.set('socket-1', { ifName: 'wan', socket: fakeSocket });

  collector._processPacket('wan', {
    'rx-bits-per-second': '27.8kbps',
    'tx-bits-per-second': '1.5Mbps',
    running: 'true',
    disabled: 'false',
  });

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

test('traffic collector calls onSample before idle gate', () => {
  const samples = [];
  const ros = { connected: true, on() {} };
  const io  = { engine: { clientsCount: 0 }, emit() {} }; // idle — no connected clients
  const collector = new TrafficCollector({
    ros, io, defaultIf: 'wan', historyMinutes: 1, state: {},
    onSample: (ifName, rxMbps, txMbps, ts) => samples.push({ ifName, rxMbps, txMbps, ts }),
  });

  collector._processPacket('wan', { 'rx-bits-per-second': '1000000', 'tx-bits-per-second': '500000', running: 'true', disabled: 'false' });

  assert.equal(samples.length, 1, 'onSample fires even when clientsCount is 0');
  assert.equal(samples[0].ifName, 'wan');
  assert.equal(samples[0].rxMbps, 1);
  assert.equal(samples[0].txMbps, 0.5);
  assert.equal(typeof samples[0].ts, 'number');
  // Ring buffer must also accumulate regardless of idle state
  const buf = collector.hist.get('wan');
  assert.ok(buf, 'ring buffer created for interface');
  assert.equal(buf.toArray().length, 1, 'ring buffer has one entry when clientsCount is 0');
});

test('traffic collector preloadHistory seeds ring buffer from DB rows', () => {
  const ros = { connected: true, on() {} };
  const io  = { engine: { clientsCount: 0 }, emit() {} };
  const collector = new TrafficCollector({ ros, io, defaultIf: 'wan', historyMinutes: 1, state: {} });

  const rows = [
    { ts: 1000, rx_mbps: 0.5, tx_mbps: 0.1 },
    { ts: 2000, rx_mbps: 1.0, tx_mbps: 0.2 },
    { ts: 3000, rx_mbps: 1.5, tx_mbps: 0.3 },
  ];
  collector.preloadHistory('wan', rows);

  const pts = collector.hist.get('wan').toArray();
  assert.equal(pts.length, 3);
  assert.equal(pts[0].ts, 1000);
  assert.equal(pts[2].rx_mbps, 1.5);
});

test('traffic collector treats missing or zero traffic fields as zero Mbps', () => {
  const socketEmits = [];
  const fakeSocket2 = { emit(ev, data) { socketEmits.push({ ev, data }); } };
  const ros = { connected: true, on() {} };
  const io = { engine: { clientsCount: 1 }, emit() {} };
  const collector = new TrafficCollector({ ros, io, defaultIf: 'wan', historyMinutes: 1, state: {} });
  collector.subscriptions.set('socket-1', { ifName: 'wan', socket: fakeSocket2 });

  collector._processPacket('wan', {
    'rx-bits-per-second': undefined,
    'tx-bits-per-second': '0',
    running: false,
    disabled: true,
  });

  assert.equal(socketEmits[0].data.rx_mbps, 0);
  assert.equal(socketEmits[0].data.tx_mbps, 0);
  assert.equal(socketEmits[0].data.running, false);
  assert.equal(socketEmits[0].data.disabled, true);
});

// --- System Collector ---
const SystemCollector = track(require('../src/collectors/system'));

test('system collector parses CPU, memory, and HDD percentages', () => {
  const emitted = [];
  const _chain = { emit(ev, data) { emitted.push({ ev, data }); } }; _chain.to = () => _chain;
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); }, to: () => _chain };
  const ros = { connected: true, on() {} };
  const collector = new SystemCollector({ ros, io, pollMs: 5000, state: {} });
  collector._lastUpdateFetch = Date.now();
  collector._lastHealth = [{ name: 'cpu-temperature', value: '47' }];
  collector._lastUpdateRow = { 'latest-version': '7.17', status: 'New version is available' };
  collector._processRow({ 'cpu-load': '42', 'total-memory': '1073741824', 'free-memory': '536870912', 'total-hdd-space': '134217728', 'free-hdd-space': '67108864', version: '7.16 (stable)', uptime: '3d12h', 'board-name': 'RB4011', 'cpu-count': '4', 'cpu-frequency': '1400' });

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

test('system collector handles zero total memory without division by zero', () => {
  const emitted = [];
  const _chain = { emit(ev, data) { emitted.push({ ev, data }); } }; _chain.to = () => _chain;
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); }, to: () => _chain };
  const ros = { connected: true, on() {} };
  const collector = new SystemCollector({ ros, io, pollMs: 5000, state: {} });
  collector._lastUpdateFetch = Date.now();
  collector._lastHealth = [];
  collector._lastUpdateRow = {};
  collector._processRow({ 'cpu-load': '0', 'total-memory': '0' });

  const d = emitted[0].data;
  assert.equal(d.memPct, 0);
  assert.equal(d.hddPct, 0);
  assert.equal(d.cpuLoad, 0);
});

test('system collector returns null temperature when health data is missing (virtualized RouterOS)', () => {
  const emitted = [];
  const _chain = { emit(ev, data) { emitted.push({ ev, data }); } }; _chain.to = () => _chain;
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); }, to: () => _chain };
  const ros = { connected: true, on() {} };
  const collector = new SystemCollector({ ros, io, pollMs: 5000, state: {} });
  collector._lastUpdateFetch = Date.now();
  collector._lastHealth = [];
  collector._lastUpdateRow = {};
  collector._processRow({ 'cpu-load': '10', 'total-memory': '1000000', 'free-memory': '500000', version: '7.16' });

  assert.equal(emitted[0].data.tempC, null);
});

test('system collector returns null temperature when health query fails entirely', () => {
  const emitted = [];
  const _chain = { emit(ev, data) { emitted.push({ ev, data }); } }; _chain.to = () => _chain;
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); }, to: () => _chain };
  const ros = { connected: true, on() {} };
  const collector = new SystemCollector({ ros, io, pollMs: 5000, state: {} });
  collector._lastUpdateFetch = Date.now();
  collector._lastHealth = [];
  collector._lastUpdateRow = { 'latest-version': '7.16', status: 'System is already up to date' };
  collector._processRow({ 'cpu-load': '5', 'total-memory': '1000000', 'free-memory': '500000', version: '7.16' });

  assert.equal(emitted.length, 1);
  assert.equal(emitted[0].data.tempC, null);
  assert.equal(emitted[0].data.cpuLoad, 5);
});

test('system collector detects no update when versions match', () => {
  const emitted = [];
  const _chain = { emit(ev, data) { emitted.push({ ev, data }); } }; _chain.to = () => _chain;
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); }, to: () => _chain };
  const ros = { connected: true, on() {} };
  const collector = new SystemCollector({ ros, io, pollMs: 5000, state: {} });
  collector._lastUpdateFetch = Date.now();
  collector._lastHealth = [];
  collector._lastUpdateRow = { 'latest-version': '7.16', status: 'System is already up to date' };
  collector._processRow({ version: '7.16 (stable)', 'cpu-load': '0', 'total-memory': '1' });

  assert.equal(emitted[0].data.updateAvailable, false);
});

test('system collector handles health items without temperature name', () => {
  const emitted = [];
  const _chain = { emit(ev, data) { emitted.push({ ev, data }); } }; _chain.to = () => _chain;
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); }, to: () => _chain };
  const ros = { connected: true, on() {} };
  const collector = new SystemCollector({ ros, io, pollMs: 5000, state: {} });
  collector._lastUpdateFetch = Date.now();
  collector._lastHealth = [{ name: 'voltage', value: '24' }, { name: 'fan-speed', value: '3500' }];
  collector._lastUpdateRow = {};
  collector._processRow({ version: '7.16', 'cpu-load': '0', 'total-memory': '1' });

  assert.equal(emitted[0].data.tempC, null);
});

test('system collector includes arch, serial, and license level in payload', () => {
  const emitted = [];
  const _chain = { emit(ev, data) { emitted.push({ ev, data }); } }; _chain.to = () => _chain;
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); }, to: () => _chain };
  const ros = { connected: true, on() {} };
  const collector = new SystemCollector({ ros, io, pollMs: 5000, state: {} });
  collector._lastUpdateFetch = Date.now();
  collector._lastHealth = [];
  collector._lastUpdateRow = {};
  collector._staticSerial  = 'ABC1234XYZ';
  collector._staticLicense = '6';
  collector._staticFetched = true;
  collector._processRow({ 'cpu-load': '0', 'total-memory': '1', 'architecture-name': 'arm64' });
  assert.equal(emitted[0].data.arch, 'arm64');
  assert.equal(emitted[0].data.serial, 'ABC1234XYZ');
  assert.equal(emitted[0].data.licenseLevel, '6');
});

// --- Connections Collector ---
const ConnectionsCollector = track(require('../src/collectors/connections'));

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
  const io = {
    engine: { clientsCount: 1 },
    sockets: { adapter: { rooms: new Map() } },
    // Records chained room emits too: since issue #108 the full conn:update
    // payload is scoped to page-connections + dash-card-connections, while
    // only the sidebar count stays router-wide.
    to(room) { const rec = { to() { return rec; }, emit(ev, data) { emitted.push({ ev, data, room }); } }; return rec; },
    emit(ev, data) { emitted.push({ ev, data }); },
  };
  const collector = new ConnectionsCollector({
    ros, io, pollMs: 5000, topN: 5, state: {},
    dhcpNetworks: { getLanCidrs: () => ['192.168.1.0/24'] },
    dhcpLeases: { getNameByIP: () => null, getNameByMAC: () => null },
    arp: { getByIP: () => null },
  });
  await collector.tick();

  const p = emitted.find(e => e.ev === 'conn:update').data.protoCounts;
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
  const io = {
    engine: { clientsCount: 1 },
    sockets: { adapter: { rooms: new Map() } },
    // Records chained room emits too: since issue #108 the full conn:update
    // payload is scoped to page-connections + dash-card-connections, while
    // only the sidebar count stays router-wide.
    to(room) { const rec = { to() { return rec; }, emit(ev, data) { emitted.push({ ev, data, room }); } }; return rec; },
    emit(ev, data) { emitted.push({ ev, data }); },
  };
  const collector = new ConnectionsCollector({
    ros, io, pollMs: 5000, topN: 10, state: {},
    dhcpNetworks: { getLanCidrs: () => ['192.168.1.0/24'] },
    dhcpLeases: { getNameByIP: () => null, getNameByMAC: () => null },
    arp: { getByIP: () => null },
  });
  await collector.tick();

  const d = emitted.find(e => e.ev === 'conn:update').data;
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
  const io = {
    engine: { clientsCount: 1 },
    sockets: { adapter: { rooms: new Map() } },
    // Records chained room emits too: since issue #108 the full conn:update
    // payload is scoped to page-connections + dash-card-connections, while
    // only the sidebar count stays router-wide.
    to(room) { const rec = { to() { return rec; }, emit(ev, data) { emitted.push({ ev, data, room }); } }; return rec; },
    emit(ev, data) { emitted.push({ ev, data }); },
  };
  const collector = new ConnectionsCollector({
    ros, io, pollMs: 5000, topN: 5, state: {},
    dhcpNetworks: { getLanCidrs: () => ['192.168.1.0/24'] },
    dhcpLeases: { getNameByIP: () => null, getNameByMAC: () => null },
    arp: { getByIP: () => null },
  });
  await collector.tick();

  const d = emitted.find(e => e.ev === 'conn:update').data;
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
  const io = {
    engine: { clientsCount: 1 },
    sockets: { adapter: { rooms: new Map() } },
    // Records chained room emits too: since issue #108 the full conn:update
    // payload is scoped to page-connections + dash-card-connections, while
    // only the sidebar count stays router-wide.
    to(room) { const rec = { to() { return rec; }, emit(ev, data) { emitted.push({ ev, data, room }); } }; return rec; },
    emit(ev, data) { emitted.push({ ev, data }); },
  };
  const collector = new ConnectionsCollector({
    ros, io, pollMs: 5000, topN: 5, state: {},
    dhcpNetworks: { getLanCidrs: () => ['192.168.1.0/24'] },
    dhcpLeases: { getNameByIP: () => null, getNameByMAC: () => null },
    arp: { getByIP: () => null },
  });

  await collector.tick();
  assert.equal(emitted.find(e => e.ev === 'conn:update').data.newSinceLast, 1);

  collector.lastPayload = null; // reset so tick() proceeds despite stream guard
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
  const io = {
    engine: { clientsCount: 1 },
    sockets: { adapter: { rooms: new Map() } },
    // Records chained room emits too: since issue #108 the full conn:update
    // payload is scoped to page-connections + dash-card-connections, while
    // only the sidebar count stays router-wide.
    to(room) { const rec = { to() { return rec; }, emit(ev, data) { emitted.push({ ev, data, room }); } }; return rec; },
    emit(ev, data) { emitted.push({ ev, data }); },
  };
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

  const sources = emitted.find(e => e.ev === 'conn:update').data.topSources;
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
  const io = {
    engine: { clientsCount: 1 },
    sockets: { adapter: { rooms: new Map() } },
    // Records chained room emits too: since issue #108 the full conn:update
    // payload is scoped to page-connections + dash-card-connections, while
    // only the sidebar count stays router-wide.
    to(room) { const rec = { to() { return rec; }, emit(ev, data) { emitted.push({ ev, data, room }); } }; return rec; },
    emit(ev, data) { emitted.push({ ev, data }); },
  };
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

  const payload = emitted.find(e => e.ev === 'conn:update').data;
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
  const io = {
    engine: { clientsCount: 1 },
    sockets: { adapter: { rooms: new Map() } },
    // Records chained room emits too: since issue #108 the full conn:update
    // payload is scoped to page-connections + dash-card-connections, while
    // only the sidebar count stays router-wide.
    to(room) { const rec = { to() { return rec; }, emit(ev, data) { emitted.push({ ev, data, room }); } }; return rec; },
    emit(ev, data) { emitted.push({ ev, data }); },
  };
  const collector = new ConnectionsCollector({
    ros, io, pollMs: 5000, topN: 10, maxConns: 2, state: {},
    geoLookup: (ip) => ({ country: ip.endsWith('.3') ? 'TRUNC' : 'KEPT', city: ip }),
    dhcpNetworks: { getLanCidrs: () => ['192.168.1.0/24'] },
    dhcpLeases: { getNameByIP: () => null, getNameByMAC: () => null },
    arp: { getByIP: () => null },
  });
  await collector.tick();

  const payload = emitted.find(e => e.ev === 'conn:update').data;
  assert.equal(payload.processingCapped, true);
  assert.equal(payload.processed, 2);
  assert.ok(!payload.topDestinations.some(d => d.key.includes('198.51.100.3')));
  assert.ok(!payload.topCountries.some(c => c.cc === 'TRUNC'));
  assert.deepEqual(payload.topPorts, [{ port: '443', count: 1 }, { port: '53', count: 1 }]);
});

// --- Firewall Collector ---
const FirewallCollector = track(require('../src/collectors/firewall'));

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
  const io = {
    engine: { clientsCount: 1 },
    emit(ev, data) { emitted.push({ ev, data }); },
    to(room) {
      const chain = { to() { return chain; }, emit(ev, data) { emitted.push({ ev, data }); } };
      return chain;
    },
  };
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
  const io = {
    engine: { clientsCount: 1 },
    emit(ev, data) { emitted.push({ ev, data }); },
    to(room) {
      const chain = { to() { return chain; }, emit(ev, data) { emitted.push({ ev, data }); } };
      return chain;
    },
  };
  const collector = new FirewallCollector({ ros, io, pollMs: 10000, state: {}, topN: 10 });

  await collector._loadInitial();
  loadNum++;
  await collector._loadInitial();

  assert.equal(emitted[1].data.filter[0].deltaPackets, 0);
});

test('firewall collector carries disabled rules, flagged', async () => {
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
  const io = {
    engine: { clientsCount: 1 },
    emit(ev, data) { emitted.push({ ev, data }); },
    to(room) {
      const chain = { to() { return chain; }, emit(ev, data) { emitted.push({ ev, data }); } };
      return chain;
    },
  };
  const collector = new FirewallCollector({ ros, io, pollMs: 10000, state: {}, topN: 10 });
  await collector._loadInitial();

  // These used to be dropped, so the page never showed one and there was no way
  // to re-enable a rule from MikroDash. They travel now; the page dims them and
  // the Action Breakdown and Chain Count cards leave them out of their totals.
  const rules = emitted[0].data.filter;
  assert.equal(rules.length, 3, 'a disabled rule must still reach the page');
  assert.deepEqual(rules.map(r => r.id), ['*1', '*2', '*3']);
  assert.deepEqual(rules.map(r => r.disabled), [true, false, true],
    'both the string and the boolean form of disabled must be recognised');
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
  const io = {
    engine: { clientsCount: 1 },
    emit(ev, data) { emitted.push({ ev, data }); },
    to(room) {
      const chain = { to() { return chain; }, emit(ev, data) { emitted.push({ ev, data }); } };
      return chain;
    },
  };
  const collector = new FirewallCollector({ ros, io, pollMs: 10000, state: {}, topN: 10 });

  await collector._loadInitial();
  assert.ok(collector.prevCounts.has('*1'));
  assert.ok(collector.prevCounts.has('*2'));
  loadNum++;

  await collector._loadInitial();
  assert.ok(!collector.prevCounts.has('*1'), 'stale *1 should be pruned');
  assert.ok(collector.prevCounts.has('*2'));
});

test('firewall collector includes raw table in payload and counter poll', async () => {
  // Verifies that /ip/firewall/raw rules are loaded, emitted in the payload,
  // and included in prevCounts / counter-poll just like filter/nat/mangle.
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    stream: (words, cb) => ({ stop() {} }),
    write: async (cmd) => {
      if (cmd.includes('/filter')) return [{ '.id': '*F1', chain: 'forward', action: 'accept', packets: '10', bytes: '1000', disabled: 'false' }];
      if (cmd.includes('/nat'))    return [];
      if (cmd.includes('/mangle')) return [];
      if (cmd.includes('/raw'))    return [{ '.id': '*R1', chain: 'prerouting', action: 'notrack', packets: '50', bytes: '5000', disabled: 'false' }];
      return [];
    },
  };
  const io = { to() { return io; },
    engine: { clientsCount: 1 },
    emit(ev, data) { emitted.push({ ev, data }); },
    to(room) {
      const chain = { to() { return chain; }, emit(ev, data) { emitted.push({ ev, data }); } };
      return chain;
    },
  };
  const collector = new FirewallCollector({ ros, io, pollMs: 10000, state: {}, topN: 10 });

  await collector._loadInitial();

  assert.equal(emitted.length, 1, 'one emit after _loadInitial');
  const payload = emitted[0].data;

  // raw array present and correctly parsed
  assert.ok(Array.isArray(payload.raw), 'payload.raw is an array');
  assert.equal(payload.raw.length, 1, 'one raw rule');
  assert.equal(payload.raw[0].id, '*R1');
  assert.equal(payload.raw[0].chain, 'prerouting');
  assert.equal(payload.raw[0].action, 'notrack');
  assert.equal(payload.raw[0].packets, 50);
  assert.equal(payload.raw[0].bytes, 5000);

  // filter rule still present
  assert.equal(payload.filter.length, 1, 'filter rule still present');

  // raw rule tracked in prevCounts
  assert.ok(collector.prevCounts.has('*R1'), 'raw rule tracked in prevCounts');

  // activeTable field present in payload
  assert.ok('activeTable' in payload, 'payload includes activeTable');
});

// --- Ping Collector ---
const PingCollector = track(require('../src/collectors/ping'));

test('ping collector processes reply packets and tracks RTT and loss', () => {
  const emitted = [];
  const ros = { connected: true, on() {} };
  const io = { to() { return io; }, engine: { clientsCount: 1 }, on() {}, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new PingCollector({ ros, io, pollMs: 10000, state: {}, target: '1.1.1.1' });

  collector._processPacket({ status: 'replied', time: '3ms' });
  collector._processPacket({ status: 'replied', time: '5ms' });
  collector._processPacket({ status: 'replied', time: '4ms' });

  // rtt reflects the last emitted packet; loss = 0 (3/3 replied)
  assert.equal(emitted[emitted.length - 1].data.rtt, 4);
  assert.equal(emitted[emitted.length - 1].data.loss, 0);
});

test('ping collector calculates loss percentage', () => {
  const emitted = [];
  const ros = { connected: true, on() {} };
  const io = { to() { return io; }, engine: { clientsCount: 1 }, on() {}, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new PingCollector({ ros, io, pollMs: 10000, state: {}, target: '1.1.1.1' });

  collector._processPacket({ status: 'replied', time: '3ms' });
  collector._processPacket({ status: 'timeout' });
  collector._processPacket({ status: 'timeout' });

  // 2 out of 3 lost → 67%
  assert.equal(emitted[emitted.length - 1].data.loss, 67);
});

test('ping collector returns null rtt and 100% loss on no replies', () => {
  const emitted = [];
  const ros = { connected: true, on() {} };
  const io = { to() { return io; }, engine: { clientsCount: 1 }, on() {}, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new PingCollector({ ros, io, pollMs: 10000, state: {}, target: '1.1.1.1' });

  collector._processPacket({ status: 'timeout' });
  collector._processPacket({ status: 'timeout' });
  collector._processPacket({ status: 'timeout' });

  assert.equal(emitted[emitted.length - 1].data.rtt, null);
  assert.equal(emitted[emitted.length - 1].data.loss, 100);
});

test('ping collector parses rtt from response-time field when time is absent', () => {
  const emitted = [];
  const ros = { connected: true, on() {} };
  const io = { to() { return io; }, engine: { clientsCount: 1 }, on() {}, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new PingCollector({ ros, io, pollMs: 10000, state: {}, target: '1.1.1.1' });

  collector._processPacket({ status: 'replied', 'response-time': '10ms' });
  collector._processPacket({ status: 'replied', 'response-time': '20ms' });

  // rtt from last packet; both replied → 0% loss
  assert.equal(emitted[emitted.length - 1].data.rtt, 20);
  assert.equal(emitted[emitted.length - 1].data.loss, 0);
});

test('ping collector maintains bounded history', () => {
  const ros = { connected: true, on() {} };
  const io = { to() { return io; }, engine: { clientsCount: 1 }, on() {}, emit() {} };
  const collector = new PingCollector({ ros, io, pollMs: 10000, state: {}, target: '1.1.1.1' });

  for (let i = 0; i < 65; i++) {
    collector._processPacket({ status: 'replied', time: '5ms' });
  }

  assert.equal(collector.history.toArray().length, 60);
  const h = collector.getHistory();
  assert.equal(h.target, '1.1.1.1');
  assert.equal(h.history.length, 60);
});

// --- Top Talkers Collector ---
const TopTalkersCollector = track(require('../src/collectors/talkers'));

test('talkers collector calculates throughput rate between polls', () => {
  // The stream delivers rate-up/rate-down (bits/second) per device directly.
  // tx_mbps = rateUp / 1_000_000; rx_mbps = rateDown / 1_000_000
  const emitted = [];
  const ros = { connected: true, on() {} };
  const io = { to() { return io; }, engine: { clientsCount: 1 }, on() {}, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new TopTalkersCollector({ ros, io, pollMs: 3000, state: {}, topN: 5 });

  // Populate _devicesNext as the stream 'data' event handler does, then commit
  collector._devicesNext.set('AA:BB:CC:DD:EE:FF', { name: 'laptop', mac: 'AA:BB:CC:DD:EE:FF', rateUp: 1_000_000, rateDown: 2_000_000 });
  collector._commitTick();

  // tx = 1_000_000 / 1_000_000 = 1.0 Mbps; rx = 2_000_000 / 1_000_000 = 2.0 Mbps
  assert.equal(emitted[0].data.devices[0].tx_mbps, 1);
  assert.equal(emitted[0].data.devices[0].rx_mbps, 2);
});

test('talkers collector returns zero rate on counter reset', () => {
  const emitted = [];
  const ros = { connected: true, on() {} };
  const io = { to() { return io; }, engine: { clientsCount: 1 }, on() {}, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new TopTalkersCollector({ ros, io, pollMs: 3000, state: {}, topN: 5 });

  // Zero rates reflect an idle device (RouterOS sends rate-up=0/rate-down=0)
  collector._devicesNext.set('AA:BB:CC:DD:EE:FF', { name: 'laptop', mac: 'AA:BB:CC:DD:EE:FF', rateUp: 0, rateDown: 0 });
  collector._commitTick();

  assert.equal(emitted[0].data.devices[0].tx_mbps, 0);
  assert.equal(emitted[0].data.devices[0].rx_mbps, 0);
});

test('talkers collector prunes stale devices', () => {
  const emitted = [];
  const ros = { connected: true, on() {} };
  const io = { to() { return io; }, engine: { clientsCount: 1 }, on() {}, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new TopTalkersCollector({ ros, io, pollMs: 3000, state: {}, topN: 5 });

  // Tick 1: two devices
  collector._devicesNext.set('AA:BB', { name: 'a', mac: 'AA:BB', rateUp: 8000, rateDown: 16000 });
  collector._devicesNext.set('CC:DD', { name: 'b', mac: 'CC:DD', rateUp: 4000, rateDown: 8000 });
  collector._commitTick();
  assert.equal(emitted[0].data.devices.length, 2);

  // Tick 2: CC:DD absent — _devicesNext.clear() after commit means it won't appear
  collector._devicesNext.set('AA:BB', { name: 'a', mac: 'AA:BB', rateUp: 8000, rateDown: 16000 });
  collector._commitTick();
  // fp differs (CC:DD gone), so a new emit fires
  const last = emitted[emitted.length - 1];
  assert.equal(last.data.devices.length, 1);
  assert.ok(!last.data.devices.find(d => d.mac === 'CC:DD'), 'stale device CC:DD should be pruned');
});

test('talkers stream error "unknown command" disables permanently with no retry timer', () => {
  const emitted = [];
  const streamHandlers = {};
  const fakeStream = { on(ev, fn) { streamHandlers[ev] = fn; } };
  const ros = { connected: true, on() {}, stream() { return fakeStream; } };
  const io = { to() { return io; }, engine: { clientsCount: 1 }, on() {}, emit(ev, data) { emitted.push({ ev, data }); } };
  const state = {};
  const collector = new TopTalkersCollector({ ros, io, pollMs: 3000, state, topN: 5 });

  collector._startStream();
  streamHandlers.error(new Error('unknown command'));

  assert.equal(collector._unavailable, true);
  assert.equal(collector._backoffTimer, null, 'no retry timer must be scheduled');
  assert.equal(emitted.length, 1, 'one empty payload emitted');
  assert.deepEqual(emitted[0].data.devices, []);
});

test('talkers stream timeout auto-downgrades to poll mode', () => {
  const streamHandlers = {};
  const fakeStream = { on(ev, fn) { streamHandlers[ev] = fn; } };
  const ros = { connected: true, on() {}, stream() { return fakeStream; }, write: async () => [] };
  const io = { to() { return io; }, engine: { clientsCount: 0 }, on() {}, emit() {} };
  const state = {};
  const collector = new TopTalkersCollector({ ros, io, pollMs: 3000, state, topN: 5 });

  collector._startStream();
  streamHandlers.error(new Error('request timeout'));

  assert.equal(collector.streamMode, false, 'streamMode must flip to false');
  assert.notEqual(collector._pollTimer, null, 'poll timer must be scheduled');
  collector.stop();
});

test('talkers poll error "unknown command" disables permanently and stops scheduling', async () => {
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    write: async () => { throw new Error('unknown command'); },
  };
  const io = { to() { return io; }, engine: { clientsCount: 1 }, on() {}, emit(ev, data) { emitted.push({ ev, data }); } };
  const state = {};
  const collector = new TopTalkersCollector({ ros, io, pollMs: 3000, state, topN: 5, streamMode: false });

  await collector._pollTalkersOnce();

  assert.equal(collector._unavailable, true);
  collector._scheduleTalkersNext();
  assert.equal(collector._pollTimer, null, 'scheduling must be a no-op when unavailable');
  assert.deepEqual(emitted[0].data.devices, []);
});

test('talkers poll timeout is transient — logs error and keeps scheduling', async () => {
  const ros = {
    connected: true,
    on() {},
    write: async () => { throw new Error('connection timed out'); },
  };
  const io = { to() { return io; }, engine: { clientsCount: 1 }, on() {}, emit() {} };
  const state = {};
  const collector = new TopTalkersCollector({ ros, io, pollMs: 3000, state, topN: 5, streamMode: false });

  await collector._pollTalkersOnce();

  assert.equal(collector._unavailable, false, 'transient timeout must not set _unavailable');
  assert.ok(state.lastTalkersErr, 'error should be logged to state.lastTalkersErr');
  collector._scheduleTalkersNext();
  assert.notEqual(collector._pollTimer, null, 'poll timer should still be schedulable');
  collector.stop();
});

// --- VPN Collector ---
const VpnCollector = track(require('../src/collectors/vpn'));

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
  const _chain = { emit(ev, data) { emitted.push({ ev, data }); } }; _chain.to = () => _chain;
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); }, to: () => _chain };
  const collector = new VpnCollector({ ros, io, pollMs: 10000, state: {} });
  await collector._loadInitial();

  const t = emitted[0].data.tunnels;
  assert.equal(t[0].name, 'myphone');
  assert.equal(t[1].name, 'server');
  assert.equal(t[2].name, '10.0.0.4/32');
  assert.equal(t[3].name, 'DDDDEEEEFFFFGGGG' + '\u2026');
  assert.equal(t[4].name, '?');
});

test('vpn collector distinguishes active, stale and never-connected peers', async () => {
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    stream: (words, cb) => ({ stop() {} }),
    write: async () => [
      { 'public-key': 'A', 'last-handshake': '30s', 'rx-bytes': '0', 'tx-bytes': '0' },
      { 'public-key': 'B', 'last-handshake': 'never', 'rx-bytes': '0', 'tx-bytes': '0' },
      { 'public-key': 'C', 'last-handshake': '', 'rx-bytes': '0', 'tx-bytes': '0' },
      // Handshook once, then vanished. The old rule called this connected.
      { 'public-key': 'D', 'last-handshake': '3d4h', 'rx-bytes': '0', 'tx-bytes': '0' },
    ],
  };
  const _chain = { emit(ev, data) { emitted.push({ ev, data }); } }; _chain.to = () => _chain;
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); }, to: () => _chain };
  const collector = new VpnCollector({ ros, io, pollMs: 10000, state: {} });
  await collector._loadInitial();

  const t = emitted[0].data.tunnels;
  assert.equal(t[0].state, 'active');
  assert.equal(t[1].state, 'never');
  assert.equal(t[2].state, 'never');
  assert.equal(t[3].state, 'stale', 'a peer last seen days ago is not active');
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
  const _chain = { emit(ev, data) { emitted.push({ ev, data }); } }; _chain.to = () => _chain;
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); }, to: () => _chain };
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

test('vpn collector: counter stream updates last-handshake and drives live rates', async () => {
  // Core regression: /listen stream does not reliably push rx-bytes/tx-bytes/
  // last-handshake on RouterOS 7. The counter stream (_onCounterRecord) must
  // merge these and emit updated tunnels when they change.
  const emitted = [];
  const initialPeer = { 'public-key': 'A', name: 'peer', interface: 'wg0', 'last-handshake': '30s', 'rx': '1000', 'tx': '2000', 'allowed-address': '10.0.0.2/32' };
  const updatedPeer = { 'public-key': 'A', name: 'peer', interface: 'wg0', 'last-handshake': '5s',  'rx': '5000', 'tx': '8000', 'allowed-address': '10.0.0.2/32' };
  const ros = {
    connected: true, on() {},
    stream: () => ({ stop() {}, on() { return this; } }),
    write: async () => [initialPeer],
  };
  const _chain = { emit(ev, data) { emitted.push({ ev, data }); } }; _chain.to = () => _chain;
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); }, to: () => _chain };
  const collector = new VpnCollector({ ros, io, pollMs: 10000, state: {} });

  await collector._loadInitial();
  assert.equal(emitted.length, 1);
  assert.equal(emitted[0].data.tunnels[0].lastHandshake, '30s');

  // Simulate counter record arriving 2 s later
  const origNow = Date.now;
  Date.now = () => origNow() + 2000;
  try {
    collector._onCounterRecord(updatedPeer);
    await new Promise(r => setTimeout(r, 100)); // flush 50ms emit debounce
  } finally { Date.now = origNow; }

  assert.equal(emitted.length, 2, 'counter record must emit when bytes/handshake change');
  const t = emitted[1].data.tunnels[0];
  assert.equal(t.lastHandshake, '5s', 'last-handshake updated by counter record');
  assert.equal(t.rx, 5000, 'rx-bytes updated');
  assert.ok(t.rxRate > 0, 'rxRate positive: ' + t.rxRate);
  assert.ok(t.txRate > 0, 'txRate positive: ' + t.txRate);
});

test('vpn collector: _prev.ts not advanced on handshake-only update; rates decay after idle >10s', async () => {
  const emitted = [];
  const base = 1000000;
  const origNow = Date.now;
  Date.now = () => base;
  try {
    const ros = { connected: true, on() {}, stream: (words, cb) => ({ stop() {} }), write: async () => [] };
    const _ch = { emit(ev, d) { emitted.push({ ev, d }); } }; _ch.to = () => _ch;
    const io = { emit(ev, d) { emitted.push({ ev, d }); }, to: () => _ch };
    const collector = new VpnCollector({ ros, io, pollMs: 10000, state: {} });

    // Seed _prev 10 s in the past, bytes at 1000/2000
    collector._prev.set('A', { rx: 1000, tx: 2000, ts: base - 10000 });
    collector._peers.set('A', { 'public-key': 'A', name: 'p', 'last-handshake': '3s', 'rx-bytes': '1000', 'tx-bytes': '2000' });

    // Handshake-only emit: bytes unchanged — _prev.ts must NOT advance
    collector._emit();
    assert.equal(collector._prev.get('A').ts, base - 10000, '_prev.ts unchanged when bytes same');

    // 15 s later, bytes still the same — rates must decay to zero
    Date.now = () => base + 15000;
    collector._emit();
    const idle = emitted[emitted.length - 1].d.tunnels[0];
    assert.equal(idle.rxRate, 0, 'rxRate decays to 0 after idle >10s');
    assert.equal(idle.txRate, 0, 'txRate decays to 0 after idle >10s');
  } finally { Date.now = origNow; }
});

// --- Wireless Collector ---
const WirelessCollector = track(require('../src/collectors/wireless'));
// Since issue #108 a wireless tick emits twice: the client list to the page and
// dash-card rooms, and a bare count router-wide for the sidebar badge. Tests
// that count emits mean the payload, so they count that one.
const wlEmits = (a) => a.filter(e => e.ev === 'wireless:update');

test('wireless collector detects band from RouterOS band field', () => {
  const emitted = [];
  const ros = { connected: true, on() {} };
  const io = { to() { return io; }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new WirelessCollector({
    ros, io, pollMs: 5000, state: {},
    dhcpLeases: { getNameByMAC: () => null },
    arp: { getByMAC: () => null },
  });
  collector._onBatch('wifi', [
    { 'mac-address': 'AA:BB', interface: 'wifi1', band: '5ghz-n/ac/ax', signal: '-50' },
    { 'mac-address': 'CC:DD', interface: 'wifi3', band: '6ghz-ax',       signal: '-60' },
    { 'mac-address': 'EE:FF', interface: 'wlan0', band: '2ghz-b/g/n',    signal: '-70' },
    { 'mac-address': '11:22', interface: 'wlan0', band: '5ghz-ax',       signal: '-55' },
  ]);

  const clients = wlEmits(emitted)[0].data.clients;
  const byMac = Object.fromEntries(clients.map(c => [c.mac, c]));
  assert.equal(byMac['AA:BB'].band, '5GHz');
  assert.equal(byMac['CC:DD'].band, '6GHz');
  assert.equal(byMac['EE:FF'].band, '2.4GHz');
  assert.equal(byMac['11:22'].band, '5GHz');
});

test('wireless collector sorts clients by signal strength descending', () => {
  const emitted = [];
  const ros = { connected: true, on() {} };
  const io = { to() { return io; }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new WirelessCollector({
    ros, io, pollMs: 5000, state: {},
    dhcpLeases: { getNameByMAC: () => null },
    arp: { getByMAC: () => null },
  });
  collector._onBatch('wifi', [
    { 'mac-address': 'AA:BB', signal: '-70', interface: 'wifi1' },
    { 'mac-address': 'CC:DD', signal: '-40', interface: 'wifi1' },
    { 'mac-address': 'EE:FF', signal: '-55', interface: 'wifi1' },
  ]);

  const macs = wlEmits(emitted)[0].data.clients.map(c => c.mac);
  assert.deepEqual(macs, ['CC:DD', 'EE:FF', 'AA:BB']);
});

test('wireless collector enriches payloads with DHCP names, ARP IPs, and holds absent clients for ABSENCE_THRESHOLD ticks', () => {
  const emitted = [];
  const ros = { connected: true, on() {} };
  const io = { to() { return io; }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new WirelessCollector({
    ros, io, pollMs: 5000, state: {},
    dhcpLeases: { getNameByMAC: (mac) => mac === 'AA:BB' ? { name: 'Laptop' } : null },
    arp: { getByMAC: (mac) => mac === 'AA:BB' ? { ip: '192.168.1.20' } : null },
  });
  const client = { 'mac-address': 'AA:BB', signal: '-55', interface: 'wifi1', 'tx-rate': 'HE-MCS 11 80MHz', ssid: 'Office' };

  collector._onBatch('wifi', [client]);        // batch 1: client present — emits
  assert.equal(wlEmits(emitted).length, 1);
  assert.equal(wlEmits(emitted)[0].data.clients[0].name, 'Laptop');
  assert.equal(wlEmits(emitted)[0].data.clients[0].ip, '192.168.1.20');
  assert.equal(wlEmits(emitted)[0].data.clients[0].ssid, 'Office');
  assert.equal(wlEmits(emitted)[0].data.mode, 'wifi');

  // Per-MAC absence guard: client must be absent for ABSENCE_THRESHOLD (3)
  // consecutive batches before being removed from the emitted list.
  collector._onBatch('wifi', []);   // batch 2: absent tick 1 — held (absentTicks=1)
  assert.equal(wlEmits(emitted).length, 1, 'absent batch 1 held');
  assert.equal(collector._absentTicks.get('AA:BB'), 1);

  collector._onBatch('wifi', []);   // batch 3: absent tick 2 — held (absentTicks=2)
  assert.equal(wlEmits(emitted).length, 1, 'absent batch 2 held');

  collector._onBatch('wifi', []);   // batch 4: absent tick 3 — authoritative removal, emits []
  assert.equal(wlEmits(emitted).length, 2, 'removed after 3 absent batches');
  assert.deepEqual(wlEmits(emitted)[1].data.clients, []);
  assert.equal(wlEmits(emitted)[1].data.mode, 'wifi');
  assert.ok(!collector._knownClients.has('AA:BB'), 'client removed from knownClients');
});

test('wireless collector holds partial result during wifi-qcom re-association (per-MAC absence guard)', () => {
  // Simulates HAPax2 wifi-qcom behaviour: physical radios briefly return only
  // the virtual-AP client during re-association. The mightBePartial guard
  // fires when the API returns > 0 but < 50% of known clients (and >= 3 known).
  // On a partial batch, absence aging is SKIPPED entirely — _absentTicks stays
  // empty and all known clients are preserved indefinitely until a non-partial
  // result arrives.
  const emitted = [];
  const fullList = [
    { 'mac-address': 'AA:BB', signal: '-55', interface: 'wifi1' },
    { 'mac-address': 'CC:DD', signal: '-65', interface: 'wifi2' },
    { 'mac-address': 'EE:FF', signal: '-70', interface: 'wifi3-virt' },
  ];
  const partial = [{ 'mac-address': 'EE:FF', signal: '-70', interface: 'wifi3-virt' }];
  const ros = { connected: true, on() {} };
  const io = { to() { return io; }, emit(ev, data) { emitted.push({ ev, data }); } };
  const state = {};
  const collector = new WirelessCollector({ ros, io, pollMs: 5000, state, dhcpLeases: null, arp: null });

  collector._onBatch('wifi', fullList);   // batch 1: 3 clients (full) — emits
  assert.equal(wlEmits(emitted)[0].data.clients.length, 3, 'all 3 clients on batch 1');

  collector._onBatch('wifi', partial);    // batch 2: partial (1/3 < 50%) — mightBePartial=true, aging SKIPPED
  assert.equal(wlEmits(emitted).length, 1, 'no new emit on partial batch (clients unchanged)');
  assert.equal(collector._absentTicks.size, 0, 'absence aging skipped on partial batch');
  assert.ok(collector._knownClients.has('AA:BB'), 'AA:BB still held during partial');
  assert.ok(collector._knownClients.has('CC:DD'), 'CC:DD still held during partial');

  collector._onBatch('wifi', partial);    // batch 3: partial — aging still skipped
  assert.equal(wlEmits(emitted).length, 1, 'still no extra emit on second partial batch');
  assert.equal(collector._absentTicks.size, 0, 'still no absence ticks');

  assert.ok(state.lastWirelessTs > 0, 'lastWirelessTs updated during hold');
});

test('wireless collector: client that reappears before eviction resets its absence counter', () => {
  // Ensures that a client which briefly disappears then returns is not evicted.
  const emitted = [];
  const seq = [
    [{ 'mac-address': 'AA:BB', signal: '-55', interface: 'wifi1' }], // batch 1: present
    [],                                                                // batch 2: absent (1)
    [{ 'mac-address': 'AA:BB', signal: '-55', interface: 'wifi1' }], // batch 3: returns — reset
    [],                                                                // batch 4: absent (1) again — held
    [],                                                                // batch 5: absent (2) — held
    [],                                                                // batch 6: absent (3) — evicted
  ];
  const ros = { connected: true, on() {} };
  const io = { to() { return io; }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new WirelessCollector({ ros, io, pollMs: 5000, state: {}, dhcpLeases: null, arp: null });

  collector._onBatch('wifi', seq[0]);   // batch 1: present
  assert.equal(collector._absentTicks.has('AA:BB'), false, 'no absent entry when present');

  collector._onBatch('wifi', seq[1]);   // batch 2: absent (1)
  assert.equal(collector._absentTicks.get('AA:BB'), 1);
  assert.ok(collector._knownClients.has('AA:BB'), 'still in knownClients at absent=1');

  collector._onBatch('wifi', seq[2]);   // batch 3: returns — counter reset
  assert.equal(collector._absentTicks.has('AA:BB'), false, 'absent counter cleared on return');
  assert.ok(collector._knownClients.has('AA:BB'), 'still in knownClients after return');

  collector._onBatch('wifi', seq[3]);   // batch 4: absent (1) fresh
  assert.equal(collector._absentTicks.get('AA:BB'), 1, 'fresh absent counter');

  collector._onBatch('wifi', seq[4]);   // batch 5: absent (2)
  assert.ok(collector._knownClients.has('AA:BB'), 'still held at absent=2');

  collector._onBatch('wifi', seq[5]);   // batch 6: absent (3) — evicted
  assert.ok(!collector._knownClients.has('AA:BB'), 'evicted at absent=3');
  const lastEmit = wlEmits(emitted).slice(-1)[0];
  assert.deepEqual(lastEmit.data.clients, [], 'empty clients emitted on eviction');
});

test('wireless collector merges CAPsMAN clients when _capsmanAvailable is true', () => {
  const emitted = [];
  const ros = { connected: true, on() {} };
  const io = { to() { return io; }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new WirelessCollector({ ros, io, pollMs: 5000, state: {}, dhcpLeases: null, arp: null });
  collector._capsmanAvailable = true;

  collector._onBatch('capsman', [{ 'mac-address': 'CA:PM:AN:01:02:03', 'rx-signal': '-62', interface: 'ap1-2g', 'tx-rate-set': '54Mbps', uptime: '30m' }]);

  assert.equal(wlEmits(emitted).length, 1, 'one emit');
  assert.equal(wlEmits(emitted)[0].ev, 'wireless:update');
  const clients = wlEmits(emitted)[0].data.clients;
  assert.equal(clients.length, 1, 'one CAPsMAN client');
  assert.equal(clients[0].mac, 'CA:PM:AN:01:02:03');
  assert.equal(clients[0].signal, -62);
  assert.equal(clients[0].iface, 'ap1-2g');
  assert.equal(clients[0].band, '2.4GHz', 'band inferred from -2g suffix');
  assert.equal(clients[0].source, 'capsman');
  assert.equal(wlEmits(emitted)[0].data.capsmanAvailable, true);
});

test('wireless collector band inference from CAPsMAN interface name suffixes', () => {
  const emitted = [];
  const ros = { connected: true, on() {} };
  const io = { to() { return io; }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new WirelessCollector({ ros, io, pollMs: 5000, state: {}, dhcpLeases: null, arp: null });
  collector._capsmanAvailable = true;

  collector._onBatch('capsman', [
    { 'mac-address': 'AA:00:00:00:00:01', 'rx-signal': '-50', interface: 'ap-2g',  uptime: '1m' },
    { 'mac-address': 'AA:00:00:00:00:02', 'rx-signal': '-50', interface: 'ap-5g',  uptime: '1m' },
    { 'mac-address': 'AA:00:00:00:00:03', 'rx-signal': '-50', interface: 'ap-6g',  uptime: '1m' },
    { 'mac-address': 'AA:00:00:00:00:04', 'rx-signal': '-50', interface: 'ap-lan', uptime: '1m' },
  ]);

  const byMac = {};
  wlEmits(emitted)[0].data.clients.forEach(function(c){ byMac[c.mac] = c; });
  assert.equal(byMac['AA:00:00:00:00:01'].band, '2.4GHz');
  assert.equal(byMac['AA:00:00:00:00:02'].band, '5GHz');
  assert.equal(byMac['AA:00:00:00:00:03'].band, '6GHz');
  assert.equal(byMac['AA:00:00:00:00:04'].band, '', 'no band for unrecognised suffix');
});

test('wireless collector does not duplicate client when MAC appears in both local wireless and CAPsMAN', () => {
  const emitted = [];
  const ros = { connected: true, on() {} };
  const io = { to() { return io; }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new WirelessCollector({ ros, io, pollMs: 5000, state: {}, dhcpLeases: null, arp: null });
  collector.mode = 'wifi';
  collector._lastCapsmanBatch = [{ 'mac-address': 'DD:DD:DD:DD:DD:DD', 'rx-signal': '-40', interface: 'ap-5g', uptime: '1m', _capsman: true }];
  collector._onBatch('wifi', [{ 'mac-address': 'DD:DD:DD:DD:DD:DD', signal: '-40', interface: 'wlan1', band: '5GHz' }]);

  const clients = wlEmits(emitted)[0].data.clients;
  assert.equal(clients.length, 1, 'MAC deduplicated — local wireless wins');
  assert.equal(clients[0].source, undefined, 'local wireless client has no capsman source tag');
});

test('wireless collector filters out Ethernet interface rows with no wireless-specific fields', () => {
  const emitted = [];
  const ros = { connected: true, on() {} };
  const io = { to() { return io; }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new WirelessCollector({ ros, io, pollMs: 5000, state: {}, dhcpLeases: null, arp: null });
  collector.mode = 'wireless';
  collector._onBatch('wireless', [
    { 'mac-address': 'AA:BB:CC:DD:EE:01', name: 'ether1', type: 'ether' },
    { 'mac-address': 'AA:BB:CC:DD:EE:02', interface: 'wlan1', 'signal-strength': '-55', ssid: 'MyNet' },
  ]);
  assert.equal(wlEmits(emitted).length, 1, 'one emit');
  assert.equal(wlEmits(emitted)[0].data.clients.length, 1, 'Ethernet row filtered out');
  assert.equal(wlEmits(emitted)[0].data.clients[0].mac, 'AA:BB:CC:DD:EE:02', 'only real wireless client kept');
});

// --- Logs Collector ---
const LogsCollector = track(require('../src/collectors/logs'));

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
  // Logs collector emits via io.to('page-logs').to('dash-card-logs').emit(...)
  const io = {
    to(room) {
      const chain = { to() { return chain; }, emit(ev, data) { emitted.push({ ev, data }); } };
      return chain;
    },
  };
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

test('logs collector _loadInitial() seeds ring buffer from /log/print response', async () => {
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    write: async (cmd) => {
      if (cmd === '/log/print') return [
        { time: '12:00:00', topics: 'system,info',    message: 'router started' },
        { time: '12:00:01', topics: 'firewall,error', message: 'packet dropped' },
        { time: '12:00:02', topics: '',               message: '' },
      ];
      return [];
    },
    stream() { return { stop() {} }; },
  };
  const io = {
    engine: { clientsCount: 0 },
    to() { const chain = { to() { return chain; }, emit(ev, d) { emitted.push({ ev, d }); } }; return chain; },
  };
  const state = {};
  const collector = new LogsCollector({ ros, io, state });
  await collector._loadInitial();
  const history = collector.getHistory();
  assert.equal(history.length, 2, 'empty-message row skipped');
  assert.equal(history[0].message, 'router started');
  assert.equal(history[0].severity, 'info');
  assert.equal(history[1].message, 'packet dropped');
  assert.equal(history[1].severity, 'error');
  assert.equal(emitted.length, 0, 'no emit when clientsCount is 0');
});

// --- DHCP Leases Collector ---
const DhcpLeasesCollector = track(require('../src/collectors/dhcpLeases'));

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

  const active = collector.getInUseLeaseIPs();
  assert.ok(active.includes('192.168.1.1'));
  assert.ok(active.includes('192.168.1.2'));
  assert.ok(active.includes('192.168.1.3'));
  assert.ok(!active.includes('192.168.1.4'));
});

// --- Interface Status Collector ---
const InterfaceStatusCollector = track(require('../src/collectors/interfaceStatus'));

test('interface status collector normalizes booleans and computes Mbps', () => {
  // The collector no longer uses _loadInitial(). Data flows from three persistent
  // streams into _ifaces, _addrs, and _streamRates maps; _buildAndEmit() reads them.
  const emitted = [];
  const ros = { connected: true, on() {}, stream: () => ({ stop() {} }) };
  const _chain = { emit(ev, data) { emitted.push({ ev, data }); } }; _chain.to = () => _chain;
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); }, to: () => _chain };
  const collector = new InterfaceStatusCollector({ ros, io, pollMs: 5000, state: {} });

  // Populate as the metadata and monitor-traffic streams would
  collector._ifaces.set('ether1', { name: 'ether1', type: 'ether', running: 'true', disabled: 'false' });
  collector._ifaces.set('ether2', { name: 'ether2', type: 'ether', running: true,   disabled: false   });
  collector._addrs.set('ether1', ['192.168.1.1/24', '10.0.0.1/24']);
  // _streamRates holds already-parsed Mbps values (computed by the monitor stream)
  collector._streamRates.set('ether1', { rxMbps: 15, txMbps: 8.5 });
  collector._streamRates.set('ether2', { rxMbps: 0,  txMbps: 0   });

  collector._buildAndEmit();

  const ifaces = emitted[0].data.interfaces;
  assert.equal(ifaces[0].running, true);
  assert.equal(ifaces[0].disabled, false);
  assert.equal(ifaces[0].rxMbps, 15);
  assert.equal(ifaces[0].txMbps, 8.5);
  assert.deepEqual(ifaces[0].ips, ['192.168.1.1/24', '10.0.0.1/24']);
  assert.equal(ifaces[1].running, true);
  assert.equal(ifaces[1].rxMbps, 0);
});

// ── A split print cycle must not truncate the interface list (issue #119) ───
//
// `/interface/print` runs with `=interval=N`, so the router re-prints the WHOLE
// list every cycle, one packet per interface. _commitMeta then REPLACES _ifaces
// with whatever accumulated in _ifacesNext.
//
// The only thing deciding when a cycle has ended was a 300 ms debounce, reset on
// every packet. A debounce cannot delimit a burst: if any gap between two
// packets of one cycle exceeds it, the timer fires MID-CYCLE and installs a
// partial map as the complete truth. The rest of that cycle then lands in a
// fresh map, so the next commit installs an even smaller set.
//
// Reported on a CCR2004 — many interfaces, so many packets and more chances of
// a gap — as the Traffic dropdown losing all but one interface after a couple
// of minutes. A couple of minutes is one or two ticks at the default 60 s meta
// interval.
test('a print cycle split by a slow packet does not shrink the interface list', async () => {
  const { EventEmitter } = require('node:events');
  const fake = new EventEmitter(); fake.stop = () => {};
  const ros = { connected: true, on() {}, stream: () => fake };
  const _chain = { emit() {} }; _chain.to = () => _chain;
  const io = { engine: { clientsCount: 1 }, emit() {}, to: () => _chain };
  const collector = new InterfaceStatusCollector({ ros, io, pollMs: 5000, metaPollMs: 60000, state: {} });

  const row = (n) => ({ name: n, type: 'ether', running: 'true', disabled: 'false' });
  const NAMES = ['ether1', 'ether2', 'ether3', 'sfp-sfpplus1'];
  const settle = () => new Promise(r => setTimeout(r, 400));   // > the 300 ms debounce

  collector._startIfStream();

  // A clean cycle: every packet inside the debounce window.
  for (const n of NAMES) fake.emit('data', row(n));
  await settle();
  assert.equal(collector._ifaces.size, 4, 'a whole cycle must commit whole');

  // The same cycle, delivered with one slow packet in the middle. This is the
  // reported fault: the router sent all four, and the collector must end up
  // holding all four however they were spread over the wire.
  fake.emit('data', row('ether1'));
  fake.emit('data', row('ether2'));
  await settle();                       // the debounce fires here, mid-cycle
  fake.emit('data', row('ether3'));
  fake.emit('data', row('sfp-sfpplus1'));
  await settle();

  assert.deepEqual([...collector._ifaces.keys()].sort(), [...NAMES].sort(),
    'a cycle split across the debounce must not truncate the list');

  collector.stop();
});

// The two behaviours the size guard in _commitMeta depends on. It trusts a
// batch that is NOT SMALLER than the last complete cycle, which is what keeps a
// newly added interface instant while making a shrinking list wait for proof.
test('a new interface still appears without waiting for the next cycle', async () => {
  // refreshNow() restarts the meta streams precisely so a freshly created VETH
  // shows up at once rather than a meta interval later. A rule of "only commit
  // on a wrap" would have broken that, silently, and it would have read as a
  // failed save.
  const { EventEmitter } = require('node:events');
  const fake = new EventEmitter(); fake.stop = () => {};
  const ros = { connected: true, on() {}, stream: () => fake };
  const _chain = { emit() {} }; _chain.to = () => _chain;
  const io = { engine: { clientsCount: 1 }, emit() {}, to: () => _chain };
  const collector = new InterfaceStatusCollector({ ros, io, pollMs: 5000, metaPollMs: 60000, state: {} });

  const row = (n) => ({ name: n, type: 'ether', running: 'true', disabled: 'false' });
  const settle = () => new Promise(r => setTimeout(r, 400));

  collector._startIfStream();
  for (const n of ['ether1', 'ether2']) fake.emit('data', row(n));
  await settle();
  assert.equal(collector._ifaces.size, 2);

  // A cycle carrying one MORE interface commits on the debounce, no wrap needed.
  for (const n of ['ether1', 'ether2', 'veth1']) fake.emit('data', row(n));
  await settle();
  assert.ok(collector._ifaces.has('veth1'), 'a bigger cycle must not wait for a wrap');

  collector.stop();
});

test('a deleted interface leaves the list once the cycle proves it is gone', async () => {
  // The inverse, and the cost of the guard: a SMALLER cycle looks exactly like a
  // truncated one, so it is held until a repeated name proves the cycle ended.
  // Worth pinning because "removal is delayed by up to one meta interval" is a
  // deliberate trade, not an oversight.
  const { EventEmitter } = require('node:events');
  const fake = new EventEmitter(); fake.stop = () => {};
  const ros = { connected: true, on() {}, stream: () => fake };
  const _chain = { emit() {} }; _chain.to = () => _chain;
  const io = { engine: { clientsCount: 1 }, emit() {}, to: () => _chain };
  const collector = new InterfaceStatusCollector({ ros, io, pollMs: 5000, metaPollMs: 60000, state: {} });

  const row = (n) => ({ name: n, type: 'ether', running: 'true', disabled: 'false' });
  const settle = () => new Promise(r => setTimeout(r, 400));

  collector._startIfStream();
  for (const n of ['ether1', 'ether2', 'veth1']) fake.emit('data', row(n));
  await settle();
  assert.equal(collector._ifaces.size, 3);

  // veth1 deleted. This cycle is smaller, so it is not trusted on the debounce.
  fake.emit('data', row('ether1'));
  fake.emit('data', row('ether2'));
  await settle();
  assert.ok(collector._ifaces.has('veth1'), 'a smaller batch alone must not drop it');

  // The next cycle repeats ether1, which ends the previous one and commits it.
  fake.emit('data', row('ether1'));
  assert.ok(!collector._ifaces.has('veth1'), 'the wrap must commit the smaller list');
  assert.equal(collector._ifaces.size, 2);

  collector.stop();
});

test('interface status collector clamps malformed throughput fields to zero', () => {
  // The monitor stream's parseBps('bad-data') and parseBps('') both clamp to 0.
  // When no _streamRates entry exists the default {rxMbps:0,txMbps:0} applies.
  const emitted = [];
  const ros = { connected: true, on() {}, stream: () => ({ stop() {} }) };
  const _chain = { emit(ev, data) { emitted.push({ ev, data }); } }; _chain.to = () => _chain;
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); }, to: () => _chain };
  const collector = new InterfaceStatusCollector({ ros, io, pollMs: 5000, state: {} });

  collector._ifaces.set('ether1', { name: 'ether1', running: 'true', disabled: 'false' });
  // No _streamRates entry → defaults to { rxMbps: 0, txMbps: 0 }

  collector._buildAndEmit();

  const iface = emitted[0].data.interfaces[0];
  assert.equal(iface.rxMbps, 0);
  assert.equal(iface.txMbps, 0);
});

// --- Idle gating must not silence alerts (issue #79) ---

test('a router with alerts enabled keeps emitting with no viewers attached', () => {
  // The alerter is fed from the emit path, so an idle-gated collector silences
  // alerts entirely. This stayed invisible because non-active routers were
  // fine — alertSessions stubs clientsCount to 1 — so only the router you were
  // looking at went quiet, and only once you stopped looking at it.
  const mk = (alertsActive) => {
    const emitted = [];
    const ros = { connected: true, on() {}, stream: () => ({ on() {}, stop() {} }),
                  cfg: { host: '10.50.0.1', port: 8728 }, write: async () => [{}] };
    const _chain = { emit() {} }; _chain.to = () => _chain;
    const io = { engine: { clientsCount: 0 }, emit(ev, d) { emitted.push({ ev, d }); }, to: () => _chain };
    const c = new SystemCollector({ ros, io, pollMs: 2000, state: {}, alertsActive });
    return { c, emitted };
  };
  const row = { 'cpu-load': '42', 'total-memory': '1000000', 'free-memory': '500000', version: '7.23.2' };

  const off = mk(() => false);
  off.c._processRow(row);
  assert.equal(off.emitted.length, 0, 'alerts off: the idle gate still suppresses the emit');

  const on = mk(() => true);
  on.c._processRow(row);
  assert.ok(on.emitted.some(e => e.ev === 'system:update'),
    'alerts on: the emit must reach the alerter despite zero viewers');
});

test('alertsActive defaults to off so existing call sites keep the idle behaviour', () => {
  const ros = { connected: true, on() {}, stream: () => ({ on() {}, stop() {} }), write: async () => [] };
  const _chain = { emit() {} }; _chain.to = () => _chain;
  // PingCollector subscribes via io.on() in its constructor, so the stub needs it.
  const io = { to() { return io; }, engine: { clientsCount: 0 }, emit() {}, on() {}, to: () => _chain };

  const ping = new PingCollector({ ros, io, pollMs: 5000, state: {}, target: '1.1.1.1' });
  assert.equal(typeof ping._alertsActive, 'function', 'a predicate, never undefined');
  assert.equal(ping._alertsActive(), false, 'default preserves the old idle behaviour');

  const pingOn = new PingCollector({ ros, io, pollMs: 5000, state: {}, target: '1.1.1.1',
                                     alertsActive: () => true });
  assert.equal(pingOn._alertsActive(), true);
});

// --- RouterOS update check: scheduling and failure reporting ---

// Each harness gets its own host. The update schedule is keyed per router by
// design, so reusing one address would make each test inherit the previous
// test's consumed interval and skip its own fetch.
let _sysHarnessSeq = 0;
function sysHarness(rosOverrides) {
  const emitted = [];
  const calls = [];
  const ros = {
    connected: true, on() {}, stream: () => ({ on() {}, stop() {} }),
    cfg: { host: '10.0.0.' + (++_sysHarnessSeq), port: 8728 },
    write: async (path) => { calls.push(path); return [{}]; },
    ...rosOverrides,
  };
  const _chain = { emit(ev, data) { emitted.push({ ev, data }); } }; _chain.to = () => _chain;
  const io = { to() { return io; }, engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); }, to: () => _chain };
  return { ros, io, calls, emitted,
           collector: new SystemCollector({ ros, io, pollMs: 2000, state: {} }) };
}

test('update check keeps its result when it runs before the first resource tick', async () => {
  // start() fires the check while lastPayload is still null. The apply path
  // used to sit behind `if (this.lastPayload)`, so a cold start consumed the
  // 12 h window and threw the answer away.
  const h = sysHarness({
    write: async (path) => {
      if (path === '/system/package/update/check-for-updates') return [];
      return [{ 'latest-version': '7.23.3', status: 'New version is available' }];
    },
  });
  assert.equal(h.collector.lastPayload, null, 'precondition: no payload yet');

  await h.collector._fetchUpdateStatus();

  assert.equal(h.collector._lastUpdateRow['latest-version'], '7.23.3',
    'row retained even with no payload to merge into');
});

test('a denied check-for-updates is reported instead of silently showing cached state', async () => {
  // /print succeeds on read permission alone, so swallowing the denial left
  // the dashboard presenting stale data as if it were current.
  const h = sysHarness({
    write: async (path) => {
      if (path === '/system/package/update/check-for-updates') throw new Error('not enough permissions (9)');
      return [{ 'latest-version': '7.23.2', status: 'System is already up to date' }];
    },
  });
  h.collector.lastPayload = { ts: 1, version: '7.23.2' };

  await h.collector._fetchUpdateStatus();

  assert.match(h.collector.lastPayload.updateStatus, /unavailable/i, 'status flags the problem');
  assert.match(h.collector.lastPayload.updateStatus, /write permission/i, 'and says how to fix it');
  assert.equal(h.collector.lastPayload.updateAvailable, false);
});

test('update schedule is shared per router so one interval means one upstream call', async () => {
  // SystemCollector is constructed up to three times per router; each start()
  // previously fired its own check-for-updates against MikroTik.
  const shared = { host: '10.9.9.9', port: 8728 };
  const mk = () => {
    const calls = [];
    const ros = { connected: true, on() {}, stream: () => ({ on() {}, stop() {} }), cfg: shared,
                  write: async (p) => { calls.push(p); return [{ 'latest-version': '7.1', status: 'ok' }]; } };
    const _chain = { emit() {} }; _chain.to = () => _chain;
    const io = { engine: { clientsCount: 0 }, emit() {}, to: () => _chain };
    return { calls, collector: new SystemCollector({ ros, io, pollMs: 2000, state: {} }) };
  };
  const a = mk(), b = mk(), c = mk();

  await a.collector._fetchUpdateStatus();
  await b.collector._fetchUpdateStatus();
  await c.collector._fetchUpdateStatus();

  const checks = [...a.calls, ...b.calls, ...c.calls]
    .filter(p => p === '/system/package/update/check-for-updates');
  assert.equal(checks.length, 1, 'exactly one upstream check across three collectors');
});

test('collectors on different routers keep independent update schedules', async () => {
  const mk = (host) => {
    const calls = [];
    const ros = { connected: true, on() {}, stream: () => ({ on() {}, stop() {} }), cfg: { host, port: 8728 },
                  write: async (p) => { calls.push(p); return [{ 'latest-version': '7.1', status: 'ok' }]; } };
    const _chain = { emit() {} }; _chain.to = () => _chain;
    const io = { engine: { clientsCount: 0 }, emit() {}, to: () => _chain };
    return { calls, collector: new SystemCollector({ ros, io, pollMs: 2000, state: {} }) };
  };
  const a = mk('10.1.1.1'), b = mk('10.2.2.2');

  await a.collector._fetchUpdateStatus();
  await b.collector._fetchUpdateStatus();

  assert.ok(a.calls.includes('/system/package/update/check-for-updates'));
  assert.ok(b.calls.includes('/system/package/update/check-for-updates'),
    'a second router must not be blocked by the first router schedule');
});

test('transient update state retries a bounded number of times', async () => {
  // Unbounded, this became a 60 s upstream poll whenever the update server
  // never settled.
  const h = sysHarness({
    write: async (path) => {
      if (path === '/system/package/update/check-for-updates') return [];
      return [{ status: 'finding out latest version...' }];
    },
  });
  const slot = h.collector._updateSlot();

  for (let i = 0; i < 6; i++) {
    slot.lastFetch = 0;           // simulate the retry window elapsing
    await h.collector._fetchUpdateStatus();
  }
  assert.ok(slot.retries <= h.collector.UPDATE_MAX_RETRIES,
    'retries stay capped, got ' + slot.retries);
});

test('update interval follows the configured hours and stays writable', () => {
  const h = sysHarness();
  assert.equal(typeof h.collector.UPDATE_INTERVAL, 'number');
  assert.ok(h.collector.UPDATE_INTERVAL >= 60 * 60 * 1000, 'never below the 1 h floor');
  h.collector.UPDATE_INTERVAL = 1234;
  assert.equal(h.collector.UPDATE_INTERVAL, 1234, 'an explicit assignment still wins (tests rely on it)');
});

// --- Interface Status: cumulative counters for the list view ---

// _commitMeta() reaches _startMonitorStream(), which attaches 'data' and
// 'error' listeners — so unlike the _buildAndEmit-only tests above, this
// harness needs a stream stub that actually behaves like an emitter.
function fakeStream() { return { on() {}, stop() {} }; }

function ifStatusHarness() {
  const emitted = [];
  const ros = { connected: true, on() {}, stream: () => fakeStream() };
  const _chain = { emit(ev, data) { emitted.push({ ev, data }); } }; _chain.to = () => _chain;
  const io = { engine: { clientsCount: 1 }, emit(ev, data) { emitted.push({ ev, data }); }, to: () => _chain };
  const collector = new InterfaceStatusCollector({ ros, io, pollMs: 5000, state: {} });
  // Since issue #108 a tick emits twice: the full payload to the Interfaces,
  // Topology and ports-card rooms, and a names-only summary router-wide for the
  // traffic picker and sidebar badge. Counters, IPs and rates live on the
  // former, so that is what these tests mean by "the emit".
  const updates = () => emitted.filter(e => e.ev === 'ifstatus:update');
  return { collector, emitted, updates, byName: () => {
    const last = updates().slice(-1)[0];
    const out = {};
    for (const i of last.data.interfaces) out[i.name] = i;
    return out;
  } };
}

test('interface status collector reads ethernet PHY error counters, which /interface/print omits for ether', () => {
  // Verified against live hardware: ether rows return tx-queue-drop but none of
  // the rx/tx error counters, so without the /interface/ethernet merge the
  // Errors column would be empty on exactly the ports where cabling faults show.
  const h = ifStatusHarness();
  h.collector._ifaces.set('ether3', { name: 'ether3', type: 'ether', running: 'true', disabled: 'false',
    'rx-byte': '184249905040', 'tx-byte': '5000', 'tx-queue-drop': '6992' });
  h.collector._eth.set('ether3', { name: 'ether3', 'rx-fcs-error': '650', 'rx-align-error': '6', 'tx-late-collision': '0' });

  h.collector._buildAndEmit();

  const i = h.byName().ether3;
  assert.equal(i.errors, 656, 'PHY counters summed');
  assert.equal(i.drops, 6992, 'tx-queue-drop carried through');
  assert.equal(i.rxBytes, 184249905040);
  assert.equal(i.txBytes, 5000);
});

test('interface status collector sums driver error counters for non-ether types', () => {
  const h = ifStatusHarness();
  // A WireGuard peer reports rx/tx-error directly and has no PHY row.
  h.collector._ifaces.set('WG-HOME', { name: 'WG-HOME', type: 'wg', running: 'true', disabled: 'false',
    'rx-error': '3', 'tx-error': '30340', 'rx-drop': '1', 'tx-drop': '2', 'tx-queue-drop': '4' });

  h.collector._buildAndEmit();

  const i = h.byName()['WG-HOME'];
  assert.equal(i.errors, 30343, 'rx-error + tx-error');
  assert.equal(i.drops, 7, 'rx-drop + tx-drop + tx-queue-drop kept separate from errors');
});

test('interface status collector reports null, not zero, for counters an interface does not expose', () => {
  // The distinction the UI depends on: "this driver does not report errors" must
  // not render as a clean bill of health.
  const h = ifStatusHarness();
  h.collector._ifaces.set('ether1', { name: 'ether1', type: 'ether', running: 'true', disabled: 'false' });
  // No _eth entry — the ethernet stream failed or the router has no ether ports.

  h.collector._buildAndEmit();

  const i = h.byName().ether1;
  assert.strictEqual(i.errors, null);
  assert.strictEqual(i.drops, null);
  assert.strictEqual(i.rxBytes, null);
  assert.strictEqual(i.linkDowns, null);
  assert.notStrictEqual(i.errors, 0, 'a missing counter must never collapse to 0');
});

test('interface status collector emits counter movement since the previous metadata tick', () => {
  const h = ifStatusHarness();
  h.collector._ifacesNext.set('ether3', { name: 'ether3', type: 'ether', running: 'true', 'tx-queue-drop': '100' });
  h.collector._eth.set('ether3', { name: 'ether3', 'rx-fcs-error': '650' });
  h.collector._commitMeta();

  let i = h.byName().ether3;
  assert.strictEqual(i.errorsDelta, null, 'no baseline on the first tick, so no delta is claimed');

  h.collector._ifacesNext.set('ether3', { name: 'ether3', type: 'ether', running: 'true', 'tx-queue-drop': '112' });
  h.collector._eth.set('ether3', { name: 'ether3', 'rx-fcs-error': '656' });
  h.collector._commitMeta();

  i = h.byName().ether3;
  assert.equal(i.errors, 656, 'lifetime total still absolute');
  assert.equal(i.errorsDelta, 6, 'movement since the previous tick');
  assert.equal(i.dropsDelta, 12);
  assert.ok(i.deltaWindowMs >= 0, 'window is reported so the rate is interpretable');
});

test('interface status collector treats a counter reset as zero movement, never negative', () => {
  // A reboot or /interface/reset-counters restarts every counter at 0. Reporting
  // that as a large negative delta would be nonsense.
  const h = ifStatusHarness();
  h.collector._ifacesNext.set('ether3', { name: 'ether3', type: 'ether', 'tx-queue-drop': '9000' });
  h.collector._eth.set('ether3', { name: 'ether3', 'rx-fcs-error': '656' });
  h.collector._commitMeta();

  h.collector._ifacesNext.set('ether3', { name: 'ether3', type: 'ether', 'tx-queue-drop': '4' });
  h.collector._eth.set('ether3', { name: 'ether3', 'rx-fcs-error': '0' });
  h.collector._commitMeta();

  const i = h.byName().ether3;
  assert.equal(i.errorsDelta, 0);
  assert.equal(i.dropsDelta, 0);
});

test('interface status collector does not fabricate a delta window on an address-only commit', () => {
  // The address and ethernet streams tick independently and also trigger
  // _commitMeta. Differencing the interface rows against themselves would
  // report a zero-error window that never actually elapsed.
  const h = ifStatusHarness();
  h.collector._ifacesNext.set('ether3', { name: 'ether3', type: 'ether', 'tx-queue-drop': '100' });
  h.collector._commitMeta();
  h.collector._ifacesNext.set('ether3', { name: 'ether3', type: 'ether', 'tx-queue-drop': '150' });
  h.collector._commitMeta();
  assert.equal(h.byName().ether3.dropsDelta, 50);

  // Now a commit driven purely by the address stream — no new counter read.
  h.collector._addrsNext.set('ether3', ['10.0.0.1/24']);
  h.collector._commitMeta();

  const i = h.byName().ether3;
  assert.equal(i.dropsDelta, 50, 'previous delta retained rather than reset to a phantom 0');
  assert.deepEqual(i.ips, ['10.0.0.1/24'], 'address still applied');
});

test('interface status collector drops the counter baseline on reconnect', () => {
  // A reconnect may follow a reboot where counters restarted. Keeping the old
  // baseline would report the drop-to-zero as activity.
  const handlers = {};
  const ros = { connected: true, on(ev, fn) { handlers[ev] = fn; }, stream: () => fakeStream() };
  const _chain = { emit() {} }; _chain.to = () => _chain;
  const io = { engine: { clientsCount: 0 }, emit() {}, to: () => _chain };
  const collector = new InterfaceStatusCollector({ ros, io, pollMs: 5000, state: {} });

  collector._ifacesNext.set('ether3', { name: 'ether3', type: 'ether', 'tx-queue-drop': '9000' });
  collector._commitMeta();
  assert.equal(collector._prevCounters.size, 1);

  handlers.connected();
  assert.equal(collector._prevCounters.size, 0, 'baseline cleared');
  assert.equal(collector._deltas.size, 0);
});

test('interface status collector passes link flap count and last-up time through', () => {
  const h = ifStatusHarness();
  h.collector._ifaces.set('WAN1', { name: 'WAN1', type: 'ether', running: 'true', disabled: 'false',
    'link-downs': '3', 'last-link-up-time': '2026-07-28 22:49:20' });

  h.collector._buildAndEmit();

  const i = h.byName().WAN1;
  assert.strictEqual(i.linkDowns, 3, 'parsed as a number, not the raw string');
  assert.equal(i.lastLinkUp, '2026-07-28 22:49:20');
});

test('interface status fingerprint reacts to error movement but not to byte totals', () => {
  // Byte counters creep up even on an idle link, so including them would defeat
  // the idle-emit suppression entirely. Errors hold steady when healthy, so any
  // movement should push immediately.
  const h = ifStatusHarness();
  h.collector._ifaces.set('ether1', { name: 'ether1', type: 'ether', running: 'true', 'rx-byte': '1000', 'rx-error': '0' });
  h.collector._buildAndEmit();
  const afterFirst = h.updates().length;

  h.collector._ifaces.set('ether1', { name: 'ether1', type: 'ether', running: 'true', 'rx-byte': '2000', 'rx-error': '0' });
  h.collector._buildAndEmit();
  assert.equal(h.updates().length, afterFirst, 'byte growth alone does not force an emit');

  h.collector._ifaces.set('ether1', { name: 'ether1', type: 'ether', running: 'true', 'rx-byte': '3000', 'rx-error': '5' });
  h.collector._buildAndEmit();
  assert.equal(h.updates().length, afterFirst + 1, 'an error appearing does');
});

// --- ARP Collector ---
const ArpCollector = track(require('../src/collectors/arp'));

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

// --- DHCP lease filtering by server / interface / VLAN (#65) ---

// A lease only carries its DHCP server name; the interface and VLAN behind it
// come from /ip/dhcp-server and /interface/vlan. Route each path to its own
// canned response so the join is exercised properly.
function makeLeaseRos(leases, { servers, vlans, failMap } = {}) {
  return {
    connected: true,
    on() {},
    async write(path) {
      if (path === '/ip/dhcp-server/print') {
        if (failMap) throw new Error('no permission');
        return servers || [];
      }
      if (path === '/interface/vlan/print') {
        if (failMap) throw new Error('no permission');
        return vlans || [];
      }
      return leases;
    },
    stream() { return { stop() {} }; },
  };
}

const SERVERS = [
  { name: 'Home DHCP',   interface: 'Home' },
  { name: 'IoT DHCP',    interface: 'IoT' },
  { name: 'Office DHCP', interface: 'ether3' },   // plain interface, no VLAN
];
const VLANS = [
  { name: 'Home', 'vlan-id': '5' },
  { name: 'IoT',  'vlan-id': '10' },
];
const LEASES = [
  { address: '10.0.0.5',  'mac-address': 'AA:01', 'host-name': 'desktop', server: 'Home DHCP',   status: 'bound' },
  { address: '10.0.0.6',  'mac-address': 'AA:02', 'host-name': 'nas',     server: 'Home DHCP',   status: 'bound' },
  { address: '10.0.10.5', 'mac-address': 'AA:03', 'host-name': 'bulb',    server: 'IoT DHCP',    status: 'bound' },
  { address: '10.0.20.5', 'mac-address': 'AA:04', 'host-name': 'printer', server: 'Office DHCP', status: 'bound' },
];

test('dhcp leases resolve their server interface and VLAN id', async () => {
  const emitted = [];
  const ros = makeLeaseRos(LEASES, { servers: SERVERS, vlans: VLANS });
  const collector = new DhcpLeasesCollector({
    ros, io: { emit: (ev, p) => { if (ev === 'leases:list') emitted.push(p); } }, state: {},
  });
  await collector.start();

  const byIp = Object.fromEntries(emitted[emitted.length - 1].leases.map(l => [l.ip, l]));
  assert.equal(byIp['10.0.0.5'].server, 'Home DHCP');
  assert.equal(byIp['10.0.0.5'].iface, 'Home');
  assert.equal(byIp['10.0.0.5'].vlanId, '5');
  assert.equal(byIp['10.0.10.5'].vlanId, '10');
  // A server on a plain interface has an interface but no VLAN.
  assert.equal(byIp['10.0.20.5'].iface, 'ether3');
  assert.equal(byIp['10.0.20.5'].vlanId, '', 'non-VLAN interface must not invent a VLAN id');
});

test('leases:list carries a server summary with per-server counts', async () => {
  const emitted = [];
  const ros = makeLeaseRos(LEASES, { servers: SERVERS, vlans: VLANS });
  const collector = new DhcpLeasesCollector({
    ros, io: { emit: (ev, p) => { if (ev === 'leases:list') emitted.push(p); } }, state: {},
  });
  await collector.start();

  const servers = emitted[emitted.length - 1].servers;
  assert.equal(servers.length, 3);
  // Sorted by count descending, so the busiest server leads the dropdown.
  assert.equal(servers[0].name, 'Home DHCP');
  assert.equal(servers[0].count, 2);
  assert.equal(servers[0].vlanId, '5');
  const office = servers.find(s => s.name === 'Office DHCP');
  assert.equal(office.count, 1);
  assert.equal(office.iface, 'ether3');
  assert.equal(office.vlanId, '');
  assert.equal(servers.reduce((a, s) => a + s.count, 0), 4, 'counts account for every lease');
});

test('a server whose config could not be read still appears, without interface or VLAN', async () => {
  const emitted = [];
  // Lease references a server missing from /ip/dhcp-server (added since connect).
  const leases = LEASES.concat([
    { address: '10.0.30.5', 'mac-address': 'AA:05', 'host-name': 'new', server: 'Lab DHCP', status: 'bound' },
  ]);
  const ros = makeLeaseRos(leases, { servers: SERVERS, vlans: VLANS });
  const collector = new DhcpLeasesCollector({
    ros, io: { emit: (ev, p) => { if (ev === 'leases:list') emitted.push(p); } }, state: {},
  });
  await collector.start();

  const lab = emitted[emitted.length - 1].servers.find(s => s.name === 'Lab DHCP');
  assert.ok(lab, 'unknown server is still offered as a filter option');
  assert.equal(lab.count, 1);
  assert.equal(lab.iface, '');
  assert.equal(lab.vlanId, '');
});

test('leases still load when the server/VLAN lookup fails', async () => {
  const emitted = [];
  const ros = makeLeaseRos(LEASES, { failMap: true });
  const collector = new DhcpLeasesCollector({
    ros, io: { emit: (ev, p) => { if (ev === 'leases:list') emitted.push(p); } }, state: {},
  });
  await collector.start();

  const payload = emitted[emitted.length - 1];
  assert.equal(payload.leases.length, 4, 'lease table is unaffected by the failed join');
  // Degrades to server-only filtering rather than losing the filter entirely.
  assert.equal(payload.leases[0].server, 'Home DHCP');
  assert.equal(payload.leases[0].iface, '');
  assert.equal(payload.servers.length, 3);
});

// --- DHCP Networks Collector ---
const DhcpNetworksCollector = track(require('../src/collectors/dhcpNetworks'));

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
  const io = { to() { return io; }, emit(ev, data) { emitted.push({ ev, data }); } };
  const leases = {
    getInUseLeaseIPs: () => ['192.168.1.10', '192.168.1.11', '10.0.0.5'],
  };
  const collector = new DhcpNetworksCollector({ ros, io, pollMs: 15000, dhcpLeases: leases, state: {}, wanIface: 'WAN1' });
  await collector._fetchOnce();

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
  const io = { to() { return io; }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new DhcpNetworksCollector({ ros, io, pollMs: 15000, dhcpLeases: { getInUseLeaseIPs: () => [] }, state: {}, wanIface: 'WAN1' });
  await collector._fetchOnce();

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
  const io = { to() { return io; }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new DhcpNetworksCollector({ ros, io, pollMs: 15000, dhcpLeases: { getInUseLeaseIPs: () => [] }, state, wanIface: 'WAN1' });
  await collector._fetchOnce();

  assert.equal(emitted[0].data.wanIp, '');
  assert.equal(state.lastWanIp, '');
});

// ═══════════════════════════════════════════════════════════════════════════
// --- Routing Collector ---
// ═══════════════════════════════════════════════════════════════════════════
const RoutingCollector = track(require('../src/collectors/routing'));
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

// ── BGP-only mode, for the headless alert pool ───────────────────────────────
//
// BGP alerts used to fire only for the router whose Routing page someone had
// open, because the alert pool (src/alertSessions.js) built System, Ping,
// InterfaceStatus, Vpn and Netwatch but no routing collector. Every other
// router produced no BGP alerts at all — an alert type that reads as enabled in
// Settings and silently does nothing for most of the fleet.
//
// The pool only needs peers: the evaluator reads data.peers and nothing else.
// Running the collector as configured would open /ip/route/listen and
// /ipv6/route/listen on every alert-enabled router and pull full route tables
// for a payload nobody renders, which is exactly the load this avoids.

// Like makeRoutingRos, but records what was asked for.
function makeRecordingRoutingRos({ printRows = [], sessionRows = [], peerCfgRows = [] } = {}) {
  const writes = [];
  const streams = [];
  return {
    writes, streams,
    connected: true,
    on() {},
    write: async (cmd) => {
      writes.push(String(cmd));
      if (String(cmd).includes('/routing/bgp/session')) return sessionRows;
      if (String(cmd).includes('/routing/bgp/peer'))    return peerCfgRows;
      if (String(cmd).includes('/ip/route'))            return printRows;
      return [];
    },
    stream: (words) => { streams.push(String(words)); return { stop() {} }; },
  };
}

const _bgpOnlyRos = () => makeRecordingRoutingRos({
  printRows:   [{ '.id': '*1', 'dst-address': '0.0.0.0/0', gateway: '10.0.0.1', distance: '1', '.flags': 'AS' }],
  sessionRows: [{ name: 'peer1', 'remote.address': '10.0.0.1', 'remote.as': '65001',
                  state: 'established', uptime: '1h', 'prefix-count': '100' }],
});

test('bgpOnly never reads the route table, but still reads BGP sessions', async () => {
  const ros = _bgpOnlyRos();
  const io  = { to() { return { emit() {} }; } };
  const c = new RoutingCollector({ ros, io, pollMs: 10000, state: {}, streamMode: false, bgpOnly: true });

  await c.resume();

  assert.ok(!ros.writes.some(w => w.includes('/ip/route')),
    'no route query: ' + JSON.stringify(ros.writes));
  assert.ok(ros.writes.some(w => w.includes('/routing/bgp/session')),
    'BGP sessions are still read — they are the whole point');
  assert.strictEqual(c.lastPayload.peers.length, 1, 'peers reach the payload the evaluator reads');
  assert.strictEqual(c.lastPayload.peers[0].state, 'established');
  c.stop();
});

test('bgpOnly opens the BGP stream and neither route stream', async () => {
  // Guarded independently of poll mode. The pool uses polling, but a flag that
  // only works in one delivery mode is a trap for whoever wires the next caller.
  const ros = _bgpOnlyRos();
  const io  = { to() { return { emit() {} }; } };
  const c = new RoutingCollector({ ros, io, pollMs: 10000, state: {}, bgpOnly: true });

  await c.resume();

  assert.ok(!ros.streams.some(s => s.includes('/ip/route/listen')),   'no IPv4 route stream');
  assert.ok(!ros.streams.some(s => s.includes('/ipv6/route/listen')), 'no IPv6 route stream');
  assert.ok(ros.streams.some(s => s.includes('/routing/bgp/session/listen')), 'BGP stream still opens');
  c.stop();
});

test('without bgpOnly the route table is still loaded', async () => {
  // The flag defaults off, and the Routing page depends on it staying that way.
  const ros = _bgpOnlyRos();
  const io  = { to() { return { emit() {} }; } };
  const c = new RoutingCollector({ ros, io, pollMs: 10000, state: {}, streamMode: false });

  await c.resume();

  assert.ok(ros.writes.some(w => w.includes('/ip/route')), 'routes are read as normal');
  assert.ok(c.lastPayload.routeCounts.total > 0, 'and counted');
  c.stop();
});

// ── start() happy path ───────────────────────────────────────────────────────

test('routing collector start() emits correct payload with routes and BGP sessions', async () => {
  const emitted = [];
  // Routing collector emits via io.to('page-routing').emit(...); resume() loads data
  const io = { to(room) { return { emit(ev, d) { emitted.push({ ev, data: d }); } }; } };
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
  await collector.resume();

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
  const io = { to(room) { return { emit(ev, d) { emitted.push({ ev, data: d }); } }; } };
  const collector = new RoutingCollector({ ros, io, pollMs: 10000, state: {} });
  await collector.resume();
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
  const io = { to(room) { return { emit(ev, d) { emitted.push(d); } }; } };
  const collector = new RoutingCollector({ ros, io, pollMs: 10000, state: {} });
  await collector.resume();

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
  const io = { to(room) { return { emit(ev, d) { emitted.push(d); } }; } };
  const collector = new RoutingCollector({ ros, io, pollMs: 10000, state: {} });
  await collector.resume();

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
  const io = { to(room) { return { emit(ev, d) { emitted.push(d); } }; } };
  const collector = new RoutingCollector({ ros, io, pollMs: 10000, state: {} });
  await collector.resume();

  bgpCb(null, { name: 'p1', 'remote.address': '10.0.0.1', state: 'established', 'prefix-count': '50' });
  await new Promise(r => setTimeout(r, 10));
  assert.equal(collector._sessions.size, 1);

  bgpCb(null, { name: 'p1', 'remote.address': '10.0.0.1', '.dead': 'true' });
  await new Promise(r => setTimeout(r, 10));
  assert.equal(collector._sessions.size, 0, 'session removed on .dead=true');
});

// ── Route stream delta ────────────────────────────────────────────────────────

// A static route and a connected one, so routeCounts has something in more
// than one bucket to count.
const ROUTE_ROWS = [
  { '.id': '*1', 'dst-address': '0.0.0.0/0',   gateway: '1.2.3.1', distance: '1', '.flags': 'AS' },
  { '.id': '*2', 'dst-address': '10.0.0.0/24', gateway: 'bridge',  distance: '0', '.flags': 'AC' },
];

test('a route crosses the wire without its internals', async () => {
  // The destructure named `_flags` and _mapRoute calls the field `flags`, so
  // the exclusion silently did nothing and every route carried its whole flags
  // object. Nothing renders it: `active`, `type` and `protocol` are derived
  // from those flags server-side and are what the table shows.
  const emitted = [];
  const io = { to(room) { return { emit(ev, d) { emitted.push(d); } }; } };
  const collector = new RoutingCollector({ ros: makeRoutingRos({ printRows: ROUTE_ROWS }),
                                           io, pollMs: 10000, state: {} });
  await collector._loadRoutes();
  collector._emit(null);

  const route = emitted[0].routes[0];
  assert.ok(route, 'a route to inspect');
  for (const internal of ['flags', '_flags', '_raw', '_id']) {
    assert.ok(!(internal in route), internal + ' must not cross the wire');
  }
  // And what the page does render is still all there.
  for (const shown of ['id', 'dst', 'gateway', 'distance', 'active',
                       'comment', 'type', 'protocol', 'family']) {
    assert.ok(shown in route, shown + ' is rendered and must survive the projection');
  }
});

test('stripping flags from the wire leaves the route counts intact', async () => {
  // routeCounts is computed from the STORED routes, which keep their flags —
  // a fix that reached into those would empty the summary instead.
  const emitted = [];
  const io = { to(room) { return { emit(ev, d) { emitted.push(d); } }; } };
  const collector = new RoutingCollector({ ros: makeRoutingRos({ printRows: ROUTE_ROWS }),
                                           io, pollMs: 10000, state: {} });
  await collector._loadRoutes();
  collector._emit(null);

  const c = emitted[0].routeCounts;
  assert.ok(c.total > 0, 'routes are still counted');
  for (const k of ['connect', 'static', 'dynamic', 'bgp', 'ospf']) {
    assert.equal(typeof c[k], 'number', k + ' must still be counted');
  }
});

test('routing collector route stream delta adds new route', async () => {
  const emitted = [];
  const io = { to(room) { return { emit(ev, d) { emitted.push(d); } }; } };
  const collector = new RoutingCollector({ ros: makeRoutingRos(), io, pollMs: 10000, state: {} });
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
  const io = { to(room) { return { emit(ev, d) { emitted.push(d); } }; } };
  const collector = new RoutingCollector({ ros, io, pollMs: 10000, state: {} });
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
  const io = { to(room) { return { emit(ev, d) { emitted.push(d); } }; } };
  const collector = new RoutingCollector({ ros, io, pollMs: 10000, state: {} });
  await collector.resume();

  // Route stream event fires — reuses BGP peers from lastPayload
  collector._applyRouteDelta({ '.id': '*1', 'dst-address': '1.0.0.0/8', gateway: '10.0.0.1', distance: '1', '.flags': 'AS' });
  collector._emit(null);

  assert.equal(emitted[emitted.length - 1].peers.length, 1, 'last known peers reused');
});

test('routing collector _emit(null) before any peers returns empty array', async () => {
  const emitted = [];
  const io = { to(room) { return { emit(ev, d) { emitted.push(d); } }; } };
  const collector = new RoutingCollector({ ros: makeRoutingRos(), io, pollMs: 10000, state: {} });
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
  const io = { to(room) { return { emit(ev, d) { emitted.push(d); } }; } };
  const collector = new RoutingCollector({ ros, io, pollMs: 10000, state: {} });
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
  const io = { to(room) { return { emit(ev, d) { emitted.push(d); } }; } };
  const collector = new RoutingCollector({ ros, io, pollMs: 10000, state: {} });
  await collector._loadRoutes(); collector._emit(null);
  await collector._loadRoutes(); collector._emit(null);
  assert.equal(emitted[0].routes.length, 0, 'tick 1 (no .flags): interface route excluded');
  assert.equal(emitted[1].routes.length, 0, 'tick 2 (.flags=AC): interface route excluded');
});

// ── Route counts ──────────────────────────────────────────────────────────────

test('routing collector counts all route protocol types correctly', async () => {
  const emitted = [];
  const io = { to(room) { return { emit(ev, d) { emitted.push(d); } }; } };
  const collector = new RoutingCollector({ ros: makeRoutingRos(), io, pollMs: 10000, state: {} });
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
  const io = { to(room) { return { emit(ev, d) { emitted.push(d); } }; } };
  const collector = new RoutingCollector({ ros: makeRoutingRos(), io, pollMs: 10000, state });
  await collector.resume();
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
  const io = { to(room) { return { emit(ev, d) { emitted.push(d); } }; } };
  const collector = new RoutingCollector({ ros, io, pollMs: 10000, state: {} });
  await collector.resume();
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
  const io = { to(room) { return { emit(ev, d) { emitted.push(d); } }; } };
  const collector = new RoutingCollector({ ros, io, pollMs: 10000, state: {} });
  await collector.resume();
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
  const io = { to(room) { return { emit(ev, d) { emitted.push(d); } }; } };
  const collector = new RoutingCollector({ ros, io, pollMs: 10000, state: {} });
  await collector.resume();
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
  const io = { to(room) { return { emit(ev, d) { emitted.push(d); } }; } };
  const collector = new RoutingCollector({ ros, io, pollMs: 10000, state: {} });
  await collector.resume();
  const d = emitted[emitted.length - 1];
  assert.equal(d.peers.length, 1);
  assert.equal(d.peers[0].remoteAs, 65002);
});

test('routing collector sets pollMs=0 to signal stream-based delivery', async () => {
  const emitted = [];
  const io = { to(room) { return { emit(ev, d) { emitted.push(d); } }; } };
  const collector = new RoutingCollector({ ros: makeRoutingRos(), io, pollMs: 15000, state: {} });
  await collector.resume();
  assert.equal(emitted[0].pollMs, 0);
});

// ── Wireless: proplist removal fixes single-client bug ───────────────────────

test('wireless collector returns all clients without =.proplist= restriction', () => {
  const WirelessCollector = require('../src/collectors/wireless');
  const emitted = [];
  const collector = new WirelessCollector({
    ros: { connected: true, on() {} },
    io: { to() { return this; }, emit(ev, d) { emitted.push({ ev, data: d }); } }, pollMs: 5000, state: {},
    dhcpLeases: { getNameByMAC: () => null },
    arp: { getByMAC: () => null },
  });
  collector._onBatch('wifi', [
    { 'mac-address': 'AA:01', 'signal-strength': '-55', interface: 'wifi1', band: '5ghz', uptime: '1h' },
    { 'mac-address': 'AA:02', 'signal-strength': '-65', interface: 'wifi1', band: '5ghz', uptime: '30m' },
    { 'mac-address': 'AA:03', 'signal-strength': '-70', interface: 'wifi2', band: '2.4ghz', uptime: '15m' },
  ]);
  assert.equal(wlEmits(emitted)[0].data.clients.length, 3, 'all 3 clients present');
  assert.equal(collector.mode, 'wifi');
});

test('wireless collector stream commands contain no =.proplist= restriction', () => {
  const WirelessCollector = require('../src/collectors/wireless');
  const streamCalls = [];
  const ros = {
    connected: true, on() {},
    stream: (words) => { streamCalls.push(words); return { stop() {}, on() {} }; },
  };
  const collector = new WirelessCollector({
    ros, io: { to() { return this; }, emit() {} }, pollMs: 5000, state: {},
    dhcpLeases: null, arp: null,
  });
  collector._startStream('wifi');
  assert.equal(streamCalls.length, 1);
  const hasProplst = streamCalls[0].some(p => String(p).includes('.proplist'));
  assert.ok(!hasProplst, 'no .proplist in wifi stream command');
});

test('wireless collector resolves name after DHCP loads without a new RouterOS call', async () => {
  // The collector schedules a 500ms retry that re-resolves names from the already-
  // held client list WITHOUT making any new RouterOS API call.
  const emitted = [];
  let leasesReady = false;
  const ros = { connected: true, on() {} };
  const io = { to() { return io; }, emit(ev, data) { emitted.push({ ev, data }); } };
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

  // First batch — DHCP not ready, both clients have empty names
  collector._onBatch('wifi', [
    { 'mac-address': 'AA:BB', signal: '-50', interface: 'wifi1' },
    { 'mac-address': 'CC:DD', signal: '-60', interface: 'wifi1' },
  ]);
  assert.equal(wlEmits(emitted).length, 1, 'first batch emits');
  assert.equal(wlEmits(emitted)[0].data.clients.length, 2, 'all clients present on first emit');
  assert.equal(wlEmits(emitted)[0].data.clients[0].name, '', 'names empty before DHCP loads');
  assert.ok(collector._retryTimer, 'retry timer scheduled');

  // DHCP now available — retry fires within 500ms without a new RouterOS call
  leasesReady = true;
  await new Promise(r => setTimeout(r, 600));

  assert.equal(wlEmits(emitted).length, 2, 'retry emits updated names');
  assert.equal(wlEmits(emitted)[1].data.clients.length, 2, 'all clients still present after retry');
  assert.equal(wlEmits(emitted)[1].data.clients[0].name, 'Laptop', 'first client name resolved');
  assert.equal(wlEmits(emitted)[1].data.clients[1].name, 'Phone', 'second client name resolved');
  assert.equal(collector._retryTimer, null, 'retry stops once all names resolved');
});

// ── BGP flap detection ────────────────────────────────────────────────────────

test('routing collector flapping is false when peer state is stable', () => {
  const collector = new RoutingCollector({ ros: makeRoutingRos(), io: { emit() {} }, pollMs: 10000, state: {} });
  collector._sessions.set('10.0.0.1', { name: 'p1', 'remote.address': '10.0.0.1', 'remote.as': '65001', state: 'established' });
  const peers = collector._buildPeers();
  assert.equal(peers[0].flapping, false);
});

test('routing collector flapping is false after only two state changes', () => {
  const collector = new RoutingCollector({ ros: makeRoutingRos(), io: { emit() {} }, pollMs: 10000, state: {} });
  const session = { name: 'p1', 'remote.address': '10.0.0.1', 'remote.as': '65001', state: 'established' };
  collector._sessions.set('10.0.0.1', session);
  collector._buildPeers();                          // initial — records state
  session.state = 'active';       collector._buildPeers(); // change 1
  session.state = 'established';
  const peers = collector._buildPeers();            // change 2 — threshold is 3
  assert.equal(peers[0].flapping, false);
});

test('routing collector flapping is true after three state changes within the window', () => {
  const collector = new RoutingCollector({ ros: makeRoutingRos(), io: { emit() {} }, pollMs: 10000, state: {} });
  const session = { name: 'p1', 'remote.address': '10.0.0.1', 'remote.as': '65001', state: 'established' };
  collector._sessions.set('10.0.0.1', session);
  collector._buildPeers();
  session.state = 'active';       collector._buildPeers(); // change 1
  session.state = 'established';  collector._buildPeers(); // change 2
  session.state = 'active';
  const peers = collector._buildPeers();                   // change 3 → flapping
  assert.equal(peers[0].flapping, true);
});

test('routing collector flap window prunes stale entries older than 5 minutes', () => {
  const collector = new RoutingCollector({ ros: makeRoutingRos(), io: { emit() {} }, pollMs: 10000, state: {} });
  const session = { name: 'p1', 'remote.address': '10.0.0.1', 'remote.as': '65001', state: 'established' };
  collector._sessions.set('10.0.0.1', session);
  collector._buildPeers();
  // Inject two stale flapWindow entries (> 5 min old) representing prior changes
  const staleTs = Date.now() - 6 * 60 * 1000;
  collector._peerState.set('10.0.0.1', { lastState: 'established', lastChange: staleTs, flapWindow: [staleTs, staleTs] });
  // One more state change — stale entries pruned, leaving only 1 recent entry
  session.state = 'active';
  const peers = collector._buildPeers();
  assert.equal(peers[0].flapping, false, 'stale window entries pruned; only 1 recent change remains');
  assert.equal(collector._peerState.get('10.0.0.1').flapWindow.length, 1, 'flapWindow retains only the recent entry');
});

test('routing collector peerState is pruned when peer disappears from sessions', () => {
  const collector = new RoutingCollector({ ros: makeRoutingRos(), io: { emit() {} }, pollMs: 10000, state: {} });
  const session = { name: 'p1', 'remote.address': '10.0.0.1', 'remote.as': '65001', state: 'established' };
  collector._sessions.set('10.0.0.1', session);
  // Trigger flapping
  session.state = 'active';       collector._buildPeers();
  session.state = 'established';  collector._buildPeers();
  session.state = 'active';       collector._buildPeers();
  assert.ok(collector._peerState.has('10.0.0.1'), 'peerState present while peer is live');
  // Remove peer; next buildPeers should prune the stale entry
  collector._sessions.clear();
  collector._buildPeers();
  assert.ok(!collector._peerState.has('10.0.0.1'), 'peerState pruned after peer removed from sessions');
});

// --- VPN: peer liveness, PPP and IPsec (#64) ---

// The bug this replaces: state was "has this peer ever handshaken", so a peer
// that vanished days ago still counted as connected — while the UI graded the
// same value by age and drew it red. State is now derived from age.
test('vpn peer state is derived from handshake age, not mere existence', () => {
  assert.equal(VpnCollector.peerState('30s'),   'active');
  assert.equal(VpnCollector.peerState('2m59s'), 'active', 'just inside the 3 minute rekey window');
  assert.equal(VpnCollector.peerState('3m1s'),  'stale',  'just outside it');
  assert.equal(VpnCollector.peerState('3d4h'),  'stale',  'gone for days is not connected');
  assert.equal(VpnCollector.peerState('never'), 'never');
  assert.equal(VpnCollector.peerState(''),      'never');
  // The old rule would have called every one of these connected.
  const oldRule = (lh) => !!(lh && lh !== 'never');
  assert.equal(oldRule('3d4h'), true, 'old rule counted a 3-day-old peer as connected');
  assert.notEqual(VpnCollector.peerState('3d4h'), 'active');
});

test('vpn handshake age parses RouterOS duration strings', () => {
  assert.equal(VpnCollector.handshakeAgeSec('45s'), 45);
  assert.equal(VpnCollector.handshakeAgeSec('2m30s'), 150);
  assert.equal(VpnCollector.handshakeAgeSec('1h5m20s'), 3920);
  assert.equal(VpnCollector.handshakeAgeSec('3d4h'), 273600);
  assert.equal(VpnCollector.handshakeAgeSec('never'), Infinity);
  assert.equal(VpnCollector.handshakeAgeSec(''), Infinity);
});

test('vpn parses PPP sessions, which carry a real uptime', () => {
  const rows = [
    { name: 'roadwarrior', service: 'l2tp', address: '10.20.0.2', uptime: '5m12s',
      'caller-id': '203.0.113.9', 'bytes-in': '1024', 'bytes-out': '2048' },
    { name: 'branch', service: 'sstp', address: '10.20.0.3', uptime: '2h1m' },
    {},                                   // sentinel row from the API
  ];
  const out = VpnCollector.parsePppSessions(rows);
  assert.equal(out.length, 2, 'empty sentinel rows dropped');
  assert.equal(out[0].type, 'PPP');
  assert.equal(out[0].service, 'L2TP');
  assert.equal(out[0].uptime, '5m12s', 'a genuine session uptime, unlike WireGuard');
  assert.equal(out[0].rx, 1024);
  assert.equal(out[0].tx, 2048);
  assert.equal(out[1].rx, 0, 'missing counters default to zero rather than NaN');
});

test('vpn joins IPsec peers to their SA ciphers', () => {
  const peers = [
    { 'remote-address': '203.0.113.9', state: 'established', uptime: '10m', side: 'responder' },
    { 'remote-address': '198.51.100.4', state: 'established', uptime: '1m' },
  ];
  const sas = [
    { 'dst-address': '203.0.113.9/32', 'enc-algorithm': 'aes-256-cbc', 'auth-algorithm': 'sha256' },
  ];
  const out = VpnCollector.parseIpsecPeers(peers, sas);
  assert.equal(out.length, 2);
  assert.equal(out[0].name, '203.0.113.9');
  assert.equal(out[0].enc, 'aes-256-cbc', 'encryption details come from the SA, not the peer');
  assert.equal(out[0].auth, 'sha256');
  assert.equal(out[1].enc, '', 'a peer with no matching SA degrades rather than throwing');
  assert.deepEqual(VpnCollector.parseIpsecPeers(null, null), []);
});

test('vpn backs off polling once PPP and IPsec come back empty', async () => {
  const calls = [];
  const ros = { connected: true, on() {}, stream() { return { stop() {} }; },
    async write(path) { calls.push(path); return []; } };
  const collector = new VpnCollector({ ros, io: { to() { return { to() { return { emit() {} }; } }; } }, pollMs: 10000, state: {} });

  await collector._loadOtherVpns();
  assert.equal(collector._ppp.length, 0);
  assert.equal(collector._ipsec.length, 0);
  assert.ok(VpnCollector.OTHER_IDLE_MS > VpnCollector.OTHER_ACTIVE_MS,
    'idle cadence must be slower than the active one');

  // Suspend must leave no timer behind, or the idle gate is defeated.
  collector._scheduleOtherVpns(50000);
  assert.ok(collector._otherTimer, 'timer scheduled');
  collector.suspend();
  assert.equal(collector._otherTimer, null, 'suspend cancels the poll timer');
  collector.stop();
});

test('vpn stops probing a router that has neither subsystem', async () => {
  const ros = { connected: true, on() {}, stream() { return { stop() {} }; },
    async write() { throw new Error('no such command or directory (ppp)'); } };
  const collector = new VpnCollector({ ros, io: { to() { return { to() { return { emit() {} }; } }; } }, pollMs: 10000, state: {} });

  await collector._loadOtherVpns();
  assert.equal(collector._pppAvailable, false, 'latched off after "no such command"');
  assert.equal(collector._ipsecAvailable, false);
  collector._scheduleOtherVpns(0);
  assert.equal(collector._otherTimer, null, 'no timer scheduled when nothing is supported');
  collector.stop();
});

// Regression for CodeQL js/tainted-format-string (alert #130, system.js:206).
// console.* treats its first argument as a format string. The label embeds the
// user-set router name, so concatenating it into position 0 meant a router
// named with a "%s" consumed the error message — the failure reason vanished
// from the log entirely, which is worse than the "garbled output" the rule
// describes.
test('system collector logs a %s-bearing router label literally and keeps the error', async () => {
  const _chain = { emit() {} }; _chain.to = () => _chain;
  const io = { engine: { clientsCount: 1 }, emit() {}, to: () => _chain };
  // Unique host: _updateSchedule is module-level and keyed 'host:port'.
  const ros = {
    connected: true, on() {}, host: '203.0.113.77', port: 8729,
    write: () => Promise.reject(new Error('ECONNREFUSED')),
  };
  ros.routerLabel = '%s pwned';

  const collector = new SystemCollector({ ros, io, pollMs: 5000, state: {} });
  collector._updateIntervalOverride = 0;   // don't wait out the 12 h window

  const util = require('node:util');
  const orig = console.error;
  const lines = [];
  console.error = (...a) => lines.push(util.format(...a));
  try { await collector._fetchUpdateStatus(); } finally { console.error = orig; }

  const line = lines.find(l => l.includes('update check failed'));
  assert.ok(line, 'the failure was logged at all');
  assert.ok(line.includes('[%s pwned][system]'), 'label rendered literally, got: ' + line);
  assert.ok(line.includes('ECONNREFUSED'), 'error reason survived the format, got: ' + line);
});

// ── Wireless: the SSID list (WiFi SSIDs card) ────────────────────────────────
//
// Read from the interface table rather than from connected clients, because an
// SSID with nobody on it is still being broadcast — and that is precisely the
// one somebody is looking at the card to explain.
//
// The rows these come from also carry `security.passphrase` in clear text. The
// payload goes to every browser on the Wireless page, so what is NOT copied out
// matters as much as what is.

const _wlCollector = () => new WirelessCollector({
  ros: { connected: true, on() {}, write: async () => [] },
  io: { emit() {}, to() { return this; } },
  pollMs: 5000, state: {},
  dhcpLeases: { getNameByMAC: () => null },
  arp: { getByMAC: () => null },
});

test('SSIDs come from the interface list, and the same SSID on two radios is one entry', () => {
  const c = _wlCollector();
  const { ssids } = c._parseSsids([
    { name: '2.4GHz WiFi', 'configuration.ssid': 'SkyNet', running: 'true', disabled: 'false' },
    { name: '5GHz WiFi',   'configuration.ssid': 'SkyNet', running: 'true', disabled: 'false' },
    { name: 'Guest 2.4',   'configuration.ssid': 'Guests', running: 'true', disabled: 'false' },
  ]);
  assert.strictEqual(ssids.length, 2, 'one row per network, not per radio');
  const sky = ssids.find(s => s.ssid === 'SkyNet');
  assert.deepStrictEqual(sky.ifaces, ['2.4GHz WiFi', '5GHz WiFi']);
});

test('a passphrase never leaves the collector', () => {
  // The one assertion in this file that is about a leak rather than a value.
  const c = _wlCollector();
  const { ssids } = c._parseSsids([{
    name: 'wifi1', 'configuration.ssid': 'SkyNet', running: 'true', disabled: 'false',
    'security.passphrase': 'hunter2', 'security.authentication-types': 'wpa2-psk',
  }]);
  const blob = JSON.stringify(ssids);
  assert.ok(!blob.includes('hunter2'), 'the passphrase is not in the payload');
  assert.ok(!blob.includes('security'), 'no security field is copied through at all');
});

test('the legacy wireless stack uses a plain ssid field', () => {
  // /interface/wireless predates the flattened configuration.* keys.
  const c = _wlCollector();
  const { ssids } = c._parseSsids([
    { name: 'wlan1', ssid: 'OldSkool', running: 'true', disabled: 'false' },
  ]);
  assert.strictEqual(ssids[0].ssid, 'OldSkool');
});

test('an SSID is only off when every radio carrying it is', () => {
  // One radio up is enough to be on the air; reporting it as disabled because
  // its partner is down would send somebody debugging a working network.
  const c = _wlCollector();
  const { ssids } = c._parseSsids([
    { name: 'a', 'configuration.ssid': 'Split', running: 'false', disabled: 'true' },
    { name: 'b', 'configuration.ssid': 'Split', running: 'true',  disabled: 'false' },
  ]);
  assert.strictEqual(ssids[0].disabled, false);
  assert.strictEqual(ssids[0].running, true);

  const both = c._parseSsids([
    { name: 'a', 'configuration.ssid': 'Dead', running: 'false', disabled: 'true' },
    { name: 'b', 'configuration.ssid': 'Dead', running: 'false', disabled: 'true' },
  ]).ssids[0];
  assert.strictEqual(both.disabled, true);
  assert.strictEqual(both.running, false);
});

test('a CAPsMAN-managed radio is counted, not silently dropped', () => {
  // A CAP takes its configuration from the manager and genuinely has no local
  // SSID. Counting these lets the card explain an empty list instead of looking
  // like a router with no wireless.
  const c = _wlCollector();
  const r = c._parseSsids([
    { name: 'wifi1', 'configuration.manager': 'capsman', running: 'true', disabled: 'false' },
    { name: 'wifi2', 'configuration.manager': 'capsman', running: 'true', disabled: 'false' },
  ]);
  assert.deepStrictEqual(r.ssids, []);
  assert.strictEqual(r.managedElsewhere, 2);
});

// ── Bands and counts are live, the SSID list is not ──────────────────────────
//
// Reported as "the SSIDs show up, but the band labels are not showing and the
// client counts are empty".
//
// Two cadences meet in one payload. The SSID list is configuration, refreshed
// every 5 minutes. Bands and client counts come from the registration table,
// which streams. Folding the second into the first at refresh time froze them
// at whatever the client table held during that refresh — and at startup the
// refresh runs before the first client batch has arrived, so every SSID was
// published with no bands and a count of zero, and stayed that way for the rest
// of the 5-minute cycle.

test('client counts follow the registration table, not the 5-minute SSID refresh', () => {
  const c = _wlCollector();
  // The startup ordering: the list is fetched while nobody is associated.
  c._ssids = c._parseSsids([
    { name: 'wifi1', 'configuration.ssid': 'SkyNet', running: 'true', disabled: 'false' },
  ]).ssids;
  assert.strictEqual(c._ssids[0].clients, 0, 'precondition: nobody was connected yet');

  // A client associates. This arrives on the stream, not on the SSID timer.
  c._knownClients.set('AA', { mac: 'AA', iface: 'wifi1', ssid: 'SkyNet', band: '5GHz', signal: -50 });
  c._emitClients();

  const out = c.lastPayload.ssids[0];
  assert.strictEqual(out.clients, 1, 'the emitted count reflects who is connected now');
  assert.deepStrictEqual(out.bands, ['5GHz'], 'and so does the band');
});

test('a client is matched to its SSID by interface, not only by name', () => {
  // The interface is the one field the registration table is guaranteed to
  // carry — it is what the association is keyed on. Relying on a per-client
  // `ssid` field means that if the running RouterOS build does not report one,
  // every count silently reads zero, which is indistinguishable from an idle
  // network.
  const c = _wlCollector();
  c._ssids = c._parseSsids([
    { name: 'wifi1', 'configuration.ssid': 'SkyNet', running: 'true', disabled: 'false' },
    { name: 'wifi2', 'configuration.ssid': 'Guests', running: 'true', disabled: 'false' },
  ]).ssids;

  c._knownClients.set('AA', { mac: 'AA', iface: 'wifi1', ssid: '', band: '5GHz',   signal: -50 });
  c._knownClients.set('BB', { mac: 'BB', iface: 'wifi2', ssid: '', band: '2.4GHz', signal: -70 });
  c._emitClients();

  const byName = Object.fromEntries(c.lastPayload.ssids.map(s => [s.ssid, s]));
  assert.strictEqual(byName.SkyNet.clients, 1);
  assert.deepStrictEqual(byName.SkyNet.bands, ['5GHz']);
  assert.strictEqual(byName.Guests.clients, 1);
  assert.deepStrictEqual(byName.Guests.bands, ['2.4GHz']);
});

test('recomputing counts does not corrupt the stored SSID list', () => {
  // The cached list is configuration truth and gets reused every emit. Counting
  // into it in place would accumulate: two emits, two clients, one connection.
  const c = _wlCollector();
  c._ssids = c._parseSsids([
    { name: 'wifi1', 'configuration.ssid': 'SkyNet', running: 'true', disabled: 'false' },
  ]).ssids;
  c._knownClients.set('AA', { mac: 'AA', iface: 'wifi1', ssid: 'SkyNet', band: '5GHz', signal: -50 });

  c._emitClients();
  c._emitClients();

  assert.strictEqual(c.lastPayload.ssids[0].clients, 1, 'still one client after two emits');
  assert.deepStrictEqual(c.lastPayload.ssids[0].bands, ['5GHz'], 'and one band, not two');
});

// ── Saying so when hostname resolution cannot work (issue #93, from #88) ─────
//
// Wireless names come from the DHCP lease table (by MAC) or from a reverse
// lookup of the client's IP, and that IP comes only from the router's ARP
// table. A router that bridges wireless clients at layer 2 — a CAP whose
// gateway and DHCP live on another device — never holds an ARP entry for them,
// so no IP is known and no lookup is ever attempted.
//
// That is correct behaviour with no error to report, which is exactly why it
// has to be said out loud. The reporter in #88 had working reverse DNS the
// whole time and no lookup was ever tried; the page showed bare MACs and
// nothing distinguished "your DNS is wrong" from "this router cannot know".

function _wlLogCapture(fn) {
  const util = require('node:util');
  const orig = console.log;
  const lines = [];
  console.log = (...a) => lines.push(util.format(...a));
  try { fn(); } finally { console.log = orig; }
  return lines.filter(l => /hostname resolution/.test(l));
}

function _wlWithClients(clients) {
  const c = _wlCollector();
  clients.forEach((cl, i) => c._knownClients.set('M' + i, cl));
  return c;
}

const _noIp = (n) => Array.from({ length: n }, (_, i) => ({
  mac: 'AA:BB:CC:00:00:0' + i, iface: 'wifi1', ssid: 'SkyNet',
  band: '5GHz', signal: -50, ip: '', name: '',
}));

test('a router with no ARP entry for any client says so, once', () => {
  const c = _wlWithClients(_noIp(52));
  const lines = _wlLogCapture(() => c._emitClients());

  assert.strictEqual(lines.length, 1, 'one line, not one per client');
  assert.match(lines[0], /52\/52/, 'reports the ratio, so the scale is obvious');
  assert.match(lines[0], /ARP/, 'names the actual cause');
  c.stop();
});

test('the warning is throttled while the condition persists', () => {
  // A layer 2 AP is permanently in this state. Repeating every poll would bury
  // the log it is meant to make readable.
  const c = _wlWithClients(_noIp(3));
  const first  = _wlLogCapture(() => c._emitClients());
  const second = _wlLogCapture(() => { c._emitClients(); c._emitClients(); });

  assert.strictEqual(first.length, 1);
  assert.strictEqual(second.length, 0, 'silent while inside the throttle window');
  c.stop();
});

test('a client that does have an IP means nothing is wrong', () => {
  // Partial absence is ordinary: a client that just associated has not spoken
  // IP yet. Warning on that would cry wolf on every healthy router.
  const c = _wlWithClients([
    ..._noIp(2),
    { mac: 'AA:BB:CC:00:00:09', iface: 'wifi1', ssid: 'SkyNet', band: '5GHz', signal: -40, ip: '192.168.88.10', name: 'laptop' },
  ]);
  assert.deepStrictEqual(_wlLogCapture(() => c._emitClients()), []);
  c.stop();
});

test('an access point with nobody connected is not reported as broken', () => {
  const c = _wlWithClients([]);
  assert.deepStrictEqual(_wlLogCapture(() => c._emitClients()), []);
  c.stop();
});

test('the warning returns promptly after the condition clears and recurs', () => {
  // Reset on recovery rather than riding out the throttle, so somebody who
  // fixes the router and breaks it again is told the second time too.
  const c = _wlWithClients(_noIp(2));
  assert.strictEqual(_wlLogCapture(() => c._emitClients()).length, 1);

  c._knownClients.set('M0', { mac: 'AA', iface: 'wifi1', ip: '192.168.88.5', name: 'pc', signal: -50, band: '5GHz' });
  c._knownClients.set('M1', { mac: 'BB', iface: 'wifi1', ip: '192.168.88.6', name: 'tv', signal: -50, band: '5GHz' });
  assert.deepStrictEqual(_wlLogCapture(() => c._emitClients()), [], 'healthy again, nothing logged');

  c._knownClients.set('M0', { mac: 'AA', iface: 'wifi1', ip: '', name: '', signal: -50, band: '5GHz' });
  c._knownClients.set('M1', { mac: 'BB', iface: 'wifi1', ip: '', name: '', signal: -50, band: '5GHz' });
  assert.strictEqual(_wlLogCapture(() => c._emitClients()).length, 1, 'reported again on recurrence');
  c.stop();
});

// ── The scannable-radio catalogue (frequency analyzer) ───────────────────────
//
// Built from rows _refreshSsids already fetches, so the scan can validate a
// request without a write() — whose 30s timeout closes the connection every
// collector on the router shares.

test('only real radios are scannable, not virtual APs', () => {
  // The live fleet reports twelve /interface/wifi rows of which only four have a
  // radio of their own. Offering the other eight would offer scans that cannot
  // happen, and `master` is the only field that separates them.
  const c = _wlCollector();
  c._scanIfaces = c._parseScanIfaces([
    { name: '2.4GHz WiFi',  master: 'true',  disabled: 'false', running: 'true' },
    { name: '2.4GHz WiFi2', master: 'false', disabled: 'false', running: 'true' },
    { name: '5GHz WiFi',    master: 'true',  disabled: 'false', running: 'true' },
  ], '/interface/wifi/print');

  assert.deepStrictEqual(c.listScannableInterfaces().map(i => i.name),
    ['2.4GHz WiFi', '5GHz WiFi']);
});

test('a radio reports the clients a scan would actually disconnect', () => {
  // Measured on the live fleet, and the reason this exists: scanning a radio
  // dropped all 15 of its clients within 2 seconds and they took over 15 seconds
  // to start returning — and not one was associated to the radio's OWN
  // interface. Every one was on a virtual AP riding the same radio. Counting
  // only the interface would have shown "no clients" immediately before
  // knocking fifteen devices off the network.
  const c = _wlCollector();
  c._scanIfaces = c._parseScanIfaces([
    { name: 'radioA', '.id': '*45', master: 'true',  disabled: 'false' },
    { name: 'radioA-guest', '.id': '*46', master: 'false', 'master-interface': 'radioA', disabled: 'false' },
    { name: 'radioB', '.id': '*49', master: 'true',  disabled: 'false' },
    { name: 'radioB-guest', '.id': '*4A', master: 'false', 'master-interface': 'radioB', disabled: 'false' },
  ], '/interface/wifi/print');

  c._knownClients.set('1', { mac: '1', iface: 'radioA' });
  c._knownClients.set('2', { mac: '2', iface: 'radioA-guest' });
  c._knownClients.set('3', { mac: '3', iface: 'radioA-guest' });
  c._knownClients.set('4', { mac: '4', iface: 'radioB-guest' });

  const byName = Object.fromEntries(c.listScannableInterfaces().map(i => [i.name, i.clients]));
  assert.strictEqual(byName.radioA, 3, 'its own client plus both on its virtual AP');
  assert.strictEqual(byName.radioB, 1, 'a radio with clients only on its virtual AP is not idle');
});

test('an idle radio reports no clients', () => {
  // The other half: a radio that really is idle must say so, or the warning
  // cries wolf and stops being read.
  const c = _wlCollector();
  c._scanIfaces = c._parseScanIfaces([
    { name: 'idle', '.id': '*45', master: 'true', disabled: 'false' },
    { name: 'busy', '.id': '*49', master: 'true', disabled: 'false' },
  ], '/interface/wifi/print');
  c._knownClients.set('1', { mac: '1', iface: 'busy' });

  const byName = Object.fromEntries(c.listScannableInterfaces().map(i => [i.name, i.clients]));
  assert.strictEqual(byName.idle, 0);
  assert.strictEqual(byName.busy, 1);
});

test('a CAPsMAN-managed radio and a disabled one are not offered', () => {
  // A managed CAP's radio is not this box's to take off the air.
  const c = _wlCollector();
  c._scanIfaces = c._parseScanIfaces([
    { name: 'managed',  master: 'true', 'configuration.manager': 'capsman', disabled: 'false' },
    { name: 'disabled', master: 'true', disabled: 'true' },
    { name: 'good',     master: 'true', disabled: 'false' },
  ], '/interface/wifi/print');

  assert.deepStrictEqual(c.listScannableInterfaces().map(i => i.name), ['good']);
});

test('the catalogue copies no security field', () => {
  // Same hazard as the SSID list: these rows carry security.passphrase in clear
  // text and this list reaches a browser.
  const c = _wlCollector();
  const blob = JSON.stringify(c._parseScanIfaces([{
    name: 'wifi1', master: 'true', disabled: 'false',
    'security.passphrase': 'hunter2', 'security.authentication-types': 'wpa2-psk',
  }], '/interface/wifi/print'));
  assert.ok(!blob.includes('hunter2'));
  assert.ok(!blob.includes('security'));
});

test('the legacy wireless stack reports no scannable radios', () => {
  // Its scan command differs and there is no device here to verify it against.
  // An empty list disables the button; guessing would ship an untestable path.
  const c = _wlCollector();
  assert.deepStrictEqual(
    c._parseScanIfaces([{ name: 'wlan1', master: 'true' }], '/interface/wireless/print'), []);
});

// ── The CAPsMAN probe must not touch SSID state ──────────────────────────────
//
// Reported as "the WiFi SSIDs card takes some time to populate". The cause was
// five constructor lines duplicated into _probeCAPsMAN's catch block, so on any
// router where /caps-man does not exist — every router running only the wifi
// package — the probe's failure path reset the SSID list and the refresh timer.
//
// It survived because the initial start happens to be ordered safely: start()
// awaits the probe and only then calls _startSsidRefresh(). Reconnect is not:
// there the probe is fired without await, so it lands after the refresh and
// wipes what the refresh just fetched. The card then waits out the 5-minute
// cadence, which is exactly the reported symptom.

const _wlNoCapsman = () => new WirelessCollector({
  ros: {
    connected: true, on() {},
    async write(path) {
      if (String(path).includes('caps-man')) throw new Error('no such command or directory');
      return [{ name: 'wifi1', 'configuration.ssid': 'SkyNet', running: 'true', disabled: 'false' }];
    },
  },
  io: { emit() {}, to() { return this; } },
  pollMs: 5000, state: {},
  dhcpLeases: { getNameByMAC: () => null },
  arp: { getByMAC: () => null },
});

test('a failed CAPsMAN probe leaves the SSID list alone', async () => {
  const c = _wlNoCapsman();
  await c._refreshSsids();
  assert.strictEqual(c._ssids.length, 1, 'precondition: the list was fetched');

  await c._probeCAPsMAN();

  assert.strictEqual(c._capsmanAvailable, false, 'the probe still latches CAPsMAN off');
  assert.strictEqual(c._ssids.length, 1, 'but it must not clear the SSIDs it never owned');
  assert.strictEqual(c._ssidEndpoint, '/interface/wifi/print',
    'nor un-latch the endpoint, which would re-probe both stacks every cycle');
});

test('a failed CAPsMAN probe does not orphan the SSID refresh timer', async () => {
  // Nulling the handle without clearing the interval is worse than it looks:
  // stop() can then never cancel it, and the next _startSsidRefresh() sees a
  // null handle and starts a second one. One reconnect, one leaked timer, each
  // still querying the router every five minutes.
  const c = _wlNoCapsman();
  c._startSsidRefresh();
  const first = c._ssidTimer;
  assert.ok(first, 'precondition: the refresh timer is running');

  await c._probeCAPsMAN();
  assert.strictEqual(c._ssidTimer, first, 'the probe must not drop the timer handle');

  c._startSsidRefresh();
  assert.strictEqual(c._ssidTimer, first, 'and a second call must still be a no-op');
  c.stop();
  assert.strictEqual(c._ssidTimer, null, 'stop() can still cancel it');
});

test('rows with no SSID at all are skipped rather than listed blank', () => {
  const c = _wlCollector();
  const { ssids } = c._parseSsids([
    { name: 'wifi9', running: 'true', disabled: 'false' },
    { name: 'wifi8', 'configuration.ssid': '   ', running: 'true', disabled: 'false' },
  ]);
  assert.deepStrictEqual(ssids, []);
});

test('bands and client counts come from the registration table', () => {
  // The interface table does not say which band is in use; the clients do. An
  // SSID with nobody on it reports no band rather than guessing from a name.
  const c = _wlCollector();
  c._knownClients.set('AA', { mac: 'AA', ssid: 'SkyNet', band: '5GHz', signal: -50 });
  c._knownClients.set('BB', { mac: 'BB', ssid: 'SkyNet', band: '2.4GHz', signal: -60 });
  const { ssids } = c._parseSsids([
    { name: 'w1', 'configuration.ssid': 'SkyNet', running: 'true', disabled: 'false' },
    { name: 'w2', 'configuration.ssid': 'Quiet',  running: 'true', disabled: 'false' },
  ]);
  const sky = ssids.find(s => s.ssid === 'SkyNet');
  assert.strictEqual(sky.clients, 2);
  assert.deepStrictEqual(sky.bands, ['2.4GHz', '5GHz']);
  const quiet = ssids.find(s => s.ssid === 'Quiet');
  assert.strictEqual(quiet.clients, 0);
  assert.deepStrictEqual(quiet.bands, []);
});

// ── DHCP utilisation on a large subnet (issue #115) ──────────────────────────
//
// A CCR2004 with two /23 pools showed them ~100% used — 507 of 512 — while only
// ~110 addresses were actually held. The denominator was never wrong:
// poolRangeSize works off 32-bit integers and is prefix-agnostic. The numerator
// counted every lease the table held, `waiting` reservations included, and a
// `waiting` lease is precisely one nobody is using.

/** A /23 network + pool, and a lease stub with a given status mix. */
function makeSlash23(leaseStub) {
  const emitted = [];
  const ros = {
    connected: true,
    on() {},
    write: async (cmd) => {
      if (cmd.includes('network')) return [{ address: '10.10.0.0/23', gateway: '10.10.0.1' }];
      if (cmd.includes('pool'))    return [{ name: 'big', ranges: '10.10.0.10-10.10.1.254' }];
      return [];
    },
  };
  const io = { to() { return io; }, emit(ev, data) { emitted.push({ ev, data }); } };
  const collector = new DhcpNetworksCollector({
    ros, io, pollMs: 15000, dhcpLeases: leaseStub, state: {},
  });
  return { collector, emitted };
}

/**
 * n addresses inside 10.10.0.0/23, by offset from 10.10.0.0.
 *
 * The /23 spans offsets 0-511, so `from + n` must stay under 512 or the tail
 * falls outside the subnet and ipInCidr correctly refuses to count it — which
 * is a bug in the fixture, not in the collector.
 */
function slash23Ips(n, from = 10) {
  assert.ok(from + n <= 512, 'fixture overflows the /23: ' + (from + n) + ' > 512');
  const out = [];
  for (let i = 0; i < n; i++) {
    const v = from + i;
    out.push('10.10.' + Math.floor(v / 256) + '.' + (v % 256));
  }
  return out;
}

test('a /23 counts the addresses in use, not every lease row', async () => {
  // The reported shape: 110 held, 397 reservations nobody is using.
  const held = slash23Ips(110);
  const RESERVED = 397;                    // waiting rows the router also holds
  const { collector, emitted } = makeSlash23({ getInUseLeaseIPs: () => held });
  await collector._fetchOnce();

  const net = emitted[0].data.networks[0];
  assert.equal(net.poolSize, 501, 'the denominator was never wrong: 10.10.0.10-10.10.1.254');
  assert.equal(net.leaseCount, 110);
  assert.notEqual(net.leaseCount, held.length + RESERVED,
    'issue #115: 507 was 110 held plus 397 reservations, counted as if all were used');
});

test('and still counts them all when they really are all in use', async () => {
  // The inverse. Without this, a filter that returned nothing would pass above.
  const all = slash23Ips(507, 0);
  const { collector, emitted } = makeSlash23({ getInUseLeaseIPs: () => all });
  await collector._fetchOnce();
  assert.equal(emitted[0].data.networks[0].leaseCount, 507);
});

test('only `waiting` frees an address; every other status holds one', async () => {
  // A deny-list, not an allow-list. `testing` and `authorizing` are
  // mid-allocation, and a `declined`/`conflict` address stays busy for the lease
  // time — all of them are unavailable to another client. An empty status counts
  // too: it comes from a partial stream row, never from an untaken reservation.
  const ros = makeLeaseRos([
    { address: '10.10.0.1', 'mac-address': 'A1', status: 'bound' },
    { address: '10.10.0.2', 'mac-address': 'A2', status: 'offered' },
    { address: '10.10.0.3', 'mac-address': 'A3', status: 'testing' },
    { address: '10.10.0.4', 'mac-address': 'A4', status: 'authorizing' },
    { address: '10.10.0.5', 'mac-address': 'A5', status: 'declined' },
    { address: '10.10.0.6', 'mac-address': 'A6', status: 'conflict' },
    { address: '10.10.0.7', 'mac-address': 'A7', status: '' },
    { address: '10.10.0.8', 'mac-address': 'A8', status: 'waiting' },
  ]);
  const collector = new DhcpLeasesCollector({ ros, io: { emit() {} }, pollMs: 15000, state: {} });
  await collector._loadInitial();

  const inUse = collector.getInUseLeaseIPs();
  assert.equal(inUse.length, 7);
  assert.ok(!inUse.includes('10.10.0.8'), 'the waiting reservation is the one that is free');
  for (const ip of ['10.10.0.1', '10.10.0.2', '10.10.0.3', '10.10.0.4',
                    '10.10.0.5', '10.10.0.6', '10.10.0.7']) {
    assert.ok(inUse.includes(ip), ip + ' holds an address and must count');
  }
  collector.stop();
});

test('the lease table still lists a reservation the utilisation excludes', async () => {
  // Two different questions. The table says what exists; the bar says what a new
  // client could not be given. A future "fix" that filters the table too would
  // hide reservations from the page that exists to show them.
  const emitted = [];
  const ros = makeLeaseRos([
    { address: '10.10.0.1', 'mac-address': 'A1', status: 'bound' },
    { address: '10.10.0.8', 'mac-address': 'A8', status: 'waiting' },
  ]);
  const collector = new DhcpLeasesCollector({
    ros, io: { emit(ev, d) { emitted.push({ ev, d }); } }, pollMs: 15000, state: {},
  });
  await collector._loadInitial();

  const listed = emitted.filter(e => e.ev === 'leases:list').pop().d.leases;
  assert.equal(listed.length, 2, 'both leases are listed');
  assert.ok(listed.some(l => l.ip === '10.10.0.8' && l.status === 'waiting'));
  assert.equal(collector.getInUseLeaseIPs().length, 1, 'but only one holds an address');
  collector.stop();
});

test('a full re-read drops a lease the router no longer has', async () => {
  // /print is the whole table, so it replaces rather than merges. _applyLease
  // only prunes on a `.dead` that arrives on the listen stream, so in poll mode
  // nothing was ever pruned and a vanished lease stayed for good — keeping its
  // last status, usually `bound`, where no status filter can reach it.
  const rows = [
    { address: '10.10.0.1', 'mac-address': 'A1', status: 'bound' },
    { address: '10.10.0.2', 'mac-address': 'A2', status: 'bound' },
    { address: '10.10.0.3', 'mac-address': 'A3', status: 'bound' },
  ];
  const ros = makeLeaseRos(rows);
  const collector = new DhcpLeasesCollector({ ros, io: { emit() {} }, pollMs: 15000, state: {} });
  await collector._loadInitial();
  assert.equal(collector.getInUseLeaseIPs().length, 3);

  rows.splice(1, 2);                       // two clients go away
  await collector._loadInitial();

  assert.equal(collector.getInUseLeaseIPs().length, 1, 'the snapshot is authoritative');
  assert.ok(!collector.getNameByIP('10.10.0.2'), 'and the name lookup forgets them too');
  assert.ok(collector.getNameByIP('10.10.0.1'), 'while the survivor is untouched');
  collector.stop();
});

test('a failed re-read leaves the last good table standing', async () => {
  // The clear happens AFTER the await, never before. Blanking on a failed read
  // would empty the lease table and every name lookup hanging off it.
  const rows = [
    { address: '10.10.0.1', 'mac-address': 'A1', status: 'bound' },
    { address: '10.10.0.2', 'mac-address': 'A2', status: 'bound' },
  ];
  let fail = false;
  const ros = {
    connected: true,
    on() {},
    async write(path) {
      if (path === '/ip/dhcp-server/print' || path === '/interface/vlan/print') return [];
      if (fail) throw new Error('timeout');
      return rows;
    },
    stream() { return { stop() {} }; },
  };
  const collector = new DhcpLeasesCollector({ ros, io: { emit() {} }, pollMs: 15000, state: {} });
  await collector._loadInitial();
  assert.equal(collector.getInUseLeaseIPs().length, 2);

  fail = true;
  await collector._loadInitial();
  assert.equal(collector.getInUseLeaseIPs().length, 2, 'a failed read must not blank the table');
  collector.stop();
});
