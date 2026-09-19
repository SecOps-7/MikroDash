/**
 * THE SECURITY SCAN PAGE (2026-09-19).
 *
 * - Opening the page asks for the last report; with none it asks for a scan,
 *   and with a fresh one it does not.
 * - Progress frames drive the bar and the Rescan button; the report draws the
 *   score (top-left card), its grade, the count pill and the findings.
 * - The findings table is failures only, severity by rank (critical first),
 *   and every router-derived value is escaped.
 * - A frame about the router just left is dropped.
 * - The cards' pure renderers: Open links name the fixing page, the accounts
 *   and surface cards mark what is wrong, the unknown checks are listed.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.secscan-entry.ts');
fs.writeFileSync(ENTRY, [
  "export { initSecurityScanPage } from '../web/src/pages/security-scan.js';",
  "export * as cards from '../web/src/pages/security-scan-cards.js';",
].join('\n') + '\n');
const OUT = path.join(ROOT, 'testdata', '.secscan.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const mod = require(OUT);

const doc = makeDoc(['secScanBadge', 'secScanTabs', 'secScanAge', 'secScanRun', 'secScanBody', 'secScanProgress',
  'secScanProgressBar', 'secScanStatus', 'secScoreCard', 'secScoreRing', 'secScoreVal', 'secScoreLabel', 'secScoreSub',
  'secSevCritical', 'secSevHigh', 'secSevMedium', 'secSevLow', 'secCats', 'secTop', 'secSurface', 'secFirewall',
  'secAccounts', 'secUpdates', 'secCoverage', 'secFindingsHead', 'secFindingsRows', 'secPassedRows', 'secFilters',
  'secPanel-overview', 'secPanel-findings', 'secPanel-passed'],
  // The filter chips' active state: a descendant selector this shim does not
  // answer. Which chip is lit is checked in the browser.
  { allowUnknown: ['#secFilters [data-filter]'] });
global.document = doc;
global.window = { addEventListener: () => {}, setTimeout, clearTimeout };
const handlers = {};
const sent = [];
mod.initSecurityScanPage({ on: (ev, fn) => { handlers[ev] = fn; }, emit: (ev, d) => sent.push([ev, d]) }, () => true);
const n = doc.nodes;

const f = (id, severity, status, extra = {}) => ({ id, category: 'Firewall', severity, title: 'T ' + id, status,
  detail: [], why: 'why', fix: 'fix', link: 'firewall', ...extra });
const report = {
  score: 59, passed: 2, unknown: 1,
  failed: { critical: 1, high: 1, medium: 0, low: 1, info: 0 },
  categories: [{ name: 'Firewall', score: 59, pass: 2, fail: 3, unknown: 1 }],
  findings: [
    f('fw.syncookies', 'low', 'fail'),
    f('fw.input-default-drop', 'critical', 'fail', { detail: ['<b>rule</b>'] }),
    f('fw.forward-wan', 'high', 'fail', { link: '' }),
    f('fw.input-established', 'medium', 'pass'),
    f('fw.input-invalid', 'low', 'pass'),
    f('sys.firmware', 'low', 'unknown', { title: 'Firmware behind' }),
  ],
  facts: { services: [{ name: 'telnet', port: '23', enabled: true, restricted: false, plaintext: true }],
    installed: '7.24.3', latest: '7.24.4', firmwareCurrent: '', firmwareUpgrade: '', users: 2, fullUsers: 2,
    minPasswordLen: '0', adminEnabled: true },
};
const frame = (extra) => ({ routerId: 'r1', report: null, scannedAt: 0, running: false, done: 0, total: 37,
  code: '', message: '', ...extra });

// OPENING THE PAGE asks for the last report; none, so it asks for a scan.
doc.dispatchEvent({ type: 'mikrodash:pagechange', detail: 'security-scan' });
assert.deepStrictEqual(sent, [['secscan:get', {}]], 'opening the page did not ask for the last report');
handlers['secscan:result'](frame({}));
assert.deepStrictEqual(sent.slice(1), [['secscan:run', {}]], 'with no report the page did not scan');
assert.strictEqual(n.secScanRun.disabled, true, 'Rescan is offered while a scan runs');

// PROGRESS drives the bar and the status.
handlers['secscan:result'](frame({ running: true, done: 10 }));
assert.strictEqual(n.secScanProgressBar.style.width, '27%', 'the progress bar does not follow the menus read');
assert.ok(/10 of 37/.test(String(n.secScanStatus.textContent)), 'the status does not count the menus read');

// THE REPORT draws the score, its grade, the pill and the findings.
handlers['secscan:result'](frame({ report, scannedAt: Date.now(), done: 37 }));
assert.strictEqual(String(n.secScoreVal.textContent), '59', 'the score card does not show the score');
assert.ok(n.secScoreCard.classList.contains('grade-poor') && !n.secScoreCard.classList.contains('grade-good'),
  'a 59 is not graded poor');
assert.strictEqual(n.secScoreRing.attributes['stroke-dashoffset'], (2 * Math.PI * 52 * 0.41).toFixed(1),
  'the ring does not sweep to the score');
assert.ok(/At risk · 3 issues/.test(String(n.secScoreLabel.textContent)), 'the label is ' + n.secScoreLabel.textContent);
assert.strictEqual(String(n.secScanBadge.textContent), '3', 'the count pill does not count the issues');
assert.ok(/active-blue/.test(String(n.secScanBadge.className)), 'the count pill is not blue with issues to count');
assert.strictEqual(n.secScanRun.disabled, false, 'Rescan stays disabled after the report');
assert.strictEqual(sent.length, 2, 'a fresh report started another scan');

// THE FINDINGS: failures only, critical first, escaped.
const rows = String(n.secFindingsRows.innerHTML);
const at = (id) => rows.indexOf('T ' + id);
assert.ok(at('fw.input-default-drop') >= 0 && at('fw.input-default-drop') < at('fw.forward-wan') &&
  at('fw.forward-wan') < at('fw.syncookies'), 'findings are not in severity order:\n' + rows);
assert.ok(!/T fw.input-established/.test(rows), 'a passed check is listed as a finding');
assert.ok(!/<b>rule<\/b>/.test(rows) && /&lt;b&gt;rule/.test(rows), 'a finding detail was not escaped');
assert.ok(/data-goto="firewall"/.test(rows), 'a finding does not link to the page it is fixed on');
assert.strictEqual((rows.match(/data-goto=/g) || []).length, 2, 'a finding with no page got an Open link');
assert.ok(/T fw.input-established/.test(String(n.secPassedRows.innerHTML)), 'the passed checks are not listed');

// A FRAME ABOUT THE ROUTER JUST LEFT is dropped; the new router is asked.
sent.length = 0;
handlers['router:switched']({ activeId: 'r2' });
assert.deepStrictEqual(sent, [['secscan:get', {}]], 'a router switch did not ask the new router');
assert.strictEqual(String(n.secScoreVal.textContent), '—', 'the old router\'s score survived the switch');
handlers['secscan:result'](frame({ report, scannedAt: Date.now() }));
assert.strictEqual(String(n.secScoreVal.textContent), '—', 'the old router\'s report was drawn on the new router');
// A fresh report for the new router is drawn and starts no scan.
handlers['secscan:result'](frame({ routerId: 'r2', report: { ...report, score: 91 }, scannedAt: Date.now() }));
assert.strictEqual(String(n.secScoreVal.textContent), '91', 'the new router\'s report was not drawn');
assert.ok(n.secScoreCard.classList.contains('grade-good'), 'a 91 is not graded good');
assert.strictEqual(sent.length, 1, 'a fresh report started a scan');

// A REFUSAL says why.
handlers['secscan:result'](frame({ routerId: 'r2', code: 'denied' }));
assert.ok(/may not/.test(String(n.secScanStatus.textContent)), 'a denied scan does not say so');

// THE PURE RENDERERS.
const c = mod.cards;
assert.ok(/Firmware behind/.test(c.coverageCard(report)), 'coverage does not list what could not be answered');
assert.ok(/class="sec-bad">enabled/.test(c.accountsCard(report)), 'an enabled admin is not marked');
assert.ok(/hs-stale sec-svc/.test(c.surfaceCard(report)), 'a plaintext service is not a red pill');
assert.strictEqual(c.sortValue(report.findings[1], 'severity'), 0, 'critical does not sort first');
assert.strictEqual(c.openLink(report.findings[2]), '', 'a finding with no page got a link');

// LEAVING THE PAGE stops its age ticker (and lets this test exit).
doc.dispatchEvent({ type: 'mikrodash:pagechange', detail: 'dashboard' });

fs.rmSync(OUT, { force: true });
say('security-scan: ok');
