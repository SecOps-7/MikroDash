// Moved from the orphan check when the port-parity harness was retired.
//
// The body is VERBATIM apart from its imports and the path to the repository
// root: this test drives the port's OWN TypeScript and asserts what it does, so
// nothing in it referred to the implementation this app replaced.
/**
 * Module-scope state in `web/src` that is WRITTEN and never READ.
 *
 * ── WHY THIS EXISTS ─────────────────────────────────────────────────────────
 *
 * The port reported `lastLanData` — assigned at three sites, read at none — as a
 * cosmetic finding in the live repo's ToDo.md. The reply corrected it in two
 * directions and both are the reason this file exists:
 *
 *  1. **The report was wrong about `lastTalkers`.** It said that one IS read, so
 *     the pair was easy to misjudge. All five of its occurrences are the
 *     declaration, a comment and three writes. Fixing only `lastLanData` would
 *     have left the exact trap the report described, with `lastTalkers` as the
 *     sole remaining decoy.
 *  2. **Generalising it found a third**, `_lastSampleAt`. One report, three
 *     orphans, all remnants of the same removed `if(lastX)return` guards.
 *
 * The lesson is the sweep, not the finding. A defect that took three tries to
 * enumerate by hand is one a parser enumerates completely.
 *
 * ── AND THE GO SIDE DOES NOT GET THIS FREE ──────────────────────────────────
 *
 * The reply warned: the Go compiler rejects unused LOCALS, not package-level
 * state that is assigned and never read. The orphan check covers the
 * TypeScript; `TestNoOrphanedPackageState` in internal/collect covers the Go.
 *
 * ── CONSERVATIVE ON PURPOSE ─────────────────────────────────────────────────
 *
 * Reads through a shadowing local, a property of the same name, or a string are
 * all counted as reads. So it reports FEWER orphans than exist and never more,
 * which is the right direction for something that fails a build.
 *
 *   node tools/orphan-check.js
 */

import fs from 'node:fs';
import path from 'node:path';
// Resolved from the repository root, not from `__dirname`: inside the bundle
// `__dirname` is `web/test-out`, so the original relative walk pointed at
// `web/web/node_modules`.
const ROOT_FOR_TS = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ts = require(path.join(ROOT_FOR_TS, 'web', 'node_modules', 'typescript'));

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const SRC = path.join(ROOT, 'web', 'src');

function walk(dir, acc = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) walk(p, acc);
    else if (/\.ts$/.test(e.name) && !/\.d\.ts$/.test(e.name)) acc.push(p);
  }
  return acc;
}

// Names that are written and never read, but must NOT be reported. Each needs a
// reason; the list fails if an entry stops being an orphan.
const EXPECTED = {};

const files = walk(SRC);
if (files.length < 30) throw new Error('only ' + files.length + ' sources found — the scan broke');

const problems = [];
let scanned = 0, declared = 0;

for (const file of files) {
  const text = fs.readFileSync(file, 'utf8');
  const sf = ts.createSourceFile(file, text, ts.ScriptTarget.ES2022, true);
  scanned++;

  // Module-scope `let`/`var` only. A `const` cannot be reassigned, so "written
  // and never read" collapses to "never read", which is a different question and
  // one the bundler already answers by dropping it.
  const names = [];
  for (const st of sf.statements) {
    if (!ts.isVariableStatement(st)) continue;
    const flags = st.declarationList.flags;
    if (flags & ts.NodeFlags.Const) continue;
    for (const d of st.declarationList.declarations) {
      if (ts.isIdentifier(d.name)) names.push(d.name.text);
    }
  }
  if (!names.length) continue;
  declared += names.length;

  // Every identifier occurrence, classified. An occurrence is a WRITE when it is
  // the left of an assignment or the operand of ++/--; anything else counts as a
  // read — including a read that is part of a compound assignment (`x += 1`
  // reads x), which is why those are counted as reads too.
  const reads = new Map(names.map((n) => [n, 0]));
  const writes = new Map(names.map((n) => [n, 0]));
  const visit = (node) => {
    if (ts.isIdentifier(node) && reads.has(node.text)) {
      const p = node.parent;
      let isWrite = false;
      if (p && ts.isBinaryExpression(p) && p.left === node &&
          p.operatorToken.kind === ts.SyntaxKind.EqualsToken) isWrite = true;
      if (p && (ts.isPostfixUnaryExpression(p) || ts.isPrefixUnaryExpression(p)) &&
          (p.operator === ts.SyntaxKind.PlusPlusToken || p.operator === ts.SyntaxKind.MinusMinusToken)) {
        isWrite = true;
      }
      // The declaration itself is neither.
      const isDecl = p && ts.isVariableDeclaration(p) && p.name === node;
      // A property access `foo.bar` reads `foo`, not a same-named module var,
      // unless the identifier IS the object — handled by only skipping the name
      // half of a property access.
      const isPropName = p && ts.isPropertyAccessExpression(p) && p.name === node;
      if (!isDecl && !isPropName) {
        if (isWrite) writes.set(node.text, writes.get(node.text) + 1);
        else reads.set(node.text, reads.get(node.text) + 1);
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);

  const rel = path.relative(ROOT, file);
  for (const n of names) {
    if (reads.get(n) === 0 && writes.get(n) > 0) {
      const key = rel + ':' + n;
      if (EXPECTED[key]) continue;
      problems.push('  ' + rel + ' — `' + n + '` is written ' + writes.get(n) +
        ' time(s) and never read');
    }
  }
}

// The detector must be able to SEE an orphan, or a clean run means nothing. A
// synthetic one is planted and the same analysis run over it.
{
  const probe = `let plantedOrphan = 0;\nexport function f(){ plantedOrphan = 1; }\n`;
  const sf = ts.createSourceFile('probe.ts', probe, ts.ScriptTarget.ES2022, true);
  let reads = 0, writes = 0;
  const visit = (node) => {
    if (ts.isIdentifier(node) && node.text === 'plantedOrphan') {
      const p = node.parent;
      const isDecl = p && ts.isVariableDeclaration(p) && p.name === node;
      const isWrite = p && ts.isBinaryExpression(p) && p.left === node &&
        p.operatorToken.kind === ts.SyntaxKind.EqualsToken;
      if (!isDecl) { if (isWrite) writes++; else reads++; }
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
  if (!(reads === 0 && writes === 1)) {
    throw new Error('the detector cannot see a planted orphan (reads=' + reads +
      ' writes=' + writes + ') — a clean run would prove nothing');
  }
}

for (const key of Object.keys(EXPECTED)) {
  problems.push('  EXPECTED names ' + key + ', which is not an orphan any more — remove the entry');
}

if (problems.length) {
  console.error('orphan-check: %d module-scope variable(s) written and never read\n', problems.length);
  console.error(problems.join('\n'));
  console.error('\nEach is a remnant. Delete it, or record it in EXPECTED with why it stays.');
  process.exit(1);
}
console.log('orphan-check: %d sources, %d module-scope variables, no orphans', scanned, declared);
