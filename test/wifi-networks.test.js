'use strict';
// The Wifi Networks page: the CONFIGURATION side of wireless.
//
// Fixtures are shaped exactly as the live routers returned them on 2026-08-20 —
// a hAP AX3 provisioning its own radios through CAPsMAN, and a hAP ac2 whose
// radios are disabled. Both shapes cost real bugs on the first live pass and are
// pinned here so they cannot come back:
//
//   * every row came back editable:false with nothing on screen saying why. A
//     router running CAPsMAN against its own radios reports them `dynamic` with
//     NO `configuration.manager`, so keying the badge on manager alone left
//     twelve uneditable rows and no explanation.
//   * band and width were empty on every row, because both live on the channel
//     PROFILE rather than inline.
//   * an empty RouterOS menu answers with one nameless junk row.

const { test } = require('node:test');
const assert   = require('node:assert');
const fs       = require('node:fs');
const path     = require('node:path');

const Wifi      = require('../src/collectors/wifi');
const wifiGuard = require('../src/routeros/wifiGuard');
const R         = require('../src/routeros/resources');

const SRC = (f) => fs.readFileSync(path.join(__dirname, '..', 'src', f), 'utf8');

// ── Fixtures ─────────────────────────────────────────────────────────────────

// hAP AX3. Note `dynamic: 'true'` with no configuration.manager, and that band,
// frequency and width are absent from the interface entirely.
const AX3_IFACES = [
  { '.id': '*45', name: '2.4GHz WiFi', 'default-name': 'wifi2', disabled: 'false', running: 'true',
    'master-interface': '', 'radio-mac': '48:A9:8A:09:FE:2C', configuration: 'Home WiFi 2.4Ghz',
    'configuration.ssid': 'SkyNet', 'configuration.country': 'Germany',
    'security.authentication-types': 'wpa2-psk,wpa3-psk', security: 'Home WiFi',
    'datapath.vlan-id': '5', 'datapath.bridge': 'Bridge', dynamic: 'true' },
  { '.id': '*46', name: '2.4GHz WiFi2', disabled: 'false', running: 'false',
    'master-interface': '2.4GHz WiFi', configuration: 'Guest WiFi 2.4Ghz',
    'configuration.ssid': 'The Promised LAN',
    'security.authentication-types': 'wpa2-psk,wpa3-psk', security: 'Guest WiFi',
    'datapath.vlan-id': '20', dynamic: 'true' },
  { '.id': '*42', name: '5GHz WiFi', 'default-name': 'wifi1', disabled: 'false', running: 'true',
    'master-interface': '', 'radio-mac': '48:A9:8A:09:FE:2B', configuration: 'Home WiFi 5Ghz',
    'configuration.ssid': 'SkyNet', 'configuration.country': 'Germany',
    'security.authentication-types': 'wpa2-psk,wpa3-psk', security: 'Home WiFi',
    channel: '5Ghz Channel Range', 'datapath.vlan-id': '5', dynamic: 'true' },
];
const AX3_CONFIGS = [
  { '.id': '*1', name: 'Home WiFi 2.4Ghz', ssid: 'SkyNet', security: 'Home WiFi' },
  { '.id': '*2', name: 'Guest WiFi 2.4Ghz', ssid: 'The Promised LAN', security: 'Guest WiFi' },
  { '.id': '*3', name: 'Home WiFi 5Ghz', ssid: 'SkyNet', security: 'Home WiFi',
    channel: '5Ghz Channel Range' },
];
const AX3_SECURITY = [{ '.id': '*1', name: 'Home WiFi', 'authentication-types': 'wpa2-psk,wpa3-psk' },
                      { '.id': '*2', name: 'Guest WiFi', 'authentication-types': 'wpa2-psk,wpa3-psk' }];
const AX3_CHANNELS = [{ '.id': '*1', name: '5Ghz Channel Range', frequency: '5180-5730',
                        width: '20/40/80mhz' }];
const AX3_REG = [{ interface: '2.4GHz WiFi', ssid: 'SkyNet' },
                 { interface: '5GHz WiFi', ssid: 'SkyNet' },
                 { interface: '5GHz WiFi', ssid: 'SkyNet' }];

