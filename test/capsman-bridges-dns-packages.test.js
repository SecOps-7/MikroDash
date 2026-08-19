'use strict';
// CAPsMAN, Bridges, DNS and Packages collectors.
//
// Every fixture below is the live fleet's actual output, trimmed. The two things
// hardware could not show are a legacy /caps-man manager (absent fleet-wide) and
// a CAP that reports no `cap` field, so those get synthetic rows.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs   = require('fs');
const path = require('path');

const { track } = require('./helpers/collector-cleanup');
const CapsmanCollector  = track(require('../src/collectors/capsman'));
const BridgesCollector  = track(require('../src/collectors/bridges'));
const DnsCollector      = track(require('../src/collectors/dns'));
const PackagesCollector = track(require('../src/collectors/packages'));

const { buildCapsmanView, parseCapField } = CapsmanCollector;
const { buildBridgeRows } = BridgesCollector;
const { parseDnsSettings, parseStaticEntries } = DnsCollector;
const { parsePackages, parseFirmware, parseUpdate, scheduledAction } = PackagesCollector;

// A ROS stub that records every command issued, which is the only way to assert
// that something was NOT asked for.
function fakeRos(answers) {
  const issued = [];
  return {
    connected: true,
    routerLabel: 'test',
    issued,
    on() {},
    async write(cmd) {
      issued.push(cmd);
      if (answers[cmd] instanceof Error) throw answers[cmd];
      return answers[cmd] || [];
    },
  };
}
const fakeIo = () => ({ to() { return this; }, emit() {} });

// ── CAPsMAN ──────────────────────────────────────────────────────────────────

test('the cap field parses into identity, base MAC and id', () => {
  assert.deepStrictEqual(parseCapField('cAP@48:A9:8A:E5:CE:34%*41'),
    { identity: 'cAP', baseMac: '48:A9:8A:E5:CE:34', id: '*41' });
  // No %id half — older RouterOS, and still usable.
  assert.deepStrictEqual(parseCapField('cAP@48:A9:8A:E5:CE:34'),
    { identity: 'cAP', baseMac: '48:A9:8A:E5:CE:34', id: '' });
});

test('a cap field that is not identity@mac yields null, never a partial CAP', () => {
  // Returning {identity:'', baseMac:undefined} here would invent a CAP called
  // undefined and attribute every radio to it.
  for (const junk of ['', null, undefined, 'nonsense', '@48:A9:8A:E5:CE:34', 'cAP@']) {
    assert.strictEqual(parseCapField(junk), null, JSON.stringify(junk));
  }
});

test('role distinguishes manager, CAP, both and neither', () => {
  const role = (mgr, cap) => buildCapsmanView(mgr, cap, [], [], [], [], []).role;
  assert.strictEqual(role({ enabled: 'yes' }, { enabled: 'no' }),  'manager');
  assert.strictEqual(role({ enabled: 'no' },  { enabled: 'yes' }), 'cap');
  // The live hAP AX3: a manager whose own radios are a CAP pointed at 127.0.0.1.
  assert.strictEqual(role({ enabled: 'yes' }, { enabled: 'yes' }), 'both');
  assert.strictEqual(role({ enabled: 'no' },  { enabled: 'no' }),  'none');
  assert.strictEqual(role(null, null), 'none');
});

