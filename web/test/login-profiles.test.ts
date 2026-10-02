/**
 * MIKRODASH LOGIN PROFILES, THE BROWSER HALF.
 *
 *   - LINK TARGETS: a device ticked directly or through a site, minus those
 *     already on this profile. A device on ANOTHER profile is a target (linking
 *     moves it); one already linked is not (nothing to do).
 *   - THE FORM refuses a short or mismatched password before anything is sent,
 *     and an edit may leave the password blank (it keeps the current one).
 *   - THE ROW never offers Delete while devices sign in with the profile, and
 *     everything the server supplies is escaped.
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
  assert.deepStrictEqual(L.linkTargets(new Set(['s1']), new Set(), devices, 'p1'), ['a'],
    'a site did not tick its devices, or re-linked one already on the profile');
  assert.deepStrictEqual(L.linkTargets(new Set(), new Set(['c', 'd']), devices, 'p1'), ['c', 'd'],
    'a device on another profile, or a directly ticked one, was not a target');
  assert.deepStrictEqual(L.linkTargets(new Set(), new Set(), devices, 'p1'), []);
  say('ok  link targets: by site or directly, never one already linked');
}

{
  assert.match(L.formError('', 'x'.repeat(12), 'x'.repeat(12), true), /name/);
  assert.match(L.formError('Fleet', '', '', true), /required/);
  assert.match(L.formError('Fleet', 'short', 'short', true), /12/);
  assert.match(L.formError('Fleet', 'x'.repeat(12), 'y'.repeat(12), true), /differ/);
  assert.strictEqual(L.formError('Fleet', '', '', false), '', 'an edit could not keep the password');
  assert.strictEqual(L.formError('Fleet', 'x'.repeat(12), 'x'.repeat(12), true), '');
  say('ok  the form: name, length, match; blank keeps it on edit');
}

{
  const p = { id: 'p1', name: '<img src=x>', username: 'MikroDash', group: 'MikroDash', hasSecret: true,
    devices: ['b'], updatedAt: 0, op: null };
  const row = L.loginRow(p);
  assert.ok(!/<img/.test(row), 'a profile name reached the row unescaped');
  assert.match(row, /data-lp-del="p1" disabled/, 'Delete was offered while a device signs in with the profile');
  assert.ok(!/data-lp-del="p1" disabled/.test(L.loginRow({ ...p, devices: [] })),
    'control: an unused profile could not be deleted');
  const running = L.opStatus({ kind: 'password', running: true, started: 0, summary: '',
    results: { b: { state: 'done', message: '' }, c: { state: 'pending', message: '' } } });
  assert.match(running, /Changing password 1\/2/);
  say('ok  the row: escaped, Delete held while in use, the job shows progress');
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