const buildAx3 = () => Wifi.buildWifiView({
  ifaces: AX3_IFACES, configs: AX3_CONFIGS, security: AX3_SECURITY,
  channels: AX3_CHANNELS, reg: AX3_REG,
});

// Legacy stack: the passphrase is on the profile, not the interface.
const WL_IFACES = [
  { '.id': '*1', name: 'wlan1', disabled: 'false', running: 'true', ssid: 'Old-Net',
    mode: 'ap-bridge', band: '5ghz-a/n/ac', frequency: '5180', 'channel-width': '20/40mhz-Ce',
    'security-profile': 'guest-wpa2', 'master-interface': '', 'mac-address': 'AA:BB:CC:DD:EE:01' },
  { '.id': '*2', name: 'wlan1-guest', disabled: 'true', running: 'false', ssid: 'Old-Guest',
    'security-profile': 'open-profile', 'master-interface': 'wlan1' },
];
const WL_PROFILES = [
  { '.id': '*1', name: 'guest-wpa2', mode: 'dynamic-keys', 'authentication-types': 'wpa2-psk' },
  { '.id': '*2', name: 'open-profile', mode: 'none', 'authentication-types': '' },
];

const buildWl = () => Wifi.buildWirelessView({
  ifaces: WL_IFACES, profiles: WL_PROFILES, reg: [{ interface: 'wlan1' }],
});

// ── The security property ────────────────────────────────────────────────────

test('no passphrase is ever asked for', () => {
  // The absence of these words from the proplists IS the security property.
  // /interface/wifi/print returns security.passphrase in clear text, and this
  // payload goes to every browser holding read on the page.
  const commands = JSON.stringify([Wifi.WIFI_CMDS, Wifi.WL_CMDS]).toLowerCase();
  for (const secret of ['passphrase', 'pre-shared-key', 'presharedkey']) {
    assert.ok(!commands.includes(secret),
      'a collector command asks the router for ' + secret);
  }
});

test('a passphrase present in a router row never reaches the payload', () => {
  // Belt as well as braces: even if a proplist were widened by accident, no
  // field of the built view may carry the value through.
  const leaky = AX3_IFACES.map(r => Object.assign({}, r, {
    'security.passphrase': 'hunter2hunter2',
  }));
  const view = Wifi.buildWifiView({
    ifaces: leaky, configs: AX3_CONFIGS, security: AX3_SECURITY,
    channels: AX3_CHANNELS, reg: AX3_REG,
  });
  assert.ok(!JSON.stringify(view).includes('hunter2hunter2'),
    'the built view carries a passphrase');
});

test('a legacy pre-shared key never reaches the payload', () => {
  const leaky = WL_PROFILES.map(p => Object.assign({}, p, {
    'wpa2-pre-shared-key': 'hunter2hunter2', 'wpa-pre-shared-key': 'hunter2hunter2',
  }));
  const view = Wifi.buildWirelessView({ ifaces: WL_IFACES, profiles: leaky, reg: [] });
  assert.ok(!JSON.stringify(view).includes('hunter2hunter2'),
    'the built view carries a pre-shared key');
});

test('both passphrase fields are declared secret, so they are stripped both ways', () => {
  // `secret` is what makes rowValues() omit the value from the edit form and
  // _resAuditValues record SET/UNSET instead of the key itself.
  const secretOf = (key, field) => {
    const f = R.byKey(key).fields.find(x => x.name === field);
    assert.ok(f, key + ' has no field ' + field);
    return f.type;
  };
  assert.strictEqual(secretOf('wifiNet', 'passphrase'), 'secret');
  assert.strictEqual(secretOf('wlSecProfile', 'wpa2PreSharedKey'), 'secret');
  assert.strictEqual(secretOf('wlSecProfile', 'wpaPreSharedKey'), 'secret');

  const row = { name: 'wifi1', 'configuration.ssid': 'Home', 'security.passphrase': 'hunter2hunter2' };
  const vals = R.rowValues(R.byKey('wifiNet'), row);
  assert.strictEqual(vals.passphrase, undefined, 'the form would be filled with the passphrase');
  assert.strictEqual(vals.ssid, 'Home');
});