test('radios and clients are attributed to the CAP the router names', () => {
  const view = buildCapsmanView(
    { enabled: 'yes' }, { enabled: 'no' },
    [{ identity: 'cAP', 'base-mac': '48:A9:8A:E5:CE:34', 'board-name': 'cAPGi-5HaxD2HaxD',
       state: 'Ok', version: '7.24' }],
    [],
    [{ 'radio-mac': '48:A9:8A:09:FE:2B', interface: '5GHz WiFi' },
     { 'radio-mac': '48:A9:8A:E5:CE:36', interface: '5GHz WiFi4', cap: 'cAP@48:A9:8A:E5:CE:34%*41' }],
    [{ name: '5GHz WiFi',  'radio-mac': '48:A9:8A:09:FE:2B' },
     { name: '5GHz WiFi4', 'radio-mac': '48:A9:8A:E5:CE:36', cap: 'cAP@48:A9:8A:E5:CE:34%*41' }],
    [{ interface: '5GHz WiFi4', 'mac-address': 'AA:BB:CC:00:00:01', signal: '-48' },
     { interface: '5GHz WiFi',  'mac-address': 'AA:BB:CC:00:00:02', signal: '-60' }]);

  assert.strictEqual(view.caps.length, 1);
  assert.deepStrictEqual(view.caps[0].radios.map(r => r.interface), ['5GHz WiFi4']);
  assert.strictEqual(view.caps[0].clientCount, 1);
  assert.deepStrictEqual(view.localRadios.map(r => r.interface), ['5GHz WiFi']);
  assert.strictEqual(view.totals.clientsOnCaps, 1);
  assert.strictEqual(view.totals.clientsLocal, 1);
});

test('a virtual AP on a CAP counts to that CAP, not to the manager', () => {
  // Only the MASTER interface carries `cap`. Without chasing master-interface,
  // every guest-SSID client on a CAP would be attributed to the manager — which
  // on the live router is most of them.
  const view = buildCapsmanView(
    { enabled: 'yes' }, null,
    [{ identity: 'cAP', 'base-mac': '48:A9:8A:E5:CE:34' }],
    [], [],
    [{ name: '2.4GHz WiFi4', 'radio-mac': '48:A9:8A:E5:CE:37', cap: 'cAP@48:A9:8A:E5:CE:34%*41' },
     { name: '2.4GHz WiFi6', 'master-interface': '2.4GHz WiFi4' }],
    [{ interface: '2.4GHz WiFi6', 'mac-address': 'AA:BB:CC:00:00:03', ssid: 'Guest' }]);
  assert.strictEqual(view.caps[0].clientCount, 1);
  assert.strictEqual(view.totals.clientsLocal, 0);
});

test('attribution falls back to the MAC prefix when the router omits cap', () => {
  // Older RouterOS does not report the field; topology.js has always matched on
  // the first five octets, and that stays as the fallback rather than losing the
  // CAP entirely.
  const view = buildCapsmanView(
    { enabled: 'yes' }, null,
    [{ identity: 'cAP', 'base-mac': '48:A9:8A:E5:CE:34' }],
    [], [{ 'radio-mac': '48:A9:8A:E5:CE:36', interface: 'wifi9' }], [], []);
  assert.deepStrictEqual(view.caps[0].radios.map(r => r.interface), ['wifi9']);
});

test('a CAP with no clients reports 0, and is still listed', () => {
  const view = buildCapsmanView({ enabled: 'yes' }, null,
    [{ identity: 'quiet-cap', 'base-mac': 'AA:AA:AA:AA:AA:AA', state: 'Ok' }], [], [], [], []);
  assert.strictEqual(view.caps.length, 1);
  assert.strictEqual(view.caps[0].clientCount, 0);
  assert.deepStrictEqual(view.caps[0].clients, []);
  assert.strictEqual(view.totals.capsOk, 1);
});

test('the empty-menu junk row produces no CAP and no provisioning rule', () => {
  const view = buildCapsmanView(null, null, [{ undefined: '' }], [{ undefined: '' }],
                                [{ undefined: '' }], [{ undefined: '' }], [{ undefined: '' }]);
  assert.deepStrictEqual(view.caps, []);
  assert.deepStrictEqual(view.provisioning, []);
  assert.strictEqual(view.totals.clients, 0);
});

