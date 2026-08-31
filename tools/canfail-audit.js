'use strict';
/**
 * CAN EVERY GATE ACTUALLY FAIL?
 *
 * ── WHY THIS EXISTS ─────────────────────────────────────────────────────────
 *
 * `sched-runs-check` spent an unknown period unable to report failure. It calls
 * an async `main()` and installs `process.on('unhandledRejection', () => {})` to
 * model a browser — the live Remove/Send handlers `fetch` without a `.catch`, so
 * the page really does reject with nobody listening, and the gate has to survive
 * that to compare what happened.
 *
 * That handler also swallowed the gate's OWN assertions. An AssertionError
 * anywhere asynchronous vanished: exit 0, no output, green sweep. Its two
 * siblings had it too.
 *
 * A gate that cannot fail is worse than no gate, because it is counted. The gate
 * census answers "did this gate check less than it used to"; this answers the
 * cruder question underneath it — "would this gate tell us if it failed at all".
 *
 * ── WHY IT IS TARGETED RATHER THAN EXHAUSTIVE ───────────────────────────────
 *
 * Injecting a throw into all ~136 gates costs a full sweep twice over. The
 * swallowing mechanisms are structural, so this tests the gates that HAVE such a
 * structure: an unhandledRejection handler, or an async entry point. Sixteen
 * gates matched that shape by static reading and only TWO actually swallowed —
 * which is the argument for testing rather than reporting the static hits.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/canfail-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const TOOLS = path.join(ROOT, 'tools');

/** Where to inject a throw so it runs: the entry point, whatever shape it has. */
const ENTRIES = [
  /^\(async \(\) => \{/m,
  /^\(async function[^{]*\{/m,
  /^async function main\(\) \{/m,
];

const candidates = fs.readdirSync(TOOLS)
  .filter((f) => /-(check|audit)\.js$/.test(f) && f !== 'canfail-audit.js')
  .filter((f) => {
    const s = fs.readFileSync(path.join(TOOLS, f), 'utf8');
    return /unhandledRejection/.test(s) || ENTRIES.some((re) => re.test(s));
  });

if (candidates.length === 0) {
  console.error('canfail-audit: no gate has an async entry point — the detector is broken');
  process.exit(1);
}

const swallow = [];
let tested = 0;
for (const f of candidates) {
  const p = path.join(TOOLS, f);
  const original = fs.readFileSync(p, 'utf8');
  const re = ENTRIES.find((r) => r.test(original));
  if (!re) continue;
  const m = original.match(re);
  const mutated = original.slice(0, m.index + m[0].length) +
    "\n  throw new Error('CANFAIL-AUDIT-FORCED');" +
    original.slice(m.index + m[0].length);
  fs.writeFileSync(p, mutated);
  let rc = 0;
  try {
    execFileSync(process.execPath, [p], { cwd: ROOT, stdio: 'pipe' });
  } catch (e) {
    rc = e.status == null ? 1 : e.status;
  } finally {
    fs.writeFileSync(p, original);
  }
  tested++;
  if (rc === 0) swallow.push(f);
}

if (swallow.length) {
  console.error('canfail-audit: %d of %d gate(s) CANNOT REPORT FAILURE\n', swallow.length, tested);
  for (const f of swallow) {
    console.error('  - %s exits 0 with a thrown error at its entry point.', f);
  }
  console.error('\n  A gate that cannot fail is worse than no gate, because it is counted.');
  console.error('  The usual cause is a blanket unhandledRejection handler, or an async');
  console.error('  entry called without a .catch. Narrow the handler to let an');
  console.error('  AssertionError through, and give the entry point a .catch that exits 1.');
  process.exit(1);
}
console.log('canfail-audit: %d gate(s) with an async entry point all report a forced failure', tested);
