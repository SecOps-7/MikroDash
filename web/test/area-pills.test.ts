/**
 * A GENERATED PAGE'S KEY COLUMNS ARE COLOURED PILLS (2026-09-18).
 *
 * `internal/areas` names a pill KIND per column — `Table.Pills`, plus the flag
 * columns in `CommonPills` — and `web/src/pages/area.ts` holds the one colour
 * table per kind, in the hand-built pages' own pill styles. This drives the
 * renderer with rows and reads the cells back:
 *
 *   - a status word is coloured by its meaning, an unknown one is neutral, and
 *     RouterOS's trailing "..." does not hide the meaning;
 *   - a flag reads yes or no, coloured only when true;
 *   - an action uses the Firewall page's badge;
 *   - the CONTROL: a column with no kind is plain text, and a value is escaped
 *     inside a pill exactly as it is outside one.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.area-pills-entry.ts');
fs.writeFileSync(ENTRY,
  "export { initAreaPages } from '../web/src/pages/area.js';\n" +
  "export { AREAS } from '../web/src/gen/areas.js';\n");
const OUT = path.join(ROOT, 'testdata', '.area-pills.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const mod = require(OUT);
const ids = mod.AREAS.flatMap((a) => ['areaBody-', 'areaBadge-', 'areaTabs-', 'areaAdd-'].map((p) => p + a.key));
const doc = makeDoc(ids, { query: { '[data-res-add]': [], '[data-res-rows]': [] } });
const handlers = {};
global.document = doc;
global.window = { addEventListener: () => {}, setTimeout, clearTimeout };
mod.initAreaPages({ on: (ev, fn) => { handlers[ev] = fn; }, emit: () => {} }, () => true);

const send = (key, rows) => {
  const area = mod.AREAS.find((a) => a.key === key);
  handlers['area:update']({ ts: 1, pollMs: 60000, area: key, title: area.title, denied: false,
    tables: area.tables.map((t, i) => ({ resource: t.resource, title: t.title, unsupported: false,
      singleton: false, columns: t.columns, rows: i === 0 ? rows : [] })) });
  return area.tables[0];
};
/** The cells of each drawn row, by the table's declared column. */
const cells = (key, table) => [...String(doc.nodes['areaBody-' + key].innerHTML)
  .matchAll(/<tr[^>]*\sdata-id="[^"]*"[^>]*>([\s\S]*?)<\/tr>/g)]
  .map((m) => {
    const tds = [...m[1].matchAll(/<td[^>]*>([\s\S]*?)<\/td>/g)].map((c) => c[1]);
    return Object.fromEntries(table.columns.map((c, i) => [c, tds[i]]));
  });

// ── STATUS WORDS, AND THE CONTROL COLUMN ────────────────────────────────────
const dhcp = send('dhcp-clients', [
  { id: '*1', identity: 'ether1', values: { interface: 'ether1', status: 'bound', disabled: 'false' } },
  { id: '*2', identity: 'ether2', values: { interface: 'ether2', status: 'searching...', disabled: 'true' } },
  { id: '*3', identity: 'ether3', values: { interface: 'ether3', status: 'error' } },
  { id: '*4', identity: 'ether4', values: { interface: '<b>x</b>', status: 'something-new<i>' } },
]);
assert.strictEqual(dhcp.pills.status, 'state', 'the DHCP client status is not declared a state pill');
const d = cells('dhcp-clients', dhcp);
assert.strictEqual(d.length, 4);
assert.strictEqual(d[0].status, '<span class="vpn-hs-badge hs-ok">bound</span>');
assert.strictEqual(d[1].status, '<span class="vpn-hs-badge hs-warn">searching...</span>',
  'a trailing "..." hid the meaning of the word');
assert.strictEqual(d[2].status, '<span class="vpn-hs-badge hs-stale">error</span>');
assert.strictEqual(d[3].status, '<span class="vpn-hs-badge hs-never">something-new&lt;i&gt;</span>',
  'an unknown state is not a neutral, escaped pill');
// THE CONTROL: `interface` has no kind, so it is the plain escaped value, no span.
assert.strictEqual(d[0].interface, 'ether1', 'a column with no kind was drawn as something other than text');
assert.strictEqual(d[3].interface, '&lt;b&gt;x&lt;/b&gt;', 'a plain value is not escaped');
say('ok  a status word is coloured by meaning; a column with no kind stays plain text');

// ── FLAGS: YES OR NO, COLOURED ONLY WHEN TRUE ───────────────────────────────
assert.strictEqual(d[0].disabled, '<span class="vpn-hs-badge hs-never">no</span>');
assert.strictEqual(d[1].disabled, '<span class="vpn-hs-badge hs-warn">yes</span>');
assert.ok(/&mdash;/.test(d[2].disabled), 'a flag the router did not send is not the dash');
const lists = send('address-lists', [
  { id: '*1', identity: 'a', values: { list: 'blocked', address: '198.51.100.7', dynamic: 'true' } },
]);
assert.strictEqual(cells('address-lists', lists)[0].dynamic, '<span class="vpn-hs-badge hs-info">yes</span>');
const vrrp = send('vrrp', [
  { id: '*1', identity: 'v', values: { name: 'vrrp1', running: 'true', invalid: 'true' } },
]);
const v = cells('vrrp', vrrp)[0];
assert.strictEqual(v.running, '<span class="vpn-hs-badge hs-ok">yes</span>');
assert.strictEqual(v.invalid, '<span class="vpn-hs-badge hs-stale">yes</span>');
say('ok  a flag reads yes or no, in its kind\'s colour only when true');

// ── ACTIONS: THE FIREWALL PAGE'S BADGE ──────────────────────────────────────
const rules = send('routing-rules', [
  { id: '*1', identity: 'r1', values: { action: 'lookup', table: 'main' } },
  { id: '*2', identity: 'r2', values: { action: 'unreachable' } },
]);
const r = cells('routing-rules', rules);
assert.ok(/^<span style="[^"]*rgba\(99,130,190/.test(r[0].action) && r[0].action.endsWith('>lookup</span>'),
  'lookup is not the Firewall badge in its neutral colour: ' + r[0].action);
assert.ok(/rgba\(248,113,113/.test(r[1].action), 'unreachable is not red: ' + r[1].action);
assert.strictEqual(r[0].table, 'main', 'the table column became a pill');
say('ok  an action is drawn with the Firewall page\'s badge');

fs.rmSync(OUT, { force: true });
