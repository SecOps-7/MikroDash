/**
 * ZERO-TOUCH PROVISIONING IN THE BROWSER (2026-09-22).
 *
 * The Devices page's Provisioning section, the Add device and Onboard wizards'
 * steps, and the script panel, as the pure functions `ztp.ts` wires. Pinned:
 *   - everything a router or an operator supplied is escaped (a router's
 *     identity and model arrive from the router itself);
 *   - each state offers exactly its actions, and a provisioned device leaves
 *     the section for the ordinary grid;
 *   - a step says why it cannot go on, and each gate opens once satisfied;
 *   - the checks that need an OK are one per code, because the server accepts
 *     by code (lines are only known from the device's own preview).
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.ztp-entry.ts');
fs.writeFileSync(ENTRY, "export * from '../web/src/pages/ztp-views.js';\nexport { railHtml } from '../web/src/wizard.js';\n");
const OUT = path.join(ROOT, 'testdata', '.ztp.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const V = require(OUT);
fs.rmSync(OUT, { force: true });

const NOW = Date.UTC(2026, 8, 22, 12);
const dev = (o: Record<string, unknown>) => ({
  id: 'd1', mode: 'remote', state: 'awaiting', label: 'Branch', serial: '', tunnelIp: '', model: '', version: '',
  identity: '', source: '', templateId: '', routerId: '', runId: '', batchName: '', siteIds: [], error: '',
  createdAt: NOW, expiresAt: 0, firstSeen: 0, lastSeen: 0, ...o,
});
const status = (o: Record<string, unknown> = {}) => ({
  enabled: true, up: true, error: '', instanceId: 'i', publicKey: 'k', endpoint: 'vpn.example.net', port: 13231,
  subnet: '10.249.0.0/16', lanUrl: '', peers: 0, handshakes: 0, ...o,
});

// ── ESCAPED, with a control that the plain value is there at all ────────────
const evil = '<img src=x onerror=alert(1)>';
const card = V.deviceCard(dev({ state: 'pending', label: evil, identity: evil + 'id', model: evil, error: evil }), NOW);
assert.ok(!card.includes('<img'), 'a hostile label, identity, model or error reached the markup');
assert.ok(card.includes('&lt;img'), 'the escaped text is missing, so the check above proves nothing');
const plain = V.deviceCard(dev({ label: 'Branch office', model: 'RB5009' }), NOW);
assert.ok(plain.includes('Branch office') && plain.includes('RB5009'), 'control: a plain card lost its facts');
const script = V.scriptPanel({ script: ':put "</pre><script>x</script>"', filename: 'mikrodash-ztp-a.rsc',
  expiresAt: NOW + 7 * 86400e3 }, NOW, 'Run it.');
assert.ok(!script.includes('<script>'), 'the script text reached the markup unescaped');
assert.ok(script.includes('/import file-name=mikrodash-ztp-a.rsc'), 'the command that runs the script is missing');
assert.ok(script.includes('in 7 days'), 'when the script stops working is not said');

// ── EACH STATE, ITS ACTIONS ──────────────────────────────────────────────────
const acts = (state: string): string[] =>
  [...V.deviceCard(dev({ state }), NOW).matchAll(/data-ztp-act="([a-z]+)"/g)].map((m: RegExpMatchArray) => m[1]);
assert.deepStrictEqual(acts('pending'), ['onboard', 'reject']);
assert.deepStrictEqual(acts('awaiting'), ['regenerate', 'delete']);
assert.deepStrictEqual(acts('failed'), ['retry', 'delete']);
assert.deepStrictEqual(acts('rejected'), ['delete']);
assert.deepStrictEqual(acts('provisioning'), [], 'a device mid-deploy offers something to press');
assert.deepStrictEqual(acts('enrolled'), []);
assert.ok(V.deviceCard(dev({ state: 'provisioning' }), NOW).includes('ztp-spin'), 'a busy device shows no progress');
assert.ok(!V.deviceCard(dev({ state: 'pending' }), NOW).includes('ztp-spin'), 'control: an idle device spins');

// ── THE SECTION ──────────────────────────────────────────────────────────────
const p = {
  status: status(),
  batches: [],
  devices: [dev({ id: 'a', state: 'awaiting' }), dev({ id: 'p', state: 'provisioned' }),
    dev({ id: 'n', state: 'pending', createdAt: NOW - 1 }), dev({ id: 'f', state: 'failed' })],
};
assert.deepStrictEqual(V.sectionDevices(p).map((d: { id: string }) => d.id), ['n', 'f', 'a'],
  'the order is not pending, failed, then the rest, or a provisioned device stayed in the section');
const sec = V.sectionHtml(p, NOW);
assert.match(sec, /card-badge active-blue">3</, 'the count pill does not count what is shown');
assert.match(sec, /1 device is waiting to be onboarded/);
assert.strictEqual(V.sectionHtml({ status: status({ enabled: false }), batches: [], devices: [] }, NOW), '',
  'an install not using provisioning shows an empty section');
assert.match(V.sectionHtml({ status: status(), batches: [], devices: [] }, NOW), /No devices are waiting/);

// ── STEP 1: remote needs a running tunnel with an address ───────────────────
assert.strictEqual(V.remoteBlocked(status()), '');
assert.match(V.remoteBlocked(status({ enabled: false })), /Switch provisioning on/);
assert.match(V.remoteBlocked(status({ up: false, error: 'port in use' })), /port in use/);
assert.match(V.remoteBlocked(status({ endpoint: '' })), /address routers reach/);
assert.match(V.whereStep('local', status({ enabled: false })), /value="remote" disabled/, 'a blocked Remote can be chosen');
assert.ok(!/value="remote"[^>]*disabled/.test(V.whereStep('remote', status())), 'control: an open Remote is disabled');

// ── STEP 2 ───────────────────────────────────────────────────────────────────
const form = { label: 'Branch', serial: '', siteIds: [], days: 7, lanUrl: 'http://192.168.88.10:3081' };
assert.strictEqual(V.deviceProblem(form, 'remote'), '');
assert.match(V.deviceProblem({ ...form, label: '  ' }, 'remote'), /name/);
assert.match(V.deviceProblem({ ...form, serial: 'HF 12;3' }, 'remote'), /serial/i);
assert.strictEqual(V.deviceProblem({ ...form, serial: 'HF1234567AB' }, 'remote'), '');
assert.match(V.deviceProblem({ ...form, lanUrl: '192.168.88.10' }, 'local'), /address the router reaches/);
assert.match(V.deviceProblem({ ...form, lanUrl: 'http://h/path' }, 'local'), /address the router reaches/);
assert.strictEqual(V.deviceProblem(form, 'local'), '');
assert.strictEqual(V.deviceProblem({ ...form, lanUrl: '' }, 'remote'), '', 'a remote device was asked for a LAN address');

// ── STEP 3: the template's gate ──────────────────────────────────────────────
const tpl = {
  id: 't', defs: [{ name: 'vlan', type: 'int', required: true }, { name: 'pass', type: 'secret' }],
  findings: [{ level: 'ack', code: 'mgmt-path', line: 3, message: 'May cut MikroDash off' },
    { level: 'ack', code: 'mgmt-path', line: 9, message: 'May cut MikroDash off' },
    { level: 'info', code: 'x', message: 'note' }],
};
assert.deepStrictEqual(V.ackCodes(tpl.findings).map((f: { code: string }) => f.code), ['mgmt-path'],
  'one OK per code, not per line');
assert.strictEqual(V.configProblem(null, '', {}, new Set()), '', 'choosing no template blocked the step');
assert.match(V.configProblem(null, 't', {}, new Set()), /Opening/);
assert.match(V.configProblem(tpl, 't', { vlan: '' }, new Set()), /Fill in vlan/);
assert.match(V.configProblem(tpl, 't', { vlan: '20' }, new Set()), /Tick OK/);
assert.strictEqual(V.configProblem(tpl, 't', { vlan: '20' }, new Set(['mgmt-path'])), '');
const refused = { ...tpl, findings: [{ level: 'refuse', code: 'r', message: 'no' }] };
assert.match(V.configProblem(refused, 't', { vlan: '20' }, new Set()), /cannot be applied/);
const step3 = V.configStep([{ id: 't', name: 'VLANs', description: '', canned: false }], 't', tpl, 'Branch',
  { vlan: '20', pass: 's3cret' }, new Set(), false);
assert.ok(step3.includes('data-ztp-ack="mgmt-path"'), 'the check needing an OK has no box');
assert.strictEqual((step3.match(/data-ztp-ack=/g) || []).length, 1, 'one box per code');
assert.match(step3, /type="password" value="s3cret"/, 'a secret setting is shown in the clear');

// ── THE RAIL ─────────────────────────────────────────────────────────────────
const rail = V.railHtml([{ title: 'Where', render: () => '' }, { title: 'Device', render: () => '' },
  { title: 'Script', render: () => '' }], 1);
assert.match(rail, /is-done"><span class="wiz-dot">&#10003;<\/span><span class="wiz-step-name">Where/);
assert.match(rail, /is-current" aria-current="step"><span class="wiz-dot">2<\/span><span class="wiz-step-name">Device/);
assert.match(rail, /Step 2 of 3: Device/, 'the narrow screen has no step count');

// ── GENERIC SCRIPTS ──────────────────────────────────────────────────────────
const rows = V.batchRows([{ id: 'b1', name: 'Rollout', live: true, devices: 2, createdAt: 0, expiresAt: NOW + 3 * 86400e3, revokedAt: 0 },
  { id: 'b2', name: 'Old', live: false, devices: 0, createdAt: 0, expiresAt: 0, revokedAt: NOW }], NOW);
assert.strictEqual((rows.match(/data-ztp-revoke=/g) || []).length, 1, 'only a live script can be revoked');
assert.match(rows, /Revoked/);

// ── NEAR TIMES ───────────────────────────────────────────────────────────────
assert.strictEqual(V.relTime(NOW + 7 * 86400e3, NOW), 'in 7 days');
assert.strictEqual(V.relTime(NOW - 3 * 60e3, NOW), '3 minutes ago');
assert.strictEqual(V.relTime(NOW - 1 * 3600e3, NOW), '60 minutes ago');
assert.strictEqual(V.relTime(NOW - 20e3, NOW), 'just now');
assert.strictEqual(V.relTime(0, NOW), '');

console.log('ztp: views, gates and rail pinned');
