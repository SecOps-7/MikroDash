'use strict';
/**
 * A TypeScript module nothing imports.
 *
 * ---- WHY MODULES AND NOT JUST FUNCTIONS ------------------------------------
 *
 * `tools/orphaned-consumer-audit.js` catches a differentially-gated FUNCTION
 * with no caller. It cannot catch a whole MODULE that nothing imports: its
 * functions call each other, so every one of them has a caller, and the gate
 * that drives them passes. `web/src/pages/settings.ts` looked exactly like that
 * for a while — 961 lines, gated, and reachable only through its own internals.
 *
 * This walks the import graph from `main.ts`, which is what `build.mjs` bundles,
 * and reports anything the walk never reaches.
 *
 * ---- THE RESOLVER MUST NOT UNDER-RESOLVE -----------------------------------
 *
 * Written first without handling `.js` specifiers — this codebase imports
 * `'./gen/appearance-tables.js'`, naming the OUTPUT — every such edge was
 * dropped and the audit reported three generated tables as dead code. They are
 * imported by four modules.
 *
 * A resolver that silently fails produces false positives, and a false positive
 * is how an audit gets ignored. So an unresolvable RELATIVE import is a hard
 * error here: better to stop than to report a module as unreachable because the
 * edge to it could not be read.
 *
 *   node tools/module-reachability-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');
const SRC = path.join(ROOT, 'web', 'src');

/** Unreachable ON PURPOSE, with what would close it. */
const KNOWN_UNREACHABLE = {
  // `pages/router-form.ts` and `pages/router-modal.ts` were both here. Closed
  // 2026-08-28: `pages/settings-routers.ts` renders the per-row Edit button the
  // live app opens the dialog from, and `main.ts` calls `initRouterModal`. The
  // form was never separately unreachable — it is imported only by the modal, so
  // the two were always going to close together.
  // `pages/setup-overlay-wire` WAS recorded here as deliberately inert, because
  // its Connect button called `POST /api/routers/{id}/activate` and that route
  // was a 404. Closed 2026-08-29: the route is ported
  // (`internal/server/routers_activate.go`) and `main.ts` mounts the overlay.
  // The entry said "DELETE THIS ENTRY when that route lands", and it landed.
    // `pages/setup-overlay` — the overlay's PURE half — was recorded here too,
  // and went with the wire module on 2026-08-29: it is imported by
  // `setup-overlay-wire.ts`, which `main.ts` now mounts. It was never
  // separately unreachable; the two were always going to close together.
};

// ---- Read every module -----------------------------------------------------
const files = [];
(function walk(dir) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) walk(p);
    else if (e.name.endsWith('.ts')) files.push(p);
  }
})(SRC);

const src = new Map(files.map((f) => [f, fs.readFileSync(f, 'utf8')]));
const rel = (f) => path.relative(SRC, f).split(path.sep).join('/');

