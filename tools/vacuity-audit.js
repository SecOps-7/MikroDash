'use strict';
/**
 * DOES EACH GATE STILL CHECK AS MUCH WITHOUT THE REFERENCE AS WITH IT?
 *
 * ── WHY THIS IS NOT THE GATE CENSUS ─────────────────────────────────────────
 *
 * The census ratchets the LARGEST number a gate has ever printed and fails when
 * it shrinks. That catches a gate losing coverage over time. It cannot catch a
 * gate that checks less in ONE CONDITION than the other, because it only ever
 * sees one run.
 *
 * This runs every gate BOTH WAYS and compares what each SAYS it checked. Three
 * real instances were found by hand before this existed:
 *
 *   - `grid-drag-check`   76 comparisons with a reference, 67 without — a guard
 *                         had been copied onto a block that needed none.
 *   - `sched-runs-check`  printed nothing at all without one; an async `main()`
 *                         swallowed the assertion that would have said so.
 *   - `stub-audit`        37 shadowing definitions with, 0 without — its filter
 *                         set was empty, so every finding was dismissed.
 *
 * All three PASSED. A gate that agrees with itself about nothing is the failure
 * this whole conversion risks, and it is invisible to every other check here.
 *
 * ── WHAT COUNTS AS A COUNT ──────────────────────────────────────────────────
 *
 * Whatever the gate prints about its own work: "N cases", "N comparisons",
 * "N steps", "N scenarios", and so on. That is the gate's own claim, which is
 * the only thing worth comparing — a gate is free to say nothing, but if it says
 * a number it must say the same number either way.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/vacuity-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const TOOLS = path.join(ROOT, 'tools');
const LIVE = process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash');

if (!fs.existsSync(LIVE)) {
  console.log('vacuity-audit: no reference at %s — this audit compares the two ' +
              'conditions and needs both. Skipped.', LIVE);
  process.exit(0);
}

const COUNT = /\b(\d+)\s+(cases?|comparisons?|steps?|scenarios?|tabs?|ids?|pages?|definitions?|tools?|checks?)\b/g;

function run(file, src) {
  try {
    return execFileSync(process.execPath, [file], {
      cwd: ROOT, encoding: 'utf8', stdio: 'pipe',
      env: Object.assign({}, process.env, { MIKRODASH_SRC: src }),
    });
  } catch (e) {
    return (e.stdout || '') + (e.stderr || '');
  }
}

/** The gate's own claims, as {noun: number}. */
function claims(out) {
  const m = {};
  for (const x of out.matchAll(COUNT)) {
    const noun = x[2].replace(/s$/, '');
    m[noun] = Math.max(m[noun] || 0, Number(x[1]));
  }
  return m;
}

/**
 * Gates whose subject IS the reference-lifting machinery, so checking less
 * without a reference is correct rather than a defect.
 *
 * DECLARED, NOT SKIPPED: the audit fails if one of these stops being thin, so
 * the exemption cannot outlive its reason — the same shape as `KNOWN_INCOMPLETE`
 * in `nodecheck/`.
 */
const EXPECTED_THIN = {
  // EMPTY, and that is the finished state. `lift-audit.js` was the only entry:
  // it checked that `live-renderer.js` could still lift each page, and
  // live-renderer lifts FROM the reference, so it had no subject without one. It
  // was retired on 2026-08-31 with the other five — see
  // `docs/port-history/retired/README.md`.
  //
  // Kept as a mechanism rather than deleted: the next gate that legitimately
  // cannot check the same either way needs somewhere to say so, and an audit
  // with no way to declare an exception gets switched off instead.
};

const files = fs.readdirSync(TOOLS)
  .filter((f) => /-(check|audit)\.js$/.test(f) && f !== 'vacuity-audit.js')
  .sort();

const thin = [];
const stale = [];
let compared = 0;
for (const f of files) {
  const p = path.join(TOOLS, f);
  const a = claims(run(p, LIVE));
  const b = claims(run(p, '/nonexistent'));
  const nouns = new Set([...Object.keys(a), ...Object.keys(b)]);
  if (!nouns.size) continue;          // says no numbers; nothing to compare
  compared++;
  let isThin = false;
  for (const n of nouns) {
    const x = a[n] || 0, y = b[n] || 0;
    if (y < x) { isThin = true; if (!EXPECTED_THIN[f]) thin.push({ f, n, with: x, without: y }); }
  }
  if (EXPECTED_THIN[f] && !isThin) {
    stale.push(f);
  }
}

// AN EXEMPTION THAT IS NO LONGER NEEDED IS A LIE. If one of these starts
// checking the same either way, the note explaining why it could not has to go.
if (stale.length) {
  console.error('vacuity-audit: %d exemption(s) are stale — these now check the same '
    + 'either way, so remove them from EXPECTED_THIN:\n', stale.length);
  for (const f of stale) console.error('  %s', f);
  process.exit(1);
}

if (!compared) {
  console.error('vacuity-audit: no gate printed a count — the detector is broken');
  process.exit(1);
}
if (thin.length) {
  console.error('vacuity-audit: %d gate(s) check LESS without the reference\n', thin.length);
  for (const t of thin) {
    console.error('  %s: %d %s with the reference, %d without', t.f, t.with, t.n, t.without);
  }
  console.error('\n  A gate that passes while checking less is worse than one that fails.');
  console.error('  Usually a guard on a block that needed none, or a lifted VALUE that was');
  console.error('  guarded instead of frozen — see LOOP.md 3ab.');
  process.exit(1);
}
console.log('vacuity-audit: %d gate(s) print a count and all check the same either way '
            + '(%d declared exception%s)', compared, Object.keys(EXPECTED_THIN).length,
            Object.keys(EXPECTED_THIN).length === 1 ? '' : 's');
