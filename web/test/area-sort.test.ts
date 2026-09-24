/**
 * A GENERATED PAGE'S TABLES SORT BY THEIR HEADERS (2026-09-18).
 *
 * Every generated table sorts through the shared `renderSortHeader` and
 * `sortRows`, as the hand-built pages do: a click sorts ascending, a second
 * descending, a third returns to the router's order. The state is per area and
 * per tab, and outlives the periodic `area:update` redraw. Each row keeps its
 * engine attributes, so a click on a sorted row still opens that row's form.
 *
 * An ORDERED resource does not sort at all: the first row that matches decides,
 * so the router's order is what the table means. Its headers carry no sort, a
 * click on one changes nothing, and its arrows stay, with the right ends
 * disabled - the Queues page's rule for its first-match tables.
 *
 * The count pill beside the title is blue (`active-blue`) when it counts
 * something, as every hand-built page's is, and plain at zero.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc, RES_MODAL_IDS } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.area-sort-entry.ts');
fs.writeFileSync(ENTRY,
  "export { initAreaPages } from '../web/src/pages/area.js';\n" +
  "export { AREAS } from '../web/src/gen/areas.js';\n");
const OUT = path.join(ROOT, 'testdata', '.area-sort.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const mod = require(OUT);
const ids = mod.AREAS.flatMap((a) => ['areaBody-', 'areaBadge-', 'areaTabs-', 'areaAdd-'].map((p) => p + a.key));
const doc = makeDoc(ids, { allowUnknown: [...RES_MODAL_IDS], query: { '[data-res-add]': [], '[data-res-rows]': [] } });
const handlers = {};
global.document = doc;
global.window = { addEventListener: () => {}, setTimeout, clearTimeout };
mod.initAreaPages({ on: (ev, fn) => { handlers[ev] = fn; }, emit: () => {} }, (p) => p === 'ip-pools' || p === 'ospf');

const pools = mod.AREAS.find((a) => a.key === 'ip-pools');
const ospf = mod.AREAS.find((a) => a.key === 'ospf');
const TPL = ospf.tables.findIndex((t) => t.ordered);
assert.ok(TPL > 0, 'the OSPF area has no ordered table behind a tab');

const sendTables = (area, tables) => handlers['area:update']({ ts: 1, pollMs: 60000, area: area.key, title: area.title,
  denied: false, tables: area.tables.map((t, i) => ({ resource: t.resource, title: t.title, unsupported: false,
    singleton: false, columns: t.columns, rows: tables[i] || [] })) });
const body = (key) => String(doc.nodes['areaBody-' + key].innerHTML);
/** The data-id of every row, in the order drawn. */
const order = (key) => [...body(key).matchAll(/<tr[^>]*\sdata-id="([^"]*)"/g)].map((m) => m[1]);
/** Click the header cell of a declared column (the arrow column, when present, is not one). */
const clickHeader = (key, col, arrows) => {
  const area = mod.AREAS.find((a) => a.key === key);
  const at = area.key === 'ospf' ? currentTab : 0;
  const i = area.tables[at].columns.indexOf(col) + (arrows ? 1 : 0);
  const cells = doc.nodes['areaThead-' + key].querySelectorAll('th');
  assert.ok(cells[i], 'no header cell for ' + col);
  cells[i].click();
};
let currentTab = 0;

// ── THE COUNT PILL ──────────────────────────────────────────────────────────
sendTables(pools, [[]]);
assert.strictEqual(doc.nodes['areaBadge-ip-pools'].className, 'card-badge', 'an empty table\'s pill is not plain');
const pool = (id, name, used) => ({ id, identity: 'pool-' + name, values: { name, used: String(used) } });
const poolRows = [pool('*1', 'lan', 9), pool('*2', 'guest', 10), pool('*3', 'dmz', 2)];
sendTables(pools, [poolRows]);
assert.strictEqual(doc.nodes['areaBadge-ip-pools'].className, 'card-badge active-blue', 'a counted pill is not blue');
assert.strictEqual(doc.nodes['areaBadge-ip-pools'].textContent, '3');
say('ok  the count pill is blue when it counts something, plain at zero');

// ── ASCENDING, THEN DESCENDING, THEN THE ROUTER'S ORDER ─────────────────────
assert.deepStrictEqual(order('ip-pools'), ['*1', '*2', '*3'], 'an unsorted table is not in the router\'s order');
clickHeader('ip-pools', 'name');
assert.deepStrictEqual(order('ip-pools'), ['*3', '*2', '*1'], 'first click is not ascending by name');
assert.ok(/class="sort-asc"/.test(doc.nodes['areaThead-ip-pools'].innerHTML), 'the header does not show ascending');
clickHeader('ip-pools', 'name');
assert.deepStrictEqual(order('ip-pools'), ['*1', '*2', '*3'], 'second click is not descending by name');
assert.ok(/class="sort-desc"/.test(doc.nodes['areaThead-ip-pools'].innerHTML), 'the header does not show descending');
say('ok  a header click sorts ascending, a second descending');

// A NUMBER SORTS AS A NUMBER: 10 follows 9.
clickHeader('ip-pools', 'used');
assert.deepStrictEqual(order('ip-pools'), ['*3', '*1', '*2'], 'a numeric column sorted as text');
say('ok  a numeric column sorts numerically');

