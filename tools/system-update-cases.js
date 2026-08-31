#!/usr/bin/env node
'use strict';
/**
 * Pin the system collector's UPDATE decision against the live module.
 *
 * ── WHY THIS PATH HAS NO COVERAGE AT ALL, MEASURED ──────────────────────────
 *
 * `system` has no golden — it fills a cache and emits from elsewhere, so
 * `make-golden` records it under "no snapshot payload". It has no Go unit test
 * either. And its fixture cannot reach the update path: replaying the capture
 * asks five commands and the fixture holds three.
 *
 *   asked:    /system/resource/print, /system/routerboard/print,
 *             /system/license/print, /system/package/update/check-for-updates,
 *             /system/package/update/print
 *   captured: /system/resource/print, /system/license/print,
 *             /system/routerboard/print
 *
 * `nodecheck`'s KNOWN_INCOMPLETE blamed "routerboard, license and the update
 * check" as un-awaited follow-ups from `_processRow`. Two of those three ARE
 * captured, so that was the wrong reads and the wrong cause. The real one is
 * narrower and unfixable by any settle window: `check-for-updates` contacts
 * MikroTik's UPSTREAM server, and the live code races it against a
 * **15-second** timeout (`system.js:209`). No capture waits that long.
 *
 * So the whole update path — `latestVersion`, `updateStatus`, `updateChannel`
 * and the availability verdict — was covered by nothing. This is that cover.
 *
 * ── THE TWO DECISIONS, AND WHY EACH IS EASY TO GET WRONG ────────────────────
 *
 * `_isUpdateAnswer` decides whether a row is a real ANSWER or the router still
 * thinking. Getting it wrong caches "checking for updates" as the result, and
 * the shared per-router slot then serves that to every session built later.
 *
 * The availability rule has two branches and they disagree about what "no
 * information" means: with a `latest-version` it is a STRING INEQUALITY against
 * the installed base, and without one it falls back to sniffing the status text
 * for "new version". A port that only implemented the first branch reports "up
 * to date" for a router that has told it otherwise in words.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/system-update-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');

const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT = path.join(__dirname, '..', 'testdata', 'system-update-cases.json');
const CHECK = process.argv.includes('--check');

const SystemCollector = require(path.join(SRC, 'src', 'collectors', 'system.js'));
assert.strictEqual(typeof SystemCollector._isUpdateAnswer, 'function',
  '_isUpdateAnswer is no longer a static on SystemCollector');

// The availability rule, LIFTED from _applyUpdateRow by its two literal
// anchors rather than retyped — it is three lines and reimplementing them here
// would test the reimplementation.
const src = fs.readFileSync(path.join(SRC, 'src', 'collectors', 'system.js'), 'utf8');
const START = "const latestVersion   = u['latest-version'] || '';";
const END = '// `channel` comes back in this same row';
const from = src.indexOf(START);
assert.ok(from > 0, 'the availability rule has moved in system.js');
const to = src.indexOf(END, from);
assert.ok(to > from && to - from < 800, 'the rule is not where its anchors say');
const block = src.slice(from, to);
for (const must of ['latestVersion', 'installedBase', "includes('new version')"]) {
  assert.ok(block.includes(must), 'the lifted rule lost: ' + must);
}
for (const mustNot of ['updateChannel', 'this.lastPayload =', 'io.emit']) {
  assert.ok(!block.includes(mustNot), 'the slice over-read and took in: ' + mustNot);
}
// `this.lastPayload.version` is the only instance reference in the slice; it is
// supplied as a bare local so the block runs outside a collector.
const run = new Function('u', 'lastPayload',
  'const self = { lastPayload };\n' + block.replace(/this\.lastPayload/g, 'self.lastPayload') +
  '\n return { latestVersion, updateStatus, installedBase, updateAvailable };');

const ROWS = [
  { name: 'a plain newer version', row: { 'latest-version': '7.25', status: 'New version is available' }, version: '7.24' },
  { name: 'the SAME version installed', row: { 'latest-version': '7.24', status: 'System is already up to date' }, version: '7.24' },
  { name: 'an OLDER latest than installed is still "available" — it is inequality, not ordering',
    row: { 'latest-version': '7.23', status: '' }, version: '7.24' },
  { name: 'no latest-version, status says a new version exists',
    row: { status: 'New version is available' }, version: '7.24' },
  { name: 'no latest-version, status in MIXED case', row: { status: 'NEW VERSION IS AVAILABLE' }, version: '7.24' },
  { name: 'no latest-version, status says up to date', row: { status: 'System is already up to date' }, version: '7.24' },
  { name: 'no latest-version and no status at all', row: {}, version: '7.24' },
  { name: 'the router is still finding out', row: { status: 'finding out latest version' }, version: '7.24' },
  { name: 'the router is checking', row: { status: 'checking for updates' }, version: '7.24' },
  { name: 'a check in progress', row: { status: 'update in progress' }, version: '7.24' },
  // The installed version carries a channel suffix that must be stripped before
  // the comparison — `7.24 (stable)` is not a different version from `7.24`.
  { name: 'installed version with a (stable) suffix', row: { 'latest-version': '7.24', status: '' }, version: '7.24 (stable)' },
  { name: 'installed version with a (testing) suffix and a newer latest',
    row: { 'latest-version': '7.25', status: '' }, version: '7.24 (testing)' },
  { name: 'installed version with surrounding whitespace', row: { 'latest-version': '7.24', status: '' }, version: '  7.24  ' },
  { name: 'no installed version known yet', row: { 'latest-version': '7.25', status: '' }, version: '' },
  { name: 'a channel is carried through', row: { 'latest-version': '7.25', status: '', channel: 'testing' }, version: '7.24' },
  { name: 'an empty channel', row: { 'latest-version': '7.25', status: '', channel: '' }, version: '7.24' },
];

const cases = ROWS.map((c) => ({
  name: c.name,
  row: c.row,
  version: c.version,
  isAnswer: SystemCollector._isUpdateAnswer(c.row),
  ...run(c.row, { version: c.version }),
}));

// Rows that are not objects at all — `_isUpdateAnswer` guards for them, and the
// guard is the reason a failed check does not poison the shared slot.
for (const [name, u] of [['null', null], ['undefined', undefined], ['a string', 'nope'], ['a number', 7]]) {
  cases.push({ name: 'not a row: ' + name, row: u === undefined ? null : u, version: '7.24',
    isAnswer: SystemCollector._isUpdateAnswer(u), notARow: true });
}

// BELIEVABILITY, on both decisions separately.
const answers = cases.filter((c) => c.isAnswer).length;
if (!answers || answers === cases.length) throw new Error('isAnswer is constant across the corpus');
const avail = cases.filter((c) => c.updateAvailable === true).length;
const unavail = cases.filter((c) => c.updateAvailable === false).length;
if (!avail || !unavail) throw new Error('the availability verdict is constant across the corpus');
// And the corpus must separate the STATUS-TEXT branch from the version branch:
// a port implementing only the first would agree everywhere else.
const textOnly = cases.filter((c) => !c.notARow && !c.latestVersion && c.updateAvailable);
if (!textOnly.length) throw new Error('no case reaches the status-text branch with a positive verdict');

const text = JSON.stringify({ generatedFrom: 'src/collectors/system.js', cases }, null, 2) + '\n';
if (CHECK) {
  const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (cur !== text) { console.error('system-update-cases.json is STALE — run: node tools/system-update-cases.js'); process.exit(1); }
  console.log(`system-update-cases.json up to date (${cases.length} cases, ${answers} answers, ${avail} available)`);
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, text);
  console.log(`wrote ${cases.length} cases (${answers} answers, ${avail} available, ` +
    `${textOnly.length} via the status-text branch) -> ${path.relative(process.cwd(), OUT)}`);
}