test('no wireless passphrase can reach the CAPsMAN payload', () => {
  // /interface/wifi/configuration holds security.passphrase in clear text.
  // Provisioning references configurations by NAME, so that table is never
  // fetched — but a later change could spread a row in, and this is what would
  // catch it.
  const view = buildCapsmanView(
    { enabled: 'yes', 'security.passphrase': 'hunter2' },
    { enabled: 'no' },
    [{ identity: 'cAP', 'base-mac': 'AA:AA:AA:AA:AA:AA', 'security.passphrase': 'hunter2' }],
    [{ action: 'create-dynamic-enabled', 'master-configuration': 'Home', 'security.passphrase': 'hunter2' }],
    [{ 'radio-mac': 'AA:AA:AA:AA:AA:AB', interface: 'w1', 'security.passphrase': 'hunter2' }],
    [{ name: 'w1', 'security.passphrase': 'hunter2' }],
    [{ interface: 'w1', 'mac-address': 'BB:BB:BB:BB:BB:BB', 'security.passphrase': 'hunter2' }]);
  const json = JSON.stringify(view);
  assert.ok(!/passphrase/i.test(json), 'no passphrase key survives the projection');
  assert.ok(!json.includes('hunter2'), 'no passphrase value survives the projection');
});

test('the CAPsMAN collector stops asking when the wifi stack is absent', async () => {
  const err = new Error('no such command prefix');
  const ros = fakeRos({
    '/interface/wifi/capsman/print': err, '/interface/wifi/cap/print': err,
    '/interface/wifi/capsman/remote-cap/print': err, '/interface/wifi/provisioning/print': err,
    '/interface/wifi/radio/print': err, '/interface/wifi/print': err,
    '/interface/wifi/registration-table/print': err,
  });
  const c = new CapsmanCollector({ ros, io: fakeIo(), state: {}, pollMs: 5000 });
  await c._tick();
  const first = ros.issued.length;
  await c._tick();
  assert.strictEqual(ros.issued.length, first, 'a latched-off menu is not asked again');
  assert.strictEqual(c.lastPayload.available, false);
  c.stop();
});

// ── Bridges ──────────────────────────────────────────────────────────────────

test('a junk key merged into a real bridge row does not break it', () => {
  // Observed live on the cAP AX: RouterOS merged {undefined:''} INTO the first
  // row rather than sending it separately, so filtering whole junk rows is not
  // enough — the projection has to be by name, which it is.
  const built = buildBridgeRows(
    [{ undefined: '', '.id': '*6', name: 'bridgeLocal', 'protocol-mode': 'rstp',
       'vlan-filtering': 'false', running: 'true', 'actual-mtu': '1500' }], [], [], null);
  assert.strictEqual(built.bridges.length, 1);
  assert.strictEqual(built.bridges[0].name, 'bridgeLocal');
  assert.strictEqual(built.bridges[0].mtu, 1500);
  assert.ok(!('undefined' in built.bridges[0]));
});

test('a port on a bridge with no spanning tree reports no role, not a wrong one', () => {
  const built = buildBridgeRows([], [{ interface: 'ether2', bridge: 'b', role: '' }], [], null);
  assert.strictEqual(built.ports[0].role, '');
});

test('bridge rates are borrowed from ifStatus, and are null when it has none', () => {
  const rows = [{ name: 'Bridge', running: 'true' }];
  const withRates = buildBridgeRows(rows, [], [],
    { interfaces: [{ name: 'Bridge', rxMbps: 12.5, txMbps: 3.25 }] });
  assert.strictEqual(withRates.bridges[0].rxMbps, 12.5);
  assert.strictEqual(withRates.ratesAvailable, true);

  // null, never 0 — ifStatus disabled must not read as "this bridge is idle".
  const without = buildBridgeRows(rows, [], [], null);
  assert.strictEqual(without.bridges[0].rxMbps, null);
  assert.strictEqual(without.bridges[0].txMbps, null);
  assert.strictEqual(without.ratesAvailable, false);
});

test('port counts are per bridge, not a total', () => {
  const built = buildBridgeRows(
    [{ name: 'br1' }, { name: 'br2' }],
    [{ interface: 'e1', bridge: 'br1' }, { interface: 'e2', bridge: 'br1' },
     { interface: 'e3', bridge: 'br2' }], [], null);
  const by = Object.fromEntries(built.bridges.map(b => [b.name, b.portCount]));
  assert.deepStrictEqual(by, { br1: 2, br2: 1 });
});

