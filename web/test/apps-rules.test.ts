/**
 * APPS USAGE (#167): THE PURE PARTS OF CATEGORIES, RULES AND THE UNNAMED DETAIL.
 *
 * - apps.ts: categoryRows sums a view by category, so the categories add up
 *   to the apps; catLabel names a custom category, a built-in one and an app
 *   with none ("uncategorized", which is not unnamed traffic).
 * - apps-manage.ts: the editor's three boxes, read and written back.
 * - apps-unnamed.ts: what picked rows can name an app by.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.apps-rules-entry.ts');
fs.writeFileSync(ENTRY, "export { categoryRows, catLabel, catTone, setAppCategories } from '../web/src/pages/apps.js';\n" +
  "export { parseDomains, parseASNs, parsePorts, ruleText, ruleSummary } from '../web/src/pages/apps-manage.js';\n" +
  "export { prefillFrom } from '../web/src/pages/apps-unnamed.js';\n");
const OUT = path.join(ROOT, 'testdata', '.apps-rules.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
// eslint-disable-next-line @typescript-eslint/no-require-imports
const m = require(OUT);
fs.rmSync(OUT, { force: true });

// Categories: custom names and tones, built-in labels, and none.
m.setAppCategories([
  { id: 'gaming', name: '', tone: '', builtin: true, apps: 3 },
  { id: 'u_kids', name: 'Kids', tone: 'pink', builtin: false, apps: 1 },
  { id: 'u_odd', name: 'Odd', tone: 'not-a-tone', builtin: false, apps: 0 },
]);
assert.equal(m.catLabel('u_kids'), 'Kids');
assert.equal(m.catTone('u_kids'), 'pink');
assert.equal(m.catTone('u_odd'), 'muted', 'an unknown tone falls back to grey');
assert.equal(m.catLabel('conferencing'), 'calls', 'a built-in label');
assert.equal(m.catLabel(''), 'uncategorized', 'an app with no category');

// The category sum: bytes and shares of ALL traffic, unnamed included.
const app = (id: string, category: string, rx: number, tx: number) => ({
  id, name: id, category, color: '#000000', logo: '', dark: false, rx, tx, total: rx + tx, share: 0, devices: 1, top: '', spark: [],
});
const rows = m.categoryRows({
  hour: true, rx: 900, tx: 100, unnamed: 400, span: 'in the last hour',
  apps: [app('a', 'gaming', 100, 0), app('b', 'u_kids', 300, 50), app('c', 'gaming', 50, 0), app('d', '', 100, 0)],
});
assert.deepEqual(rows.map((r: { id: string }) => r.id), ['u_kids', 'gaming', ''], 'busiest category first');
assert.equal(rows[1].total, 150);
assert.equal(rows[1].share, 15, 'a share is of all traffic, unnamed included');
assert.deepEqual(rows[1].apps.map((a: { id: string }) => a.id), ['a', 'c'], 'its apps, busiest first');
assert.equal(rows.reduce((s: number, r: { total: number }) => s + r.total, 0), 600, 'the categories add up to the apps');

// The editor's boxes.
assert.deepEqual(m.parseDomains(' *.Example.com \n\nexact.example.net, other.example'), [
  { domain: 'example.com', subdomains: true }, { domain: 'exact.example.net', subdomains: false },
  { domain: 'other.example', subdomains: false }]);
assert.deepEqual(m.parseASNs('AS13335\n64500\n as 1'), [13335, 64500, 1]);
assert.ok(typeof m.parseASNs('AS13335\nCloudflare') === 'string', 'a word is not an AS number');
assert.deepEqual(m.parsePorts('27015/udp\n3074-3075 / TCP'), [
  { proto: 'udp', from: 27015, to: 27015 }, { proto: 'tcp', from: 3074, to: 3075 }]);
assert.ok(typeof m.parsePorts('27015') === 'string', 'a port needs its protocol');
const rule = { domains: [{ domain: 'example.com', subdomains: true }, { domain: 'a.example', subdomains: false }],
  asns: [64500], ports: [{ proto: 'udp', from: 1, to: 1 }, { proto: 'tcp', from: 5, to: 9 }] };
const text = m.ruleText(rule);
assert.deepEqual(m.parseDomains(text.domains), rule.domains, 'domains round-trip');
assert.deepEqual(m.parseASNs(text.asns), rule.asns, 'networks round-trip');
assert.deepEqual(m.parsePorts(text.ports), rule.ports, 'ports round-trip');
assert.equal(m.ruleSummary({ ...rule, id: 'x', builtin: false, name: 'x', color: '', icon: '', category: '' }),
  '2 domains, 1 network, 2 ports');
assert.equal(m.ruleSummary(null), '');

// Picked unnamed rows: a known owner names by network, else a non-web port;
// a web port with no owner gives nothing to name by.
const row = (asn: number, proto: string, port: string) => ({
  dst: '198.51.100.1', proto, port, asn, owner: '', reason: 'no-name', client: '192.168.88.2', clientName: '',
  clientMac: '', rxBytes: 1, txBytes: 1,
});
const p = m.prefillFrom([row(64500, 'tcp', '443'), row(64500, 'udp', '9'), row(0, 'udp', '3074'), row(0, 'udp', '3074'),
  row(0, 'tcp', '443'), row(0, 'icmp', '')]);
assert.deepEqual(p.asns, [64500], 'one network, once');
assert.deepEqual(p.ports, [{ proto: 'udp', from: 3074, to: 3074 }], 'one port, once');
assert.equal(p.skipped, 2, 'a web port and an ICMP row have nothing to name by');

console.log('apps-rules: categories, the editor boxes and the unnamed prefill ok');
