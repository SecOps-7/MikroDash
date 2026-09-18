/**
 * A GROUPED GENERATED TABLE: ONE ROW PER GROUP, A GROUP'S ROWS WHEN OPENED
 * (2026-09-18).
 *
 * Address Lists groups by `list`, because a synced blocklist put 37,111 entries
 * on the operator's router and the page sent every one to the browser each
 * minute. The page now draws the summary the collector sends, asks for one list
 * with `area:group` when it is clicked, shows at most what the server capped with
 * the true count, searches on the server, and asks again after a write or a
 * summary change. The control is IP Pools, which is not grouped and still draws
 * its rows directly.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.area-group-entry.ts');
fs.writeFileSync(ENTRY,
  "export { initAreaPages } from '../web/src/pages/area.js';\n" +
  "export { AREAS } from '../web/src/gen/areas.js';\n");
const OUT = path.join(ROOT, 'testdata', '.area-group.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const mod = require(OUT);
const KEY = 'address-lists';
const ids = mod.AREAS.flatMap((a) => ['areaBody-', 'areaBadge-', 'areaTabs-', 'areaAdd-', 'areaGroupTable-', 'areaGroupNote-']
  .map((p) => p + a.key));
const doc = makeDoc(ids, { query: { '[data-res-add]': [], '[data-res-rows]': [] } });
const handlers = {};
const sent = [];
global.document = doc;
global.window = { addEventListener: () => {}, setTimeout, clearTimeout };
mod.initAreaPages({
  on: (ev, fn) => { (handlers[ev] = handlers[ev] || []).push(fn); },
  emit: (ev, data) => sent.push({ ev, data }),
}, (p) => p === KEY || p === 'ip-pools');
const fire = (ev, d) => (handlers[ev] || []).forEach((fn) => fn(d));

const area = mod.AREAS.find((a) => a.key === KEY);
const body = () => String(doc.nodes['areaBody-' + KEY].innerHTML);
const summary = (groups) => fire('area:update', { ts: 1, pollMs: 60000, area: KEY, title: area.title, denied: false,
  tables: [{ resource: 'addressList', title: 'Address List', columns: area.tables[0].columns, rows: [],
    unsupported: false, singleton: false, groupBy: 'list', groups }] });
const click = (attrs) => doc.dispatch('click', { closest: (sel) => {
  const name = sel.slice(1, -1);
  return name in attrs ? { getAttribute: (a) => (a in attrs ? attrs[a] : null) } : null;
} });
const groupReqs = () => sent.filter((s) => s.ev === 'area:group');

// ── THE SUMMARY ─────────────────────────────────────────────────────────────
summary([{ name: 'admins', count: 2, dynamic: 0, disabled: 1 }, { name: 'blocklist', count: 37111, dynamic: 37000, disabled: 0 }]);
let html = body();
assert.ok(/data-areagroup="blocklist"/.test(html) && /data-areagroup="admins"/.test(html), 'the summary has no row per list: ' + html);
assert.ok(html.includes('37,111'), 'the summary does not show the entry count');
assert.ok(!/data-res="addressList"/.test(html), 'the summary drew entry rows; it has none to draw');
assert.strictEqual(doc.nodes['areaBadge-' + KEY].textContent, '37113', 'the pill does not count every entry');
assert.strictEqual(groupReqs().length, 0, 'the summary asked for a group nobody opened');
say('ok  the summary is one row per list, with its counts, and no entries');

// ── OPENING A LIST ASKS FOR THAT LIST ONLY ──────────────────────────────────
click({ 'data-areagroup': 'blocklist', 'data-areagroupof': KEY });
assert.deepStrictEqual(groupReqs().at(-1).data, { area: KEY, resource: 'addressList', group: 'blocklist', search: '', refresh: true });
assert.ok(/Loading/.test(String(doc.nodes['areaGroupTable-' + KEY].innerHTML)), 'no loading state while the list is read');
assert.ok(/data-areagroupback/.test(body()) && /data-areagroupsearch/.test(body()), 'no back link or search box');
const entry = (id, addr) => ({ id, identity: addr, values: { list: 'blocklist', address: addr, dynamic: 'true' } });
const reply = (over) => fire('area:grouprows', { area: KEY, resource: 'addressList', group: 'blocklist', search: '',
  total: 37111, rows: [entry('*1', '198.51.100.7'), entry('*2', '198.51.100.8')], columns: area.tables[0].columns, error: '', ...over });

// A STALE ANSWER IS IGNORED: another group's, or another search's.
reply({ group: 'admins' });
reply({ search: 'old' });
assert.ok(/Loading/.test(String(doc.nodes['areaGroupTable-' + KEY].innerHTML)), 'a stale answer was drawn');

reply({});
const rows = String(doc.nodes['areaGroupTable-' + KEY].innerHTML);
assert.ok(/data-id="\*1"[^>]*data-res="addressList"|data-res="addressList"[^>]*data-id="\*1"/.test(rows.replace(/\n/g, '')) || (rows.includes('data-id="*1"') && rows.includes('data-res="addressList"')),
  'the entries do not carry the engine attributes: ' + rows);
assert.ok(rows.includes('hs-info">yes'), 'the entries lost their pills');
assert.strictEqual(doc.nodes['areaGroupNote-' + KEY].textContent, 'Showing 2 of 37,111. Search to narrow.');
say('ok  opening a list asks for it alone, ignores stale answers, and says 2 of 37,111');

// ── THE SEARCH GOES TO THE SERVER, AFTER TYPING STOPS ───────────────────────
const before = groupReqs().length;
const box = { value: '198.51', getAttribute: (a) => (a === 'data-areagroupsearch' ? KEY : null) };
doc.dispatch('input', box);
box.value = '198.51.100.7';
doc.dispatch('input', box);
assert.strictEqual(groupReqs().length, before, 'a request went out on every keystroke');
setTimeout(() => {
  assert.strictEqual(groupReqs().length, before + 1, 'the search was never sent');
  assert.strictEqual(groupReqs().at(-1).data.search, '198.51.100.7');
  assert.strictEqual(groupReqs().at(-1).data.refresh, false, 'a search asked the router to read the whole list again');
  reply({ search: '198.51.100.7', total: 1, rows: [entry('*1', '198.51.100.7')] });
  assert.strictEqual(doc.nodes['areaGroupNote-' + KEY].textContent, '1 matching entry');
  say('ok  the search is debounced, sent to the server, and its answer drawn');

  // ── A WRITE, OR A SUMMARY CHANGE, READS THE LIST AGAIN ────────────────────
  let n = groupReqs().length;
  fire('res:ok', { resource: 'addressList', action: 'update', name: '198.51.100.7' });
  assert.strictEqual(groupReqs().length, n + 1, 'an edit did not refresh the open list');
  assert.strictEqual(groupReqs().at(-1).data.refresh, true, 'a refresh after a write reused the old rows');
  fire('res:ok', { resource: 'ipPool', action: 'update', name: 'x' });
  assert.strictEqual(groupReqs().length, n + 1, 'another resource\'s write refreshed this list');
  n = groupReqs().length;
  summary([{ name: 'blocklist', count: 37112, dynamic: 37001, disabled: 0 }]);
  assert.strictEqual(groupReqs().length, n + 1, 'a summary change did not refresh the open list');
  say('ok  a write to this resource, or a summary change, reads the open list again');

  // ── BACK TO THE SUMMARY ─────────────────────────────────────────────────
  click({ 'data-areagroupback': KEY });
  assert.ok(/data-areagroup="blocklist"/.test(body()) && !/data-areagroupback/.test(body()), 'back did not return to the summary');
  n = groupReqs().length;
  fire('res:ok', { resource: 'addressList', action: 'update', name: 'x' });
  assert.strictEqual(groupReqs().length, n, 'with no list open, a write still asked for one');
  say('ok  back returns to the summary, which asks for nothing');

  // ── THE CONTROL: AN UNGROUPED AREA STILL DRAWS ITS ROWS ───────────────────
  const pools = mod.AREAS.find((a) => a.key === 'ip-pools');
  fire('area:update', { ts: 1, pollMs: 60000, area: 'ip-pools', title: pools.title, denied: false,
    tables: [{ resource: 'ipPool', title: 'IP Pool', columns: pools.tables[0].columns, unsupported: false, singleton: false,
      groupBy: '', groups: [], rows: [{ id: '*9', identity: 'lan', values: { name: 'lan' } }] }] });
  assert.ok(/data-id="\*9"/.test(String(doc.nodes['areaBody-ip-pools'].innerHTML)), 'an ungrouped area lost its rows');
  say('ok  an ungrouped area still draws its rows (control)');
  fs.rmSync(OUT, { force: true });
}, 400);
