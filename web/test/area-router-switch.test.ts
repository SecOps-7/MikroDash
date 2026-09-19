/**
 * A ROUTER SWITCH FORGETS THE GENERATED PAGES' ROWS (review loop, 2026-09-19).
 *
 * `latest`, the open group, its rows and the write permissions were never
 * cleared, so an area page drew router A's rows under router B's name until B's
 * first payload, and offered A's writability. `resetAreaPages` forgets them and
 * redraws each area as waiting.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.area-switch-entry.ts');
fs.writeFileSync(ENTRY,
  "export { initAreaPages, resetAreaPages } from '../web/src/pages/area.js';\n" +
  "export { AREAS } from '../web/src/gen/areas.js';\n");
const OUT = path.join(ROOT, 'testdata', '.area-switch.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const mod = require(OUT);
const KEY = 'ip-pools';
const ids = mod.AREAS.flatMap((a) => ['areaBody-', 'areaBadge-', 'areaTabs-', 'areaAdd-', 'areaGroupTable-', 'areaGroupNote-']
  .map((p) => p + a.key));
const doc = makeDoc(ids, { query: { '[data-res-add]': [], '[data-res-rows]': [] } });
const handlers = {};
global.document = doc;
global.window = { addEventListener: () => {}, setTimeout, clearTimeout };
mod.initAreaPages({
  on: (ev, fn) => { (handlers[ev] = handlers[ev] || []).push(fn); },
  emit: () => {},
}, (p) => p === KEY);
const fire = (ev, d) => (handlers[ev] || []).forEach((fn) => fn(d));

const area = mod.AREAS.find((a) => a.key === KEY);
const body = () => String(doc.nodes['areaBody-' + KEY].innerHTML);
fire('area:update', { ts: 1, pollMs: 60000, area: KEY, title: area.title, denied: false,
  tables: [{ resource: area.tables[0].resource, title: 'Pool', columns: area.tables[0].columns,
    rows: [{ id: '*1', identity: 'pool-of-router-a', values: { name: 'pool-of-router-a', ranges: '198.51.100.10-198.51.100.20' } }],
    unsupported: false, singleton: false }] });
assert.ok(body().includes('pool-of-router-a'), "the control: router A's row is drawn");
assert.strictEqual(doc.nodes['areaBadge-' + KEY].textContent, '1');

mod.resetAreaPages();
assert.ok(!body().includes('pool-of-router-a'), "after a router switch, router A's row is still drawn: " + body());
assert.ok(/Waiting/.test(body()), 'the area is not shown as waiting for the new router');
assert.strictEqual(doc.nodes['areaBadge-' + KEY].textContent, '0', "the pill still counts router A's rows");
console.log("ok  a router switch forgets the generated pages' rows");
fs.rmSync(OUT, { force: true });
