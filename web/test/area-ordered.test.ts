/**
 * AN ORDERED RESOURCE ON A GENERATED PAGE GETS REORDER ARROWS (2026-09-18).
 *
 * Routing rules are first-match-wins, so position is part of what a rule does.
 * The generated page draws the Firewall page's arrows for a resource the
 * registry marks Ordered — named `data-res-move`, so the resource engine owns
 * the move — with the first row's up and the last row's down disabled, and
 * none at all for a viewer who may not write. The control is an unordered area
 * rendered by the same module, which gets no arrows for a writer either.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.area-ordered-entry.ts');
fs.writeFileSync(ENTRY,
  "export { initAreaPages } from '../web/src/pages/area.js';\n" +
  "export { AREAS } from '../web/src/gen/areas.js';\n");
const OUT = path.join(ROOT, 'testdata', '.area-ordered.cjs');
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
const shown = ['routing-rules', 'ip-pools'];
mod.initAreaPages({ on: (ev, fn) => { handlers[ev] = fn; }, emit: () => {} }, (p) => shown.includes(p));

const rules = mod.AREAS.find((a) => a.key === 'routing-rules');
assert.ok(rules && rules.tables[0].ordered === true, 'routing rules are not declared ordered in the generated table');
const pools = mod.AREAS.find((a) => a.key === 'ip-pools');
assert.ok(pools && pools.tables[0].ordered === false, 'ip pools are declared ordered');

const row = (id) => ({ id, identity: id, values: { action: 'lookup' } });
const send = (area, rows) => handlers['area:update']({ ts: 1, pollMs: 60000, area: area.key, title: area.title, denied: false,
  tables: [{ resource: area.tables[0].resource, title: area.tables[0].title, unsupported: false, singleton: false,
    columns: area.tables[0].columns, rows }] });
const body = (key) => String(doc.nodes['areaBody-' + key].innerHTML);

// A READER first: no arrows.
send(rules, [row('*1'), row('*2'), row('*3')]);
assert.ok(!/data-res-move/.test(body('routing-rules')), 'a viewer who may not write was drawn arrows');

// A WRITER: arrows on every row, the ends disabled.
handlers['res:schema']({ key: 'routingRule', permitted: true });
handlers['res:schema']({ key: 'ipPool', permitted: true });
const html = body('routing-rules');
const ups = html.match(/<button[^>]*data-res-move="up"[^>]*>/g) || [];
const downs = html.match(/<button[^>]*data-res-move="down"[^>]*>/g) || [];
assert.strictEqual(ups.length, 3, 'up arrows: ' + ups.length);
assert.strictEqual(downs.length, 3, 'down arrows: ' + downs.length);
assert.ok(/disabled/.test(ups[0]) && !/disabled/.test(ups[1]), 'the first row can move up, or the second cannot');
assert.ok(/disabled/.test(downs[2]) && !/disabled/.test(downs[1]), 'the last row can move down, or the one before cannot');
assert.ok((html.match(/<th[\s>]/g) || []).length === rules.tables[0].columns.length + 1, 'the arrow column has no header cell');
say('ok  an ordered area draws arrows for a writer, ends disabled, none for a reader');

// CONTROL: an unordered area, for the same writer, has none.
send(pools, [row('*1'), row('*2')]);
assert.ok(!/data-res-move/.test(body('ip-pools')), 'an unordered area was drawn arrows');
say('ok  an unordered area has none');

fs.rmSync(OUT, { force: true });
