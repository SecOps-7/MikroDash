'use strict';
/**
 * A client function the APPLICATION never uses, kept alive by its own gate.
 *
 * ---- THE SHAPE ------------------------------------------------------------
 *
 * `web/src/stale.ts` exported `applyCollectionConfig` and `applyCollectionStatus`.
 * Both were complete, both reproduced the live behaviour, and both were pinned by
 * `tools/stale-check.js`, which drives them against the live implementation and
 * passes. Nothing else referenced either one. The port's server emitted neither
 * of the events that feed them — `collection:config` and `collection:status` —
 * so the gate proved the functions correct and said nothing about whether they
 * run.
 *
 * That is the netwatch failure (`internal/session/lifecycle_test.go`: "'present
 * in the source' and 'reachable at runtime' are different claims"), and the
 * differentially-gated version is the worst of it, because the gate is what makes
 * the code look covered.
 *
 * Both were closed on 2026-08-28 — by this audit, which failed the moment each
 * consumer gained a caller and named the stale entry.
 *
 * ---- REFERENCES, NOT CALLS (corrected 2026-08-28) -------------------------
 *
 * This looked for `name(`. A scan built on that rule reported `drawDonutCentre`
 * as orphaned, and it is not: it is passed as a value —
 * `plugins: [{ afterDraw: drawDonutCentre }]` — which is a perfectly good use. A
 * rule that cannot see a function used as a callback produces false alarms, and a
 * false alarm is how an audit gets ignored. It now counts REFERENCES.
 *
 * ---- WHY THIS STILL READS ONE GATE, MEASURED ------------------------------
 *
 * Extending it to all 116 `*-check.js` gates was tried and abandoned, on
 * measurements rather than taste:
 *
 *   - 27 gates bundle a module out of `web/src`; the rest compare markup, drive
 *     the live implementation only, or read source.
 *   - Of those, the `m.<name>` handle this audit reads is stale-check's own
 *     convention. Others destructure, or name the module something else, and
 *     only EIGHT driven exports resolve across all 27.
 *   - A targeted scan of every gated export found ZERO orphans today.
 *
 * So the general version needs per-gate handle parsing for no present yield, and
 * a half-working version with invented floors would be worse than this one. The
 * measurements are recorded so the next attempt starts from them.
 *
 * `tools/module-reachability-audit.js` covers the other half of the class — a
 * whole MODULE nothing imports, which this cannot see because its functions
 * reference each other.
 *
 *   node tools/orphaned-consumer-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');
const SRC = path.join(ROOT, 'web', 'src');

/**
 * Orphaned ON PURPOSE, with the reason and what would close it.
 *
 * EMPTY, AND THAT IS THE POINT. Both original entries were closed on 2026-08-28.
 * The file stays because the CHECK is what matters, not the list: the next
 * differentially-gated function written before its emit fails here rather than
 * looking covered.
 */
const KNOWN_ORPHANED = {};

/** The functions the stale gate drives, read from the gate rather than listed. */
function gatedFunctions() {
  const src = fs.readFileSync(path.join(ROOT, 'tools', 'stale-check.js'), 'utf8');
  const names = new Set();
  for (const m of src.matchAll(/\bm\.([A-Za-z_$][\w$]*)\s*\(/g)) names.add(m[1]);
  if (names.size === 0) {
    throw new Error('read no driven functions out of tools/stale-check.js — the '
      + '`m.<name>(` shape it used changed, and this audit is now checking nothing');
  }
  return [...names].sort();
}

/**
 * Comments removed, because this file's own doc comments mention the functions
 * it checks.
 *
 * Without this, `applyCollectionConfig` counted as "referenced" by the paragraph
 * in `stale.ts` explaining what it does — so the mutation that deleted its only
 * real caller SURVIVED. A rule that reads prose as usage cannot tell wired from
 * documented, and everything this audit exists for lives in that gap.
 *
 * Crude on purpose: it is not a parser, and a `//` inside a string literal would
 * be over-cut. That direction is safe here — over-cutting can only make a
 * function look LESS used, which fails loudly, where under-cutting passes
 * quietly.
 */
function stripComments(text) {
  return text.replace(/\/\*[\s\S]*?\*\//g, ' ').replace(/\/\/[^\n]*/g, ' ');
}

/** Every .ts under web/src, with its text and its comments stripped. */
function sources() {
  const out = [];
  const walk = (dir) => {
    for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
      const p = path.join(dir, e.name);
      if (e.isDirectory()) walk(p);
      else if (e.name.endsWith('.ts')) {
        out.push({ file: path.relative(ROOT, p), text: stripComments(fs.readFileSync(p, 'utf8')) });
      }
    }
  };
  walk(SRC);
  return out;
}

const files = sources();

/**
 * Where `name` is REFERENCED, its declaration removed first.
 *
 * The declaration is cut out and everything left that still names the function
 * counts — a call, a callback, an export list. Skipping the whole defining file
 * was the first version of this and it was wrong: `sweepStale` is used by
 * `startStaleSweep` eleven lines below its own declaration, and the audit
 * reported it as orphaned.
 */
function usersOf(name) {
  const decl = new RegExp(
    `export\\s+(?:async\\s+)?function\\s+${name}\\b|export\\s+const\\s+${name}\\s*[:=]`, 'g');
  const use = new RegExp(`\\b${name}\\b`);
  return files.filter((f) => use.test(f.text.replace(decl, 'DECL_REMOVED'))).map((f) => f.file);
}

const gated = gatedFunctions();
const problems = [];

for (const name of gated) {
  const used = usersOf(name);
  const known = Object.prototype.hasOwnProperty.call(KNOWN_ORPHANED, name);
  if (used.length === 0 && !known) {
    problems.push(`${name} is driven by tools/stale-check.js and referenced NOWHERE in web/src.\n`
      + '    The gate proves it correct and says nothing about whether it runs. Either wire it,\n'
      + '    or add it to KNOWN_ORPHANED with the reason and what would close it.');
  }
  if (used.length > 0 && known) {
    problems.push(`${name} is now referenced by ${used.join(', ')} — the KNOWN_ORPHANED entry is `
      + 'stale and must be deleted. A gap that is documented and no longer true teaches nothing.');
  }
}

// A ledger naming something the gate no longer drives is describing code that is gone.
for (const name of Object.keys(KNOWN_ORPHANED)) {
  if (!gated.includes(name)) {
    problems.push(`${name} is in KNOWN_ORPHANED but tools/stale-check.js no longer drives it — `
      + 'remove it, or the ledger is describing code that is gone.');
  }
}

if (problems.length) {
  console.error('orphaned-consumer-audit FAILED:');
  for (const p of problems) console.error('  - ' + p);
  process.exit(1);
}
console.log(`orphaned-consumer-audit: ${gated.length} gated function(s), `
  + `${Object.keys(KNOWN_ORPHANED).length} orphaned on purpose and still orphaned`);
