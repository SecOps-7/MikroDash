#!/usr/bin/env node
'use strict';
/**
 * MODULES NOTHING CAN REACH.
 *
 * ── THE GAP THIS CLOSES, WHICH I OPENED MYSELF ──────────────────────────────
 *
 * On 2026-08-25 the router Add/Edit dialog was wired: `router-modal.ts` binds
 * all 32 of its ids, and `wiring-audit` duly reported the group closed — 88
 * known gaps down to 55. **Nothing imported the module.** The dialog could not
 * be opened, and every other audit here read as green, because each one asks a
 * question this does not answer:
 *
 *   wiring-audit        does the port TOUCH this id?          yes, in a dead file
 *   attr-audit          is this attribute READ?               yes, in a dead file
 *   class-hook-audit    does the stylesheet answer this?      yes
 *   page-gate-audit     does a GATE drive this module?        that is about tests
 *   element-coverage    does a gate cover these elements?     also about tests
 *
 * Every one of them was satisfied by code that cannot run. "Bound" and
 * "reachable" are different questions, and only this one asks the second.
 *
 * ── REACHABLE MEANS: IMPORTED FROM `main.ts`, TRANSITIVELY ──────────────────
 *
 * `main.ts` is the bundle's only entry point (`web/build.mjs`), so a module
 * outside its import closure is not in the shipped bundle at all.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/reachable-audit.js
 */

const fs = require('node:fs');
const path = require('node:path');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const SRC = path.join(ROOT, 'web', 'src');

function walk(dir, out = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) walk(p, out);
    else if (e.name.endsWith('.ts')) out.push(p);
  }
  return out;
}

