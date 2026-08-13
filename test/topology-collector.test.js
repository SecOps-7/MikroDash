'use strict';
// Topology collector — classification, graph shape, retention, ping round-robin.
//
// The neighbour rows below are REAL output captured from the development network
// (three MikroTik devices and a Meraki switch). That matters: `system-caps` is
// LLDP-only and comes back EMPTY for every MNDP-discovered MikroTik neighbour, so
// the board fallback is the common path, not an edge case. Fixtures invented by
// hand would have had caps populated everywhere and hidden that entirely.

const test = require('node:test');
const assert = require('node:assert/strict');
const EventEmitter = require('events');

const TopologyCollector = require('../src/collectors/topology');
const { classifyDevice, parseAgeSec, parseRttMs, RETAIN_MS } = TopologyCollector;

// ── fixtures (captured from /ip/neighbor/print) ──────────────────────────────

const ROW_CAP = {
  '.id': '*1', interface: 'ether1,bridgeLocal', address: '10.0.0.4', address4: '10.0.0.4',
  address6: 'fe80::4aa9:8aff:fee5:ce34', 'mac-address': '48:A9:8A:E5:CE:34',
  identity: 'cAP', platform: 'MikroTik', version: '7.23.3 (stable)', age: '5s',
  uptime: '2d15h36m22s', 'software-id': 'I5DX-28HL', board: 'cAPGi-5HaxD2HaxD',
  ipv6: 'true', 'interface-name': 'bridgeLocal/ether1',
  'system-caps': '', 'system-caps-enabled': '', 'discovered-by': 'mndp',
};

const ROW_MERAKI = {
  '.id': '*2', interface: 'ether1,bridgeLocal', address: '10.0.0.3', address4: '10.0.0.3',
  'mac-address': '34:56:FE:2F:98:5C', identity: 'Home Switch', platform: 'Meraki',
  version: '', age: '27s', 'interface-name': 'Port 1',
  'system-description': 'Meraki MS120-8LP Cloud Managed PoE Switch',
  'system-caps': 'bridge', 'system-caps-enabled': 'bridge', 'discovered-by': 'lldp',
};

const ROW_HAP = {
  '.id': '*3', interface: 'ether1', address: '10.0.0.2', address4: '10.0.0.2',
  'mac-address': '48:A9:8A:09:FE:27', identity: 'hAP', platform: 'MikroTik',
  version: '7.23.3 (stable)', age: '17s', uptime: '2d15h29m24s',
  board: 'C53UiG+5HPaxD2HPaxD', 'interface-name': 'Home',
  'system-description': 'MikroTik RouterOS 7.23.3 C53UiG+5HPaxD2HPaxD',
  'system-caps': 'bridge,router', 'system-caps-enabled': 'bridge,router',
  'discovered-by': 'cdp,lldp,mndp', running: 'capsman',
};

// ── harness ──────────────────────────────────────────────────────────────────

function mockRos(writeFn) {
  const ros = new EventEmitter();
  ros.setMaxListeners(30);
  ros.connected = true;
  ros.host = '10.0.0.2';
  ros.routerLabel = 'test-router';
  ros.write = writeFn || (async () => []);
  ros.stream = (words) => {
    const s = { _words: words, stopped: false, stop() { this.stopped = true; } };
    s._handlers = {};
    s.on = (ev, cb) => { s._handlers[ev] = cb; return s; };
    ros._lastStream = s;
    return s;
  };
  return ros;
}

function mockIo(clients = 1) {
  const emitted = [];
  return {
    emitted,
    engine: { clientsCount: clients },
    emit(ev, data) { emitted.push({ room: null, ev, data }); },
    to(room) { return { emit: (ev, data) => emitted.push({ room, ev, data }) }; },
  };
}

function build(rows, opts = {}) {
  const ros = opts.ros || mockRos(async (cmd) => {
    if (cmd === '/ip/neighbor/print') return rows;
    if (cmd === '/ip/neighbor/discovery-settings/print') {
      return [{ protocol: 'cdp,lldp,mndp', mode: 'tx-and-rx', 'discover-interface-list': 'Management' }];
    }
    if (cmd === '/interface/bridge/host/print') return opts.hosts || [];
    return [];
  });
  const io = opts.io || mockIo();
  const state = {};
  const c = new TopologyCollector({
    ros, io, state, rid: 'r1',
    pollMs: opts.pollMs || 30000,
    streamMode: opts.streamMode !== undefined ? opts.streamMode : false,
    arp: opts.arp || null,
    ifStatus: opts.ifStatus === undefined ? null : opts.ifStatus,
    system: opts.system || null,
    dhcpLeases: opts.dhcpLeases || null,
    showClients: opts.showClients,
  });
  return { c, ros, io, state };
}

/** Drive one poll + the 10 ms rebuild debounce without waiting on real timers. */
async function pump(c) {
  await c._pollOnce();
  if (c._rebuildDebounce) { clearTimeout(c._rebuildDebounce); c._rebuildDebounce = null; }
  c._hostsTs = 0;
  await c._fetchVlans();
  await c._refreshFabric();
  c._rebuild();
}

// ── classification ───────────────────────────────────────────────────────────

test('LLDP caps classify a bridge-only device as a switch', () => {
  assert.deepEqual(classifyDevice(ROW_MERAKI), { type: 'switch', typeSource: 'caps' });
});

test('bridge+router ties break on the board, not arbitrarily', () => {
  // A MikroTik router over LLDP advertises both. C53UiG+ is an hAP ax3 -> router.
  assert.deepEqual(classifyDevice(ROW_HAP), { type: 'router', typeSource: 'board' });
});

test('router-only caps classify as a router', () => {
  assert.deepEqual(classifyDevice({ 'system-caps-enabled': 'router' }),
    { type: 'router', typeSource: 'caps' });
});

test('an empty caps list falls back to the board — the common MNDP path', () => {
  assert.deepEqual(classifyDevice(ROW_CAP), { type: 'ap', typeSource: 'board' });
});

test('system-caps is used when system-caps-enabled is absent', () => {
  assert.deepEqual(classifyDevice({ 'system-caps': 'bridge' }), { type: 'switch', typeSource: 'caps' });
});

test('wlan capability wins over bridge and router on an all-in-one AP', () => {
  assert.deepEqual(classifyDevice({ 'system-caps-enabled': 'bridge,router,wlan-access-point' }),
    { type: 'ap', typeSource: 'caps' });
});

