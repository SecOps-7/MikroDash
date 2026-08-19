'use strict';
/**
 * WAN — the uplink join, and the guard that stops you cutting your own path.
 *
 * Two areas carry the risk. THE JOIN: a WAN row is assembled from five separate
 * menus, and the default-route match in particular has three shapes that look
 * alike and are not. THE GUARD: it must warn when a lease action would drop the
 * session managing the router, and stay quiet otherwise — the second half being
 * the one that is easy to get wrong silently.
 */

const { test } = require('node:test');
const assert = require('node:assert');
const fs   = require('fs');
const path = require('path');

const Wan   = require('../src/collectors/wan');
const guard = require('../src/routeros/wanGuard');

const SRC = (...p) => fs.readFileSync(path.join(__dirname, '..', 'src', ...p), 'utf8');

/**
 * The body of _wanLeaseAction.
 *
 * Bounded by searching FORWARD from the declaration, not by indexOf on the
 * registration: that function's own doc comment quotes the string
 * `socket.on('wan:renew'` to explain why the handlers are registered by name,
 * and a plain indexOf finds the comment first and slices backwards into nothing.
 * A source-scan in a repo whose comments quote code has to be anchored.
 */
function leaseBody(src) {
  const at  = src.indexOf('const _wanLeaseAction');
  assert.ok(at > 0, '_wanLeaseAction is gone');
  const end = src.indexOf("socket.on('wan:renew'", at);
  assert.ok(end > at, 'the registration should follow the declaration');
  return src.slice(at, end);
}
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');

// Shapes exactly as the live hAP AX3 returned them.
const DETECT = [
  { '.id': '*1', name: 'WAN1',   state: 'internet', 'state-change-time': '2026-08-18 08:32:55' },
  { '.id': '*2', name: 'WG-SA',  state: 'internet', 'state-change-time': '2026-08-18 08:33:01' },
  { '.id': '*3', name: 'Bridge', state: 'lan' },
  { '.id': '*4', name: 'IoT',    state: 'wan' },
  { '.id': '*5', name: 'ether2', state: 'slave' },
];
const DHCP = [{ '.id': '*1', interface: 'WAN1', status: 'bound', address: '37.120.66.55/22',
                gateway: '37.120.64.1', 'primary-dns': '62.117.4.88', 'secondary-dns': '217.68.168.15',
                'expires-after': '2h45m40s', 'dhcp-server': '172.28.152.11',
                disabled: 'false', invalid: 'false' }];
const ROUTES = [{ 'dst-address': '0.0.0.0/0', gateway: '37.120.64.1', distance: '1', active: 'true' },
                { 'dst-address': '0.0.0.0/0', gateway: 'WG-SA',       distance: '2', active: 'false' },
                { 'dst-address': '10.0.0.0/24', gateway: 'Bridge',    distance: '0', active: 'true' }];
const ADDRS = [{ address: '37.120.66.55/22', interface: 'WAN1',   disabled: 'false' },
               { address: '10.255.255.5/24', interface: 'WG-SA',  disabled: 'false' },
               { address: '10.0.0.2/24',     interface: 'Bridge', disabled: 'false' }];
const IFACES = [{ name: 'WAN1', type: 'ether', running: 'true' },
                { name: 'WG-SA', type: 'wg', running: 'true' },
                { name: 'Bridge', type: 'bridge', running: 'true' }];
const build = (over) => Wan.buildWanRows(
  (over && over.detect) || DETECT, (over && over.dhcp) || DHCP, (over && over.routes) || ROUTES,
  (over && over.addrs) || ADDRS, (over && over.ifaces) || IFACES,
  over && 'ifPayload' in over ? over.ifPayload : { interfaces: [{ name: 'WAN1', rxMbps: 12.5, txMbps: 3.2, rxBytes: 99, txBytes: 88 }] });

// ── The set ──────────────────────────────────────────────────────────────────

