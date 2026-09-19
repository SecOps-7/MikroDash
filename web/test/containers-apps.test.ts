/**
 * THE CONTAINERS PAGE'S APPS TAB (2026-09-19).
 *
 * - The Apps tab follows the Containers tables; choosing it hides the table
 *   body, shows the panel and asks for the store.
 * - Cards by state: Install for an available app, Open (http only) and Stop,
 *   Restart, Remove for a running one, Start for a stopped one; every
 *   router-derived value escaped, a `javascript:` address never linked.
 * - Search, category chips and All/Installed/Running filter the cards.
 * - Install sends exactly one `apps:do`; progress shows on the card, and the
 *   end of it asks for the store again.
 * - Remove opens a dialog that stays disarmed until the name is typed exactly,
 *   and sends the typed name with the request.
 * - A viewer who may not manage sees Open and no buttons; the setup card sends
 *   the disk the router offered; a store from the router just left is dropped.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc, RES_MODAL_IDS } from './dom-shim.js';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.apps-entry.ts');
fs.writeFileSync(ENTRY, [
  "export { initAreaPages } from '../web/src/pages/area.js';",
  "export { initContainersApps } from '../web/src/pages/containers-apps.js';",
  "export * as cards from '../web/src/pages/containers-apps-cards.js';",
  "export { AREAS } from '../web/src/gen/areas.js';",
].join('\n') + '\n');
const OUT = path.join(ROOT, 'testdata', '.apps.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const mod = require(OUT);

const KEY = 'containers';
const area = mod.AREAS.find((a) => a.key === KEY);
const ids = [...['areaBody-', 'areaBadge-', 'areaTabs-', 'areaAdd-', 'areaGroupTable-', 'areaGroupNote-'].map((p) => p + KEY),
  'areaPanel-containers-apps', 'appsHero', 'appsQ', 'appsSeg', 'appsCats', 'appsGrid', 'appsDrawer', 'appsModal',
  'appsConfirm', 'appsRemove', 'appsDisk', 'appsBridge'];
const doc = makeDoc(ids, { allowUnknown: [...RES_MODAL_IDS], query: { '[data-res-add]': [], '[data-res-rows]': [] } });
global.document = doc;
global.window = { addEventListener: () => {}, setTimeout, clearTimeout };
(globalThis as any).CustomEvent = function (type, init) { return { type, detail: init && init.detail }; };
const handlers = {};
const sent = [];
const socket = { on: (ev, fn) => { (handlers[ev] = handlers[ev] || []).push(fn); }, emit: (ev, d) => sent.push([ev, d]) };
mod.initContainersApps(socket);
mod.initAreaPages(socket, (p) => p === KEY);
const fire = (ev, d) => (handlers[ev] || []).forEach((fn) => fn(d));
const n = doc.nodes;
const host = n['areaPanel-containers-apps'];

/** Type into one of the panel's fields, as the browser would: the node's value, then its input event. */
const type = (node, value) => { node.value = value; host.fire('input', { target: node }); };

/** A click target whose closest() answers the given selectors with these attributes. */
const target = (map) => ({
  id: '',
  closest: (sel) => (map[sel] ? { getAttribute: (a) => (a in map[sel] ? map[sel][a] : null), textContent: '' } : null),
});
const click = (map) => host.fire('click', { target: target(map) });
const grid = () => String(n.appsGrid.innerHTML);

fire('router:switched', { activeId: 'r1' });
fire('area:update', { ts: 1, pollMs: 60000, area: KEY, title: area.title, denied: false,
  tables: area.tables.map((t) => ({ resource: t.resource, title: t.title, columns: t.columns, rows: [],
    unsupported: false, singleton: false })) });
doc.dispatchEvent({ type: 'mikrodash:pagechange', detail: KEY });

// THE APPS TAB follows the tables, and choosing it shows the panel.
const tabs = String(n['areaTabs-' + KEY].innerHTML);
const at = area.tables.length;
assert.ok(new RegExp('data-areatabindex="' + at + '">Apps</button>').test(tabs), 'no Apps tab after the tables: ' + tabs);
assert.ok(!sent.some(([ev]) => ev === 'apps:list'), 'the store was asked for before its tab was chosen');
doc.dispatchEvent({ type: 'click', target: target({ '[data-areatab]': { 'data-areatab': KEY, 'data-areatabindex': String(at) } }) });
assert.strictEqual(n['areaBody-' + KEY].style.display, 'none', 'the tables are still shown under the Apps tab');
assert.strictEqual(host.style.display, '', 'the Apps panel is not shown');
assert.deepStrictEqual(sent.filter(([ev]) => ev === 'apps:list'), [['apps:list', {}]], 'choosing the tab did not ask for the store once');

const app = (name, state, extra = {}) => ({ id: '*' + name, name, category: 'networking', description: 'd ' + name,
  projectPage: '', defaultCredentials: '', defaultNetwork: 'internal', ports: '', state, status: '', uiUrl: '',
  appSize: '', dataSize: '', memory: '', cpu: '', custom: false, ...extra });
