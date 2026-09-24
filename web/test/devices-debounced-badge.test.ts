/**
 * THE FLEET'S BADGE READS THE DEBOUNCED VERDICT, NOT THE LIVE SOCKET.
 *
 * ── THE DEFECT THIS PINS ────────────────────────────────────────────────────
 *
 * Every device carries an "Offline threshold" - `connDownThresholdSec`, thirty
 * seconds by default - and the field's own help said "Status badge and alerts
 * only change after the router has been unreachable for this long". Neither was
 * true. The badge came straight from `Session.Connected()`, so a routine
 * five-second reconnect painted the card red and back, and a router in a dial
 * loop flickered.
 *
 * The frame now carries BOTH facts and they are not interchangeable:
 *
 *   connected - the API socket this instant. The banner, the header dots and
 *               the write path; those must react at once.
 *   online    - the debounced verdict, decided by `internal/connstate`. Every
 *               badge, dot, tile, search term and marker on the fleet's pages.
 *
 * ── AND IT IS CHECKED IN BOTH DIRECTIONS ────────────────────────────────────
 *
 * A row where the two agree proves nothing: the page passed for a year reading
 * the wrong one. Every case below feeds a row where they DISAGREE, so reverting
 * any single ternary to `connected` fails here. Each place the page draws
 * status is a separate expression in the source - tiles, card badge, card icon,
 * list row, search terms, popover dot - and they were separately wrong once
 * already (see devices-unknown-state.test.ts).
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

// innerHTML and textContent are RECORDED rather than parsed: every assertion is
// about the markup the page wrote, so a shim that dropped it would let them all
// pass against a page that rendered nothing.
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

const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-devices-debounce.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'routers.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });

const doc = makeDoc();
global.document = doc;
global.window = { addEventListener: () => {}, location: { pathname: '/devices' } };

const page = require(OUT);

function row(over) {
  return Object.assign({
    id: 'r', label: 'R', host: '198.51.100.1', isActive: false,
    connected: false, online: false, known: true, lastError: null, openAlerts: 0,
    cpu: null, uptime: null, memPct: null, hddPct: null,
    version: null, boardName: null, arch: null, serial: null, licenseLevel: null,
    rxMbps: null, txMbps: null, clients: null,
    siteIds: [], siteNames: [], siteId: null, siteName: null, geo: null,
  }, over);
}

// ── THE TWO ROWS WHERE THE FACTS DISAGREE ───────────────────────────────────
//
// BLIP is the case the threshold exists for: the socket is shut right now and
// the debounce has not expired, so the fleet still counts it up.
const BLIP = row({ id: 'b', label: 'Blip', connected: false, online: true });
// SETTLING is the mirror: the outage was declared, and the socket has just come
// back. A page reading `connected` would call it Online before the debounce
// agrees, which is the same bug in the other direction.
const SETTLING = row({ id: 's', label: 'Settling', connected: true, online: false,
  lastError: 'dial: connection refused' });
const ALL = [BLIP, SETTLING];

let failed = 0;
function check(what, fn) {
  try { fn(); say('  ok   ' + what); } catch (e) { failed++; say('  FAIL ' + what + '\n       ' + e.message); }
}

say('devices: the badge follows the Offline threshold, not the socket');

// ── the summary tiles ───────────────────────────────────────────────────────
//
// ── ITS OWN ROWS, AND THAT IS NOT TIDINESS ──────────────────────────────────
//
// Driven with BLIP and SETTLING the tiles read 1 and 1 whichever field they
// count, because the two rows are mirror images: swapping the field swaps which
// router lands in which tile and the NUMBERS do not move. Planting the failure
// is what showed it - every other check here failed and this one passed. Two
// blips against one settled outage makes the counts asymmetric, so the tiles
// read 2/1 on the verdict and 1/2 on the socket.
const TILES = [BLIP, row({ id: 'b2', label: 'Blip2', connected: false, online: true }), SETTLING];
page.renderRoutersSummary(TILES);
check('the tiles count the debounced verdict', () => {
  assert.equal(doc.els.rsOnline.textContent, '2',
    'Online read ' + doc.els.rsOnline.textContent + ' of 3; a router inside its '
    + 'Offline threshold must still be counted up');
  assert.equal(doc.els.rsOffline.textContent, '1',
    'Offline read ' + doc.els.rsOffline.textContent + ' of 3');
});

// ── the search terms ────────────────────────────────────────────────────────
check('the online and offline searches use the verdict', () => {
  assert.equal(page.rtrMatches(BLIP, 'online'), true,
    'a router inside its threshold did not answer the online search');
  assert.equal(page.rtrMatches(BLIP, 'offline'), false);
  assert.equal(page.rtrMatches(SETTLING, 'offline'), true);
  assert.equal(page.rtrMatches(SETTLING, 'online'), false);
});

// ── the cards ───────────────────────────────────────────────────────────────
page.setView('comfortable');
page.renderRoutersStats(ALL);
const grid = doc.els['routers-grid'].innerHTML;
const cards = grid.split('<div class="card h-100">');
const cardFor = (label) => cards.find((c) => c.indexOf(label) !== -1);

check('the card badge names the verdict', () => {
  // ── THE WORD AND THE COLOUR ARE TWO TERNARIES, AND BOTH ARE CHECKED ─────
  //
  // Found by planting the failure: reverting the CLASS to `connected` left the
  // badge reading "Online" on a red pill, and an assertion on the word alone
  // passed. Two expressions, two assertions, or half of this is decorative.
  const blip = cardFor('Blip');
  assert.ok(blip, 'the blipping router did not render');
  assert.ok(blip.indexOf('bg-green-lt">Online<') !== -1,
    'a router whose socket is shut inside its threshold was not badged a green '
    + 'Online; the badge is: ' + (blip.match(/<span class="badge[^<]*<\/span>/) || [''])[0]);
  const settling = cardFor('Settling');
  assert.ok(settling.indexOf('bg-red-lt">Offline<') !== -1,
    'a declared-offline router was not badged a red Offline; the badge is: '
    + (settling.match(/<span class="badge[^<]*<\/span>/) || [''])[0]);
});

check('the card icon is coloured by the verdict', () => {
  // A SECOND TERNARY, in the wifi icon's stroke. It was missed the last time
  // this page grew a state.
  assert.ok(cardFor('Blip').indexOf('#d63939') === -1,
    'the blipping card carries the offline red in its icon stroke');
  assert.ok(cardFor('Settling').indexOf('#2fb344') === -1,
    'the declared-offline card carries the online green in its icon stroke');
});

// ── the list ────────────────────────────────────────────────────────────────
page.setView('list');
page.renderRoutersStats(ALL);
const list = doc.els.routersListBody.innerHTML;
check('the list dims by the verdict, not the socket', () => {
  const rows = list.split('<tr class=');
  const blip = rows.find((r) => r.indexOf('Blip') !== -1);
  const settling = rows.find((r) => r.indexOf('Settling') !== -1);
  assert.ok(blip && settling, 'the list did not render both rows');
  assert.ok(blip.indexOf('rtl-offline') === -1,
    'a router inside its Offline threshold was dimmed as down');
  assert.ok(settling.indexOf('rtl-offline') !== -1,
    'a declared-offline router was not dimmed');
});

// ── the popover dot ─────────────────────────────────────────────────────────
check('a popover dot follows the verdict', () => {
  assert.ok(page.dotColour(BLIP).indexOf('green') !== -1,
    'the map popover reddened a router inside its threshold');
  assert.ok(page.dotColour(SETTLING).indexOf('red') !== -1);
});

say(failed ? '  ' + failed + ' failed' : 'devices-debounced-badge: all checks passed');
if (failed) process.exit(1);
