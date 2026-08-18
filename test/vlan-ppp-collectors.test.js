'use strict';
// VLANs and PPP collectors (issue #32).
//
// The VLAN half is verifiable against a real router and the fixtures below are
// its actual output. The PPP half is not: the fleet this was written on runs no
// PPP at all, so every session-shaped assertion here IS the verification.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs   = require('fs');
const path = require('path');

const { track } = require('./helpers/collector-cleanup');
const VlansCollector = track(require('../src/collectors/vlans'));
const PppCollector   = track(require('../src/collectors/ppp'));
const Pages = require('../src/pages');

const { parseVlanIds, buildVlanRows } = VlansCollector;
const { parsePppSessions } = PppCollector;

// ── vlan-ids parsing ─────────────────────────────────────────────────────────
//
// RouterOS `vlan-ids` is a LIST. A plain parseInt reads the first entry and
// silently drops the rest, which on a trunk port loses most of the config.

test('a vlan-ids list and range both expand', () => {
  assert.deepStrictEqual(parseVlanIds('10').ids, [10]);
  assert.deepStrictEqual(parseVlanIds('5,10,20').ids, [5, 10, 20]);
  assert.deepStrictEqual(parseVlanIds('10-12').ids, [10, 11, 12]);
  assert.deepStrictEqual(parseVlanIds('1, 10-12,20').ids, [1, 10, 11, 12, 20]);
});

test('ids are deduped and numerically sorted, not lexically', () => {
  // '9' vs '10' is the case a string sort gets wrong.
  assert.deepStrictEqual(parseVlanIds('20,9,10,9').ids, [9, 10, 20]);
});

test('a wide range is not expanded', () => {
  // 2-4094 is legal on a trunk port. Expanding it yields 4093 ids from one row,
  // rebuilt every poll and shipped over the socket.
  const r = parseVlanIds('2-4094');
  assert.deepStrictEqual(r.ids, []);
  assert.strictEqual(r.truncated, true);
  assert.deepStrictEqual(r.ranges, [[2, 4094]]);
});

test('out-of-range, reversed and malformed entries are dropped, not guessed at', () => {
  // 0 is priority-tagged and 4095 is reserved, so neither is a VLAN. A reversed
  // range cannot be entered in WinBox, so inventing an interpretation for it is
  // worse than ignoring it.
  for (const bad of ['0', '4095', '9999', '20-10', 'junk', '', '  ', ',,']) {
    assert.deepStrictEqual(parseVlanIds(bad).ids, [], JSON.stringify(bad));
  }
  assert.deepStrictEqual(parseVlanIds(null).ids, []);
});

test('the raw string survives verbatim', () => {
  // It is what the operator sees in WinBox, so the UI must be able to show
  // exactly what is configured rather than a reconstruction of it.
  assert.strictEqual(parseVlanIds('1, 10-12,20').raw, '1, 10-12,20');
});

// ── the VLAN join ────────────────────────────────────────────────────────────

const VLAN_ROWS = [
  { name: 'Home',  'vlan-id': '5',  interface: 'Bridge', mtu: '1500', running: 'true', disabled: 'false' },
  { name: 'IoT',   'vlan-id': '10', interface: 'Bridge', mtu: '1500', running: 'true', disabled: 'false' },
  { name: 'Guest', 'vlan-id': '20', interface: 'Bridge', mtu: '1500', running: 'true', disabled: 'false' },
];
const BVLAN_ROWS = [
  { bridge: 'Bridge', 'vlan-ids': '5',  tagged: 'ether5', untagged: 'ether2,ether3,ether4', dynamic: 'false' },
  { bridge: 'Bridge', 'vlan-ids': '10', tagged: 'ether5', untagged: '', dynamic: 'false' },
  { bridge: 'Bridge', 'vlan-ids': '10', tagged: '2.4GHz WiFi6', untagged: '', dynamic: 'true' },
];
const BPORT_ROWS = [
  { bridge: 'Bridge', interface: 'ether2', pvid: '5',  'frame-types': 'admit-all', disabled: 'false' },
  { bridge: 'Bridge', interface: '2.4GHz WiFi3', pvid: '10', 'frame-types': 'admit-all', disabled: 'false' },
];
const IF_PAYLOAD = { interfaces: [
  { name: 'Home', rxMbps: 12.4, txMbps: 3.1, ips: ['10.0.5.1/24'], macAddr: 'AA:BB:CC:DD:EE:FF',
    rxBytes: 999, txBytes: 111, errors: 2, drops: 1, linkDowns: 3 },
  { name: 'IoT',  rxMbps: 0.8,  txMbps: 0.2, ips: ['10.0.10.1/24'], macAddr: 'AA:BB:CC:DD:EE:00' },
] };