test('capability spelling variants are all tolerated', () => {
  for (const spelling of ['wlan-access-point', 'wlan-ap', 'wlan_ap', 'wlanap', 'wlan']) {
    assert.equal(classifyDevice({ 'system-caps-enabled': spelling }).type, 'ap', spelling);
  }
  assert.equal(classifyDevice({ 'system-caps-enabled': 'station-only' }).type, 'station');
  assert.equal(classifyDevice({ 'system-caps-enabled': 'telephone' }).type, 'phone');
  assert.equal(classifyDevice({ 'system-caps-enabled': 'docsis-cable-device' }).type, 'modem');
  assert.equal(classifyDevice({ 'system-caps-enabled': 'repeater' }).type, 'repeater');
});

test('board families map to the right device types', () => {
  const cases = [
    ['CRS328-24P-4S+', 'switch'], ['CSS610-8G-2S+', 'switch'],
    ['cAPGi-5HaxD2HaxD', 'ap'], ['wAP-2nD', 'ap'], ['Audience', 'ap'], ['LHG-5nD', 'ap'],
    ['CCR2004-1G-12S+2XS', 'router'], ['RBD52G-5HacD2HnD', 'router'],
    ['C53UiG+5HPaxD2HPaxD', 'router'], ['hEX-S', 'router'],
  ];
  for (const [board, want] of cases) {
    assert.equal(classifyDevice({ board }).type, want, board);
  }
});

test('hAP is a router, not an access point', () => {
  // "hAP" reads like an AP but is a home router with a radio. Drawing every home
  // router as an AP would make the map wrong for the most common MikroTik device.
  assert.equal(classifyDevice({ board: 'hAP-ac2' }).type, 'router');
});

test('a non-MikroTik platform with no caps and no board is "other"', () => {
  assert.deepEqual(classifyDevice({ platform: 'Cisco IOS' }), { type: 'other', typeSource: 'platform' });
});

test('an unknown token does not throw and does not win', () => {
  const r = classifyDevice({ 'system-caps-enabled': 'quantum-relay,bridge' });
  assert.equal(r.type, 'switch');
});

test('a completely empty row classifies as unknown rather than throwing', () => {
  assert.deepEqual(classifyDevice({}), { type: 'unknown', typeSource: 'unknown' });
});

// ── age parsing ──────────────────────────────────────────────────────────────

test('age parses to seconds and is never NaN', () => {
  assert.equal(parseAgeSec('5s'), 5);
  assert.equal(parseAgeSec('1m20s'), 80);
  assert.equal(parseAgeSec('2d15h36m22s'), 2 * 86400 + 15 * 3600 + 36 * 60 + 22);
  assert.equal(parseAgeSec('42'), 42);
  assert.equal(parseAgeSec('abc'), null);
  assert.equal(parseAgeSec(''), null);
  assert.equal(parseAgeSec(undefined), null);
});

// ── rtt parsing ──────────────────────────────────────────────────────────────

test('sub-millisecond RTT is read as microseconds, not milliseconds', () => {
  // A real LAN reply is "413us". Stripping the unit yields 413, which would read
  // as 413 ms — fast enough to mark every healthy device as degraded.
  assert.equal(parseRttMs('413us'), 0.413);
  assert.equal(parseRttMs('2ms'), 2);
  assert.equal(parseRttMs('1s'), 1000);
  assert.equal(parseRttMs('7'), 7);
  assert.equal(parseRttMs(''), null);
  assert.equal(parseRttMs(undefined), null);
});

test('a microsecond reply leaves a LAN device healthy', async () => {
  const ros = mockRos(async (cmd) => {
    if (cmd === '/ip/neighbor/print') return [ROW_HAP];
    if (cmd === '/tool/ping') {
      return [{ seq: '0', host: '10.0.0.2', ttl: '64', time: '413us',
                sent: '1', received: '1', 'packet-loss': '0' }];
    }
    return [];
  });
  const { c } = build([], { ros });
  await pump(c);
  await c._pingNextOnce();
  c._rebuild();
  const n = c.lastPayload.nodes[1];
  assert.equal(n.rtt, 0.413);
  assert.equal(n.status, 'up', 'a 0.4 ms hop is healthy, not degraded');
});

test('received=0 counts as a loss even if a time field is present', async () => {
  const ros = mockRos(async (cmd) => {
    if (cmd === '/ip/neighbor/print') return [ROW_HAP];
    if (cmd === '/tool/ping') return [{ sent: '1', received: '0', 'packet-loss': '100' }];
    return [];
  });
  const { c } = build([], { ros });
  await pump(c);
  await c._pingNextOnce();
  c._rebuild();
  assert.equal(c.lastPayload.nodes[1].loss, 100);
});

// ── graph shape ──────────────────────────────────────────────────────────────

test('builds a core node plus one node per neighbour', async () => {
  const { c } = build([ROW_CAP, ROW_MERAKI, ROW_HAP]);
  await pump(c);
  const p = c.lastPayload;
  assert.equal(p.nodes.length, 4);
  assert.equal(p.nodes[0].kind, 'core');
  assert.equal(p.neighborCount, 3);
  assert.deepEqual(p.nodes.slice(1).map(n => n.type).sort(), ['ap', 'router', 'switch']);
});

test('every edge starts at the core and names its local interface', async () => {
  const { c } = build([ROW_HAP]);
  await pump(c);
  assert.equal(c.lastPayload.edges.length, 1);
  assert.deepEqual(c.lastPayload.edges[0], {
    id: 'ether1|48:A9:8A:09:FE:27',
    from: 'core', to: '48:A9:8A:09:FE:27', iface: 'ether1', viaPort: 'ether1',
    remoteIface: 'Home', shared: false, inferred: false, gone: false,
  });
});

test('a bridge member collapses to the physical port', async () => {
  // RouterOS reports interface="ether1,bridgeLocal"; without collapsing, every
  // device would render with two links to the core.
  const ifStatus = { lastPayload: { interfaces: [
    { name: 'ether1', type: 'ether' }, { name: 'bridgeLocal', type: 'bridge' },
  ] } };
  const { c } = build([ROW_CAP], { ifStatus });
  await pump(c);
  assert.deepEqual(c.lastPayload.nodes[1].ifaces, ['ether1']);
  assert.equal(c.lastPayload.edges.length, 1);
});

