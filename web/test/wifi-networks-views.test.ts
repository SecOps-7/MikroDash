/**
 * THE FOUR READINGS OF THE WIFI NETWORKS TABLE.
 *
 * ── WHAT WAS ASKED FOR ──────────────────────────────────────────────────────
 *
 * "The page is confusing — give me other ways to look at it, and keep the one
 * that is there." One row per interface grouped by radio is RouterOS's own
 * model and stays the default; it is the wrong shape for two ordinary questions,
 * which the other three answer:
 *
 *	ap     both bands of one access point together
 *	ssid   one row per network, aggregated across every radio
 *	flat   no grouping at all
 *
 * ── THE DISCRIMINATING CASE IS A DUAL-BAND CAP ──────────────────────────────
 *
 * Two radios, one box, one SSID on both plus a guest network on one. Under
 * `radio` that is two groups; under `ap` it is one; under `ssid` it is two rows
 * whatever the hardware looks like. A fixture with one radio cannot tell the
 * three apart, so every case below uses the same four interfaces.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const ENTRY = path.join(ROOT, 'testdata', '.wnv-entry.ts');
fs.writeFileSync(ENTRY, "export { initWifiPage } from '../web/src/pages/wifi.js';\n");
const OUT = path.join(ROOT, 'testdata', '.wnv.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const IDS = [
  'wnTable', 'wnThead', 'wnBadge', 'wnViewBtns', 'wnNote',
  'wnRadioCount', 'wnNetCount', 'wnClientCount',
  'wnStackNote', 'wnVirtualNote', 'wnCapNote',
  'wnSecCard', 'wnSecBadge', 'wnSecTable',
];

function net(over) {
  return Object.assign({
    id: '*1', name: 'x', ssid: 'Net', radio: 'x', master: '', isVirtual: false,
    band: '2.4GHz', bandRaw: '2ghz-ax', security: 'WPA2', authTypes: 'wpa2-psk',
    hidden: false, vlanId: '', bridge: '', disabled: false, running: true,
    clients: 0, comment: '', capsManaged: false, profile: '', profileUsedBy: 0,
    inherits: null, readOnlyReason: '', editable: true, removable: false,
    resource: 'wifiNet', ap: '',
  }, over);
}

function radio(over) {
  return Object.assign({
    name: 'x', ap: '', defaultName: '', mac: '', band: '2.4GHz', bandRaw: '2ghz-ax',
    frequency: '', channelWidth: '', country: '', disabled: false, running: true,
    capsManaged: false, readOnlyReason: '', profile: '',
  }, over);
}

// One dual-band CAP: two radios, the same SSID on both, plus a guest network
// riding the 2.4 GHz one.
const NETWORKS = [
  net({ id: '*1', name: 'cap24', radio: 'cap24', ssid: 'guest', band: '2.4GHz',
    ap: 'hap-ax2', clients: 3 }),
  net({ id: '*2', name: 'cap24-guest', radio: 'cap24', master: 'cap24', isVirtual: true,
    ssid: 'Guest', band: '2.4GHz', ap: 'hap-ax2', clients: 1, security: 'Open' }),
  net({ id: '*3', name: 'cap5', radio: 'cap5', ssid: 'guest', band: '5GHz',
    ap: 'hap-ax2', clients: 5 }),
  // A radio this router owns, so the AP view has a second group and the "" key
  // is exercised.
  net({ id: '*4', name: 'wifi1', radio: 'wifi1', ssid: 'Home', band: '5GHz', clients: 2 }),
];
const RADIOS = [
  radio({ name: 'cap24', ap: 'hap-ax2' }),
  radio({ name: 'cap5', ap: 'hap-ax2', band: '5GHz' }),
  radio({ name: 'wifi1', band: '5GHz' }),
];

function boot() {
  const doc = makeDoc(IDS);
  const handlers = {};
  const socket = { on: (ev, fn) => { handlers[ev] = fn; }, emit: () => {} };
  const prevDoc = global.document;
  const prevWin = global.window;
  global.document = doc;
  global.window = { addEventListener: () => {}, setTimeout, clearTimeout };
  const { initWifiPage } = require(OUT);
  initWifiPage(socket, () => true);
  return {
    doc,
    send: () => handlers['wifi:update']({
      ts: 1, pollMs: 30000, stack: 'wifi', available: true,
      radios: RADIOS, networks: NETWORKS, secProfiles: [],
      totals: { radios: 3, networks: 4, clients: 11, capsManaged: 0, readOnly: 0 },
    }),
    view: (v) => doc.nodes['wnViewBtns'].fire('click', {
      target: { closest: (sel) => (sel === '.wl-sort-btn' ? { dataset: { wnview: v } } : null) },
    }),
    body: () => String(doc.nodes['wnTable'].innerHTML),
    head: () => String(doc.nodes['wnThead'].innerHTML),
    restore: () => { global.document = prevDoc; global.window = prevWin; },
  };
}

/** The group headings, in render order. */
const groups = (html: string): string[] =>
  [...html.matchAll(/font-weight:600;font-size:\.76rem">([^<]*)</g)].map((m) => m[1].trim());

/** The interface column of every data row. */
const ifaces = (html: string): string[] =>
  [...html.matchAll(/<td>(cap24|cap24-guest|cap5|wifi1)<\/td>/g)].map((m) => m[1]);

// ── 1. the default is unchanged: one group per radio ────────────────────────
{
  const { send, body, restore } = boot();
  send();
  const html = body();
  restore();
  assert.deepStrictEqual(groups(html), ['cap24', 'cap5', 'wifi1'],
    'the default view no longer groups by radio: ' + JSON.stringify(groups(html)));
  assert.deepStrictEqual(ifaces(html), ['cap24', 'cap24-guest', 'cap5', 'wifi1'],
    'a row went missing from the default view: ' + JSON.stringify(ifaces(html)));
  say('ok  the Radio view is the default and is unchanged');
}

// ── 2. the AP view puts both bands of one box together ─────────────────────
{
  const { send, view, body, restore } = boot();
  send();
  view('ap');
  const html = body();
  restore();
  assert.deepStrictEqual(groups(html), ['hap-ax2', 'This router'],
    'the AP view did not collapse the two radios into one access point, or lost ' +
    'the local radio: ' + JSON.stringify(groups(html)));
  assert.deepStrictEqual(ifaces(html), ['cap24', 'cap24-guest', 'cap5', 'wifi1'],
    'a row went missing from the AP view: ' + JSON.stringify(ifaces(html)));
  // A LOCAL RADIO IS A GROUP, NOT AN ABSENCE. On a manager that also runs radios
  // of its own those networks belong to the manager, and an empty heading would
  // read as a bug.
  assert.ok(html.includes('This router'),
    'the local radio has no heading of its own:\n' + html);
  say('ok  the AP view groups both bands of one access point');
}

// ── 3. the SSID view is one row per network, aggregated ────────────────────
{
  const { send, view, body, head, restore } = boot();
  send();
  view('ssid');
  const html = body();
  const th = head();
  restore();
  assert.ok(!html.includes('wn-radio-row'),
    'the SSID view still draws group headings:\n' + html);
  const rows = (html.match(/<tr>/g) || []).length;
  assert.strictEqual(rows, 3,
    'expected one row per SSID (guest, Guest, Home), got ' + rows + ':\n' + html);
  // guest is on both bands and both must show, or the aggregate has silently
  // picked one.
  assert.ok(html.includes('2.4GHz') && html.includes('5GHz'),
    'the aggregated row does not show both of its bands:\n' + html);
  // The clients of every interface carrying the SSID, summed: 3 + 5.
  assert.ok(/>8</.test(html),
    'the SSID row does not sum its clients across radios:\n' + html);
  assert.ok(th.includes('Interfaces') && th.includes('Bands'),
    'the SSID view did not re-label its columns:\n' + th);
  say('ok  the SSID view aggregates across radios');
}

// ── 4. the flat view drops the grouping and keeps every row ────────────────
{
  const { send, view, body, restore } = boot();
  send();
  view('flat');
  const html = body();
  restore();
  assert.deepStrictEqual(groups(html), [],
    'the flat view still draws group headings: ' + JSON.stringify(groups(html)));
  assert.strictEqual(ifaces(html).length, 4,
    'the flat view lost rows — it is a view, not a filter:\n' + html);
  say('ok  the flat view drops the grouping and keeps every row');
}

// ── 5. the columns sort, and Band sorts by frequency ───────────────────────
//
// The same rule the Wifi Clients table follows: ranked, never alphabetical. With
// today's three bands the two agree, so the case that separates them is a row
// with NO band — which a CAPsMAN-provisioned interface reports when the manager
// names no channel.
{
  const { doc, send, view, body, restore } = boot();
  send();
  view('flat');
  const ths = doc.nodes['wnThead'].querySelectorAll('th');
  assert.strictEqual(ths.length, 7, 'expected 7 header cells, got ' + ths.length);
  ths[2].click();                      // Band
  const asc = ifaces(body());
  ths[2].click();                      // and back
  const desc = ifaces(body());
  restore();
  assert.deepStrictEqual(asc.slice(0, 2).sort(), ['cap24', 'cap24-guest'],
    'Band ascending did not lead with the 2.4GHz rows: ' + JSON.stringify(asc));
  assert.deepStrictEqual(desc, asc.slice().reverse(),
    'a second click on Band did not reverse it: ' + JSON.stringify(desc));
  say('ok  the columns sort in every view, Band by frequency');
}

fs.rmSync(OUT, { force: true });
say('wifi-networks-views: all checks passed');
