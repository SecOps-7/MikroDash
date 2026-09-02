/**
 * A router NOBODY HAS ASKED ABOUT must not be drawn as Offline.
 *
 * ── THE DEFECT THIS PINS ────────────────────────────────────────────────────
 *
 * `connected` is a boolean, so "we asked and it is down" and "nothing has asked"
 * arrived at the browser identically. Open the Devices page and every device but
 * the selected one sat in the second state for the few seconds the overview pool
 * took to dial — and the page rendered all of them as red "Offline" cards, with
 * an Offline tile counting the whole fleet, until the first payload landed and
 * they all flipped to green at once.
 *
 * The server now sends `known` beside `connected`. This drives the REAL page
 * module against a DOM shim and asserts the three states stay three, in every
 * place the page draws status:
 *
 *   summary tiles · card badge · card icon · list row · search terms · popovers
 *
 * Each is a separate ternary in the source, which is exactly why they are
 * checked separately: five of the six were written from the same two-state
 * assumption and one of them was missed on the first pass.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

// ── the shim ────────────────────────────────────────────────────────────────
//
// innerHTML and textContent are RECORDED rather than parsed: every assertion
// below is about the markup the page wrote, so a shim that dropped it would let
// all of them pass against a page that rendered nothing.
function makeEl(id) {
  const classes = new Set();
  const node = {
    id,
    value: '',
    textContent: '',
    innerHTML: '',
    style: {},
    hidden: false,
    setAttribute: (k, v) => { node[k] = v; },
    getAttribute: (k) => (k in node ? node[k] : null),
    classList: {
      add: (c) => classes.add(c),
      remove: (c) => classes.delete(c),
      contains: (c) => classes.has(c),
      toggle: (c, on) => (on ? classes.add(c) : classes.delete(c)),
    },
    addEventListener: () => {},
    appendChild: () => {},
    querySelectorAll: () => [],
    querySelector: () => null,
  };
  return node;
}

function makeDoc() {
  const ids = [
    'rsTotal', 'rsOnline', 'rsOffline', 'rsAlerting', 'rsSites',
    'routersSearch', 'routersShown', 'routersSiteFilter', 'routersView',
    'routers-grid', 'routersListWrap', 'routersListBody',
    'routersMapWrap', 'rtrMapTray',
  ];
  const els = {};
  ids.forEach((id) => { els[id] = makeEl(id); });
  return {
    els,
    getElementById: (id) => els[id] || null,
    querySelectorAll: () => [],
    querySelector: () => null,
    addEventListener: () => {},
    body: makeEl('body'),
  };
}

// ── the port ────────────────────────────────────────────────────────────────
const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-devices-unknown.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'routers.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

const doc = makeDoc();
global.document = doc;
global.window = { addEventListener: () => {}, location: { pathname: '/devices' } };

const page = require(OUT);

/** One row, defaulted to every field the page reads. */
function row(over) {
  return Object.assign({
    id: 'r', label: 'R', host: '198.51.100.1', isActive: false,
    connected: false, known: false, lastError: null, openAlerts: 0,
    cpu: null, uptime: null, memPct: null, hddPct: null,
    version: null, boardName: null, arch: null, serial: null, licenseLevel: null,
    rxMbps: null, txMbps: null, clients: null,
    siteIds: [], siteNames: [], siteId: null, siteName: null, geo: null,
  }, over);
}

// The three states, once, reused by every case below.
const UNASKED = row({ id: 'u', label: 'Unasked' });
const UP = row({ id: 'a', label: 'Up', known: true, connected: true });
const DOWN = row({ id: 'd', label: 'Down', known: true, connected: false,
  lastError: 'dial: connection refused' });
const ALL = [UNASKED, UP, DOWN];

let failed = 0;
function check(what, fn) {
  try { fn(); say('  ok   ' + what); } catch (e) { failed++; say('  FAIL ' + what + '\n       ' + e.message); }
}

say('devices: a router nobody has asked about is not Offline');