const build = (over = {}) => buildVlanRows(
  over.vlanRows   || VLAN_ROWS,
  over.bridgeVlan || BVLAN_ROWS,
  over.bridgePort || BPORT_ROWS,
  'ifPayload' in over ? over.ifPayload : IF_PAYLOAD,
  'leases' in over ? over.leases : null);

const byId = (out) => Object.fromEntries(out.vlans.map(v => [v.vlanId, v]));

test('VLANs are joined from the interface, trunk and port tables at once', () => {
  const v = byId(build());
  assert.strictEqual(v[5].name, 'Home');
  assert.ok(v[5].tagged.includes('ether5'));
  assert.ok(v[5].untagged.includes('ether2'));
  assert.strictEqual(v[5].rxMbps, 12.4);
});

test('a pvid puts a port on a VLAN that has no bridge VLAN row of its own', () => {
  // This is how a WiFi virtual AP lands on a VLAN, and it is the only source
  // for a VLAN that exists purely at layer 2.
  const v = byId(build());
  assert.ok(v[10].untagged.includes('2.4GHz WiFi3'));
});

test('dynamic rows contribute membership even though the page hides them', () => {
  // On a real router most membership comes from the dynamic rows. Filtering
  // them at parse time instead of at render would show every VLAN with no
  // tagged ports at all.
  const out = build();
  assert.strictEqual(out.dynamicCount, 1);
  assert.ok(byId(out)[10].tagged.includes('2.4GHz WiFi6'),
    'the dynamic row still supplies its tagged port');
});

test('a bridge row spanning several ids contributes to every one of them', () => {
  const out = build({ bridgeVlan: [
    { bridge: 'Bridge', 'vlan-ids': '5,10,20', tagged: 'ether5', untagged: '', dynamic: 'false' },
  ] });
  for (const id of [5, 10, 20]) assert.ok(byId(out)[id].tagged.includes('ether5'), 'vlan ' + id);
  assert.strictEqual(out.bridgeVlans.length, 1, 'and occupies exactly one row in the bridge table');
});

test('an unreported rate is null, never zero', () => {
  // "The router did not tell us" and "this VLAN is idle" must stay tellable
  // apart, or the page confidently shows 0 Mbps on a busy VLAN while
  // interfaceStatus is still starting up.
  const v = byId(build());
  assert.strictEqual(v[20].rxMbps, null, 'Guest has no interface in the payload');
  assert.notStrictEqual(v[20].rxMbps, 0);

  const none = build({ ifPayload: null });
  assert.strictEqual(none.ratesAvailable, false);
  assert.strictEqual(byId(none)[5].rxMbps, null);
});

test('a missing or empty ifStatus yields a payload rather than a throw', () => {
  // ifStatus is DISABLEABLE, so it may be a null-collector stub whose
  // lastPayload never becomes anything.
  for (const p of [null, undefined, {}, { interfaces: null }]) {
    assert.doesNotThrow(() => build({ ifPayload: p }), JSON.stringify(p));
  }
});

