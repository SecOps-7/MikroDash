/**
 * The Devices page sorts on its own, for every view (issue #163).
 *
 * ── WHAT THIS PINS ──────────────────────────────────────────────────────────
 *
 * The card views drew routers in the order routers.json holds them (the order
 * set with the arrows in Settings -> Devices), so a 200-router fleet came out
 * in the order it was added. The operator chose (2026-11-13): a Sort dropdown
 * beside the site filter, name A to Z by default, one order for the cards, the
 * list and the map; the Settings order stays the main router selector's.
 *
 * Driven against the real page module and a DOM shim: the default order, the
 * dropdown, a List header (which must keep the search and the site filter, and
 * shows "Sorted by a column" when the dropdown has no such option), and the
 * choice remembered for this browser.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

function makeEl(id, options) {
  const classes = new Set();
  const listeners = {};
  const node = {
    id, value: '', textContent: '', innerHTML: '', style: {}, hidden: false, options: options || [],
    setAttribute: (k, v) => { node[k] = v; },
    getAttribute: (k) => (k in node ? node[k] : null),
    classList: {
      add: (c) => classes.add(c),
      remove: (c) => classes.delete(c),
      contains: (c) => classes.has(c),
      toggle: (c, on) => (on ? classes.add(c) : classes.delete(c)),
    },
    addEventListener: (ev, fn) => { (listeners[ev] = listeners[ev] || []).push(fn); },
    fire: (ev) => (listeners[ev] || []).forEach((fn) => fn({ target: node })),
    appendChild: () => {},
    querySelectorAll: () => [], querySelector: () => null,
  };
  return node;
}

// The dropdown's options, read from the real markup so the test cannot offer a
// sort the page does not.
const html = fs.readFileSync(path.join(ROOT, 'web', 'src', 'ui', 'page-devices.html'), 'utf8');
const sortMarkup = (html.match(/<select id="routersSort"[\s\S]*?<\/select>/) || [''])[0];
const sortOptions = [...sortMarkup.matchAll(/<option value="([^"]*)"/g)].map((m) => ({ value: m[1] }));
assert.ok(sortOptions.length >= 5, 'the Sort dropdown is missing from page-devices.html');
assert.strictEqual(sortOptions[0].value, 'label:1', 'Name A to Z is not the first option');
const siteAt = html.indexOf('id="routersSiteFilter"');
const sortAt = html.indexOf('id="routersSort"');
const searchAt = html.indexOf('id="routersSearch"');
assert.ok(siteAt > 0 && siteAt < sortAt && sortAt < searchAt, 'the Sort dropdown is not right of the site filter');

const ids = [
  'rsTotal', 'rsOnline', 'rsOffline', 'rsAlerting', 'rsSites',
  'routersSearch', 'routersShown', 'routersSiteFilter', 'routersView',
  'routers-grid', 'routersListWrap', 'routersListBody', 'routersMapWrap', 'rtrMapTray',
];
const els = {};
ids.forEach((id) => { els[id] = makeEl(id); });
els.routersSort = makeEl('routersSort', sortOptions);
global.document = {
  getElementById: (id) => els[id] || null,
  querySelectorAll: () => [], querySelector: () => null,
  addEventListener: () => {}, body: makeEl('body'), hidden: true,
};
global.window = { addEventListener: () => {}, location: { pathname: '/devices' } };
const store = {};
global.localStorage = {
  getItem: (k) => (k in store ? store[k] : null),
  setItem: (k, v) => { store[k] = String(v); },
};
// The overview poll would keep the process alive; this test never needs it.
const realInterval = global.setInterval;
global.setInterval = () => 0;

const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-devices-sort.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'routers.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
const page = require(OUT);

function row(id, label, over) {
  return Object.assign({
    id, label, host: '198.51.100.' + id.length, isActive: false,
    connected: true, online: true, known: true, lastError: null, openAlerts: 0,
    cpu: null, uptime: null, memPct: null, hddPct: null,
    version: null, boardName: null, arch: null, serial: null, licenseLevel: null,
    clients: null, updateAvailable: null, latestVersion: null,
    siteIds: [], siteNames: [], siteId: null, siteName: null, geo: null,
  }, over || {});
}

// IN THE ORDER THEY WERE ADDED, which is what the cards used to show.
const ROWS = [
  row('r1', 'Router 10', { openAlerts: 1, version: '7.20.1', boardName: 'RB5009' }),
  row('r2', 'router 2', { online: false, version: '7.25.1', boardName: 'hAP ax3' }),
  row('r3', 'Alpha', { openAlerts: 3, version: '7.24.5', boardName: 'CCR2004', siteIds: ['s1'], siteNames: ['HQ'] }),
  row('r4', 'Beta', { online: false, version: null, boardName: 'cAP ax' }),
];
const order = (htmlText) => ROWS.map((r) => [r.label, htmlText.indexOf('>' + r.label + '<') >= 0 ? htmlText.indexOf('>' + r.label + '<') : htmlText.indexOf(r.label)])
  .filter((x) => x[1] >= 0).sort((a, b) => a[1] - b[1]).map((x) => x[0]);
const grid = () => order(String(els['routers-grid'].innerHTML));
const list = () => order(String(els.routersListBody.innerHTML));

say('devices: the page sorts on its own, name A to Z by default');

// Mount with nothing stored: the default, before any payload arrives.
page.mountRouters({ on: () => {} });
assert.deepStrictEqual(page.getSort(), { key: 'label', dir: 1 }, 'the default is not name A to Z');
assert.strictEqual(els.routersSort.value, 'label:1', 'the dropdown does not show the default');

page.applyView('comfortable');
page.renderRoutersStats(ROWS);
assert.deepStrictEqual(grid(), ['Alpha', 'Beta', 'router 2', 'Router 10'],
  'the cards are not name A to Z (natural, any case): ' + grid());
page.applyView('compact');
assert.deepStrictEqual(grid(), ['Alpha', 'Beta', 'router 2', 'Router 10'], 'compact cards are in another order');
say('  ok   the cards are name A to Z, naturally, not in the order added');

// The dropdown.
const choose = (v) => { els.routersSort.value = v; els.routersSort.fire('change'); };
choose('label:-1');
assert.deepStrictEqual(grid(), ['Router 10', 'router 2', 'Beta', 'Alpha'], 'Name Z to A: ' + grid());
assert.strictEqual(store.mikrodash_routers_sort, 'label:-1', 'the choice was not remembered');
choose('online:1');
assert.deepStrictEqual(grid(), ['Beta', 'router 2', 'Alpha', 'Router 10'], 'Offline first, then by name: ' + grid());
choose('openAlerts:-1');
assert.deepStrictEqual(grid(), ['Alpha', 'Router 10', 'Beta', 'router 2'], 'Most alerts, then by name: ' + grid());
choose('version:-1');
assert.deepStrictEqual(grid(), ['router 2', 'Alpha', 'Router 10', 'Beta'], 'RouterOS newest first, none last: ' + grid());
say('  ok   each option orders the cards, ties by name, and is remembered');

// The list follows the same order, and a header keeps the search and the site.
page.applyView('list');
assert.deepStrictEqual(list(), ['router 2', 'Alpha', 'Router 10', 'Beta'], 'the list is in another order: ' + list());
els.routersSearch.value = 'a';
page.renderRoutersStats(null);
const filtered = list();
page.sortBy('host');
assert.deepStrictEqual(list().slice().sort(), filtered.slice().sort(), 'a header click dropped the search: ' + list());
assert.strictEqual(els.routersSort.value, '', 'a column the dropdown does not offer still shows an option');
page.sortBy('label');
assert.strictEqual(els.routersSort.value, 'label:1', 'a header that matches an option does not show it');
els.routersSearch.value = '';
els.routersSiteFilter.value = 's1';
page.renderRoutersStats(null);
page.sortBy('label');
assert.deepStrictEqual(list(), ['Alpha'], 'a header click dropped the site filter: ' + list());
els.routersSiteFilter.value = '';
say('  ok   the list shares the order; a header keeps the search and the site');

// A reload comes back in the stored order; an unknown stored value is ignored.
store.mikrodash_routers_sort = 'openAlerts:-1';
page.setSort('label', 1);
page.mountRouters({ on: () => {} });
assert.deepStrictEqual(page.getSort(), { key: 'openAlerts', dir: -1 }, 'the stored order was not restored');
assert.strictEqual(els.routersSort.value, 'openAlerts:-1');
store.mikrodash_routers_sort = 'password:1';
page.setSort('label', 1);
page.mountRouters({ on: () => {} });
assert.deepStrictEqual(page.getSort(), { key: 'label', dir: 1 }, 'a stored value naming no column was applied');
say('  ok   the choice survives a reload; a bad stored value falls back');

global.setInterval = realInterval;
fs.rmSync(OUT, { force: true });
say('devices-sort: all checks passed');