test('the host table is capped and reports its true total', () => {
  const many = Array.from({ length: 640 }, (_, i) => ({
    'mac-address': 'AA:BB:CC:00:00:' + String(i % 256).padStart(2, '0') + ':' + i,
    'on-interface': 'ether2', dynamic: 'true',
  }));
  const built = buildBridgeRows([], [], many, null);
  assert.strictEqual(built.hosts.length, BridgesCollector.HOST_CAP);
  // The total is what makes "showing 500 of 640" honest rather than a silent
  // truncation that reads as the whole table.
  assert.strictEqual(built.hostTotal, 640);
});

test('the router own port MACs are dropped before learned clients are', () => {
  const built = buildBridgeRows([], [], [
    { 'mac-address': 'AA:00:00:00:00:01', local: 'true' },
    { 'mac-address': 'BB:00:00:00:00:02', dynamic: 'true' },
  ], null);
  assert.strictEqual(built.hosts[0].mac, 'BB:00:00:00:00:02', 'learned entries sort first');
});

// ── DNS ──────────────────────────────────────────────────────────────────────

test('a router with no DoH says so rather than rendering blank fields', () => {
  const off = parseDnsSettings({ 'use-doh-server': '', servers: '10.0.0.1' });
  assert.strictEqual(off.dohEnabled, false);
  const on = parseDnsSettings({ 'use-doh-server': 'https://d.example/dns-query', 'verify-doh-cert': 'true' });
  assert.strictEqual(on.dohEnabled, true);
  assert.strictEqual(on.dohVerifyCert, true);
});

test('the DNS collector never enumerates the cache', () => {
  // The cache-used FIGURE comes from the settings row; the cache CONTENTS are a
  // log of everywhere the network has been, and this page has no use for them.
  // Asserted against the source rather than the payload, because the cost is in
  // the query being issued at all.
  const src = fs.readFileSync(path.join(__dirname, '..', 'src', 'collectors', 'dns.js'), 'utf8');
  const code = src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');
  assert.ok(!code.includes('/ip/dns/cache'), 'dns.js must not read /ip/dns/cache');
  // The settings fields the gauge needs are still there.
  const parsed = parseDnsSettings({ 'cache-size': '2048', 'cache-used': '341' });
  assert.strictEqual(parsed.cacheUsed, 341);
  assert.strictEqual(parsed.cacheSize, 2048);
});

test('static entries keep regexp rows, which have no name', () => {
  const out = parseStaticEntries([
    { name: 'MikroTik', address: '10.0.0.2', type: 'A', ttl: '1d' },
    { regexp: '.*\\.ads\\.example', address: '0.0.0.0' },
    { undefined: '' },
  ]);
  assert.strictEqual(out.length, 2);
  assert.ok(out.some(e => e.regexp), 'a regexp entry is not dropped for having no name');
});

// ── Packages ─────────────────────────────────────────────────────────────────

test('the five package states are told apart', () => {
  const out = parsePackages([
    { name: 'routeros',  version: '7.24', disabled: 'false', available: 'false' },
    { name: 'off-pkg',   version: '7.24', disabled: 'true',  available: 'false' },
    { name: 'calea',     version: '',     disabled: 'true',  available: 'true'  },
    { name: 'going',     version: '7.24', disabled: 'false', available: 'false',
      scheduled: 'scheduled for uninstall' },
    { name: 'weird',     version: '',     disabled: 'false', available: 'false' },
  ]);
  const by = Object.fromEntries(out.map(p => [p.name, p.state]));
  assert.strictEqual(by.routeros, 'installed');
  assert.strictEqual(by['off-pkg'], 'disabled');
  // available=true means "on MikroTik's server", NOT "installed here" — the one
  // field most likely to be read backwards.
  assert.strictEqual(by.calea, 'available');
  assert.strictEqual(by.weird, 'unknown');
  assert.strictEqual(out[0].name, 'going', 'a scheduled change sorts to the top');
});

