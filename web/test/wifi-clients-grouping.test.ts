/**
 * GROUPING, FOLDING AND THE ADDRESS SORT ON THE WIFI CLIENTS TABLE.
 *
 * ── WHAT WAS ASKED FOR ──────────────────────────────────────────────────────
 *
 * Three things, and they are independent: turn the grouping by access point off
 * and get a flat list; fold one noisy access point away and leave the rest; sort
 * by address alongside the sorts that were already there.
 *
 * ── DRIVEN THROUGH THE PAGE, NOT THROUGH THE HELPERS ────────────────────────
 *
 * `ipRank` could be perfectly correct and not wired to a button, which is the
 * "written but never called" failure this port has shipped before. So this boots
 * the real module, feeds it a real `wireless:update`, and presses the controls
 * the markup declares.
 *
 * ── THE ADDRESS CASE THAT SEPARATES A REAL SORT FROM A STRING ONE ───────────
 *
 * `198.51.100.9` and `198.51.100.100`. Alphabetically the .100 comes first,
 * which is the bug this column would otherwise have and which a test built from
 * .1/.2/.3 cannot see. The addressless client is the second discriminator: it is
 * unknown rather than lowest, so it belongs at the END ascending - the same rule
 * Band and Standard already follow.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const ENTRY = path.join(ROOT, 'testdata', '.wlgrp-entry.ts');
fs.writeFileSync(ENTRY, "export { initWirelessPage } from '../web/src/pages/wireless.js';\n");
const OUT = path.join(ROOT, 'testdata', '.wlgrp.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const IDS = [
  'wirelessTable', 'wlThead', 'wlSsidList', 'wlBandGrid', 'wifiSortBtns', 'wlGroupBtn',
  'wlSigBarE', 'wlSigBarG', 'wlSigBarF', 'wlSigBarP',
  'wlSigCntE', 'wlSigCntG', 'wlSigCntF', 'wlSigCntP',
  'wlBandNum24', 'wlBandNum5', 'wlBandNum6', 'wlBandRow6',
  'wirelessTabBadge', 'ndWirelessCount',
];

function client(over) {
  return Object.assign({
    mac: '02:00:00:00:00:01', signal: -50, iface: 'ap-one', txRate: '866Mbps',
    band: '5GHz', standard: 'Wi-Fi 5', ip: '', rxRate: '', uptime: '1h',
    ssid: 'Net', name: 'device',
  }, over);
}

function boot() {
  const doc = makeDoc(IDS, { allowUnknown: ['faModal', 'faOpenBtn', 'wlBand24', 'wlBand5', 'wlBand6'] });
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
    /** Press a button in the sort bar, the way a browser delivers the event. */
    sortBy: (key) => doc.nodes['wifiSortBtns'].fire('click', {
      target: { closest: (sel) => (sel === '.wl-sort-btn' ? { dataset: { sort: key } } : null) },
    }),
    body: () => String(doc.nodes['wirelessTable'].innerHTML),
    restore: () => { global.document = prevDoc; global.window = prevWin; },
  };
}