test('the preview masks a passphrase rather than printing it', () => {
  const w = R.byKey('wifiNet');
  const v = R.validate(w, { name: 'wifi1-guest', masterInterface: 'wifi1', ssid: 'Guest',
                            authTypes: 'wpa2-psk', passphrase: 'longenough1' }, { editing: false });
  assert.ok(v.ok, JSON.stringify(v.errors));
  const preview = R.previewCommand(w, v, null);
  assert.ok(preview.includes('«set»'), 'the preview should mask the value');
  assert.ok(!preview.includes('longenough1'), 'the preview prints the passphrase');
});

// ── Reading the modern stack ─────────────────────────────────────────────────

test('a radio and its virtual APs are told apart', () => {
  const { networks, radios } = buildAx3();
  const byName = Object.fromEntries(networks.map(n => [n.name, n]));
  assert.strictEqual(byName['2.4GHz WiFi'].isVirtual, false);
  assert.strictEqual(byName['2.4GHz WiFi2'].isVirtual, true);
  assert.strictEqual(byName['2.4GHz WiFi2'].radio, '2.4GHz WiFi',
    'a virtual AP groups under its master, not under itself');
  // Only masters are radios.
  assert.deepStrictEqual(radios.map(r => r.name).sort(), ['2.4GHz WiFi', '5GHz WiFi']);
});

test('a router provisioning its own radios is read-only, and says which kind', () => {
  // The live bug: `dynamic` with no configuration.manager. Keying read-only on
  // the manager alone left every row uneditable with no badge to explain it.
  const { networks } = buildAx3();
  for (const n of networks) {
    assert.strictEqual(n.capsManaged, false, 'no configuration.manager is set on these rows');
    assert.strictEqual(n.readOnlyReason, 'provisioned');
    assert.strictEqual(n.editable, false);
    assert.strictEqual(n.removable, false);
  }
});

test('a CAP row is distinguished from a locally provisioned one', () => {
  const { networks } = Wifi.buildWifiView({
    ifaces: [Object.assign({}, AX3_IFACES[0], { 'configuration.manager': 'capsman', dynamic: 'false' })],
    configs: AX3_CONFIGS, security: AX3_SECURITY, channels: AX3_CHANNELS, reg: [],
  });
  assert.strictEqual(networks[0].capsManaged, true);
  assert.strictEqual(networks[0].readOnlyReason, 'caps');
});

test('a static interface is editable, and only a virtual AP is removable', () => {
  const statics = AX3_IFACES.map(r => Object.assign({}, r, { dynamic: 'false' }));
  const { networks } = Wifi.buildWifiView({
    ifaces: statics, configs: AX3_CONFIGS, security: AX3_SECURITY,
    channels: AX3_CHANNELS, reg: [],
  });
  const byName = Object.fromEntries(networks.map(n => [n.name, n]));
  assert.strictEqual(byName['2.4GHz WiFi'].editable, true);
  assert.strictEqual(byName['2.4GHz WiFi'].removable, false, 'a radio is hardware');
  assert.strictEqual(byName['2.4GHz WiFi2'].editable, true);
  assert.strictEqual(byName['2.4GHz WiFi2'].removable, true, 'a virtual AP can be removed');
});

test('band and width are read through the channel profile, not only inline', () => {
  // The live bug: neither is set on the interface, so every Band cell was an
  // em dash on a router whose channel profile named both.
  const { radios } = buildAx3();
  const byName = Object.fromEntries(radios.map(r => [r.name, r]));
  assert.strictEqual(byName['5GHz WiFi'].frequency, '5180-5730');
  assert.strictEqual(byName['5GHz WiFi'].channelWidth, '20/40/80mhz');
  assert.strictEqual(byName['5GHz WiFi'].band, '5GHz', 'inferred from the profile frequency');
});

test('a band with no property, profile or frequency falls back to the name', () => {
  // The 2.4 GHz radios on the live AX3 carry none of the three.
  assert.strictEqual(Wifi.bandFromFrequency('5180-5730'), '5GHz');
  assert.strictEqual(Wifi.bandFromFrequency('2412'), '2.4GHz');
  assert.strictEqual(Wifi.bandFromFrequency(''), '');
  assert.strictEqual(Wifi.bandFromName('2.4GHz WiFi'), '2.4GHz');
  assert.strictEqual(Wifi.bandFromName('5GHz WiFi4'), '5GHz');
  assert.strictEqual(Wifi.bandFromName('wifi1'), '', 'a nameless band must stay empty, not guess');
  // Spelled exactly as wireless.js spells them: the Wifi Clients page's band
  // pill keys off these three strings, and a space would render as plain text.
  assert.strictEqual(Wifi.bandLabel('6ghz-ax'), '6GHz');
  const { radios } = buildAx3();
  assert.strictEqual(radios.find(r => r.name === '2.4GHz WiFi').band, '2.4GHz');
});