test('only interfaces reporting state=internet are uplinks', () => {
  // The set is RouterOS's, and matches the Dashboard Network card exactly. A
  // page that disagreed with the card about what a WAN is would be worse than
  // one that shows nothing.
  const r = build();
  assert.deepStrictEqual(r.wans.map(w => w.name).sort(), ['WAN1', 'WG-SA']);
});

test('detection switched off is not the same as no uplinks', () => {
  // detect-interface-list defaults to none, so zero rows is the common case on
  // an unconfigured router — two of three in this fleet. The page explains it
  // rather than rendering an empty table.
  const r = build({ detect: [] });
  assert.deepStrictEqual(r.wans, []);
  assert.strictEqual(r.activeDefaultWan, '');
  assert.strictEqual(r.publicIp, '');
});

test('a tunnel is told apart from a physical uplink', () => {
  const r = build();
  const byName = Object.fromEntries(r.wans.map(w => [w.name, w]));
  assert.strictEqual(byName['WAN1'].isTunnel, false);
  assert.strictEqual(byName['WG-SA'].isTunnel, true);
  assert.strictEqual(byName['WG-SA'].type, 'wg');
});

// ── The default-route join ───────────────────────────────────────────────────

test('a default route matches its uplink all three ways it can point', () => {
  // The bug this exists for: the first version compared a route's gateway
  // against our OWN address. A gateway is the next hop, so nothing ever matched
  // and every uplink reported as standby with no distance.
  const r = build({
    detect: [{ name: 'WAN1', state: 'internet' }, { name: 'WG-SA', state: 'internet' },
             { name: 'STATIC', state: 'internet' }],
    routes: [{ 'dst-address': '0.0.0.0/0', gateway: '37.120.64.1', distance: '1', active: 'true' },
             { 'dst-address': '0.0.0.0/0', gateway: 'WG-SA',       distance: '2', active: 'false' },
             { 'dst-address': '0.0.0.0/0', gateway: '198.51.100.1', distance: '3', active: 'false' }],
    addrs: ADDRS.concat([{ address: '198.51.100.9/24', interface: 'STATIC', disabled: 'false' }]),
    ifaces: IFACES.concat([{ name: 'STATIC', type: 'ether', running: 'true' }]),
  });
  const by = Object.fromEntries(r.wans.map(w => [w.name, w]));
  assert.strictEqual(by['WAN1'].routeDistance, '1', 'dhcp uplink matched by the lease gateway');
  assert.strictEqual(by['WG-SA'].routeDistance, '2', 'tunnel matched by interface name');
  assert.strictEqual(by['STATIC'].routeDistance, '3', 'static matched by a gateway inside its subnet');
  for (const w of r.wans) assert.strictEqual(w.hasDefaultRoute, true);
});

test('an uplink with no default route says so rather than reporting standby', () => {
  const r = build({ routes: [{ 'dst-address': '10.0.0.0/24', gateway: 'Bridge', active: 'true' }] });
  for (const w of r.wans) {
    assert.strictEqual(w.hasDefaultRoute, false);
    assert.strictEqual(w.routeActive, false);
    assert.strictEqual(w.routeDistance, '');
  }
  assert.strictEqual(r.activeDefaultWan, '');
});

test('the active uplink leads, then standby by distance', () => {
  // The order an operator reads them in: what is carrying traffic now, then
  // what would take over next.
  const r = build();
  assert.strictEqual(r.wans[0].name, 'WAN1');
  assert.strictEqual(r.wans[0].routeActive, true);
  assert.strictEqual(r.activeDefaultWan, 'WAN1');
});

// ── Borrowed rates ───────────────────────────────────────────────────────────

