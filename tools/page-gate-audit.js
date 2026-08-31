'use strict';
/**
 * WHICH PORTED PAGE MODULES ARE ACTUALLY EXERCISED BY A GATE.
 *
 * ── WHY IT EXISTS ───────────────────────────────────────────────────────────
 *
 * `tools/live-renderer.js <page>` LOOKS like the acceptance test for a ported
 * page — CLAUDE.md described it that way — but it only lifts and validates the
 * LIVE renderer and writes `web/dist/_compare/live-<page>.js`. Nothing is
 * compared unless a separate gate consumes that bundle, and for most pages none
 * does. It prints a reassuring "wrote … (522 renderer lines)" and exits 0.
 *
 * That was found by mutating a shared helper and watching the tool report
 * success. This audit is so it cannot be found that way twice.
 *
 * ── THE MEASUREMENT IS EASY TO GET WRONG IN BOTH DIRECTIONS ────────────────
 *
 * Three attempts disagreed before this one, and the disagreements were entirely
 * in how the seed set was built, never in the graph:
 *
 *   - TOO STRICT: matching only `pages/<name>.js` in an import string missed
 *     the gates that bundle via `path.join(…, 'pages', 'routers.ts')`, which
 *     reported `routers` and `settings` as ungated when both are gated.
 *   - TOO GENEROUS: a looser regex picked up `src/main.ts` and `src/index.ts`
 *     as seeds. Those import EVERY page, so the closure swallowed the whole
 *     tree and reported 66 of 68 covered. Being bundled is not being exercised;
 *     that run was measuring nothing at all.
 *
 * So entry points are excluded by name, both spellings of a gate entry are
 * matched, and the closure runs over the real import graph.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/page-gate-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const SRC = path.join(ROOT, 'web', 'src');

// Whole-app entry points. They import every page, so seeding the closure with
// one makes the audit vacuous — see the header.
const ENTRY_POINTS = new Set(['main', 'index']);

function walk(dir, out) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) walk(p, out);
    else if (e.name.endsWith('.ts')) out.push(p);
  }
  return out;
}

// ── the import graph ────────────────────────────────────────────────────────
const imports = new Map();
for (const file of walk(SRC, [])) {
  const mod = path.relative(SRC, file).replace(/\.ts$/, '').split(path.sep).join('/');
  const deps = new Set();
  const body = fs.readFileSync(file, 'utf8');
  for (const m of body.matchAll(/from\s+'(\.[^']+)'/g)) {
    const rel = path.posix.normalize(path.posix.join(path.posix.dirname(mod), m[1]))
      .replace(/\.js$/, '');
    deps.add(rel);
  }
  imports.set(mod, deps);
}

// ── the seed: modules a gate builds directly ────────────────────────────────
const gatedBy = new Map();

/**
 * Modules a gate REFERENCES but does not gate.
 *
 * The matcher below infers "this gate builds that module" from a reference to
 * its path, which is right almost always and wrong in one shape: a gate that
 * READS another module's source to check a WIRING CONTRACT — that the page hands
 * the right element id to a function this gate drives with synthetic nodes.
 *
 * That happened on 2026-08-25. `map-fs-check` gained a check that
 * `connections.ts` passes `#mapFullscreenBtn` as `btn:` and not as `close:`; it
 * killed three mutations, and it made this audit report `pages/connections` as
 * gated when its `conn:update` wiring — twelve functions, most of them SVG — is
 * not. The check was reverted rather than leave this audit asserting something
 * false, and the seam was recorded instead.
 *
 * A gate can now say so itself. `--not-gates` prints the module paths it only
 * reads, and this audit does not credit them to it. The declaration is CHECKED
 * IN BOTH DIRECTIONS below: naming a module the gate does not reference is an
 * entry that has outlived its reason, and this audit fails on it.
 */
const disclaimed = new Map();
for (const f of fs.readdirSync(path.join(ROOT, 'tools'))) {
  if (!f.endsWith('.js') || f === path.basename(__filename)) continue;
  const body = fs.readFileSync(path.join(ROOT, 'tools', f), 'utf8');
  if (!body.includes("process.argv.includes('--not-gates')")) continue;
  const r = spawnSync(process.execPath, [path.join(ROOT, 'tools', f), '--not-gates'],
    { encoding: 'utf8', env: process.env });
  if (r.status !== 0 || !r.stdout.trim().startsWith('[')) {
    throw new Error(f + ' declares --not-gates but did not answer with a list: ' +
      (r.stderr || r.stdout).slice(0, 200));
  }
  disclaimed.set(f, new Set(JSON.parse(r.stdout)));
}