test('inheritance names the profile a value comes from, and how many share it', () => {
  const { networks } = buildAx3();
  const home = networks.find(n => n.name === '2.4GHz WiFi');
  assert.strictEqual(home.inherits.ssid, 'Home WiFi 2.4Ghz');
  assert.strictEqual(home.inherits.security, 'Home WiFi');
  assert.strictEqual(home.profileUsedBy, 1, 'one interface names this profile in the fixture');
});

test('a value overridden on the interface is not reported as inherited', () => {
  // The whole point of the comparison: the profile says SkyNet, the interface
  // says something else, so somebody has already overridden it locally.
  const { networks } = Wifi.buildWifiView({
    ifaces: [Object.assign({}, AX3_IFACES[0], { 'configuration.ssid': 'Overridden' })],
    configs: AX3_CONFIGS, security: AX3_SECURITY, channels: AX3_CHANNELS, reg: [],
  });
  assert.strictEqual(networks[0].inherits.ssid, null);
});

test('client counts come from the registration table, by interface', () => {
  const { networks, radios } = buildAx3();
  assert.strictEqual(networks.find(n => n.name === '5GHz WiFi').clients, 2);
  assert.strictEqual(networks.find(n => n.name === '2.4GHz WiFi').clients, 1);
  assert.strictEqual(networks.find(n => n.name === '2.4GHz WiFi2').clients, 0);
  assert.ok(radios.length);
});

test('an empty menu answers with a nameless junk row, which must not become a profile', () => {
  // /interface/wifi/channel/print returns [{"undefined":""}] on a router with no
  // channel profiles. Keyed by name that becomes an entry under '', which is
  // exactly what an interface naming no profile looks up.
  const { networks } = Wifi.buildWifiView({
    ifaces: [{ '.id': '*1', name: 'wifi1', 'configuration.ssid': 'X' }],
    configs: [{ undefined: '' }], security: [{ undefined: '' }],
    channels: [{ undefined: '' }], reg: [],
  });
  assert.strictEqual(networks[0].inherits, null);
  assert.strictEqual(networks[0].profileUsedBy, 0);
});

// ── Reading the legacy stack ─────────────────────────────────────────────────

test('legacy security is resolved through the profile the interface names', () => {
  const { networks } = buildWl();
  const byName = Object.fromEntries(networks.map(n => [n.name, n]));
  assert.strictEqual(byName['wlan1'].security, 'WPA2');
  assert.strictEqual(byName['wlan1'].resource, 'wlNet', 'the row names the resource that owns it');
});

test('a profile in `none` mode is an open network however it is named', () => {
  const { networks } = buildWl();
  assert.strictEqual(networks.find(n => n.name === 'wlan1-guest').security, 'Open');
});

test('the legacy stack publishes its security profiles as rows of their own', () => {
  const { secProfiles } = buildWl();
  assert.deepStrictEqual(secProfiles.map(p => p.name).sort(), ['guest-wpa2', 'open-profile']);
  assert.strictEqual(secProfiles.find(p => p.name === 'guest-wpa2').security, 'WPA2');
});

test('an open network is labelled, never left blank', () => {
  // The failure this catches is an SSID with no security that reads as missing
  // data rather than as a problem.
  assert.strictEqual(Wifi.securityLabel(''), 'Open');
  assert.strictEqual(Wifi.securityLabel('wpa2-psk,wpa3-psk'), 'WPA3/WPA2');
  assert.strictEqual(Wifi.securityLabel('wpa2-eap'), 'WPA2 Enterprise');
  assert.strictEqual(Wifi.securityLabel('owe'), 'OWE');
});

test('rows sort with each radio ahead of the virtual APs riding it', () => {
  const sorted = Wifi.sortNetworks(buildAx3().networks);
  const names = sorted.map(n => n.name);
  assert.ok(names.indexOf('2.4GHz WiFi') < names.indexOf('2.4GHz WiFi2'));
});