test('rates are null, not zero, when Interface Rates is not collecting', () => {
  // "The router did not report this" and "this uplink is idle" must stay
  // tellable apart, or the page shows a confident 0 Mb/s on a saturated link.
  const r = build({ ifPayload: null });
  assert.strictEqual(r.ratesAvailable, false);
  for (const w of r.wans) {
    assert.strictEqual(w.rxMbps, null);
    assert.strictEqual(w.txMbps, null);
  }
  assert.strictEqual(r.wans.length, 2, 'the page still renders without rates');
});

test('borrowing brings rates and nothing else', () => {
  // ifStatus carries MAC addresses, per-interface IP lists and error counters,
  // and `wan` is a different permission from `interfaces`. Fields are projected
  // by name; spreading the interface object would silently re-leak all of it.
  const r = build({ ifPayload: { interfaces: [{
    name: 'WAN1', rxMbps: 1, txMbps: 2, rxBytes: 3, txBytes: 4,
    macAddr: 'AA:BB:CC:DD:EE:FF', ips: ['37.120.66.55/22'], errors: 17, drops: 9, linkDowns: 5,
  }] } });
  const s = JSON.stringify(r);
  for (const leaked of ['macAddr', 'AA:BB:CC:DD:EE:FF', '"ips"', 'errors', 'drops', 'linkDowns']) {
    assert.ok(!s.includes(leaked), 'the payload leaked ' + leaked + ' from the interface collector');
  }
  assert.strictEqual(r.wans.find(w => w.name === 'WAN1').rxMbps, 1);
});

// ── Address classification ───────────────────────────────────────────────────

test('a public address is told apart from CGNAT and RFC1918', () => {
  // The point is to show an operator their real public address. A wrong answer
  // is worse than none, so anything unrecognised is null rather than a guess.
  assert.strictEqual(Wan.isPublicV4('37.120.66.55/22'), true);
  assert.strictEqual(Wan.isPublicV4('8.8.8.8'), true);
  for (const priv of ['10.0.0.5', '192.168.1.1', '172.16.0.1', '172.31.255.1',
                      '127.0.0.1', '169.254.1.1', '100.64.0.1', '224.0.0.1']) {
    assert.strictEqual(Wan.isPublicV4(priv), false, priv + ' must not read as public');
  }
  assert.strictEqual(Wan.isPublicV4('2001:db8::1'), null, 'v6 is not judged');
  assert.strictEqual(Wan.isPublicV4(''), null);
});

test('the headline public address skips tunnel addresses', () => {
  const r = build();
  assert.strictEqual(r.publicIp, '37.120.66.55/22');
});

// ── The self-cutoff guard ────────────────────────────────────────────────────

const LAN = ['10.0.0.2/24', '192.168.88.1/24'];
const pathFor = (addrs, cidrs) => guard.resolveManagementPath({
  selfAddresses: { addresses: addrs, resolved: addrs.length > 0 },
  connectedCidrs: cidrs === undefined ? LAN : cidrs,
});

test('a session from a connected subnet is local', () => {
  const p = pathFor(['10.0.0.5']);
  assert.deepStrictEqual(p, { resolved: true, local: true, address: '10.0.0.5' });
});

test('a session from outside every connected subnet arrives over a WAN', () => {
  const p = pathFor(['203.0.113.9']);
  assert.strictEqual(p.local, false);
  assert.strictEqual(p.address, '203.0.113.9');
});

test('one off-subnet session among several is enough to count as remote', () => {
  // We hold several sessions per router and they need not share a path. The one
  // that would be cut is the one that matters.
  assert.strictEqual(pathFor(['10.0.0.5', '203.0.113.9']).local, false);
});

test('unknown inputs report unresolved rather than remote', () => {
  // Concluding "not local" from no information would warn on every action.
  assert.strictEqual(pathFor([]).resolved, false);
  assert.strictEqual(pathFor(['10.0.0.5'], []).resolved, false);
});

