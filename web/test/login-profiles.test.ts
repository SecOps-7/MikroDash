/**
 * MIKRODASH LOGIN PROFILES, THE BROWSER HALF.
 *
 *   - LINK TARGETS: the picked devices minus those already on this login. A
 *     device on ANOTHER login is a target (linking moves it).
 *   - THE FORM refuses a missing, short or mismatched password before anything
 *     is sent.
 *   - THE PINNED ROW is always drawn - "not set" with Set password before the
 *     login exists - never offers Delete, and escapes what the server sends.
 *   - A DOWN DEVICE ON A PROFILE says which profile's password it used.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const OUT = path.join(ROOT, 'testdata', '.lp.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'config-management-logins.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
const L = require(OUT);

const devices = [
  { id: 'a', label: 'A', siteIds: ['s1'], loginProfileId: '' },
  { id: 'b', label: 'B', siteIds: ['s1'], loginProfileId: 'p1' },
  { id: 'c', label: 'C', siteIds: [], loginProfileId: 'p2' },
  { id: 'd', label: 'D', siteIds: [], loginProfileId: '' },
];

{
  // RE-AIMED 2026-10-02: a site is the picker's shortcut now, adding its devices
  // as picked ones, so Link acts on the picked devices minus the linked.
  assert.deepStrictEqual(L.linkTargets(['a', 'b', 'c'], devices, 'p1'), ['a', 'c'],
    'a device already on the login was a target, or one on another login was not');
  assert.deepStrictEqual(L.linkTargets([], devices, 'p1'), []);
  say('ok  link targets: the picked devices, never one already linked');
}

{
  assert.match(L.formError('', '', true), /required/);
  assert.match(L.formError('short', 'short', true), /12/);
  assert.match(L.formError('x'.repeat(12), 'y'.repeat(12), true), /differ/);
  assert.strictEqual(L.formError('x'.repeat(12), 'x'.repeat(12), true), '');
  say('ok  the form: required, length, match');
}

{
  const none = L.pinnedRow(null);
  assert.match(none, /MikroDash login/);
  assert.match(none, /not set/);
  assert.match(none, /data-lp-new="1"/, 'a login not set up yet offers no way to set it');
  const p = { id: 'p1', name: '<img src=x>', username: 'mikrodash', group: 'mikrodash', hasSecret: true,
    devices: ['b'], updatedAt: 0, op: null };
  const row = L.pinnedRow(p);
  assert.ok(!/<img/.test(row), 'the login name reached the row unescaped');
  assert.ok(!/data-lp-del|Delete/.test(row), 'the pinned login offers Delete');
  assert.match(row, /data-lp-edit="p1"/);
  assert.match(row, /<td>1<\/td>/, 'the device count is wrong');
  const running = L.opStatus({ kind: 'password', running: true, started: 0, summary: '',
    results: { b: { state: 'done', message: '' }, c: { state: 'pending', message: '' } } });
  assert.match(running, /Changing password 1\/2/);
  assert.match(L.opStatus(null, 2), />applied</, 'a linked login with no job in memory does not say applied');
  assert.match(L.opStatus(null, 0), />not linked</);
  const failed = L.opStatus({ kind: 'link', running: false, started: 0, summary: 'a long sentence',
    results: { b: { state: 'failed', message: 'x' }, c: { state: 'done', message: '' } } });
  assert.match(failed, />1 failed</, 'a finished job does not say how many failed');
  assert.match(failed, /title="a long sentence"/, 'the summary is not in the tooltip');
  say('ok  the pinned row: always drawn, never deletable, escaped; the job shows progress');
}
fs.rmSync(OUT, { force: true });

// ── the device card ─────────────────────────────────────────────────────────
const COUT = path.join(ROOT, 'testdata', '.lpcard.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'devices-card.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + COUT, '--log-level=warning'],
  { stdio: 'inherit' });
const C = require(COUT);
{
  const row = {
    id: 'r', label: 'R', host: '198.51.100.1', isActive: false, connected: false, online: false, known: true,
    lastError: 'invalid user name or password (6)', openAlerts: 0, uptime: null, version: null, boardName: null,
    arch: null, serial: null, licenseLevel: null, updateAvailable: null, latestVersion: null, clients: null,
    siteIds: [], siteNames: [], siteId: null, siteName: null, geo: null, loginProfile: 'Fleet',
  };
  assert.match(C.deviceCardHtml(row, undefined, false, 0), /invalid user name or password \(6\) <span class="dv-why-via">\(login profile Fleet\)/,
    'a down device on a profile does not say which profile');
  assert.ok(!/login profile/.test(C.deviceCardHtml({ ...row, loginProfile: null }, undefined, false, 0)),
    'control: a device with its own login named a profile');
  say('ok  a down device on a profile names it');
}
fs.rmSync(COUT, { force: true });
say('login-profiles: all checks passed');