test('two VLAN interfaces sharing one id are kept apart and never summed', () => {
  // Unreachable on the router this was built against — every id there has
  // exactly one interface — so only a fixture can catch it.
  const out = build({
    vlanRows: [
      { name: 'a', 'vlan-id': '7', interface: 'Bridge', running: 'true' },
      { name: 'b', 'vlan-id': '7', interface: 'ether5', running: 'true' },
    ],
    ifPayload: { interfaces: [{ name: 'a', rxMbps: 1, txMbps: 1 }, { name: 'b', rxMbps: 2, txMbps: 2 }] },
  });
  const v = byId(out)[7];
  assert.strictEqual(v.interfaces.length, 2, 'both interfaces are carried');
  assert.deepStrictEqual(v.interfaces.map(i => i.parent), ['Bridge', 'ether5']);
  assert.deepStrictEqual(v.interfaces.map(i => i.rxMbps), [1, 2], 'per-interface rates intact');
});

test('the empty-menu junk row produces no VLAN', () => {
  const out = build({ vlanRows: [{ undefined: '' }], bridgeVlan: [], bridgePort: [] });
  assert.deepStrictEqual(out.vlans, []);
});

test('no interface internals reach the VLAN payload', () => {
  // The whole reason the rate join happens server-side rather than by adding
  // page-vlans to interfaceStatus's rooms: `vlans` is a different permission
  // from `interfaces`, and that payload carries addresses and counters. This
  // also catches a careless {...iface} spread.
  const blob = JSON.stringify(build());
  for (const leak of ['ips', 'macAddr', 'rxBytes', 'txBytes', 'errors', 'drops', 'linkDowns',
                      'AA:BB:CC', '10.0.5.1']) {
    assert.ok(!blob.includes(leak), 'leaked: ' + leak);
  }
});

test('client counts survive the string-to-number join', () => {
  // dhcpLeases stores vlanId as the STRING '10' while every id here is a
  // number. Comparing them directly yields 0 for every VLAN, which reads as
  // "no DHCP clients on any VLAN" rather than as a bug — the most plausible
  // wrong answer this collector could give.
  const v = byId(build({ leases: new Map([['5', 23], ['10', 41]]) }));
  assert.strictEqual(v[5].clients, 23);
  assert.strictEqual(v[10].clients, 41);
  assert.strictEqual(v[20].clients, 0, 'a VLAN with no leases reports zero, not undefined');
});

// ── PPP sessions ─────────────────────────────────────────────────────────────

const sess = (over = {}) => Object.assign({
  '.id': '*1', name: 'alice', service: 'pppoe', address: '10.0.0.5',
  'caller-id': 'AA:BB:CC:DD:EE:FF', uptime: '1h2m', 'bytes-in': '1000', 'bytes-out': '500',
}, over);

test('the first sample reports no rate rather than a fabricated zero', () => {
  // There is no measurement window yet. Reporting 0 would claim an idle session
  // that may be saturating the line.
  const out = parsePppSessions([sess()], new Map(), 1000);
  assert.strictEqual(out[0].rxRate, null);
  assert.strictEqual(out[0].txRate, null);
});

test('rates are derived from byte deltas, because RouterOS reports totals only', () => {
  const prev = new Map();
  parsePppSessions([sess()], prev, 1000);
  const out = parsePppSessions([sess({ 'bytes-in': '11000', 'bytes-out': '5500' })], prev, 2000);
  assert.strictEqual(out[0].rxRate, 10000, 'bytes per second over a 1s window');
  assert.strictEqual(out[0].txRate, 5000);
});

test('a session that reconnects does not report a negative rate', () => {
  // Counters restart from zero on reassociation.
  const prev = new Map();
  parsePppSessions([sess({ 'bytes-in': '900000' })], prev, 1000);
  const out = parsePppSessions([sess({ 'bytes-in': '50' })], prev, 2000);
  assert.strictEqual(out[0].rxRate, 0);
  assert.ok(out[0].rxRate >= 0);
});

test('an idle session settles to zero rather than holding its last rate', () => {
  const prev = new Map();
  parsePppSessions([sess()], prev, 1000);
  const out = parsePppSessions([sess()], prev, 20000);   // unchanged bytes, >10s
  assert.strictEqual(out[0].rxRate, 0);
});