const store = (extra = {}) => ({ routerId: 'r1', supported: true, reason: '', ready: true, disk: 'pcie1', lanBridge: '',
  routerIp: '', disks: [{ slot: 'pcie1', fs: 'ext4', free: '', size: '' }], bridges: ['internal'], httpsLinks: false,
  mayManage: true, code: '', message: '',
  apps: [
    app('uptime-kuma', 'available', { category: 'monitoring', defaultCredentials: 'admin:admin' }),
    app('librespeed', 'running', { uiUrl: 'http://198.51.100.15:3004', ports: '3004:80:tcp:web', defaultCredentials: 'none' }),
    app('pihole', 'stopped', { defaultCredentials: 'admin:<pw>' }),
    app('<img src=x onerror=alert(1)>', 'running', { uiUrl: 'javascript:alert(1)', description: '<b>bold</b>' }),
  ], ...extra });

// CARDS BY STATE.
fire('apps:state', store());
assert.ok(/data-app-do="install" data-app="uptime-kuma"/.test(grid()), 'an available app has no Install');
assert.ok(/href="http:\/\/198\.51\.100\.15:3004"[^>]*>Open/.test(grid()), 'a running app has no Open link');
assert.ok(/data-app-do="stop" data-app="librespeed"/.test(grid()) && /data-app-do="remove" data-app="librespeed"/.test(grid()),
  'a running app cannot be stopped or removed');
assert.ok(/data-app-do="start" data-app="pihole"/.test(grid()), 'a stopped app has no Start');
assert.ok(grid().includes('admin:&lt;pw&gt;'), "a stopped app's default login is not shown escaped");
assert.ok(!grid().includes('<img') && grid().includes('&lt;img src=x'), 'an app name is not escaped');
assert.ok(!grid().includes('<b>bold'), 'a description is not escaped');
assert.ok(!/javascript:/.test(grid()), 'a javascript: address was linked');
assert.ok(grid().indexOf('librespeed') < grid().indexOf('pihole') && grid().indexOf('pihole') < grid().indexOf('uptime-kuma'),
  'running apps are not first and the store last');
assert.ok(/<b>2<\/b><span>running/.test(String(n.appsHero.innerHTML)), 'the hero does not count the running apps');
assert.ok(/plain HTTP/.test(String(n.appsHero.innerHTML)), 'without IP Cloud the hero does not say links are plain HTTP');

