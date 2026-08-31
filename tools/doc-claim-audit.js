#!/usr/bin/env node
'use strict';
/**
 * THE MEASURED NUMBERS IN `CLAUDE.md`, RE-MEASURED.
 *
 * ── THE DIRECTION NOBODY WATCHES ──────────────────────────────────────────
 *
 * `CLAUDE.md` opens by telling a reader to distrust it: "Every count names the
 * file it was measured from, so re-check rather than trust", "Check the map, not
 * the prose", "ask the test, never a grep". Every one of those warnings exists
 * because a number in that file had already gone stale and cost a session.
 *
 * Nothing re-measured them. On 2026-08-30 three were wrong at once:
 *
 *   - `page-gate-audit` was quoted as "68 of 70; 2 recorded as gaps". It is
 *     85 of 85 with none ungated — the prose understated finished work, so a
 *     session could have gone looking for two gaps that had been closed.
 *   - `hook-selftest.js` was described as "16 cases: 8 writes it must catch and
 *     8 reads it must stay silent about". That was the contract BEFORE the
 *     operator disabled the hook on 2026-08-23. The selftest kept asserting the
 *     old one, printed "17 of 32 hook cases wrong", exited 1, and nobody saw.
 *   - "Three generators need the live `better-sqlite3`" sat two paragraphs above
 *     a note saying verify.sh "now COUNTS that list rather than saying 3". One
 *     comment contradicting itself; the real number is 12.
 *
 * `cutover-premise-audit.js` watches facts about the LIVE source and
 * `tools/verify.sh`'s census watches gates going blind. This is the third
 * direction: the port's own documentation making a claim nobody checks.
 *
 * ── THE CLAIM LIST IS HAND-WRITTEN ON PURPOSE ─────────────────────────────
 *
 * A regex sweep for "every number in the file" would be unmaintainable and would
 * flag prose. Each entry names WHERE the claim is, HOW to find it and HOW to
 * measure the truth — so adding one is deliberate, the way `portedGuards` is.
 *
 * ── AND A REWORDED CLAIM FAILS RATHER THAN PASSING ────────────────────────
 *
 * The trap this tool could fall into is its own subject matter: if a claim is
 * rewritten and the locator stops matching, a naive version finds nothing to
 * disagree with and reports success. So a locator that does not match is a
 * FAILURE, not a skip. That is the `checked < 2` guard from
 * `TestEveryAlertEvaluationReachesASink`, and the reason `tools/live-renderer.js`
 * needed a step two.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/doc-claim-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('doc-claim-audit');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const read = (f) => fs.readFileSync(path.join(ROOT, f), 'utf8');

const runTool = (rel) =>
  execFileSync(process.execPath, [path.join(ROOT, rel)], {
    encoding: 'utf8',
    env: { ...process.env, MIKRODASH_SRC: LIVE },
    maxBuffer: 32 * 1024 * 1024,
  });

const CLAIMS = [
  {
    label: 'page-gate-audit: page modules exercised by a gate',
    where: 'CLAUDE.md',
    // The comment beside the command in the navigation table.
    find: /page-gate-audit\.js\s+#\s*(\d+) of (\d+)/,
    measure() {
      const m = /page-gate-audit: (\d+) of (\d+) page modules/.exec(runTool('tools/page-gate-audit.js'));
      if (!m) throw new Error('page-gate-audit did not print its usual summary');
      return [m[1], m[2]];
    },
  },
  {
    label: 'verify.sh: generators that need the mikrodash container',
    where: 'CLAUDE.md',
    find: /\*\*(\d+)\*\* generators need `better-sqlite3`/,
    measure() {
      // COUNTED FROM `DOCKER_ONLY`, which is what verify.sh itself counts. A
      // number typed here would be the same mistake one layer down.
      const m = /^DOCKER_ONLY='([^']*)'/m.exec(read('tools/verify.sh'));
      if (!m) throw new Error('DOCKER_ONLY is gone from tools/verify.sh');
      return [String(m[1].trim().split(/\s+/).filter(Boolean).length)];
    },
  },
  {
    // ADDED 2026-08-31, because this claim had ALREADY drifted: CLAUDE.md said
    // "Five are in" while go.mod held seven. `maxminddb` arrived with the geo
    // migration and `esbuild` with the Node-free build, and neither edit touched
    // the sentence counting them.
    //
    // It is the cheapest possible claim to check — go.mod is a list — and it was
    // the one going stale, which is the usual shape: nobody re-reads a sentence
    // that was true when written.
    label: 'the Go dependency count',
    where: 'CLAUDE.md',
    find: /\n  (\w+) are in: `golang\.org\/x\/crypto`/,
    measure() {
      const mod = read('go.mod');
      const m = /require \(\n([\s\S]*?)\n\)/.exec(mod);
      if (!m) throw new Error('could not read the require block from go.mod');
      const n = m[1].split('\n').filter((l) => l.trim() && !l.includes('// indirect')).length;
      const words = ['zero', 'one', 'two', 'three', 'four', 'five', 'six', 'seven',
        'eight', 'nine', 'ten', 'eleven', 'twelve'];
      const w = words[n] || String(n);
      // CAPITALISED: the claim opens a sentence, and this audit compares strings.
      // Returning "seven" against a documented "Seven" reports a stale claim that
      // is in fact correct — a false alarm is how an audit gets ignored.
      return [w.charAt(0).toUpperCase() + w.slice(1)];
    },
  },
  {
    label: 'the live write guards: module count',
    where: 'CLAUDE.md',
    find: /~(\d+) lines across \*\*(\w+)\*\* modules/,
    // FROZEN — this claim is ABOUT the reference, so the measurement is a lifted
    // value rather than a question. Recording it keeps CLAUDE.md honest against
    // the last measured truth: editing the numbers in the doc still fails here
    // without a reference, which is the whole point of this audit.
    //
    // Guarding instead would have dropped the claim entirely, and a claim nobody
    // checks is exactly what this file exists to prevent.
    measure() {
      return G.value('the live write guards: lines and modules', () => {
        const dir = path.join(LIVE, 'src', 'routeros');
        const files = fs.readdirSync(dir).filter((f) => /Guard.*\.js$/.test(f));
        const lines = files.reduce(
          (n, f) => n + fs.readFileSync(path.join(dir, f), 'utf8').split('\n').length - 1, 0);
        const words = ['zero', 'one', 'two', 'three', 'four', 'five', 'six', 'seven', 'eight', 'nine'];
        return [String(lines), words[files.length] || String(files.length)];
      });
    },
    // "~960" is deliberately approximate; the MODULE COUNT is exact. A tolerance
    // here is not laziness — the claim itself is written as an approximation, and
    // failing on a one-line edit upstream would make this tool noise.
    compare(claimed, actual) {
      const near = Math.abs(Number(claimed[0]) - Number(actual[0])) <= Number(actual[0]) * 0.05;
      return near && claimed[1] === actual[1];
    },
  },
  {
    label: 'pdf metrics: measurements agreeing with pdfkit',
    where: 'CLAUDE.md',
    find: /agree to 2e-13 pt across (\d+)/,
    measure() {
      const doc = JSON.parse(read('testdata/pdf-metrics-cases.json'));
      const cases = Array.isArray(doc) ? doc : doc.cases;
      return [String(Array.isArray(cases) ? cases.length : Object.keys(cases).length)];
    },
  },
];

let bad = 0;
let checked = 0;
for (const c of CLAIMS) {
  const doc = read(c.where);
  const found = c.find.exec(doc);
  if (!found) {
    // A LOCATOR THAT MISSES IS A FAILURE. See the header: this is the exact
    // shape the tool exists to catch, and it would be absurd to have it here.
    console.error(
      `MISSING  ${c.where}: the claim "${c.label}" no longer matches its locator.\n` +
      `         Either the sentence was reworded (update this tool) or the claim was\n` +
      `         deleted (delete the entry). It is NOT ok to leave this unmatched:\n` +
      `         an unmatched claim is a claim nobody is checking.`);
    bad++;
    continue;
  }
  const claimed = found.slice(1);
  let actual;
  try {
    actual = c.measure();
  } catch (e) {
    console.error(`ERROR    ${c.label}: could not measure — ${e.message}`);
    bad++;
    continue;
  }
  checked++;
  const ok = c.compare ? c.compare(claimed, actual)
    : claimed.length === actual.length && claimed.every((v, i) => v === actual[i]);
  if (!ok) {
    console.error(
      `STALE    ${c.where}: ${c.label}\n` +
      `         says   ${claimed.join(' / ')}\n` +
      `         is     ${actual.join(' / ')}`);
    bad++;
  }
}

if (bad) {
  console.error(`\ndoc-claim-audit: ${bad} of ${CLAIMS.length} documented claims are wrong or unlocatable`);
  process.exit(1);
}
console.log(`doc-claim-audit: ${checked} documented claims in CLAUDE.md re-measured and true`);