test('rate state for a departed session is not kept forever', () => {
  const prev = new Map();
  parsePppSessions([sess(), sess({ '.id': '*2', name: 'bob' })], prev, 1000);
  assert.strictEqual(prev.size, 2);
  parsePppSessions([sess()], prev, 2000);
  assert.strictEqual(prev.size, 1, 'bob is gone and so is his baseline');
});

test('the empty-menu junk row produces no PPP session', () => {
  assert.deepStrictEqual(parsePppSessions([{ undefined: '' }], new Map(), 1), []);
});

test('neither collector reads /ppp/secret', () => {
  // It stores account passwords in clear text. vpn.js recorded the decision in
  // a comment; this makes it a guarantee across both files.
  for (const f of ['ppp.js', 'vpn.js']) {
    const src = fs.readFileSync(path.join(__dirname, '..', 'src', 'collectors', f), 'utf8');
    const code = src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');
    assert.ok(!code.includes('/ppp/secret'), f + ' must not query /ppp/secret');
  }
});

// ── registry drift guards the review found nothing was protecting ────────────

test('ALL_NAV_PAGES in app.js matches the page registry', () => {
  // Not pinned by anything before this. A page missing here is hidden from the
  // nav regardless of role or toggle.
  const src = fs.readFileSync(path.join(__dirname, '..', 'public', 'app.js'), 'utf8');
  const m = src.match(/var ALL_NAV_PAGES = \[([\s\S]*?)\];/);
  assert.ok(m, 'found ALL_NAV_PAGES');
  const keys = [...m[1].matchAll(/'([a-z]+)'/g)].map(x => x[1]).sort();
  assert.deepStrictEqual(keys, [...Pages.KEYS].sort());
});

test('both settings-form page arrays cover every toggleable page', () => {
  // The highest-risk edit in adding a page: miss one of these two arrays and
  // the toggle renders in Settings but never loads or persists — which is
  // exactly the pageTopology bug src/pages.js was written to make impossible,
  // reappearing in the one place the registry does not reach.
  const src = fs.readFileSync(path.join(__dirname, '..', 'public', 'app.js'), 'utf8');
  const arrays = [...src.matchAll(/\[((?:'page[A-Za-z]+',?\s*)+)\]\.forEach/g)];
  assert.strictEqual(arrays.length, 2, 'expected exactly two settings-form page arrays');
  for (const a of arrays) {
    const keys = [...a[1].matchAll(/'(page[A-Za-z]+)'/g)].map(x => x[1]).sort();
    assert.deepStrictEqual(keys, [...Pages.SETTING_KEYS].sort());
  }
});

test('buildSession returns every collector the registry names', () => {
  // The bug this exists for: vlans and ppp were constructed, pushed onto
  // allCollectors, and left out of the returned session object. Nothing caught
  // it. per-router-collection.test.js does compare sessionProp names, but
  // against a hand-written list whose comment claims it "mirrors the session
  // object" — a claim no test checked. startCollectors then threw
  // "Cannot read properties of undefined (reading 'start')" on the first
  // missing prop and abandoned every collector after it.
  //
  // Read from source rather than by building a session: buildSession needs a
  // live ROS connection, and the failure is a source-level omission anyway.
  const src = fs.readFileSync(path.join(__dirname, '..', 'src', 'index.js'), 'utf8');
  const m = src.match(/return \{ ros, state, connTableCache[\s\S]*?\};/);
  assert.ok(m, 'found the buildSession return literal');
  const returned = m[0];
  const { COLLECTORS } = require('../src/collection');
  for (const c of COLLECTORS) {
    assert.ok(new RegExp('[{,\\s]' + c.sessionProp + '[,\\s}:]').test(returned),
      'buildSession must return ' + c.sessionProp + ' — startCollectors calls session.' +
      c.sessionProp + '.start()');
  }
});