test('bridge collapse still works when ifStatus is disabled', async () => {
  const { c } = build([ROW_CAP], { ifStatus: null });
  await pump(c);
  assert.deepEqual(c.lastPayload.nodes[1].ifaces, ['ether1']);
  assert.equal(c.lastPayload.edges.length, 1);
});

test('an LLDP device on a shared port becomes the parent, not a sibling', async () => {
  // Both sit on ether1. The Meraki answers LLDP so it is the direct neighbour;
  // the cAP is MNDP-only, so it is behind the Meraki rather than beside it.
  const { c } = build([ROW_CAP, ROW_MERAKI]);
  await pump(c);
  const cap = c.lastPayload.nodes.find(n => n.key === '48:A9:8A:E5:CE:34');
  assert.equal(cap.parent, '34:56:FE:2F:98:5C');
  assert.ok(c.lastPayload.edges.some(e => e.from === '34:56:FE:2F:98:5C' && e.to === cap.key));
});

test('the same device heard twice yields one node', async () => {
  const dup = { ...ROW_CAP, '.id': '*9', interface: 'ether5' };
  const { c } = build([ROW_CAP, dup]);
  await pump(c);
  assert.equal(c.lastPayload.neighborCount, 1);
  assert.deepEqual(c.lastPayload.nodes[1].ifaces.sort(), ['ether1', 'ether5']);
});

test('an empty neighbour table yields just the core, no throw', async () => {
  const { c } = build([]);
  await pump(c);
  assert.equal(c.lastPayload.nodes.length, 1);
  assert.deepEqual(c.lastPayload.edges, []);
});

test('malformed rows never produce NaN or undefined in the payload', async () => {
  const junk = [
    null, 'nonsense', [], 42,
    { '.id': '*7' },                                   // no mac, no anything
    { 'mac-address': '', age: 'abc', interface: '' },  // unusable
    { '.id': '*8', 'mac-address': 'AA:BB:CC:DD:EE:FF', age: 'zzz' },
  ];
  const { c } = build(junk);
  await pump(c);
  const json = JSON.stringify(c.lastPayload);
  assert.ok(!json.includes('NaN'), 'no NaN');
  assert.ok(!json.includes('undefined'), 'no undefined');
  for (const n of c.lastPayload.nodes) {
    assert.ok(n.ageSec === null || Number.isFinite(n.ageSec), 'ageSec finite or null');
    assert.ok(typeof n.key === 'string' && n.key.length, 'every node has a key');
  }
});

test('a MAC-less neighbour gets a key the layout API will accept', async () => {
  // Node keys are persisted as object keys by /api/topology-layout, whose
  // validator rejects punctuation. A raw RouterOS id ("*7") would be silently
  // dropped there, so such a device could never keep a dragged position.
  const { cleanPositions } = require('../src/topologyLayout');
  const { c } = build([{ '.id': '*7', interface: 'ether3', board: 'CRS326' }]);
  await pump(c);
  const key = c.lastPayload.nodes[1].key;
  assert.equal(key, 'id:7');
  assert.ok(cleanPositions({ [key]: { x: 1, y: 2 } }), 'the layout API accepts it');
});

test('arp supplies the IP when the neighbour advertises none', async () => {
  const row = { '.id': '*4', 'mac-address': 'AA:BB:CC:DD:EE:FF', interface: 'ether2', board: 'CRS326' };
  const arp = { getByMAC: (mac) => (mac === 'AA:BB:CC:DD:EE:FF' ? { ip: '10.0.0.9' } : null) };
  const { c } = build([row], { arp });
  await pump(c);
  assert.equal(c.lastPayload.nodes[1].ip, '10.0.0.9');
});

// ── parentage: Router -> Switch -> cAP ───────────────────────────────────────
//
// These rows are the real ones from the development network once discovery was
// enabled on ether5. The Meraki answers LLDP (so it is directly attached); the
// cAP is MNDP-only and its MAC is learned on the SAME physical port, so it must
// be behind the Meraki. That is the whole inference.

const ROW_MERAKI_E5 = {
  ...ROW_MERAKI, interface: 'ether5,Bridge', 'discovered-by': 'lldp',
};
const ROW_CAP_HOME = {
  // Note the arrival interface is the VLAN, not the port — which is exactly why
  // the bridge host table is needed to place it on ether5.
  ...ROW_CAP, interface: 'Home', 'discovered-by': 'mndp',
};
const HOSTS_REAL = [
  { 'mac-address': '48:A9:8A:E5:CE:34', 'on-interface': 'ether5', bridge: 'Bridge' },
  { 'mac-address': '34:56:FE:2F:98:5B', 'on-interface': 'ether5', bridge: 'Bridge' },
  { 'mac-address': '2C:C8:1B:5D:2A:41', 'on-interface': 'ether2', bridge: 'Bridge' },
];

test('a device behind a switch is parented to the switch, not the router', async () => {
  const { c } = build([ROW_MERAKI_E5, ROW_CAP_HOME], { hosts: HOSTS_REAL });
  await pump(c);

  const cap = c.lastPayload.nodes.find(n => n.key === '48:A9:8A:E5:CE:34');
  const sw = c.lastPayload.nodes.find(n => n.key === '34:56:FE:2F:98:5C');
  assert.equal(sw.parent, null, 'the switch answers LLDP, so it is directly attached');
  assert.equal(cap.parent, sw.key, 'the cAP is MNDP-only on the same port, so it is behind it');

  const chain = c.lastPayload.edges.filter(e => !e.client).map(e => e.from + '>' + e.to).sort();
  assert.deepEqual(chain, ['34:56:FE:2F:98:5C>48:A9:8A:E5:CE:34', 'core>34:56:FE:2F:98:5C']);
});

test('the bridge host table overrides the arrival interface', async () => {
  // /ip/neighbor said "Home" (a VLAN); the MAC table says ether5. Without this
  // the cAP and the switch would look like they were on different links.
  const { c } = build([ROW_MERAKI_E5, ROW_CAP_HOME], { hosts: HOSTS_REAL });
  await pump(c);
  assert.equal(c.lastPayload.nodes.find(n => n.key === '48:A9:8A:E5:CE:34').port, 'ether5');
});

