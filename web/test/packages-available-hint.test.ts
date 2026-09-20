/**
 * THE PACKAGES PAGE SAYS WHY "AVAILABLE" READS ZERO (2026-09-20).
 *
 * RouterOS lists the packages it could install only after a successful
 * `check-for-updates`, and lists none until then. The operator's hAP AX3 had
 * had no successful check since its upgrade, so the page showed its two
 * installed packages and nothing else, which reads exactly like a build with no
 * extras. The count now carries a line saying where the list comes from, and it
 * is there only while the count is zero.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.pkghint-entry.ts');
fs.writeFileSync(ENTRY, "export { initPackagesPage } from '../web/src/pages/packages.js';\n");
const OUT = path.join(ROOT, 'testdata', '.pkghint.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const mod = require(OUT);

const doc = makeDoc(['pkgSumInstalled', 'pkgSumAvailable', 'pkgAvailHint', 'pkgSumDisabled', 'pkgSumUpdate',
  'pkgPendingCard', 'pkgPendingList', 'pkgApplyBtn', 'packagesCard', 'packagesBadge', 'pkgActionNote',
  'pkgStatus', 'pkgCheckBtn', 'packagesSearch', 'packagesThead', 'packagesTable', 'packagesFwCard',
  'pkgFwBody', 'pkgRbCard', 'pkgRbBody', 'pkgAutoUpgrade']);
global.document = doc;
global.window = { addEventListener: () => {}, setTimeout, clearTimeout };
const handlers = {};
mod.initPackagesPage({ on: (ev, fn) => { handlers[ev] = fn; }, emit: () => {} }, () => true);
const n = doc.nodes;

const payload = (available, packages) => ({
  packages, counts: { installed: 2, available, disabled: 0, scheduled: 0, unknown: 0 },
  update: { installedVersion: '7.24.4', latestVersion: '', updateAvailable: false, status: '', channel: 'stable' },
  firmware: { current: '', upgrade: '', upgradeAvailable: false, autoUpgrade: false },
  routerId: 'r1', code: '', message: '',
});
const pkg = (name, state) => ({ id: '*1', name, version: state === 'installed' ? '7.24.4' : '', state,
  size: '1000', buildTime: '', scheduled: '' });

// A ROUTER THAT HAS NOT CHECKED: installed packages only, and the line saying so.
handlers['packages:update'](payload(0, [pkg('routeros', 'installed'), pkg('wifi-qcom', 'installed')]));
assert.strictEqual(n.pkgSumAvailable.textContent, '0');
assert.strictEqual(n.pkgAvailHint.style.display, '', 'an empty available count does not say where the list comes from');
// THE WORDING LIVES IN THE MARKUP, not in a string this module assigns, so it
// is read from the file: the shim's nodes start empty and asserting their text
// would be asserting the shim.
const markup = fs.readFileSync(path.join(ROOT, 'web', 'src', 'ui', 'page-packages.html'), 'utf8');
const hintLine = markup.split('\n').find((l) => l.includes('id="pkgAvailHint"')) || '';
assert.match(hintLine, /update check/i, 'the line beside the count does not name what fills it in');

// AND ONCE A CHECK HAS RUN, the line goes: the number speaks for itself.
handlers['packages:update'](payload(17, [pkg('routeros', 'installed'), pkg('calea', 'available')]));
assert.strictEqual(n.pkgSumAvailable.textContent, '17');
assert.strictEqual(n.pkgAvailHint.style.display, 'none', 'the line stayed after the packages were listed');

// A payload that never reached the router shows a dash, and explains nothing:
// "unknown" is not "none".
handlers['packages:update']({ ...payload(0, []), counts: { installed: 0, disabled: 0, scheduled: 0, unknown: 0 } });
assert.strictEqual(n.pkgSumAvailable.textContent, '—');

console.log('ok  the Packages page says an empty Available count waits on an update check');
fs.rmSync(OUT, { force: true });