// ── A MODULE OF NOTHING BUT TYPES IS NEVER IN A BUNDLE ────────────────────
//
// It compiles to zero bytes, so asking whether it is "reachable at runtime" has
// no answer. These are excluded from the question rather than recorded as
// expected gaps: an EXPECTED entry saying "types erase" would be true of every
// such file forever and would have to be added by hand to each new one, which is
// the leave-it-to-be-remembered arrangement this repo keeps getting bitten by.
//
// DETECTED, not listed: a file with no runtime export at all. If one of these
// ever grows a `const` or a `function` it rejoins the audit automatically, and
// an unwired one then fails.
function isTypeOnly(mod) {
  // NEVER the entry point. `main.ts` exports nothing at all — it is a script —
  // so a rule phrased only as "no runtime export" swallowed it, `visit('main')`
  // returned immediately, and the audit reported ninety-six modules as
  // unreachable from a module it had itself excluded. It said so loudly, which
  // is the only reason this took one run to find.
  if (mod === 'main' || mod.startsWith('entry/')) return false;
  const body = fs.readFileSync(path.join(SRC, mod + '.ts'), 'utf8');
  // BOTH HALVES ARE REQUIRED: it must export types AND export no values. A file
  // that exports nothing whatsoever is not a type module, it is a script or a
  // mistake, and either way it should stay in the question.
  const declaresTypes = /^\s*export\s+(?:type|interface)\b/m.test(body);
  const declaresValues = /^\s*export\s+(?:async\s+)?(?:function|const|let|var|class|enum|default)\b/m.test(body)
    || /^\s*export\s*\{/m.test(body);
  return declaresTypes && !declaresValues;
}

const all = walk(SRC).map((p) => path.relative(SRC, p).replace(/\.ts$/, '').split(path.sep).join('/'))
  .filter((m) => !isTypeOnly(m));

/** Which modules a file imports, resolved to the same relative form. */
function importsOf(mod) {
  const body = fs.readFileSync(path.join(SRC, mod + '.ts'), 'utf8');
  const out = new Set();
  // ── TYPE-ONLY IMPORTS ARE ERASED, SO THEY ARE NOT REACHABILITY ──────────
  //
  // `import type { X } from './y'` compiles to NOTHING: esbuild drops it and no
  // byte of `y` reaches the bundle. Counting it would make this audit answer a
  // different question from the one its name asks, and the way that surfaced is
  // instructive — `main.ts` gained one type import from `pages/router-form` and
  // the ledger immediately reported the module as shipped. It is not; nothing
  // that page's dialog does is in the bundle.
  //
  // Stripped rather than skipped at the match, because `import type {A}, then
  // from './x'` puts the spelling and the specifier on different lines when the
  // list is long enough to wrap.
  const runtime = body
    .replace(/^\s*import\s+type\s[\s\S]*?from\s*'[^']+';/gm, '')
    .replace(/^\s*export\s+type\s[\s\S]*?from\s*'[^']+';/gm, '');
  // `import … from './x'`, `export … from './x'`, and the INLINE type-only
  // spelling (`import { type A, b } from './x'`), which is NOT erased — `b` is a
  // value and the module is in the bundle.
  for (const m of runtime.matchAll(/(?:from|import)\s*\(?\s*'(\.[^']+)'/g)) {
    const rel = m[1].replace(/\.js$/, '');
    const abs = path.normalize(path.join(path.dirname(mod), rel)).split(path.sep).join('/');
    out.add(abs);
  }
  return [...out];
}

// ── EVERY BUILD ENTRY POINT IS A ROOT, AND THE LIST COMES FROM build.mjs ──
//
// This walked from `main.ts` alone, which was right while the app was the only
// document. It is not: `login.html` and the `<head>` of `index.html` have their
// own bundles (see `web/src/entry/README.md`), and an entry point is imported by
// nothing — so both were reported as dead code the moment they arrived.
//
// READ, not listed. A typed copy of the entry list is a copy that goes stale,
// which is the failure this repo keeps finding; if `build.mjs` gains a fourth
// bundle it becomes a root here without anybody remembering.
// `web/build.mjs` became `cmd/webbuild` on 2026-08-31 when the build stopped
// needing Node. Same declarations, same job — this follows them there. The
// emptiness guard below is what makes that safe: a regex that stopped matching
// fails loudly instead of reporting live code as dead.
const buildMjs = fs.readFileSync(path.join(ROOT, 'cmd', 'webbuild', 'main.go'), 'utf8');
const ROOTS = ['main'];
for (const m of buildMjs.matchAll(/\{"([^"]+)\.ts",/g)) ROOTS.push(m[1]);
if (ROOTS.length < 2) {
  throw new Error('read no classic entry points out of cmd/webbuild. Either the CLASSIC list changed '
    + 'shape or it is gone — find out which, because guessing here reports live code as dead.');
}

const reached = new Set();
function visit(mod) {
  if (reached.has(mod) || !all.includes(mod)) return;
  reached.add(mod);
  for (const dep of importsOf(mod)) visit(dep);
}
for (const r of ROOTS) visit(r);

// module -> why it is unreachable and that being so is correct.
const EXPECTED = {
  // ── PAGES NOT YET IN `main.ts`'s PORTED SET ─────────────────────────────
  //
  // The strangler rule is that a page and its endpoints cut over together, so a
  // page that is finished but not shipped is a normal state, not a defect. Each
  // is blocked on something recorded during the port, and each entry names it —
  // an unreachable module with no reason is the thing this audit is for.
  // `pages/backups` and `pages/bandwidth` WERE here and are now SHIPPED
  // (2026-08-25). Backups waited on `backups:restore`, whose base-URL question
  // the operator answered — Go owns `/api/backups/:id/raw` outright — and the
  // handler landed with it. Bandwidth was complete and unshipped only for want
  // of live verification, which is now a queue item covering every page rather
  // than a gate on this one.
  // `pages/routers`, `pages/settings` and `gen/settings-form-map` were all
  // recorded here. Closed 2026-08-26, when `pages/settings-sites` was mounted:
  // it imports the Sites card's renderers from `pages/settings`, which imports
  // `siteIdsOf` from `pages/routers`, which pulls in the generated form map.
  //
  // REACHABILITY IS NOT COMPLETION, and the distinction matters here more than
  // usual. The Routers PAGE is still unwired and still blocked on the
  // background-sessions decision; the settings WRITE path is still blocked on
  // `src/settings.js` caching its object. Both reasons are recorded in
  // the port record, which is where they belong — this audit answers "is a
  // module in the bundle", and all three now are. Deleting the entries rather
  // than rewording them, because a reason kept past its question is how this
  // file's own `pages/routers` entry stayed wrong for weeks.
  //
  // Checked before mounting: `pages/routers.ts` has no import-time side
  // effects, only declarations, so pulling it in for one helper runs nothing.

  // ── THE ROUTER DIALOG'S CHAIN ───────────────────────────────────────────
  // `pages/router-form` WAS here — "the dialog's pure half … imported only by
  // pages/router-modal, so it shares its reachability". Closed 2026-08-28 with
  // `pages/router-modal` below, and for the same reason.

  // box. Same chain." Closed 2026-08-26: the SITE FORM now mounts it too, through
  // the shared `mountCityPicker`, so it no longer reaches the bundle only via the
  // router dialog. `pages/router-modal` below is still unreachable and its own
  // entry still holds; the two stopped sharing a fate the moment there was a
  // second caller.
  // `pages/router-modal` WAS here, recorded as "complete, gated and deliberately
  // unreachable" because its openers live on the Settings page. The entry ended
  // with "Delete this entry the moment Settings can call it", and that moment is
  // 2026-08-28: `pages/settings-routers.ts` renders the per-row Edit button and
  // calls `initRouterModal`, and `main.ts` mounts both.
  //
  // Its own warning is worth keeping in mind rather than copying forward: it
  // said `wiring-audit` seeing 32 bound ids "is true and says nothing about
  // whether anyone can open it". Reachable now means the module is in the
  // bundle and has a caller. The Settings PAGE still does not mount (LOOP 1h),
  // so nobody can click that Edit button yet — this ledger asks whether code is
  // reachable, and that is a different question from whether a user can get to
  // it. Two different audits, and neither answers the other's question.

  // ── THE FIRST-RUN ROUTER OVERLAY ────────────────────────────────────────
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

const dead = all.filter((m) => !reached.has(m)).sort();
const problems = [];
for (const m of dead) {
  if (!EXPECTED[m]) {
    problems.push(m + ' is not reachable from any build entry point — nothing imports it, so it is not in the '
      + 'bundle. Wire it, delete it, or record why it is deliberately inert.');
  }
}
for (const m of Object.keys(EXPECTED)) {
  if (!dead.includes(m)) {
    problems.push(m + ' is recorded as unreachable and IS reachable now — delete the entry.');
  }
}

say(`reachable-audit: ${reached.size} of ${all.length} modules reachable from the build entry points; `
  + `${dead.length} unreachable (${Object.keys(EXPECTED).length} recorded)`);
if (problems.length) {
  shout('');
  for (const p of problems) shout('  ✗ ' + p);
  process.exit(1);
}
say('every module is reachable or recorded');