// ── A MODULE OF NOTHING BUT TYPES COMPILES TO ZERO BYTES ───────────────────
//
// So "is it in the bundle" has no answer for one, and reporting it as dead code
// is a false positive — which the header above already names as the thing that
// gets an audit ignored. Excluded from the QUESTION rather than listed in
// KNOWN_UNREACHABLE, because an entry saying "types erase" would be true of
// every such file forever and would have to be written again for each new one.
//
// DETECTED, not enumerated. BOTH halves are required — it must export types and
// export no values — so a file exporting nothing at all stays in the question
// rather than being excused, and one that later grows a `const` rejoins it
// automatically.
function isTypeOnly(text) {
  const declaresTypes = /^\s*export\s+(?:type|interface)\b/m.test(text);
  const declaresValues =
    /^\s*export\s+(?:async\s+)?(?:function|const|let|var|class|enum|default)\b/m.test(text)
    || /^\s*export\s*\{/m.test(text);
  return declaresTypes && !declaresValues;
}

function resolve(from, spec) {
  if (!spec.startsWith('.')) return null;            // a package, not ours
  const bare = spec.endsWith('.js') ? spec.slice(0, -3) : spec;
  const p = path.normalize(path.join(path.dirname(from), bare));
  for (const cand of [p + '.ts', path.join(p, 'index.ts')]) {
    if (src.has(cand)) return cand;
  }
  return null;
}

// ── A TYPE-ONLY IMPORT IS NOT AN EDGE ──────────────────────────────────────
//
// `import type { X } from './y'` compiles to NOTHING — esbuild erases it, and no
// byte of `y` reaches the bundle. Counting it as an edge makes this audit answer
// a different question from the one it asks, and the failure is the reassuring
// kind: a module reads as SHIPPED because somebody imported one of its
// interfaces. `main.ts` gained exactly such an import of `pages/router-form` on
// 2026-08-28 and both reachability audits immediately called that dialog live.
// It is not; nothing it does is in the bundle.
//
// Stripped whole-statement rather than filtered at the match, because a wrapped
// type import puts the `type` keyword and the specifier on different lines.
//
// The INLINE spelling — `import { type A, b } from './x'` — is deliberately NOT
// stripped: `b` is a value, so the module IS in the bundle.
function runtimeText(text) {
  return text
    .replace(/^\s*import\s+type\s[\s\S]*?from\s*'[^']+';/gm, '')
    .replace(/^\s*export\s+type\s[\s\S]*?from\s*'[^']+';/gm, '');
}

// DYNAMIC IMPORTS COUNT. `import('./x')` puts `./x` in the bundle exactly as a
// static import does — esbuild inlines it into the single output file — but it
// has no `from` clause, so a `from '...'` match alone cannot see it. On
// 2026-08-29 that made `pages/routers-map.ts` read as "imported by nothing
// reachable" while its code was demonstrably in `dist/app.js`, and the honest
// fix is here rather than in the module: an audit that cannot see `import()`
// mis-reports every lazily-loaded module, not just that one.
const edges = new Map(files.map((f) => [f, new Set()]));
for (const [f, text] of src) {
  const runtime = runtimeText(text);
  const specs = [
    ...[...runtime.matchAll(/from\s+'([^']+)'/g)].map((m) => m[1]),
    ...[...runtime.matchAll(/\bimport\s*\(\s*'([^']+)'\s*\)/g)].map((m) => m[1]),
  ];
  for (const spec of specs) {
    const r = resolve(f, spec);
    if (r) { edges.get(f).add(r); continue; }
    if (spec.startsWith('.')) {
      throw new Error(`${rel(f)} imports '${spec}', which resolves to nothing. An unresolvable `
        + 'relative import makes its target look unreachable — see the header. Fix the resolver '
        + 'or the import before trusting this audit.');
    }
  }
}

// ---- Walk from EVERY bundle's entry point ----------------------------------
//
// This walked from `main.ts` alone, which was right while the app was the only
// document. It is not: `login.html` and the `<head>` of `index.html` have their
// own bundles (`web/src/entry/README.md`), and an entry point is imported by
// nothing — so both were reported as dead code the moment they arrived.
//
// The list is READ out of `build.mjs`, not typed here. A typed copy goes stale,
// and a stale copy of this particular list reports live code as dead.
// `web/build.mjs` became `cmd/webbuild` on 2026-08-31 when the build stopped
// needing Node. Same declarations, same job — this follows them there. The
// emptiness guard below is what makes that safe: a regex that stopped matching
// fails loudly instead of reporting live code as dead.
const buildMjs = fs.readFileSync(path.join(ROOT, 'cmd', 'webbuild', 'main.go'), 'utf8');
const entries = [path.join(SRC, 'main.ts')];
for (const m of buildMjs.matchAll(/\{"([^"]+\.ts)",/g)) {
  entries.push(path.join(SRC, ...m[1].split('/')));
}
if (entries.length < 2) {
  throw new Error('read no classic entry points out of cmd/webbuild. Either the CLASSIC list '
    + 'changed shape or it is gone — find out which, because guessing here reports live code as '
    + 'dead, which is how an audit gets ignored (see the header).');
}
for (const e of entries) {
  if (!src.has(e)) {
    throw new Error(`${rel(e)} is a build entry point and does not exist. build.mjs and web/src `
      + 'disagree about what is being bundled.');
  }
}
const seen = new Set(entries);
const stack = [...entries];
while (stack.length) {
  for (const e of edges.get(stack.pop())) if (!seen.has(e)) { seen.add(e); stack.push(e); }
}

const unreachable = files
  .filter((f) => !seen.has(f) && !isTypeOnly(src.get(f)))
  .map(rel).sort();
const problems = [];
for (const u of unreachable) {
  if (!KNOWN_UNREACHABLE[u]) {
    problems.push(`${u} is imported by nothing reachable from any build entry point. It is not in the bundle, `
      + 'so nothing it contains runs — whatever gate covers it is testing code the app cannot '
      + 'reach. Wire it, delete it, or record it in KNOWN_UNREACHABLE with what would close it.');
  }
}
// The ledger's other direction.
for (const k of Object.keys(KNOWN_UNREACHABLE)) {
  if (!unreachable.includes(k)) {
    problems.push(`${k} is in KNOWN_UNREACHABLE but IS reachable now — delete the entry.`);
  }
}
// And the walk must have actually walked.
if (seen.size < files.length / 2) {
  problems.push(`the walk reached only ${seen.size} of ${files.length} modules; the import `
    + 'pattern has stopped matching and everything downstream of this is noise');
}

if (problems.length) {
  console.error('module-reachability-audit FAILED:');
  for (const p of problems) console.error('  - ' + p);
  process.exit(1);
}
console.log(`module-reachability-audit: ${seen.size} of ${files.length} modules reachable from the ${entries.length} build entry point(s), rooted at `
  + `main.ts; ${unreachable.length} unreachable and all recorded`);