test('a local session is never warned about', () => {
  // The control that matters most: on a LAN-managed router — every router in
  // this fleet — renew and release must not prompt.
  const v = guard.checkLeaseAction({ path: pathFor(['10.0.0.5']), targetWan: 'WAN1', activeDefaultWan: 'WAN1' });
  assert.strictEqual(v.level, 'none');
});

test('a remote session is warned about only for the uplink carrying it', () => {
  const remote = pathFor(['203.0.113.9']);
  const hit  = guard.checkLeaseAction({ path: remote, targetWan: 'WAN1', activeDefaultWan: 'WAN1' });
  const miss = guard.checkLeaseAction({ path: remote, targetWan: 'WAN2', activeDefaultWan: 'WAN1' });
  assert.strictEqual(hit.level, 'warn');
  assert.strictEqual(hit.code, 'self-cutoff');
  assert.strictEqual(hit.detail.address, '203.0.113.9');
  assert.strictEqual(hit.detail.certain, true);
  assert.strictEqual(miss.level, 'none', 'another uplink is not on our return path');
});

test('when the carrying uplink cannot be determined, every uplink is warned about', () => {
  // Verified live: four default routes can be active at distance 1 at once, so
  // naming one would warn about the wrong uplink and stay silent on the right.
  const v = guard.checkLeaseAction({ path: pathFor(['203.0.113.9']), targetWan: 'WAN2', activeDefaultWan: '' });
  assert.strictEqual(v.level, 'warn');
  assert.strictEqual(v.detail.certain, false, 'the page must not claim certainty it does not have');
});

test('it fails OPEN when the session address is unknown', () => {
  // /user/active is denied to a read-only API user, which is the common case.
  // Failing closed would prompt on every routine renew on most installs.
  const v = guard.checkLeaseAction({ path: pathFor([]), targetWan: 'WAN1', activeDefaultWan: 'WAN1' });
  assert.strictEqual(v.level, 'none');
});

test('an acknowledgement binds to the uplink and path it was issued for', () => {
  const remote = pathFor(['203.0.113.9']);
  const a = guard.checkLeaseAction({ path: remote, targetWan: 'WAN1', activeDefaultWan: 'WAN1' });
  const b = guard.checkLeaseAction({ path: remote, targetWan: 'WAN1', activeDefaultWan: 'WAN1' });
  const c = guard.checkLeaseAction({ path: remote, targetWan: 'WAN2', activeDefaultWan: '' });
  assert.strictEqual(a.fingerprint, b.fingerprint, 'stable for identical inputs');
  assert.notStrictEqual(a.fingerprint, c.fingerprint, 'cannot be replayed against another uplink');
});

// ── The collector reads, and only reads ──────────────────────────────────────

test('the wan collector issues no write commands, ever', () => {
  const code = stripComments(SRC('collectors', 'wan.js'));
  for (const verb of ['/ip/dhcp-client/renew', '/ip/dhcp-client/release',
                      '/ip/dhcp-client/set', '/ip/address/set', '/ip/route/set']) {
    assert.ok(!code.includes("'" + verb + "'"), 'wan.js must not issue ' + verb);
  }
});

// ── The handlers ─────────────────────────────────────────────────────────────

const WRITE_EVENTS = ['wan:renew', 'wan:release'];

test('both lease actions are gated on router:write and the page toggle', () => {
  const src = SRC('index.js');
  assert.ok(/_wanMayWrite = \(rid\) =>\s*\n?\s*_pageAllowed\(socket, 'wan', 'write'\) && _socketCan\(socket, 'router:write', rid\)/
    .test(src), '_wanMayWrite is the page gate AND router:write');
  assert.ok(leaseBody(src).includes('_wanMayWrite(rid)'), 'the shared body checks both gates');
});

test('each verb is registered by name, not built in a loop', () => {
  // Every drift guard in this repo greps for a literal handler name, and so does
  // the next person looking for where this is handled.
  const src = SRC('index.js');
  for (const ev of WRITE_EVENTS) {
    assert.ok(src.includes("socket.on('" + ev + "'"), ev + ' is not registered by name');
    assert.ok(new RegExp("socket\\.on\\('" + ev + "',\\s*\\(req\\) => _routerWriteQueue\\(socket\\.routerId,")
      .test(src), ev + ' must queue with the router id captured at enqueue');
  }
});

