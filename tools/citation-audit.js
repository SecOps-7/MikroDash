'use strict';
/**
 * EVERY SOURCE PATH CITED IN A NOTE STILL EXISTS.
 *
 * ── THE DECAY THIS CATCHES, AND THE LARGER ONE IT DOES NOT ─────────────────
 *
 * Four consecutive ticks (Parts 121–124) each found a record that had outlived
 * its problem: a button described as dead that works, a drift TODO whose tests
 * no longer exist, a version number, two mis-filed ids, a comment's reasoning,
 * and a claim that a 692-line ported page was "unported". None was a code
 * defect. All would have cost a future session real time, and two were sitting
 * in the list of things blocking cutover.
 *
 * Most of that cannot be automated: an entry whose CONCLUSION stays true while
 * its REASON goes stale reads correctly to any checker. What CAN be automated is
 * the narrowest slice — a note citing a file that no longer exists. A rename
 * leaves every note that named it pointing at nothing, and nothing else notices.
 *
 * 200 paths are cited across `tools/`, `PORT-QUEUE.md` and `CLAUDE.md`, and all
 * 200 exist today. This is here to keep that true, not because it is failing.
 *
 * ── WHAT IT DELIBERATELY DOES NOT DO ────────────────────────────────────────
 *
 * It does not check that the file still CONTAINS what the note claims. That is
 * the interesting half and it needs a reader, not a script. The header of each
 * ledger says so; this only stops the cheapest form of rot.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/citation-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');

const SOURCES = [
  ...fs.readdirSync(path.join(ROOT, 'tools'))
    .filter((f) => f.endsWith('.js'))
    .map((f) => path.join('tools', f)),
  'CLAUDE.md',
  'Changes.md',
];

// `docs/port-history/PORT-QUEUE.md` was scanned until the 2026-08-31 cutover and
// is deliberately not any more.
//
// The premise of this audit is that a note citing a missing file has gone stale.
// That holds for LIVE documentation. `docs/port-history/` is a RECORD of a job
// that finished — CLAUDE.md calls it "a record rather than a queue" — and the
// cutover deleted the entire Node implementation those notes were written about.
// A record of the past is SUPPOSED to name things that no longer exist; marking
// that as decay would either produce permanent noise or, worse, invite someone
// to edit the history so the checker goes quiet.
//
// The first casualty was real and is the worked example: PORT-QUEUE.md cites the
// hook selftest under tools/, which pinned the contract of a hook that guarded
// the old reference repo. Both were correct to delete, and the note describing
// them is correct to keep.
//
// Note the path above is deliberately NOT backticked. The regex below scans
// every file in tools/ including this one, so writing the citation the way the
// checker recognises would make this comment fail the check it exists to
// explain.

// Backticked paths under a directory this repo owns. Anything under
// `../MikroDash` is deliberately excluded: that tree is not ours and moves on
// its own schedule, and a note about it going stale is a different problem.
const CITE = /`((?:web\/src|internal|tools|nodecheck|cmd|testdata)\/[A-Za-z0-9_./-]+\.(?:ts|go|js|mjs|json))`/g;

// `testdata/fixtures/.../vpn.json` is PROSE — an ellipsis standing in for a
// router directory nobody wants to type. The first version of this audit
// reported four of them as missing files, which is a checker inventing work.
// A path containing `...` is illustrative, not a citation.
const isIllustrative = (p) => p.includes('...');

const cited = new Map();
for (const rel of SOURCES) {
  const abs = path.join(ROOT, rel);
  if (!fs.existsSync(abs)) continue;
  const body = fs.readFileSync(abs, 'utf8');
  for (const m of body.matchAll(CITE)) {
    if (isIllustrative(m[1])) continue;
    if (!cited.has(m[1])) cited.set(m[1], new Set());
    cited.get(m[1]).add(rel);
  }
}

// Paths cited that are EXPECTED not to exist — a note describing something that
// was deleted, or a file a future part will add. Each needs a reason.
const EXPECTED_ABSENT = {};

const problems = [];
for (const [p, where] of [...cited].sort()) {
  const exists = fs.existsSync(path.join(ROOT, p));
  if (exists && EXPECTED_ABSENT[p]) {
    problems.push(p + ' is recorded as absent but exists now — remove the entry');
  } else if (!exists && !EXPECTED_ABSENT[p]) {
    problems.push(p + ' is cited by ' + [...where].join(', ') + ' and does not exist');
  }
}
for (const p of Object.keys(EXPECTED_ABSENT)) {
  if (!cited.has(p)) problems.push(p + ' is recorded but nothing cites it — remove the entry');
}

if (problems.length) {
  shout('citation-audit: %d problem(s)\n', problems.length);
  for (const p of problems) shout('  - ' + p);
  process.exit(1);
}
say('citation-audit: %d source paths cited across %d notes, all present',
  cited.size, SOURCES.filter((s) => fs.existsSync(path.join(ROOT, s))).length);