test('only the router-side edge carries an interface, so rates are not double-counted', async () => {
  const { c } = build([ROW_MERAKI_E5, ROW_CAP_HOME], { hosts: HOSTS_REAL });
  await pump(c);
  const toSwitch = c.lastPayload.edges.find(e => e.from === 'core');
  const behind = c.lastPayload.edges.find(e => e.from !== 'core');
  assert.equal(toSwitch.iface, 'ether5');
  assert.equal(behind.iface, '', 'the router cannot measure a link it is not on');
  assert.equal(behind.inferred, true);
  assert.equal(behind.viaPort, 'ether5');
});

test('a device on a port with no LLDP device stays on the router', async () => {
  // hap-ac2 sits on ether2 and answers only CDP/MNDP. Nothing on that port
  // identifies itself, so there is nothing to attribute it to — an unmanaged
  // switch is invisible by definition, and guessing would be worse than flat.
  const hapAc2 = { ...ROW_CAP, '.id': '*5', 'mac-address': '2C:C8:1B:5D:2A:41',
    identity: 'hap-ac2', board: 'RBD52G-5HacD2HnD', interface: 'Home',
    'discovered-by': 'cdp,mndp' };
  const { c } = build([ROW_MERAKI_E5, hapAc2], { hosts: HOSTS_REAL });
  await pump(c);
  assert.equal(c.lastPayload.nodes.find(n => n.key === '2C:C8:1B:5D:2A:41').parent, null);
});

test('two LLDP devices on one port are left flat rather than guessed at', async () => {
  const other = { ...ROW_MERAKI, '.id': '*9', 'mac-address': 'AA:BB:CC:DD:EE:01',
    identity: 'Other Switch', interface: 'ether5', 'discovered-by': 'lldp' };
  const hosts = HOSTS_REAL.concat([{ 'mac-address': 'AA:BB:CC:DD:EE:01', 'on-interface': 'ether5' }]);
  const { c } = build([ROW_MERAKI_E5, other, ROW_CAP_HOME], { hosts });
  await pump(c);
  for (const n of c.lastPayload.nodes) {
    if (n.kind === 'client') continue;              // clients always have a parent
    assert.equal(n.parent, null, n.key + ' stays on the core');
  }
});

test('devices sharing a port with no parent are still flagged as a shared segment', async () => {
  const a = { ...ROW_CAP, '.id': '*a', 'mac-address': 'AA:BB:CC:00:00:01', interface: 'ether3', 'discovered-by': 'mndp' };
  const b = { ...ROW_CAP, '.id': '*b', 'mac-address': 'AA:BB:CC:00:00:02', interface: 'ether3', 'discovered-by': 'mndp' };
  const { c } = build([a, b], { hosts: [] });
  await pump(c);
  assert.ok(c.lastPayload.edges.every(e => e.shared && e.from === 'core'));
});

test('the map still builds when the bridge host table is unavailable', async () => {
  // A bridgeless router, or an API user without the policy: fall back to the
  // arrival interface rather than losing the whole map.
  const ros = mockRos(async (cmd) => {
    if (cmd === '/ip/neighbor/print') return [ROW_MERAKI_E5, ROW_CAP_HOME];
    if (cmd === '/interface/bridge/host/print') throw new Error('no such command prefix');
    return [];
  });
  const { c } = build([], { ros });
  await pump(c);
  assert.equal(c._hostsDenied, true);
  assert.equal(c.lastPayload.nodes.length, 3, 'both devices still present');
  assert.equal(c.lastPayload.nodes.find(n => n.key === '48:A9:8A:E5:CE:34').port, 'Home');
});

test('re-parenting changes the fingerprint so the client is told', async () => {
  const { c, io } = build([ROW_MERAKI_E5, ROW_CAP_HOME], { hosts: HOSTS_REAL });
  await pump(c);
  const before = io.emitted.length;
  // The cAP is moved to a port of its own — it is now directly on the router.
  c._hosts.set('48:A9:8A:E5:CE:34', 'ether4');
  c._rebuild();
  assert.equal(io.emitted.length, before + 1);
  assert.equal(c.lastPayload.nodes.find(n => n.key === '48:A9:8A:E5:CE:34').parent, null);
});

// ── client attribution ───────────────────────────────────────────────────────
//
// Modelled on the real controller: the hAP's own radios carry its MAC family
// (48:A9:8A:09:*), the CAPsMAN-managed cAP's radios carry its own (48:A9:8A:E5:CE:*),
// and a managed radio is base+N rather than the base MAC exactly — which is why
// matching is done on the first five octets.

const WIFI_FIXTURE = {
  ifaces: [
    { name: '2.4GHz WiFi',  'radio-mac': '48:A9:8A:09:FE:2C' },       // hAP's own
    { name: '5GHz WiFi',    'radio-mac': '48:A9:8A:09:FE:2B' },       // hAP's own
    { name: '5GHz WiFi4',   'radio-mac': '48:A9:8A:E5:CE:36' },       // the cAP's
    { name: 'Guest-SSID',   'radio-mac': '', 'master-interface': '5GHz WiFi4' }, // virtual AP
  ],
  caps: [{ identity: 'cAP', address: '48:A9:8A:E5:CE:34%Home', state: 'Ok' }],
  reg: [
    { 'mac-address': 'AA:00:00:00:00:01', interface: '5GHz WiFi',  ssid: 'Home', signal: '-52' },
    { 'mac-address': 'AA:00:00:00:00:02', interface: '5GHz WiFi4', ssid: 'Home', signal: '-61' },
    { 'mac-address': 'AA:00:00:00:00:03', interface: 'Guest-SSID', ssid: 'Guest', signal: '-70' },
  ],
};

function buildWithClients(rows, hosts, opts = {}) {
  const ros = mockRos(async (cmd) => {
    if (cmd === '/ip/neighbor/print') return rows;
    if (cmd === '/ip/neighbor/discovery-settings/print') return [{ protocol: 'lldp', mode: 'tx-and-rx' }];
    if (cmd === '/interface/bridge/host/print') return hosts;
    if (cmd === '/interface/wifi/print') return WIFI_FIXTURE.ifaces;
    if (cmd === '/interface/wifi/capsman/remote-cap/print') return WIFI_FIXTURE.caps;
    if (cmd === '/interface/wifi/registration-table/print') return WIFI_FIXTURE.reg;
    if (cmd === '/interface/vlan/print') {
      return opts.vlans || [
        { name: 'Home', 'vlan-id': '5' },
        { name: 'IoT', 'vlan-id': '10' },
        { name: 'Guest', 'vlan-id': '20' },
      ];
    }
    return [];
  });
  return build([], Object.assign({ ros }, opts));
}

