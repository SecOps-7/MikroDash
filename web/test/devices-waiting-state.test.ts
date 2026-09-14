/**
 * "No routers configured" is a claim the SERVER makes, not the page's default.
 *
 * ── THE DEFECT THIS PINS ────────────────────────────────────────────────────
 *
 * The Devices page renders once at mount, from an empty starting list, before
 * any `routers:stats` has arrived. It said "No routers configured." there, so a
 * WebSocket that never connected looked exactly like an install with no devices,
 * and issue #129 was reported as the second when it was the first.
 *
 * Driven against the real page module and a DOM shim, in both views that write
 * the message: the card grid and the list.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

function makeEl(id) {
  const classes = new Set();
  const node = {
    id, value: '', textContent: '', innerHTML: '', style: {}, hidden: false,
    setAttribute: (k, v) => { node[k] = v; },
    getAttribute: (k) => (k in node ? node[k] : null),
    classList: {
      add: (c) => classes.add(c),
      remove: (c) => classes.delete(c),
      contains: (c) => classes.has(c),
      toggle: (c, on) => (on ? classes.add(c) : classes.delete(c)),
    },
    addEventListener: () => {}, appendChild: () => {},
    querySelectorAll: () => [], querySelector: () => null,
  };
  return node;
}

const ids = [
  'rsTotal', 'rsOnline', 'rsOffline', 'rsAlerting', 'rsSites',
  'routersSearch', 'routersShown', 'routersSiteFilter', 'routersView',
  'routers-grid', 'routersListWrap', 'routersListBody', 'routersMapWrap', 'rtrMapTray',
];
const els = {};
ids.forEach((id) => { els[id] = makeEl(id); });
global.document = {
  getElementById: (id) => els[id] || null,
  querySelectorAll: () => [], querySelector: () => null,
  addEventListener: () => {}, body: makeEl('body'),
};
global.window = { addEventListener: () => {}, location: { pathname: '/devices' } };

const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-devices-waiting.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'routers.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
// A FRESH bundle, so the module has never been told anything.
const page = require(OUT);

const WAITING = 'Waiting for the router list';
const EMPTY = 'No routers configured.';
const NO_MATCH = 'No routers match that search.';

say('devices: "No routers configured" waits for the server');

// The mount render: the page draws from its starting list.
page.applyView('comfortable');
assert.ok(els['routers-grid'].innerHTML.includes(WAITING),
  'the grid before any payload says: ' + els['routers-grid'].innerHTML);
assert.ok(!els['routers-grid'].innerHTML.includes(EMPTY),
  'the grid claimed an empty install before the server said so');
page.applyView('list');
assert.ok(els.routersListBody.innerHTML.includes(WAITING),
  'the list before any payload says: ' + els.routersListBody.innerHTML);
say('  ok   both views wait before the first payload');

// A repaint from what is held (`refreshRouters` passes null) is not a payload.
page.renderRoutersStats(null);
assert.ok(els.routersListBody.innerHTML.includes(WAITING),
  'a null repaint counted as the server answering: ' + els.routersListBody.innerHTML);
say('  ok   a repaint from held rows is not an answer');

// A search that matches nothing keeps its own message, answered or not.
els.routersSearch.value = 'zzz';
page.renderRoutersStats(null);
assert.ok(els.routersListBody.innerHTML.includes(NO_MATCH),
  'a search before any payload said: ' + els.routersListBody.innerHTML);
els.routersSearch.value = '';
say('  ok   an empty search result keeps its own message');

// The server answers with an empty fleet: now the claim is true.
page.renderRoutersStats([]);
assert.ok(els.routersListBody.innerHTML.includes(EMPTY),
  'an empty payload in the list said: ' + els.routersListBody.innerHTML);
page.applyView('comfortable');
assert.ok(els['routers-grid'].innerHTML.includes(EMPTY),
  'an empty payload in the grid said: ' + els['routers-grid'].innerHTML);
say('  ok   an empty payload says no routers are configured');

// A REPAINT KEEPS THE ROWS. `refreshRouters` passes null after every
// `routers:update`; it drew `[]` and blanked the page until the next payload.
page.renderRoutersStats([{
  id: 'r1', label: 'Edge', host: '198.51.100.1', isActive: false,
  connected: true, known: true, lastError: null, openAlerts: 0,
  cpu: null, uptime: null, memPct: null, hddPct: null,
  version: null, boardName: null, arch: null, serial: null, licenseLevel: null,
  rxMbps: null, txMbps: null, clients: null,
  siteIds: [], siteNames: [], siteId: null, siteName: null, geo: null,
}]);
page.renderRoutersStats(null);
assert.ok(els['routers-grid'].innerHTML.includes('Edge'),
  'a null repaint after a payload drew: ' + els['routers-grid'].innerHTML);
say('  ok   a repaint keeps the rows it holds');