// ── the summary tiles ───────────────────────────────────────────────────────
page.renderRoutersSummary(ALL);
check('Offline counts only routers actually observed down', () => {
  assert.equal(doc.els.rsOffline.textContent, '1',
    'Offline read ' + doc.els.rsOffline.textContent + ' of 3 routers; it used to be '
    + 'derived as total-minus-online, which counts every unasked router as down');
  assert.equal(doc.els.rsOnline.textContent, '1');
  assert.equal(doc.els.rsTotal.textContent, '3');
});
check('Online + Offline may be less than Total while a sweep is running', () => {
  const total = Number(doc.els.rsTotal.textContent);
  const seen = Number(doc.els.rsOnline.textContent) + Number(doc.els.rsOffline.textContent);
  assert.ok(seen < total, 'the two tiles still account for every device, so the '
    + 'unasked state is being folded into one of them');
});

// ── the search terms ────────────────────────────────────────────────────────
check('searching "offline" does not list unasked routers', () => {
  assert.equal(page.rtrMatches(DOWN, 'offline'), true);
  assert.equal(page.rtrMatches(UNASKED, 'offline'), false,
    'an unasked router answered the offline search, so the term means '
    + '"not currently known to be up" rather than "down"');
  assert.equal(page.rtrMatches(UP, 'online'), true);
  assert.equal(page.rtrMatches(UNASKED, 'online'), false);
});

// ── the cards ───────────────────────────────────────────────────────────────
page.setView('comfortable');
page.renderRoutersStats(ALL);
const grid = doc.els['routers-grid'].innerHTML;
check('the card badge has three states, not two', () => {
  assert.ok(/Checking/.test(grid), 'no neutral badge was rendered at all');
  assert.equal((grid.match(/>Offline</g) || []).length, 1,
    'expected exactly one Offline badge across three routers');
  assert.equal((grid.match(/>Online</g) || []).length, 1);
});
check('the unasked card is not painted in the offline red', () => {
  // The badge and the icon are separate ternaries and were separately wrong.
  const cards = grid.split('<div class="card h-100">');
  const unasked = cards.find((c) => c.indexOf('Unasked') !== -1);
  assert.ok(unasked, 'the unasked router did not render');
  assert.ok(unasked.indexOf('#d63939') === -1,
    'the unasked card still carries the offline red; the wifi icon stroke is a '
    + 'second ternary on `connected` and is easy to miss');
  assert.ok(unasked.indexOf('bg-red-lt') === -1);
});
check('a genuinely offline card keeps its red and its reason', () => {
  const cards = grid.split('<div class="card h-100">');
  const down = cards.find((c) => c.indexOf('Down') !== -1);
  assert.ok(down.indexOf('#d63939') !== -1, 'the observed-down card lost its red');
  assert.ok(down.indexOf('dial: connection refused') !== -1,
    'the reason a router is down must still be shown; suppressing it would be '
    + 'the opposite failure to the one this file exists for');
});

// ── the list view ───────────────────────────────────────────────────────────
page.setView('list');
page.renderRoutersStats(ALL);
const list = doc.els.routersListBody.innerHTML;
check('the list dims only rows observed to be down', () => {
  assert.equal((list.match(/rtl-offline/g) || []).length, 1,
    'rtl-offline dims the row, and it was applied to every not-yet-checked '
    + 'router as well as the one that is actually down');
  assert.ok(/title="Checking/.test(list), 'the list dot has no neutral state');
});

// ── the map popovers ────────────────────────────────────────────────────────
check('a popover dot is grey, green or red', () => {
  const grey = page.dotColour(UNASKED);
  assert.ok(grey.indexOf('green') === -1 && grey.indexOf('red') === -1,
    'dotColour(unasked) = ' + grey);
  assert.ok(page.dotColour(UP).indexOf('green') !== -1);
  assert.ok(page.dotColour(DOWN).indexOf('red') !== -1);
});
check('a cluster does not announce unasked routers as offline', () => {
  const html = page.groupPopHtml({ key: 'k', x: 0, y: 0, routers: ALL });
  assert.ok(/1 offline/.test(html),
    'the cluster reported "' + (html.match(/\d+ offline/) || ['nothing'])[0]
    + '"; one of these three routers is down and the other two are unknown');
});

if (failed) { say('\n' + failed + ' failed'); process.exit(1); }
say('\nall passed');