const CLIENT_HOSTS = [
  { 'mac-address': '34:56:FE:2F:98:5B', 'on-interface': 'ether5' },      // the switch itself
  { 'mac-address': '48:A9:8A:E5:CE:34', 'on-interface': 'ether5' },      // the cAP itself
  { 'mac-address': 'BB:00:00:00:00:01', 'on-interface': 'ether3' },      // wired to the router
  { 'mac-address': 'BB:00:00:00:00:02', 'on-interface': 'ether5' },      // wired behind the switch
  { 'mac-address': 'AA:00:00:00:00:01', 'on-interface': '5GHz WiFi' },   // wireless on the hAP
  { 'mac-address': 'AA:00:00:00:00:02', 'on-interface': '5GHz WiFi4' },  // wireless on the cAP
  { 'mac-address': 'AA:00:00:00:00:03', 'on-interface': 'Guest-SSID' },  // virtual AP on the cAP
];

function clientParent(payload, mac) {
  const n = payload.nodes.find(x => x.key === mac);
  return n ? n.parent : undefined;
}

test('clients attribute to router, switch and AP separately', async () => {
  const { c } = buildWithClients([ROW_MERAKI_E5, ROW_CAP_HOME], CLIENT_HOSTS);
  await pump(c);
  const p = c.lastPayload;
  const SWITCH = '34:56:FE:2F:98:5C', CAP = '48:A9:8A:E5:CE:34';

  assert.equal(clientParent(p, 'BB:00:00:00:00:01'), 'core',  'wired into the router');
  assert.equal(clientParent(p, 'BB:00:00:00:00:02'), SWITCH,  'wired behind the switch');
  assert.equal(clientParent(p, 'AA:00:00:00:00:01'), 'core',  "on the router's own radio");
  assert.equal(clientParent(p, 'AA:00:00:00:00:02'), CAP,     "on the managed AP's radio");
});

test('a virtual AP resolves through master-interface to the real radio', async () => {
  // Guest-SSID has no radio of its own. Without following master-interface this
  // client would be misattributed to the router.
  const { c } = buildWithClients([ROW_MERAKI_E5, ROW_CAP_HOME], CLIENT_HOSTS);
  await pump(c);
  assert.equal(clientParent(c.lastPayload, 'AA:00:00:00:00:03'), '48:A9:8A:E5:CE:34');
});

test('infrastructure devices are never also drawn as clients of themselves', async () => {
  const { c } = buildWithClients([ROW_MERAKI_E5, ROW_CAP_HOME], CLIENT_HOSTS);
  await pump(c);
  const clients = c.lastPayload.nodes.filter(n => n.kind === 'client').map(n => n.key);
  assert.ok(!clients.includes('48:A9:8A:E5:CE:34'), 'the cAP is a node, not a client');
  assert.ok(!clients.some(k => k.startsWith('34:56:FE')), 'the switch is a node, not a client');
  assert.equal(clients.length, 5);
});

test('wired and wireless clients are typed apart, and carry signal and SSID', async () => {
  const { c } = buildWithClients([ROW_MERAKI_E5, ROW_CAP_HOME], CLIENT_HOSTS);
  await pump(c);
  const wifi = c.lastPayload.nodes.find(n => n.key === 'AA:00:00:00:00:02');
  const wired = c.lastPayload.nodes.find(n => n.key === 'BB:00:00:00:00:01');
  assert.equal(wifi.type, 'wifi-client');
  assert.equal(wifi.ssid, 'Home');
  assert.equal(wifi.signal, '-61');
  assert.equal(wired.type, 'wired-client');
});

test('each parent reports how many clients it has, for the collapsed chip', async () => {
  const { c } = buildWithClients([ROW_MERAKI_E5, ROW_CAP_HOME], CLIENT_HOSTS);
  await pump(c);
  const p = c.lastPayload;
  assert.equal(p.nodes.find(n => n.key === 'core').clientCount, 2);
  assert.equal(p.nodes.find(n => n.key === '48:A9:8A:E5:CE:34').clientCount, 2);
  assert.equal(p.nodes.find(n => n.key === '34:56:FE:2F:98:5C').clientCount, 1);
  assert.equal(p.clientCount, 5);
});

test('clients are named from DHCP and addressed from ARP', async () => {
  // dhcpLeases.getNameByMAC returns the lease RECORD, not a string — using it
  // directly renders every client as "[object Object]".
  const dhcpLeases = {
    getNameByMAC: (m) => (m === 'BB:00:00:00:00:01' ? { name: 'kitchen-ipad', ip: '10.0.0.77' } : undefined),
  };
  const arp = { getByMAC: (m) => (m === 'BB:00:00:00:00:01' ? { ip: '10.0.0.77' } : null) };
  const { c } = buildWithClients([ROW_MERAKI_E5], CLIENT_HOSTS, { dhcpLeases, arp });
  await pump(c);
  const n = c.lastPayload.nodes.find(x => x.key === 'BB:00:00:00:00:01');
  assert.equal(n.name, 'kitchen-ipad');
  assert.equal(n.ip, '10.0.0.77');

  // A client with no lease at all still gets a usable label, never a stray object.
  const un = c.lastPayload.nodes.find(x => x.key === 'BB:00:00:00:00:02');
  assert.equal(typeof un.name, 'string');
  assert.ok(un.name.length, 'falls back to IP or MAC');
});

test('a lease record never leaks into the payload as an object', async () => {
  const dhcpLeases = { getNameByMAC: () => ({ name: '', ip: '10.0.0.9', hostName: 'x' }) };
  const { c } = buildWithClients([ROW_MERAKI_E5], CLIENT_HOSTS, { dhcpLeases });
  await pump(c);
  for (const n of c.lastPayload.nodes) {
    assert.equal(typeof n.name, 'string', n.key + ' name must be a string');
  }
  assert.ok(!JSON.stringify(c.lastPayload).includes('[object Object]'));
});