// ── The inherit guard ────────────────────────────────────────────────────────

test('overriding a profile only one interface uses is not worth a warning', () => {
  // A warning that fires on the innocent case is one people learn to click
  // through — see the note on the VLAN guard in resources.js.
  const v = wifiGuard.checkInherit({
    action: 'update',
    values: { ssid: 'New' },
    before: { name: 'wifi1', configuration: 'shared', 'configuration.ssid': 'Old' },
    siblings: [{ name: 'wifi1', configuration: 'shared' }],
  });
  assert.strictEqual(v.level, 'none');
});

test('overriding a profile two interfaces share warns, and says which fields', () => {
  const v = wifiGuard.checkInherit({
    action: 'update',
    values: { ssid: 'New' },
    before: { name: 'wifi1', configuration: 'shared', 'configuration.ssid': 'Old' },
    siblings: [{ name: 'wifi1', configuration: 'shared' }, { name: 'wifi2', configuration: 'shared' }],
  });
  assert.strictEqual(v.level, 'warn');
  assert.strictEqual(v.code, 'wifi-inherit');
  assert.strictEqual(v.detail.profile, 'shared');
  assert.strictEqual(v.detail.sharedBy, 2);
  assert.deepStrictEqual(v.detail.fields, ['ssid']);
  assert.ok(v.fingerprint, 'the acknowledgement has to be bound to something');
});

test('submitting a shared value unchanged is not an override', () => {
  const v = wifiGuard.checkInherit({
    action: 'update',
    values: { ssid: 'Same' },
    before: { name: 'wifi1', configuration: 'shared', 'configuration.ssid': 'Same' },
    siblings: [{ configuration: 'shared' }, { configuration: 'shared' }],
  });
  assert.strictEqual(v.level, 'none');
});

test('a blank passphrase means unchanged, so it is not an override', () => {
  const base = {
    action: 'update',
    before: { name: 'wifi1', configuration: 'shared' },
    siblings: [{ configuration: 'shared' }, { configuration: 'shared' }],
  };
  assert.strictEqual(wifiGuard.checkInherit(Object.assign({ values: { passphrase: '' } }, base)).level, 'none');
  assert.strictEqual(wifiGuard.checkInherit(Object.assign({ values: { passphrase: 'x' } }, base)).level, 'warn');
});

test('a create overrides nothing and an interface with no profile inherits nothing', () => {
  assert.strictEqual(wifiGuard.checkInherit({
    action: 'create', values: { ssid: 'New' }, before: null, siblings: [] }).level, 'none');
  assert.strictEqual(wifiGuard.checkInherit({
    action: 'update', values: { ssid: 'New' },
    before: { name: 'wifi1', configuration: '' },
    siblings: [{ configuration: '' }, { configuration: '' }] }).level, 'none');
});

// ── The registry, and how the engine reaches it ──────────────────────────────

test('all three wireless resources are on the wifi page and named by the collector', () => {
  for (const key of ['wifiNet', 'wlNet', 'wlSecProfile']) {
    const r = R.byKey(key);
    assert.ok(r, key + ' is not registered');
    assert.strictEqual(r.page, 'wifi');
    assert.strictEqual(r.collector, 'wifi');
    assert.strictEqual(r.identity, 'name');
    // requiresMenu is what makes a router show exactly one of the two stacks:
    // res:schema withholds the resource whose menu does not answer.
    assert.ok(r.requiresMenu, key + ' must declare the menu it needs');
  }
  assert.strictEqual(R.byKey('wifiNet').menu, '/interface/wifi');
  assert.strictEqual(R.byKey('wlNet').menu, '/interface/wireless');
});

test('Add can only ever attach an SSID to an existing radio', () => {
  // Making the radio required is the whole mechanism: with no way to omit it,
  // the form cannot create a stray physical interface.
  for (const key of ['wifiNet', 'wlNet']) {
    const f = R.byKey(key).fields.find(x => x.name === 'masterInterface');
    assert.ok(f && f.required, key + ' must require a master interface');
    assert.ok(f.optionsFrom && f.optionsFrom.menu, key + ' must offer a radio picker');
  }
});