/** The device names in render order, read out of the rows themselves. */
const names = (html: string): string[] =>
  [...html.matchAll(/font-weight:600;font-size:\.78rem">([^<]*)</g)].map((m) => m[1]);

// Distinct signals, because the table's default sort is by signal and equal
// values would leave the order decided by the input - which proves nothing about
// where the grouping put each client.
const TWO_APS = [
  client({ mac: '02:00:00:00:00:01', name: 'a-one', iface: 'ap-one', signal: -40, ip: '198.51.100.9' }),
  client({ mac: '02:00:00:00:00:02', name: 'b-one', iface: 'ap-one', signal: -50, ip: '198.51.100.100' }),
  client({ mac: '02:00:00:00:00:03', name: 'c-two', iface: 'ap-two', signal: -60, ip: '198.51.100.2' }),
];

// ── 1. grouped is the default, and every group carries a fold control ───────
{
  const { doc, send, body, restore } = boot();
  send(TWO_APS);
  const html = body();
  const btn = doc.nodes['wlGroupBtn'];
  restore();

  assert.ok(html.includes('wl-group-row'),
    'no group header rendered for two access points:\n' + html);
  const toggles = (html.match(/class="wl-group-toggle"/g) || []).length;
  assert.strictEqual(toggles, 2,
    'expected one fold control per access point, got ' + toggles + ':\n' + html);
  // A REAL BUTTON, not a click handler on the row: it is the only control in
  // this table and it has to be reachable by keyboard.
  assert.ok(/<button[^>]*class="wl-group-toggle"/.test(html),
    'the group header is not a button, so it cannot be operated from the keyboard:\n' + html);
  assert.deepStrictEqual(names(html), ['a-one', 'b-one', 'c-two'],
    'the clients are not under their own access points: ' + JSON.stringify(names(html)));
  assert.strictEqual(String(btn.textContent), 'Grouped by AP',
    'the toggle does not say what the table is doing: ' + btn.textContent);
  say('ok  two access points render two foldable groups');
}

// ── 2. folding one group hides ITS clients and nobody else's ────────────────
{
  const { doc, send, body, restore } = boot();
  send(TWO_APS);
  doc.nodes['wlGrp0'].fire('click');
  const shut = body();

  assert.deepStrictEqual(names(shut), ['c-two'],
    'folding the first access point did not hide its clients, or hid the other ' +
    "group's too: " + JSON.stringify(names(shut)));
  assert.strictEqual((shut.match(/class="wl-group-toggle"/g) || []).length, 2,
    'the folded group lost its header, so there is no way to unfold it:\n' + shut);
  assert.ok(shut.includes('aria-expanded="false"') && shut.includes('aria-expanded="true"'),
    'the headers do not report their state to a screen reader:\n' + shut);

  // And back. The second press states the same target state the first one
  // inverted, so a repeat cannot leave the two disagreeing.
  doc.nodes['wlGrp0'].fire('click');
  const open = body();
  restore();
  assert.deepStrictEqual(names(open), ['a-one', 'b-one', 'c-two'],
    'unfolding did not bring the clients back: ' + JSON.stringify(names(open)));
  say('ok  a group folds and unfolds on its own, leaving the others alone');
}

// ── 3. the toggle turns grouping off, and nothing is lost ───────────────────
{
  const { doc, send, body, restore } = boot();
  send(TWO_APS);
  doc.nodes['wlGroupBtn'].fire('click');
  const flat = body();
  const label = String(doc.nodes['wlGroupBtn'].textContent);

  assert.ok(!flat.includes('wl-group-row'),
    'group headers survived the toggle, so it did nothing:\n' + flat);
  assert.deepStrictEqual(names(flat), ['a-one', 'b-one', 'c-two'],
    'turning grouping off dropped rows - it is a view, not a filter: ' +
    JSON.stringify(names(flat)));
  assert.strictEqual(label, 'Flat list',
    'the toggle still claims the table is grouped: ' + label);
  // THE INTERFACE COLUMN IS WHAT CARRIES THE ACCESS POINT once the header is
  // gone, so a flat list must not leave it blank.
  assert.ok(flat.includes('ap-one') && flat.includes('ap-two'),
    'a flat list does not say which access point each client is on:\n' + flat);

  doc.nodes['wlGroupBtn'].fire('click');
  const back = body();
  restore();
  assert.ok(back.includes('wl-group-row'), 'grouping did not come back:\n' + back);
  say('ok  grouping toggles off to a flat list and back');
}

// ── 4. a single access point still draws no header ──────────────────────────
//
// Not an oversight: the header would repeat the Interface column on every row,
// and there is nothing to fold it away FROM. Pinned so the grouping rework is
// not read as "always draw a header".
{
  const { send, body, restore } = boot();
  send([client({ name: 'only', iface: 'ap-one' })]);
  const html = body();
  restore();
  assert.ok(!html.includes('wl-group-row'),
    'one access point drew a group header, which just repeats the column:\n' + html);
  say('ok  a single access point is not a grouping');
}

// ── 5. the address sort is numeric, and the addressless go last ─────────────
{
  const { send, sortBy, body, restore } = boot();
  // One interface, so the grouping - which has ordering behaviour of its own -
  // cannot be what produces the result.
  send([
    client({ mac: '02:00:00:00:00:01', name: 'c-100', iface: 'ap', ip: '198.51.100.100' }),
    client({ mac: '02:00:00:00:00:02', name: 'x-none', iface: 'ap', ip: '' }),
    client({ mac: '02:00:00:00:00:03', name: 'a-9', iface: 'ap', ip: '198.51.100.9' }),
    client({ mac: '02:00:00:00:00:04', name: 'b-20', iface: 'ap', ip: '198.51.100.20' }),
  ]);
  sortBy('ip');
  const got = names(body());
  restore();
  assert.deepStrictEqual(got, ['a-9', 'b-20', 'c-100', 'x-none'],
    'the address sort is not numeric, or the addressless client did not sort ' +
    'last. Alphabetically .100 comes before .9, which is the bug this column ' +
    'would otherwise have.\ngot: ' + JSON.stringify(got));
  say('ok  IP sorts by address, unknown last');
}

// ── 6. a second press on the same button reverses the sort ─────────────────
//
// Reported as "let me switch ascending and descending by pressing the same
// button". The bar used to reset to the column's natural direction on every
// press, so the reversed order was reachable from the column headers and not
// from the buttons - and IP has no header at all, which made half its orders
// unreachable entirely.
{
  const { send, sortBy, body, restore } = boot();
  send([
    client({ mac: '02:00:00:00:00:01', name: 'c-100', iface: 'ap', ip: '198.51.100.100' }),
    client({ mac: '02:00:00:00:00:02', name: 'a-9', iface: 'ap', ip: '198.51.100.9' }),
    client({ mac: '02:00:00:00:00:03', name: 'b-20', iface: 'ap', ip: '198.51.100.20' }),
  ]);

  sortBy('ip');
  assert.deepStrictEqual(names(body()), ['a-9', 'b-20', 'c-100'],
    'the first press did not sort ascending');

  sortBy('ip');
  assert.deepStrictEqual(names(body()), ['c-100', 'b-20', 'a-9'],
    'a second press on IP did not reverse the order: ' + JSON.stringify(names(body())));

  sortBy('ip');
  assert.deepStrictEqual(names(body()), ['a-9', 'b-20', 'c-100'],
    'a third press did not go back: ' + JSON.stringify(names(body())));

  // A DIFFERENT BUTTON STARTS AT ITS OWN NATURAL DIRECTION rather than
  // inheriting the one before it - Name is A to Z whatever IP was doing.
  sortBy('ip');
  sortBy('name');
  const got = names(body());
  restore();
  assert.deepStrictEqual(got, ['a-9', 'b-20', 'c-100'],
    'pressing Name inherited the previous direction instead of starting at its ' +
    'own: ' + JSON.stringify(got));
  say('ok  a repeat press reverses, a new column starts at its natural direction');
}

fs.rmSync(OUT, { force: true });
say('wifi-clients-grouping: all checks passed');