test('showClients:false skips the client tables entirely', async () => {
  const asked = [];
  const ros = mockRos(async (cmd) => {
    asked.push(cmd);
    if (cmd === '/ip/neighbor/print') return [ROW_MERAKI_E5];
    if (cmd === '/interface/bridge/host/print') return CLIENT_HOSTS;
    return [];
  });
  const { c } = build([], { ros, showClients: false });
  await pump(c);
  assert.ok(!asked.includes('/interface/wifi/registration-table/print'), 'no wireless queries');
  assert.equal(c.lastPayload.nodes.filter(n => n.kind === 'client').length, 0);
});

test('a router with no wireless still attributes wired clients', async () => {
  const ros = mockRos(async (cmd) => {
    if (cmd === '/ip/neighbor/print') return [ROW_MERAKI_E5];
    if (cmd === '/interface/bridge/host/print') return CLIENT_HOSTS;
    if (String(cmd).indexOf('wifi') !== -1 || String(cmd).indexOf('wireless') !== -1) {
      throw new Error('no such command prefix');
    }
    return [];
  });
  const { c } = build([], { ros });
  await pump(c);
  assert.equal(clientParent(c.lastPayload, 'BB:00:00:00:00:02'), '34:56:FE:2F:98:5C');
  assert.equal(clientParent(c.lastPayload, 'BB:00:00:00:00:01'), 'core');
});

test('the client tier is capped so a big LAN cannot swamp the payload', async () => {
  const many = [];
  for (let i = 0; i < 450; i++) {
    many.push({ 'mac-address': 'CC:00:00:00:' + String(Math.floor(i / 256)).padStart(2, '0') + ':' +
      String(i % 256).padStart(2, '0'), 'on-interface': 'ether3' });
  }
  const { c } = buildWithClients([ROW_MERAKI_E5], many);
  await pump(c);
  assert.equal(c.lastPayload.nodes.filter(n => n.kind === 'client').length, 400);
  assert.ok(c.lastPayload.clientsTruncated > 0, 'and it says so rather than silently dropping');
});

// ── VLANs ────────────────────────────────────────────────────────────────────

const VLAN_HOSTS = [
  { 'mac-address': '34:56:FE:2F:98:5B', 'on-interface': 'ether5', vid: '5' },
  { 'mac-address': 'BB:00:00:00:00:01', 'on-interface': 'ether3', vid: '10' },
  { 'mac-address': 'BB:00:00:00:00:02', 'on-interface': 'ether5', vid: '20' },
  // A trunked device legitimately appears on more than one VLAN.
  { 'mac-address': 'BB:00:00:00:00:03', 'on-interface': 'ether4', vid: '5' },
  { 'mac-address': 'BB:00:00:00:00:03', 'on-interface': 'ether4', vid: '10' },
  { 'mac-address': 'BB:00:00:00:00:04', 'on-interface': 'ether4' },   // untagged
];

test('clients carry their VLAN id resolved to the configured name', async () => {
  const { c } = buildWithClients([ROW_MERAKI_E5], VLAN_HOSTS);
  await pump(c);
  const n = c.lastPayload.nodes.find(x => x.key === 'BB:00:00:00:00:01');
  assert.deepEqual(n.vlans, [10]);
  assert.deepEqual(n.vlanNames, ['IoT']);
});

test('a device on several VLANs reports all of them, sorted', async () => {
  const { c } = buildWithClients([ROW_MERAKI_E5], VLAN_HOSTS);
  await pump(c);
  const n = c.lastPayload.nodes.find(x => x.key === 'BB:00:00:00:00:03');
  assert.deepEqual(n.vlans, [5, 10]);
  assert.deepEqual(n.vlanNames, ['Home', 'IoT']);
});

test('an untagged client reports no VLAN rather than a bogus one', async () => {
  const { c } = buildWithClients([ROW_MERAKI_E5], VLAN_HOSTS);
  await pump(c);
  const n = c.lastPayload.nodes.find(x => x.key === 'BB:00:00:00:00:04');
  assert.deepEqual(n.vlans, []);
  assert.deepEqual(n.vlanNames, []);
});

test('an unnamed VLAN falls back to its id, so the filter still works', async () => {
  const { c } = buildWithClients([ROW_MERAKI_E5], VLAN_HOSTS, { vlans: [] });
  await pump(c);
  const n = c.lastPayload.nodes.find(x => x.key === 'BB:00:00:00:00:01');
  assert.deepEqual(n.vlanNames, ['10']);
});

test('the payload lists only VLANs that clients were actually seen on', async () => {
  const { c } = buildWithClients([ROW_MERAKI_E5], VLAN_HOSTS);
  await pump(c);
  // 20 belongs to a client behind the switch; 5 and 10 to others. No VLAN with
  // zero clients should be offered as a filter option.
  assert.deepEqual(c.lastPayload.vlans,
    [{ vid: 5, name: 'Home' }, { vid: 10, name: 'IoT' }, { vid: 20, name: 'Guest' }]);
});

// ── parenting: a lone device fronts its port ─────────────────────────────────

test('a lone non-LLDP neighbour still owns the clients on its port', async () => {
  // hap-ac2 answers only CDP/MNDP. It is the ONLY discovered device on ether2,
  // so there is nothing to disambiguate and it must be able to front that port —
  // otherwise a whole AP's clients silently pile up on the router.
  const hapAc2 = { '.id': '*5', 'mac-address': '2C:C8:1B:5D:2A:41', identity: 'hap-ac2',
    board: 'RBD52G-5HacD2HnD', interface: 'ether2', 'discovered-by': 'cdp,mndp' };
  const hosts = [
    { 'mac-address': '2C:C8:1B:5D:2A:41', 'on-interface': 'ether2' },
    { 'mac-address': 'BB:00:00:00:00:09', 'on-interface': 'ether2' },
  ];
  const { c } = buildWithClients([hapAc2], hosts);
  await pump(c);
  const client = c.lastPayload.nodes.find(n => n.key === 'BB:00:00:00:00:09');
  assert.equal(client.parent, '2C:C8:1B:5D:2A:41');
  assert.equal(client.attrib, 'port');
});

