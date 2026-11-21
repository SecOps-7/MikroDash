/**
 * THE APPS USAGE DASHBOARD CARD, ITS PURE PARTS.
 *
 * web/src/pages/dashboard-card-apps.ts draws the Apps Usage page's last hour:
 * the page's ring (apps.ts ringSlices) and a list of the busiest apps. This
 * drives the list rows, the span label and the ring's slices with payloads
 * shaped like internal/collect/apps.go's AppUsagePayload.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.apps-card-entry.ts');
fs.writeFileSync(ENTRY, "export { appsCardRows, cardSpan } from '../web/src/pages/dashboard-card-apps.js';\n" +
  "export { ringSlices } from '../web/src/pages/apps.js';\n");
const OUT = path.join(ROOT, 'testdata', '.apps-card.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
// eslint-disable-next-line @typescript-eslint/no-require-imports
const { appsCardRows, cardSpan, ringSlices } = require(OUT);
fs.rmSync(OUT, { force: true });

const app = (id: string, name: string, total: number, share: number) => ({
  id, name, category: 'streaming', color: '#e50914', logo: '', dark: false,
  rxBytes: total, txBytes: 0, totalBytes: total, share, devices: 1, via: 'name', spark: [], top: '',
});
const hour = (apps: ReturnType<typeof app>[], mins = 60) => ({
  ts: 1, from: 0, to: mins * 60000, rxBytes: 1000, txBytes: 0, unnamedBytes: 100,
  apps, devices: {}, dnsNames: 0, dnsAt: 0,
});

// The rows: at most as many as fit, busiest first, every name escaped.
const many = hour([app('netflix', 'Netflix', 500, 50), app('zoom', 'Zoom', 300, 30), app('x', '<b>X</b>', 100, 10)]);
const two = appsCardRows(many, 2);
assert.equal((two.match(/au-card-row/g) || []).length, 2, 'two rows asked, two drawn');
assert.ok(two.indexOf('Netflix') < two.indexOf('Zoom'), 'busiest first');
const all = appsCardRows(many, 10);
assert.equal((all.match(/au-card-row/g) || []).length, 3, 'never more rows than apps');
assert.ok(!all.includes('<b>X</b>') && all.includes('&lt;b&gt;X&lt;/b&gt;'), 'an app name is escaped');
assert.ok(all.includes('width:50.0%'), "a row's bar is its share");
assert.equal((appsCardRows(many, 0).match(/au-card-row/g) || []).length, 1, 'a card with no height still shows one');
assert.ok(appsCardRows(hour([]), 5).includes('au-card-empty'), 'an hour with no apps says so');

// The span: the whole hour, or the minutes measured so far.
assert.equal(cardSpan(hour([], 60)), 'last hour');
assert.equal(cardSpan(hour([], 12)), 'last 12 minutes');

// The ring: the busiest apps, then the rest as one slice, then the unnamed.
const s = ringSlices([{ id: 'a', color: '#111', total: 5 }, { id: 'b', color: '#222', total: 3 },
  { id: 'c', color: '#333', total: 2 }], 4, 2);
assert.deepEqual(s.map((x: { id: string }) => x.id), ['a', 'b', '~other', '~unnamed']);
assert.equal(s[2].v, 2, 'the rest is summed');
assert.equal(ringSlices([], 0).length, 0, 'no traffic, no slices');

console.log('apps-card: rows, span and ring slices ok');
