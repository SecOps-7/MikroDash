'use strict';
/**
 * The three client rules upstream recorded with `packages:notes`.
 *
 * `wiring-audit`'s `upd_notes` entry carried them for a day before the event was
 * ported — "three client rules worth taking from upstream rather than
 * rediscovering". This is what took them.
 *
 *   1. ASK ON MODAL OPEN, never on the per-tick update event. The
 *      update-available path fires on every poll, and an unconditional rebuild
 *      there is what made the update strip flash. Nobody who never opens the
 *      dialog should cost a fetch of a third-party URL.
 *   2. DISCARD a reply whose version is not the one on screen, or switching
 *      routers with the dialog open paints the previous router's changelog under
 *      the new router's numbers.
 *   3. ESCAPE BEFORE INSERTION — it is the only third-party content this app
 *      renders into the DOM.
 *
 * Rule 2 is a pure function and is driven directly. Rules 1 and 3 are about
 * WHERE code sits, so they are source checks — anchored on structure, not on a
 * character window.
 *
 *   node tools/notes-rules-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const OUT = path.join(ROOT, 'testdata', '.notes-rules-port.cjs');
const SRC = fs.readFileSync(path.join(ROOT, 'web', 'src', 'pages', 'upgrade.ts'), 'utf8');

const problems = [];

// ── Rule 2, driven ──────────────────────────────────────────────────────────
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'upgrade.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
const m = require(OUT);

const CASES = [
  ['the reply is for the open dialog', '7.24', '7.24', true],
  ['a reply for the PREVIOUS router\'s version', '7.24', '6.49.18', false],
  ['no dialog is open', '', '7.24', false],
  ['no dialog is open and the reply has no version', '', '', false],
  ['a reply with no version while one is open', '7.24', '', false],
  ['a reply whose version is not a string', '7.24', 42, false],
  ['a reply whose version is null', '7.24', null, false],
  // A LOOSE COMPARISON WOULD PASS THIS. '7.24' != 7.24 only under ===.
  ['a numeric version that would coerce equal', '7.24', 7.24, false],
];
let painted = 0;
for (const [why, showing, reply, want] of CASES) {
  const got = m.notesAreForThisDialog(showing, reply);
  if (got) painted++;
  if (got !== want) problems.push(`rule 2 — ${why}: got ${got}, want ${want}`);
}
if (painted === 0) problems.push('rule 2: no case paints; the corpus agrees with `false`');
if (painted === CASES.length) problems.push('rule 2: every case paints; the guard is not exercised');

// ── Rule 1: asked on OPEN, not on the update-available path ─────────────────
const body = SRC.replace(/\/\*[\s\S]*?\*\//g, ' ').replace(/\/\/[^\n]*/g, ' ');
const openAt = body.indexOf("closest('#sysUpdateBtn')");
if (openAt < 0) {
  problems.push('rule 1: the modal-open branch could not be found');
} else {
  const openBranch = body.slice(openAt, body.indexOf('return;', openAt));
  if (!/packages:notes/.test(openBranch)) {
    problems.push('rule 1: the modal-open branch does not request the notes');
  }
}
// The update-available listener must NOT.
const tickAt = body.indexOf('mikrodash:updateavailable');
if (tickAt >= 0) {
  const tickBranch = body.slice(tickAt, tickAt + 400);
  if (/packages:notes/.test(tickBranch)) {
    problems.push('rule 1: the update-available path requests the notes. It fires on EVERY poll '
      + 'tick, so this would fetch a third-party URL repeatedly for a dialog nobody opened.');
  }
}
// And exactly one place emits it.
const emits = (body.match(/emit\('packages:notes'/g) || []).length;
if (emits !== 1) {
  problems.push(`rule 1: packages:notes is emitted from ${emits} places; there is exactly one — `
    + 'the modal-open branch');
}

// ── Rule 3: escaped before insertion ────────────────────────────────────────
const setAt = body.indexOf('const setNotes');
if (setAt < 0) {
  problems.push('rule 3: setNotes could not be found');
} else {
  const setBody = body.slice(setAt, body.indexOf('\n  };', setAt));
  if (/innerHTML/.test(setBody) && !/esc\(/.test(setBody)) {
    problems.push('rule 3: setNotes writes innerHTML without esc(). This is the ONLY third-party '
      + 'content this app renders into the DOM — fetched from mikrotik.com, not produced by this '
      + 'app or a router.');
  }
}

fs.rmSync(OUT, { force: true });
if (problems.length) {
  console.error('notes-rules-check FAILED:');
  for (const p of problems) console.error('  - ' + p);
  process.exit(1);
}
console.log(`notes-rules-check: rule 2 agrees on ${CASES.length} cases (${painted} paint), and `
  + 'rules 1 and 3 hold in the source');
