#!/usr/bin/env node
'use strict';
/**
 * Pin the two wireless views, and the helpers under them, against the LIVE
 * implementation.
 *
 * WHY THIS ONE EXISTS, AND IT IS NOT THE USUAL REASON. The other generators
 * cover pure logic that no fixture reaches because it is a DECISION rather than
 * a payload. This one covers a code path no fixture reaches because **no router
 * in this fleet can produce it**: the AX3, the cAP AX and the AC2 all answer on
 * `/interface/wifi`, and `buildWirelessView` reads `/interface/wireless`, which
 * none of them has. Checked, not assumed — `/interface/wireless/print` returns
 * zero rows on the AC2 and the menu is absent on the others.
 *
 * So the legacy half of this collector has NO golden and can never have one
 * here. Synthetic rows through both implementations are the only honest
 * coverage available, and without them roughly half the collector would ship
 * unverified behind a green suite — which is the exact shape of the Queues
 * fixture problem, arriving from a direction a re-capture cannot fix.
 *
 * The modern half IS covered by the golden. It is included anyway, because the
 * helpers are shared and a case that exercises one exercises both.
 *
 *   node tools/wifiview-cases.js            # write testdata/wifiview-cases.json
 *   node tools/wifiview-cases.js --check    # fail if it is stale
 */

const fs   = require('node:fs');
const path = require('node:path');

const ROOT = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT  = process.env.WIFIVIEW_OUT || path.join(__dirname, '..', 'testdata', 'wifiview-cases.json');

const W = require(path.join(ROOT, 'src', 'collectors', 'wifi.js'));
for (const fn of ['buildWifiView', 'buildWirelessView', 'bandLabel', 'securityLabel',
                  'bandFromFrequency', 'bandFromName', 'sortNetworks']) {
  if (typeof W[fn] !== 'function') {
    console.error('src/collectors/wifi.js no longer exposes ' + fn + ' — this generator was ' +
                  'pinning a function that has moved. Find it before regenerating.');
    process.exit(1);
  }
}

// ── The helpers ──────────────────────────────────────────────────────────────
const BANDS = ['2ghz-ax', '2ghz-b/g/n', '5ghz-ac', '5ghz-a/n/ac', '6ghz-ax',
               '2GHZ-AX', '5G', '6g', '2.4', '2.4ghz', 'ax', '', '   ', 'nonsense'];

const AUTHS = ['', '   ', 'wpa2-psk', 'wpa3-psk', 'wpa2-psk,wpa3-psk', 'wpa-psk',
               'wpa2-eap', 'wpa3-eap', 'wpa2-psk,wpa2-eap', 'owe', 'owe,wpa3-psk',
               'WPA2-PSK', 'tkip', 'something-else'];

const FREQS = ['2412', '2437', '5180', '5745', '5955', '6115', '4920', '2399',
               '5180-5320', '5180,5200', '', 'auto', '0', '99999', ' 5180 '];

const NAMES = ['wifi1', '5GHz WiFi', '2.4GHz WiFi', 'cap-6g', 'wlan-5g', 'wlan-2g',
               '6 GHz Radio', 'guest', '', 'WiFi5GHz'];

// ── The views ────────────────────────────────────────────────────────────────
//
// An EMPTY RouterOS menu answers with one nameless junk row; both builders drop
// it, and every scenario below includes one so that stays pinned.
const JUNK = { undefined: '' };