test('the scheduled verb is derived, and uninstall is not read as install', () => {
  // RouterOS answers with a sentence: Use "apply-changes" to proceed with install
  assert.strictEqual(scheduledAction('Use "apply-changes" to proceed with install'), 'install');
  // 'uninstall' contains 'install', so the order of the tests is load-bearing.
  assert.strictEqual(scheduledAction('scheduled for uninstall'), 'uninstall');
  assert.strictEqual(scheduledAction('scheduled for disable'), 'disable');
  assert.strictEqual(scheduledAction(''), '');
});

test('firmware only claims an upgrade when both versions are known and differ', () => {
  assert.strictEqual(parseFirmware({ 'current-firmware': '7.21.3', 'upgrade-firmware': '7.24' }).upgradeAvailable, true);
  assert.strictEqual(parseFirmware({ 'current-firmware': '7.24', 'upgrade-firmware': '7.24' }).upgradeAvailable, false);
  // A missing field must not read as "up to date" any more than "out of date".
  assert.strictEqual(parseFirmware({ 'current-firmware': '7.24' }).upgradeAvailable, false);
  assert.strictEqual(parseFirmware(null).upgradeAvailable, false);
});

test('the update row is read the same way system.js reads it', () => {
  // The same router state must not produce two different answers on two pages.
  const u = parseUpdate({ 'installed-version': '7.23.3 (stable)', 'latest-version': '7.24',
                          status: 'New version is available', channel: 'stable' });
  assert.strictEqual(u.installedVersion, '7.23.3', 'the channel suffix is stripped');
  assert.strictEqual(u.updateAvailable, true);
  const ok = parseUpdate({ 'installed-version': '7.24', 'latest-version': '7.24',
                           status: 'System is already up to date' });
  assert.strictEqual(ok.updateAvailable, false);
});

test('the packages collector issues no write commands, ever', () => {
  // It runs unattended on a timer for every connected router, so a write
  // reachable from here would be a write nobody asked for.
  const src = fs.readFileSync(path.join(__dirname, '..', 'src', 'collectors', 'packages.js'), 'utf8');
  const code = src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');
  for (const verb of ['apply-changes', '/system/package/enable', '/system/package/disable',
                      '/system/package/uninstall', '/system/package/unschedule',
                      'check-for-updates', '/system/reboot']) {
    assert.ok(!code.includes(verb), 'packages.js must not issue ' + verb);
  }
});

// ── The write path ───────────────────────────────────────────────────────────

test('every packages action is gated on router:write and the page toggle', () => {
  const src = fs.readFileSync(path.join(__dirname, '..', 'src', 'index.js'), 'utf8');
  for (const ev of ['packages:schedule', 'packages:check', 'packages:apply']) {
    const start = src.indexOf("socket.on('" + ev + "'");
    assert.ok(start > 0, ev + ' handler exists');
    const body = src.slice(start, start + 1400);
    assert.ok(body.includes("_socketCan(socket, 'router:write', rid)"), ev + ' checks router:write');
    assert.ok(body.includes("_pageAllowed(socket, 'packages', 'write')"), ev + ' checks the page toggle');
  }
});

test('apply-changes additionally demands the router name typed back', () => {
  // It reboots a production router. One permission check is not enough to stand
  // between a misclick and an outage.
  const src = fs.readFileSync(path.join(__dirname, '..', 'src', 'index.js'), 'utf8');
  const start = src.indexOf("socket.on('packages:apply'");
  const body  = src.slice(start, start + 1800);
  assert.ok(body.includes('confirm-mismatch'), 'a mismatched name is refused');
  assert.ok(/confirm\.toLowerCase\(\)\s*!==\s*routerName\.toLowerCase\(\)/.test(body),
    'the typed name is compared against the router name');
  assert.ok(body.includes('nothing-scheduled'), 'applying nothing is refused rather than rebooting');
});