test('removableWhen refuses a radio and allows a virtual AP', () => {
  for (const key of ['wifiNet', 'wlNet']) {
    const fn = R.byKey(key).removableWhen;
    assert.ok(typeof fn === 'function', key + ' must declare removableWhen');
    assert.strictEqual(!!fn({ name: 'wifi1' }), false, 'a radio is not removable');
    assert.strictEqual(!!fn({ name: 'wifi1-guest', 'master-interface': 'wifi1' }), true);
  }
});

test('readOnlyWhen refuses a CAP-managed and a dynamic row', () => {
  const fn = R.byKey('wifiNet').readOnlyWhen;
  assert.strictEqual(!!fn({ 'configuration.manager': 'capsman' }), true);
  assert.strictEqual(!!fn({ dynamic: 'true' }), true);
  assert.strictEqual(!!fn({ name: 'wifi1' }), false);
});

test('res:remove consults removableWhen on the freshly read row', () => {
  // Source-scanned for the same reason readOnlyWhen is: the check must sit
  // after the read, never against the browser's claim about the row.
  const src = SRC('index.js');
  const at  = src.indexOf("socket.on('res:remove'");
  const body = src.slice(at, src.indexOf('\n  }));', at));
  assert.ok(/resource\.removableWhen && !resource\.removableWhen\(before\)/.test(body),
    'res:remove must check removableWhen against `before`');
  assert.ok(body.indexOf('_resRead') < body.indexOf('removableWhen'),
    'the row must be read before it is judged');
});

test('a passphrase is length-checked here rather than left to a bare router refusal', () => {
  const w = R.byKey('wifiNet');
  const short = R.validate(w, { name: 'a', masterInterface: 'wifi1', ssid: 'S',
                                authTypes: 'wpa2-psk', passphrase: 'short' }, {});
  assert.strictEqual(short.ok, false);
  assert.strictEqual(short.errors[0].field, 'passphrase');
  // Blank means "leave the current one alone", so it must NOT be an error —
  // otherwise renaming an SSID would demand the passphrase be retyped.
  const blank = R.validate(w, { name: 'a', masterInterface: 'wifi1', ssid: 'S',
                                authTypes: 'wpa2-psk', passphrase: '' }, { editing: true });
  assert.strictEqual(blank.ok, true, 'a blank passphrase must mean unchanged');
});

test('no typed non-string field is clearable, because RouterOS refuses an empty one', () => {
  // Found by the first live write: `clearable` emits `=datapath.vlan-id=` on
  // every edit, and RouterOS answers a typed integer property given an empty
  // string with "invalid value  for datapath.vlan-id, an integer required".
  // That made EVERY edit of a wireless network fail, whether or not it touched
  // the VLAN. A bool is exempt: validate() always produces yes/no for one, so
  // the clearable branch in buildArgs can never fire for it.
  for (const key of ['wifiNet', 'wlNet', 'wlSecProfile']) {
    for (const f of R.byKey(key).fields) {
      if (!f.clearable) continue;
      assert.ok(f.type === 'bool' || f.type === 'text',
        key + '.' + f.name + ' is clearable but typed ' + f.type +
        ', and an empty value would be refused');
    }
  }
});

test('an edit that leaves the VLAN alone does not send an empty VLAN word', () => {
  const w = R.byKey('wifiNet');
  const v = R.validate(w, { name: 'wifi1-guest', masterInterface: 'wifi1', ssid: 'Guest',
                            authTypes: 'wpa2-psk', passphrase: '' }, { editing: true });
  assert.ok(v.ok, JSON.stringify(v.errors));
  const args = R.buildArgs(w, v);
  assert.ok(!args.some(a => a === '=datapath.vlan-id='),
    'an empty VLAN word is what the router refused');
  assert.ok(!args.some(a => a.startsWith('=security.passphrase=')),
    'a blank passphrase must mean unchanged, not cleared');
});

test('band, width and auth type are suggestions, never a closed list', () => {
  // Vocabularies differ across the wifi-qcom, wifi-qcom-ac and legacy drivers.
  // A hard select would refuse a value the router itself accepts.
  for (const [key, names] of [['wifiNet', ['band', 'width', 'authTypes']],
                              ['wlNet',   ['band', 'channelWidth']]]) {
    for (const n of names) {
      const f = R.byKey(key).fields.find(x => x.name === n);
      assert.strictEqual(f.type, 'text', key + '.' + n + ' must not be a hard select');
      assert.ok(f.optionsFrom && f.optionsFrom.values, key + '.' + n + ' should still suggest');
    }
  }
});