// FILTERS: the search, a category, and running only.
type(n.appsQ, 'SPEED');
assert.ok(grid().includes('librespeed') && !grid().includes('pihole'), 'the search does not filter, case-blind');
type(n.appsQ, '');
click({ '[data-cat]': { 'data-cat': 'monitoring' } });
assert.ok(grid().includes('uptime-kuma') && !grid().includes('librespeed'), 'a category chip does not filter');
assert.ok(/apps-chip active" data-cat="monitoring"/.test(String(n.appsCats.innerHTML)), 'the chosen chip is not lit');
click({ '[data-cat]': { 'data-cat': '' } });
click({ '[data-show]': { 'data-show': 'running' } });
assert.ok(grid().includes('librespeed') && !grid().includes('pihole') && !grid().includes('uptime-kuma'), 'Running shows others');
assert.ok(/class="active">Running/.test(String(n.appsSeg.innerHTML)), 'Running is not lit');
click({ '[data-show]': { 'data-show': 'all' } });

// INSTALL: one click, one request, and the card follows the progress.
const before = sent.length;
click({ '[data-app-do]': { 'data-app-do': 'install', 'data-app': 'uptime-kuma' } });
assert.deepStrictEqual(sent.slice(before), [['apps:do', { name: 'uptime-kuma', verb: 'install', confirm: '' }]],
  'Install did not send exactly one request');
assert.ok(/is-busy[^]*Starting install/.test(grid()), 'the card does not show the install starting');
fire('apps:progress', { routerId: 'r1', name: 'uptime-kuma', verb: 'install', status: 'downloading/extracting',
  running: false, uiUrl: '', done: false, code: '', message: '' });
assert.ok(grid().includes('downloading/extracting'), "the card does not show the router's status");
const lists = sent.filter(([ev]) => ev === 'apps:list').length;
fire('apps:progress', { routerId: 'r1', name: 'uptime-kuma', verb: 'install', status: '', running: true,
  uiUrl: '', done: true, code: '', message: '' });
assert.ok(!grid().includes('is-busy'), 'the card is still busy after the install ended');
assert.strictEqual(sent.filter(([ev]) => ev === 'apps:list').length, lists + 1, 'the end of an install did not ask for the store');
fire('apps:progress', { routerId: 'r1', name: 'pihole', verb: 'start', status: '', running: false,
  uiUrl: '', done: true, code: 'failed', message: 'error: not enough space' });
assert.ok(grid().includes('apps-card-error') && grid().includes('not enough space'), 'a failure is not shown on its card');

// REMOVE: disarmed until the name is typed exactly.
const beforeDialog = sent.length;
click({ '[data-app-do]': { 'data-app-do': 'remove', 'data-app': 'librespeed' } });
assert.strictEqual(n.appsModal.hidden, false, 'Remove did not open its dialog');
assert.strictEqual(sent.length, beforeDialog, 'Remove sent something before it was confirmed');
// The shim does not reflect a `disabled` written in markup onto the node, so
// the dialog's first state is read from its markup.
assert.ok(/id="appsRemove" data-apps-remove disabled>/.test(String(n.appsModal.innerHTML)),
  'the dialog is armed before anything is typed');
type(n.appsConfirm, 'Librespeed');
assert.strictEqual(n.appsRemove.disabled, true, 'the dialog armed on a near miss');
n.appsConfirm.value = 'libre';
click({ '[data-apps-remove]': {} });
assert.strictEqual(sent.length, beforeDialog, 'a remove was sent with the wrong name typed');
type(n.appsConfirm, 'librespeed');
assert.strictEqual(n.appsRemove.disabled, false, 'the dialog did not arm on the exact name');
n.appsConfirm.value = 'librespeed';
click({ '[data-apps-remove]': {} });
assert.deepStrictEqual(sent.slice(beforeDialog), [['apps:do', { name: 'librespeed', verb: 'remove', confirm: 'librespeed' }]],
  'the confirmed remove did not send the typed name');
assert.strictEqual(n.appsModal.hidden, true, 'the dialog stayed open after the remove');
assert.ok(/is-busy[^]*Removing/.test(grid()), 'the card does not show the remove running');
fire('apps:progress', { routerId: 'r1', name: 'librespeed', verb: 'remove', status: '', running: false,
  uiUrl: '', done: true, code: '', message: '' });

// A VIEWER WHO MAY NOT MANAGE sees Open and no buttons.
fire('apps:state', store({ mayManage: false }));
assert.ok(!/data-app-do=/.test(grid()), 'a viewer who may not manage is offered a change');
assert.ok(/>Open/.test(grid()), 'a viewer lost the Open link');

// A STORE FROM THE ROUTER JUST LEFT is dropped.
fire('apps:state', store({ routerId: 'r2', apps: [app('from-router-b', 'available')] }));
assert.ok(!grid().includes('from-router-b'), 'a store from another router was drawn');

// SETUP: the card offers the router's disks and sends the one chosen.
fire('apps:state', store({ ready: false, disk: '' }));
assert.ok(/<option value="pcie1">/.test(String(n.appsHero.innerHTML)), 'the setup card does not offer the disk');
assert.ok(!/data-app-do="install"/.test(grid()), 'Install is offered before the store is set up');
assert.ok(/<option value="internal">internal/.test(String(n.appsHero.innerHTML)), 'the setup card does not offer the bridge');
// The selects are written inside the hero's markup, where the shim cannot give
// them their default; the choice is made as a user would make it.
n.appsDisk.value = 'pcie1';
n.appsBridge.value = 'internal';
click({ '[data-apps-setup]': {} });
assert.deepStrictEqual(sent.slice(-1), [['apps:setup', { disk: 'pcie1', lanBridge: 'internal' }]], 'setup did not send the choice');

// A ROUTER SWITCH forgets the store and asks the new router.
const listsBefore = sent.filter(([ev]) => ev === 'apps:list').length;
fire('router:switched', { activeId: 'r2' });
assert.strictEqual(sent.filter(([ev]) => ev === 'apps:list').length, listsBefore + 1, 'a router switch did not ask the new router');
assert.ok(/Loading/.test(grid()), "a router switch left the old router's cards");

// THE PURE PIECES.
const c = mod.cards;
assert.strictEqual(c.safeURL('https://example.org/x'), 'https://example.org/x');
for (const bad of ['javascript:alert(1)', 'data:text/html,x', '//evil', 'http://a"onmouseover=x']) {
  assert.strictEqual(c.safeURL(bad), '', 'linked: ' + bad);
}
assert.strictEqual(c.monogram('home-assistant'), 'HA');
assert.strictEqual(c.monogram('librespeed'), 'LI');
assert.deepStrictEqual(c.ports({ ports: '8123:8123:tcp:web, 53:53:udp' }), ['8123 web', '53']);
assert.strictEqual(c.credentials({ defaultCredentials: 'None' }), '');
assert.deepStrictEqual(c.tileHues({ name: 'a', category: 'x' }), c.tileHues({ name: 'a', category: 'x' }), 'a tile is not stable');
console.log('ok  the Apps tab: cards, filters, install, progress, remove, permissions, setup and router switches');
fs.rmSync(OUT, { force: true });