const WIFI_SCENARIOS = [
  {
    name: 'two radios sharing a profile, one virtual AP, one CAPsMAN row',
    ifaces: [
      { '.id': '*1', name: 'wifi1', configuration: 'home',
        'configuration.ssid': 'HomeNet', 'security.authentication-types': 'wpa2-psk,wpa3-psk',
        'channel.band': '5ghz-ax', 'channel.frequency': '5180', 'channel.width': '20/40/80mhz',
        'datapath.vlan-id': '10', 'datapath.bridge': 'bridge', 'default-name': 'wifi1',
        'radio-mac': '02:00:00:00:00:01', running: 'true', disabled: 'false' },
      { '.id': '*2', name: 'wifi2', configuration: 'home',
        'configuration.ssid': 'HomeNet', 'security.authentication-types': 'wpa2-psk,wpa3-psk',
        'channel.band': '2ghz-ax', running: 'true', disabled: 'false' },
      // A virtual AP hanging off wifi1.
      { '.id': '*3', name: 'wifi1-guest', 'master-interface': 'wifi1', configuration: 'guest',
        'configuration.ssid': 'GuestNet', 'configuration.hide-ssid': 'true',
        'security.authentication-types': '', running: 'true' },
      // CAPsMAN-managed: a manager field, so `caps`.
      { '.id': '*4', name: 'cap-5g', 'configuration.manager': 'capsman',
        'configuration.ssid': 'CampusNet', running: 'true' },
      // Dynamic with NO manager — the AX3's own shape, so `provisioned`.
      { '.id': '*5', name: 'cap-2g', dynamic: 'true',
        'configuration.ssid': 'CampusNet', running: 'true' },
      // Names nothing at all: band must fall through to the NAME.
      { '.id': '*6', name: '6 GHz Radio', running: 'false', disabled: 'true' },
    ],
    configs: [JUNK,
      { '.id': '*c1', name: 'home', ssid: 'HomeNet', security: 'home-sec', channel: 'home-chan' },
      { '.id': '*c2', name: 'guest', ssid: 'GuestNet' }],
    security: [JUNK, { '.id': '*s1', name: 'home-sec', 'authentication-types': 'wpa2-psk' }],
    channels: [JUNK, { '.id': '*h1', name: 'home-chan', band: '5ghz-ax', frequency: '5180',
                       width: '20/40/80mhz' }],
    reg: [{ interface: 'wifi1' }, { interface: 'wifi1' }, { interface: 'wifi2' },
          { interface: '' }, { interface: '  ' }, { interface: 'gone' }],
  },
  {
    name: 'inheritance: the profile defines it and the value still equals it',
    ifaces: [
      // ssid EQUALS the profile's -> inherited.
      { '.id': '*1', name: 'a', configuration: 'p', 'configuration.ssid': 'Net',
        security: 'sec', channel: 'ch' },
      // ssid DIFFERS -> overridden, so not inherited.
      { '.id': '*2', name: 'b', configuration: 'p', 'configuration.ssid': 'Other',
        security: 'sec', channel: 'ch' },
      // Names a profile that does not exist.
      { '.id': '*3', name: 'c', configuration: 'missing', 'configuration.ssid': 'Net' },
      // No profile at all.
      { '.id': '*4', name: 'd', 'configuration.ssid': 'Net' },
    ],
    configs: [{ '.id': '*c1', name: 'p', ssid: 'Net', security: 'sec', channel: 'ch' }],
    security: [{ '.id': '*s1', name: 'sec', 'authentication-types': 'wpa2-psk' }],
    channels: [{ '.id': '*h1', name: 'ch', band: '5ghz-ax' }],
    reg: [],
  },
  { name: 'every menu empty but the junk row',
    ifaces: [], configs: [JUNK], security: [JUNK], channels: [JUNK], reg: [] },
];

const WL_SCENARIOS = [
  {
    name: 'legacy: profiles, a virtual AP, an open none-mode profile, a dynamic row',
    ifaces: [
      { '.id': '*1', name: 'wlan1', 'security-profile': 'default', ssid: 'LegacyNet',
        band: '2ghz-b/g/n', frequency: '2437', 'channel-width': '20/40mhz-Ce',
        'mac-address': '02:00:00:00:00:0A', 'default-name': 'wlan1',
        'hide-ssid': 'true', 'vlan-id': '20', running: 'true' },
      { '.id': '*2', name: 'wlan1-v', 'master-interface': 'wlan1',
        'security-profile': 'open-prof', ssid: 'GuestLegacy', running: 'true' },
      { '.id': '*3', name: 'wlan2', 'security-profile': 'missing', ssid: 'NoProfile',
        band: '5ghz-a/n/ac', frequency: '5180' },
      { '.id': '*4', name: 'wlan3', dynamic: 'true', ssid: 'Provisioned', band: '5ghz-a/n/ac' },
      // Neither band nor frequency: the NAME has to answer.
      { '.id': '*5', name: 'wlan-6g', ssid: 'SixGig' },
    ],
    profiles: [JUNK,
      { '.id': '*p1', name: 'default', mode: 'dynamic-keys',
        'authentication-types': 'wpa2-psk', default: 'true' },
      // mode `none` is an OPEN network however the profile is named.
      { '.id': '*p2', name: 'open-prof', mode: 'none',
        'authentication-types': 'wpa2-psk' }],
    reg: [{ interface: 'wlan1' }, { interface: 'wlan1' }, { interface: 'wlan1-v' }],
  },
  { name: 'legacy: no interfaces, one junk profile row',
    ifaces: [], profiles: [JUNK], reg: [] },
];

function run() {
  return {
    bandLabel:        BANDS.map((raw) => ({ raw, want: W.bandLabel(raw) })),
    securityLabel:    AUTHS.map((raw) => ({ raw, want: W.securityLabel(raw) })),
    bandFromFrequency: FREQS.map((raw) => ({ raw, want: W.bandFromFrequency(raw) })),
    bandFromName:     NAMES.map((raw) => ({ raw, want: W.bandFromName(raw) })),
    wifi: WIFI_SCENARIOS.map((s) => {
      const v = W.buildWifiView(s);
      return { name: s.name, input: s,
               want: { networks: W.sortNetworks(v.networks), radios: v.radios } };
    }),
    wireless: WL_SCENARIOS.map((s) => {
      const v = W.buildWirelessView(s);
      return { name: s.name, input: s,
               want: { networks: W.sortNetworks(v.networks), radios: v.radios,
                       secProfiles: v.secProfiles } };
    }),
  };
}

const out = JSON.stringify(run(), null, 2) + '\n';
const n = run();
const counts = `${n.bandLabel.length + n.securityLabel.length + n.bandFromFrequency.length +
  n.bandFromName.length} helper cases, ${n.wifi.length} wifi + ${n.wireless.length} wireless views`;

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== out) {
    console.error('testdata/wifiview-cases.json is stale — run: node tools/wifiview-cases.js');
    process.exit(1);
  }
  console.log(`wifiview-cases up to date (${counts})`);
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, out);
  console.log(`wrote ${path.relative(process.cwd(), OUT)} — ${counts}`);
}