// ── THE SORT OUTLIVES THE POLL ──────────────────────────────────────────────
sendTables(pools, [[pool('*2', 'guest', 10), pool('*4', 'vpn', 1), pool('*1', 'lan', 9), pool('*3', 'dmz', 2)]]);
assert.deepStrictEqual(order('ip-pools'), ['*4', '*3', '*1', '*2'], 'an area:update threw the sort away');
say('ok  the sort survives an area:update');

// ── EVERY ROW KEEPS ITS ENGINE ATTRIBUTES ───────────────────────────────────
for (const m of body('ip-pools').matchAll(/<tr[^>]*\sdata-id="([^"]*)"[^>]*>/g)) {
  const tr = m[0];
  const name = { '*1': 'lan', '*2': 'guest', '*3': 'dmz', '*4': 'vpn' }[m[1]];
  assert.ok(tr.includes('data-identity="pool-' + name + '"'), 'row ' + m[1] + ' lost its identity: ' + tr);
  assert.ok(tr.includes('data-res="ipPool"'), 'row ' + m[1] + ' lost its resource: ' + tr);
}
assert.ok(/data-res-rows="ipPool"/.test(body('ip-pools')), 'the sorted table lost data-res-rows');
say('ok  a sorted row keeps data-id, data-identity and data-res');

// ── THE THIRD CLICK IS THE ROUTER'S ORDER ───────────────────────────────────
clickHeader('ip-pools', 'used');   // descending
clickHeader('ip-pools', 'used');   // cleared
assert.deepStrictEqual(order('ip-pools'), ['*2', '*4', '*1', '*3'], 'the third click did not return to the router\'s order');
assert.ok(!/sort-(asc|desc)/.test(doc.nodes['areaThead-ip-pools'].innerHTML), 'a cleared sort still shows a direction');
say('ok  a third click returns to the router\'s order');

// ── AN ORDERED TABLE, BEHIND A TAB: PER-TAB STATE, AND NO ARROWS WHILE SORTED ─
handlers['res:schema']({ key: ospf.tables[TPL].resource, permitted: true });
const tpl = (id, area, cost) => ({ id, identity: id, values: { area, cost: String(cost) } });
const tplRows = [tpl('*A', 'backbone', 30), tpl('*B', 'area1', 10), tpl('*C', 'area2', 20)];
const nbr = (id, rid) => ({ id, identity: id, values: { routerId: rid } });
const tables = [];
tables[0] = [nbr('*n2', '10.0.0.2'), nbr('*n1', '10.0.0.1')];
tables[TPL] = tplRows;
sendTables(ospf, tables);
// Sort the first tab, then move to the templates tab.
clickHeader('ospf', 'routerId');
assert.deepStrictEqual(order('ospf'), ['*n1', '*n2'], 'the neighbour tab did not sort');
currentTab = TPL;
doc.dispatch('click', { closest: () => ({ getAttribute: (k) => (k === 'data-areatab' ? 'ospf' : String(TPL)) }) });
assert.deepStrictEqual(order('ospf'), ['*A', '*B', '*C'], 'the templates tab inherited another tab\'s sort');
const moves = () => (body('ospf').match(/data-res-move=/g) || []).length;
assert.strictEqual(moves(), 6, 'an unsorted ordered table lost its arrows');

// A HEADER CLICK ON AN ORDERED TABLE CHANGES NOTHING. The control is the
// neighbour tab above, whose header click did sort.
const head = String(doc.nodes['areaThead-ospf'].innerHTML);
assert.ok(!/cursor:pointer/.test(head), 'an ordered table\'s header offers a sort: ' + head);
clickHeader('ospf', 'cost', true);
clickHeader('ospf', 'area', true);
assert.deepStrictEqual(order('ospf'), ['*A', '*B', '*C'], 'an ordered table sorted, so it no longer shows the order the router applies');
assert.ok(!/sort-(asc|desc)/.test(String(doc.nodes['areaThead-ospf'].innerHTML)), 'an ordered table\'s header shows a direction');
sendTables(ospf, tables);
assert.deepStrictEqual(order('ospf'), ['*A', '*B', '*C'], 'an area:update drew the ordered table out of the router\'s order');
const html = body('ospf');
const ups = html.match(/<button[^>]*data-res-move="up"[^>]*>/g) || [];
const downs = html.match(/<button[^>]*data-res-move="down"[^>]*>/g) || [];
assert.strictEqual(ups.length, 3, 'the arrows went');
assert.ok(/disabled/.test(ups[0]) && !/disabled/.test(ups[1]) && /disabled/.test(downs[2]) && !/disabled/.test(downs[1]),
  'the arrows disable the wrong ends');
say('ok  an ordered table does not sort, and keeps its arrows in the router\'s order');

// Back on the first tab, its own sort is still there.
currentTab = 0;
doc.dispatch('click', { closest: () => ({ getAttribute: (k) => (k === 'data-areatab' ? 'ospf' : '0') }) });
assert.deepStrictEqual(order('ospf'), ['*n1', '*n2'], 'the neighbour tab forgot its sort');
say('ok  each tab keeps its own sort');

fs.rmSync(OUT, { force: true });
