/**
 * THE DASHBOARD'S SECURITY SCORE CARD (2026-09-19).
 *
 * - A report draws the page's ring, grade words and issue count, lights the
 *   severity tiles that have issues, and says checks passed and the age.
 * - A scan in flight shows progress and disables Rescan; the end of it enables
 *   it again.
 * - A refused Rescan says why and keeps the report on the card.
 * - A frame about another router is not drawn; a switch draws that router's
 *   last frame, even one sent before the switch was announced (the server's
 *   order), or waits when there is none.
 * - Rescan sends one `secscore:scan`; Open goes to the Security Scan page.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.secscore-entry.ts');
fs.writeFileSync(ENTRY, "export * from '../web/src/pages/dashboard-card-secscore.js';\n");
const OUT = path.join(ROOT, 'testdata', '.secscore.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const mod = require(OUT);

const doc = makeDoc(['dc-secScore', 'dc-secRing', 'dc-secVal', 'dc-secLabel', 'dc-secSevs', 'dc-secProgress',
  'dc-secProgressBar', 'dc-secMeta', 'dc-secRescan', 'dc-secOpen']);
global.document = doc;
global.window = { addEventListener: () => {}, setTimeout, clearTimeout };
const n = doc.nodes;
const handlers = {};
const sent = [];
mod.initSecScoreCard({ on: (ev, fn) => { handlers[ev] = fn; }, emit: (ev, d) => sent.push([ev, d]) });

const state = (extra = {}) => ({ routerId: 'r1', has: true, score: 72, issues: 3, critical: 0, high: 1, medium: 2,
  low: 0, passed: 30, checks: 46, scannedAt: Date.now() - 5 * 60_000, running: false, done: 0, total: 37,
  code: '', message: '', ...extra });

handlers['router:switched']({ activeId: 'r1' });

// A REPORT: the ring, the grade and issues, the lit tiles, the age.
handlers['secscore:state'](state());
assert.strictEqual(n['dc-secVal'].textContent, '72');
assert.strictEqual(n['dc-secLabel'].textContent, 'Fair · 3 issues', 'the label is not the page\'s grade and count');
assert.strictEqual(n['dc-secRing'].attributes['stroke-dashoffset'], (2 * Math.PI * 52 * 0.28).toFixed(1),
  'the ring does not sweep to the score');
assert.ok(n['dc-secScore'].classList.contains('grade-fair') && !n['dc-secScore'].classList.contains('grade-good'),
  'the card is not coloured by its grade');
const sevs = String(n['dc-secSevs'].innerHTML);
assert.ok(/sev-high is-on"><b>1</.test(sevs) && /sev-medium is-on"><b>2</.test(sevs), 'a severity with issues is not lit');
assert.ok(/sev-critical"><b>0</.test(sevs), 'a severity with no issues is lit');
assert.strictEqual(n['dc-secMeta'].textContent, '30 of 46 checks passed · scanned 5 min ago');
assert.strictEqual(n['dc-secRescan'].disabled, false);

// A SCAN IN FLIGHT: progress, Rescan disabled; its end enables it again.
handlers['secscore:state'](state({ running: true, done: 10 }));
assert.strictEqual(n['dc-secMeta'].textContent, 'Scanning 10 of 37 menus…');
assert.ok(n['dc-secProgress'].classList.contains('is-on'), 'no progress bar while scanning');
assert.strictEqual(n['dc-secProgressBar'].style.width, '27%');
assert.strictEqual(n['dc-secRescan'].disabled, true, 'Rescan is offered while a scan runs');
handlers['secscore:state'](state({ score: 91, issues: 0, high: 0, medium: 0 }));
assert.strictEqual(n['dc-secRescan'].disabled, false, 'Rescan stayed disabled after the scan landed');
assert.strictEqual(n['dc-secLabel'].textContent, 'Good · 0 issues');
assert.ok(!n['dc-secProgress'].classList.contains('is-on'), 'the progress bar stayed after the scan');

// A REFUSED RESCAN says why and keeps the report.
handlers['secscore:state']({ ...state({ has: false, score: 0 }), code: 'denied' });
assert.strictEqual(n['dc-secVal'].textContent, '91', 'a refusal blanked the report');
assert.strictEqual(n['dc-secMeta'].textContent, 'You may not scan this router.');
assert.ok(n['dc-secMeta'].classList.contains('is-bad'));

// NO REPORT YET, and one being taken (the card's own first scan).
handlers['secscore:state'](state({ has: false, score: 0, issues: 0, running: true, done: 0 }));
assert.strictEqual(n['dc-secLabel'].textContent, 'Scanning…');
assert.strictEqual(n['dc-secVal'].textContent, '—');

// ANOTHER ROUTER'S FRAME is dropped; the switch resets the card.
handlers['router:switched']({ activeId: 'r2' });
assert.strictEqual(n['dc-secLabel'].textContent, 'Waiting for the router…', 'a switch left r1\'s state on the card');
handlers['secscore:state'](state({ routerId: 'r1', score: 13 }));
assert.notStrictEqual(n['dc-secVal'].textContent, '13', 'a frame about the router just left was drawn');
handlers['secscore:state'](state({ routerId: 'r2', score: 55 }));
assert.strictEqual(n['dc-secVal'].textContent, '55');
assert.strictEqual(n['dc-secLabel'].textContent, 'At risk · 3 issues');

// THE NEW ROUTER'S STATE BEFORE THE SWITCH: the server sends it as part of the
// move and announces the switch after. Found live: the card dropped it and
// then waited for ever.
handlers['secscore:state'](state({ routerId: 'r3', score: 44, issues: 7 }));
assert.strictEqual(n['dc-secVal'].textContent, '55', 'a frame for a router not yet selected was drawn');
handlers['router:switched']({ activeId: 'r3' });
assert.strictEqual(n['dc-secVal'].textContent, '44', 'the new router\'s state, sent before the switch, was lost');
assert.strictEqual(n['dc-secLabel'].textContent, 'At risk · 7 issues');
// And back: r2's last state is drawn at once, not "waiting".
handlers['router:switched']({ activeId: 'r2' });
assert.strictEqual(n['dc-secVal'].textContent, '55', 'switching back did not draw r2\'s last state');

// RESCAN sends one request; OPEN goes to the page.
n['dc-secRescan'].fire('click');
assert.deepStrictEqual(sent, [['secscore:scan', {}]], 'Rescan did not send exactly one request');
assert.strictEqual(n['dc-secRescan'].disabled, true, 'Rescan can be pressed twice');
let went = '';
doc.querySelector = (sel) => (sel === '.nav-item[data-page="security-scan"]' ? { click: () => { went = 'security-scan'; } } : null);
n['dc-secOpen'].fire('click', { preventDefault: () => {} });
assert.strictEqual(went, 'security-scan', 'Open does not go to the Security Scan page');

console.log('ok  the Security Score card: report, progress, refusal, router switch, Rescan and Open');
fs.rmSync(OUT, { force: true });