test('with several devices on a port, LLDP still decides — no guessing', async () => {
  const other = { '.id': '*6', 'mac-address': 'DD:00:00:00:00:01', identity: 'Mystery',
    board: 'RB750', interface: 'ether2', 'discovered-by': 'mndp' };
  const hapAc2 = { '.id': '*5', 'mac-address': '2C:C8:1B:5D:2A:41', identity: 'hap-ac2',
    board: 'RBD52G-5HacD2HnD', interface: 'ether2', 'discovered-by': 'cdp,mndp' };
  const hosts = [
    { 'mac-address': '2C:C8:1B:5D:2A:41', 'on-interface': 'ether2' },
    { 'mac-address': 'DD:00:00:00:00:01', 'on-interface': 'ether2' },
    { 'mac-address': 'BB:00:00:00:00:09', 'on-interface': 'ether2' },
  ];
  const { c } = buildWithClients([hapAc2, other], hosts);
  await pump(c);
  const client = c.lastPayload.nodes.find(n => n.key === 'BB:00:00:00:00:09');
  assert.equal(client.parent, 'core', 'neither candidate is provably in front');
  assert.equal(client.attrib, 'direct');
});

test('a device already behind a switch never fronts that port itself', async () => {
  // The cAP sits behind the Meraki on ether5. Clients on ether5 belong to the
  // Meraki — the thing at the near end of the cable — not to the cAP.
  const { c } = buildWithClients([ROW_MERAKI_E5, ROW_CAP_HOME], CLIENT_HOSTS);
  await pump(c);
  const wired = c.lastPayload.nodes.find(n => n.key === 'BB:00:00:00:00:02');
  assert.equal(wired.parent, '34:56:FE:2F:98:5C');
});

test('how a client was attributed is recorded on the node', async () => {
  const { c } = buildWithClients([ROW_MERAKI_E5, ROW_CAP_HOME], CLIENT_HOSTS);
  await pump(c);
  const by = {};
  c.lastPayload.nodes.forEach(n => { by[n.key] = n; });
  assert.equal(by['AA:00:00:00:00:02'].attrib, 'radio',  'associated with an AP radio');
  assert.equal(by['BB:00:00:00:00:01'].attrib, 'direct', 'straight into a router port');
  assert.equal(by['BB:00:00:00:00:02'].attrib, 'port',   'deduced from a shared port');
});

test('client links are drawn uniformly, whatever the attribution', async () => {
  // The purple "inferred" styling is reserved for links between infrastructure.
  // Applying it to a whole tier of client links only added noise.
  const { c } = buildWithClients([ROW_MERAKI_E5, ROW_CAP_HOME], CLIENT_HOSTS);
  await pump(c);
  const clientEdges = c.lastPayload.edges.filter(e => e.client);
  assert.ok(clientEdges.length >= 3);
  assert.ok(clientEdges.every(e => e.inferred === false));
});

// ── emit discipline ──────────────────────────────────────────────────────────

test('emits to the page room, not router-wide', async () => {
  const { c, io } = build([ROW_HAP]);
  await pump(c);
  const ev = io.emitted.find(e => e.ev === 'topology:update');
  assert.ok(ev, 'emitted');
  assert.equal(ev.room, 'page-topology');
});

test('lastPayload is assigned even when the fingerprint is unchanged', async () => {
  const { c, io } = build([ROW_HAP]);
  await pump(c);
  const firstEmits = io.emitted.length;
  const firstTs = c.lastPayload.ts;

  await new Promise(r => setTimeout(r, 5));
  await pump(c);

  assert.equal(io.emitted.length, firstEmits, 'no second emit — nothing changed');
  assert.ok(c.lastPayload.ts >= firstTs, 'lastPayload still refreshed for sendInitialState()');
});

test('a structural change emits immediately despite the floor', async () => {
  const { c, io } = build([ROW_HAP]);
  await pump(c);
  const before = io.emitted.length;
  c._rows = [ROW_HAP, ROW_MERAKI];
  c._rebuild();
  assert.equal(io.emitted.length, before + 1);
});

test('no client in the room means no emit, but lastPayload stays fresh', async () => {
  const io = mockIo(0);
  const { c } = build([ROW_HAP], { io });
  await pump(c);
  assert.equal(io.emitted.length, 0);
  assert.ok(c.lastPayload, 'still built for replay on first connect');
});

test('state timestamps and errors are maintained', async () => {
  const { c, state } = build([ROW_HAP]);
  await pump(c);
  assert.ok(state.lastTopologyTs > 0);
  assert.equal(state.lastTopologyErr, null);

  const ros = mockRos(async () => { throw new Error('boom'); });
  const { c: c2, state: s2 } = build([], { ros });
  await pump(c2);
  assert.equal(s2.lastTopologyErr, 'boom');
});

// ── retention ────────────────────────────────────────────────────────────────

test('a departed device is retained as down, then dropped after the window', async () => {
  const { c } = build([ROW_HAP]);
  await pump(c);
  assert.equal(c.lastPayload.neighborCount, 1);

  c._rows = [];
  c._rebuild();
  const gone = c.lastPayload.nodes.find(n => n.key === '48:A9:8A:09:FE:27');
  assert.ok(gone, 'device is kept so the outage is visible rather than silent');
  assert.equal(gone.gone, true);
  assert.equal(gone.status, 'down');

  // Age it past the retention window.
  for (const s of c._seen.values()) s.lastSeen = Date.now() - RETAIN_MS - 1000;
  c._rebuild();
  assert.equal(c.lastPayload.nodes.length, 1, 'only the core remains');
});

// ── ping round-robin ─────────────────────────────────────────────────────────

test('each tick issues exactly one ping and the cursor advances', async () => {
  const calls = [];
  const ros = mockRos(async (cmd, args) => {
    if (cmd === '/ip/neighbor/print') return [ROW_CAP, ROW_MERAKI, ROW_HAP];
    if (cmd === '/tool/ping') { calls.push(args[0]); return [{ time: '3ms' }]; }
    return [];
  });
  const { c } = build([], { ros });
  await pump(c);

  await c._pingNextOnce();
  await c._pingNextOnce();
  await c._pingNextOnce();

  assert.equal(calls.length, 3, 'one probe per tick — never N concurrent streams');
  assert.equal(new Set(calls).size, 3, 'round-robin covers every device');
});

test('a ping reply records rtt and clears loss', async () => {
  const ros = mockRos(async (cmd) => {
    if (cmd === '/ip/neighbor/print') return [ROW_HAP];
    if (cmd === '/tool/ping') return [{ time: '4ms' }];
    return [];
  });
  const { c } = build([], { ros });
  await pump(c);
  await c._pingNextOnce();
  c._rebuild();
  const n = c.lastPayload.nodes[1];
  assert.equal(n.rtt, 4);
  assert.equal(n.loss, 0);
  assert.equal(n.status, 'up');
});