test('the guard runs against a fresh read, before the write, and audits both ways', () => {
  const src = stripComments(SRC('index.js'));
  const body = leaseBody(src);
  const read  = body.indexOf('_wanRead(session, rid)');
  const check = body.indexOf('wanGuard.checkLeaseAction');
  const write = body.indexOf('session.ros.write(_WAN_VERBS');
  assert.ok(read > 0 && check > 0 && write > 0, 'read, check and write are all present');
  assert.ok(read < check, 'the read precedes the check');
  assert.ok(check < write, 'the check precedes the write');
  assert.ok(body.includes('audit.fromSocket(socket).denied'), 'refusals are recorded');
  assert.ok(body.includes('audit.fromSocket(socket).record'), 'successes are recorded');
  assert.ok(body.includes("_wanErr('stale-row')"), 'a row that changed underneath is refused');
  // The prompt is not a denial, so it must not write an audit row.
  const promptAt = body.indexOf("_wanErr('self-cutoff'");
  assert.ok(promptAt > 0, 'the self-cutoff prompt exists');
});

test('no lease action resolves its target from the collector payload', () => {
  // The payload is what goes stale in the dangerous direction; the guard's
  // inputs must come from a read taken in the same tick as the write.
  const src = stripComments(SRC('index.js'));
  assert.ok(!/lastPayload/.test(leaseBody(src)), 'a lease action must not read lastPayload');
});

test('the connected subnets come from the router, not from a cache', () => {
  const src = SRC('index.js');
  const body = src.slice(src.indexOf('const _wanRead'), src.indexOf('const _wanRow'));
  assert.ok(body.includes("'/ip/address/print'"), 'subnets are read fresh');
  assert.ok(body.includes("'/user/active/print'"), 'the session address is read fresh');
  assert.ok(body.includes('activeDefaults.length === 1'),
    'the carrying uplink is named only when exactly one default route is active');
});

// ── Registry ─────────────────────────────────────────────────────────────────

test('the page and collector are registered and page-scoped', () => {
  const Pages = require('../src/pages');
  const { COLLECTORS, BY_KEY } = require('../src/collection');
  const page = Pages.BY_KEY.wan;
  assert.ok(page, 'wan is a registered page');
  assert.strictEqual(page.settingsKey, 'pageWan');
  assert.strictEqual(page.category, 'network');
  assert.deepStrictEqual(page.streamRooms, ['page-wan'], 'suspends when nobody is on the page');
  const col = BY_KEY.wan;
  assert.strictEqual(col.page, 'wan');
  assert.strictEqual(col.sessionProp, 'wan', 'must equal the page key or page:focus never replays it');
  assert.strictEqual(col.streamKey, 'streamWan');
  assert.strictEqual(col.disableable, true);
  assert.ok(COLLECTORS.some(c => c.key === 'wan'));
});

test('wan does not hard-require ifStatus', () => {
  // It borrows rates by reference. A hard require would cascade into a disable,
  // blanking the page when only the rate column should have degraded.
  const { BY_KEY } = require('../src/collection');
  assert.deepStrictEqual(BY_KEY.wan.requires, []);
});

test('the poll interval is settable and bounded', () => {
  const Settings = require('../src/settings');
  assert.strictEqual(typeof Settings.DEFAULTS.pollWan, 'number');
  assert.strictEqual(Settings.DEFAULTS.pageWan, true);
  const src = SRC('index.js');
  assert.ok(src.includes('pollWan:[1000,60000]'), 'bounded in intFields');
  assert.ok(src.includes("pollWan:'wan'"), 'reaches the live collector on save');
});