test('all four pages are registered and page-scoped', () => {
  const Pages = require('../src/pages');
  const { COLLECTORS } = require('../src/collection');
  for (const key of ['capsman', 'bridges', 'dns', 'packages']) {
    const page = Pages.BY_KEY[key];
    assert.ok(page, key + ' is a registered page');
    // streamRooms is about a SUSPENDABLE counter stream that page focus toggles,
    // which is a different thing from the collector's delivery mode. None of
    // these has one; the listen channel follows the collector, not the page.
    // Each watches its own page room, so the collector suspends when nobody is
    // looking at it — these used to declare [] and poll the router from the
    // Dashboard forever.
    assert.deepStrictEqual(page.streamRooms, ['page-' + key], key + ' must watch its own page room');
    const col = COLLECTORS.find(c => c.key === key);
    assert.ok(col, key + ' has a collector');
    assert.strictEqual(col.page, key);
    // requires: [] on purpose — a missing dependency should degrade the data,
    // not blank the page. resolveCollection cascades requires into a hard
    // disable, which is why bridges does not declare ifStatus.
    assert.deepStrictEqual(col.requires, []);
  }
});

test('bridges and capsman ship both delivery paths', () => {
  // AI_CONTEXT.md: a collector marked `pollable` must implement both. Stream is
  // the default; poll mode is the escape hatch for hardware that cannot afford
  // the channel.
  const { COLLECTORS, resolveCollection } = require('../src/collection');
  const Settings = require('../src/settings');
  for (const key of ['bridges', 'capsman', 'vlans', 'ppp']) {
    const col = COLLECTORS.find(c => c.key === key);
    assert.ok(col.streamKey, key + ' declares a stream key');
    assert.ok(col.pollable, key + ' declares a poll path');
  }
  const streamed = resolveCollection(Settings.DEFAULTS, { id: 'r1' });
  const polled   = resolveCollection(Settings.DEFAULTS, { id: 'r1', collection: { mode: 'poll' } });
  for (const key of ['bridges', 'capsman', 'vlans', 'ppp']) {
    assert.strictEqual(streamed.stream[key], true,  key + ' streams by default');
    assert.strictEqual(polled.stream[key],   false, key + ' falls back to polling');
  }
});

test('dns and packages are poll-only by choice, and say so', () => {
  // Both menus accept /listen — verified against the live router — so this is a
  // decision, not a limitation, and the registry has to carry the reason or the
  // next reader will "fix" it.
  const { COLLECTORS } = require('../src/collection');
  const fs   = require('fs');
  const path = require('path');
  for (const key of ['dns', 'packages']) {
    const col = COLLECTORS.find(c => c.key === key);
    assert.strictEqual(col.streamKey, null, key + ' holds no channel');
    assert.ok(col.pollable, key + ' still honours the router poll interval');
  }
  const src = fs.readFileSync(path.join(__dirname, '..', 'src', 'collection.js'), 'utf8');
  assert.ok(/streamKey: null ON PURPOSE/.test(src), 'the reason is recorded in the registry');
});

test('a listen event refreshes without a second parsing path', async () => {
  // The stream carries no data into the payload: it says "something changed" and
  // the ordinary tick does the reading. That is what keeps stream and poll mode
  // producing identical payloads.
  let onEvent = null;
  const ros = {
    connected: true, routerLabel: 'test', on() {},
    async write() { return []; },
    stream(cmds, cb) { onEvent = cb; return { on() {}, stop: () => Promise.resolve() }; },
  };
  const c = new BridgesCollector({ ros, io: fakeIo(), state: {}, pollMs: 5000, streamMode: true });
  await c.start();
  assert.ok(onEvent, 'a listen channel was opened in stream mode');
  c._dirty = false;
  onEvent(null, {});                    // the router says the port table changed
  assert.strictEqual(c._dirty, true, 'an event marks the cached tables stale');
  c.stop();

  const polled = new BridgesCollector({ ros, io: fakeIo(), state: {}, pollMs: 5000, streamMode: false });
  onEvent = null;
  await polled.start();
  assert.strictEqual(onEvent, null, 'poll mode opens no channel at all');
  polled.stop();
});
