/**
 * THE STANDARD COLUMN ON THE WIFI CLIENTS TABLE.
 *
 * ── WHAT IT IS FOR ──────────────────────────────────────────────────────────
 *
 * Band says WHICH RADIO a client is on; Standard says WHICH GENERATION it
 * negotiated with that radio. They are orthogonal, and the gap between them is
 * the point: a client reading 5GHz + Wi-Fi 5 on a Wi-Fi 6 access point is not
 * getting Wi-Fi 6, and nothing else on the page would say so. Measured on a live
 * hAP AX3 on 2026-09-07, where a client was doing exactly that (`band=5ghz-ac`).
 *
 * ── DRIVEN THROUGH THE PAGE, NOT THROUGH THE BADGE ──────────────────────────
 *
 * `standardBadge` could be perfectly correct and simply not wired into the row,
 * which is a failure this port has shipped before - "written but never called".
 * So this boots the real page module and feeds it a real `wireless:update`,
 * which exercises the collector's field name, the row builder and the badge in
 * one go. A unit test on the badge alone would pass against a table that never
 * renders it.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const ENTRY = path.join(ROOT, 'testdata', '.wl-entry.ts');
fs.writeFileSync(ENTRY, "export { initWirelessPage } from '../web/src/pages/wireless.js';\n");
const OUT = path.join(ROOT, 'testdata', '.wl.cjs');
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
];

function client(over) {
  return Object.assign({
    mac: '02:00:00:00:00:01', signal: -50, iface: '5GHz WiFi', txRate: '866Mbps',
    band: '5GHz', standard: 'Wi-Fi 5', ip: '', rxRate: '', uptime: '1h',
    ssid: 'Net', name: 'device',
  }, over);
}

function boot() {
  const doc = makeDoc(IDS, { allowUnknown: ['faModal', 'faOpenBtn', 'wirelessTabBadge', 'wifiSortBtns', 'wlGroupBtn', 'ndWirelessCount', 'wlBand24', 'wlBand5', 'wlBand6'] });
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

// ── 1. every generation renders in its own colour ───────────────────────────
{
  const { doc, send, restore } = boot();
  send([
    client({ mac: '02:00:00:00:00:01', standard: 'Wi-Fi 4' }),
    client({ mac: '02:00:00:00:00:02', standard: 'Wi-Fi 5' }),
    client({ mac: '02:00:00:00:00:03', standard: 'Wi-Fi 6' }),
    client({ mac: '02:00:00:00:00:04', standard: 'Wi-Fi 6E' }),
    client({ mac: '02:00:00:00:00:05', standard: 'Wi-Fi 7' }),
    client({ mac: '02:00:00:00:00:06', standard: 'Legacy' }),
  ]);
  const html = String(doc.nodes['wirelessTable'].innerHTML);
  restore();

  // THE CLASS IS THE COLOUR. A distinct class per generation is what was asked
  // for, so asserting the label alone would pass against six identical grey
  // pills - which is the version of this feature that is not worth having.
  const want = [
    ['Wi-Fi 4', 'wl-std-4'], ['Wi-Fi 5', 'wl-std-5'], ['Wi-Fi 6', 'wl-std-6'],
    ['Wi-Fi 6E', 'wl-std-6e'], ['Wi-Fi 7', 'wl-std-7'], ['Legacy', 'wl-std-legacy'],
  ];
  for (const [label, cls] of want) {
    assert.ok(html.includes(cls),
      'no "' + cls + '" pill rendered, so ' + label + ' has no colour of its own:\n' + html);
    assert.ok(html.includes('>' + label + '<'),
      'the ' + label + ' pill does not carry its label:\n' + html);
  }
  const classes = new Set((html.match(/wl-std-[a-z0-9]+/g) || []));
  assert.strictEqual(classes.size, 6,
    'six generations rendered ' + classes.size + ' distinct colours; two share one, ' +
    'so the column cannot be read at a glance: ' + [...classes].join(', '));
  say('ok  each standard renders its own coloured pill');
}

// ── 2. an unknown generation is a DASH, not a blank pill ────────────────────
//
// A CAPsMAN row carries no band at all - `wlBandOf` falls back to the interface
// name for the band, and there is nothing to fall back to for the generation. An
// empty pill reads as a value; the dash matches every other column's absence.
{
  const { doc, send, restore } = boot();
  send([client({ standard: '', band: '5GHz' })]);
  const html = String(doc.nodes['wirelessTable'].innerHTML);
  restore();
  assert.ok(/-|-/.test(html),
    'a client with no known standard rendered nothing at all; the cell must say ' +
    '"not known" rather than look like a rendering failure:\n' + html);
  assert.ok(!/wl-std\s/.test(html) && !/wl-std"/.test(html),
    'a client with no known standard rendered a pill rather than a dash:\n' + html);
  // AND THE BAND MUST SURVIVE IT. The two are independent, and a CAPsMAN row
  // has a band while having no generation.
  assert.ok(/wl-band/.test(html),
    'the band pill vanished for a client with no standard, so the new column ' +
    'has broken the old one:\n' + html);
  say('ok  an unknown standard is a dash, and the band pill still renders');
}

// ── 3. the column is in the header, to the RIGHT of Band ────────────────────
//
// That position was asked for specifically, and the markup comment on the static
// header says it "must match renderWireless()'s column list" - two places that
// can disagree silently.
{
  const { doc, send, restore } = boot();
  send([client({})]);
  const head = String(doc.nodes['wlThead'].innerHTML);
  restore();
  const band = head.indexOf('Band');
  const std = head.indexOf('Standard');
  assert.ok(band >= 0, 'the Band header is gone:\n' + head);
  assert.ok(std >= 0, 'no Standard header was rendered:\n' + head);
  assert.ok(std > band,
    'Standard is rendered to the LEFT of Band; it was asked for on the right ' +
    'because it qualifies the band:\n' + head);
  say('ok  Standard sits to the right of Band in the header');
}

// ── 4. the row still has as many cells as the header has columns ────────────
//
// Adding a column means several edits - colgroup, thead, the row, and two
// colspans - and a missed one shifts every later cell under the wrong heading
// without throwing anything.
{
  const { doc, send, restore } = boot();
  send([client({})]);
  const head = String(doc.nodes['wlThead'].innerHTML);
  const body = String(doc.nodes['wirelessTable'].innerHTML);
  restore();
  const ths = (head.match(/<th\b/g) || []).length;
  const tds = (body.match(/<td\b/g) || []).length;
  assert.strictEqual(tds, ths,
    'the row renders ' + tds + ' cells against ' + ths + ' headers, so every ' +
    'column after the mismatch sits under the wrong heading.\n' + head + '\n' + body);
  say('ok  the row has one cell per header column (' + ths + ')');
}

// ── 5. Band and Standard sort when their headers are clicked ───────────────
//
// Reported as "I can't sort by Band or Standard by clicking the header names".
// Both columns were declared without a sort key, on the stated grounds that they
// are "derived labels" - which is not a reason, since every column in this table
// is derived from something.
//
// ── THE DISCRIMINATING CASE IS THE UNKNOWN ONE ──────────────────────────────
//
// With today's vocabulary, alphabetical and ranked order AGREE: "2.4GHz" <
// "5GHz" < "6GHz", and "Legacy" < "Wi-Fi 4" < … < "Wi-Fi 7". So a test built
// only from real values cannot tell a ranked comparator from a `localeCompare`
// one, and would pass against an implementation that breaks the moment MikroTik
// ships a band whose name sorts differently from its frequency.
//
// The empty value is what separates them, and it is not hypothetical: a CAPsMAN
// row carries no band at all and therefore no generation. Alphabetically "" is
// FIRST; ranked, it is last. Every assertion below leans on that.
//
// ONE INTERFACE FOR EVERY CLIENT, deliberately. The table groups by interface
// and only draws group headers when there is more than one, so a single
// interface isolates the comparator from the grouping - which has its own
// ordering behaviour and is not what was reported.
{
  const IFACE = 'wifi1';
  const names = (html: string): string[] =>
    [...html.matchAll(/font-weight:600;font-size:\.78rem">([^<]*)</g)].map((m) => m[1]);

  const { doc, send, restore } = boot();
  // Fed in an order that is neither the band order nor the standard order, so
  // neither result can come from the input surviving unsorted.
  send([
    client({ mac: '02:00:00:00:00:01', name: 'c-5-wifi6', iface: IFACE, band: '5GHz', standard: 'Wi-Fi 6' }),
    client({ mac: '02:00:00:00:00:02', name: 'd-none', iface: IFACE, band: '', standard: '' }),
    client({ mac: '02:00:00:00:00:03', name: 'a-24-legacy', iface: IFACE, band: '2.4GHz', standard: 'Legacy' }),
    client({ mac: '02:00:00:00:00:04', name: 'b-6-wifi7', iface: IFACE, band: '6GHz', standard: 'Wi-Fi 7' }),
  ]);

  const ths = doc.nodes['wlThead'].querySelectorAll('th');
  const idx = { band: 2, standard: 3 };
  assert.strictEqual(ths.length, 7, 'expected 7 header cells, got ' + ths.length);

  // ── Band ────────────────────────────────────────────────────────────────
  ths[idx.band].click();
  let got = names(String(doc.nodes['wirelessTable'].innerHTML));
  assert.deepStrictEqual(got, ['a-24-legacy', 'c-5-wifi6', 'b-6-wifi7', 'd-none'],
    'clicking Band did not order by frequency with the unknown last. If the ' +
    'unknown came FIRST the comparator is alphabetical, not ranked; if nothing ' +
    'moved the column has no sort key at all, which is the reported bug.\n' +
    'got: ' + JSON.stringify(got));
  say('ok  Band sorts by frequency, unknown last');

  // A second click reverses, which is what every other column here does.
  ths[idx.band].click();
  got = names(String(doc.nodes['wirelessTable'].innerHTML));
  assert.deepStrictEqual(got, ['d-none', 'b-6-wifi7', 'c-5-wifi6', 'a-24-legacy'],
    'clicking Band twice did not reverse the order: ' + JSON.stringify(got));
  say('ok  a second Band click reverses it');

  // ── Standard ────────────────────────────────────────────────────────────
  ths[idx.standard].click();
  got = names(String(doc.nodes['wirelessTable'].innerHTML));
  assert.deepStrictEqual(got, ['a-24-legacy', 'c-5-wifi6', 'b-6-wifi7', 'd-none'],
    'clicking Standard did not order by generation with the unknown last. ' +
    'Legacy before Wi-Fi 6 before Wi-Fi 7, and the CAPsMAN-shaped row with no ' +
    'generation at the end.\ngot: ' + JSON.stringify(got));
  say('ok  Standard sorts by generation, unknown last');

  ths[idx.standard].click();
  got = names(String(doc.nodes['wirelessTable'].innerHTML));
  assert.deepStrictEqual(got, ['d-none', 'b-6-wifi7', 'c-5-wifi6', 'a-24-legacy'],
    'clicking Standard twice did not reverse the order: ' + JSON.stringify(got));
  say('ok  a second Standard click reverses it');

  // ── and the header shows WHICH column is sorted ─────────────────────────
  //
  // Without the indicator a user cannot tell a sorted table from an unsorted
  // one, which is half of what "I cannot sort by this" means in practice.
  const head = String(doc.nodes['wlThead'].innerHTML);
  assert.ok(/sort-(asc|desc)/.test(head),
    'no sort indicator class is rendered on the active column, so nothing on ' +
    'screen says the table is sorted:\n' + head);
  restore();
  say('ok  the active sort column is marked');
}

// ── 6. Interface stays UNSORTABLE, which is not an oversight ────────────────
//
// The table groups by interface, so a sort on it would order rows by the very
// thing the grouping has already collapsed. Pinned so that "make the headers
// sortable" is not later read as "make them all sortable".
{
  const { doc, send, restore } = boot();
  send([client({})]);
  const ths = doc.nodes['wlThead'].querySelectorAll('th');
  const ifaceTh = ths[1];
  const clickable = (ifaceTh._clicks || []).length;
  restore();
  assert.strictEqual(clickable, 0,
    'the Interface header became clickable. The table is GROUPED by interface, ' +
    'so sorting on it orders rows by what the grouping already collapsed.');
  say('ok  Interface is still not sortable, deliberately');
}

fs.rmSync(OUT, { force: true });
say('wifi-standard-column: all checks passed');
