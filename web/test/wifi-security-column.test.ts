/**
 * THE SECURITY COLUMN ON THE WIFI CLIENTS TABLE.
 *
 * RouterOS 7.25 added two fields to the Wi-Fi registration table: `mfp`, whether
 * a client negotiated management frame protection (802.11w), and `eap-identity`,
 * who an enterprise client signed in as. Both names are in the 7.25.1 field list
 * on CHR Test. The column shows a pill for the first and the identity under it.
 *
 * DRIVEN THROUGH THE PAGE, for the reason wifi-standard-column.test.ts gives:
 * a helper can be right and never be wired into the row.
 *
 * NULL IS "NOT REPORTED". An older router sends no `mfp`, and the collector
 * passes that on as null. Rendering it as "No MFP" would accuse every client on
 * a 7.24 router of running unprotected, so the discriminating case below is the
 * null one.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const ENTRY = path.join(ROOT, 'testdata', '.wlsec-entry.ts');
fs.writeFileSync(ENTRY, "export { initWirelessPage } from '../web/src/pages/wireless.js';\n");
const OUT = path.join(ROOT, 'testdata', '.wlsec.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const IDS = [
  'wirelessTable', 'wlThead', 'wlSsidList', 'wlBandGrid',
  'wlSigBarE', 'wlSigBarG', 'wlSigBarF', 'wlSigBarP',
  'wlSigCntE', 'wlSigCntG', 'wlSigCntF', 'wlSigCntP',
  'wlBandNum24', 'wlBandNum5', 'wlBandNum6', 'wlBandRow6',
  'wlSigNoData', 'wlCount', 'wlSearch', 'wlFaBtn',
  // Settings -> Features: the watch list and a client's history.
  'wlWatchCard', 'wlWatchThead', 'wlWatchTable', 'wlWatchAdd', 'wlWatchMac', 'wlWatchLabel', 'wlWatchGrace',
  'wlWatchBadge', 'wlWatchNote', 'wlWatchHint', 'wlRecCard', 'wlRecNote', 'wlRecToggle', 'wlHistCard', 'wlHistBody', 'wlHistTitle', 'wlHistRange', 'wlHistClose',
];

function client(over) {
  return Object.assign({
    mac: '02:00:00:00:00:01', signal: -50, iface: '5GHz WiFi', txRate: '866Mbps',
    band: '5GHz', standard: 'Wi-Fi 5', ip: '', rxRate: '', uptime: '1h',
    ssid: 'Net', name: 'device',
  }, over);
}

function boot() {
  const doc = makeDoc(IDS, { allowUnknown: [
    'nf-cnt-wireless', // the Wifi page pushes its client count to the Dashboard's Network Flow card, which is not in this page's document
    'faModal', 'faOpenBtn', 'wirelessTabBadge', 'wifiSortBtns', 'wlGroupBtn', 'ndWirelessCount', 'wlBand24', 'wlBand5', 'wlBand6'] });
  const handlers = {};
  const socket = { on: (ev, fn) => { handlers[ev] = fn; }, emit: () => {} };
  const prevDoc = global.document;
  const prevWin = global.window;
  global.document = doc;
  global.window = { addEventListener: () => {}, setTimeout, clearTimeout };
  const { initWirelessPage } = require(OUT);
  initWirelessPage(socket, () => true);
  return {
    doc,
    send: (clients) => handlers['wireless:update']({
      ts: 1, pollMs: 30000, mode: 'wifi', capsmanAvailable: false,
      clients, ssids: [], ssidsManagedElsewhere: 0,
    }),
    restore: () => { global.document = prevDoc; global.window = prevWin; },
  };
}

// ── 1. on, off and not reported render as three different things ───────────
{
  const { doc, send, restore } = boot();
  send([
    client({ mac: '02:00:00:00:00:01', name: 'on', mfp: true, eapIdentity: 'alice@example.org' }),
    client({ mac: '02:00:00:00:00:02', name: 'off', mfp: false, eapIdentity: '' }),
    client({ mac: '02:00:00:00:00:03', name: 'old', mfp: null, eapIdentity: '' }),
  ]);
  const html = String(doc.nodes['wirelessTable'].innerHTML);
  restore();
  const rows = html.split('<tr').slice(1);
  const row = (n: string): string => rows.find((r) => r.includes('>' + n + '<')) || '';
  assert.ok(/pill pill-ok">MFP</.test(row('on')), 'mfp=true did not render a green MFP pill:\n' + row('on'));
  assert.ok(row('on').includes('alice@example.org'), 'the EAP identity is not shown:\n' + row('on'));
  assert.ok(/pill pill-warn">No MFP</.test(row('off')), 'mfp=false did not render an amber No MFP pill:\n' + row('off'));
  assert.ok(!/MFP</.test(row('old')),
    'a router that does not report MFP rendered an MFP pill; null is "not reported", not "off":\n' + row('old'));
  assert.ok(/>-</.test(row('old')), 'not reported must read as a dash:\n' + row('old'));
  say('ok  MFP on, off and not reported render differently, with the identity');
}

// ── 2. the identity is escaped ────────────────────────────────────────────────
//
// It is typed on the client device, so it is whatever a stranger chose.
{
  const { doc, send, restore } = boot();
  send([client({ mfp: true, eapIdentity: '<img src=x onerror=alert(1)>' })]);
  const html = String(doc.nodes['wirelessTable'].innerHTML);
  restore();
  assert.ok(!html.includes('<img'), 'the EAP identity reached the page as markup:\n' + html);
  assert.ok(html.includes('&lt;img'), 'the EAP identity was dropped instead of escaped:\n' + html);
  say('ok  the EAP identity is escaped');
}

// ── 3. one cell per header, and Security sits right of Standard ──────────────
{
  const { doc, send, restore } = boot();
  send([client({ mfp: true })]);
  const head = String(doc.nodes['wlThead'].innerHTML);
  const body = String(doc.nodes['wirelessTable'].innerHTML);
  restore();
  const ths = (head.match(/<th\b/g) || []).length;
  const tds = (body.match(/<td\b/g) || []).length;
  assert.strictEqual(tds, ths, tds + ' cells against ' + ths + ' headers:\n' + head + '\n' + body);
  assert.ok(head.indexOf('Security') > head.indexOf('Standard') && head.indexOf('Standard') >= 0,
    'Security is not to the right of Standard:\n' + head);
  say('ok  the row has one cell per header, Security right of Standard');
}

// ── 4. sorting puts the unprotected first and the unreported last ────────────
//
// Alphabetical on the label would put "MFP" before "No MFP" and the dash first;
// ranked, it is off, on, unknown. Fed in an order that is none of those.
{
  const names = (html: string): string[] =>
    [...html.matchAll(/font-weight:600;font-size:\.78rem">([^<]*)</g)].map((m) => m[1]);
  const { doc, send, restore } = boot();
  send([
    client({ mac: '02:00:00:00:00:01', name: 'b-on', iface: 'wifi1', mfp: true }),
    client({ mac: '02:00:00:00:00:02', name: 'c-old', iface: 'wifi1', mfp: null }),
    client({ mac: '02:00:00:00:00:03', name: 'a-off', iface: 'wifi1', mfp: false }),
  ]);
  const ths = doc.nodes['wlThead'].querySelectorAll('th');
  ths[4].click();
  const got = names(String(doc.nodes['wirelessTable'].innerHTML));
  restore();
  assert.deepStrictEqual(got, ['a-off', 'b-on', 'c-old'],
    'clicking Security did not order unprotected, protected, not reported: ' + JSON.stringify(got));
  say('ok  Security sorts unprotected first, not reported last');
}

fs.rmSync(OUT, { force: true });
say('wifi-security-column: all checks passed');
