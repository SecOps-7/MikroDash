'use strict';
/**
 * A page is mounted by THREE lists agreeing. The Dashboard was in one of them.
 *
 * ---- THE DEFECT --------------------------------------------------------
 *
 * Mounting a page takes three things, and nothing checked that they matched:
 *
 *   1. `web/src/ui/page-<key>.html`      the extracted body
 *   2. `cmd/webbuild`'s PAGES               composes that body into index.html
 *   3. `main.ts`'s PORTED                lets the nav route to it
 *
 * The Dashboard had (1) — 43 KB of extracted markup carrying `#page-dashboard` —
 * and neither (2) nor (3). So its body was never in the document, and its
 * nineteen modules initialised at boot against elements that did not exist,
 * every one returning early in silence. The nav, meanwhile, treated it as
 * unported and sent the browser to Node; in standalone that redirect came back
 * to the app and landed on the default page, which the operator reported as
 * "unable to go to the Dashboard page, it just sends me back".
 *
 * ---- WHY THE EXISTING AUDITS COULD NOT SEE IT --------------------------
 *
 * `module-reachability-audit` asks whether a MODULE is imported — the dashboard
 * modules were. `wiring-audit` asks whether the ids of a PORTED page have
 * writers — the dashboard was not in PORTED, so it was never asked about.
 * `dash-coverage-check` measured 109 of 122 ids with a writer and was right:
 * the writers existed. **Reachability of code says nothing about the presence of
 * the thing it acts on.**
 *
 *   node tools/page-mount-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');

/** Extracted but deliberately NOT mounted, with what blocks each. */
// `settings` WAS here — "ONE UNIT, and it lands at cutover … 54 elements, every
// one a write or a send". Closed 2026-08-29, and the entry was wrong in three
// ways by the end, each worth recording because each is a shape this project
// keeps finding:
//
//   THE COUNT. Measured by mounting it and reading the audit: FIFTEEN, not 54.
//   Most of the page had been ported in the year between the note being written
//   and being read, and nothing recounted.
//
//   "EVERY ONE A WRITE OR A SEND." The fifteen were the poll sliders, the
//   banner, Reset, the routers table, the alert filters and the four Test
//   buttons. Only the last four send, and they are operator-initiated one-shots
//   rather than the alerter's automatic duplicates that blocker 5 is about.
//
//   "WOULD REPLACE A WORKING PROXIED PAGE." There is no proxy any more. That
//   premise expired when the port went standalone, and it is the load-bearing
//   half of the argument: not mounting Settings now means the app has NO
//   settings page, which is strictly worse than a partially-writable one.
//
// The settings WRITES are still cutover-gated, and that has not changed — the
// page mounting and its Save button being live are two different questions.
const NOT_MOUNTED = {};

// THE PAGES LIST MOVED TO GO on 2026-08-31, when `web/build.mjs` was replaced by
// `cmd/webbuild` and the build stopped needing Node. Same list, same job — it is
// what composes each page body into index.html — so this audit follows it there.
const build = fs.readFileSync(path.join(ROOT, 'cmd', 'webbuild', 'main.go'), 'utf8');
const main = fs.readFileSync(path.join(ROOT, 'web', 'src', 'main.ts'), 'utf8');

function listFrom(src, re, what) {
  const m = re.exec(src);
  if (!m) throw new Error(`could not read ${what} — the declaration shape changed`);
  const out = new Set([...m[1].matchAll(/['"]([a-z]+)['"]/g)].map((x) => x[1]));
  if (out.size < 15) throw new Error(`${what} holds only ${out.size} entries; the match broke`);
  return out;
}

const composed = listFrom(build, /var PAGES = \[\]string\{([\s\S]*?)\}/, "cmd/webbuild's PAGES");
const ported = listFrom(main, /const PORTED = new Set\(\[([\s\S]*?)\]\)/, "main.ts's PORTED");

const extracted = new Set(
  fs.readdirSync(path.join(ROOT, 'web', 'src', 'ui'))
    .filter((f) => /^page-[a-z]+\.html$/.test(f))
    .map((f) => f.slice('page-'.length, -'.html'.length)));

const problems = [];

// ── The three must agree, or a page is half-mounted ────────────────────────
for (const key of [...extracted].sort()) {
  const inBuild = composed.has(key);
  const inPorted = ported.has(key);
  if (inBuild && inPorted) continue;
  if (!inBuild && !inPorted) {
    if (!NOT_MOUNTED[key]) {
      problems.push(`page-${key}.html is extracted and mounted NOWHERE. Either mount it in both `
        + "build.mjs's PAGES and main.ts's PORTED, or record it in NOT_MOUNTED with what blocks it.");
    }
    continue;
  }
  problems.push(`${key} is HALF-MOUNTED: ${inBuild ? 'composed into index.html' : 'NOT composed'}`
    + ` but ${inPorted ? 'in PORTED' : 'NOT in PORTED'}.\n`
    + '    Composed without PORTED = the body is in the document and the nav refuses to route to '
    + 'it (the Dashboard, until 2026-08-28).\n'
    + '    PORTED without composed = the nav routes to a page whose markup is not there, and every '
    + 'module writing to it no-ops in silence.');
}

// ── A page cannot be mounted without a body ────────────────────────────────
for (const key of [...composed].sort()) {
  if (!extracted.has(key)) {
    problems.push(`build.mjs composes '${key}' and web/src/ui/page-${key}.html does not exist`);
  }
}
for (const key of [...ported].sort()) {
  if (!extracted.has(key)) {
    problems.push(`main.ts routes to '${key}' and web/src/ui/page-${key}.html does not exist`);
  }
}

// ── The ledger's other direction ───────────────────────────────────────────
for (const key of Object.keys(NOT_MOUNTED)) {
  if (!extracted.has(key)) {
    problems.push(`${key} is in NOT_MOUNTED but has no extracted body — remove the entry`);
  } else if (composed.has(key) || ported.has(key)) {
    problems.push(`${key} is in NOT_MOUNTED and IS mounted now — delete the entry rather than `
      + 'leaving a note that describes a state the code is no longer in.');
  }
}

if (problems.length) {
  console.error('page-mount-audit FAILED:');
  for (const p of problems) console.error('  - ' + p);
  process.exit(1);
}
console.log(`page-mount-audit: ${extracted.size} extracted bodies — `
  + `${composed.size} fully mounted, ${Object.keys(NOT_MOUNTED).length} recorded as blocked`);