test('total loss marks the device down without removing it', async () => {
  const ros = mockRos(async (cmd) => {
    if (cmd === '/ip/neighbor/print') return [ROW_HAP];
    if (cmd === '/tool/ping') return [{ status: 'timeout' }];
    return [];
  });
  const { c } = build([], { ros });
  await pump(c);
  await c._pingNextOnce();
  c._rebuild();
  const n = c.lastPayload.nodes[1];
  assert.equal(n.loss, 100);
  assert.equal(n.status, 'down');
});

test('the test policy being absent disables latency without throwing', async () => {
  const ros = mockRos(async (cmd) => {
    if (cmd === '/ip/neighbor/print') return [ROW_HAP];
    if (cmd === '/tool/ping') throw new Error('not enough privileges (9)');
    return [];
  });
  const { c } = build([], { ros });
  await pump(c);
  await c._pingNextOnce();
  assert.equal(c._pingDenied, true);
  c._rebuild();
  assert.equal(c.lastPayload.pingDenied, true, 'the UI can explain why latency is blank');
});

test('one unreachable host does not stop the round-robin', async () => {
  let n = 0;
  const ros = mockRos(async (cmd) => {
    if (cmd === '/ip/neighbor/print') return [ROW_CAP, ROW_HAP];
    if (cmd === '/tool/ping') { n++; if (n === 1) throw new Error('host unreachable'); return [{ time: '2ms' }]; }
    return [];
  });
  const { c } = build([], { ros });
  await pump(c);
  await c._pingNextOnce();
  await c._pingNextOnce();
  assert.equal(c._pingDenied, false);
  assert.equal(n, 2, 'the loop kept going after a per-host failure');
});

test('ping targets are capped and skip devices with no IPv4', async () => {
  const rows = [];
  for (let i = 0; i < 40; i++) {
    rows.push({ '.id': '*' + i, 'mac-address': 'AA:BB:CC:00:00:' + String(i).padStart(2, '0'),
      interface: 'ether1', address: '10.0.1.' + i, board: 'CRS326' });
  }
  rows.push({ '.id': '*x', 'mac-address': 'AA:BB:CC:FF:FF:FF', interface: 'ether1', board: 'CRS326' });
  const { c } = build(rows);
  await pump(c);
  const targets = c._pingTargets();
  assert.equal(targets.length, 24);
  assert.ok(targets.every(t => /^\d+\.\d+\.\d+\.\d+$/.test(t.ip)));
});

// ── permission handling on the neighbour table itself ────────────────────────

test('an unreadable neighbour table degrades instead of retrying forever', async () => {
  const ros = mockRos(async () => { throw new Error('no such command prefix'); });
  const { c } = build([], { ros });
  await c._pollOnce();
  assert.equal(c._permissionDenied, true);
  c._rebuild();
  assert.equal(c.lastPayload.permissionDenied, true);
});

// ── stream / poll parity — the #105 contract ─────────────────────────────────

test('the stream path and the poll path produce an identical payload', async () => {
  const rows = [ROW_CAP, ROW_MERAKI, ROW_HAP];

  const polled = build(rows, { streamMode: false });
  await pump(polled.c);

  const streamed = build([], { streamMode: true });
  streamed.c._startStream();
  const s = streamed.ros._lastStream;
  assert.ok(s, 'stream opened');
  assert.ok(s._words.some(w => w.startsWith('=interval=')), 'streams with =interval=N');
  for (const r of rows) s._handlers.data(r);
  clearTimeout(streamed.c._debounce); streamed.c._debounce = null;
  streamed.c._rows = rows;
  streamed.c._rebuild();

  const strip = (p) => JSON.stringify({
    ...p, ts: 0, pollMs: 0, discovery: null,
    nodes: p.nodes.map(n => ({ ...n, firstSeen: 0, lastSeen: 0 })),
  });
  assert.equal(strip(streamed.c.lastPayload), strip(polled.c.lastPayload));
});

test('an empty stream batch clears departed neighbours', async () => {
  const { c } = build([ROW_HAP], { streamMode: true });
  await pump(c);
  c._startStream();
  c._rows = [ROW_HAP];
  c._stream._handlers.data([]);          // RStream's empty-table signal
  clearTimeout(c._rebuildDebounce); c._rebuildDebounce = null;
  c._rebuild();
  assert.equal(c.lastPayload.neighborCount, 1, 'retained as gone, not silently dropped');
  assert.equal(c.lastPayload.nodes[1].gone, true);
});

// ── lifecycle ────────────────────────────────────────────────────────────────

test('stop() clears every timer and stream', async () => {
  const { c } = build([ROW_HAP], { streamMode: true });
  await c.start();
  assert.ok(c._heartbeat, 'heartbeat running');
  c.stop();
  assert.equal(c._stream, null);
  assert.equal(c._heartbeat, null);
  assert.equal(c._debounce, null);
  assert.equal(c._restartTimer, null);
  assert.equal(c._rebuildDebounce, null);
  assert.equal(c._poll.running, false);
  assert.equal(c._pingLoop.running, false);
});

test('suspend stops delivery and resume restarts it', async () => {
  const { c } = build([ROW_HAP], { streamMode: true });
  await c.start();
  c.suspend();
  assert.equal(c._stream, null);
  assert.equal(c._pingLoop.running, false);
  c.resume();
  assert.ok(c._stream, 'stream reopened');
  c.stop();
});

test('start() registers its ros handlers exactly once', async () => {
  const { c, ros } = build([ROW_HAP], { streamMode: true });
  await c.start();
  assert.equal(ros.listenerCount('connected'), 1);
  assert.equal(ros.listenerCount('close'), 1);
  c.stop();
});

test('discovery settings are surfaced so an empty map can explain itself', async () => {
  const { c } = build([], { streamMode: false });
  await c._fetchDiscovery();
  await pump(c);
  assert.deepEqual(c.lastPayload.discovery.protocol, ['cdp', 'lldp', 'mndp']);
  assert.equal(c.lastPayload.discovery.mode, 'tx-and-rx');
  assert.equal(c.lastPayload.discovery.interfaceList, 'Management');
});