for (const f of fs.readdirSync(path.join(ROOT, 'tools'))) {
  if (!f.endsWith('.js') || f === path.basename(__filename)) continue;
  const s = fs.readFileSync(path.join(ROOT, 'tools', f), 'utf8');
  const skip = disclaimed.get(f) || new Set();
  const add = (name) => {
    if (skip.has(name) || skip.has('pages/' + name)) return;
    if (!name) return;
    const leaf = name.split('/').pop();
    if (ENTRY_POINTS.has(leaf)) return;
    const full = imports.has(name) ? name
      : imports.has('pages/' + name) ? 'pages/' + name : null;
    if (full && !gatedBy.has(full)) gatedBy.set(full, f);
  };
  // `from '../web/src/pages/dns.js'` and `require`-style string references
  for (const m of s.matchAll(/(?:web\/)?src\/((?:pages\/)?[A-Za-z0-9_/-]+)\.(?:js|ts)'/g)) add(m[1]);
  // AND THE EXTENSION-LESS SPELLING, which is how a gate that writes a temporary
  // esbuild entry references its module: `from '../web/src/pages/upgrade'`.
  // Widening this was measured first — three references in the whole tools
  // directory use it, and two of their modules were already reached by other
  // gates, so the only module this newly recognises is one that really is gated.
  // A matcher that misses a real spelling reports a gated module as ungated,
  // which is the direction that gets an audit ignored.
  for (const m of s.matchAll(/(?:web\/)?src\/((?:pages\/)?[A-Za-z0-9_/-]+)'/g)) add(m[1]);
  // `path.join(ROOT, 'web', 'src', 'pages', 'routers.ts')`
  for (const m of s.matchAll(/'(?:pages|src)',\s*'([A-Za-z0-9_-]+)\.ts'/g)) add(m[1]);
}

// ── transitive closure ──────────────────────────────────────────────────────
const reached = new Set(gatedBy.keys());
const stack = [...gatedBy.keys()];
while (stack.length) {
  for (const d of imports.get(stack.pop()) || []) {
    if (!reached.has(d)) { reached.add(d); stack.push(d); }
  }
}

// ── the ledger ──────────────────────────────────────────────────────────────
// A module with NO runtime exports has nothing a gate could drive. Asserted
// rather than trusted: if one of these grows a function, the audit says so.
const TYPES_ONLY = ['pages/routing-types', 'pages/topology-types'];

// Ported before the Dashboard work began, when the port had no DOM-equality
// harness. Every one is a real gap, recorded so the number cannot quietly grow.
// Closing one means DELETING its line here, which is why the audit also fails
// when a listed module becomes gated.
// NOTE ON WHAT "EXERCISED" MEANS HERE. This audit answers one question: does a
// gate DRIVE this module. It does not measure how much of it. `topology` left
// this list when `topology-layout-check.js` landed, and that gate covers the
// LAYOUT ARITHMETIC only — the SVG construction, the drag handlers and the
// animation need a browser. `element-coverage-audit.js` is the finer measure,
// but it counts `el('id')` lookups and topology has none: it builds its nodes.
// So for that page the honest record is in PORT-QUEUE.md and in the gate's own
// header, not in a number here.
const UNGATED = new Set([
  // `pages/routers-map` was ADDED here on 2026-08-29 and removed the same
  // minute, because this audit answered better than the entry did: it sees that
  // `tools/map-labels-check.js` drives the module and said "now HAS a gate —
  // remove it from the ledger".
  //
  // The reasoning was still half right and is worth keeping: a gate over the SVG
  // construction, the pointer capture and the `getBoundingClientRect` popover
  // placement would be comparing this port's own fakes against itself — the same
  // reason `pages/connections` sat here. What changed the answer is that the
  // PURE piece was split out rather than left inside: `keepLabels` decides which
  // place names to drop when they overlap, takes numbers, returns a subset, and
  // is compared against the live inline block over 13 cases with 11 mutations
  // killed.
  //
  // The lesson is the useful part: "this module is untestable" was true of the
  // module as first written and stopped being true once the decision was moved
  // out of the imperative code. The rest of it is browser-verified.

  // `pages/connections` WAS here, and left on 2026-08-28 when
  // `tools/localcc-timing-check.js` landed. The nuance matters, so it is kept
  // rather than deleted with the entry:
  //
  // THAT GATE COVERS TIMING, NOT THE DOM. The reasoning that kept this module
  // out of a DOM gate still stands — the `conn:update` handler calls twelve
  // functions and `applySourceFilter` seven, most of them SVG map work, so
  // lifting the page faithfully means lifting all of it and stubbing it means
  // comparing this port's own glue. What the new gate pins is WHEN
  // `/api/localcc` is asked for, which is what the operator's missing-arcs
  // report turned out to be: a correct endpoint asked at boot, before the
  // session existed, so `localCC` stayed `ZZ` and no arc was ever drawn.
  //
  // A DOM gate would not have caught that either. The markup was right; the
  // moment was wrong.

  // `pages/router-modal` was recorded here — "a gate would compare my glue" —
  // with the caveat that the line would change "if a DECISION ever moves into
  // this file". What actually changed was the opposite: a decision moved OUT.
  // `tools/router-modal-picker-check.js` was written on 2026-08-26 so the
  // dialog's inline town-picker wiring could be MIGRATED onto the shared
  // `mountCityPicker` with something to check the refactor against. Six
  // mutations killed against the inline version before the move, and the same
  // set against the shared one after it.
  //
  // The judgement above was not wrong: gating glue for its own sake buys little.
  // Gating it to make a refactor safe is a different question, and it earned it.
]);

const pages = [...imports.keys()].filter((m) => m.startsWith('pages/')).sort();
const problems = [];

// ── A DISCLAIMER THAT IS NO LONGER TRUE IS A FAILURE ───────────────────────
//
// Naming a module a gate does not reference means the read it was written for is
// gone, and the entry now hides nothing while looking like it hides something.
for (const [f, names] of disclaimed) {
  const body = fs.readFileSync(path.join(ROOT, 'tools', f), 'utf8');
  for (const name of names) {
    const leaf = name.split('/').pop();
    if (body.includes(leaf + '.ts') || body.includes(leaf + '.js') ||
        body.includes("'" + leaf + "'")) continue;
    problems.push(f + ' disclaims ' + name + ', which it does not reference at all — ' +
      'the read it was written for is gone, so remove the entry');
  }
}


for (const m of TYPES_ONLY) {
  if (!imports.has(m)) { problems.push(m + ' is in TYPES_ONLY but no longer exists'); continue; }
  const body = fs.readFileSync(path.join(SRC, m + '.ts'), 'utf8');
  if (/^export (?:function|const|let|class)/m.test(body)) {
    problems.push(m + ' is listed as types-only but now exports runtime code — it needs a gate');
  }
}

const ungated = pages.filter((m) => !reached.has(m) && !TYPES_ONLY.includes(m));
for (const m of ungated) {
  if (!UNGATED.has(m)) {
    problems.push(m + ' has NO gate driving it and no entry in the ledger — gate it or record why not');
  }
}
for (const m of UNGATED) {
  if (!imports.has(m)) problems.push(m + ' is in the ledger but no longer exists');
  else if (reached.has(m)) problems.push(m + ' now HAS a gate — remove it from the ledger');
}

// A dashboard module appearing here is a regression: every one is gated today,
// and the whole point of the ledger is that the boundary does not creep.
for (const m of ungated) {
  if (m.startsWith('pages/dashboard')) problems.push(m + ' is a DASHBOARD module with no gate');
}

if (problems.length) {
  shout('page-gate-audit: %d problem(s)\n', problems.length);
  for (const p of problems) shout('  - ' + p);
  process.exit(1);
}
say('page-gate-audit: %d of %d page modules exercised by a gate (%d%%); %d ungated and all recorded',
  pages.length - ungated.length - TYPES_ONLY.length, pages.length - TYPES_ONLY.length,
  Math.round(((pages.length - ungated.length - TYPES_ONLY.length) / (pages.length - TYPES_ONLY.length)) * 100),
  ungated.length);