test('the modern resource runs both guards and names its disruptive fields', () => {
  const r = R.byKey('wifiNet');
  assert.deepStrictEqual(r.guard, ['selfPath', 'wifiInherit']);
  assert.ok(r.guardInterfaceFields.includes('name'));
  // Renaming and disabling are not the only edits that cut a link: changing the
  // SSID or passphrase drops every client on the radio.
  for (const n of ['ssid', 'passphrase', 'authTypes'])
    assert.ok(r.guardDisruptiveFields.includes(n), n + ' should count as disruptive');
});

test('_resVerdict runs every declared guard and takes the first warning', () => {
  const src = SRC('index.js');
  assert.ok(/Array\.isArray\(resource\.guard\) \? resource\.guard : \[resource\.guard\]/.test(src),
    'a resource must be able to declare more than one guard');
  const at = src.indexOf('const _resVerdict =');
  const body = src.slice(at, src.indexOf('const _resVerdictOne', at));
  assert.ok(/v\.level === 'warn'/.test(body), 'the first warn must win');
});

test('a disruptive change counts as a guard target even without a rename', () => {
  const src = SRC('index.js');
  const at  = src.indexOf('const _resGuardTargets');
  const body = src.slice(at, src.indexOf('\n  };', at));
  assert.ok(/guardDisruptiveFields/.test(body), 'the disruptive list must be consulted');
  assert.ok(/!renamed && !disruptive/.test(body),
    'a disruptive change must not be filtered out alongside a plain comment edit');
});

// ── Page and collector registration ──────────────────────────────────────────

test('the page and collector are registered and page-scoped', () => {
  const Pages = require('../src/pages');
  const { COLLECTORS, BY_KEY } = require('../src/collection');
  const page = Pages.BY_KEY.wifi;
  assert.ok(page, 'wifi is a registered page');
  assert.strictEqual(page.title, 'Wifi Networks');
  assert.strictEqual(page.settingsKey, 'pageWifi');
  assert.strictEqual(page.category, 'wireless');
  assert.deepStrictEqual(page.streamRooms, ['page-wifi'], 'suspends when nobody is on the page');

  const col = BY_KEY.wifi;
  assert.strictEqual(col.page, 'wifi');
  assert.strictEqual(col.sessionProp, 'wifi', 'must equal the page key or page:focus never replays it');
  assert.strictEqual(col.streamKey, 'streamWifi');
  assert.strictEqual(col.disableable, true);
  assert.ok(COLLECTORS.some(c => c.key === 'wifi'));
});

test('the poll interval is settable and bounded', () => {
  const Settings = require('../src/settings');
  assert.strictEqual(typeof Settings.DEFAULTS.pollWifi, 'number');
  assert.strictEqual(Settings.DEFAULTS.pageWifi, true);
  const src = SRC('index.js');
  assert.ok(src.includes('pollWifi:[10000,600000]'), 'bounded in intFields');
  assert.ok(src.includes("pollWifi:'wifi'"), 'reaches the live collector on save');
});

test('a viewer can be told whether the page exists', () => {
  // Omitting the key from VIEWER_FIELDS hides the toggle from exactly the
  // people whose nav it governs.
  const src = SRC('settings.js');
  const at = src.indexOf('const VIEWER_FIELDS');
  assert.ok(src.slice(at, src.indexOf('];', at)).includes("'pageWifi'"));
});

test('the collector exposes refreshNow, which is how a write reaches the table', () => {
  // Without it a save lands on the router and the page sits still until the
  // next poll, which reads as a failed write.
  assert.strictEqual(typeof Wifi.prototype.refreshNow, 'function');
  assert.strictEqual(typeof Wifi.prototype.suspend, 'function');
  assert.strictEqual(typeof Wifi.prototype.resume, 'function');
});

test('the page emits to its own room and nowhere else', () => {
  const src = SRC(path.join('collectors', 'wifi.js'));
  const rooms = [...src.matchAll(/\.to\('([^']+)'\)/g)].map(m => m[1]);
  assert.deepStrictEqual([...new Set(rooms)], ['page-wifi']);
});
