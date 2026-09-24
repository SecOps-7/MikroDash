/**
 * A SETTINGS MENU (a singleton) RENDERS AS A CARD (2026-09-18).
 *
 * /system/ntp/client and its kind are one row with no `.id`. The server gives
 * that row the synthetic id `singleton`; the page must draw it as a label and a
 * value per declared field - not as a one-row table - with every line carrying
 * the row's engine attributes, so clicking any of them opens the form for the
 * one row. The control is the same area's Servers tab, an ordinary table.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc, RES_MODAL_IDS } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.area-single-entry.ts');
fs.writeFileSync(ENTRY,
  "export { initAreaPages } from '../web/src/pages/area.js';\n" +
  "export { AREAS } from '../web/src/gen/areas.js';\n");
const OUT = path.join(ROOT, 'testdata', '.area-single.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const KEY = 'ntp-client';
const mod = require(OUT);
const ids = mod.AREAS.flatMap((a) => ['areaBody-', 'areaBadge-', 'areaTabs-', 'areaAdd-'].map((p) => p + a.key));
const doc = makeDoc(ids, { allowUnknown: [...RES_MODAL_IDS], query: { '[data-res-add]': [], '[data-res-rows]': [] } });
const handlers = {};
global.document = doc;
global.window = { addEventListener: () => {}, setTimeout, clearTimeout };
mod.initAreaPages({ on: (ev, fn) => { handlers[ev] = fn; }, emit: () => {} }, (p) => p === KEY);

const area = mod.AREAS.find((a) => a.key === KEY);
assert.ok(area, 'no ntp-client area in the generated table');
handlers['area:update']({
  ts: 1, pollMs: 60000, area: KEY, title: 'NTP Client', denied: false,
  tables: [
    { resource: 'ntpClient', title: 'Settings', unsupported: false, singleton: true,
      columns: area.tables[0].columns,
      rows: [{ id: 'singleton', identity: 'NTP Client',
        values: { enabled: 'true', mode: 'unicast', servers: 'pool.ntp.org', vrf: 'main', status: 'synchronized' } }] },
    { resource: 'ntpServer', title: 'Servers', unsupported: false, singleton: false,
      columns: area.tables[1].columns, rows: [{ id: '*1', identity: '192.0.2.123', values: { address: '192.0.2.123' } }] },
  ],
});

const body = () => String(doc.nodes['areaBody-' + KEY].innerHTML);
let html = body();
const lines = html.match(/<tr[^>]*>/g) || [];
assert.strictEqual(lines.length, area.tables[0].columns.length,
  'the settings card has ' + lines.length + ' lines for ' + area.tables[0].columns.length + ' fields:\n' + html);
assert.ok(lines.every((l) => /data-id="singleton"/.test(l) && /data-res="ntpClient"/.test(l)),
  'a settings line does not open the singleton row:\n' + html);
assert.ok(!/<thead>/.test(html), 'a singleton was drawn as a table with a header row');
assert.ok(/Servers<\/th><td>pool\.ntp\.org<\/td>/.test(html), 'the servers value is not beside its label:\n' + html);
say('ok  a singleton is a card: one label and value per field, each opening the one row');

// CONTROL: the Servers tab is an ordinary table with a header.
doc.dispatch('click', { closest: () => ({ getAttribute: (k) => (k === 'data-areatab' ? KEY : '1') }) });
html = body();
assert.ok(/<thead>/.test(html) && /data-id="\*1"/.test(html), 'the ordinary tab lost its table:\n' + html);
say('ok  the ordinary tab beside it is still a table');

fs.rmSync(OUT, { force: true });
